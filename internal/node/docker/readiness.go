package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

const (
	// readinessInterval is how often a sidecar is re-probed.
	readinessInterval = 500 * time.Millisecond
	// probeTimeout bounds one probe attempt, so a hung dial or exec cannot outlive the
	// interval budget by much.
	probeTimeout = 5 * time.Second
	// probeShell is what an exec probe runs. Every image that can host a sidecar worth
	// probing has it; one that does not is reported rather than waited out.
	probeShell = "/bin/sh"
	// probeMissingTool is the exit status a shell uses for "command not found", which the
	// probe scripts reuse to say "this image has neither nc nor wget". It is a permanent
	// failure, not a not-ready-yet.
	probeMissingTool = 127
	// probeCannotExecute is the shell's "found it, could not run it".
	probeCannotExecute = 126
)

// errProbeUnsupported means the sidecar's image cannot answer the probe the spec asked
// for — no shell, or no nc/wget. Retrying would burn the whole readiness timeout for
// nothing, so it fails immediately with an actionable message.
var errProbeUnsupported = errors.New("readiness probe not supported by this image")

// waitReady blocks until the sidecar passes its readiness probe, its container dies, or
// the spec's timeout expires.
func (e *Executor) waitReady(ctx context.Context, cid, name string, r spec.Readiness) error {
	timeout := r.Timeout.Std()
	if timeout <= 0 {
		timeout = spec.DefaultReadinessTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(readinessInterval)
	defer ticker.Stop()

	// Two "last errors": the last real verdict from a probe, and whatever went wrong
	// most recently. The final attempt usually dies of the readiness deadline itself, and
	// reporting *that* would replace "nothing is listening on port 5432" with a context
	// error — the one thing an operator cannot act on.
	var lastVerdict, lastErr error
	for {
		running, err := e.containerRunning(ctx, cid)
		switch {
		case err != nil:
			lastErr = err
		case !running:
			return fmt.Errorf("sidecar %s exited before it became ready", name)
		default:
			ready, perr := e.probe(ctx, cid, r)
			if ready {
				return nil
			}
			if errors.Is(perr, errProbeUnsupported) {
				return perr
			}
			lastErr = perr
			if perr != nil && !errors.Is(perr, context.DeadlineExceeded) {
				lastVerdict = perr
			}
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			cause := lastVerdict
			if cause == nil {
				cause = lastErr
			}
			if cause == nil {
				cause = errors.New("never passed its readiness probe")
			}
			return fmt.Errorf("gave up after %s: %w", timeout, cause)
		}
	}
}

// probe runs one readiness attempt. A sidecar with no probe is ready as soon as its
// container is running, which the caller has already established.
func (e *Executor) probe(ctx context.Context, cid string, r spec.Readiness) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	switch {
	case r.TCPPort > 0:
		return e.probeTCP(ctx, cid, r.TCPPort)
	case r.HTTPPath != "":
		port := r.HTTPPort
		if port == 0 {
			port = spec.DefaultHTTPPort
		}
		return e.probeHTTP(ctx, cid, port, r.HTTPPath)
	case len(r.Command) > 0:
		code, out, err := e.execProbe(ctx, cid, r.Command)
		if err != nil {
			return false, err
		}
		switch code {
		case 0:
			return true, nil
		case probeCannotExecute, probeMissingTool:
			// The engine could not run the command at all. Retrying it every 500ms until
			// the timeout would hide a typo behind a minute of waiting.
			return false, fmt.Errorf("%w: readiness command %v: %s",
				errProbeUnsupported, r.Command, trimProbeOutput(out))
		default:
			return false, fmt.Errorf("readiness command exited %d: %s", code, trimProbeOutput(out))
		}
	default:
		return true, nil
	}
}

// probeTCP checks that something is listening on the sidecar's port.
//
// The design dials the container's address on the task bridge from the node. That works
// on a native Linux engine, where the bridge is an interface on the same host, and not on
// Docker Desktop, where the engine lives in a VM the host cannot route into — the dial
// does not fail fast there, it hangs until the readiness timeout. So the node only dials
// directly when it really shares a network namespace with the engine, and otherwise
// probes from inside the container with busybox nc.
func (e *Executor) probeTCP(ctx context.Context, cid string, port int) (bool, error) {
	if e.directDial {
		addr, err := e.containerAddr(ctx, cid)
		if err != nil {
			return false, err
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
		if err != nil {
			return false, err
		}
		_ = conn.Close()
		return true, nil
	}

	script := fmt.Sprintf(`nc -z -w 1 127.0.0.1 %[1]d >/dev/null 2>&1 && exit 0
nc -w 1 127.0.0.1 %[1]d </dev/null >/dev/null 2>&1 && exit 0
command -v nc >/dev/null 2>&1 || exit %[2]d
exit 1`, port, probeMissingTool)

	code, out, err := e.execProbe(ctx, cid, []string{probeShell, "-c", script})
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case probeMissingTool:
		return false, fmt.Errorf(
			"%w: it has no nc to test port %d from the inside, and this host cannot route to the task network; give the sidecar a readiness.command instead",
			errProbeUnsupported, port)
	default:
		return false, fmt.Errorf("nothing is listening on port %d: %s", port, trimProbeOutput(out))
	}
}

// probeHTTP treats any 2xx or 3xx as ready.
func (e *Executor) probeHTTP(ctx context.Context, cid string, port int, path string) (bool, error) {
	if e.directDial {
		addr, err := e.containerAddr(ctx, cid)
		if err != nil {
			return false, err
		}
		url := "http://" + net.JoinHostPort(addr, strconv.Itoa(port)) + path
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			return true, nil
		}
		return false, fmt.Errorf("GET %s returned %s", path, resp.Status)
	}

	// busybox wget follows redirects and fails on anything that is not a success, which
	// is the 2xx/3xx rule expressed in the only HTTP client these images have.
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	script := fmt.Sprintf(`wget -q -O /dev/null -T 2 %s && exit 0
command -v wget >/dev/null 2>&1 || exit %d
exit 1`, shellQuote(url), probeMissingTool)

	code, out, err := e.execProbe(ctx, cid, []string{probeShell, "-c", script})
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case probeMissingTool:
		return false, fmt.Errorf(
			"%w: it has no wget to fetch %s from the inside, and this host cannot route to the task network; give the sidecar a readiness.command instead",
			errProbeUnsupported, path)
	default:
		return false, fmt.Errorf("GET %s did not succeed: %s", path, trimProbeOutput(out))
	}
}

// execProbe runs cmd inside the container and reports its exit status. An image with no
// shell — or no such binary — fails the exec outright, which becomes errProbeUnsupported
// rather than a probe that is retried until the timeout.
func (e *Executor) execProbe(ctx context.Context, cid string, cmd []string) (int, []byte, error) {
	created, err := e.cli.ContainerExecCreate(ctx, cid, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", errProbeUnsupported, err)
	}

	attached, err := e.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", errProbeUnsupported, err)
	}
	defer attached.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = attached.Conn.SetDeadline(deadline)
	}

	var out strings.Builder
	if _, err := stdcopy.StdCopy(&out, &out, attached.Reader); err != nil && !errors.Is(err, io.EOF) {
		e.log.Debug("readiness probe output ended badly", "container", cid, "error", err)
	}

	insp, err := e.cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return 0, nil, fmt.Errorf("inspect readiness probe: %w", err)
	}
	return insp.ExitCode, []byte(out.String()), nil
}

func (e *Executor) containerRunning(ctx context.Context, cid string) (bool, error) {
	insp, err := e.cli.ContainerInspect(ctx, cid)
	if err != nil {
		return false, fmt.Errorf("inspect container %s: %w", cid, err)
	}
	return insp.State != nil && insp.State.Running, nil
}

// containerAddr is the container's address on its task network, which is the only network
// it is attached to.
func (e *Executor) containerAddr(ctx context.Context, cid string) (string, error) {
	insp, err := e.cli.ContainerInspect(ctx, cid)
	if err != nil {
		return "", fmt.Errorf("inspect container %s: %w", cid, err)
	}
	if insp.NetworkSettings == nil {
		return "", fmt.Errorf("container %s has no network settings yet", cid)
	}
	for _, ep := range insp.NetworkSettings.Networks {
		if ep.IPAddress != "" {
			return ep.IPAddress, nil
		}
	}
	return "", fmt.Errorf("container %s has no address on the task network yet", cid)
}

// shellQuote wraps s so a shell sees exactly these bytes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// trimProbeOutput keeps a probe's diagnostics short enough to sit inside an error.
func trimProbeOutput(out []byte) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "(no output)"
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
