package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ArtifactsDir is where a task drops files it wants kept without asking for anything: the
// node collects everything under it when the task exits. It is inside the workspace volume
// so a sidecar or a later step can write there too.
const ArtifactsDir = "/workspace/.podium/artifacts"

// AddArtifact is `podium-runner artifact add PATH [--name NAME] [--type CONTENT_TYPE]`.
//
// It writes one line to the node's event socket and exits. It is deliberately a separate
// invocation of the same binary rather than an API: a task can call it from any shell, in
// the middle of a run, with no library and no credentials — the socket is already mounted
// and the node is the only thing listening on it.
func AddArtifact(cfg Config, args []string) int {
	path, name, contentType, err := parseArtifactArgs(args)
	if err != nil {
		fmt.Fprintln(stderrOf(cfg), "podium-runner artifact add:", err)
		return exitUsage
	}

	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintln(stderrOf(cfg), "podium-runner artifact add:", err)
		return exitUsage
	}
	if info.IsDir() {
		fmt.Fprintf(stderrOf(cfg), "podium-runner artifact add: %s is a directory; "+
			"put files under %s instead and they are collected when the task exits\n", path, ArtifactsDir)
		return exitUsage
	}

	sock := cfg.EventsSock
	if sock == "" {
		sock = DefaultEventsSock
	}
	client := dialEvents(sock, cfg.dialTimeout())
	if client == nil {
		fmt.Fprintf(stderrOf(cfg), "podium-runner artifact add: no node is listening on %s; "+
			"the file is still on disk but nothing will collect it\n", sock)
		return exitInternal
	}
	defer client.close()

	client.artifact(name, path, contentType)
	return 0
}

// parseArtifactArgs handles the three flags by hand, for the same reason main does: the
// flag package would mangle a path that starts with a dash and there is nothing else here
// worth a dependency.
func parseArtifactArgs(args []string) (path, name, contentType string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--name" || a == "--type":
			if i+1 >= len(args) {
				return "", "", "", fmt.Errorf("%s needs a value", a)
			}
			i++
			if a == "--name" {
				name = args[i]
			} else {
				contentType = args[i]
			}
		case strings.HasPrefix(a, "--name="):
			name = strings.TrimPrefix(a, "--name=")
		case strings.HasPrefix(a, "--type="):
			contentType = strings.TrimPrefix(a, "--type=")
		case path == "":
			path = a
		default:
			return "", "", "", fmt.Errorf("unexpected argument %q", a)
		}
	}
	if path == "" {
		return "", "", "", fmt.Errorf("usage: podium-runner artifact add PATH [--name NAME] [--type CONTENT_TYPE]")
	}
	if !filepath.IsAbs(path) {
		abs, aerr := filepath.Abs(path)
		if aerr != nil {
			return "", "", "", aerr
		}
		path = abs
	}
	if name == "" {
		name = filepath.Base(path)
	}
	return path, name, contentType, nil
}

func stderrOf(cfg Config) *os.File {
	if cfg.Stderr != nil {
		return cfg.Stderr
	}
	return os.Stderr
}

func (c Config) dialTimeout() time.Duration {
	if c.DialTimeout > 0 {
		return c.DialTimeout
	}
	return DefaultDialTimeout
}
