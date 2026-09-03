//go:build integration

package nodes_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server"
	"github.com/alvaroibarguen/podium/internal/server/artifacts"
	"github.com/alvaroibarguen/podium/internal/server/artifacts/fakes3"
	"github.com/alvaroibarguen/podium/internal/server/logs"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

// withFakeS3 points a harness at an in-process S3 endpoint. Podium's own tests may not pull
// a MinIO image on this machine, so the fake is what the object-store paths run against;
// the presigned URLs it hands back are verified for real (see internal/server/artifacts/fakes3).
func withFakeS3(t *testing.T) (func(*server.Config), *fakes3.Server) {
	t.Helper()
	fake := fakes3.Start(t)
	return func(c *server.Config) { c.S3 = fake.Config() }, fake
}

// withFastRollUp shrinks the roll-up schedule so a test can watch a task's logs move into
// the object store and its hot rows disappear inside a few seconds instead of a day.
func withFastRollUp(c *server.Config) {
	c.Rollup = logs.RollupConfig{
		Interval:      200 * time.Millisecond,
		Settle:        time.Millisecond,
		PruneInterval: 200 * time.Millisecond,
		Grace:         time.Second,
	}
}

// uploadArtifact is what internal/node's artifactUploader does, reduced to the wire.
func (n *fakeNode) uploadArtifact(
	ctx context.Context, a *podiumv1.Assign, name, contentType string, body []byte,
) (*podiumv1.UploadArtifactResponse, error) {
	stream := n.h.nodes.UploadArtifact(ctx)
	if err := stream.Send(&podiumv1.UploadArtifactRequest{
		Msg: &podiumv1.UploadArtifactRequest_Metadata{Metadata: &podiumv1.ArtifactMetadata{
			NodeId:      n.id,
			NodeKey:     n.key,
			TaskId:      a.GetTaskId(),
			LeaseId:     a.GetLeaseId(),
			Name:        name,
			ContentType: contentType,
			SizeBytes:   int64(len(body)),
		}},
	}); err != nil {
		_, rerr := stream.CloseAndReceive()
		return nil, rerr
	}
	const chunk = 1 << 20
	for off := 0; off < len(body); off += chunk {
		end := min(off+chunk, len(body))
		if err := stream.Send(&podiumv1.UploadArtifactRequest{
			Msg: &podiumv1.UploadArtifactRequest_Chunk{Chunk: body[off:end]},
		}); err != nil {
			_, rerr := stream.CloseAndReceive()
			return nil, rerr
		}
	}
	res, err := stream.CloseAndReceive()
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func (h *harness) listArtifacts(taskID string) []*podiumv1.Artifact {
	h.t.Helper()
	res, err := h.artifacts.ListArtifacts(context.Background(),
		connect.NewRequest(&podiumv1.ListArtifactsRequest{TaskId: taskID}))
	require.NoError(h.t, err)
	return res.Msg.GetArtifacts()
}

// download fetches an artifact through the server's own proxy route, which is what
// `podium artifact get` uses by default.
func (h *harness) download(artifactID string) (int, []byte) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		h.url+"/artifacts/"+artifactID, nil)
	require.NoError(h.t, err)
	resp, err := h.http.Do(req)
	require.NoError(h.t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	return resp.StatusCode, body
}

// assignTaskTo creates a task and waits for the node to be handed it, so a test can push
// events under a real lease.
func (h *harness) assignTaskTo(node *fakeNode) *podiumv1.Assign {
	h.t.Helper()
	task := h.createTask(nil, "sh", "-c", "echo hi")
	assign := node.awaitAssign(assignTimeout)
	require.Equal(h.t, task.GetId(), assign.GetTaskId())
	return assign
}

// countLogChunks is how many hot rows a task still has in Postgres.
func countLogChunks(t *testing.T, databaseURL, taskID string) int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err)
	defer func() { _ = conn.Close(ctx) }()
	var n int
	require.NoError(t, conn.QueryRow(ctx,
		"select count(*) from task_log_chunks where task_id = $1", taskID).Scan(&n))
	return n
}

// TestNodeUploadsAnArtifactThroughTheServer is the acceptance path: a task produces a file,
// the node streams it through NodeService.UploadArtifact, and it comes back byte-identical
// from both the presigned URL and the server's own proxy.
func TestNodeUploadsAnArtifactThroughTheServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s3opt, fake := withFakeS3(t)
	h := newHarness(t, s3opt)
	node := enrollNode(t, h, "node-artifacts", nil)
	node.open(ctx)
	defer node.disconnect()

	assign := h.assignTaskTo(node)
	// Bigger than one 1 MB chunk, so the client-streaming path is what is exercised.
	body := bytes.Repeat([]byte("report line\n"), 200_000)

	res, err := node.uploadArtifact(ctx, assign, "report.txt", "text/plain", body)
	require.NoError(t, err)
	require.NotEmpty(t, res.GetArtifactId())
	require.Equal(t, int64(len(body)), res.GetSizeBytes())
	require.Equal(t,
		fmt.Sprintf("tasks/%s/artifacts/%s-report.txt", assign.GetTaskId(), res.GetArtifactId()),
		res.GetObjectKey())

	node.send(node.lifecycle(assign, 0, "hi\n")...)
	h.awaitTaskStatus(assign.GetTaskId(), podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 20*time.Second)

	list := h.listArtifacts(assign.GetTaskId())
	require.Len(t, list, 1)
	require.Equal(t, "report.txt", list[0].GetName())
	require.Equal(t, "file", list[0].GetKind())
	require.Equal(t, "text/plain", list[0].GetContentType())
	require.Equal(t, int64(len(body)), list[0].GetSizeBytes())
	require.Len(t, list[0].GetSha256(), 64)

	// Through the server.
	code, got := h.download(res.GetArtifactId())
	require.Equal(t, http.StatusOK, code, string(got))
	require.Equal(t, body, got, "the proxied download must be byte-identical")

	// Straight from the object store, with a real signature.
	urlRes, err := h.artifacts.GetArtifactURL(ctx,
		connect.NewRequest(&podiumv1.GetArtifactURLRequest{ArtifactId: res.GetArtifactId()}))
	require.NoError(t, err)
	require.NotEmpty(t, urlRes.Msg.GetUrl())
	require.True(t, urlRes.Msg.GetExpiresAt().AsTime().After(time.Now()))

	presigned, err := http.Get(urlRes.Msg.GetUrl()) //nolint:noctx // fetching a presigned URL
	require.NoError(t, err)
	defer func() { _ = presigned.Body.Close() }()
	direct, err := io.ReadAll(presigned.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, presigned.StatusCode)
	require.Equal(t, body, direct)
	require.Zero(t, fake.Rejected, "no presigned request may be refused")
}

// TestAnOversizedArtifactIsRejectedAndTheTaskStillSucceeds is the 512 MB cap, asserted
// without moving 512 MB: the metadata declares more than the limit and the server refuses
// before it reads a byte.
func TestAnOversizedArtifactIsRejectedAndTheTaskStillSucceeds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s3opt, _ := withFakeS3(t)
	h := newHarness(t, s3opt)
	node := enrollNode(t, h, "node-toobig", nil)
	node.open(ctx)
	defer node.disconnect()

	assign := h.assignTaskTo(node)
	stream := h.nodes.UploadArtifact(ctx)
	require.NoError(t, stream.Send(&podiumv1.UploadArtifactRequest{
		Msg: &podiumv1.UploadArtifactRequest_Metadata{Metadata: &podiumv1.ArtifactMetadata{
			NodeId: node.id, NodeKey: node.key,
			TaskId: assign.GetTaskId(), LeaseId: assign.GetLeaseId(),
			Name:      "huge.bin",
			SizeBytes: 600 << 20,
		}},
	}))
	_, err := stream.CloseAndReceive()
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "limit")

	require.Empty(t, h.listArtifacts(assign.GetTaskId()))

	// The refusal is a retryable error event, which implies no status transition: the task
	// itself is unaffected and still succeeds.
	node.send(node.stamp(assign, &podiumv1.TaskEvent{
		Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR,
		Payload: &podiumv1.TaskEvent_Error{Error: &podiumv1.Error{
			Message: `artifact "huge.bin" was not stored: over the limit`, Retryable: true,
		}},
	}))
	node.send(node.lifecycle(assign, 0, "hi\n")...)
	h.awaitTaskStatus(assign.GetTaskId(), podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 20*time.Second)
}

// TestAnUploadMustComeFromTheNodeHoldingTheTask covers the two ways an upload can be
// somebody else's: a bad node key, and a node that does not hold the task.
func TestAnUploadMustComeFromTheNodeHoldingTheTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s3opt, _ := withFakeS3(t)
	h := newHarness(t, s3opt)
	node := enrollNode(t, h, "node-owner", nil)
	node.open(ctx)
	defer node.disconnect()
	assign := h.assignTaskTo(node)

	other := enrollNode(t, h, "node-other", nil)

	for name, tc := range map[string]struct {
		meta *podiumv1.ArtifactMetadata
		code connect.Code
	}{
		"bad key": {
			meta: &podiumv1.ArtifactMetadata{
				NodeId: node.id, NodeKey: "not-the-key",
				TaskId: assign.GetTaskId(), Name: "r.txt",
			},
			code: connect.CodeUnauthenticated,
		},
		"another node": {
			meta: &podiumv1.ArtifactMetadata{
				NodeId: other.id, NodeKey: other.key,
				TaskId: assign.GetTaskId(), Name: "r.txt",
			},
			code: connect.CodePermissionDenied,
		},
		"unknown task": {
			meta: &podiumv1.ArtifactMetadata{
				NodeId: node.id, NodeKey: node.key,
				TaskId: "task_does_not_exist", Name: "r.txt",
			},
			code: connect.CodeNotFound,
		},
	} {
		t.Run(name, func(t *testing.T) {
			stream := h.nodes.UploadArtifact(ctx)
			require.NoError(t, stream.Send(&podiumv1.UploadArtifactRequest{
				Msg: &podiumv1.UploadArtifactRequest_Metadata{Metadata: tc.meta},
			}))
			_, err := stream.CloseAndReceive()
			require.Error(t, err)
			require.Equal(t, tc.code, connect.CodeOf(err))
		})
	}
	require.Empty(t, h.listArtifacts(assign.GetTaskId()))
}

// TestADeadObjectStoreDoesNotStopATask is the invariant the whole step rests on: artifacts
// are not required for a task to run. The server is pointed at an endpoint with nothing
// listening on it and a task still goes queued -> succeeded, with /readyz reporting 503
// the whole time.
func TestADeadObjectStoreDoesNotStopATask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dead := fakes3.DeadConfig(t)
	h := newHarness(t, func(c *server.Config) { c.S3 = dead })

	resp, err := h.http.Get(h.url + "/readyz") //nolint:noctx // a health probe in a test
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Contains(t, string(body), "object store unreachable")

	node := enrollNode(t, h, "node-no-s3", nil)
	node.open(ctx)
	defer node.disconnect()

	task := h.createTask(nil, "sh", "-c", "echo hi")
	assign := node.awaitAssign(assignTimeout)
	require.Equal(t, task.GetId(), assign.GetTaskId())

	// An upload fails, and it fails in a way the node can report as retryable.
	_, uerr := node.uploadArtifact(ctx, assign, "r.txt", "text/plain", []byte("x"))
	require.Error(t, uerr)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(uerr))

	node.send(node.lifecycle(assign, 0, "hi\n")...)
	final := h.awaitTaskStatus(task.GetId(), podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 20*time.Second)
	require.Equal(t, int32(0), final.GetExitCode())
	require.Empty(t, final.GetFailureReason())

	// /healthz is process liveness and stays 200: the process is fine, a dependency is not.
	health, err := h.http.Get(h.url + "/healthz") //nolint:noctx // a health probe in a test
	require.NoError(t, err)
	_ = health.Body.Close()
	require.Equal(t, http.StatusOK, health.StatusCode)
}

// TestNoObjectStoreConfiguredIsASupportedDeployment: Podium without PODIUM_S3_ENDPOINT is
// still a task runner. /readyz is green and an upload is refused with a message that says
// what to configure.
func TestNoObjectStoreConfiguredIsASupportedDeployment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newHarness(t)
	resp, err := h.http.Get(h.url + "/readyz") //nolint:noctx // a health probe in a test
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	node := enrollNode(t, h, "node-none", nil)
	node.open(ctx)
	defer node.disconnect()
	assign := h.assignTaskTo(node)

	_, uerr := node.uploadArtifact(ctx, assign, "r.txt", "text/plain", []byte("x"))
	require.Error(t, uerr)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(uerr))
	require.Contains(t, uerr.Error(), "PODIUM_S3_ENDPOINT")

	node.send(node.lifecycle(assign, 0, "hi\n")...)
	h.awaitTaskStatus(assign.GetTaskId(), podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 20*time.Second)
}

// TestLogsRollUpAndThePrunedTaskStillReadsBack is the log half of the step: a finished
// task's chunks move into the object store as logs/<stream>.log.zst, the hot rows go after
// the grace period, and `podium logs` — StreamTaskEvents — still returns the whole log.
func TestLogsRollUpAndThePrunedTaskStillReadsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s3opt, _ := withFakeS3(t)
	h := newHarness(t, s3opt, withFastRollUp)
	node := enrollNode(t, h, "node-rollup", nil)
	node.open(ctx)
	defer node.disconnect()

	assign := h.assignTaskTo(node)
	taskID := assign.GetTaskId()

	lines := make([]string, 0, 64)
	for i := range 64 {
		lines = append(lines, fmt.Sprintf("tick %d\n", i))
	}
	node.send(node.lifecycle(assign, 0, lines...)...)
	h.awaitTaskStatus(taskID, podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 20*time.Second)

	before := h.streamEvents(taskID, 0)
	wantLog := logBytes(before)
	require.Equal(t, strings.Join(lines, ""), string(wantLog))

	// The roll-up sweep writes tasks/<id>/logs/stdout.log.zst.
	var logArtifact *podiumv1.Artifact
	waitFor(t, 20*time.Second, "the task's logs to be rolled up", func() bool {
		for _, a := range h.listArtifacts(taskID) {
			if a.GetKind() == "log" {
				logArtifact = a
				return true
			}
		}
		return false
	})
	require.Equal(t, "stdout", logArtifact.GetName())
	require.Equal(t, "tasks/"+taskID+"/logs/stdout.log.zst", logArtifact.GetObjectKey())
	require.Equal(t, artifacts.LogContentType, logArtifact.GetContentType())

	// The object really is the log, zstd-compressed.
	code, compressed := h.download(logArtifact.GetId())
	require.Equal(t, http.StatusOK, code, string(compressed))
	dec, err := zstd.NewReader(bytes.NewReader(compressed))
	require.NoError(t, err)
	defer dec.Close()
	plain, err := io.ReadAll(dec)
	require.NoError(t, err)
	require.Equal(t, wantLog, plain)

	// After the grace period the hot rows go, and the log still reads back whole.
	waitFor(t, 30*time.Second, "the task's log chunks to be pruned", func() bool {
		return countLogChunks(t, h.databaseURL, taskID) == 0
	})

	after := h.streamEvents(taskID, 0)
	require.Equal(t, wantLog, logBytes(after), "a pruned task's log must still read back in full")
	requireAscendingSeq(t, after)

	// from_seq still means what it means everywhere else.
	half := after[len(after)/2].GetSeq()
	resumed := h.streamEvents(taskID, half)
	require.NotEmpty(t, resumed)
	require.Greater(t, resumed[0].GetSeq(), half)
}

// TestRollUpKeepsTheResumeOffsetsFromGoingBackwards is the hazard ruling 6 names: the
// per-stream byte offsets a restarting node resumes against, and the sequence high-water
// mark a synthetic event numbers above, are both derived from the very rows the prune
// deletes. They are recorded on the task when it is rolled up, so pruning cannot lower
// them — and a node that comes back is still told the truth.
func TestRollUpKeepsTheResumeOffsetsFromGoingBackwards(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s3opt, _ := withFakeS3(t)
	h := newHarness(t, s3opt, withFastRollUp)
	node := enrollNode(t, h, "node-offsets", nil)
	node.open(ctx)
	defer node.disconnect()

	assign := h.assignTaskTo(node)
	taskID := assign.GetTaskId()

	// Explicit source offsets, so the expected answers are known numbers.
	const stdoutOffset, stderrOffset = 4096, 512
	node.send(
		node.stamp(assign, &podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING}),
		node.stamp(assign, &podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED}),
		node.stamp(assign, &podiumv1.TaskEvent{
			Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
			Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
				Stream:       podiumv1.LogChunk_STREAM_STDOUT,
				Bytes:        []byte("output\n"),
				SourceOffset: stdoutOffset,
			}},
		}),
		node.stamp(assign, &podiumv1.TaskEvent{
			Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
			Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
				Stream:       podiumv1.LogChunk_STREAM_STDERR,
				Bytes:        []byte("warning\n"),
				SourceOffset: stderrOffset,
			}},
		}),
		node.stamp(assign, &podiumv1.TaskEvent{
			Kind:    podiumv1.TaskEventKind_TASK_EVENT_KIND_EXITED,
			Payload: &podiumv1.TaskEvent_Exited{Exited: &podiumv1.Exited{}},
		}),
		node.stamp(assign, &podiumv1.TaskEvent{
			Kind:    podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED,
			Payload: &podiumv1.TaskEvent_Finished{Finished: &podiumv1.Finished{}},
		}),
	)
	h.awaitTaskStatus(taskID, podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, 20*time.Second)

	st, err := store.New(ctx, h.databaseURL)
	require.NoError(t, err)
	defer st.Close()

	offsetsBefore, err := st.TaskStreamOffsets(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, int64(stdoutOffset), offsetsBefore.Stdout)
	require.Equal(t, int64(stderrOffset), offsetsBefore.Stderr)
	highBefore, err := st.MaxTaskSeq(ctx, taskID)
	require.NoError(t, err)
	require.Positive(t, highBefore)

	waitFor(t, 30*time.Second, "the task's log chunks to be pruned", func() bool {
		return countLogChunks(t, h.databaseURL, taskID) == 0
	})

	// The rows those two answers were computed from are gone. The answers are not.
	offsetsAfter, err := st.TaskStreamOffsets(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, offsetsBefore, offsetsAfter,
		"pruning a rolled-up task must not move the resume offsets")
	highAfter, err := st.MaxTaskSeq(ctx, taskID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, highAfter, highBefore,
		"the sequence high-water mark must never go backwards")

	// And a node that comes back reporting the container is told to drop it rather than
	// resume it: roll-up only ever touches terminal tasks, and a terminal task is never
	// adopted. That is the first of the two belts; the offsets above are the second.
	node.disconnect()
	again := &fakeNode{t: t, h: h, name: node.name, id: node.id, key: node.key, seq: node.seq}
	again.openWith(ctx, &podiumv1.NodeCapacity{MaxTasks: 4, CpuCores: 8, MemoryMb: 16384}, []string{taskID})
	defer again.disconnect()

	ack := again.recv(10 * time.Second).GetHelloAck()
	require.NotNil(t, ack, "the HelloAck is always the first message on a stream")
	require.Len(t, ack.GetTasks(), 1)
	require.Equal(t, taskID, ack.GetTasks()[0].GetTaskId())
	require.False(t, ack.GetTasks()[0].GetAdopt(), "a terminal task is never adopted")
}

// TestARunningTaskIsNeverRolledUp: roll-up is terminal-only, so a task that is still
// running keeps every hot row no matter how long it has been going.
func TestARunningTaskIsNeverRolledUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s3opt, _ := withFakeS3(t)
	h := newHarness(t, s3opt, withFastRollUp)
	node := enrollNode(t, h, "node-running", nil)
	node.open(ctx)
	defer node.disconnect()

	assign := h.assignTaskTo(node)
	taskID := assign.GetTaskId()
	node.send(
		node.stamp(assign, &podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING}),
		node.stamp(assign, &podiumv1.TaskEvent{Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED}),
		node.stamp(assign, &podiumv1.TaskEvent{
			Kind: podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG,
			Payload: &podiumv1.TaskEvent_Log{Log: &podiumv1.LogChunk{
				Stream: podiumv1.LogChunk_STREAM_STDOUT, Bytes: []byte("still going\n"), SourceOffset: 12,
			}},
		}),
	)
	h.awaitTaskStatus(taskID, podiumv1.TaskStatus_TASK_STATUS_RUNNING, 20*time.Second)

	// Several roll-up and prune sweeps' worth.
	time.Sleep(3 * time.Second)
	require.Positive(t, countLogChunks(t, h.databaseURL, taskID),
		"a running task's log chunks must never be pruned")
	require.Empty(t, h.listArtifacts(taskID))
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func logBytes(events []*podiumv1.TaskEvent) []byte {
	var out []byte
	for _, e := range events {
		if c := e.GetLog(); c != nil {
			out = append(out, c.GetBytes()...)
		}
	}
	return out
}

func maxSeq(events []*podiumv1.TaskEvent) uint64 {
	var high uint64
	for _, e := range events {
		if e.GetSeq() > high {
			high = e.GetSeq()
		}
	}
	return high
}

func requireAscendingSeq(t *testing.T, events []*podiumv1.TaskEvent) {
	t.Helper()
	var last uint64
	for _, e := range events {
		require.Greater(t, e.GetSeq(), last, "events must be strictly ascending by seq")
		last = e.GetSeq()
	}
}
