package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/agent/profiles"
	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// ErrConflict is what a write returns when the name it wanted is already taken.
var ErrConflict = errors.New("agent store: already exists")

// StoredPlaybook is one playbook an operator created through the API, with who last wrote it.
// The definition is the same document a playbooks/<name>.yaml holds.
type StoredPlaybook struct {
	Playbook  profiles.Playbook
	UpdatedAt time.Time
	UpdatedBy string
}

// ListStoredPlaybooks returns every stored playbook, sorted by name. A row whose definition no
// longer decodes is an error naming it rather than a silently missing playbook: the conductor
// would otherwise run a profile that quietly lost one.
func (s *Store) ListStoredPlaybooks(ctx context.Context) ([]StoredPlaybook, error) {
	rows, err := s.q.ListStoredPlaybooks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list stored playbooks: %w", err)
	}
	out := make([]StoredPlaybook, 0, len(rows))
	for _, r := range rows {
		var playbook profiles.Playbook
		if err := json.Unmarshal(r.Definition, &playbook); err != nil {
			return nil, fmt.Errorf("decode stored playbook %s: %w", r.Name, err)
		}
		playbook.Name = r.Name
		playbook.Origin = profiles.OriginStored
		out = append(out, StoredPlaybook{Playbook: playbook, UpdatedAt: r.UpdatedAt.UTC(), UpdatedBy: r.UpdatedBy})
	}
	return out, nil
}

// InsertStoredPlaybook writes a new playbook. ErrConflict means the name is already stored.
func (s *Store) InsertStoredPlaybook(ctx context.Context, playbook profiles.Playbook, login string) error {
	raw, err := json.Marshal(playbook)
	if err != nil {
		return fmt.Errorf("encode playbook %s: %w", playbook.Name, err)
	}
	n, err := s.q.InsertStoredPlaybook(ctx, db.InsertStoredPlaybookParams{
		Name: playbook.Name, Definition: raw, UpdatedAt: time.Now().UTC(), UpdatedBy: login,
	})
	if err != nil {
		return fmt.Errorf("insert playbook %s: %w", playbook.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: playbook %s", ErrConflict, playbook.Name)
	}
	return nil
}

// UpdateStoredPlaybook replaces a stored playbook. ErrNotFound means there was none to replace.
func (s *Store) UpdateStoredPlaybook(ctx context.Context, playbook profiles.Playbook, login string) error {
	raw, err := json.Marshal(playbook)
	if err != nil {
		return fmt.Errorf("encode playbook %s: %w", playbook.Name, err)
	}
	n, err := s.q.UpdateStoredPlaybook(ctx, db.UpdateStoredPlaybookParams{
		Name: playbook.Name, Definition: raw, UpdatedAt: time.Now().UTC(), UpdatedBy: login,
	})
	if err != nil {
		return fmt.Errorf("update playbook %s: %w", playbook.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: playbook %s", ErrNotFound, playbook.Name)
	}
	return nil
}

// DeleteStoredPlaybook removes one. ErrNotFound means it was not stored — which the caller
// needs, because a file-defined playbook of the same name is a different answer than nothing.
func (s *Store) DeleteStoredPlaybook(ctx context.Context, name string) error {
	n, err := s.q.DeleteStoredPlaybook(ctx, name)
	if err != nil {
		return fmt.Errorf("delete playbook %s: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: playbook %s", ErrNotFound, name)
	}
	return nil
}
