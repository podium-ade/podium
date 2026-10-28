package nodes

import (
	"context"
	"slices"
	"strings"
	"time"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// reapEvery is how often queued sandbox deletions are asked for again.
const reapEvery = time.Minute

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

// ReapSandboxes asks the provider's connected nodes, in turn, to delete each sandbox queued
// for it. A reap stays queued until a node says the sandbox is gone.
func (s *Service) ReapSandboxes(ctx context.Context, provider string) {
	if provider == "" {
		return
	}
	s.reapOn(ctx, provider, s.reg.withLabel(labelSandboxProvider+provider))
}

func (s *Service) reapOn(ctx context.Context, provider string, sessions []*Session) {
	if len(sessions) == 0 {
		return
	}
	reaps, err := s.store.ListSandboxReaps(ctx, provider)
	if err != nil {
		s.logger.WarnContext(ctx, "listing sandboxes to delete failed", "provider", provider, "error", err)
		return
	}
	for i, r := range reaps {
		sess := sessions[i%len(sessions)]
		if err := sess.Send(ctx, &podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_ReapSandbox{ReapSandbox: &podiumv1.ReapSandbox{
			Provider: r.Provider, SandboxId: r.SandboxID, TaskId: r.TaskID,
		}}}); err != nil {
			s.logger.WarnContext(ctx, "asking a node to delete a sandbox failed",
				"node_id", sess.nodeID, "sandbox_id", r.SandboxID, "error", err)
		}
	}
}

// sweepReaps asks again for every queued deletion, at most once a minute, so one whose node
// went away or failed is not forgotten.
func (s *Service) sweepReaps(ctx context.Context) {
	now := time.Now()
	if now.Sub(s.lastReapSweep) < reapEvery {
		return
	}
	s.lastReapSweep = now
	seen := map[string]bool{}
	for _, c := range s.reg.Snapshot() {
		if p := c.SandboxProvider; p != "" && !seen[p] {
			seen[p] = true
			s.ReapSandboxes(ctx, p)
		}
	}
}

// sandboxReaped records a node's answer to ReapSandbox, or its word that it deleted a
// task's sandbox itself. Only a node of the provider can clear one of its sandboxes.
func (s *Service) sandboxReaped(ctx context.Context, sess *Session, r *podiumv1.SandboxReaped) {
	if sess.snapshot().SandboxProvider != r.GetProvider() {
		s.logger.WarnContext(ctx, "ignoring a sandbox deletion from a node of another provider",
			"node_id", sess.nodeID, "provider", r.GetProvider())
		return
	}
	if r.GetError() != "" {
		s.logger.WarnContext(ctx, "a node could not delete a sandbox; asking again later",
			"node_id", sess.nodeID, "sandbox_id", r.GetSandboxId(), "error", r.GetError())
		return
	}
	if err := s.store.SandboxDeleted(ctx, r.GetProvider(), r.GetSandboxId()); err != nil {
		s.logger.WarnContext(ctx, "clearing a deleted sandbox failed", "sandbox_id", r.GetSandboxId(), "error", err)
		return
	}
	s.logger.DebugContext(ctx, "sandbox deleted",
		"node_id", sess.nodeID, "provider", r.GetProvider(), "sandbox_id", r.GetSandboxId())
}

// SandboxUsage is one provider's sandboxes.
type SandboxUsage struct {
	// InUse is the tasks its nodes run or are about to, plus the sandboxes still up after
	// their task: held previews, those being torn down and those waiting to be deleted.
	InUse int32
	// AccountLimit is the largest account cap any of its nodes reported, 0 unknown.
	AccountLimit int32
}

// SandboxUsage is every provider's sandboxes.
func (s *Service) SandboxUsage(ctx context.Context) map[string]SandboxUsage {
	kept, err := s.store.SandboxesKept(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "counting kept sandboxes failed", "error", err)
	}
	return SandboxUsageOf(s.reg.Snapshot(), kept)
}

// SandboxUsageOf sums the sandbox nodes among snapshots by provider, and adds what each
// provider keeps up with no task running in it.
func SandboxUsageOf(snaps []Snapshot, kept map[string]int32) map[string]SandboxUsage {
	out := map[string]SandboxUsage{}
	for name, n := range kept {
		out[name] = SandboxUsage{InUse: n}
	}
	for _, c := range snaps {
		if c.SandboxProvider == "" {
			continue
		}
		u := out[c.SandboxProvider]
		u.InUse += c.RunningTasks
		u.AccountLimit = max(u.AccountLimit, c.SandboxAccountLimit)
		out[c.SandboxProvider] = u
	}
	return out
}
