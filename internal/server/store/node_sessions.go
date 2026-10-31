package store

// Which podium-server holds each node's stream, and the commands relayed to it. See migration
// 0018 and internal/server/nodes/relay.go.

import (
	"context"
	"fmt"
	"time"

	db "github.com/podium-ade/podium/internal/server/store/db"
)

// NodeCommandsChannel wakes every server when a command is queued for a node another server
// may hold. The payload is the command's id.
const NodeCommandsChannel = "podium_node_commands"

// ClaimNodeSession records that server holds node's stream.
func (s *Store) ClaimNodeSession(ctx context.Context, nodeID, server string) error {
	if err := s.q.ClaimNodeSession(ctx, db.ClaimNodeSessionParams{Server: server, ID: nodeID}); err != nil {
		return fmt.Errorf("claim the session of node %s: %w", nodeID, err)
	}
	return nil
}

// EndNodeSession marks node unreachable as server's stream to it ends, and reports whether it
// did: a node that reconnected to another server meanwhile is left as that server has it.
func (s *Store) EndNodeSession(ctx context.Context, nodeID, server string) (bool, error) {
	n, err := s.q.EndNodeSession(ctx, db.EndNodeSessionParams{ID: nodeID, Server: server})
	if err != nil {
		return false, fmt.Errorf("end the session of node %s: %w", nodeID, err)
	}
	return n > 0, nil
}

// NodesHeldElsewhere is how many nodes a server other than server holds and has heard from
// within fresh.
func (s *Store) NodesHeldElsewhere(ctx context.Context, server string, fresh time.Duration) (int, error) {
	n, err := s.q.NodesHeldElsewhere(ctx, db.NodesHeldElsewhereParams{Server: server, FreshSeconds: int32(fresh.Seconds())})
	if err != nil {
		return 0, fmt.Errorf("count nodes held by other servers: %w", err)
	}
	return int(n), nil
}

// ActiveSandboxTasks is how many tasks each sandbox provider's nodes are working on, across
// every server.
func (s *Store) ActiveSandboxTasks(ctx context.Context) (map[string]int32, error) {
	rows, err := s.q.ActiveSandboxTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("count active sandbox tasks: %w", err)
	}
	out := make(map[string]int32, len(rows))
	for _, r := range rows {
		out[r.Provider] = r.Active
	}
	return out, nil
}

// EnqueueNodeCommand stores a command for nodeID and wakes every server.
func (s *Store) EnqueueNodeCommand(ctx context.Context, nodeID string, payload []byte) (int64, error) {
	id, err := s.q.EnqueueNodeCommand(ctx, db.EnqueueNodeCommandParams{NodeID: nodeID, Payload: payload})
	if err != nil {
		return 0, fmt.Errorf("queue a command for node %s: %w", nodeID, err)
	}
	if _, err := s.pool.Exec(ctx, "select pg_notify($1, $2)", NodeCommandsChannel, fmt.Sprint(id)); err != nil {
		return id, fmt.Errorf("announce a command for node %s: %w", nodeID, err)
	}
	return id, nil
}

// TakeNodeCommand claims command id when it is for one of nodeIDs: whoever takes it sends it.
// Nil when it is not, or another server took it.
func (s *Store) TakeNodeCommand(ctx context.Context, id int64, nodeIDs []string) ([]byte, error) {
	payload, err := s.q.TakeNodeCommand(ctx, db.TakeNodeCommandParams{ID: id, NodeIds: nodeIDs})
	if noRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("take node command %d: %w", id, err)
	}
	return payload, nil
}

// NodeCommandWaiting reports whether command id is still waiting to be taken.
func (s *Store) NodeCommandWaiting(ctx context.Context, id int64) (bool, error) {
	ok, err := s.q.NodeCommandWaiting(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read node command %d: %w", id, err)
	}
	return ok, nil
}

// DropNodeCommand withdraws command id, and reports whether it was still waiting: false means
// a server took it after all.
func (s *Store) DropNodeCommand(ctx context.Context, id int64) (bool, error) {
	n, err := s.q.DropNodeCommand(ctx, id)
	if err != nil {
		return false, fmt.Errorf("withdraw node command %d: %w", id, err)
	}
	return n > 0, nil
}

// PruneNodeCommands deletes commands nobody took and nobody withdrew.
func (s *Store) PruneNodeCommands(ctx context.Context) error {
	if err := s.q.PruneNodeCommands(ctx); err != nil {
		return fmt.Errorf("prune node commands: %w", err)
	}
	return nil
}

// SubscribeNodeCommands streams the id of every command queued for a node. Like the other
// subscriptions it hijacks one connection per server.
func (s *Store) SubscribeNodeCommands(ctx context.Context) (<-chan string, error) {
	return s.subscribe(ctx, NodeCommandsChannel)
}
