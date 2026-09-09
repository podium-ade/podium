//go:build integration

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"
)

// registryImage is the registry that stands in for a private one. Unlike the suite's task
// images, which the executor pulls as part of a run, it is started by hand, so the test pulls
// it by hand too — anonymously, which is the ordinary Docker Hub case.
const registryImage = "registry:2"

// registryHtpasswd is `htpasswd -nbB podium secret`: the one user the private registry knows.
const registryHtpasswd = "podium:$2y$05$XiAQUhRqtubn9OqQPTZ0muGjz9wrga6inSBfBQqqMjrDUiSYQ0DBS\n"

// startPrivateRegistry runs a registry that refuses anonymous pulls, published on a
// loopback port the engine treats as insecure (plain HTTP), and returns that host:port.
func startPrivateRegistry(t *testing.T, e *Executor) string {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, e.ensureImage(ctx, registryImage, nil, newEmitter(ctx, newCollector().ch)))
	authDir := shortTempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(authDir, "htpasswd"), []byte(registryHtpasswd), 0o644))

	resp, err := e.cli.ContainerCreate(ctx, &container.Config{
		Image: registryImage,
		Env: []string{
			"REGISTRY_AUTH=htpasswd",
			"REGISTRY_AUTH_HTPASSWD_REALM=podium-test",
			"REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd",
		},
	}, &container.HostConfig{
		Binds:        []string{authDir + ":/auth:ro"},
		PortBindings: nat.PortMap{"5000/tcp": {{HostIP: "127.0.0.1", HostPort: "0"}}},
	}, &network.NetworkingConfig{}, nil, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = e.cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
	})
	require.NoError(t, e.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}))

	insp, err := e.cli.ContainerInspect(ctx, resp.ID)
	require.NoError(t, err)
	bindings := insp.NetworkSettings.Ports["5000/tcp"]
	require.NotEmpty(t, bindings)
	return "127.0.0.1:" + bindings[0].HostPort
}

// pushWithAuth tags testImage into the private registry and pushes it as its one user,
// then drops the local tag so the only way to have the image is to pull it back.
func pushWithAuth(t *testing.T, e *Executor, ref, auth string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, e.cli.ImageTag(ctx, testImage, ref))
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := pushOnce(ctx, e, ref, auth)
		if err == nil {
			break
		}
		require.Less(t, time.Now(), deadline, "push never succeeded: %v", err)
		time.Sleep(500 * time.Millisecond) // the registry is still coming up
	}
	_, err := e.cli.ImageRemove(ctx, ref, image.RemoveOptions{})
	require.NoError(t, err)
}

// pushOnce pushes and reads the progress stream to its end, because a push, like a pull,
// reports a registry refusal inline rather than as the request's error.
func pushOnce(ctx context.Context, e *Executor, ref, auth string) error {
	rc, err := e.cli.ImagePush(ctx, ref, image.PushOptions{RegistryAuth: auth})
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	dec := json.NewDecoder(rc)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return errors.New(msg.Error)
		}
	}
}

// The Assign's credential is the whole difference between a task that runs and one that
// fails at provisioning: without it the private registry's refusal is permanent and points
// at the Registries screen; with it the same pull succeeds and the image is Podium's to prune.
func TestPrivateRegistryPullUsesTheAssignedCredential(t *testing.T) {
	ctx := context.Background()
	e := newTestExecutor(t)
	host := startPrivateRegistry(t, e)
	ref := host + "/podium-test/alpine:private"

	auth, err := registry.EncodeAuthConfig(registry.AuthConfig{Username: "podium", Password: "secret", ServerAddress: host})
	require.NoError(t, err)
	pushWithAuth(t, e, ref, auth)
	t.Cleanup(func() { _, _ = e.cli.ImageRemove(ctx, ref, image.RemoveOptions{}) })

	em := newEmitter(ctx, newCollector().ch)
	err = e.ensureImage(ctx, ref, Request{}.registryAuths(), em)
	require.ErrorIs(t, err, errImageUnavailable, "an anonymous pull from a private registry is refused for good")
	require.ErrorContains(t, err, "Registries screen")

	withAuth := Request{Registries: []RegistryCredential{{Host: host, Username: "podium", Password: []byte("secret")}}}
	require.NoError(t, e.ensureImage(ctx, ref, withAuth.registryAuths(), em))
	_, err = e.cli.ImageInspect(ctx, ref)
	require.NoError(t, err, "the image is on the engine")
	require.True(t, e.images.Owns(ref), "a pull with a credential is still a pull Podium made")
}
