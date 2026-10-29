// Package skills is a third-party Agent Skill on its way into a task container: what a
// bundle is, how one is read off the conductor's own disk, and the guards it has to pass
// before any of it reaches a turn.
//
// A skill is a directory holding a SKILL.md with YAML frontmatter — the industry shape the
// harness discovers natively (https://opencode.ai/docs/skills/). It is somebody else's
// content, so everything below treats it as hostile: the walk refuses a symlink rather than
// following it, a path outside the skill's own directory is an error and not a normalised
// path, and a bundle over any cap fails with the cap in the message.
//
// This package reads a directory and produces bytes. It does not decide which skills a turn
// gets — that is the playbook's allow-list — and the only thing it writes is a skill an
// operator uploaded, or a host turn's copy of one.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	yaml "go.yaml.in/yaml/v3"

	"github.com/podium-ade/podium/pkg/spec"
)

// SkillFile is the one file a skill directory must contain, spelled exactly this way.
const SkillFile = "SKILL.md"

// Docs is where a validation error points a reader who wants the rules rather than the
// one that stopped them.
const Docs = "docs/agent.md"

// NameRE is the harness's own skill-name rule, and the directory name has to match it too:
// lowercase alphanumerics in single-hyphen-separated groups, no leading, trailing or
// doubled hyphen. It is deliberately borrowed rather than invented — a name this refuses is
// a skill the harness would refuse after Podium had already shipped it into a container.
var NameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// pathSegmentRE is what one path component inside a bundle may look like. It is narrower
// than any filesystem's rule on purpose: the components reach a container as directory and
// file names, and there is nothing a skill legitimately needs outside this set.
var pathSegmentRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// The frontmatter rules, from the harness's documentation.
const (
	MaxNameLen        = 64
	MaxDescriptionLen = 1024
)

// The caps. Every one of them is named in the error that trips it, because a skill that is
// too big is an operator's problem to fix and a number they cannot see is no help.
//
// A bundle reaches a container as a read-only directory on the task spec (spec.FileMount),
// so no environment variable bounds it. What does is the spec: MaxSkills bundles at
// MaxBytes is exactly spec.MaxMountBytes, so a playbook within these caps always fits.
const (
	// MaxSkills is how many skills one playbook may declare.
	MaxSkills = 8
	// MaxFiles is how many files one skill may carry, SKILL.md included.
	MaxFiles = 64
	// MaxBytes caps the content of one skill's files, together.
	MaxBytes = 10 << 20
)

// ContainerDir is where a task container finds its skills: one read-only mount per skill,
// at ContainerDir/<name>. The harness is pointed at it in its config.
const ContainerDir = spec.FilesMount + "/skills"

// DirEnv is the directory on the CONDUCTOR'S OWN HOST that skills are read from. There is
// no other source in this phase: no object store, no upload, no RPC.
const DirEnv = "PODIUM_AGENT_SKILLS_DIR"

// Bundle is one skill, read and checked, on its way to a turn.
type Bundle struct {
	// Name is the skill's name, which is also its directory name here and in the container.
	Name string
	// Description is the frontmatter's, kept for the log line and nothing else.
	Description string
	// SHA256 is Digest(Files): what the runtime verifies the directory it was given against
	// before the harness reads any of it.
	SHA256 string
	// Files is every file of the skill by its path relative to the skill's directory.
	Files map[string]string
}

// Size is how many bytes of content the bundle carries.
func (b Bundle) Size() int {
	n := 0
	for _, content := range b.Files {
		n += len(content)
	}
	return n
}

// Mount is the read-only directory this bundle is delivered as.
func (b Bundle) Mount() spec.FileMount {
	return spec.FileMount{Target: ContainerDir + "/" + b.Name, Files: maps.Clone(b.Files)}
}

// WriteUnder writes the bundle to root/<name>, for a turn that has no node to mount it: a
// host turn. The files are read-only, the same as a mount, though the turn runs as the
// conductor's own user and that is a convention here rather than a control.
func (b Bundle) WriteUnder(root string) error {
	dir := filepath.Join(root, b.Name)
	for _, rel := range slices.Sorted(maps.Keys(b.Files)) {
		if err := checkPath(rel); err != nil {
			return fmt.Errorf("skill %q: %w", b.Name, err)
		}
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("skill %q: %w", b.Name, err)
		}
		if err := os.WriteFile(path, []byte(b.Files[rel]), 0o444); err != nil {
			return fmt.Errorf("skill %q: %w", b.Name, err)
		}
	}
	return nil
}

// Digest is the hash a skill is identified and verified by. The runtime computes it again
// over the directory it finds, so it is defined over the files themselves rather than over
// any encoding of them: for each path in byte order, the path, a NUL, the content's length
// in decimal, a NUL, and the content. A path cannot hold a NUL and the length says where
// the content ends, so no two file sets share a preimage.
func Digest(files map[string]string) string {
	h := sha256.New()
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		content := files[rel]
		h.Write([]byte(rel + "\x00" + strconv.Itoa(len(content)) + "\x00"))
		h.Write([]byte(content))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// frontmatter is the part of SKILL.md this package validates. It is deliberately not
// KnownFields-strict: `license`, `compatibility`, `metadata` and whatever a skill written
// for another harness carries are none of Podium's business, and refusing them would refuse
// skills the harness itself accepts.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ValidateName reports whether a name is one the harness would accept. It is exported
// because a playbook's allow-list is checked with it, long before any directory is read.
func ValidateName(name string) error {
	switch {
	case name == "":
		return errors.New("a skill name is required")
	case len(name) > MaxNameLen:
		return fmt.Errorf("skill name %q is %d characters; the limit is %d", name, len(name), MaxNameLen)
	case !NameRE.MatchString(name):
		return fmt.Errorf("skill name %q must match %s (see %s)", name, NameRE, Docs)
	}
	return nil
}

// Load reads one skill directory and packs it.
func Load(dir, name string) (Bundle, error) {
	if err := ValidateName(name); err != nil {
		return Bundle{}, err
	}
	root := filepath.Join(dir, name)

	// Lstat, not Stat: a symlinked skill directory would otherwise be followed, and
	// `ln -s /etc pr-review` inside the skills directory would pack /etc into a container.
	info, err := os.Lstat(root)
	switch {
	case err != nil:
		return Bundle{}, fmt.Errorf("skill %q: %w", name, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return Bundle{}, fmt.Errorf("skill %q: %s is a symlink; a skill must be a real directory", name, root)
	case !info.IsDir():
		return Bundle{}, fmt.Errorf("skill %q: %s is not a directory", name, root)
	}

	files, err := collect(root, name)
	if err != nil {
		return Bundle{}, err
	}
	if _, ok := files[SkillFile]; !ok {
		return Bundle{}, fmt.Errorf("skill %q: %s has no %s", name, root, SkillFile)
	}
	return Build(name, files)
}

// Build is every rule a bundle has to pass, applied to a file map that is already in hand.
// Load calls it after walking a directory on the conductor's host.
func Build(name string, files map[string]string) (Bundle, error) {
	if err := ValidateName(name); err != nil {
		return Bundle{}, err
	}
	if len(files) == 0 {
		return Bundle{}, fmt.Errorf("skill %q is empty", name)
	}
	if len(files) > MaxFiles {
		return Bundle{}, fmt.Errorf("skill %q holds %d files; the limit is %d", name, len(files), MaxFiles)
	}
	total := 0
	for rel, content := range files {
		if err := checkPath(rel); err != nil {
			return Bundle{}, fmt.Errorf("skill %q: %w", name, err)
		}
		if !utf8.ValidString(content) {
			return Bundle{}, fmt.Errorf("skill %q: %s is not valid UTF-8; a skill bundle carries text (see %s)",
				name, rel, Docs)
		}
		total += len(content)
		if total > MaxBytes {
			return Bundle{}, fmt.Errorf("skill %q holds more than %d bytes of files; that is the limit",
				name, MaxBytes)
		}
	}
	skill, ok := files[SkillFile]
	if !ok {
		return Bundle{}, fmt.Errorf("skill %q has no %s", name, SkillFile)
	}
	fm, err := readFrontmatter(skill)
	if err != nil {
		return Bundle{}, fmt.Errorf("skill %q: %s: %w", name, SkillFile, err)
	}
	if err := checkFrontmatter(name, fm); err != nil {
		return Bundle{}, fmt.Errorf("skill %q: %s: %w", name, SkillFile, err)
	}

	return Bundle{
		Name:        name,
		Description: strings.TrimSpace(fm.Description),
		SHA256:      Digest(files),
		Files:       maps.Clone(files),
	}, nil
}

// collect walks one skill directory into the bundle's file map. Everything that is not a
// directory or a regular file is refused by name rather than skipped: a skill that ships a
// symlink is a skill whose author expected it to arrive, and silently dropping it would
// produce a bundle that behaves differently from the directory it was made from.
func collect(root, name string) (map[string]string, error) {
	files := map[string]string{}
	total := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := checkPath(rel); err != nil {
			return err
		}
		if d.IsDir() {
			// An empty directory carries nothing and the file map has no way to express
			// one. Descending is all that is wanted here.
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is a %s; a skill may carry only files and directories", rel, kindOf(d.Type()))
		}
		if len(files) == MaxFiles {
			return fmt.Errorf("more than %d files; that is the limit for one skill", MaxFiles)
		}
		raw, err := os.ReadFile(path) //nolint:gosec // the operator's own skills directory
		if err != nil {
			return err
		}
		total += len(raw)
		if total > MaxBytes {
			return fmt.Errorf("more than %d bytes of files; that is the limit for one skill", MaxBytes)
		}
		if !utf8.Valid(raw) {
			return fmt.Errorf("%s is not valid UTF-8; a skill bundle carries text (see %s)", rel, Docs)
		}
		files[rel] = string(raw)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", name, err)
	}
	return files, nil
}

// checkPath is the traversal guard on this side of the wire. WalkDir cannot hand us a path
// outside root, so this is not the control — the control is that a symlink is refused and
// the wire format cannot carry one. It is here because the same rule runs in the runtime,
// against bytes that did cross a wire, and a bundle should fail where it was made rather
// than in a container.
func checkPath(rel string) error {
	switch {
	case rel == "":
		return errors.New("a bundle path must not be empty")
	case len(rel) > 255:
		return fmt.Errorf("path %q is longer than 255 characters", rel)
	case strings.HasPrefix(rel, "/"):
		return fmt.Errorf("path %q must be relative to the skill's own directory", rel)
	}
	parts := strings.Split(rel, "/")
	if len(parts) > 8 {
		return fmt.Errorf("path %q is more than 8 directories deep", rel)
	}
	for _, p := range parts {
		switch {
		case p == "" || p == "." || p == "..":
			return fmt.Errorf("path %q must not contain %q", rel, p)
		case !pathSegmentRE.MatchString(p):
			return fmt.Errorf("path %q has a component %q outside %s", rel, p, pathSegmentRE)
		}
	}
	return nil
}

func kindOf(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m&fs.ModeDevice != 0:
		return "device"
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	default:
		return "special file"
	}
}

// readFrontmatter pulls the YAML block off the top of SKILL.md. The file must open with a
// `---` line and close the block with another one; anything else is a SKILL.md the harness
// would ignore, and a skill nobody can use is not a skill to ship.
func readFrontmatter(content string) (frontmatter, error) {
	body := strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return frontmatter{}, errors.New("must open with a --- frontmatter block")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return frontmatter{}, errors.New("the --- frontmatter block is never closed")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fm); err != nil {
		return frontmatter{}, fmt.Errorf("the frontmatter is not YAML: %w", err)
	}
	return fm, nil
}

func checkFrontmatter(dir string, fm frontmatter) error {
	var errs []error
	switch {
	case fm.Name == "":
		errs = append(errs, errors.New("name is required in the frontmatter"))
	case fm.Name != dir:
		errs = append(errs, fmt.Errorf("name is %q but the directory is %q; they must match", fm.Name, dir))
	default:
		if err := ValidateName(fm.Name); err != nil {
			errs = append(errs, err)
		}
	}
	desc := strings.TrimSpace(fm.Description)
	switch {
	case desc == "":
		errs = append(errs, errors.New(
			"description is required in the frontmatter: it is the whole of what the model "+
				"reads to decide whether to use the skill"))
	case len(fm.Description) > MaxDescriptionLen:
		errs = append(errs, fmt.Errorf("description is %d characters; the limit is %d",
			len(fm.Description), MaxDescriptionLen))
	}
	return errors.Join(errs...)
}
