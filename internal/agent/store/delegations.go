package store

// The delegations table: a task a host turn asked for. See migration 0008 for why the row
// exists at all — the task outlives the turn, so something has to own it.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	db "github.com/alvaroibarguen/podium/internal/agent/store/db"
	"github.com/alvaroibarguen/podium/internal/ids"
)

// Delegation is one delegated task. Status shares the turns table's vocabulary, so the same
// classify() maps a terminal task onto either.
type Delegation struct {
	ID          string
	SessionID   string
	TurnID      string
	TriggerRef  string
	Playbook    string
	Instruction string
	TaskID      string
	Status      string
	FinalText   string
	CreatedAt   time.Time
	FinishedAt  *time.Time
	// Backend is what ran it, recorded as the row is written. Zero for a delegation from
	// before the columns existed.
	Backend Backend
	// NumTurns and CostUSD are what the task reported spending, nil when its accounting
	// never arrived. A conversation's spend is almost entirely here rather than on its
	// turns: the assistant answers on the host and the container does the work.
	NumTurns *int
	CostUSD  *float64
}

// NewDelegation is what a caller has to know to record one.
type NewDelegation struct {
	SessionID   string
	TurnID      string
	TriggerRef  string
	Playbook    string
	Instruction string
	// Backend is what this task will run on, resolved by the caller from the playbook. It is
	// recorded now rather than derived later, for the same reason a turn's is: a playbook's
	// model is a default, and editing it would otherwise relabel every delegation that ever
	// ran under it.
	Backend Backend
}

// CreateDelegation records a delegation before its task exists, so a task the control plane
// accepts is never one this database has never heard of. SetDelegationTask fills in the id.
func (s *Store) CreateDelegation(ctx context.Context, want NewDelegation) (Delegation, error) {
	row, err := s.q.CreateDelegation(ctx, db.CreateDelegationParams{
		ID:          ids.New("dlg"),
		SessionID:   want.SessionID,
		TurnID:      want.TurnID,
		TriggerRef:  want.TriggerRef,
		Playbook:    want.Playbook,
		Instruction: want.Instruction,
		Status:      TurnRunning,
		CreatedAt:   time.Now().UTC(),
		Agent:       nilIfEmpty(want.Backend.Agent),
		Model:       nilIfEmpty(want.Backend.Model),
		Effort:      nilIfEmpty(want.Backend.Effort),
		Provider:    nilIfEmpty(want.Backend.Provider),
	})
	if err != nil {
		return Delegation{}, fmt.Errorf("create delegation for turn %s: %w", want.TurnID, err)
	}
	return delegationFromRow(row), nil
}

// SetDelegationTask binds a delegation to the task now running it.
func (s *Store) SetDelegationTask(ctx context.Context, id, taskID string) error {
	if err := s.q.SetDelegationTask(ctx, db.SetDelegationTaskParams{TaskID: &taskID, ID: id}); err != nil {
		return fmt.Errorf("set task %s on delegation %s: %w", taskID, id, err)
	}
	return nil
}

// FinishDelegation records how a delegated task ended. finalText is what the task actually
// said, which may be empty; numTurns and costUSD are nil when its accounting never arrived.
func (s *Store) FinishDelegation(
	ctx context.Context, id, status, finalText string, numTurns *int, costUSD *float64,
) error {
	now := time.Now().UTC()
	var text *string
	if finalText != "" {
		text = &finalText
	}
	var turns *int32
	if numTurns != nil {
		v := int32(*numTurns)
		turns = &v
	}
	if err := s.q.FinishDelegation(ctx, db.FinishDelegationParams{
		Status:     status,
		FinishedAt: &now,
		FinalText:  text,
		NumTurns:   turns,
		CostUsd:    costUSD,
		ID:         id,
	}); err != nil {
		return fmt.Errorf("finish delegation %s: %w", id, err)
	}
	return nil
}

// GetDelegation reads one by id. ErrNotFound when there is no such delegation.
func (s *Store) GetDelegation(ctx context.Context, id string) (Delegation, error) {
	row, err := s.q.GetDelegation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Delegation{}, ErrNotFound
	}
	if err != nil {
		return Delegation{}, fmt.Errorf("get delegation %s: %w", id, err)
	}
	return delegationFromRow(row), nil
}

// DelegationsForTurn is everything one turn delegated, oldest first.
func (s *Store) DelegationsForTurn(ctx context.Context, turnID string) ([]Delegation, error) {
	rows, err := s.q.ListDelegationsForTurn(ctx, turnID)
	if err != nil {
		return nil, fmt.Errorf("list delegations for turn %s: %w", turnID, err)
	}
	return delegationsFromRows(rows), nil
}

// RunningDelegations is every delegated task still in flight, for the recovery pass on
// start. A delegation's task runs on a NODE, so it survives this process dying — which is
// exactly why it has to be picked up again rather than abandoned.
func (s *Store) RunningDelegations(ctx context.Context) ([]Delegation, error) {
	rows, err := s.q.ListRunningDelegations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list running delegations: %w", err)
	}
	return delegationsFromRows(rows), nil
}

// RunningDelegationsForRef is the in-flight delegations of one conversation, which is what
// deleting a chat has to stop.
func (s *Store) RunningDelegationsForRef(ctx context.Context, ref string) ([]Delegation, error) {
	rows, err := s.q.ListRunningDelegationsForRef(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("list running delegations for %s: %w", ref, err)
	}
	return delegationsFromRows(rows), nil
}

func delegationsFromRows(rows []db.Delegation) []Delegation {
	out := make([]Delegation, 0, len(rows))
	for _, r := range rows {
		out = append(out, delegationFromRow(r))
	}
	return out
}

func delegationFromRow(r db.Delegation) Delegation {
	return Delegation{
		ID:          r.ID,
		SessionID:   r.SessionID,
		TurnID:      r.TurnID,
		TriggerRef:  r.TriggerRef,
		Playbook:    r.Playbook,
		Instruction: r.Instruction,
		TaskID:      deref(r.TaskID),
		Status:      r.Status,
		FinalText:   deref(r.FinalText),
		CreatedAt:   r.CreatedAt.UTC(),
		FinishedAt:  utcPtr(r.FinishedAt),
		Backend: Backend{
			Agent:    deref(r.Agent),
			Model:    deref(r.Model),
			Effort:   deref(r.Effort),
			Provider: deref(r.Provider),
		},
		NumTurns: intPtr(r.NumTurns),
		CostUSD:  r.CostUsd,
	}
}

// nilIfEmpty keeps an unrecorded backend null rather than storing four empty strings, so
// "nobody wrote this down" and "this ran on a model with no name" stay different rows.
func nilIfEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func intPtr(v *int32) *int {
	if v == nil {
		return nil
	}
	out := int(*v)
	return &out
}
