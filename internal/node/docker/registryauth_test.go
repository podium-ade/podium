package docker

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/docker/docker/api/types/registry"
	"github.com/stretchr/testify/require"
)

func decodeAuth(t *testing.T, encoded string) registry.AuthConfig {
	t.Helper()
	raw, err := base64.URLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var ac registry.AuthConfig
	require.NoError(t, json.Unmarshal(raw, &ac))
	return ac
}

// A credential goes only to the registry that owns it: an image from anywhere else is
// pulled anonymously, as before.
func TestRegistryAuthGoesToTheRegistryThatOwnsIt(t *testing.T) {
	key := `{"type":"service_account","project_id":"acme"}`
	auths := Request{Registries: []RegistryCredential{
		{Host: "us-docker.pkg.dev", Username: "_json_key", Password: []byte(key)},
		{Host: "https://ghcr.io/", Username: "octocat", Password: []byte("ghp_token")},
	}}.registryAuths()

	gar := decodeAuth(t, auths.forImage("us-docker.pkg.dev/acme/images/app:1.2.3"))
	require.Equal(t, "_json_key", gar.Username)
	require.Equal(t, key, gar.Password, "the service account key travels verbatim")
	require.Equal(t, "us-docker.pkg.dev", gar.ServerAddress)

	gh := decodeAuth(t, auths.forImage("ghcr.io/octocat/tool@sha256:"+
		"0000000000000000000000000000000000000000000000000000000000000000"))
	require.Equal(t, "octocat", gh.Username, "the host is normalised the way the server does it")
	require.Equal(t, "ghp_token", gh.Password)

	require.Empty(t, auths.forImage("alpine:3"), "Docker Hub had no credential")
	require.Empty(t, auths.forImage("europe-docker.pkg.dev/acme/images/app"),
		"another region is another registry")
	require.Empty(t, auths.forImage("not a reference"))
	require.Empty(t, Request{}.registryAuths().forImage("alpine:3"))
}

// Docker Hub is spelled several ways as a host and one way in an image reference.
func TestRegistryAuthFoldsDockerHubSpellings(t *testing.T) {
	for _, host := range []string{"https://index.docker.io/v1/", "index.docker.io", "docker.io", "registry-1.docker.io"} {
		auths := Request{Registries: []RegistryCredential{{Host: host, Username: "u", Password: []byte("p")}}}.registryAuths()
		require.Equal(t, "u", decodeAuth(t, auths.forImage("alpine:3")).Username, host)
		require.Equal(t, "u", decodeAuth(t, auths.forImage("docker.io/library/alpine:3")).Username, host)
	}
}
