package skills

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const zipMagic = "PK\x03\x04"

// ParseUpload turns an upload into a file map. A zip is recognised by its own header; anything
// else is treated as a bare SKILL.md. The skill NAME comes from the frontmatter, not the
// filename: that is the only name the harness will load it under.
func ParseUpload(content []byte, filename string) (name string, files map[string]string, err error) {
	if len(content) == 0 {
		return "", nil, fmt.Errorf("%s is empty", label(filename))
	}
	if bytes.HasPrefix(content, []byte(zipMagic)) {
		files, err = unzip(content)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", label(filename), err)
		}
	} else {
		files = map[string]string{SkillFile: string(content)}
	}
	md, ok := files[SkillFile]
	if !ok {
		return "", nil, fmt.Errorf("%s has no %s", label(filename), SkillFile)
	}
	fm, err := readFrontmatter(md)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %s: %w", label(filename), SkillFile, err)
	}
	if err := ValidateName(fm.Name); err != nil {
		return "", nil, err
	}
	if _, err := Build(fm.Name, files); err != nil {
		return "", nil, err
	}
	return fm.Name, files, nil
}

// Install writes files into dir/name. replace allows overwriting an existing directory.
func Install(dir, name string, files map[string]string, replace bool) error {
	if dir == "" {
		return fmt.Errorf("this conductor has no skills directory (%s is unset)", DirEnv)
	}
	if err := ValidateName(name); err != nil {
		return err
	}
	root := filepath.Join(dir, name)
	if _, err := os.Lstat(root); err == nil && !replace {
		return fmt.Errorf("a skill named %q is already in %s", name, dir)
	}
	tmp, err := os.MkdirTemp(dir, "."+name+".*")
	if err != nil {
		return fmt.Errorf("install %q: %w", name, err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	for rel, body := range files {
		path := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("install %q: %w", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil { //nolint:gosec // operator-owned skills dir
			return fmt.Errorf("install %q: %w", name, err)
		}
	}
	backup := ""
	if _, err := os.Lstat(root); err == nil {
		backup = root + ".bak"
		if err := os.Rename(root, backup); err != nil {
			return fmt.Errorf("install %q: %w", name, err)
		}
	}
	if err := os.Rename(tmp, root); err != nil {
		if backup != "" {
			_ = os.Rename(backup, root)
		}
		return fmt.Errorf("install %q: %w", name, err)
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	return nil
}

// Remove deletes dir/name. A name that is not there is not an error.
func Remove(dir, name string) error {
	if dir == "" {
		return fmt.Errorf("this conductor has no skills directory (%s is unset)", DirEnv)
	}
	if err := ValidateName(name); err != nil {
		return err
	}
	root := filepath.Join(dir, name)
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("delete skill %q: %w", name, err)
	}
	return nil
}

func unzip(raw []byte) (map[string]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("not a zip: %w", err)
	}
	var names []string
	contents := map[string][]byte{}
	for _, f := range zr.File {
		name := strings.TrimPrefix(filepath.ToSlash(f.Name), "/")
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(rc, MaxBytes+1))
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		names = append(names, name)
		contents[name] = body
	}
	prefix := commonPrefix(names)
	files := map[string]string{}
	for _, name := range names {
		rel := strings.TrimPrefix(name, prefix)
		if rel == "" {
			continue
		}
		files[rel] = string(contents[name])
	}
	return files, nil
}

// commonPrefix is the wrapping folder a `zip -r skill` produces. Bare files (SKILL.md at
// the zip root) share no prefix and are left alone.
func commonPrefix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	first := names[0]
	i := strings.IndexByte(first, '/')
	if i < 0 {
		return ""
	}
	prefix := first[:i+1]
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) {
			return ""
		}
	}
	return prefix
}

func label(filename string) string {
	if strings.TrimSpace(filename) == "" {
		return "upload"
	}
	return filename
}
