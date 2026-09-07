package skills

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"unicode/utf8"
)

// MaxUploadBytes is the largest upload the API will look at.
//
// It is above MaxBytes on purpose. A zip is compressed, and the refusal a human needs is the
// one that names the unpacked cap the skill actually failed — so an archive is read as far as
// this, unpacked under the real caps, and refused with the number that matters. Nothing
// between MaxBytes and here is ever stored.
const MaxUploadBytes = 1 << 20

// MaxUploadFiles caps how many entries an archive may hold before it is read at all,
// including the directory entries and the noise below. It is deliberately larger than
// MaxFiles, which is what the skill itself is held to.
const MaxUploadFiles = 256

// zipMagic is the local file header a zip starts with. It is what tells an upload apart from
// a bare SKILL.md, so a browser does not have to declare which it sent — and a file that
// claims to be one and is the other is refused by the same rules either way.
var zipMagic = []byte{'P', 'K', 0x03, 0x04}

// archiverNoise is the two things a desktop archiver adds that nobody meant to ship. They
// are skipped rather than refused, which is the one exception to this package's rule that a
// bundle entry it cannot carry is an error: a skill's author did not put them there, and a
// Mac that cannot upload a zip it just made is a refusal about the archiver and not about
// the skill. Everything else — a symlink, a dotfile, an executable bit — is still refused,
// because somebody meant it.
var archiverNoise = []string{"__MACOSX/", "._"}

// dsStore is the other half of that: it appears at any depth, not only at the top.
const dsStore = ".DS_Store"

// Parse reads an upload into a skill's name and its files.
//
// The upload is either a zip of the skill's directory or a bare SKILL.md, told apart by the
// zip magic. The NAME comes from the frontmatter and from nowhere else: an upload has no
// directory to take one from, and the harness will only load a skill whose frontmatter name
// matches the directory it sits in — so the skill's own answer is the only one that can be
// right.
//
// Nothing here writes anything or trusts anything. Every path is checked component by
// component by Build, which is the same validator a skill read off the conductor's disk goes
// through.
func Parse(content []byte, filename string) (name string, files map[string]string, err error) {
	label := strings.TrimSpace(filename)
	if label == "" {
		label = "the upload"
	}
	switch {
	case len(content) == 0:
		return "", nil, errors.New("the upload is empty")
	case len(content) > MaxUploadBytes:
		return "", nil, fmt.Errorf("%s is %d bytes; the limit for an upload is %d",
			label, len(content), MaxUploadBytes)
	case bytes.HasPrefix(content, zipMagic):
		files, err = unzip(content, label)
	default:
		files, err = fromMarkdown(content, label)
	}
	if err != nil {
		return "", nil, err
	}
	name, err = NameOf(files)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", label, err)
	}
	return name, files, nil
}

// NameOf reads a skill's own name out of its SKILL.md.
func NameOf(files map[string]string) (string, error) {
	skill, ok := files[SkillFile]
	if !ok {
		return "", fmt.Errorf("there is no %s in it; a skill is a directory with a %s at its root",
			SkillFile, SkillFile)
	}
	fm, err := readFrontmatter(skill)
	if err != nil {
		return "", fmt.Errorf("%s: %w", SkillFile, err)
	}
	if strings.TrimSpace(fm.Name) == "" {
		return "", fmt.Errorf("%s: name is required in the frontmatter, and it is what the skill "+
			"is installed as", SkillFile)
	}
	return strings.TrimSpace(fm.Name), nil
}

// fromMarkdown is the paste-a-SKILL.md path: one file, and the file is the skill.
func fromMarkdown(content []byte, label string) (map[string]string, error) {
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("%s is not valid UTF-8. A zip is recognised by its own header; "+
			"anything else is read as a %s (see %s)", label, SkillFile, Docs)
	}
	return map[string]string{SkillFile: string(content)}, nil
}

// unzip reads a zip into a bundle's file map.
//
// The reason this can be done safely at all is that the result is a file map and not a
// filesystem: a zip entry's mode, its symlink target and its directory bit are read to
// decide whether to refuse it, and then thrown away. Nothing is created from the archive —
// Build validates the map and the runtime writes it, at 0644, exactly as it does for a skill
// that never went near an archive.
//
// The guards, in the order they run:
//
//   - An entry count cap, before any decompression.
//   - Directory entries carry nothing and are skipped; the paths of the files inside them
//     are what make the directories.
//   - Anything that is not a regular file — a symlink, a device node, a socket — is refused
//     by name. A zip that ships a symlink is a zip whose author expected it to arrive.
//   - The declared uncompressed size is checked before the entry is opened, and then the
//     read is limited anyway: a header can lie, and a zip bomb's header usually does.
//   - Paths are checked by Build, component by component, never normalised.
func unzip(content []byte, label string) (map[string]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("%s is not a readable zip: %w", label, err)
	}
	if len(zr.File) > MaxUploadFiles {
		return nil, fmt.Errorf("%s holds %d entries; the limit for an upload is %d",
			label, len(zr.File), MaxUploadFiles)
	}
	files := map[string]string{}
	total := 0
	for _, f := range zr.File {
		rel, isDir, err := entryPath(f.Name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if rel == "" || skipEntry(rel) {
			continue
		}
		if isDir || f.FileInfo().IsDir() {
			continue
		}
		if mode := f.Mode(); !mode.IsRegular() {
			return nil, fmt.Errorf("%s: %s is a %s; a skill may carry only files and directories",
				label, f.Name, kindOf(mode&fs.ModeType))
		}
		if f.UncompressedSize64 > uint64(MaxBytes) {
			return nil, fmt.Errorf("%s: %s says it is %d bytes; one skill is capped at %d unpacked",
				label, rel, f.UncompressedSize64, MaxBytes)
		}
		raw, err := readEntry(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", label, rel, err)
		}
		total += len(raw)
		if total > MaxBytes {
			return nil, fmt.Errorf("%s unpacks to more than %d bytes; that is the limit for one skill",
				label, MaxBytes)
		}
		if _, dup := files[rel]; dup {
			// Two entries for one path is how an archive smuggles a second version of a
			// file past whoever looked at the first.
			return nil, fmt.Errorf("%s holds %s twice", label, rel)
		}
		files[rel] = string(raw)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s holds no files", label)
	}
	return stripRoot(files), nil
}

// entryPath is what one zip entry name is allowed to be.
//
// It **refuses** rather than normalises, which is the whole point. `path.Clean` would turn
// `/etc/cron.d/x` into `etc/cron.d/x` and `..\..\windows` into `windows`, both of which are
// legal bundle paths — so the archive would be quietly repaired into something the author
// did not write, and nobody looking at the stored skill afterwards could tell. An entry that
// asks to go somewhere it may not is an error, and the error says where it asked to go.
//
// The only two things it does normalise are a leading `./` and a trailing `/`, which are how
// archivers spell "the current directory" and "this is a directory" and carry no intent.
func entryPath(name string) (rel string, isDir bool, err error) {
	switch {
	case strings.ContainsRune(name, '\\'):
		return "", false, fmt.Errorf("%q must be a relative path with no backslash", name)
	case strings.ContainsRune(name, 0):
		return "", false, fmt.Errorf("%q must not contain a NUL", name)
	}
	rel = strings.TrimPrefix(name, "./")
	isDir = strings.HasSuffix(rel, "/")
	rel = strings.TrimSuffix(rel, "/")
	if rel == "" || rel == "." {
		return "", true, nil
	}
	if strings.HasPrefix(rel, "/") {
		return "", false, fmt.Errorf("%q must be relative to the skill's own directory", name)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false, fmt.Errorf("%q must not contain %q", name, part)
		}
	}
	return rel, isDir, nil
}

// readEntry decompresses one entry under a hard limit. MaxBytes+1 is read so that a file
// exactly on the cap is accepted and one byte over it is caught here rather than by the
// running total, which would name the wrong number.
func readEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(io.LimitReader(rc, int64(MaxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBytes {
		return nil, fmt.Errorf("it unpacks to more than %d bytes, whatever its header said", MaxBytes)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("it is not valid UTF-8; a skill bundle carries text (see %s)", Docs)
	}
	return raw, nil
}

func skipEntry(rel string) bool {
	if rel == dsStore || strings.HasSuffix(rel, "/"+dsStore) {
		return true
	}
	for _, noise := range archiverNoise {
		if strings.HasPrefix(rel, noise) || strings.Contains(rel, "/"+noise) {
			return true
		}
	}
	return false
}

// stripRoot removes the single wrapping directory `zip -r x.zip pr-review/` produces.
//
// It only fires when there is no SKILL.md at the root and every path shares one first
// component, which is exactly the shape of an archive of a skill's directory. A zip made
// from inside the directory already has SKILL.md at its root and is left alone, and an
// archive of two skills has two first components and is left alone too — it then fails for
// having no SKILL.md, which is the honest answer: that file is not one skill.
func stripRoot(files map[string]string) map[string]string {
	if _, ok := files[SkillFile]; ok {
		return files
	}
	root := ""
	for rel := range files {
		first, rest, found := strings.Cut(rel, "/")
		if !found || rest == "" {
			return files
		}
		if root == "" {
			root = first
		} else if root != first {
			return files
		}
	}
	if root == "" {
		return files
	}
	out := make(map[string]string, len(files))
	for rel, content := range files {
		out[strings.TrimPrefix(rel, root+"/")] = content
	}
	return out
}
