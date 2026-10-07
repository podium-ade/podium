//go:build integration

package docker

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/strslice"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/ids"
	"github.com/podium-ade/podium/pkg/spec"
)

// serveHTTP is a shell command that answers every connection on port with body, using the
// only server alpine's busybox has: nc.
func serveHTTP(port, body string) string {
	return `nc -lk -p ` + port + ` -e sh -c 'read l; printf "HTTP/1.0 200 OK\r\n\r\n%s\n" "` + body + `"'`
}

// previewSpec is a task that builds nothing but serves two things once its command has
// exited: a page from the task container, started in the background by the command, and a
// page from a sidecar. That is the shape of a preview — a web app in the task and its API
// beside it — reduced to nc.
func previewSpec(t *testing.T) spec.TaskSpec {
	return specWithSidecars(t, spec.TaskSpec{
		Image: testImage,
		Command: []string{"sh", "-c",
			`(` + serveHTTP("3000", "web says $PODIUM_URL_API") + ` &) ; echo built; exit 3`},
		Sidecars: map[string]spec.Sidecar{
			"api": {
				Image:     testImage,
				Command:   []string{"sh", "-c", serveHTTP("5011", "api")},
				Readiness: spec.Readiness{TCPPort: 5011},
			},
		},
		Expose: &spec.Expose{Ports: map[string]spec.ExposedPort{
			"web": {Port: 3000},
			"api": {Port: 5011, From: "api"},
		}},
		Timeout: spec.Duration(2 * time.Minute),
	})
}

func TestHeldRunServesItsPortsAfterTheCommandExits(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	t.Cleanup(func() { _ = e.Teardown(context.Background(), taskID, false) })

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID: taskID, LeaseID: ids.NewLease(), Spec: previewSpec(t),
		Preview: &Preview{Via: spec.ExposeViaLAN, Address: "127.0.0.1", BindIP: "127.0.0.1"},
	}, c.ch)
	events := c.finish()
	require.NoError(t, err, "events: %v", kindsOf(events))
	assertSeq(t, events)

	assert.True(t, res.Held, "an exposed task's run must end held")
	assert.Equal(t, 3, res.ExitCode, "the command's own exit code is the task's")
	kinds := kindsOf(events)
	assert.Equal(t, []string{KindExited, KindFinished}, kinds[len(kinds)-2:])
	require.Contains(t, kinds, KindPreview)
	for _, ev := range events {
		if p, ok := ev.Payload.(PreviewPayload); ok {
			assert.Equal(t, "http://127.0.0.1:5011", p.URLs["api"])
		}
	}
	assert.Contains(t, taskOutput(events), "built")

	ports, err := e.GatewayPorts(context.Background(), taskID)
	require.NoError(t, err)
	require.Len(t, ports, 2)

	assert.Equal(t, "web says http://127.0.0.1:5011\n", httpGet(t, ports[3000]))
	assert.Equal(t, "api\n", httpGet(t, ports[5011]))

	held, err := e.ListPreviews(context.Background())
	require.NoError(t, err)
	var found bool
	for _, hp := range held {
		if hp.TaskID == taskID {
			found = true
			assert.Equal(t, "127.0.0.1", hp.Address)
			assert.Equal(t, ports, hp.Ports)
		}
	}
	assert.True(t, found, "ListPreviews does not see the held task")

	require.NoError(t, e.Teardown(context.Background(), taskID, false))
	left, err := e.cli.ContainerList(context.Background(), container.ListOptions{
		All: true, Filters: filters.NewArgs(filters.Arg("label", LabelTask+"="+taskID)),
	})
	require.NoError(t, err)
	assert.Empty(t, left, "teardown left containers behind")
}

// TestAdoptNoticesAHeldExit is a node that restarted while a held task's command still ran:
// the event socket is gone and the container will not exit, so the exit file is the only
// thing that can say the command is done.
func TestAdoptNoticesAHeldExit(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	leaseID := ids.NewLease()
	teardownAfter(t, e, taskID)

	cid := createOrphan(t, e, taskID, leaseID, []string{"sh", "-c", "sleep 2; exit 5"})
	// createOrphan knows nothing of holding; make this one a held task the same way Run does.
	require.NoError(t, e.cli.ContainerRemove(context.Background(), cid, container.RemoveOptions{Force: true}))
	created, err := e.cli.ContainerCreate(context.Background(), &container.Config{
		Image:      testImage,
		Entrypoint: strslice.StrSlice{},
		Cmd:        []string{runnerTarget, "--", "sh", "-c", "sleep 2; exit 5"},
		Env: []string{
			"PODIUM_TASK_ID=" + taskID, "PODIUM_EVENTS_SOCK=" + eventsTarget, "PODIUM_WORKDIR=/", holdEnvVar + "=1",
		},
		Labels: taskLabels(taskID, leaseID),
	}, &container.HostConfig{
		Mounts: []mount.Mount{{Type: mount.TypeBind, Source: e.runnerPath, Target: runnerTarget, ReadOnly: true}},
		Binds:  []string{e.eventsSocketPath(taskID) + ":" + eventsTarget, mustHoldDir(t, e, taskID) + ":" + holdDir},
	}, nil, nil, containerName(taskID))
	require.NoError(t, err)
	require.NoError(t, e.cli.ContainerStart(context.Background(), created.ID, container.StartOptions{}))

	c := newCollector()
	res, err := e.Adopt(context.Background(), AdoptRequest{TaskID: taskID, LeaseID: leaseID, FromSeq: 100}, c.ch)
	kinds := kindsOf(c.finish())
	require.NoError(t, err)
	assert.True(t, res.Held)
	assert.Equal(t, 5, res.ExitCode)
	assert.Equal(t, []string{KindExited, KindFinished}, kinds[len(kinds)-2:])

	insp, err := e.cli.ContainerInspect(context.Background(), created.ID)
	require.NoError(t, err)
	assert.True(t, insp.State.Running, "a held container must still be running after adoption")
}

func httpGet(t *testing.T, port int) string {
	t.Helper()
	var last error
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
		if err != nil {
			last = err
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return string(b)
		}
		last = nil
	}
	t.Fatalf("nothing served on %d: %v", port, last)
	return ""
}

func mustHoldDir(t *testing.T, e *Executor, taskID string) string {
	t.Helper()
	dir, err := e.mkHoldDir(taskID)
	require.NoError(t, err)
	return dir
}
