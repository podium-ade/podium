package sandboxsnaps

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/server/store"
)

var now = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// mem is the store, in memory.
type mem struct {
	mu        sync.Mutex
	rows      map[string]store.SandboxSnapshot
	retired   map[string]store.RetiredSandboxSnapshot
	retention store.SandboxSnapshotRetention
	audits    []string
}

func newMem() *mem {
	return &mem{rows: map[string]store.SandboxSnapshot{}, retired: map[string]store.RetiredSandboxSnapshot{}}
}

func (m *mem) PutSandboxSnapshot(_ context.Context, in store.SandboxSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.rows[in.SessionID]; ok && prev.ImageID != in.ImageID {
		m.retired[prev.ImageID] = store.RetiredSandboxSnapshot{ImageID: prev.ImageID, Provider: prev.Provider}
	}
	if in.UpdatedAt.IsZero() {
		in.UpdatedAt = now
	}
	m.rows[in.SessionID] = in
	return nil
}

func (m *mem) GetSandboxSnapshot(_ context.Context, id string) (store.SandboxSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return r, store.ErrNotFound
	}
	return r, nil
}

func (m *mem) ListSandboxSnapshots(context.Context) ([]store.SandboxSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.SandboxSnapshot, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (m *mem) DropSandboxSnapshot(_ context.Context, row store.SandboxSnapshot, retire bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.rows[row.SessionID]
	if !ok || cur.ImageID != row.ImageID {
		return false, nil
	}
	delete(m.rows, row.SessionID)
	if retire {
		m.retired[row.ImageID] = store.RetiredSandboxSnapshot{ImageID: row.ImageID, Provider: cur.Provider}
	}
	return true, nil
}

func (m *mem) ListRetiredSandboxSnapshots(context.Context) ([]store.RetiredSandboxSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.RetiredSandboxSnapshot, 0, len(m.retired))
	for _, r := range m.retired {
		out = append(out, r)
	}
	return out, nil
}

func (m *mem) ForgetRetiredSandboxSnapshot(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.retired, id)
	return nil
}

func (m *mem) FailRetiredSandboxSnapshot(_ context.Context, id, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.retired[id]
	r.Attempts++
	r.LastError = msg
	m.retired[id] = r
	return nil
}

func (m *mem) GetSandboxSnapshotRetention(context.Context) (store.SandboxSnapshotRetention, error) {
	return m.retention, nil
}

func (m *mem) SetSandboxSnapshotRetention(_ context.Context, in store.SandboxSnapshotRetention) (store.SandboxSnapshotRetention, error) {
	m.retention = in
	return in, nil
}

func (m *mem) Audit(_ context.Context, _, action, subject string, _ map[string]any) error {
	m.audits = append(m.audits, action+" "+subject)
	return nil
}

// fakeProviders deletes images from a set, failing those in fail.
type fakeProviders struct {
	mu      sync.Mutex
	images  map[string]bool
	fail    map[string]bool
	missing bool
	closed  int
}

func (f *fakeProviders) Snapshotters(context.Context) ([]string, error) {
	return []string{"modal"}, nil
}

func (f *fakeProviders) Deleter(context.Context, string) (Deleter, error) {
	if f.missing {
		return nil, errors.New("not configured")
	}
	return deleter{f}, nil
}

type deleter struct{ f *fakeProviders }

func (d deleter) DeleteSnapshot(_ context.Context, image string) error {
	d.f.mu.Lock()
	defer d.f.mu.Unlock()
	if d.f.fail[image] {
		return errors.New("modal is down")
	}
	delete(d.f.images, image)
	return nil
}

func (d deleter) Close() { d.f.closed++ }

func testService(t *testing.T) (*Service, *mem, *fakeProviders) {
	t.Helper()
	m := newMem()
	p := &fakeProviders{images: map[string]bool{}, fail: map[string]bool{}}
	svc := New(m, p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return now }
	return svc, m, p
}

// seed stores an image per session, the first newest, each a day older than the one before.
func seed(t *testing.T, m *mem, p *fakeProviders, sessions ...string) {
	t.Helper()
	for i, id := range sessions {
		img := "im-" + id
		p.images[img] = true
		require.NoError(t, m.PutSandboxSnapshot(context.Background(), store.SandboxSnapshot{
			SessionID: id, Provider: "modal", ImageID: img, UpdatedAt: now.Add(-time.Duration(i) * 24 * time.Hour),
		}))
	}
}

func sessions(m *mem) []string {
	var out []string
	for id := range m.rows {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func TestSweepKeepsEverythingWithNoPolicy(t *testing.T) {
	svc, m, p := testService(t)
	seed(t, m, p, "a", "b", "c")
	removed, err := svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Zero(t, removed)
	assert.Equal(t, []string{"a", "b", "c"}, sessions(m))
	assert.Len(t, p.images, 3)
}

func TestSweepDeletesImagesPastTheAgeAtTheProvider(t *testing.T) {
	svc, m, p := testService(t)
	seed(t, m, p, "a", "b", "c", "d")
	m.retention = store.SandboxSnapshotRetention{MaxAge: 36 * time.Hour}
	removed, err := svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, removed)
	assert.Equal(t, []string{"a", "b"}, sessions(m))
	assert.Equal(t, map[string]bool{"im-a": true, "im-b": true}, p.images, "the provider deleted the images too")
	assert.Empty(t, m.retired)
	assert.Equal(t, 1, p.closed, "and the deleter was released")
}

func TestSweepKeepsTheNewestByCountAndNeverOneInUse(t *testing.T) {
	svc, m, p := testService(t)
	seed(t, m, p, "a", "b", "c", "d")
	r := m.rows["d"]
	r.InUse = true
	m.rows["d"] = r
	m.retention = store.SandboxSnapshotRetention{MaxCount: 2}
	removed, err := svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Equal(t, []string{"a", "b", "d"}, sessions(m), "c went; d is in use, so it stays")
}

func TestAFailedProviderDeleteIsTriedAgain(t *testing.T) {
	svc, m, p := testService(t)
	seed(t, m, p, "a")
	p.fail["im-a"] = true
	removed, failed, err := svc.Delete(context.Background(), "ada", []string{"a", "nope"})
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Equal(t, map[string]string{"nope": "no snapshot"}, failed)
	assert.Empty(t, m.rows, "the row goes first, so nothing resumes from the image")
	require.Contains(t, m.retired, "im-a", "the image waits to be deleted")
	assert.Equal(t, 1, m.retired["im-a"].Attempts)
	assert.Contains(t, m.audits, "sandbox_snapshot.delete a")

	p.fail = nil
	_, err = svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Empty(t, m.retired)
	assert.Empty(t, p.images, "the next sweep deleted it")
}

func TestImagesWaitWhenTheProviderIsNotConfigured(t *testing.T) {
	svc, m, p := testService(t)
	seed(t, m, p, "a")
	p.missing = true
	_, _, err := svc.Delete(context.Background(), "ada", []string{"a"})
	require.NoError(t, err)
	assert.Contains(t, m.retired, "im-a")
	assert.True(t, p.images["im-a"])
}

func TestPutRetiresTheImageItReplaces(t *testing.T) {
	svc, m, p := testService(t)
	seed(t, m, p, "a")
	require.NoError(t, svc.Put(context.Background(), store.SandboxSnapshot{SessionID: "a", Provider: "modal", ImageID: "im-a2"}))
	assert.Equal(t, "im-a2", m.rows["a"].ImageID)
	assert.Contains(t, m.retired, "im-a")
	select {
	case <-svc.wake:
	default:
		t.Fatal("put did not ask for the retired image to be deleted")
	}
}

func TestTTLIsThePolicyAgeWithAMargin(t *testing.T) {
	assert.Equal(t, time.Duration(-1), TTLOf(store.SandboxSnapshotRetention{}), "no age rule is no expiry")
	assert.Equal(t, 7*24*time.Hour+TTLMargin, TTLOf(store.SandboxSnapshotRetention{MaxAge: 7 * 24 * time.Hour}))

	svc, m, p := testService(t)
	seed(t, m, p, "a")
	m.retention = store.SandboxSnapshotRetention{MaxAge: time.Hour}
	row, ttl, found, err := svc.Get(context.Background(), "a")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "im-a", row.ImageID)
	assert.Equal(t, time.Hour+TTLMargin, ttl)
	_, _, found, err = svc.Get(context.Background(), "b")
	require.NoError(t, err)
	assert.False(t, found)
}
