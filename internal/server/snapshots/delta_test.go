package snapshots

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/pkg/spec"
)

type entry struct{ name, body string }

func tarOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		if strings.HasSuffix(e.name, "/") {
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}))
			continue
		}
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func contents(t *testing.T, raw []byte) []entry {
	t.Helper()
	var out []entry
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		require.NoError(t, err)
		body, _ := io.ReadAll(tr)
		out = append(out, entry{hdr.Name, string(body)})
	}
}

// TestADifferenceStoresOnlyWhatChangedAndRestoresTheWhole: the session changed one file, added
// one, deleted one and left the rest as the base had them. The difference holds the change,
// the addition and the deletion; merged with the base it is the session's tree again.
func TestADifferenceStoresOnlyWhatChangedAndRestoresTheWhole(t *testing.T) {
	big := strings.Repeat("vendored dependency ", 100_000)
	base := tarOf(t,
		entry{"workspace/", ""}, entry{"workspace/go.mod", "module x"}, entry{"workspace/main.go", "package main"},
		entry{"workspace/vendor/", ""}, entry{"workspace/vendor/dep.go", big}, entry{"workspace/old.txt", "gone soon"})
	session := tarOf(t,
		entry{"workspace/", ""}, entry{"workspace/go.mod", "module x"}, entry{"workspace/main.go", "package main // fixed"},
		entry{"workspace/vendor/", ""}, entry{"workspace/vendor/dep.go", big}, entry{"workspace/new/", ""},
		entry{"workspace/new/file.go", "package new"})

	m, err := readManifest(bytes.NewReader(base))
	require.NoError(t, err)
	var delta bytes.Buffer
	st, err := writeDelta(&delta, bytes.NewReader(session), m)
	require.NoError(t, err)
	assert.Equal(t, deltaStats{Kept: 3, Unchanged: 4, Deleted: 1}, st)
	assert.Less(t, delta.Len()*20, len(session), "the vendored file is not stored again")

	var merged bytes.Buffer
	require.NoError(t, mergeDelta(&merged, bytes.NewReader(base), bytes.NewReader(delta.Bytes())))
	got := contents(t, merged.Bytes())
	want := contents(t, session)
	assert.ElementsMatch(t, want, got, "the merge is the session's tree: nothing deleted comes back, nothing changed is old")
	for _, e := range got {
		assert.NotEqual(t, deltaMarker, e.name, "the marker never reaches a node")
	}
	// Every directory before what is in it.
	seenDir := map[string]bool{}
	for _, e := range got {
		if strings.HasSuffix(e.name, "/") {
			seenDir[e.name] = true
			continue
		}
		dir := e.name[:strings.LastIndex(e.name, "/")+1]
		assert.True(t, seenDir[dir], "%s comes after %s", e.name, dir)
	}
}

func TestALargeEntryGoesThroughATemporaryFile(t *testing.T) {
	huge := strings.Repeat("x", inMemoryEntry+1)
	session := tarOf(t, entry{"workspace/blob", huge})
	var delta bytes.Buffer
	_, err := writeDelta(&delta, bytes.NewReader(session), manifest{})
	require.NoError(t, err)
	var merged bytes.Buffer
	require.NoError(t, mergeDelta(&merged, bytes.NewReader(tarOf(t)), bytes.NewReader(delta.Bytes())))
	assert.Equal(t, []entry{{"workspace/blob", huge}}, contents(t, merged.Bytes()))
}

// TestASessionFromABaseIsStoredAsADifference is the whole round trip through the service: a
// base is stored with its manifest; a session restored from it records where it started; its
// upload is kept as a difference a fraction of its size; opening it is the session's tree. A
// base replaced meanwhile is kept while that difference needs it, and deleted after.
func TestASessionFromABaseIsStoredAsADifference(t *testing.T) {
	svc, rows := testService(t)
	ctx := context.Background()
	repo := "https://github.com/acme/app"
	big := strings.Repeat("node_modules content ", 200_000)
	baseTar := tarOf(t, entry{"workspace/", ""}, entry{"workspace/vendor.js", big}, entry{"workspace/app.js", "v1"})
	baseStored, err := svc.Put(ctx, Upload{Repo: repo, Size: int64(len(baseTar)), Body: bytes.NewReader(baseTar)})
	require.NoError(t, err)

	// The session's first task restores the base: that is where it started.
	_, body, err := svc.Open(ctx, "sess_1", repo)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
	assert.Equal(t, baseStored.ObjectKey, rows.origins["sess_1"])

	sessionTar := tarOf(t, entry{"workspace/", ""}, entry{"workspace/vendor.js", big}, entry{"workspace/app.js", "v2"})
	stored, err := svc.Put(ctx, Upload{SessionID: "sess_1", Size: int64(len(sessionTar)), Body: bytes.NewReader(sessionTar)})
	require.NoError(t, err)
	assert.Equal(t, baseStored.ObjectKey, rows.sessions["sess_1"].BaseKey)
	assert.Less(t, stored.SizeBytes*20, int64(len(sessionTar)), "only app.js is stored again")

	_, body, err = svc.Open(ctx, "sess_1", repo)
	require.NoError(t, err)
	merged, err := io.ReadAll(body)
	require.NoError(t, err)
	_ = body.Close()
	assert.ElementsMatch(t, contents(t, sessionTar), contents(t, merged))

	// A new base: the old one stays while sess_1's difference is from it.
	newBase := tarOf(t, entry{"workspace/", ""}, entry{"workspace/app.js", "v3"})
	_, err = svc.Put(ctx, Upload{Repo: repo, Size: int64(len(newBase)), Body: bytes.NewReader(newBase)})
	require.NoError(t, err)
	svc.sweepRetiredBases(ctx)
	_, err = svc.s3.Get(ctx, baseStored.ObjectKey)
	require.NoError(t, err, "a base a snapshot is a difference from is kept")

	require.NoError(t, svc.Delete(ctx, "admin", "sess_1"))
	svc.sweepRetiredBases(ctx)
	_, err = svc.s3.Get(ctx, baseStored.ObjectKey)
	assert.Error(t, err, "nothing needs the old base any more")
	_, err = svc.s3.Get(ctx, manifestKey(baseStored.ObjectKey))
	assert.Error(t, err, "nor its manifest")
}

// A conversation from before each playbook kept its own workspace has one snapshot, keyed by
// the conversation. A playbook's first task in it continues from that, not from the base, and
// stores a difference from the base that snapshot started from.
func TestAPlaybookWorkspaceContinuesItsConversationsOlderOne(t *testing.T) {
	svc, rows := testService(t)
	ctx := context.Background()
	repo := "https://github.com/acme/app"
	big := strings.Repeat("node_modules content ", 200_000)
	baseTar := tarOf(t, entry{"workspace/", ""}, entry{"workspace/vendor.js", big}, entry{"workspace/app.js", "v1"})
	baseStored, err := svc.Put(ctx, Upload{Repo: repo, Size: int64(len(baseTar)), Body: bytes.NewReader(baseTar)})
	require.NoError(t, err)

	// The conversation's old workspace: started from the base, then changed.
	_, body, err := svc.Open(ctx, "sess_old", repo)
	require.NoError(t, err)
	_ = body.Close()
	oldTar := tarOf(t, entry{"workspace/", ""}, entry{"workspace/vendor.js", big}, entry{"workspace/app.js", "v2"})
	_, err = svc.Put(ctx, Upload{SessionID: "sess_old", Size: int64(len(oldTar)), Body: bytes.NewReader(oldTar)})
	require.NoError(t, err)

	key := spec.WorkspacePlaybookKey("sess_old", "coder")
	row, body, err := svc.Open(ctx, key, repo)
	require.NoError(t, err)
	restored, err := io.ReadAll(body)
	require.NoError(t, err)
	_ = body.Close()
	assert.Equal(t, "sess_old", row.SessionID)
	assert.ElementsMatch(t, contents(t, oldTar), contents(t, restored), "the conversation's work, not the base")
	assert.Equal(t, baseStored.ObjectKey, rows.origins[key])

	nextTar := tarOf(t, entry{"workspace/", ""}, entry{"workspace/vendor.js", big}, entry{"workspace/app.js", "v3"})
	stored, err := svc.Put(ctx, Upload{SessionID: key, Size: int64(len(nextTar)), Body: bytes.NewReader(nextTar)})
	require.NoError(t, err)
	assert.Equal(t, baseStored.ObjectKey, rows.sessions[key].BaseKey)
	assert.Less(t, stored.SizeBytes*20, int64(len(nextTar)), "stored as a difference")

	// From then on the playbook's own snapshot is what it restores.
	_, body, err = svc.Open(ctx, key, repo)
	require.NoError(t, err)
	merged, err := io.ReadAll(body)
	require.NoError(t, err)
	_ = body.Close()
	assert.ElementsMatch(t, contents(t, nextTar), contents(t, merged))

	// A conversation that never had a workspace still starts from the base.
	row, body, err = svc.Open(ctx, spec.WorkspacePlaybookKey("sess_new", "coder"), repo)
	require.NoError(t, err)
	_ = body.Close()
	assert.Equal(t, "", row.SessionID)
}
