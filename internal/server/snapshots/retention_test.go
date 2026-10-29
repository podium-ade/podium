package snapshots

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/server/store"
)

var sweepNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// seed stores a snapshot of size bytes for each session, the first one newest, each a
// day older than the one before.
func seed(t *testing.T, svc *Service, rows *mem, size int, sessions ...string) {
	t.Helper()
	for i, id := range sessions {
		if _, err := svc.Put(context.Background(), Upload{SessionID: id, Size: int64(size), Body: bytes.NewReader(make([]byte, size))}); err != nil {
			t.Fatal(err)
		}
		row := rows.sessions[id]
		row.UpdatedAt = sweepNow.Add(-time.Duration(i) * 24 * time.Hour)
		rows.sessions[id] = row
	}
	svc.now = func() time.Time { return sweepNow }
}

func kept(rows *mem) map[string]bool {
	out := map[string]bool{}
	for id := range rows.sessions {
		out[id] = true
	}
	return out
}

func TestSweepKeepsEverythingWithNoPolicy(t *testing.T) {
	svc, rows := testService(t)
	seed(t, svc, rows, 10, "a", "b", "c")
	swept, err := svc.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if swept.Removed != 0 || len(rows.sessions) != 3 {
		t.Fatalf("removed %d, %d left", swept.Removed, len(rows.sessions))
	}
}

func TestSweepDeletesSnapshotsPastTheAge(t *testing.T) {
	svc, rows := testService(t)
	seed(t, svc, rows, 10, "a", "b", "c", "d")
	old := rows.sessions["d"].ObjectKey
	rows.retention = store.SnapshotRetention{MaxAge: 36 * time.Hour}

	swept, err := svc.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := kept(rows); !got["a"] || !got["b"] || got["c"] || got["d"] {
		t.Fatalf("kept %v, want a and b", got)
	}
	if swept.Removed != 2 || swept.FreedBytes != 20 {
		t.Fatalf("swept %+v", swept)
	}
	if _, err := svc.s3.Get(context.Background(), old); err == nil {
		t.Fatal("the object of a deleted snapshot is still in the bucket")
	}
}

func TestSweepKeepsTheNewestUnderTheTotal(t *testing.T) {
	svc, rows := testService(t)
	seed(t, svc, rows, 10, "a", "b", "c", "d")
	// c would fit after b on its own, but the budget runs out at c, so d goes too:
	// an older snapshot never outlives a newer one.
	rows.retention = store.SnapshotRetention{MaxTotalBytes: 25}

	if _, err := svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := kept(rows); !got["a"] || !got["b"] || got["c"] || got["d"] {
		t.Fatalf("kept %v, want a and b", got)
	}
}

func TestSweepNeverDeletesASnapshotInUse(t *testing.T) {
	svc, rows := testService(t)
	seed(t, svc, rows, 10, "a", "b", "c")
	row := rows.sessions["c"]
	row.InUse = true
	rows.sessions["c"] = row
	rows.retention = store.SnapshotRetention{MaxAge: time.Hour, MaxTotalBytes: 5}

	if _, err := svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := kept(rows); len(got) != 1 || !got["c"] {
		t.Fatalf("kept %v, want only c", got)
	}
}

func TestSetRetentionSweepsStraightAway(t *testing.T) {
	svc, rows := testService(t)
	seed(t, svc, rows, 10, "a", "b")
	_, swept, err := svc.SetRetention(context.Background(), "ada", store.SnapshotRetention{MaxTotalBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if swept.Removed != 1 || !kept(rows)["a"] {
		t.Fatalf("swept %+v, kept %v", swept, kept(rows))
	}
	if rows.retention.UpdatedBy != "ada" {
		t.Fatalf("updated_by %q", rows.retention.UpdatedBy)
	}
}

func TestDeleteRemovesTheRowAndTheObject(t *testing.T) {
	svc, rows := testService(t)
	seed(t, svc, rows, 10, "a")
	key := rows.sessions["a"].ObjectKey
	if err := svc.Delete(context.Background(), "ada", "a"); err != nil {
		t.Fatal(err)
	}
	if len(rows.sessions) != 0 {
		t.Fatal("the row survived")
	}
	if _, err := svc.s3.Get(context.Background(), key); err == nil {
		t.Fatal("the object survived")
	}
	if err := svc.Delete(context.Background(), "ada", "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
