package store

// Tickets named in a chat, and the panel of an issue's conversation (docs/agent.md, "Tickets in
// a chat"). Read-only: see linear_issues.go for what writes these tables.

import (
	"context"
	"fmt"
	"strings"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// FindLinearIssue is an issue the webhooks described, by identifier ("ENG-12", any case) or by
// Linear id. ErrNotFound when none did.
func (s *Store) FindLinearIssue(ctx context.Context, identifier, linearID string) (LinearIssue, error) {
	identifier, linearID = strings.TrimSpace(identifier), strings.TrimSpace(linearID)
	if identifier == "" && linearID == "" {
		return LinearIssue{}, fmt.Errorf("%w: no Linear issue named", ErrNotFound)
	}
	row, err := s.q.FindLinearIssue(ctx, db.FindLinearIssueParams{Identifier: identifier, LinearID: linearID})
	if noRows(err) {
		return LinearIssue{}, fmt.Errorf("%w: Linear issue %s%s", ErrNotFound, identifier, linearID)
	}
	if err != nil {
		return LinearIssue{}, fmt.Errorf("find Linear issue %s%s: %w", identifier, linearID, err)
	}
	return LinearIssue{
		SourceKey: row.SourceKey, Kind: row.Kind, LinearID: row.LinearID, Identifier: row.Identifier,
		Title: row.Title, URL: row.Url, TeamKey: row.TeamKey, StateName: row.StateName, StateType: row.StateType,
		AskedAt: utcPtr(row.AskedAt), AskedOf: row.AskedOf, UpdatedAt: row.UpdatedAt.UTC(),
	}, nil
}

// LinearIssueTeamKeys is every team an issue the webhooks described is in, upper-case.
func (s *Store) LinearIssueTeamKeys(ctx context.Context) ([]string, error) {
	keys, err := s.q.LinearIssueTeamKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Linear team keys: %w", err)
	}
	return keys, nil
}

// SessionDelegations is the tasks one conversation started, newest first, at most limit.
func (s *Store) SessionDelegations(ctx context.Context, sessionID string, limit int) ([]Delegation, error) {
	rows, err := s.q.ListSessionDelegations(ctx, db.ListSessionDelegationsParams{
		SessionID: sessionID, MaxRows: int32(clampLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list the tasks of session %s: %w", sessionID, err)
	}
	out := make([]Delegation, 0, len(rows))
	for _, r := range rows {
		out = append(out, delegationFromRow(r))
	}
	return out, nil
}

// ChatRoutineRun is a routine's run that happened in a chat other than its own, with the
// routine's name.
type ChatRoutineRun struct {
	RoutineRun
	RoutineName string
}

// ChatRoutineRuns is the routine runs that happened in one chat other than the routine's own —
// a Linear issue's or project's conversation — newest first, at most limit.
func (s *Store) ChatRoutineRuns(ctx context.Context, chatID string, limit int) ([]ChatRoutineRun, error) {
	rows, err := s.q.ListChatRoutineRuns(ctx, db.ListChatRoutineRunsParams{
		ChatID: chatID, MaxRows: int32(clampLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list the routine runs in chat %s: %w", chatID, err)
	}
	out := make([]ChatRoutineRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, ChatRoutineRun{
			RoutineRun: routineRunFromRow(db.RoutineRun{
				ID: r.ID, RoutineID: r.RoutineID, ScheduledFor: r.ScheduledFor, Trigger: r.Trigger,
				Status: r.Status, Summary: r.Summary, Reported: r.Reported, TurnID: r.TurnID,
				CostUsd: r.CostUsd, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt, ChatID: r.ChatID,
			}),
			RoutineName: r.RoutineName,
		})
	}
	return out, nil
}
