package node

import (
	"context"
	"encoding/json"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/sandbox"
)

// applySandboxConfig builds this node's provider from what an admin configured, or empties
// it when the provider was removed. A node with the provider's settings in its own config
// file ignores the control plane.
func (n *Node) applySandboxConfig(ctx context.Context, c *podiumv1.SandboxConfig) {
	defer zeroBytes(c.GetSecret())
	if n.sandbox == nil {
		return
	}
	if c.GetProvider() != n.sandbox.Name() {
		n.logger.WarnContext(ctx, "ignoring sandbox config for another provider", "provider", c.GetProvider())
		return
	}
	if !c.GetConfigured() {
		n.sandbox.Set(nil)
		n.logger.WarnContext(ctx, "sandbox provider is not configured on the control plane; taking no work",
			"provider", c.GetProvider())
		n.signal()
		return
	}
	cfg := providerSettings(c.GetSettings())
	cfg["secret"] = string(c.GetSecret())
	p, err := sandbox.New(c.GetProvider(), cfg)
	if err != nil {
		n.sandbox.Set(nil)
		n.logger.ErrorContext(ctx, "the control plane's sandbox config does not work; taking no work",
			"provider", c.GetProvider(), "error", err)
		n.signal()
		return
	}
	n.sandbox.Set(p)
	n.logger.InfoContext(ctx, "sandbox provider configured by the control plane", "provider", c.GetProvider())
	n.signal()
}

// providerSettings turns the control plane's string settings into what a provider's config
// decodes: a number or a boolean where the text is one, the text otherwise.
func providerSettings(in map[string]string) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		var typed any
		if err := json.Unmarshal([]byte(v), &typed); err == nil {
			switch typed.(type) {
			case float64, bool:
				out[k] = typed
				continue
			}
		}
		out[k] = v
	}
	return out
}
