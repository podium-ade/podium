package store

// Work leases: which conductor is doing a piece of work when several share this database.
// See migration 0030 and internal/agent/conductor/lease.go.

import (
	"context"
	"fmt"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// SessionLease is the lease key of serving a session's turns.
func SessionLease(sessionID string) string { return "session:" + sessionID }

// DelegationLease is the lease key of following a delegated task.
func DelegationLease(delegationID string) string { return "delegation:" + delegationID }

// AcquireLease takes key for owner for ttl, and reports whether owner now holds it: nobody
// held it, its holder stopped renewing, or owner already did.
func (s *Store) AcquireLease(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	got, err := s.q.AcquireLease(ctx, db.AcquireLeaseParams{Key: key, Owner: owner, TtlSeconds: ttl.Seconds()})
	if noRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("acquire the lease on %s: %w", key, err)
	}
	return got == owner, nil
}

// RenewLeases extends owner's leases on keys by ttl.
func (s *Store) RenewLeases(ctx context.Context, owner string, keys []string, ttl time.Duration) error {
	if len(keys) == 0 {
		return nil
	}
	if err := s.q.RenewLeases(ctx, db.RenewLeasesParams{TtlSeconds: ttl.Seconds(), Owner: owner, Keys: keys}); err != nil {
		return fmt.Errorf("renew %d leases: %w", len(keys), err)
	}
	return nil
}

// HandOffToOwner leaves pending for the live conductor other than caller that holds key. When
// that owner has a warm task, nothing is left and its id is returned to inject into instead.
// ok is false when nobody holds key, and the caller should take it itself.
func (s *Store) HandOffToOwner(ctx context.Context, key, caller string, pending []byte) (warmTaskID string, ok bool, err error) {
	warm, err := s.q.HandOffToOwner(ctx, db.HandOffToOwnerParams{Pending: pending, Key: key, Caller: caller})
	if noRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("hand a message for %s to its owner: %w", key, err)
	}
	return warm, true, nil
}

// ReleaseLease lets go of key unless a message was handed off meanwhile, and reports whether
// it did. When it did not, TakePending has the message.
func (s *Store) ReleaseLease(ctx context.Context, key, owner string) (bool, error) {
	n, err := s.q.ReleaseLease(ctx, db.ReleaseLeaseParams{Key: key, Owner: owner})
	if err != nil {
		return false, fmt.Errorf("release the lease on %s: %w", key, err)
	}
	return n > 0, nil
}

// TakePending is the message handed off to owner for key, cleared as it is taken. Nil when
// there is none.
func (s *Store) TakePending(ctx context.Context, key, owner string) ([]byte, error) {
	raw, err := s.q.TakePending(ctx, db.TakePendingParams{Key: key, Owner: owner})
	if noRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("take the message handed to %s: %w", key, err)
	}
	return raw, nil
}

// SetWarmTask records owner's task for key that is waiting for a follow-up, so another
// conductor can inject into it. Empty clears it.
func (s *Store) SetWarmTask(ctx context.Context, key, owner, taskID string) error {
	if err := s.q.SetWarmTask(ctx, db.SetWarmTaskParams{WarmTaskID: taskID, Key: key, Owner: owner}); err != nil {
		return fmt.Errorf("record %s's warm task: %w", key, err)
	}
	return nil
}
