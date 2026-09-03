package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

func newSecretCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage the secrets tasks can reference",
		Long: "Manage the secrets tasks can reference.\n\n" +
			"A value goes in once and never comes back out: there is no `secret get`. The\n" +
			"only way to see a value again is to run a task that references it, and even\n" +
			"then the node redacts it from the task's logs on the way out.",
		// NoArgs makes `podium secret get NAME` an error rather than a help screen and a
		// zero exit status. There is no read endpoint, and a typo must say so.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newSecretSetCommand(e), newSecretLsCommand(e), newSecretRmCommand(e))
	return cmd
}

func newSecretSetCommand(e *env) *cobra.Command {
	var value, fromFile string

	cmd := &cobra.Command{
		Use:   "set NAME",
		Short: "Create or replace a secret",
		Long: "Create or replace a secret.\n\n" +
			"The value comes from --value, from --from-file, or from stdin. Stdin is the\n" +
			"one to prefer: --value puts the secret in your shell history and in the\n" +
			"process table, and this command says so every time you use it.\n\n" +
			"A trailing newline is stripped from stdin, because typing or echoing a value\n" +
			"adds one and almost nobody wants it. --from-file and --value are taken\n" +
			"byte for byte, so a binary key or a PEM file survives intact.\n\n" +
			"Setting an existing name replaces the value and bumps its version. Tasks\n" +
			"already assigned keep the value they were given.",
		Example: "  podium secret set GREETING < greeting.txt\n" +
			"  printf %s 'hunter2' | podium secret set DB_PASSWORD\n" +
			"  podium secret set DEPLOY_KEY --from-file ~/.ssh/id_ed25519",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !spec.SecretNameRE.MatchString(name) {
				return &ExitError{Code: ExitUsage, Err: fmt.Errorf(
					"%q is not a valid secret name (%s)", name, spec.SecretNameRE.String())}
			}
			raw, err := readSecretValue(e, cmd, value, fromFile)
			if err != nil {
				return &ExitError{Code: ExitUsage, Err: err}
			}
			if len(raw) == 0 {
				return &ExitError{Code: ExitUsage, Err: errors.New("the value is empty")}
			}

			res, err := e.client.secrets.SetSecret(cmd.Context(),
				connect.NewRequest(&podiumv1.SetSecretRequest{Name: name, Value: raw}))
			zeroValue(raw)
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("set secret: %w", err)}
			}
			s := res.Msg.GetSecret()
			e.note("secret %s is at version %d", s.GetName(), s.GetVersion())
			return nil
		},
	}
	cmd.Flags().StringVar(&value, "value", "", "the value, inline (visible in shell history — prefer stdin)")
	cmd.Flags().StringVar(&fromFile, "from-file", "", "read the value from this file, byte for byte")
	return cmd
}

// readSecretValue resolves the three ways of supplying a value, rejecting any combination
// of them: a command that silently picked one would be a command that could set the wrong
// secret.
func readSecretValue(e *env, cmd *cobra.Command, value, fromFile string) ([]byte, error) {
	switch {
	case value != "" && fromFile != "":
		return nil, errors.New("--value and --from-file are mutually exclusive")
	case value != "":
		e.note("--value puts this secret in your shell history and in the process table; " +
			"pipe it on stdin instead")
		return []byte(value), nil
	case fromFile != "":
		raw, err := os.ReadFile(fromFile) //nolint:gosec // the user names their own file
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", fromFile, err)
		}
		return raw, nil
	default:
		if isTerminal(e.stdin) {
			e.note("reading the value from stdin; end it with ctrl-d")
		}
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, fmt.Errorf("read the value from stdin: %w", err)
		}
		return bytes.TrimSuffix(bytes.TrimSuffix(raw, []byte("\n")), []byte("\r")), nil
	}
}

// zeroValue scrubs the CLI's own copy once it has been sent.
func zeroValue(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func newSecretLsCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List secret names and versions",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := e.client.secrets.ListSecrets(cmd.Context(),
				connect.NewRequest(&podiumv1.ListSecretsRequest{}))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("list secrets: %w", err)}
			}
			w := newTable(e.stdout)
			fmt.Fprintln(w, "NAME\tVERSION\tKEY\tSET BY\tUPDATED")
			for _, s := range res.Msg.GetSecrets() {
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n",
					s.GetName(), s.GetVersion(), s.GetKeyId(), s.GetCreatedBy(),
					updatedAge(s.GetUpdatedAt().AsTime()))
			}
			return w.Flush()
		},
	}
}

func updatedAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return ago(t)
}

func newSecretRmCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:     "rm NAME",
		Aliases: []string{"delete"},
		Short:   "Delete a secret",
		Long: "Delete a secret.\n\n" +
			"Tasks already assigned keep the value they were given; the next task that\n" +
			"references the name fails before it reaches a node.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := e.client.secrets.DeleteSecret(cmd.Context(),
				connect.NewRequest(&podiumv1.DeleteSecretRequest{Name: args[0]}))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("delete secret: %w", err)}
			}
			e.note("secret %s deleted", args[0])
			return nil
		},
	}
}

// parseSecretRef turns `--secret NAME`, `--secret NAME:env:KEY` or
// `--secret NAME:file:/path` into a SecretRef. The bare form is the common one: put the
// secret in an environment variable of the same name.
//
// The path is split off with SplitN, so a target path containing a colon survives.
func parseSecretRef(s string) (spec.SecretRef, error) {
	parts := strings.SplitN(s, ":", 3)
	switch len(parts) {
	case 1:
		return spec.SecretRef{Name: parts[0], Target: spec.SecretTargetEnv, Key: parts[0]}, nil
	case 3:
		return spec.SecretRef{Name: parts[0], Target: parts[1], Key: parts[2]}, nil
	default:
		return spec.SecretRef{}, fmt.Errorf(
			"--secret %q is not NAME, NAME:env:KEY or NAME:file:/absolute/path", s)
	}
}
