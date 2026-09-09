// Package podium is the conductor's client of the Podium API. The conductor is an ordinary
// API client: it never opens the control plane's database, never sees the master key and
// never touches Docker. Everything it knows about a task it learned through these calls.
package podium

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"connectrpc.com/connect"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/alvaroibarguen/podium/internal/transport/dev"
	"github.com/alvaroibarguen/podium/internal/transport/tailnet"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// artifactDownloadPrefix is the plain-HTTP route podium-server streams artifact bytes on.
// It is a literal rather than internal/server/api.ArtifactDownloadPrefix on purpose: this
// package may not import internal/server/*, and the path is part of the published API
// (docs/cli.md), not an implementation detail.
const artifactDownloadPrefix = "/artifacts/"

// MaxAttachmentBytes is the largest artifact the conductor will relay. Slack's own limit is
// larger; a bot has no business proxying hundreds of megabytes on somebody's behalf.
const MaxAttachmentBytes = 25 << 20

// ErrTooLarge is what Artifact returns for an artifact over MaxAttachmentBytes.
var ErrTooLarge = errors.New("artifact is larger than the relay limit")

// Client bundles the Podium services the conductor uses.
type Client struct {
	Tasks     podiumv1connect.TaskServiceClient
	Artifacts podiumv1connect.ArtifactServiceClient
	Secrets   podiumv1connect.SecretServiceClient
	Identity  podiumv1connect.IdentityServiceClient

	base string
	http *http.Client
}

// New builds the clients. The credential choice is the ten lines of internal/cli/client.go,
// copied rather than imported: internal/agent may not depend on internal/cli. Over the
// tailnet with no token there is no credential at all — the machine is a tailnet device and
// WhoIs names it; a token is still sent when one is configured, which keeps a mixed setup
// working.
func New(serverURL, token string) *Client {
	httpClient := dev.NewClient(token)
	if strings.HasPrefix(serverURL, "https://") && token == "" {
		httpClient = tailnet.NewUserClient()
	}
	return &Client{
		Tasks:     podiumv1connect.NewTaskServiceClient(httpClient, serverURL),
		Artifacts: podiumv1connect.NewArtifactServiceClient(httpClient, serverURL),
		Secrets:   podiumv1connect.NewSecretServiceClient(httpClient, serverURL),
		Identity:  podiumv1connect.NewIdentityServiceClient(httpClient, serverURL),
		base:      strings.TrimSuffix(serverURL, "/"),
		http:      httpClient,
	}
}

// WhoAmI is what /readyz uses to decide the Podium API is reachable and the conductor's
// credential still works.
func (c *Client) WhoAmI(ctx context.Context) (*podiumv1.WhoAmIResponse, error) {
	res, err := c.Identity.WhoAmI(ctx, connect.NewRequest(&podiumv1.WhoAmIRequest{}))
	if err != nil {
		return nil, fmt.Errorf("whoami: %w", err)
	}
	return res.Msg, nil
}

// CreateTask queues one turn at the given queue priority — the playbook's, which is the only
// thing that decides one. Higher is claimed first; 0 is the default.
func (c *Client) CreateTask(ctx context.Context, s *spec.TaskSpec, priority int32) (*podiumv1.Task, error) {
	res, err := c.Tasks.CreateTask(ctx, connect.NewRequest(&podiumv1.CreateTaskRequest{
		Spec:     s.ToProto(),
		Priority: priority,
	}))
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	return res.Msg.GetTask(), nil
}

// CancelTask asks the node to stop a task. It does not wait: the container only dies
// once the node processes the cancel.
func (c *Client) CancelTask(ctx context.Context, taskID, reason string) error {
	_, err := c.Tasks.CancelTask(ctx, connect.NewRequest(&podiumv1.CancelTaskRequest{
		TaskId: taskID,
		Reason: reason,
	}))
	if err != nil {
		return fmt.Errorf("cancel task %s: %w", taskID, err)
	}
	return nil
}

// GetTask reads a task back, which is how a follow learns the terminal status.
func (c *Client) GetTask(ctx context.Context, taskID string) (*podiumv1.Task, error) {
	res, err := c.Tasks.GetTask(ctx, connect.NewRequest(&podiumv1.GetTaskRequest{TaskId: taskID}))
	if err != nil {
		return nil, fmt.Errorf("get task %s: %w", taskID, err)
	}
	return res.Msg.GetTask(), nil
}

// ListArtifacts returns everything a task stored. Attachment names on a message are
// artifact names, and this is what resolves them.
func (c *Client) ListArtifacts(ctx context.Context, taskID string) ([]*podiumv1.Artifact, error) {
	res, err := c.Artifacts.ListArtifacts(ctx, connect.NewRequest(&podiumv1.ListArtifactsRequest{TaskId: taskID}))
	if err != nil {
		return nil, fmt.Errorf("list artifacts of %s: %w", taskID, err)
	}
	return res.Msg.GetArtifacts(), nil
}

// Artifact streams one artifact's bytes from the control plane. It is GET /artifacts/{id}
// with the same credential the Connect calls carry, and it streams: an artifact may be
// hundreds of megabytes and none of it is ever held in memory here. The caller must Close
// the body.
func (c *Client) Artifact(ctx context.Context, artifactID string) (io.ReadCloser, string, error) {
	u := c.base + artifactDownloadPrefix + url.PathEscape(artifactID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build artifact request for %s: %w", artifactID, err)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download artifact %s: %w", artifactID, err)
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		return nil, "", fmt.Errorf("download artifact %s: %s", artifactID, res.Status)
	}
	if res.ContentLength > MaxAttachmentBytes {
		_ = res.Body.Close()
		return nil, "", fmt.Errorf("%w: %s is %d bytes, the limit is %d",
			ErrTooLarge, artifactID, res.ContentLength, MaxAttachmentBytes)
	}
	contentType := res.Header.Get("Content-Type")
	if mt, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = mt
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return res.Body, contentType, nil
}

// SetSecret stores one secret in the control plane's encrypted store and returns the version
// it landed as. The value is bytes all the way: it is never turned into a string on this
// path, and it is never logged.
func (c *Client) SetSecret(ctx context.Context, name string, value []byte) (int32, error) {
	res, err := c.Secrets.SetSecret(ctx, connect.NewRequest(&podiumv1.SetSecretRequest{
		Name: name, Value: value,
	}))
	if err != nil {
		// The error is the server's, and the server never echoes a value back.
		return 0, fmt.Errorf("set secret %s: %w", name, err)
	}
	return res.Msg.GetSecret().GetVersion(), nil
}

// SecretVersion is the version of one secret, or 0 when the control plane does not have it.
// ListSecrets returns metadata only — names, versions and who set them — so this reads no
// value and there is no endpoint that could.
func (c *Client) SecretVersion(ctx context.Context, name string) (int32, error) {
	res, err := c.Secrets.ListSecrets(ctx, connect.NewRequest(&podiumv1.ListSecretsRequest{}))
	if err != nil {
		return 0, fmt.Errorf("list secrets: %w", err)
	}
	for _, s := range res.Msg.GetSecrets() {
		if s.GetName() == name {
			return s.GetVersion(), nil
		}
	}
	return 0, nil
}

// DeleteSecret removes one secret. A NotFound is returned as-is: whether "already gone"
// counts as success is the caller's decision, not the client's.
func (c *Client) DeleteSecret(ctx context.Context, name string) error {
	_, err := c.Secrets.DeleteSecret(ctx, connect.NewRequest(&podiumv1.DeleteSecretRequest{
		Name: name,
	}))
	if err != nil {
		return fmt.Errorf("delete secret %s: %w", name, err)
	}
	return nil
}
