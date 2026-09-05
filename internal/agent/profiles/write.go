package profiles

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

// Writing a skill back to the profile directory, so the web UI edits the same definition an
// operator edits in a text editor rather than a shadow copy of it in a database.
//
// TWO THINGS THIS COSTS, and both are the operator's to accept:
//
//  1. **Comments do not survive.** A skill is marshalled from the struct, so the prose in a
//     hand-written skills/<name>.yaml is gone the first time somebody presses Save in a
//     browser. There is no round-trip that keeps them: the decode is KnownFields(true) into
//     a struct and the comments are not in it.
//  2. **A GitOps deployment will overwrite what the UI wrote.** If the directory is a
//     checkout that CI redeploys, the browser is editing a working copy that the next deploy
//     replaces. Podium cannot tell the two apart and does not try.
//
// What it does NOT do is inline a `file:` prompt. A skill whose system_prompt names
// prompts/x.md keeps naming it, and the new prompt is written to that file instead — the
// alternative is that one Save in a browser silently flattens a profile's prompt layout into
// its YAML and leaves the .md orphaned.

// ErrProfileDirReadOnly means the profile directory cannot be written. It is its own error
// because the fix is a deployment change — the shipped compose mounts it `:ro` — and not
// anything about the skill being saved.
var ErrProfileDirReadOnly = errors.New("the profile directory is not writable")

// skillFilePerm and promptFilePerm are what a written file gets. The directory is the
// operator's own and its mode is theirs; a new file is readable, like the ones beside it.
const (
	skillFilePerm  fs.FileMode = 0o644
	promptFilePerm fs.FileMode = 0o644
)

// SkillPath is where a skill's file lives under a profile directory.
func SkillPath(dir, name string) string {
	return filepath.Join(dir, "skills", name+".yaml")
}

// WriteSkillFile saves a skill as skills/<name>.yaml under dir.
//
// It is used for a skill that came from a file and for one being moved into the directory,
// so it does not require the file to exist. When it does exist and its system_prompt is a
// `file:` reference, the reference is kept and the prompt is written to the file it names.
func WriteSkillFile(dir string, s Skill) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("no profile directory is configured, so there is no file to write")
	}
	if !NameRE.MatchString(s.Name) {
		return fmt.Errorf("skill name %q must match %s", s.Name, NameRE)
	}
	path := SkillPath(dir, s.Name)

	// The prompt is decided before anything is written: it is the one field that may live
	// somewhere other than this file.
	promptField, promptFile, err := promptTarget(path, s.SystemPrompt)
	if err != nil {
		return err
	}

	out := s
	out.SystemPrompt = promptField
	body, err := marshalSkill(out)
	if err != nil {
		return err
	}

	if promptFile != "" {
		if err := writeFileAtomic(promptFile, []byte(s.SystemPrompt), promptFilePerm); err != nil {
			return err
		}
	}
	return writeFileAtomic(path, body, skillFilePerm)
}

// DeleteSkillFile removes skills/<name>.yaml. A prompt file it referenced is left alone:
// prompts are shared between skills often enough that deleting one would be a surprise, and
// an orphaned .md costs nothing.
func DeleteSkillFile(dir, name string) error {
	if !NameRE.MatchString(name) {
		return fmt.Errorf("skill name %q must match %s", name, NameRE)
	}
	err := os.Remove(SkillPath(dir, name))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		// The goal state is "no such file", so this is success.
		return nil
	case isReadOnly(err):
		return readOnlyError(SkillPath(dir, name), err)
	case err != nil:
		return fmt.Errorf("removing %s: %w", SkillPath(dir, name), err)
	}
	return nil
}

// promptTarget decides where the prompt goes. It returns the value the YAML's system_prompt
// gets, and the path to write the prompt body to — empty when the prompt is inline.
//
// The existing file is what decides: a skill that names prompts/x.md keeps naming it. A
// skill with no file yet, or one whose prompt is already inline, stays inline.
func promptTarget(skillPath, prompt string) (field, file string, err error) {
	raw, err := os.ReadFile(skillPath) //nolint:gosec // the operator's own profile directory
	if errors.Is(err, fs.ErrNotExist) {
		return prompt, "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", skillPath, err)
	}
	// Only the system_prompt key is read back, and only to see whether it is a reference.
	var existing struct {
		SystemPrompt string `yaml:"system_prompt"`
	}
	if err := yaml.Unmarshal(raw, &existing); err != nil {
		// An unparseable file is not a reason to refuse a save — the save is what replaces
		// it — so the prompt goes inline and the file is overwritten wholesale.
		return prompt, "", nil
	}
	ref := strings.TrimSpace(existing.SystemPrompt)
	if !strings.HasPrefix(ref, filePrefix) {
		return prompt, "", nil
	}
	rel := strings.TrimSpace(strings.TrimPrefix(ref, filePrefix))
	if rel == "" {
		return prompt, "", nil
	}
	target := rel
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(skillPath), rel)
	}
	return existing.SystemPrompt, target, nil
}

// marshalSkill renders a skill as the YAML a human would have written: the same keys, in the
// order the documentation lists them, with the empty ones left out.
func marshalSkill(s Skill) ([]byte, error) {
	// A map would sort the keys alphabetically and put `agent` above `image`, which is not
	// how anybody reads a skill. yaml.Node keeps the order this slice sets.
	doc := &yaml.Node{Kind: yaml.MappingNode}
	put := func(key string, value any) error {
		var v yaml.Node
		if err := v.Encode(value); err != nil {
			return fmt.Errorf("encoding %s: %w", key, err)
		}
		doc.Content = append(doc.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key}, &v)
		return nil
	}

	if err := put("image", s.Image); err != nil {
		return nil, err
	}
	// A multi-line prompt reads as a block scalar rather than a quoted one-liner.
	promptNode := &yaml.Node{Kind: yaml.ScalarNode, Value: s.SystemPrompt}
	if strings.Contains(s.SystemPrompt, "\n") {
		promptNode.Style = yaml.LiteralStyle
	}
	doc.Content = append(doc.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "system_prompt"}, promptNode)

	if err := put("allowed_tools", s.AllowedTools); err != nil {
		return nil, err
	}
	if err := put("max_turns", s.MaxTurns); err != nil {
		return nil, err
	}
	if s.Timeout > 0 {
		if err := put("timeout", s.Timeout.String()); err != nil {
			return nil, err
		}
	}
	for _, f := range []struct {
		key   string
		value string
	}{
		{"agent", s.Agent}, {"model", s.Model}, {"effort", s.Effort},
	} {
		if f.value != "" {
			if err := put(f.key, f.value); err != nil {
				return nil, err
			}
		}
	}
	if len(s.Labels) > 0 {
		if err := put("labels", s.Labels); err != nil {
			return nil, err
		}
	}
	if s.Resources != (spec.Resources{}) {
		if err := put("resources", s.Resources); err != nil {
			return nil, err
		}
	}
	if len(s.Secrets) > 0 {
		if err := put("secrets", s.Secrets); err != nil {
			return nil, err
		}
	}
	if len(s.Repos) > 0 {
		if err := put("repos", s.Repos); err != nil {
			return nil, err
		}
	}
	if len(s.SlackChannels) > 0 {
		if err := put("slack_channels", s.SlackChannels); err != nil {
			return nil, err
		}
	}
	if len(s.Env) > 0 {
		if err := put("env", s.Env); err != nil {
			return nil, err
		}
	}
	for _, f := range []struct {
		key   string
		value bool
	}{{"docker", s.Docker}, {"linear", s.Linear}} {
		if f.value {
			if err := put(f.key, f.value); err != nil {
				return nil, err
			}
		}
	}

	var buf strings.Builder
	buf.WriteString("# Written by the Podium web UI. Comments are not preserved across a\n" +
		"# save from the browser; edit this file by hand only if nobody is editing it there.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encoding the skill: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encoding the skill: %w", err)
	}
	return []byte(buf.String()), nil
}

// writeFileAtomic writes through a temporary file in the same directory and renames over the
// target, so a reader never sees a half-written skill and a failed write leaves the previous
// definition intact.
func writeFileAtomic(path string, body []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".podium-*.tmp")
	if err != nil {
		if isReadOnly(err) {
			return readOnlyError(path, err)
		}
		return fmt.Errorf("writing %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		if isReadOnly(err) {
			return readOnlyError(path, err)
		}
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// isReadOnly is the two ways a container's read-only bind mount says no. It is nil-safe:
// every caller reaches it through a switch that a successful call also falls into.
func isReadOnly(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, fs.ErrPermission) || strings.Contains(err.Error(), "read-only file system")
}

func readOnlyError(path string, err error) error {
	return fmt.Errorf("%w: %s could not be written (%v). The shipped compose files mount "+
		"the profile directory :ro — change it to :rw to edit skills from the browser",
		ErrProfileDirReadOnly, path, err)
}
