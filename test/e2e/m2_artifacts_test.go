//go:build e2e

package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/server"
	"github.com/podium-ade/podium/internal/server/artifacts/fakes3"
	"github.com/podium-ade/podium/internal/server/logs"
)

// TestArtifactsAreCollectedAndDownloaded is the step's acceptance path driven the way an
// operator drives it: a real node, a real container, the real CLI. The object store is an
// in-process S3 endpoint rather than a real object store container — but it is
// the same API and the presigned URLs it hands back are verified for real.
func TestArtifactsAreCollectedAndDownloaded(t *testing.T) {
	fake := fakes3.Start(t)
	h := newHarness(t, func(c *server.Config) { c.S3 = fake.Config() })
	startNode(t, h)

	const report = "the report, byte for byte\n"
	code, stdout, stderr := h.podium("run", "--image", "alpine:3", "--",
		"sh", "-c", "mkdir -p "+docker.AutoArtifactDir+
			"; printf '"+report+"' > "+docker.AutoArtifactDir+"/report.txt"+
			"; printf 'PNG' > /tmp/shot.png"+
			"; /podium/runner artifact add /tmp/shot.png --type image/png"+
			"; echo done")
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	require.Contains(t, stdout, "done")
	taskID := taskIDFrom(t, stderr)

	list := h.podiumOK("artifacts", taskID)
	t.Logf("podium artifacts %s\n%s", taskID, list)
	require.Contains(t, list, "report.txt")
	require.Contains(t, list, "shot.png")
	require.Contains(t, list, "image/png")

	// `podium artifact get` downloads byte-identical content.
	dir := t.TempDir()
	reportID := artifactIDFor(t, list, "report.txt")
	out := filepath.Join(dir, "report.txt")
	_, _, getErr := h.podium("artifact", "get", reportID, "-o", out)
	t.Logf("podium artifact get %s -o %s\n%s", reportID, out, getErr)
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, report, string(got), "the downloaded artifact must be byte-identical")

	// And straight from the object store, with a real presigned signature.
	direct := filepath.Join(dir, "direct.txt")
	dcode, _, dstderr := h.podium("artifact", "get", reportID, "--via-server=false", "-o", direct)
	require.Equal(t, 0, dcode, dstderr)
	gotDirect, err := os.ReadFile(direct)
	require.NoError(t, err)
	require.Equal(t, report, string(gotDirect))
	require.Zero(t, fake.Rejected, "no presigned request may be refused")

	requireNoPodiumResources(t)
}

// TestLogsRollUpAndSurviveThePrune is the other half of the step: a finished task's log
// moves into the object store, its hot rows are dropped, and `podium logs` still prints
// the whole thing.
func TestLogsRollUpAndSurviveThePrune(t *testing.T) {
	fake := fakes3.Start(t)
	h := newHarness(t,
		func(c *server.Config) { c.S3 = fake.Config() },
		func(c *server.Config) {
			c.Rollup = logs.RollupConfig{
				Interval:      200 * time.Millisecond,
				Settle:        time.Millisecond,
				PruneInterval: 200 * time.Millisecond,
				Grace:         time.Second,
			}
		})
	startNode(t, h)

	code, stdout, stderr := h.podium("run", "--image", "alpine:3", "--",
		"sh", "-c", "for i in 1 2 3 4 5 6 7 8; do echo tick $i; done")
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)
	taskID := taskIDFrom(t, stderr)

	want := h.podiumOK("logs", taskID)
	require.Contains(t, want, "tick 1")
	require.Contains(t, want, "tick 8")

	waitFor(t, 30*time.Second, "the task's log to reach the object store", func() bool {
		return strings.Contains(h.podiumOK("artifacts", taskID), "stdout")
	})
	waitFor(t, 30*time.Second, "the task's log chunks to be pruned", func() bool {
		return countLogChunks(t, h.databaseURL, taskID) == 0
	})

	after := h.podiumOK("logs", taskID)
	require.Equal(t, want, after, "a pruned task's log must still read back in full")
	requireNoPodiumResources(t)
}

// artifactIDFor picks an artifact ID out of the `podium artifacts` table by name.
func artifactIDFor(t *testing.T, table, name string) string {
	t.Helper()
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[2] == name {
			return fields[0]
		}
	}
	t.Fatalf("no artifact named %q in:\n%s", name, table)
	return ""
}

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
