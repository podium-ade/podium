package nodes

// More than one podium-server on one database.
//
// A node opens one stream, to one server, and that server alone can send it anything. With a
// second server that is a problem for everything that is not an assignment: the API call that
// cancels a task, injects a follow-up or drains a node lands on whichever server the load
// balancer picked, and the scheduler's sweep on every server looks at every task.
//
// So each node's row says which server holds its stream (nodes.session_server), and a server
// that has a command for a node it does not hold RELAYS it: the command goes in node_commands,
// every server is woken by pg_notify, and the one holding the node takes the row — its delete
// is the acknowledgement — and sends the command down its stream. A command nobody takes
// within relayWait is withdrawn, and the caller hears ErrNoSession exactly as it does today.
//
// Assignments are never relayed: each server's scheduler places work on the nodes it holds,
// claiming from the shared queue, and AssignTask's transition is what makes two of them
// racing for one task safe.
//
// With no server id configured nothing here runs, and a server is the only one there is.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/podium-ade/podium/internal/server/store"
)

// relayWait bounds how long a relayed command waits for the server holding its node.
var relayWait = 2 * time.Second

// relayPoll is how often a relayed command's sender looks for its acknowledgement.
var relayPoll = 25 * time.Millisecond

// The commands a server relays to the one holding a node.
const (
	cmdCancel         = "cancel"
	cmdReleasePreview = "release_preview"
	cmdInject         = "inject"
	cmdEndWait        = "end_wait"
	cmdDrain          = "drain"
	cmdSlots          = "slots"
)

// nodeCommand is one relayed command, as node_commands.payload stores it.
type nodeCommand struct {
	Kind     string `json:"kind"`
	NodeID   string `json:"node_id"`
	TaskID   string `json:"task_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Text     string `json:"text,omitempty"`
	Draining bool   `json:"draining,omitempty"`
	MaxTasks int32  `json:"max_tasks,omitempty"`
}

// SetServerID names this server among the servers on its database, which turns sessions and
// relaying on. Empty, the default, is a server that is the only one.
func (s *Service) SetServerID(id string) { s.serverID = id }

// ServerID is this server's name among the servers on its database.
func (s *Service) ServerID() string { return s.serverID }

// HeldElsewhere reports whether another server holds node's stream and has heard from it
// recently: connected, just not here.
func (s *Service) HeldElsewhere(node store.Node) bool {
	if s.serverID == "" || node.SessionServer == "" || node.SessionServer == s.serverID {
		return false
	}
	if node.LastHeartbeatAt == nil {
		return false
	}
	return time.Since(*node.LastHeartbeatAt) < s.watchdog.UnreachableAfter
}

// OthersHoldNodes reports whether any other server holds a live node: work this server cannot
// place may well be placeable there.
func (s *Service) OthersHoldNodes(ctx context.Context) bool {
	if s.serverID == "" {
		return false
	}
	n, err := s.store.NodesHeldElsewhere(ctx, s.serverID, s.watchdog.UnreachableAfter)
	if err != nil {
		s.logger.WarnContext(ctx, "counting nodes held by other servers failed", "error", err)
		return false
	}
	return n > 0
}

// relay sends cmd through the server that holds its node, and waits for it to be taken.
func (s *Service) relay(ctx context.Context, cmd nodeCommand, what string) error {
	noSession := fmt.Errorf("%s on node %s: %w", what, cmd.NodeID, ErrNoSession)
	if s.serverID == "" {
		return noSession
	}
	node, err := s.store.GetNode(ctx, cmd.NodeID)
	if err != nil || !s.HeldElsewhere(node) {
		return noSession
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("%s on node %s: %w", what, cmd.NodeID, err)
	}
	id, err := s.store.EnqueueNodeCommand(ctx, cmd.NodeID, payload)
	if err != nil {
		if id == 0 {
			return fmt.Errorf("%s on node %s: %w", what, cmd.NodeID, err)
		}
		// Stored but not announced: the holder still finds it on its next sweep.
		s.logger.WarnContext(ctx, "announcing a relayed command failed", "node_id", cmd.NodeID, "error", err)
	}
	deadline := time.Now().Add(relayWait)
	for time.Now().Before(deadline) {
		waiting, err := s.store.NodeCommandWaiting(ctx, id)
		if err == nil && !waiting {
			s.logger.InfoContext(ctx, "relayed to the server holding the node",
				"kind", cmd.Kind, "node_id", cmd.NodeID, "task_id", cmd.TaskID, "server", node.SessionServer)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(relayPoll):
		}
	}
	dropped, err := s.store.DropNodeCommand(context.WithoutCancel(ctx), id)
	if err == nil && !dropped {
		return nil // taken at the last moment
	}
	return noSession
}

// RunRelay takes the commands other servers relay for the nodes this server holds, until ctx
// ends. A server with no id relays nothing and returns at once.
func (s *Service) RunRelay(ctx context.Context) error {
	if s.serverID == "" {
		return nil
	}
	ids, err := s.store.SubscribeNodeCommands(ctx)
	if err != nil {
		return fmt.Errorf("listen for relayed node commands: %w", err)
	}
	prune := time.NewTicker(time.Minute)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-prune.C:
			if err := s.store.PruneNodeCommands(ctx); err != nil && ctx.Err() == nil {
				s.logger.WarnContext(ctx, "pruning relayed node commands failed", "error", err)
			}
		case raw, ok := <-ids:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("the relayed node command subscription closed")
			}
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				continue
			}
			s.takeRelayed(ctx, id)
		}
	}
}

// takeRelayed sends command id if it is for a node this server holds.
func (s *Service) takeRelayed(ctx context.Context, id int64) {
	snaps := s.reg.Snapshot()
	if len(snaps) == 0 {
		return
	}
	held := make([]string, 0, len(snaps))
	for _, c := range snaps {
		held = append(held, c.NodeID)
	}
	payload, err := s.store.TakeNodeCommand(ctx, id, held)
	if err != nil || payload == nil {
		if err != nil {
			s.logger.WarnContext(ctx, "taking a relayed node command failed", "id", id, "error", err)
		}
		return
	}
	var cmd nodeCommand
	if err := json.Unmarshal(payload, &cmd); err != nil {
		s.logger.WarnContext(ctx, "a relayed node command does not parse", "id", id, "error", err)
		return
	}
	if err := s.execute(ctx, cmd); err != nil {
		s.logger.WarnContext(ctx, "sending a relayed node command failed",
			"kind", cmd.Kind, "node_id", cmd.NodeID, "task_id", cmd.TaskID, "error", err)
	}
}

// execute sends a relayed command down the stream this server holds.
func (s *Service) execute(ctx context.Context, cmd nodeCommand) error {
	switch cmd.Kind {
	case cmdCancel:
		return s.Cancel(ctx, cmd.NodeID, cmd.TaskID, cmd.Reason)
	case cmdReleasePreview:
		return s.ReleasePreview(ctx, cmd.NodeID, cmd.TaskID, cmd.Reason)
	case cmdInject:
		return s.Inject(ctx, cmd.NodeID, cmd.TaskID, cmd.Text)
	case cmdEndWait:
		return s.EndWait(ctx, cmd.NodeID, cmd.TaskID)
	case cmdDrain:
		return s.SetDrain(ctx, cmd.NodeID, cmd.Draining)
	case cmdSlots:
		return s.SetSlots(ctx, cmd.NodeID, cmd.MaxTasks)
	default:
		return fmt.Errorf("unknown relayed command %q", cmd.Kind)
	}
}
