package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"

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
		n.sandbox.Disable()
		n.logger.WarnContext(ctx, "sandbox provider is not configured on the control plane; taking no work",
			"provider", c.GetProvider())
		n.signal()
		return
	}
	cfg := providerSettings(c.GetSettings())
	cfg["secret"] = string(c.GetSecret())
	p, err := sandbox.New(c.GetProvider(), cfg)
	if err != nil {
		n.sandbox.Disable()
		n.logger.ErrorContext(ctx, "the control plane's sandbox config does not work; taking no work",
			"provider", c.GetProvider(), "error", err)
		n.signal()
		return
	}
	n.sandbox.Set(p)
	// The template is the executor's, read when it plans each task: the provider never
	// sees it. One the admin saved wins over the node's own file.
	if e, ok := n.exec.(interface{ SetTemplate(string) }); ok {
		tmpl := n.cfg.Sandbox.Template
		if t := c.GetSettings()["template"]; t != "" {
			tmpl = t
		}
		e.SetTemplate(tmpl)
	}
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

// reapTimeout bounds one sandbox deletion; a provider that polls its delete operation can
// take a while.
const reapTimeout = 2 * time.Minute

// reaper is the executor of a sandbox node.
type reaper interface {
	Reap(ctx context.Context, id string) error
	AccountLimit() int32
	OnDeleted(fn func(id string))
}

// sandboxDeleted tells the control plane a task's sandbox is gone, so it stops counting
// against the provider's cap.
func (n *Node) sandboxDeleted(id string) {
	n.mu.Lock()
	n.reaped = append(n.reaped, &podiumv1.SandboxReaped{Provider: n.cfg.Sandbox.Provider, SandboxId: id})
	n.mu.Unlock()
	n.signal()
}

// reapSandbox deletes a sandbox the control plane gave up on, in the background, and queues
// the answer for the stream. A node of another provider, or one already deleting it, does
// nothing: the control plane asks again.
func (n *Node) reapSandbox(ctx context.Context, r *podiumv1.ReapSandbox) {
	x, ok := n.exec.(reaper)
	if !ok || r.GetProvider() != n.cfg.Sandbox.Provider {
		return
	}
	id := r.GetSandboxId()
	n.mu.Lock()
	if n.reaping == nil {
		n.reaping = map[string]bool{}
	}
	if n.reaping[id] {
		n.mu.Unlock()
		return
	}
	n.reaping[id] = true
	n.mu.Unlock()
	go func() {
		rctx, cancel := context.WithTimeout(n.runCtx, reapTimeout)
		defer cancel()
		out := &podiumv1.SandboxReaped{Provider: r.GetProvider(), SandboxId: id}
		if err := x.Reap(rctx, id); err != nil {
			out.Error = err.Error()
			n.logger.WarnContext(ctx, "deleting a sandbox for the control plane failed",
				"sandbox_id", id, "task_id", r.GetTaskId(), "error", err)
		} else {
			n.logger.InfoContext(ctx, "deleted a sandbox the control plane gave up on",
				"sandbox_id", id, "task_id", r.GetTaskId())
		}
		n.mu.Lock()
		delete(n.reaping, id)
		n.reaped = append(n.reaped, out)
		n.mu.Unlock()
		n.signal()
	}()
}

// flushReaped sends the answers to finished reaps. Whatever a failed send leaves is sent on
// the next stream.
func (n *Node) flushReaped(stream *connect.BidiStreamForClient[podiumv1.NodeMessage, podiumv1.ServerMessage]) error {
	n.mu.Lock()
	out := n.reaped
	n.reaped = nil
	n.mu.Unlock()
	for i, r := range out {
		if err := stream.Send(&podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_SandboxReaped{SandboxReaped: r}}); err != nil {
			n.mu.Lock()
			n.reaped = append(out[i:], n.reaped...)
			n.mu.Unlock()
			return fmt.Errorf("send sandbox reaped %s: %w", r.GetSandboxId(), err)
		}
	}
	return nil
}
