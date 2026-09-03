package node

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// releaseServer serves one fake release: an archive holding the four binaries, and a
// checksums.txt. corrupt makes the manifest disagree with the bytes.
func releaseServer(t *testing.T, version, payload string, corrupt bool) (*httptest.Server, UpgradeOptions) {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"podium":        "cli",
		"podium-server": "server",
		"podium-node":   payload,
		"podium-runner": "runner",
		"LICENSE":       "TODO",
	} {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	archive := buf.Bytes()

	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	if corrupt {
		digest = hex.EncodeToString(make([]byte, sha256.Size))
	}

	opts := UpgradeOptions{Version: version, GOOS: "linux", GOARCH: "amd64"}
	name := opts.ArchiveName()

	mux := http.NewServeMux()
	mux.HandleFunc("/"+version+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  podium_x_darwin_arm64.tar.gz\n%s  %s\n", digest, digest, name)
	})
	mux.HandleFunc("/"+version+"/"+name, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	opts.BaseURL = srv.URL
	opts.HTTPClient = srv.Client()
	return srv, opts
}

func TestFetchBinaryVerifiesAndExtracts(t *testing.T) {
	_, opts := releaseServer(t, "v9.9.9", "the new node binary", false)
	stage := filepath.Join(t.TempDir(), "podium-node.upgrade")

	require.NoError(t, FetchBinary(context.Background(), opts, stage))

	got, err := os.ReadFile(stage)
	require.NoError(t, err)
	require.Equal(t, "the new node binary", string(got))

	info, err := os.Stat(stage)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm(),
		"a staged binary that is not executable is a node that will not start")
}

func TestFetchBinaryRefusesAWrongChecksum(t *testing.T) {
	_, opts := releaseServer(t, "v9.9.9", "tampered", true)
	stage := filepath.Join(t.TempDir(), "podium-node.upgrade")

	err := FetchBinary(context.Background(), opts, stage)
	require.ErrorContains(t, err, "failed its checksum")
	require.NoFileExists(t, stage, "a rejected download must not be left behind")
}

func TestFetchBinaryRefusesAnUnlistedArchive(t *testing.T) {
	_, opts := releaseServer(t, "v9.9.9", "irrelevant", false)
	// Ask for an architecture the manifest does not mention.
	opts.GOARCH = "riscv64"

	err := FetchBinary(context.Background(), opts, filepath.Join(t.TempDir(), "stage"))
	require.ErrorContains(t, err, "checksums.txt does not mention")
}

func TestArchiveNameMatchesTheReleaseTemplate(t *testing.T) {
	opts := UpgradeOptions{Version: "v0.3.1", GOOS: "linux", GOARCH: "arm64"}
	require.Equal(t, "podium_0.3.1_linux_arm64.tar.gz", opts.ArchiveName())
	// The leading v is optional on the way in and never present in the file name.
	opts.Version = "0.3.1"
	require.Equal(t, "podium_0.3.1_linux_arm64.tar.gz", opts.ArchiveName())
}

func TestSwapBinaryReplacesTheDestination(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "podium-node")
	require.NoError(t, os.WriteFile(dest, []byte("old"), 0o755))

	stage := StagePath(dest)
	require.Equal(t, filepath.Join(dir, ".podium-node.upgrade"), stage)
	require.NoError(t, os.WriteFile(stage, []byte("new"), 0o755))

	require.NoError(t, SwapBinary(stage, dest))
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, "new", string(got))
	require.NoFileExists(t, stage)
}
