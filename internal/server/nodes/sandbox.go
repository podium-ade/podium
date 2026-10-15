package nodes

import (
	"context"
	"slices"
	"strings"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// labelSandboxProvider is the label a sandbox node carries naming its provider.
const labelSandboxProvider = "sandbox.provider="

// SandboxResolver decrypts a provider's settings and credential for a node. found is false
// when no admin has configured the provider. The caller owns secret and zeroes it.
type SandboxResolver func(ctx context.Context, nodeID, provider string) (settings map[string]string, secret []byte, found bool, err error)

// SetSandboxResolver wires the secret store in. Without one, sandbox nodes are sent nothing
// and run on whatever their own config file says.
func (s *Service) SetSandboxResolver(r SandboxResolver) { s.sandbox = r }

// sandboxProviderOf is the provider a node's labels name, or "" for a node that is not one.
func sandboxProviderOf(labels []string) string {
	for _, l := range labels {
		if p, ok := strings.CutPrefix(l, labelSandboxProvider); ok {
			return p
		}
	}
	return ""
}

// sendSandboxConfig tells one session its provider's configuration, or that there is none.
func (s *Service) sendSandboxConfig(ctx context.Context, sess *Session, provider string) error {
	cfg := &podiumv1.SandboxConfig{Provider: provider}
	settings, secret, found, err := s.sandbox(ctx, sess.nodeID, provider)
	if err != nil {
		return err
	}
	if found {
		cfg.Configured, cfg.Settings, cfg.Secret = true, settings, secret
	}
	err = sess.Send(ctx, &podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_SandboxConfig{SandboxConfig: cfg}})
	// The send queue holds the message, not a copy of the bytes, so the plaintext stays
	// until the writer has put it on the wire; it is collected with the message.
	return err
}

// PushSandboxConfig re-sends a provider's configuration to every connected node of that
// provider, after an admin changed or removed it.
func (s *Service) PushSandboxConfig(ctx context.Context, provider string) {
	if s.sandbox == nil {
		return
	}
	for _, sess := range s.reg.withLabel(labelSandboxProvider + provider) {
		if err := s.sendSandboxConfig(ctx, sess, provider); err != nil {
			s.logger.WarnContext(ctx, "sending sandbox config failed", "node_id", sess.nodeID, "provider", provider, "error", err)
		}
	}
}

// withLabel is every live session whose node carries label.
func (r *Registry) withLabel(label string) []*Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Session
	for _, sess := range r.sessions {
		sess.mu.Lock()
		has := slices.Contains(sess.labels, label)
		sess.mu.Unlock()
		if has {
			out = append(out, sess)
		}
	}
	return out
}
