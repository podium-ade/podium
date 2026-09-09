package docker

import (
	"github.com/docker/docker/api/types/registry"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

// RegistryCredential is the login for one registry host, as the Assign delivered it.
// Password is plaintext; it lives in node memory for the length of the pulls and is zeroed
// with the run's secrets.
type RegistryCredential struct {
	Host     string
	Username string
	Password []byte
}

// registryAuths maps a registry host to the X-Registry-Auth header value a pull from it
// sends. Docker Hub is keyed "docker.io", however the credential spelled it.
type registryAuths map[string]string

// registryAuths encodes the request's credentials for the engine. A host that will not
// encode is skipped: the pull then goes out anonymous and the registry's refusal says why.
func (r Request) registryAuths() registryAuths {
	if len(r.Registries) == 0 {
		return nil
	}
	out := make(registryAuths, len(r.Registries))
	for _, c := range r.Registries {
		host := spec.NormalizeRegistryHost(c.Host)
		encoded, err := registry.EncodeAuthConfig(registry.AuthConfig{
			Username: c.Username, Password: string(c.Password), ServerAddress: host,
		})
		if err != nil {
			continue
		}
		out[host] = encoded
	}
	return out
}

// forImage returns the encoded credential for ref's registry, or "" when there is none,
// which the engine treats as an anonymous pull.
func (a registryAuths) forImage(ref string) string {
	if len(a) == 0 {
		return ""
	}
	host, err := spec.RegistryHost(ref)
	if err != nil {
		return ""
	}
	return a[host]
}
