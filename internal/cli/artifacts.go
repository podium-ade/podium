package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

func newArtifactsCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "artifacts TASK_ID",
		Short: "List the files a task produced",
		Long: "List the files a task produced.\n\n" +
			"Rolled-up log streams appear alongside them with kind \"log\": once a finished\n" +
			"task's chunks have been pruned out of Postgres, that object is its log.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := e.client.artifacts.ListArtifacts(cmd.Context(),
				connect.NewRequest(&podiumv1.ListArtifactsRequest{TaskId: args[0]}))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("list artifacts of %s: %w", args[0], err)}
			}
			return printArtifacts(e, res.Msg.GetArtifacts())
		},
	}
}

func printArtifacts(e *env, list []*podiumv1.Artifact) error {
	w := newTable(e.stdout)
	fmt.Fprintln(w, "ID\tKIND\tNAME\tSIZE\tTYPE\tCREATED")
	for _, a := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			a.GetId(), orDash(a.GetKind()), a.GetName(), humanBytes(a.GetSizeBytes()),
			orDash(a.GetContentType()), ago(a.GetCreatedAt().AsTime()))
	}
	return w.Flush()
}

func newArtifactCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifact",
		Short: "Work with a single artifact",
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newArtifactGetCommand(e))
	return cmd
}

func newArtifactGetCommand(e *env) *cobra.Command {
	var out string
	var viaServer bool
	var printURL bool

	cmd := &cobra.Command{
		Use:   "get ARTIFACT_ID",
		Short: "Download one artifact",
		Long: "Download one artifact.\n\n" +
			"By default the bytes are proxied through the control plane, because that is the\n" +
			"one endpoint a client is guaranteed to be able to reach. --via-server=false asks\n" +
			"for a presigned URL and fetches it straight from the object store instead, which\n" +
			"is faster and needs a route to it; --url prints that URL and downloads nothing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return getArtifact(cmd.Context(), e, args[0], out, viaServer, printURL)
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "write to this file (default: the artifact's name; - for stdout)")
	cmd.Flags().BoolVar(&viaServer, "via-server", true, "proxy the download through the control plane")
	cmd.Flags().BoolVar(&printURL, "url", false, "print a presigned URL instead of downloading")
	return cmd
}

func getArtifact(ctx context.Context, e *env, artifactID, out string, viaServer, printURL bool) error {
	source := strings.TrimSuffix(e.cfg.Server, "/") + "/artifacts/" + artifactID
	name := artifactID

	// A presigned URL is only minted when it is going to be used: the default path
	// proxies the bytes through the control plane and learns the file name from the
	// response, so it never has to ask the object store for a signature at all.
	if printURL || !viaServer {
		res, err := e.client.artifacts.GetArtifactURL(ctx,
			connect.NewRequest(&podiumv1.GetArtifactURLRequest{ArtifactId: artifactID}))
		if err != nil {
			return &ExitError{Code: ExitInfra, Err: fmt.Errorf("get artifact %s: %w", artifactID, err)}
		}
		if printURL {
			_, perr := fmt.Fprintln(e.stdout, res.Msg.GetUrl())
			return perr
		}
		source = res.Msg.GetUrl()
		if n := res.Msg.GetArtifact().GetName(); n != "" {
			name = n
		}
	}

	body, filename, err := e.fetch(ctx, source, viaServer)
	if err != nil {
		return &ExitError{Code: ExitInfra, Err: fmt.Errorf("download artifact %s: %w", artifactID, err)}
	}
	defer func() { _ = body.Close() }()
	if viaServer && filename != "" {
		name = filename
	}

	dst, closeDst, err := artifactSink(e, out, name)
	if err != nil {
		return &ExitError{Code: ExitUsage, Err: err}
	}
	defer closeDst()

	n, err := io.Copy(dst, body)
	if err != nil {
		return &ExitError{Code: ExitInfra, Err: fmt.Errorf("write artifact %s: %w", artifactID, err)}
	}
	if out != "-" {
		e.note("wrote %s (%s)", artifactSinkName(out, name), humanBytes(n))
	}
	return nil
}

// fetch opens the artifact's bytes. A presigned URL is fetched with no credential at all —
// the signature is the credential, and sending a bearer token to the object store would be
// leaking one to a third party.
func (e *env) fetch(ctx context.Context, source string, authenticated bool) (io.ReadCloser, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, "", err
	}
	client := http.DefaultClient
	if authenticated {
		client = httpClientFor(e.cfg)
		if e.cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+e.cfg.Token)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	return resp.Body, dispositionFilename(resp.Header.Get("Content-Disposition")), nil
}

// dispositionFilename picks the file name out of a Content-Disposition header, ignoring
// anything it cannot parse: the fallback is the artifact ID, which always works.
func dispositionFilename(header string) string {
	if header == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return filepath.Base(params["filename"])
}

// artifactSink resolves -o. An empty value writes the artifact's own name into the working
// directory; "-" writes to stdout.
func artifactSink(e *env, out, name string) (io.Writer, func(), error) {
	if out == "-" {
		return e.stdout, func() {}, nil
	}
	path := out
	if path == "" {
		path = filepath.Base(name)
	}
	if path == "" || path == "." || path == string(filepath.Separator) {
		return nil, nil, errors.New("artifact has no usable file name; pass -o")
	}
	f, err := os.Create(path) //nolint:gosec // the operator named the destination
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

func artifactSinkName(out, name string) string {
	if out != "" {
		return out
	}
	return filepath.Base(name)
}

// humanBytes is the one size format the CLI uses.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
