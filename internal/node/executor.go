package node

import (
	"context"

	"github.com/podium-ade/podium/internal/node/docker"
)

// executor is what the node needs from whatever runs its tasks: the local Docker engine,
// or a sandbox provider. The types that cross it are the Docker executor's own.
type executor interface {
	Run(ctx context.Context, req docker.Request, events chan<- docker.Event) (docker.Result, error)
	Adopt(ctx context.Context, req docker.AdoptRequest, events chan<- docker.Event) (docker.Result, error)
	Cancel(taskID string)
	Inject(taskID, text string) error
	EndWait(taskID string) error
	Teardown(ctx context.Context, taskID string, keepWorkspace bool) error
	ListOwned(ctx context.Context) ([]docker.OwnedContainer, error)
	HeldExit(taskID string) (int, bool)
	Holds(ctx context.Context, taskID string) bool
	TaskDir(taskID string) string
	ListPreviews(ctx context.Context) ([]docker.HeldPreview, error)
	GatewayPorts(ctx context.Context, taskID string) (map[int]int, error)
	PruneImages(ctx context.Context, req docker.PruneRequest) ([]docker.ImageRecord, error)
	ServerVersion() string
	Close() error
}

var _ executor = (*docker.Executor)(nil)
