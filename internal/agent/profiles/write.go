package profiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	yaml "go.yaml.in/yaml/v3"
)

// WritePlaybook writes playbooks/<name>.yaml under dir. The document is validated first,
// so a browser cannot land a file the loader would refuse. The name is the file name, not
// a field: YAML that carries `name:` is not this function's problem (KnownFields would
// already have refused it on the way in).
func WritePlaybook(dir string, s Playbook) error {
	if !NameRE.MatchString(s.Name) {
		return fmt.Errorf("playbook name %q must match %s", s.Name, NameRE)
	}
	s.applyDefaults()
	path := filepath.Join(dir, "playbooks", s.Name+".yaml")
	if err := s.validate(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create playbooks/: %w", err)
	}
	raw, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil { //nolint:gosec // operator-owned profile dir
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// RemovePlaybook deletes playbooks/<name>.yaml. A name that is not there is not an error:
// deleting twice from a browser is a refresh, not a failure.
func RemovePlaybook(dir, name string) error {
	if !NameRE.MatchString(name) {
		return fmt.Errorf("playbook name %q must match %s", name, NameRE)
	}
	path := filepath.Join(dir, "playbooks", name+".yaml")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	return nil
}
