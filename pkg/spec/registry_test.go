package spec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryHostOfAnImage(t *testing.T) {
	for image, host := range map[string]string{
		"alpine:3":                                  "docker.io",
		"library/alpine":                            "docker.io",
		"docker.io/library/alpine:3":                "docker.io",
		"us-docker.pkg.dev/acme/images/app:1.2":     "us-docker.pkg.dev",
		"ghcr.io/octocat/tool@sha256:" + zeros64:    "ghcr.io",
		"127.0.0.1:5000/podium-test/alpine:private": "127.0.0.1:5000",
		"localhost/app":                             "localhost",
	} {
		got, err := RegistryHost(image)
		require.NoError(t, err, image)
		require.Equal(t, host, got, image)
	}
	_, err := RegistryHost("not a reference")
	require.Error(t, err)
}

func TestValidateRegistryHostNormalises(t *testing.T) {
	for in, want := range map[string]string{
		"us-docker.pkg.dev":           "us-docker.pkg.dev",
		"https://index.docker.io/v1/": "docker.io",
		"GHCR.IO":                     "ghcr.io",
		"https://ghcr.io/":            "ghcr.io",
		"registry-1.docker.io":        "docker.io",
		" 192.168.1.100:5000 ":        "192.168.1.100:5000",
	} {
		got, err := ValidateRegistryHost(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "-x", "a b", "host:", "host:port"} {
		_, err := ValidateRegistryHost(bad)
		require.Error(t, err, bad)
	}
}

func TestImagesIsTheTaskAndEverySidecar(t *testing.T) {
	s := TaskSpec{Image: "app:1", Sidecars: map[string]Sidecar{
		"redis": {Image: "redis:7-alpine"},
		"db":    {Image: "postgres:16"},
	}}
	require.Equal(t, []string{"app:1", "postgres:16", "redis:7-alpine"}, s.Images())
}

const zeros64 = "0000000000000000000000000000000000000000000000000000000000000000"
