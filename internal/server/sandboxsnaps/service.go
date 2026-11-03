// Package sandboxsnaps keeps the session workspaces a sandbox provider stores as images of
// its own (a Modal directory snapshot) instead of as tars in the object store. Podium holds
// only each image's ID: the row here says which image a session resumes from, and the sweep
// deletes the ones the retention policy no longer keeps, and the ones a newer image replaced.
package sandboxsnaps

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/server/store"
)

// SweepInterval is how often the policy is applied and retired images are deleted.
const SweepInterval = 10 * time.Minute

// TTLMargin is how long after the policy's age a provider drops an image on its own: a
// backstop for a server that is down, never what normally deletes one.
const TTLMargin = 24 * time.Hour

// ErrReplaced is returned when a newer image was stored while one was being deleted.
var ErrReplaced = errors.New("sandbox snapshots: a newer snapshot was stored while deleting; nothing was deleted")

// Deleter deletes a provider's images. Close releases it.
type Deleter interface {
	DeleteSnapshot(ctx context.Context, image string) error
	Close()
}

// Providers are what the service reaches providers through: which configured ones keep
// workspaces as images, and a deleter for one of them.
type Providers interface {
	Snapshotters(ctx context.Context) ([]string, error)
	Deleter(ctx context.Context, provider string) (Deleter, error)
}

// rows is the store half. *store.Store implements it.
type rows interface {
	PutSandboxSnapshot(ctx context.Context, in store.SandboxSnapshot) error
	GetSandboxSnapshot(ctx context.Context, sessionID string) (store.SandboxSnapshot, error)
	ListSandboxSnapshots(ctx context.Context) ([]store.SandboxSnapshot, error)
	DropSandboxSnapshot(ctx context.Context, row store.SandboxSnapshot, retire bool) (bool, error)
	ListRetiredSandboxSnapshots(ctx context.Context) ([]store.RetiredSandboxSnapshot, error)
	ForgetRetiredSandboxSnapshot(ctx context.Context, imageID string) error
	FailRetiredSandboxSnapshot(ctx context.Context, imageID, msg string) error
	GetSandboxSnapshotRetention(ctx context.Context) (store.SandboxSnapshotRetention, error)
	SetSandboxSnapshotRetention(ctx context.Context, in store.SandboxSnapshotRetention) (store.SandboxSnapshotRetention, error)
	Audit(ctx context.Context, actor, action, subject string, details map[string]any) error
}

// Service is the sandbox snapshot rows, their policy and their sweep.
type Service struct {
	store     rows
	providers Providers
	logger    *slog.Logger
	now       func() time.Time

	// sweeping keeps the ticker, a policy change and a delete from sweeping at once.
	sweeping sync.Mutex
	// wake asks Run to delete retired images now rather than on the next tick.
	wake chan struct{}
}

// New returns a Service.
func New(st rows, providers Providers, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, providers: providers, logger: logger, now: time.Now, wake: make(chan struct{}, 1)}
}

// Get is a session's image and the lifetime a new one is given. found is false when the
// session has none.
func (s *Service) Get(ctx context.Context, sessionID string) (row store.SandboxSnapshot, ttl time.Duration, found bool, err error) {
	policy, err := s.store.GetSandboxSnapshotRetention(ctx)
	if err != nil {
		return row, 0, false, err
	}
	ttl = TTLOf(policy)
	row, err = s.store.GetSandboxSnapshot(ctx, sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return row, ttl, false, nil
	}
	return row, ttl, err == nil, err
}

// TTLOf is the lifetime a provider gives a new image under policy: its age and a margin, or
// never (-1) when there is no age rule.
func TTLOf(policy store.SandboxSnapshotRetention) time.Duration {
	if policy.MaxAge <= 0 {
		return -1
	}
	return policy.MaxAge + TTLMargin
}

// Put records a session's new image. The image it replaced is retired, and deleted straight
// after in the background.
func (s *Service) Put(ctx context.Context, in store.SandboxSnapshot) error {
	if err := s.store.PutSandboxSnapshot(ctx, in); err != nil {
		return err
	}
	s.kick()
	return nil
}

// Forget drops a session's row for an image the provider no longer has, without asking the
// provider to delete it.
func (s *Service) Forget(ctx context.Context, sessionID, imageID string) error {
	_, err := s.store.DropSandboxSnapshot(ctx, store.SandboxSnapshot{SessionID: sessionID, ImageID: imageID}, false)
	return err
}

// List returns every session's image, newest first.
func (s *Service) List(ctx context.Context) ([]store.SandboxSnapshot, error) {
	return s.store.ListSandboxSnapshots(ctx)
}

// Providers is the configured providers that keep workspaces as images.
func (s *Service) Providers(ctx context.Context) ([]string, error) {
	if s.providers == nil {
		return nil, nil
	}
	return s.providers.Snapshotters(ctx)
}

// Retention returns the policy the sweep applies.
func (s *Service) Retention(ctx context.Context) (store.SandboxSnapshotRetention, error) {
	return s.store.GetSandboxSnapshotRetention(ctx)
}

// SetRetention replaces the policy and applies it straight away.
func (s *Service) SetRetention(ctx context.Context, actor string, in store.SandboxSnapshotRetention) (store.SandboxSnapshotRetention, int, error) {
	in.UpdatedBy = actor
	out, err := s.store.SetSandboxSnapshotRetention(ctx, in)
	if err != nil {
		return store.SandboxSnapshotRetention{}, 0, err
	}
	s.audit(ctx, actor, "sandbox_snapshot.retention.set", "", map[string]any{
		"max_age_seconds": int64(out.MaxAge / time.Second),
		"max_count":       out.MaxCount,
	})
	removed, err := s.Sweep(ctx)
	return out, removed, err
}

// Delete deletes the images of the sessions named. Each row goes first, so nothing resumes
// from an image that is about to go; the images are then deleted at the provider before
// this returns. failed is the sessions that could not be, with why.
func (s *Service) Delete(ctx context.Context, actor string, sessionIDs []string) (removed int, failed map[string]string, err error) {
	failed = map[string]string{}
	for _, id := range sessionIDs {
		row, err := s.store.GetSandboxSnapshot(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				failed[id] = "no snapshot"
				continue
			}
			return removed, failed, err
		}
		dropped, err := s.store.DropSandboxSnapshot(ctx, row, true)
		if err != nil {
			return removed, failed, err
		}
		if !dropped {
			failed[id] = ErrReplaced.Error()
			continue
		}
		removed++
		s.audit(ctx, actor, "sandbox_snapshot.delete", id, map[string]any{"provider": row.Provider, "image_id": row.ImageID})
	}
	if removed > 0 {
		s.sweeping.Lock()
		s.deleteRetired(ctx)
		s.sweeping.Unlock()
	}
	return removed, failed, nil
}

// Sweep retires the images the policy no longer keeps — older than MaxAge, and, walking
// newest first, every one past MaxCount — then deletes every retired image at its provider.
// One whose session has an unfinished task is never retired, but it still counts.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	s.sweeping.Lock()
	defer s.sweeping.Unlock()
	defer s.deleteRetired(ctx)

	policy, err := s.store.GetSandboxSnapshotRetention(ctx)
	if err != nil {
		return 0, err
	}
	if policy.MaxAge <= 0 && policy.MaxCount <= 0 {
		return 0, nil
	}
	rows, err := s.store.ListSandboxSnapshots(ctx)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, row := range Doomed(rows, policy, s.now()) {
		dropped, err := s.store.DropSandboxSnapshot(ctx, row, true)
		if err != nil {
			if ctx.Err() != nil {
				return removed, ctx.Err()
			}
			s.logger.WarnContext(ctx, "retiring a sandbox snapshot failed", "session_id", row.SessionID, "error", err)
			continue
		}
		if dropped {
			removed++
		}
	}
	return removed, nil
}

// Doomed is what the policy no longer keeps of rows, which are newest first.
func Doomed(rows []store.SandboxSnapshot, policy store.SandboxSnapshotRetention, now time.Time) []store.SandboxSnapshot {
	var out []store.SandboxSnapshot
	cutoff := now.Add(-policy.MaxAge)
	var kept int64
	for _, row := range rows {
		expired := policy.MaxAge > 0 && row.UpdatedAt.Before(cutoff)
		over := policy.MaxCount > 0 && kept >= policy.MaxCount
		if row.InUse || (!expired && !over) {
			kept++
			continue
		}
		out = append(out, row)
	}
	return out
}

// deleteRetired asks each provider to delete its retired images. One it cannot reach, or
// that fails, stays retired and is tried on the next sweep. The caller holds sweeping.
func (s *Service) deleteRetired(ctx context.Context) {
	retired, err := s.store.ListRetiredSandboxSnapshots(ctx)
	if err != nil || len(retired) == 0 {
		if err != nil && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "listing retired sandbox snapshots failed", "error", err)
		}
		return
	}
	deleters := map[string]Deleter{}
	defer func() {
		for _, d := range deleters {
			if d != nil {
				d.Close()
			}
		}
	}()
	for _, r := range retired {
		d, ok := deleters[r.Provider]
		if !ok {
			if s.providers != nil {
				var derr error
				if d, derr = s.providers.Deleter(ctx, r.Provider); derr != nil {
					s.logger.WarnContext(ctx, "no way to delete a sandbox provider's snapshots; they wait",
						"provider", r.Provider, "error", derr)
				}
			}
			deleters[r.Provider] = d
		}
		if d == nil {
			continue
		}
		if err := d.DeleteSnapshot(ctx, r.ImageID); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.WarnContext(ctx, "deleting a sandbox snapshot failed; it is tried again",
				"provider", r.Provider, "image_id", r.ImageID, "attempts", r.Attempts+1, "error", err)
			if ferr := s.store.FailRetiredSandboxSnapshot(ctx, r.ImageID, err.Error()); ferr != nil {
				s.logger.WarnContext(ctx, "recording a failed sandbox snapshot delete failed", "error", ferr)
			}
			continue
		}
		if err := s.store.ForgetRetiredSandboxSnapshot(ctx, r.ImageID); err != nil {
			s.logger.WarnContext(ctx, "forgetting a deleted sandbox snapshot failed", "image_id", r.ImageID, "error", err)
		}
	}
}

func (s *Service) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run applies the policy every SweepInterval, and deletes retired images whenever Put
// retires one, until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	tick := time.NewTicker(SweepInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.wake:
			s.sweeping.Lock()
			s.deleteRetired(ctx)
			s.sweeping.Unlock()
		case <-tick.C:
			removed, err := s.Sweep(ctx)
			if err != nil && ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "sandbox snapshot sweep failed", "error", err)
			} else if removed > 0 {
				s.logger.InfoContext(ctx, "deleted sandbox snapshots past retention", "snapshots", removed)
			}
		}
	}
}

func (s *Service) audit(ctx context.Context, actor, action, subject string, details map[string]any) {
	if err := s.store.Audit(ctx, actor, action, subject, details); err != nil {
		s.logger.WarnContext(ctx, "writing the audit log failed", "action", action, "error", err)
	}
}
