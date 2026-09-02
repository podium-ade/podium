package cli

import (
	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/alvaroibarguen/podium/internal/transport/dev"
)

// clients bundles the server-side services the CLI uses. The CLI only makes unary and
// server-streaming calls, so plain HTTP/1.1 is enough — none of the h2c setup the node
// daemon needs applies here.
type clients struct {
	tasks podiumv1connect.TaskServiceClient
	admin podiumv1connect.NodeAdminServiceClient
}

func newClients(cfg Config) *clients {
	httpClient := dev.NewClient(cfg.Token)
	return &clients{
		tasks: podiumv1connect.NewTaskServiceClient(httpClient, cfg.Server),
		admin: podiumv1connect.NewNodeAdminServiceClient(httpClient, cfg.Server),
	}
}
