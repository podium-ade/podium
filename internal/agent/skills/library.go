package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Where a skill's bytes came from. The strings are what the API reports and what the UI
// labels a row with; they are the same two words a playbook's origin uses.
const (
	// OriginDir is a directory under PODIUM_AGENT_SKILLS_DIR on the conductor's own host.
	OriginDir = "dir"
	// OriginStored is a bundle uploaded through the API and held in the conductor's database.
	OriginStored = "stored"
)

// Stored is one skill in the conductor's database. Document is empty in a listing and set
// only when a turn is about to be delivered one: a list of twenty skills has no business
// carrying twenty bundles.
type Stored struct {
	Name        string
	Description string
	SHA256      string
	SizeBytes   int64
	FileCount   int
	Enabled     bool
	UploadedBy  string
	UploadedAt  time.Time
	Document    []byte
}

// Reader is the conductor's database, as much of it as a Library needs.
//
// It is an interface rather than *store.Store because internal/agent/store imports
// internal/agent/profiles, which imports this package: the dependency only goes one way, and
// a Library that named the store directly would close the loop.
type Reader interface {
	// StoredSkill returns one skill with its bundle document. A name that is not stored
	// returns an error satisfying errors.Is(err, ErrNotStored).
	StoredSkill(ctx context.Context, name string) (Stored, error)
	// ListStoredSkills returns every stored skill without its bundle, sorted by name.
	ListStoredSkills(ctx context.Context) ([]Stored, error)
}

// ErrNotStored is what a Reader returns for a name it does not hold. It is this package's so
// that the fallback below can be written without importing the store.
var ErrNotStored = errors.New("no stored skill of that name")

// Library is the two places a skill can come from, and the order they are tried in.
//
// **The directory wins.** A skill under PODIUM_AGENT_SKILLS_DIR is a file on the conductor's
// host, put there by whoever runs the process, and it is the escape hatch for exactly the
// case where the database or the UI is not available — so it cannot be overridden from a
// browser. That is the same rule a playbooks/<name>.yaml gets, for the same reason, and a
// stored skill whose name a directory also holds is reported as *shadowed* rather than
// quietly ignored: it never runs, and deleting it is the only way to make the list honest.
//
// Dir may be empty, and Store may be nil. A Library with neither delivers nothing and says
// so; a playbook that names no skills never asks it anything.
type Library struct {
	Dir   string
	Store Reader
}

// Bundles reads and packs the named skills, in the order given.
//
// One failure fails the whole set, which is Phase 2's rule and stays: a turn that ran with
// three of its four skills would answer differently from the playbook it claims to be, and
// nobody watching would be able to tell which of the two had happened.
func (l Library) Bundles(ctx context.Context, names []string) ([]Bundle, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > MaxSkills {
		return nil, fmt.Errorf("%d skills are declared; the limit is %d per playbook", len(names), MaxSkills)
	}
	if l.Dir == "" && l.Store == nil {
		return nil, fmt.Errorf("this conductor has no skills: %s is not set and it has no database",
			DirEnv)
	}
	out := make([]Bundle, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("skill %q is declared twice", name)
		}
		seen[name] = true
		b, err := l.bundle(ctx, name)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// bundle resolves one name. The directory is tried first and only a name that is not there
// at all falls through to the database: a directory entry that exists and cannot be loaded —
// a symlink, a directory with no SKILL.md — is an error rather than a reason to serve
// something else under the same name.
func (l Library) bundle(ctx context.Context, name string) (Bundle, error) {
	if err := ValidateName(name); err != nil {
		return Bundle{}, err
	}
	if l.Dir != "" {
		if _, err := os.Lstat(filepath.Join(l.Dir, name)); err == nil {
			return Load(l.Dir, name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Bundle{}, fmt.Errorf("skill %q: %w", name, err)
		}
	}
	if l.Store == nil {
		return Bundle{}, fmt.Errorf("no skill named %q is in %s=%q, and this conductor has no database",
			name, DirEnv, l.Dir)
	}
	row, err := l.Store.StoredSkill(ctx, name)
	switch {
	case errors.Is(err, ErrNotStored):
		return Bundle{}, fmt.Errorf("no skill named %q: it is neither uploaded nor a directory in %s=%q",
			name, DirEnv, l.Dir)
	case err != nil:
		return Bundle{}, fmt.Errorf("skill %q: %w", name, err)
	case !row.Enabled:
		// Disabled fails the turn rather than being skipped, by the same rule as everything
		// else here. "Disabled" is a statement about the skill, not about this playbook, and
		// the playbook still says it needs it.
		return Bundle{}, fmt.Errorf("skill %q is uploaded but disabled; enable it or take it out "+
			"of the playbook", name)
	}
	b, err := FromDocument(name, row.Document)
	if err != nil {
		return Bundle{}, err
	}
	if b.SHA256 != row.SHA256 {
		// The row's digest is recorded at upload and shown in the UI. If repacking the
		// stored document does not reproduce it, the row and the bytes disagree and neither
		// is trustworthy.
		return Bundle{}, fmt.Errorf("skill %q: the stored bundle hashes to %s and the row says %s",
			name, b.SHA256, row.SHA256)
	}
	return b, nil
}

// DirSkill is one directory under PODIUM_AGENT_SKILLS_DIR as a listing sees it. Problem is
// set for a directory that is there and does not load; it is reported rather than omitted,
// because a skill an operator can see in a shell and cannot see in the UI is the worst
// version of this.
type DirSkill struct {
	Name        string
	Description string
	SizeBytes   int64
	FileCount   int
	Problem     string
}

// ListDir reads every skill directory in dir. An unset or absent directory is not an error:
// a conductor that delivers no skills has nothing to list.
func ListDir(dir string) ([]DirSkill, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s=%q: %w", DirEnv, dir, err)
	}
	out := make([]DirSkill, 0, len(entries))
	for _, e := range entries {
		// A file beside the skill directories — a README, a .gitignore — is not a broken
		// skill and is not reported as one.
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if ValidateName(name) != nil {
			out = append(out, DirSkill{Name: name, Problem: fmt.Sprintf(
				"the directory name must match %s, so the harness would never load it", NameRE)})
			continue
		}
		b, err := Load(dir, name)
		if err != nil {
			out = append(out, DirSkill{Name: name, Problem: err.Error()})
			continue
		}
		out = append(out, DirSkill{
			Name:        name,
			Description: b.Description,
			SizeBytes:   int64(len(b.Document)),
			FileCount:   b.Files,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
