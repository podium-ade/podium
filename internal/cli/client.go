package cli

import (
	"net/http"

	"github.com/alvaroibarguen/podium/internal/proto/podium/v1/podiumv1connect"
	"github.com/alvaroibarguen/podium/internal/transport/dev"
	"github.com/alvaroibarguen/podium/internal/transport/tailnet"
)

// clients bundles the server-side services the CLI uses. The CLI only makes unary and
// server-streaming calls, so plain HTTP/1.1 is enough — none of the h2c setup the node
// daemon needs applies here.
type clients struct {
	tasks    podiumv1connect.TaskServiceClient
	admin    podiumv1connect.NodeAdminServiceClient
	identity podiumv1connect.IdentityServiceClient
}

func newClients(cfg Config) *clients {
	httpClient := httpClientFor(cfg)
	return &clients{
		tasks:    podiumv1connect.NewTaskServiceClient(httpClient, cfg.Server),
		admin:    podiumv1connect.NewNodeAdminServiceClient(httpClient, cfg.Server),
		identity: podiumv1connect.NewIdentityServiceClient(httpClient, cfg.Server),
	}
}

// httpClientFor picks the credential the server expects. Over the tailnet there is none: the
// user's machine is a tailnet device and WhoIs names them, so the client carries no header at
// all. A token is still sent if one was configured, which keeps a mixed setup working.
func httpClientFor(cfg Config) *http.Client {
	if cfg.Tailnet() && cfg.Token == "" {
		return tailnet.NewUserClient()
	}
	return dev.NewClient(cfg.Token)
}
