package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Library is the Agent Skills on this conductor's host: one directory per skill under
// PODIUM_AGENT_SKILLS_DIR. A playbook that names no skills never asks it anything.
//
// Dir may be empty. A Library with no directory delivers nothing and says so.
type Library struct {
	Dir string
}

// Bundles reads and packs the named skills, in the order given.
//
// One failure fails the whole set: a turn that ran with three of its four skills would
// answer differently from the playbook it claims to be, and nobody watching would be able
// to tell which of the two had happened.
func (l Library) Bundles(_ context.Context, names []string) ([]Bundle, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > MaxSkills {
		return nil, fmt.Errorf("%d skills are declared; the limit is %d per playbook", len(names), MaxSkills)
	}
	if l.Dir == "" {
		return nil, fmt.Errorf("this conductor has no skills: %s is not set", DirEnv)
	}
	out := make([]Bundle, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("skill %q is declared twice", name)
		}
		seen[name] = true
		b, err := l.bundle(name)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (l Library) bundle(name string) (Bundle, error) {
	if err := ValidateName(name); err != nil {
		return Bundle{}, err
	}
	_, err := os.Lstat(filepath.Join(l.Dir, name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Bundle{}, fmt.Errorf("no skill named %q in %s=%q", name, DirEnv, l.Dir)
	case err != nil:
		return Bundle{}, fmt.Errorf("skill %q: %w", name, err)
	}
	return Load(l.Dir, name)
}

// DirSkill is one directory under PODIUM_AGENT_SKILLS_DIR as a listing sees it. Problem is
// set for a directory that is there and does not load; it is reported rather than omitted,
// because a skill an operator can see in a shell and cannot see in the UI is the worst
// version of this.
type DirSkill struct {
	Name        string
	Description string
	SHA256      string
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
			SHA256:      b.SHA256,
			SizeBytes:   int64(len(b.Document)),
			FileCount:   b.Files,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
