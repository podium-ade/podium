//go:build integration

package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/ids"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// TestATaskResolvesTheHostByName is the node half of the shared memory: a turn's container
// reaches the memory service on the control-plane host, and it does that by name.
//
// Docker Desktop provides host.docker.internal on its own; a native Linux engine does not,
// which is what the host-gateway extra host is for. This test therefore proves the same
// thing on both, and on Linux it proves the entry rather than the platform.
func TestATaskResolvesTheHostByName(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"getent", "hosts", "host.docker.internal"},
		},
	}, c.ch)
	require.NoError(t, err)

	events := c.finish()
	out := taskOutput(events)
	require.Equal(t, 0, res.ExitCode, "getent must resolve the host; output was %q", out)

	// getent prints "<address> <name>". An empty address would mean the name resolved to
	// nothing, which is the failure this test exists to catch.
	fields := strings.Fields(strings.TrimSpace(out))
	require.GreaterOrEqual(t, len(fields), 2, "getent output was %q", out)
	assert.NotEmpty(t, fields[0], "the host must resolve to an address")
	assert.Contains(t, out, "host.docker.internal")
}

// TestASidecarDoesNotResolveTheHostByName is the other half of the decision: the extra host
// is on the task container only. A sidecar is a stock database or cache image with no reason
// to reach the control-plane host, and the smaller the surface the better.
func TestASidecarDoesNotResolveTheHostByName(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	_, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: specWithSidecars(t, spec.TaskSpec{
			Image:   testImage,
			Command: []string{"true"},
			Sidecars: map[string]spec.Sidecar{
				"one": {Image: testImage, Command: []string{"sleep", "300"}},
			},
		}),
	}, c.ch)
	require.NoError(t, err)
	c.finish()

	insp, err := e.cli.ContainerInspect(context.Background(), sidecarContainerName(taskID, "one"))
	require.NoError(t, err)
	assert.Empty(t, insp.HostConfig.ExtraHosts, "a sidecar gets no extra hosts")

	task, err := e.cli.ContainerInspect(context.Background(), containerName(taskID))
	require.NoError(t, err)
	assert.Equal(t, []string{hostGatewayEntry}, task.HostConfig.ExtraHosts,
		"and the task gets exactly the one")
}
