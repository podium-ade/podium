// Package scheduler decides which node runs which task. MVP-0 ships the naive loop; step 12
// replaces it with lease expiry, reconciliation and real bin packing behind the same interface.
package scheduler

import (
	"context"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/nodes"
	"github.com/alvaroibarguen/podium/internal/server/secrets"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// Scheduler is the seam step 12 swaps. Run blocks until ctx is cancelled.
type Scheduler interface {
	Run(ctx context.Context) error
}

// Dispatcher is the node registry, as much of it as a scheduler needs.
type Dispatcher interface {
	// Candidates is one snapshot per connected node.
	Candidates() []nodes.Snapshot
	// Assign pushes an assignment onto a node's stream.
	Assign(ctx context.Context, nodeID string, a *podiumv1.Assign) error
}

// Resolver turns a task's secret references into the plaintext an Assign carries. It is
// called immediately before the assignment and never earlier: a value should exist in
// server memory for as short a time as possible.
type Resolver interface {
	Resolve(ctx context.Context, taskID string, refs []spec.SecretRef) ([]secrets.Resolved, error)
}
