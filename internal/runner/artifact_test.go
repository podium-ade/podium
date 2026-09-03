package runner

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortTempDir is a directory shallow enough for an AF_UNIX path to fit in sun_path, which
// is 104 bytes on darwin. t.TempDir() on macOS is already ~90 characters by itself.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pdmrn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestParseArtifactArgs(t *testing.T) {
	cases := []struct {
		name              string
		args              []string
		wantName, wantCT  string
		wantBase, wantErr bool
	}{
		{name: "path only", args: []string{"/tmp/shot.png"}, wantName: "shot.png", wantBase: true},
		{name: "separate flags", args: []string{"/tmp/shot.png", "--name", "hero.png", "--type", "image/png"},
			wantName: "hero.png", wantCT: "image/png", wantBase: true},
		{name: "equals flags", args: []string{"--name=hero.png", "--type=image/png", "/tmp/shot.png"},
			wantName: "hero.png", wantCT: "image/png", wantBase: true},
		{name: "no path", args: nil, wantErr: true},
		{name: "dangling flag", args: []string{"/tmp/a", "--name"}, wantErr: true},
		{name: "two paths", args: []string{"/tmp/a", "/tmp/b"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, name, contentType, err := parseArtifactArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseArtifactArgs(%q) = %q, %q, %q; want an error", tc.args, path, name, contentType)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArtifactArgs(%q): %v", tc.args, err)
			}
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if contentType != tc.wantCT {
				t.Errorf("content type = %q, want %q", contentType, tc.wantCT)
			}
			if tc.wantBase && !filepath.IsAbs(path) {
				t.Errorf("path %q is not absolute", path)
			}
		})
	}
}

func TestParseArtifactArgsMakesARelativePathAbsolute(t *testing.T) {
	path, name, _, err := parseArtifactArgs([]string{"shot.png"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("path %q must be absolute: the node resolves it inside the container", path)
	}
	if name != "shot.png" {
		t.Fatalf("name = %q, want shot.png", name)
	}
}

// TestAddArtifactWritesOneEvent is the whole of the in-container helper: it writes a single
// artifact line to the socket the node is listening on and exits 0.
func TestAddArtifactWritesOneEvent(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "events.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	lines := make(chan string, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		sc := bufio.NewScanner(conn)
		if sc.Scan() {
			lines <- sc.Text()
		}
	}()

	file := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(file, []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := AddArtifact(Config{EventsSock: sock, DialTimeout: time.Second}, []string{file, "--type", "image/png"}); code != 0 {
		t.Fatalf("AddArtifact exited %d", code)
	}

	select {
	case line := <-lines:
		var ev struct {
			V           int    `json:"v"`
			Kind        string `json:"kind"`
			Name        string `json:"name"`
			Path        string `json:"path"`
			ContentType string `json:"content_type"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("undecodable event %q: %v", line, err)
		}
		if ev.V != protocolVersion || ev.Kind != kindArtifact {
			t.Fatalf("envelope = v%d %q", ev.V, ev.Kind)
		}
		if ev.Name != "shot.png" || ev.Path != file || ev.ContentType != "image/png" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no artifact event reached the socket")
	}
}

func TestAddArtifactRefusesADirectoryAndAMissingFile(t *testing.T) {
	dir := shortTempDir(t)
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devnull.Close() }()
	cfg := Config{EventsSock: filepath.Join(dir, "events.sock"), DialTimeout: time.Millisecond, Stderr: devnull}

	if code := AddArtifact(cfg, []string{dir}); code != exitUsage {
		t.Errorf("a directory exited %d, want %d", code, exitUsage)
	}
	if code := AddArtifact(cfg, []string{filepath.Join(dir, "nope")}); code != exitUsage {
		t.Errorf("a missing file exited %d, want %d", code, exitUsage)
	}
}

// TestAddArtifactWithNoListenerFails: nothing is going to collect the file, and saying so
// with a non-zero status is more useful than pretending it worked.
func TestAddArtifactWithNoListenerFails(t *testing.T) {
	dir := shortTempDir(t)
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devnull.Close() }()

	file := filepath.Join(dir, "r.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{EventsSock: filepath.Join(dir, "events.sock"), DialTimeout: 10 * time.Millisecond, Stderr: devnull}
	if code := AddArtifact(cfg, []string{file}); code != exitInternal {
		t.Errorf("exited %d, want %d", code, exitInternal)
	}
}
