package docker

import (
	"archive/tar"
	"errors"
	"io"
	"path"
	"strings"

	"github.com/podium-ade/podium/pkg/spec"
)

// excludedNames is what a task's environment asks its snapshot to leave out
// (spec.WorkspaceExcludeEnv).
func excludedNames(env map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, name := range strings.Split(env[spec.WorkspaceExcludeEnv], ",") {
		name = strings.TrimSpace(name)
		if name != "" && name != "." && name != ".." && !strings.Contains(name, "/") {
			out[name] = true
		}
	}
	return out
}

// filterTar copies the tar src to dst without the entries under an excluded directory. With
// nothing excluded it is a plain copy.
func filterTar(dst io.Writer, src io.Reader, exclude map[string]bool) error {
	if len(exclude) == 0 {
		_, err := io.Copy(dst, src)
		return err
	}
	tr := tar.NewReader(src)
	tw := tar.NewWriter(dst)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if excludedPath(hdr.Name, exclude) {
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	return tw.Close()
}

func excludedPath(name string, exclude map[string]bool) bool {
	for _, part := range strings.Split(path.Clean(strings.TrimPrefix(name, "./")), "/") {
		if exclude[part] {
			return true
		}
	}
	return false
}
