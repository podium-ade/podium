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
// gets — that is the playbook's allow-list — and it never writes anything.
package skills

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	yaml "go.yaml.in/yaml/v3"
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
// MaxEncodedBytes is the load-bearing one and it is not arbitrary. A bundle travels as one
// environment variable on the task spec, and Linux caps a single environment string at
// MAX_ARG_STRLEN — 128 KiB — after which the container cannot exec at all:
//
//	$ docker run --rm -e "BIG=$(python3 -c "print('x'*(128*1024))")" IMAGE printenv BIG
//	exec /usr/bin/printenv: argument list too long
//
// 64 KiB leaves half of that as margin and lets MaxSkills bundles ride along beside a
// full-sized brief.
const (
	// MaxSkills is how many skills one playbook may declare.
	MaxSkills = 8
	// MaxFiles is how many files one skill may carry, SKILL.md included.
	MaxFiles = 64
	// MaxBytes caps the decompressed bundle document.
	MaxBytes = 128 << 10
	// MaxEncodedBytes caps the base64 that goes on the task spec.
	MaxEncodedBytes = 64 << 10
)

// EnvPrefix is what a bundle's environment variable is called. A playbook may not set a
// variable with this prefix: the conductor writes them, one per skill it is delivering.
const EnvPrefix = "PODIUM_AGENT_SKILL_"

// DirEnv is the directory on the CONDUCTOR'S OWN HOST that skills are read from. There is
// no other source in this phase: no object store, no upload, no RPC.
const DirEnv = "PODIUM_AGENT_SKILLS_DIR"

// Bundle is one skill packed for delivery.
type Bundle struct {
	// Name is the skill's name, which is also its directory name here and in the container.
	Name string
	// Description is the frontmatter's, kept for the log line and nothing else.
	Description string
	// SHA256 is the hex digest of the bundle document — the bytes Encoded decompresses to,
	// and what the runtime verifies before it unpacks anything.
	SHA256 string
	// Env is the environment variable Encoded travels in.
	Env string
	// Encoded is base64(gzip(document)).
	Encoded string
	// Files is how many files the bundle carries. For the log line.
	Files int
}

// document is the bundle's wire format: a map of relative path to file content.
//
// It is JSON and not a tar deliberately. A tar entry can be a symlink, a hardlink, a device
// node or a mode bit, and every one of those is a thing an unpacker has to refuse; this
// format cannot express any of them, so the whole class of archive attacks is absent rather
// than defended against. The cost is that a bundle carries text only and nothing in it is
// executable — see docs/agent.md.
type document struct {
	Files map[string]string `json:"files"`
}

// frontmatter is the part of SKILL.md this package validates. It is deliberately not
// KnownFields-strict: `license`, `compatibility`, `metadata` and whatever a skill written
// for another harness carries are none of Podium's business, and refusing them would refuse
// skills the harness itself accepts.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// EnvFor is the environment variable one skill's bundle travels in. NameRE has already
// refused everything but lowercase alphanumerics and hyphens, so the mapping is one-to-one:
// an underscore cannot appear in a name, which means no two names can collide here.
func EnvFor(name string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
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

// LoadAll reads every named skill out of dir, in the order given. One that fails fails the
// whole set: a turn that quietly ran with three of its four skills would answer differently
// from the one the playbook describes, and nobody would know which had happened.
func LoadAll(dir string, names []string) ([]Bundle, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > MaxSkills {
		return nil, fmt.Errorf("%d skills are declared; the limit is %d per playbook", len(names), MaxSkills)
	}
	if dir == "" {
		return nil, fmt.Errorf("%s is not set, so no skill can be delivered", DirEnv)
	}
	out := make([]Bundle, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("skill %q is declared twice", name)
		}
		seen[name] = true
		b, err := Load(dir, name)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
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
	skill, ok := files[SkillFile]
	if !ok {
		return Bundle{}, fmt.Errorf("skill %q: %s has no %s", name, root, SkillFile)
	}
	fm, err := readFrontmatter(skill)
	if err != nil {
		return Bundle{}, fmt.Errorf("skill %q: %s: %w", name, SkillFile, err)
	}
	if err := checkFrontmatter(name, fm); err != nil {
		return Bundle{}, fmt.Errorf("skill %q: %s: %w", name, SkillFile, err)
	}

	raw, err := json.Marshal(document{Files: files})
	if err != nil {
		return Bundle{}, fmt.Errorf("skill %q: %w", name, err)
	}
	if len(raw) > MaxBytes {
		return Bundle{}, fmt.Errorf("skill %q is %d bytes packed; the limit is %d", name, len(raw), MaxBytes)
	}
	encoded, err := pack(raw)
	if err != nil {
		return Bundle{}, fmt.Errorf("skill %q: %w", name, err)
	}
	if len(encoded) > MaxEncodedBytes {
		return Bundle{}, fmt.Errorf(
			"skill %q is %d bytes once encoded for delivery; the limit is %d, and a bundle "+
				"travels as one environment variable on the task spec",
			name, len(encoded), MaxEncodedBytes)
	}
	sum := sha256.Sum256(raw)
	return Bundle{
		Name:        name,
		Description: fm.Description,
		SHA256:      hex.EncodeToString(sum[:]),
		Env:         EnvFor(name),
		Encoded:     encoded,
		Files:       len(files),
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

// pack is gzip then base64. gzip is not decoration: it is roughly a third of the size on
// markdown, which is the difference between a useful skill and one that does not fit in an
// environment variable.
func pack(raw []byte) (string, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := zw.Write(raw); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
