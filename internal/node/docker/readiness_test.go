package docker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/require"
)

// TestExecProbeErrorOnlyBlamesTheImageWhenItIsTheImage.
//
// errProbeUnsupported ends the readiness wait immediately, so it may only be said of an
// image that genuinely cannot run the probe. Wrapping every exec failure in it — which is
// what execProbe used to do — turned a five-second timeout on a busy engine into "readiness
// probe not supported by this image" AND threw away the rest of the readiness budget, on an
// alpine:3 sidecar that has a perfectly good `nc`.
func TestExecProbeErrorOnlyBlamesTheImageWhenItIsTheImage(t *testing.T) {
	unrunnable := map[string]error{
		"no shell": errors.New(`Error response from daemon: failed to create task for container: ` +
			`failed to create shim task: OCI runtime create failed: runc create failed: ` +
			`unable to start container process: exec: "/bin/sh": stat /bin/sh: no such file or directory: unknown`),
		"binary not on the path": errors.New(`OCI runtime exec failed: exec failed: ` +
			`unable to start container process: exec: "nc": executable file not found in $PATH: unknown`),
		"wrong architecture": errors.New(`OCI runtime exec failed: exec failed: ` +
			`unable to start container process: exec format error: unknown`),
	}
	for name, err := range unrunnable {
		t.Run(name, func(t *testing.T) {
			got := execProbeError(err)
			require.ErrorIs(t, got, errProbeUnsupported)
			require.ErrorIs(t, got, err, "the engine's own words have to survive the wrap")
		})
	}

	retryable := map[string]error{
		"this attempt's own deadline": &url.Error{
			Op:  "Post",
			URL: "http://%2Fvar%2Frun%2Fdocker.sock/v1.51/containers/abc/exec",
			Err: context.DeadlineExceeded,
		},
		"the readiness wait was cancelled": fmt.Errorf("exec: %w", context.Canceled),
		"the engine hung up": errors.New(`Post "http://%2Fvar%2Frun%2Fdocker.sock/v1.51/` +
			`containers/abc/exec": EOF`),
		"the container went away": errors.New("Error response from daemon: No such container: abc"),
		"the engine is unwell":    errors.New("Error response from daemon: internal server error"),
	}
	for name, err := range retryable {
		t.Run(name, func(t *testing.T) {
			got := execProbeError(err)
			require.NotErrorIs(t, got, errProbeUnsupported,
				"a transient failure must stay retryable or the readiness budget is wasted")
			require.Equal(t, err, got)
		})
	}

	require.NoError(t, execProbeError(nil))
}

// TestExecProbeErrorDoesNotBlameTheImageForAMissingEngine: an absent Docker socket fails
// with "connect: no such file or directory", the same words runc uses for a missing shell.
// The error comes from a real client so the classification is tested against the engine
// client's actual error type, not a hand-written imitation.
func TestExecProbeErrorDoesNotBlameTheImageForAMissingEngine(t *testing.T) {
	cli, err := client.NewClientWithOpts(client.WithHost("unix:///nonexistent/podium-test.sock"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })

	_, err = cli.ContainerExecCreate(context.Background(), "abc", container.ExecOptions{Cmd: []string{"/bin/sh"}})
	require.Error(t, err)
	require.NotErrorIs(t, execProbeError(err), errProbeUnsupported)
}
