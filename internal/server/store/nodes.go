package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/podium-ade/podium/internal/ids"
	"github.com/podium-ade/podium/internal/server/store/db"
)

// CreateNode registers an enrolled node. NodeKeyHash is the SHA-256 of the node key; the key
// itself is returned to the node once by the API and never stored.
func (s *Store) CreateNode(ctx context.Context, in NewNode) (Node, error) {
	if in.ID == "" {
		in.ID = ids.NewNode()
	}
	if in.Status == "" {
		in.Status = NodeOffline
	}
	if len(in.NodeKeyHash) == 0 {
		return Node{}, fmt.Errorf("create node %s: node key hash is required", in.ID)
	}
	labelsJSON, err := json.Marshal(nonNilStrings(in.Labels))
	if err != nil {
		return Node{}, fmt.Errorf("marshal node labels: %w", err)
	}
	capacityJSON, err := json.Marshal(in.Capacity)
	if err != nil {
		return Node{}, fmt.Errorf("marshal node capacity: %w", err)
	}
	row, err := s.q.CreateNode(ctx, db.CreateNodeParams{
		ID:          in.ID,
		Name:        in.Name,
		Tags:        nonNilStrings(in.Tags),
		Labels:      labelsJSON,
		Capacity:    capacityJSON,
		NodeKeyHash: in.NodeKeyHash,
		Status:      string(in.Status),
		Version:     ptr(in.Version),
		TsStableID:  ptr(in.TSStableID),
	})
	if err != nil {
		return Node{}, fmt.Errorf("insert node %s: %w", in.ID, err)
	}
	return nodeFromRow(row)
}

// GetNode returns one node, or ErrNotFound.
func (s *Store) GetNode(ctx context.Context, nodeID string) (Node, error) {
	row, err := s.q.GetNode(ctx, nodeID)
	if noRows(err) {
		return Node{}, fmt.Errorf("node %s: %w", nodeID, ErrNotFound)
	}
	if err != nil {
		return Node{}, fmt.Errorf("select node %s: %w", nodeID, err)
	}
	return nodeFromRow(row)
}

// GetNodeByKeyHash authenticates a node stream: look the node up by the SHA-256 of the key it
// presented.
func (s *Store) GetNodeByKeyHash(ctx context.Context, keyHash []byte) (Node, error) {
	row, err := s.q.GetNodeByKeyHash(ctx, keyHash)
	if noRows(err) {
		return Node{}, fmt.Errorf("node key: %w", ErrNotFound)
	}
	if err != nil {
		return Node{}, fmt.Errorf("select node by key hash: %w", err)
	}
	return nodeFromRow(row)
}

// ListNodes returns every node, oldest first.
func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.q.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	out := make([]Node, 0, len(rows))
	for _, r := range rows {
		n, err := nodeFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// UpdateNodeHeartbeat stamps last_heartbeat_at and refreshes the advertised capacity and
// version. A nil capacity or an empty version leaves that column alone.
func (s *Store) UpdateNodeHeartbeat(ctx context.Context, nodeID string, status NodeStatus, capacity *NodeCapacity, version string) error {
	var capacityJSON []byte
	if capacity != nil {
		b, err := json.Marshal(capacity)
		if err != nil {
			return fmt.Errorf("marshal node capacity: %w", err)
		}
		capacityJSON = b
	}
	_, err := s.q.UpdateNodeHeartbeat(ctx, db.UpdateNodeHeartbeatParams{
		Status:   string(status),
		Capacity: capacityJSON,
		Version:  ptr(version),
		ID:       nodeID,
	})
	if noRows(err) {
		return fmt.Errorf("node %s: %w", nodeID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("update heartbeat of node %s: %w", nodeID, err)
	}
	return nil
}

// SetNodeStatus changes only the status column.
func (s *Store) SetNodeStatus(ctx context.Context, nodeID string, status NodeStatus) error {
	n, err := s.q.SetNodeStatus(ctx, db.SetNodeStatusParams{Status: string(status), ID: nodeID})
	if err != nil {
		return fmt.Errorf("set status of node %s: %w", nodeID, err)
	}
	if n == 0 {
		return fmt.Errorf("node %s: %w", nodeID, ErrNotFound)
	}
	return nil
}

// SetNodeTSStableID binds a node to one Tailscale device, or unbinds it when stableID is empty
// (that is what `podium node rekey` does). The binding is what stops a stolen identity.json from
// working anywhere but the machine it was issued to.
func (s *Store) SetNodeTSStableID(ctx context.Context, nodeID, stableID string) error {
	n, err := s.q.SetNodeTSStableID(ctx, db.SetNodeTSStableIDParams{TsStableID: ptr(stableID), ID: nodeID})
	if err != nil {
		return fmt.Errorf("bind node %s to a tailscale device: %w", nodeID, err)
	}
	if n == 0 {
		return fmt.Errorf("node %s: %w", nodeID, ErrNotFound)
	}
	return nil
}

// SetNodeDraining records the operator's standing instruction that a node takes no new
// work, or clears it. It is a column, not a status: a draining node that disconnects is
// still draining when it comes back, and so is one whose control plane restarted.
func (s *Store) SetNodeDraining(ctx context.Context, nodeID string, draining bool) error {
	n, err := s.q.SetNodeDraining(ctx, db.SetNodeDrainingParams{Draining: draining, ID: nodeID})
	if err != nil {
		return fmt.Errorf("set draining of node %s: %w", nodeID, err)
	}
	if n == 0 {
		return fmt.Errorf("node %s: %w", nodeID, ErrNotFound)
	}
	return nil
}

// SetNodeMaxTasks records how many tasks an operator wants a node to run at once, or clears
// the instruction when maxTasks is nil. Like SetNodeDraining it is a column: the node's own
// max_tasks arrives in every Hello and would overwrite anything written into capacity, and an
// operator who caps a machine means it for the machine rather than for one connection.
func (s *Store) SetNodeMaxTasks(ctx context.Context, nodeID string, maxTasks *int32) error {
	n, err := s.q.SetNodeMaxTasks(ctx, db.SetNodeMaxTasksParams{MaxTasksOverride: maxTasks, ID: nodeID})
	if err != nil {
		return fmt.Errorf("set slots of node %s: %w", nodeID, err)
	}
	if n == 0 {
		return fmt.Errorf("node %s: %w", nodeID, ErrNotFound)
	}
	return nil
}

// SetNodeLabels replaces a node's labels and returns the row it wrote. Labels decide what
// work a node is eligible for, and they are set at enrollment — this is how an operator
// changes them afterwards without a re-enrollment. The caller decides what the new set is;
// the column holds exactly what it is given.
func (s *Store) SetNodeLabels(ctx context.Context, nodeID string, labels []string) (Node, error) {
	labelsJSON, err := json.Marshal(nonNilStrings(labels))
	if err != nil {
		return Node{}, fmt.Errorf("marshal node labels: %w", err)
	}
	row, err := s.q.SetNodeLabels(ctx, db.SetNodeLabelsParams{Labels: labelsJSON, ID: nodeID})
	if noRows(err) {
		return Node{}, fmt.Errorf("node %s: %w", nodeID, ErrNotFound)
	}
	if err != nil {
		return Node{}, fmt.Errorf("set labels of node %s: %w", nodeID, err)
	}
	return nodeFromRow(row)
}

// DeleteNode removes a node. Its finished tasks keep the node id they ran on: that column
// stopped being a foreign key in 0004_scheduler.sql, because a live inventory and an
// append-only history do not belong in a referential relationship. Refusing to delete a node
// that still has *running* tasks is the API's job, not the schema's.
func (s *Store) DeleteNode(ctx context.Context, nodeID string) error {
	n, err := s.q.DeleteNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("delete node %s: %w", nodeID, err)
	}
	if n == 0 {
		return fmt.Errorf("node %s: %w", nodeID, ErrNotFound)
	}
	return nil
}

func nodeFromRow(row db.Node) (Node, error) {
	n := Node{
		ID:               row.ID,
		Name:             row.Name,
		Tags:             row.Tags,
		NodeKeyHash:      row.NodeKeyHash,
		Status:           NodeStatus(row.Status),
		Version:          deref(row.Version),
		LastHeartbeatAt:  utcPtr(row.LastHeartbeatAt),
		CreatedAt:        row.CreatedAt.UTC(),
		TSStableID:       deref(row.TsStableID),
		Draining:         row.Draining,
		MaxTasksOverride: row.MaxTasksOverride,
	}
	if err := json.Unmarshal(row.Labels, &n.Labels); err != nil {
		return Node{}, fmt.Errorf("decode labels of node %s: %w", row.ID, err)
	}
	if err := json.Unmarshal(row.Capacity, &n.Capacity); err != nil {
		return Node{}, fmt.Errorf("decode capacity of node %s: %w", row.ID, err)
	}
	return n, nil
}

// nonNilStrings keeps a nil slice out of the database: text[] and jsonb columns are NOT NULL.
func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
