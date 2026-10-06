package snapshots

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/podium-ade/podium/internal/server/artifacts"
	"github.com/podium-ade/podium/internal/server/artifacts/fakes3"
	"github.com/podium-ade/podium/internal/server/store"
)

// mem is the pointer table, in process. failNext makes the following write fail so a
// test can see that the previous object survives.
type mem struct {
	sessions map[string]store.WorkspaceSnapshot
	bases    map[string]store.WorkspaceSnapshot
	failNext bool
}

func newMem() *mem {
	return &mem{
		sessions: map[string]store.WorkspaceSnapshot{},
		bases:    map[string]store.WorkspaceSnapshot{},
	}
}

func (m *mem) PutWorkspaceSnapshot(_ context.Context, in store.WorkspaceSnapshot) (string, error) {
	if m.failNext {
		m.failNext = false
		return "", errors.New("row write failed")
	}
	prev := m.sessions[in.SessionID].ObjectKey
	m.sessions[in.SessionID] = in
	if prev == in.ObjectKey {
		prev = ""
	}
	return prev, nil
}

func (m *mem) GetWorkspaceSnapshot(_ context.Context, sessionID string) (store.WorkspaceSnapshot, error) {
	row, ok := m.sessions[sessionID]
	if !ok {
		return store.WorkspaceSnapshot{}, store.ErrNotFound
	}
	return row, nil
}

func (m *mem) PutWorkspaceBase(_ context.Context, in store.WorkspaceSnapshot) (string, error) {
	if m.failNext {
		m.failNext = false
		return "", errors.New("row write failed")
	}
	prev := m.bases[in.Repo].ObjectKey
	m.bases[in.Repo] = in
	if prev == in.ObjectKey {
		prev = ""
	}
	return prev, nil
}

func (m *mem) GetWorkspaceBase(_ context.Context, repo string) (store.WorkspaceSnapshot, error) {
	row, ok := m.bases[repo]
	if !ok {
		return store.WorkspaceSnapshot{}, store.ErrNotFound
	}
	return row, nil
}

func testService(t *testing.T) (*Service, *mem) {
	t.Helper()
	fake := fakes3.Start(t)
	s3, err := artifacts.NewS3(fake.Config())
	if err != nil {
		t.Fatal(err)
	}
	if err := s3.EnsureBucket(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := newMem()
	return New(rows, s3, nil), rows
}

func TestPutReplacesThePreviousObject(t *testing.T) {
	svc, rows := testService(t)
	ctx := context.Background()

	first, err := svc.Put(ctx, Upload{SessionID: "sess_1", Size: 4, Body: bytes.NewReader([]byte("one!"))})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Put(ctx, Upload{SessionID: "sess_1", Size: 4, Body: bytes.NewReader([]byte("two!"))})
	if err != nil {
		t.Fatal(err)
	}
	if first.ObjectKey == second.ObjectKey {
		t.Fatal("a replacement must be a new object")
	}
	if rows.sessions["sess_1"].ObjectKey != second.ObjectKey {
		t.Fatalf("row points at %s, want %s", rows.sessions["sess_1"].ObjectKey, second.ObjectKey)
	}
	if _, err := svc.s3.Get(ctx, first.ObjectKey); err == nil {
		t.Fatal("the previous object is still in the bucket")
	}

	row, body, err := svc.Open(ctx, "sess_1", "https://github.com/acme/app")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if string(got) != "two!" {
		t.Fatalf("opened %q", got)
	}
	if row.SHA256 != second.SHA256 {
		t.Fatalf("sha %s, want %s", row.SHA256, second.SHA256)
	}
}

func TestFailedRowKeepsThePreviousSnapshot(t *testing.T) {
	svc, rows := testService(t)
	ctx := context.Background()
	first, err := svc.Put(ctx, Upload{SessionID: "sess_1", Size: 3, Body: bytes.NewReader([]byte("old"))})
	if err != nil {
		t.Fatal(err)
	}
	rows.failNext = true
	if _, err := svc.Put(ctx, Upload{SessionID: "sess_1", Size: 3, Body: bytes.NewReader([]byte("new"))}); err == nil {
		t.Fatal("expected the row write to fail")
	}
	if rows.sessions["sess_1"].ObjectKey != first.ObjectKey {
		t.Fatal("the row moved off the last good snapshot")
	}
	body, err := svc.s3.Get(ctx, first.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if string(got) != "old" {
		t.Fatalf("previous object is %q", got)
	}
}

func TestOpenFallsBackToTheBase(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	if _, err := svc.Put(ctx, Upload{Repo: "https://github.com/acme/app", Size: 4, Body: bytes.NewReader([]byte("base"))}); err != nil {
		t.Fatal(err)
	}
	_, body, err := svc.Open(ctx, "sess_new", "https://github.com/acme/app")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if string(got) != "base" {
		t.Fatalf("opened %q", got)
	}
}

func TestOpenMissesWhenNothingIsStored(t *testing.T) {
	svc, _ := testService(t)
	_, _, err := svc.Open(context.Background(), "sess_missing", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
