package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	db "github.com/alvaroibarguen/podium/internal/agent/store/db"
)

// ErrConflict is what a write returns when the name it wanted is already taken.
var ErrConflict = errors.New("agent store: already exists")

// StoredSkill is one skill an operator created through the API, with who last wrote it.
// The definition is the same document a skills/<name>.yaml holds.
type StoredSkill struct {
	Skill     profiles.Skill
	UpdatedAt time.Time
	UpdatedBy string
}

// ListStoredSkills returns every stored skill, sorted by name. A row whose definition no
// longer decodes is an error naming it rather than a silently missing skill: the conductor
// would otherwise run a profile that quietly lost one.
func (s *Store) ListStoredSkills(ctx context.Context) ([]StoredSkill, error) {
	rows, err := s.q.ListStoredSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("list stored skills: %w", err)
	}
	out := make([]StoredSkill, 0, len(rows))
	for _, r := range rows {
		var skill profiles.Skill
		if err := json.Unmarshal(r.Definition, &skill); err != nil {
			return nil, fmt.Errorf("decode stored skill %s: %w", r.Name, err)
		}
		skill.Name = r.Name
		skill.Origin = profiles.OriginStored
		out = append(out, StoredSkill{Skill: skill, UpdatedAt: r.UpdatedAt.UTC(), UpdatedBy: r.UpdatedBy})
	}
	return out, nil
}

// InsertStoredSkill writes a new skill. ErrConflict means the name is already stored.
func (s *Store) InsertStoredSkill(ctx context.Context, skill profiles.Skill, login string) error {
	raw, err := json.Marshal(skill)
	if err != nil {
		return fmt.Errorf("encode skill %s: %w", skill.Name, err)
	}
	n, err := s.q.InsertStoredSkill(ctx, db.InsertStoredSkillParams{
		Name: skill.Name, Definition: raw, UpdatedAt: time.Now().UTC(), UpdatedBy: login,
	})
	if err != nil {
		return fmt.Errorf("insert skill %s: %w", skill.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: skill %s", ErrConflict, skill.Name)
	}
	return nil
}

// UpdateStoredSkill replaces a stored skill. ErrNotFound means there was none to replace.
func (s *Store) UpdateStoredSkill(ctx context.Context, skill profiles.Skill, login string) error {
	raw, err := json.Marshal(skill)
	if err != nil {
		return fmt.Errorf("encode skill %s: %w", skill.Name, err)
	}
	n, err := s.q.UpdateStoredSkill(ctx, db.UpdateStoredSkillParams{
		Name: skill.Name, Definition: raw, UpdatedAt: time.Now().UTC(), UpdatedBy: login,
	})
	if err != nil {
		return fmt.Errorf("update skill %s: %w", skill.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: skill %s", ErrNotFound, skill.Name)
	}
	return nil
}

// DeleteStoredSkill removes one. ErrNotFound means it was not stored — which the caller
// needs, because a file-defined skill of the same name is a different answer than nothing.
func (s *Store) DeleteStoredSkill(ctx context.Context, name string) error {
	n, err := s.q.DeleteStoredSkill(ctx, name)
	if err != nil {
		return fmt.Errorf("delete skill %s: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: skill %s", ErrNotFound, name)
	}
	return nil
}
