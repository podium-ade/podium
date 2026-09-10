package store

import (
	"context"
	"fmt"
	"time"

	"github.com/podium-ade/podium/internal/agent/skills"
	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// The conductor's half of the skill library: the bundles somebody uploaded through the API.
// The other half is PODIUM_AGENT_SKILLS_DIR on the host, which this package knows nothing
// about — skills.Library is what puts the two together, and the directory wins.
//
// The methods below are the whole of skills.Reader, so *Store satisfies it without either
// package importing the other's concrete types.

// ListStoredSkills returns every uploaded skill, sorted by name, without its bundle.
func (s *Store) ListStoredSkills(ctx context.Context) ([]skills.Stored, error) {
	rows, err := s.q.ListStoredSkills(ctx)
	if err != nil {
		return nil, fmt.Errorf("list stored skills: %w", err)
	}
	out := make([]skills.Stored, 0, len(rows))
	for _, r := range rows {
		out = append(out, skills.Stored{
			Name:        r.Name,
			Description: r.Description,
			SHA256:      r.Sha256,
			SizeBytes:   r.SizeBytes,
			FileCount:   int(r.FileCount),
			Enabled:     r.Enabled,
			UploadedBy:  r.UploadedBy,
			UploadedAt:  r.UploadedAt.UTC(),
		})
	}
	return out, nil
}

// StoredSkill reads one uploaded skill with its bundle document. A name that is not stored
// is skills.ErrNotStored, which is what lets skills.Library fall through without knowing
// anything about this package.
func (s *Store) StoredSkill(ctx context.Context, name string) (skills.Stored, error) {
	row, err := s.q.GetStoredSkill(ctx, name)
	if noRows(err) {
		return skills.Stored{}, fmt.Errorf("%w: %s", skills.ErrNotStored, name)
	}
	if err != nil {
		return skills.Stored{}, fmt.Errorf("get stored skill %s: %w", name, err)
	}
	return skills.Stored{
		Name:        row.Name,
		Description: row.Description,
		SHA256:      row.Sha256,
		SizeBytes:   row.SizeBytes,
		FileCount:   int(row.FileCount),
		Enabled:     row.Enabled,
		UploadedBy:  row.UploadedBy,
		UploadedAt:  row.UploadedAt.UTC(),
		Document:    row.Document,
	}, nil
}

// InsertStoredSkill writes a new skill. ErrConflict means the name is already stored, which
// the caller turns into "pass replace to overwrite it" rather than overwriting silently.
func (s *Store) InsertStoredSkill(ctx context.Context, b skills.Bundle, login string) error {
	n, err := s.q.InsertStoredSkill(ctx, db.InsertStoredSkillParams{
		Name:        b.Name,
		Description: b.Description,
		Sha256:      b.SHA256,
		SizeBytes:   int64(len(b.Document)),
		FileCount:   int32(b.Files),
		Document:    b.Document,
		UploadedBy:  login,
		UploadedAt:  time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("insert skill %s: %w", b.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: skill %s", ErrConflict, b.Name)
	}
	return nil
}

// ReplaceStoredSkill overwrites a stored skill's bundle, leaving `enabled` alone.
// ErrNotFound means there was none to replace.
func (s *Store) ReplaceStoredSkill(ctx context.Context, b skills.Bundle, login string) error {
	n, err := s.q.ReplaceStoredSkill(ctx, db.ReplaceStoredSkillParams{
		Name:        b.Name,
		Description: b.Description,
		Sha256:      b.SHA256,
		SizeBytes:   int64(len(b.Document)),
		FileCount:   int32(b.Files),
		Document:    b.Document,
		UploadedBy:  login,
		UploadedAt:  time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("replace skill %s: %w", b.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: skill %s", ErrNotFound, b.Name)
	}
	return nil
}

// SetStoredSkillEnabled turns one skill on or off. ErrNotFound means it is not stored —
// which the caller needs, because a directory skill of the same name is a different answer
// than nothing, and a directory skill cannot be disabled from a browser at all.
func (s *Store) SetStoredSkillEnabled(ctx context.Context, name string, enabled bool) error {
	n, err := s.q.SetStoredSkillEnabled(ctx, db.SetStoredSkillEnabledParams{Name: name, Enabled: enabled})
	if err != nil {
		return fmt.Errorf("set skill %s enabled=%v: %w", name, enabled, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: skill %s", ErrNotFound, name)
	}
	return nil
}

// DeleteStoredSkill removes one. ErrNotFound means it was not stored.
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
