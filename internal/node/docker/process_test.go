package docker

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSecretMakesNestedDirectoriesItCanWriteIn(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"flat", "app/config", "a/b/c"} {
		path := filepath.Join(root, key)
		cmd := exec.Command("sh", "-c", writeSecret, "sh", path)
		cmd.Stdin = strings.NewReader("value-" + key)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", key, err, out)
		}
		b, err := os.ReadFile(path)
		if err != nil || string(b) != "value-"+key {
			t.Fatalf("%s: read %q, %v", key, b, err)
		}
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0o400 {
			t.Errorf("%s: file mode %v, want 0400", key, st.Mode().Perm())
		}
		if dir := filepath.Dir(path); dir != root {
			dst, _ := os.Stat(dir)
			if dst.Mode().Perm() != fs.FileMode(0o700) {
				t.Errorf("%s: directory mode %v, want 0700", key, dst.Mode().Perm())
			}
		}
	}
}

func TestUnderSecrets(t *testing.T) {
	for key, want := range map[string]bool{
		"/podium/secrets/f": true, "/podium/secrets/app/config": true,
		"/podium/secrets": false, "/podium/secrets/../../etc/passwd": false, "/etc/passwd": false, "/podium/secretsx/f": false,
	} {
		if got := underSecrets(key); got != want {
			t.Errorf("underSecrets(%q) = %v, want %v", key, got, want)
		}
	}
}
