package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// Audit actions written by the control plane. They are the strings that end up in
// audit_log.action, so they are contractual once written.
const (
	ActionSecretSet     = "secret.set"
	ActionSecretDelete  = "secret.delete"
	ActionSecretResolve = "secret.resolve"
	ActionSecretRotate  = "secret.rotate"

	ActionRegistrySet     = "registry.set"
	ActionRegistryDelete  = "registry.delete"
	ActionRegistryResolve = "registry.resolve"
)

// DefaultAuditLimit is how many rows ListAudit returns when the caller asks for none.
const DefaultAuditLimit = 100

// AuditEntry is one row of audit_log. Details is arbitrary JSON and must never carry a
// secret value: a secret audit records names, versions and counts only.
type AuditEntry struct {
	ID      int64
	TS      time.Time
	Actor   string
	Action  string
	Subject string
	Details map[string]any
}

// Audit records one action. It is deliberately not fatal to its caller — an audit write
// that fails must not stop a task from running — so callers log the error and continue.
func (s *Store) Audit(ctx context.Context, actor, action, subject string, details map[string]any) error {
	if action == "" {
		return fmt.Errorf("audit: action is required")
	}
	payload := []byte("{}")
	if len(details) > 0 {
		encoded, err := json.Marshal(details)
		if err != nil {
			return fmt.Errorf("audit %s: marshal details: %w", action, err)
		}
		payload = encoded
	}
	if _, err := s.q.AppendAudit(ctx, db.AppendAuditParams{
		Actor: actor, Action: action, Subject: subject, Details: payload,
	}); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

// ListAudit returns the most recent entries, newest first, optionally narrowed to one
// action and one subject. An empty action or subject means "any".
func (s *Store) ListAudit(ctx context.Context, action, subject string, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = DefaultAuditLimit
	}
	rows, err := s.q.ListAudit(ctx, db.ListAuditParams{
		Action: action, Subject: subject, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	out := make([]AuditEntry, 0, len(rows))
	for _, row := range rows {
		entry := AuditEntry{
			ID:      row.ID,
			TS:      row.Ts.UTC(),
			Actor:   row.Actor,
			Action:  row.Action,
			Subject: row.Subject,
		}
		if len(row.Details) > 0 {
			if err := json.Unmarshal(row.Details, &entry.Details); err != nil {
				return nil, fmt.Errorf("list audit: decode details of %d: %w", row.ID, err)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}
