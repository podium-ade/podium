// Package sandboxtest has a sandbox provider backed by the local Docker engine, and the
// conformance suite every provider has to pass. The local provider lets the whole sandbox
// path run in tests with no cloud account.
package sandboxtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/sandbox"
)

// Local is a sandbox provider whose sandboxes are containers on the local Docker engine.
// With Docker set, each one is a privileged docker:dind container, so the agent's docker
// strategy can run inside it; otherwise it is the requested image itself.
type Local struct {
	// Docker makes every sandbox a Docker-in-Docker container.
	Docker bool
	// Base is the image a sandbox with no Image starts from.
	Base string
}

const (
	localLabel   = "podium.sandboxtest"
	dindImage    = "docker:29-dind"
	defaultLocal = "ubuntu:24.04"
)

// Name implements sandbox.Provider.
func (l *Local) Name() string { return "local" }

// Capabilities implements sandbox.Provider.
func (l *Local) Capabilities() sandbox.Capabilities {
	if l.Docker {
		return sandbox.Capabilities{Docker: true, Root: true, Tmpfs: true}
	}
	return sandbox.Capabilities{NativeImage: true, Root: true}
}

func (l *Local) name(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "podium-sbx-" + hex.EncodeToString(sum[:6])
}

// Create implements sandbox.Provider.
func (l *Local) Create(ctx context.Context, req sandbox.CreateRequest) (string, error) {
	name := l.name(req.Key)
	if out, err := docker(ctx, nil, "inspect", "-f", "{{.Id}}", name); err == nil {
		return strings.TrimSpace(out), nil
	}
	if req.Snapshot != "" {
		return "", &sandbox.Error{Op: "create", Code: "unsupported", Err: errors.New("the local provider has no snapshots")}
	}
	args := []string{"run", "-d", "--name", name, "--label", localLabel + "=1"}
	for _, p := range req.Ports {
		args = append(args, "-p", "127.0.0.1::"+strconv.Itoa(p))
	}
	if req.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(req.MemoryMB)+"m")
	}
	if l.Docker {
		args = append(args, "--privileged", "-e", "DOCKER_TLS_CERTDIR=", dindImage)
	} else {
		image := req.Image
		if image == "" {
			image = l.Base
		}
		if image == "" {
			image = defaultLocal
		}
		args = append(args, "--entrypoint", "sleep", image, "infinity")
	}
	out, err := docker(ctx, nil, args...)
	if err != nil {
		return "", &sandbox.Error{Op: "create", Code: "docker", Err: err}
	}
	return strings.TrimSpace(out), nil
}

// WaitReady implements sandbox.Provider.
func (l *Local) WaitReady(ctx context.Context, id string) error {
	probe := "true"
	if l.Docker {
		probe = "docker info >/dev/null 2>&1"
	}
	for {
		if _, err := docker(ctx, nil, "exec", id, "sh", "-c", probe); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// PutFile implements sandbox.Provider.
func (l *Local) PutFile(ctx context.Context, id, path string, r io.Reader, _ int64) error {
	dir := path[:strings.LastIndex(path, "/")+1]
	_, err := docker(ctx, r, "exec", "-i", id, "sh", "-c", "mkdir -p '"+dir+"' && cat > '"+path+"'")
	return err
}

// Exec implements sandbox.Provider.
func (l *Local) Exec(ctx context.Context, id, cmd string, detached bool) (sandbox.ExecResult, error) {
	if detached {
		_, err := docker(ctx, nil, "exec", "-d", id, "sh", "-c", cmd)
		return sandbox.ExecResult{}, err
	}
	c := exec.CommandContext(ctx, "docker", "exec", id, "sh", "-c", cmd)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	res := sandbox.ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}

// Expose implements sandbox.Provider.
func (l *Local) Expose(ctx context.Context, id string, port int) (sandbox.Endpoint, error) {
	out, err := docker(ctx, nil, "port", id, strconv.Itoa(port)+"/tcp")
	if err != nil {
		return sandbox.Endpoint{}, &sandbox.Error{Op: "expose", Code: "not_published",
			Err: fmt.Errorf("port %d was not in CreateRequest.Ports: %w", port, err)}
	}
	host, p, err := net.SplitHostPort(strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]))
	if err != nil {
		return sandbox.Endpoint{}, err
	}
	return sandbox.Endpoint{URL: (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, p)}).String()}, nil
}

// ExtendTTL implements sandbox.Provider.
func (l *Local) ExtendTTL(context.Context, string, time.Duration) error { return nil }

// Delete implements sandbox.Provider.
func (l *Local) Delete(ctx context.Context, id string) error {
	_, err := docker(ctx, nil, "rm", "-f", "-v", id)
	if err != nil && strings.Contains(err.Error(), "No such container") {
		return nil
	}
	return err
}

// Limits implements sandbox.Provider.
func (l *Local) Limits(context.Context) (sandbox.Limits, error) {
	return sandbox.Limits{FreeSandboxes: -1, StartsLeft: -1}, nil
}

func docker(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "docker", args...)
	c.Stdin = stdin
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
