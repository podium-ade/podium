package store

import (
	"context"
	"fmt"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
	"github.com/podium-ade/podium/internal/ids"
)

// SetTurnCredential records what paid for a turn. It is written once the conductor has
// decided, which is after the row exists: whose credential a turn spends depends on who asked
// and whether the conversation is shared, and both are worked out as the turn starts.
func (s *Store) SetTurnCredential(ctx context.Context, turnID string, c Credential) error {
	if err := s.q.SetTurnCredential(ctx, db.SetTurnCredentialParams{
		AuthKind: c.AuthKind, CredentialOwner: c.Owner, CredentialAccount: c.Account, ID: turnID,
	}); err != nil {
		return fmt.Errorf("set credential on turn %s: %w", turnID, err)
	}
	return nil
}

// SetDelegationCredential is SetTurnCredential for a delegated task.
func (s *Store) SetDelegationCredential(ctx context.Context, delegationID string, c Credential) error {
	if err := s.q.SetDelegationCredential(ctx, db.SetDelegationCredentialParams{
		AuthKind: c.AuthKind, CredentialOwner: c.Owner, CredentialAccount: c.Account, ID: delegationID,
	}); err != nil {
		return fmt.Errorf("set credential on delegation %s: %w", delegationID, err)
	}
	return nil
}

// SetTurnTokens records the tokens a turn's runtime reported.
func (s *Store) SetTurnTokens(ctx context.Context, turnID string, t Tokens) error {
	if err := s.q.SetTurnTokens(ctx, db.SetTurnTokensParams{
		InputTokens: t.Input, OutputTokens: t.Output, ReasoningTokens: t.Reasoning,
		CacheReadTokens: t.CacheRead, CacheWriteTokens: t.CacheWrite, ID: turnID,
	}); err != nil {
		return fmt.Errorf("set tokens on turn %s: %w", turnID, err)
	}
	return nil
}

// SetDelegationTokens is SetTurnTokens for a delegated task.
func (s *Store) SetDelegationTokens(ctx context.Context, delegationID string, t Tokens) error {
	if err := s.q.SetDelegationTokens(ctx, db.SetDelegationTokensParams{
		InputTokens: t.Input, OutputTokens: t.Output, ReasoningTokens: t.Reasoning,
		CacheReadTokens: t.CacheRead, CacheWriteTokens: t.CacheWrite, ID: delegationID,
	}); err != nil {
		return fmt.Errorf("set tokens on delegation %s: %w", delegationID, err)
	}
	return nil
}

// Housekeeping is one model call the conductor made for itself rather than for a turn.
type Housekeeping struct {
	SessionID  string
	Purpose    string
	Status     string
	StartedAt  time.Time
	FinishedAt time.Time
	NumTurns   *int
	CostUSD    *float64
	Backend    Backend
	Credential Credential
	Tokens     Tokens
}

// RecordHousekeeping writes h to housekeeping_spend, which usage_rows reads beside turns and
// delegations.
func (s *Store) RecordHousekeeping(ctx context.Context, h Housekeeping) error {
	var steps *int32
	if h.NumTurns != nil {
		v := int32(*h.NumTurns)
		steps = &v
	}
	finished := h.FinishedAt.UTC()
	if err := s.q.RecordHousekeeping(ctx, db.RecordHousekeepingParams{
		ID:                ids.New("hk"),
		SessionID:         h.SessionID,
		Purpose:           h.Purpose,
		Status:            h.Status,
		StartedAt:         h.StartedAt.UTC(),
		FinishedAt:        &finished,
		NumTurns:          steps,
		CostUsd:           h.CostUSD,
		Agent:             h.Backend.Agent,
		Model:             h.Backend.Model,
		Effort:            h.Backend.Effort,
		Provider:          h.Backend.Provider,
		AuthKind:          h.Credential.AuthKind,
		CredentialOwner:   h.Credential.Owner,
		CredentialAccount: h.Credential.Account,
		InputTokens:       h.Tokens.Input,
		OutputTokens:      h.Tokens.Output,
		ReasoningTokens:   h.Tokens.Reasoning,
		CacheReadTokens:   h.Tokens.CacheRead,
		CacheWriteTokens:  h.Tokens.CacheWrite,
	}); err != nil {
		return fmt.Errorf("record %s spend for session %s: %w", h.Purpose, h.SessionID, err)
	}
	return nil
}
