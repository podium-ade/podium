package node

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultReleaseBaseURL is where a released archive lives. GoReleaser publishes
// <base>/<tag>/podium_<version>_<os>_<arch>.tar.gz alongside <base>/<tag>/checksums.txt.
const DefaultReleaseBaseURL = "https://github.com/podium-ade/podium/releases/download"

// maxArchiveBytes caps what an upgrade will read off the network. A Podium archive is a few
// tens of megabytes; a hundred is generous and a redirect to something enormous is not going
// to fill the disk of a machine that is mid-upgrade.
const maxArchiveBytes = 256 << 20

// UpgradeOptions describes one release to fetch. Everything has a default except Version.
type UpgradeOptions struct {
	// Version is the release tag, "v0.3.1". The leading v is optional.
	Version string
	// BaseURL defaults to DefaultReleaseBaseURL. A test — or an air-gapped mirror — points
	// it somewhere else.
	BaseURL string
	// GOOS and GOARCH default to this process's.
	GOOS, GOARCH string
	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
}

// ArchiveName is the release archive for these options, matching .goreleaser.yaml's
// name_template. It is exported because the installer script and the docs quote it.
func (o UpgradeOptions) ArchiveName() string {
	return fmt.Sprintf("podium_%s_%s_%s.tar.gz", strings.TrimPrefix(o.Version, "v"), o.goos(), o.goarch())
}

func (o UpgradeOptions) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

func (o UpgradeOptions) goarch() string {
	if o.GOARCH != "" {
		return o.GOARCH
	}
	return runtime.GOARCH
}

func (o UpgradeOptions) baseURL() string {
	if o.BaseURL != "" {
		return strings.TrimSuffix(o.BaseURL, "/")
	}
	return DefaultReleaseBaseURL
}

func (o UpgradeOptions) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return http.DefaultClient
}

// FetchBinary downloads the release archive, checks its SHA-256 against the release's own
// checksums.txt, extracts podium-node from it and writes the result to stagePath with mode
// 0755. It never touches the running binary: swapping is the caller's move, and it is a
// rename so that it is atomic.
//
// The checksum is verified before anything is extracted. An archive that does not appear in
// checksums.txt is refused rather than trusted — a release whose manifest does not mention a
// file is a release that was tampered with or built wrong, and either way it is not the one
// that was signed.
func FetchBinary(ctx context.Context, opts UpgradeOptions, stagePath string) error {
	if strings.TrimSpace(opts.Version) == "" {
		return errors.New("upgrade: no version given")
	}
	base := opts.baseURL() + "/" + opts.Version
	name := opts.ArchiveName()

	sums, err := fetchAll(ctx, opts.client(), base+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("upgrade: fetch checksums: %w", err)
	}
	want, err := checksumFor(string(sums), name)
	if err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}

	archive, err := fetchAll(ctx, opts.client(), base+"/"+name)
	if err != nil {
		return fmt.Errorf("upgrade: fetch %s: %w", name, err)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("upgrade: %s failed its checksum: manifest says %s, download is %s",
			name, want, hex.EncodeToString(got[:]))
	}

	return extractBinary(archive, "podium-node", stagePath)
}

// SwapBinary replaces dest with the staged file. Rename is atomic within a filesystem and
// works on a binary that is currently executing: the running process keeps the inode it
// started from and the next start picks up the new one.
func SwapBinary(stagePath, dest string) error {
	if err := os.Rename(stagePath, dest); err != nil {
		return fmt.Errorf("upgrade: install %s: %w (is %s on the same filesystem as %s?)",
			dest, err, stagePath, dest)
	}
	return nil
}

// StagePath is where FetchBinary should write, given the destination.
func StagePath(dest string) string {
	return filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".upgrade")
}

func fetchAll(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxArchiveBytes {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, maxArchiveBytes)
	}
	return body, nil
}

// checksumFor reads a GoReleaser checksums.txt — "<sha256>  <filename>" per line.
func checksumFor(manifest, name string) (string, error) {
	for _, line := range strings.Split(manifest, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt does not mention %s", name)
}

// extractBinary pulls exactly one named file out of a .tar.gz. It matches the base name and
// nothing else, so a crafted archive cannot write outside stagePath however its entries are
// spelled.
func extractBinary(archive []byte, want, stagePath string) error {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return fmt.Errorf("upgrade: read archive: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("upgrade: archive contains no %s", want)
		}
		if err != nil {
			return fmt.Errorf("upgrade: read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != want {
			continue
		}
		f, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755) //nolint:gosec // it is an executable
		if err != nil {
			return fmt.Errorf("upgrade: stage %s: %w", stagePath, err)
		}
		if _, err := io.Copy(f, io.LimitReader(tr, maxArchiveBytes)); err != nil {
			_ = f.Close()
			_ = os.Remove(stagePath)
			return fmt.Errorf("upgrade: stage %s: %w", stagePath, err)
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(stagePath)
			return fmt.Errorf("upgrade: stage %s: %w", stagePath, err)
		}
		// O_CREATE honours the umask, and a binary that is not executable is a node that
		// will not start.
		return os.Chmod(stagePath, 0o755) //nolint:gosec // it is an executable
	}
}
