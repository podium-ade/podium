//go:build integration

package docker

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/ids"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// fakeUploader stands in for the control plane. It keeps every artifact's bytes so a test
// can assert the round trip is byte-identical, and can be told to refuse.
type fakeUploader struct {
	mu    sync.Mutex
	got   map[string][]byte
	types map[string]string
	fail  error
}

func newFakeUploader() *fakeUploader {
	return &fakeUploader{got: make(map[string][]byte), types: make(map[string]string)}
}

func (f *fakeUploader) UploadArtifact(_ context.Context, up ArtifactUpload) (ArtifactStored, error) {
	if f.fail != nil {
		return ArtifactStored{}, f.fail
	}
	body, err := io.ReadAll(up.Body)
	if err != nil {
		return ArtifactStored{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got[up.Name] = body
	f.types[up.Name] = up.ContentType
	id := ids.NewArtifact()
	return ArtifactStored{
		ID:        id,
		ObjectKey: fmt.Sprintf("tasks/%s/artifacts/%s-%s", up.TaskID, id, up.Name),
		Size:      int64(len(body)),
	}, nil
}

func (f *fakeUploader) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.got))
	for k := range f.got {
		out = append(out, k)
	}
	return out
}

func artifactEvents(events []Event) map[string]ArtifactPayload {
	out := make(map[string]ArtifactPayload)
	for _, ev := range events {
		if ev.Kind != KindArtifact {
			continue
		}
		if p, ok := ev.Payload.(ArtifactPayload); ok {
			out[p.Name] = p
		}
	}
	return out
}

// TestAutoCollectedArtifactsAreUploaded is the step's first acceptance item: a task writes
// into /workspace/.podium/artifacts and the node collects everything under it when the
// container exits, naming each file by its path relative to that directory.
func TestAutoCollectedArtifactsAreUploaded(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	up := newFakeUploader()
	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:    taskID,
		LeaseID:   ids.NewLease(),
		Artifacts: up,
		Spec: spec.TaskSpec{
			Image: testImage,
			Command: []string{"sh", "-c", `
				mkdir -p ` + AutoArtifactDir + `/shots
				printf 'the report\n' > ` + AutoArtifactDir + `/report.txt
				printf 'PNGDATA' > ` + AutoArtifactDir + `/shots/first.png
				echo done`},
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)
	require.Equal(t, []byte("the report\n"), up.got["report.txt"])
	require.Equal(t, []byte("PNGDATA"), up.got["shots/first.png"])
	require.ElementsMatch(t, []string{"report.txt", "shots/first.png"}, up.names())

	// Every artifact event precedes exited, so the documented ordering still holds.
	kinds := kindsOf(events)
	first := indexOfKind(events, KindArtifact)
	require.Positive(t, first)
	require.Less(t, first, indexOfKind(events, KindExited))
	require.NotContains(t, kinds, KindError)

	stored := artifactEvents(events)
	require.Len(t, stored, 2)
	require.Equal(t, int64(len("the report\n")), stored["report.txt"].SizeBytes)
	require.Contains(t, stored["report.txt"].ObjectKey, "/artifacts/")
	require.NotEmpty(t, stored["report.txt"].ArtifactID)
}

// TestRunnerArtifactAddUploadsWithItsContentType is the second acceptance item: a task
// calls `podium-runner artifact add` mid-run and the file is collected with the type it
// declared.
func TestRunnerArtifactAddUploadsWithItsContentType(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	up := newFakeUploader()
	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:    taskID,
		LeaseID:   ids.NewLease(),
		Artifacts: up,
		Spec: spec.TaskSpec{
			Image: testImage,
			Command: []string{"sh", "-c", `
				printf 'PNG' > /tmp/shot.png
				` + runnerTarget + ` artifact add /tmp/shot.png --type image/png
				echo added`},
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)
	require.Equal(t, []byte("PNG"), up.got["shot.png"])
	require.Equal(t, "image/png", up.types["shot.png"])

	stored := artifactEvents(events)
	require.Contains(t, stored, "shot.png")
	require.Equal(t, "image/png", stored["shot.png"].ContentType)
	require.Less(t, indexOfKind(events, KindArtifact), indexOfKind(events, KindExited))
}

// TestAFailedArtifactUploadDoesNotFailTheTask: artifacts are not required for a task to
// run. A refused upload is a retryable error event — which implies no status transition —
// and the task still exits 0.
func TestAFailedArtifactUploadDoesNotFailTheTask(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	up := newFakeUploader()
	up.fail = fmt.Errorf("the object store is down")
	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:    taskID,
		LeaseID:   ids.NewLease(),
		Artifacts: up,
		Spec: spec.TaskSpec{
			Image: testImage,
			Command: []string{"sh", "-c",
				"mkdir -p " + AutoArtifactDir + "; echo ok > " + AutoArtifactDir + "/r.txt; echo done"},
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode, "a failed artifact upload must not change the task's outcome")

	events := c.finish()
	assertSeq(t, events)
	var sawRetryable bool
	for _, ev := range events {
		if p, ok := ev.Payload.(ErrorPayload); ok {
			require.False(t, p.AbortsRun, "an artifact failure must never end the run")
			require.Contains(t, p.Message, "r.txt")
			sawRetryable = true
		}
	}
	require.True(t, sawRetryable, "the failure must be reported")
	require.Equal(t, KindFinished, kindsOf(events)[len(events)-1])
}

// TestATaskWithNoArtifactUploaderStillRuns: a node whose control plane has no object store
// configured runs tasks exactly as before, and the auto-collection directory costs nothing.
func TestATaskWithNoArtifactUploaderStillRuns(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:  taskID,
		LeaseID: ids.NewLease(),
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "echo hi"},
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)
	require.NotContains(t, kindsOf(events), KindArtifact)
	require.NotContains(t, kindsOf(events), KindError)
}

// TestAMissingArtifactDirectoryIsNotAnError: most tasks never write one, so a task that
// leaves /workspace/.podium/artifacts absent must produce no artifacts and no complaint.
func TestAMissingArtifactDirectoryIsNotAnError(t *testing.T) {
	e := newTestExecutor(t)
	taskID := ids.NewTask()
	teardownAfter(t, e, taskID)

	up := newFakeUploader()
	c := newCollector()
	res, err := e.Run(context.Background(), Request{
		TaskID:    taskID,
		LeaseID:   ids.NewLease(),
		Artifacts: up,
		Spec: spec.TaskSpec{
			Image:   testImage,
			Command: []string{"sh", "-c", "echo nothing to keep"},
		},
	}, c.ch)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode)

	events := c.finish()
	assertSeq(t, events)
	require.Empty(t, up.names())
	require.NotContains(t, kindsOf(events), KindError)
	require.NotContains(t, kindsOf(events), KindArtifact)
}

// TestCollectingArtifactsReportsAFailureThatIsNotAMissingDirectory is the other half of the
// silent-artifact-loss bug: every CopyFromContainer error used to be read as "there was no
// directory" and logged at Debug, so a run whose event socket had been unlinked lost two of
// its three artifacts and still reported success. Only an absent path may be swallowed.
func TestCollectingArtifactsReportsAFailureThatIsNotAMissingDirectory(t *testing.T) {
	e := newTestExecutor(t)
	up := newFakeUploader()
	c := newCollector()

	// No container ID at all: the engine client refuses that with an invalid-argument
	// error, which is emphatically not a missing directory.
	col := e.newArtifactCollector(
		Request{TaskID: ids.NewTask(), LeaseID: ids.NewLease(), Artifacts: up},
		newEmitter(context.Background(), c.ch), "")
	col.collectDir(context.Background(), AutoArtifactDir)

	events := c.finish()
	require.Len(t, events, 1)
	require.Equal(t, KindError, events[0].Kind)
	payload, ok := events[0].Payload.(ErrorPayload)
	require.True(t, ok)
	require.False(t, payload.AbortsRun, "a lost artifact must not end the run")
	require.Contains(t, payload.Message, AutoArtifactDir)
	require.Empty(t, up.names())
}
