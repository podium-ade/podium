package spec

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/distribution/reference"
)

// RegistryHostRE is what a registry host may look like once normalised: a hostname, with an
// optional port. It is what an image reference's domain part is.
var RegistryHostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)

// NormalizeRegistryHost reduces the ways people spell a registry to the domain an image
// reference carries: lower-case, no scheme, no path, and Docker Hub's legacy index address
// folded into "docker.io".
func NormalizeRegistryHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if host == "index.docker.io" || host == "registry-1.docker.io" {
		return "docker.io"
	}
	return host
}

// ValidateRegistryHost normalises host and rejects anything that could not be the domain of
// an image reference.
func ValidateRegistryHost(host string) (string, error) {
	h := NormalizeRegistryHost(host)
	if !RegistryHostRE.MatchString(h) {
		return "", fmt.Errorf("registry host %q is not a hostname (want the part before the first slash of an image reference, like us-docker.pkg.dev)", host)
	}
	return h, nil
}

// RegistryHost is the registry an image reference is pulled from, normalised the same way:
// "alpine:3" is docker.io, "us-docker.pkg.dev/acme/images/app" is us-docker.pkg.dev.
func RegistryHost(image string) (string, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("image %q: %w", image, err)
	}
	return reference.Domain(named), nil
}

// Images is every image reference a spec pulls: the task's own and each sidecar's.
func (s *TaskSpec) Images() []string {
	out := []string{s.Image}
	for _, name := range sortedKeys(s.Sidecars) {
		out = append(out, s.Sidecars[name].Image)
	}
	return out
}
