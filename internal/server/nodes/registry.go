package nodes

import (
	"context"
	"errors"
	"sync"
	"time"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/store"
)

// sendBuffer is how many server messages may queue for a node before Send blocks. A node that
// is not draining its stream is a node that is gone; the stream's own errors handle that.
const sendBuffer = 64

// ErrSessionClosed is returned when a message is sent to a node whose stream has ended.
var ErrSessionClosed = errors.New("nodes: session closed")

// ErrNoSession is returned when a node has no live stream at all.
var ErrNoSession = errors.New("nodes: node has no live stream")

// Session is one live node stream. It is created by Stream, lives in the Registry for exactly
// as long as that stream, and is the only way to push work at a node.
type Session struct {
	nodeID string
	send   chan *podiumv1.ServerMessage
	done   chan struct{}
	once   sync.Once

	mu            sync.Mutex
	labels        []string
	capacity      store.NodeCapacity
	freeSlots     int32
	lastHeartbeat time.Time
	// running maps every task this session is accounted for to what it costs. The cost is
	// what makes "does this task fit?" answerable: max_tasks alone cannot tell a node with
	// four idle slots and 200MB left from one with four idle slots and 60GB.
	running map[string]TaskCost
	// draining is the operator's standing "no new work", mirrored from nodes.draining so
	// the scheduler need not read the row on every tick.
	draining bool
	// override is nodes.max_tasks_override: the slot count an operator set from the control
	// plane, or 0 for "the node's own max_tasks decides". It is held apart from capacity
	// because capacity is what the node advertised, and both numbers are worth reporting.
	override int32
	// lastAssignedAt breaks ties between equally free nodes, so a fleet fills evenly
	// instead of the lowest node ID taking everything.
	lastAssignedAt time.Time
}

// TaskCost is what one task takes out of a node's budget: the task container's limits plus
// every sidecar's, because a pod's sidecars run on the same machine as the task.
type TaskCost struct {
	CPU      float64
	MemoryMB int64
}

func newSession(
	nodeID string, labels []string, capacity store.NodeCapacity, override int32,
	running []string, draining bool,
) *Session {
	s := &Session{
		nodeID:   nodeID,
		send:     make(chan *podiumv1.ServerMessage, sendBuffer),
		done:     make(chan struct{}),
		labels:   append([]string(nil), labels...),
		capacity: capacity,
		override: override,
		running:  make(map[string]TaskCost, len(running)),
		draining: draining,
	}
	for _, id := range running {
		s.running[id] = TaskCost{}
	}
	s.freeSlots = s.budget() - int32(len(s.running))
	if s.freeSlots < 0 {
		s.freeSlots = 0
	}
	if draining {
		s.freeSlots = 0
	}
	s.lastHeartbeat = time.Now().UTC()
	return s
}

// budget is how many tasks this node may run at once: the operator's override when there is
// one, and what the node advertised otherwise. Callers hold s.mu.
func (s *Session) budget() int32 {
	if s.override > 0 {
		return s.override
	}
	return s.capacity.MaxTasks
}

// NodeID is the node this session belongs to.
func (s *Session) NodeID() string { return s.nodeID }

// Done is closed when the session ends, whether the node disconnected or the server replaced
// or shut it down.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close ends the session. It is idempotent.
func (s *Session) Close() { s.once.Do(func() { close(s.done) }) }

// Send queues a server message for the node.
func (s *Session) Send(ctx context.Context, msg *podiumv1.ServerMessage) error {
	select {
	case <-s.done:
		return ErrSessionClosed
	default:
	}
	select {
	case s.send <- msg:
		return nil
	case <-s.done:
		return ErrSessionClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// outbound is the channel the stream's writer goroutine drains.
func (s *Session) outbound() <-chan *podiumv1.ServerMessage { return s.send }

// observeHeartbeat records what the node last reported.
func (s *Session) observeHeartbeat(hb *podiumv1.Heartbeat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastHeartbeat = time.Now().UTC()
	s.freeSlots = hb.GetFreeSlots()
	if s.override > 0 {
		// A heartbeat already in flight when the cap was lowered was computed against the
		// old number, and taking it at face value would let the scheduler assign above the
		// cap for one interval. The node would reject that assignment and the task would be
		// requeued — correct, but a revoke nobody asked for. Only ever downwards, so a cap
		// cannot starve a node of the slots it was actually given.
		s.freeSlots = min(s.freeSlots, max(s.budget()-int32(len(s.running)), 0))
	}
	if n := hb.GetLoad().GetRunningTasks(); n >= 0 && s.override == 0 {
		// A node running more than it said it could hold has told us its advertised number
		// is wrong. An override is not wrong, it is an instruction — a node capped at two
		// while four tasks finish must not have the cap raised back out from under it.
		s.capacity.MaxTasks = max(s.capacity.MaxTasks, n)
	}
	if s.draining {
		// A heartbeat in flight when the drain was requested must not undo it.
		s.freeSlots = 0
	}
}

// reserve books a slot for taskID and charges its cost. The scheduler assigns faster than
// the node heartbeats, so without this a single tick could hand one node every queued task.
func (s *Session) reserve(taskID string, cost TaskCost) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running[taskID] = cost
	s.lastAssignedAt = time.Now().UTC()
	if s.freeSlots > 0 {
		s.freeSlots--
	}
}

// account rebuilds the session's cost bookkeeping from what the store says this node is
// running. It is called on reconciliation, where the node has just told us which tasks it
// holds and the store can say what each of them costs — a Hello carries ids, not specs.
func (s *Session) account(costs map[string]TaskCost) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.running {
		if c, ok := costs[id]; ok {
			s.running[id] = c
		}
	}
}

// forget drops a task from the session without pretending it ever had a slot. It is what
// reconciliation does with a task the node reported but the control plane has moved on.
func (s *Session) forget(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, taskID)
}

// setDraining mirrors the operator's instruction onto the live session. A draining node
// advertises no free slots, whatever its last heartbeat said, so the scheduler stops
// considering it the moment the drain is requested rather than up to a heartbeat later.
func (s *Session) setDraining(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.draining = v
	if v {
		s.freeSlots = 0
		return
	}
	// Undraining has to give the slots back now rather than at the next heartbeat, or a
	// node put back in the pool sits idle for up to ten seconds looking broken.
	s.freeSlots = max(s.budget()-int32(len(s.running)), 0)
}

// setMaxTasks records the operator's slot count on the live session, 0 for "no override".
// The node is told separately (Service.SetSlots) and its next heartbeat carries the new free
// count; this is what stops the scheduler waiting up to ten seconds for it, in both
// directions — a raise that nothing acts on looks broken, and a cut that the next tick
// ignores over-assigns the machine it was meant to protect.
func (s *Session) setMaxTasks(maxTasks int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.override = maxTasks
	if s.draining {
		return
	}
	s.freeSlots = max(s.budget()-int32(len(s.running)), 0)
}

// setLabels replaces what this session advertises to the scheduler. The labels were copied
// off the node's row when the stream opened, so without this a relabelled node would keep
// matching the old set until it reconnected.
func (s *Session) setLabels(labels []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.labels = append([]string(nil), labels...)
}

// release gives back the slot booked for taskID and reports whether this session held one.
// freeSlots is deliberately left alone: the node's heartbeat is authoritative for it, and
// reserve's local decrement is what stops the scheduler over-assigning between heartbeats.
func (s *Session) release(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[taskID]; !ok {
		return false
	}
	delete(s.running, taskID)
	return true
}

// Snapshot is what the scheduler and ListNodes read off a live session. It is a value, so
// a scheduler tick can decrement its own copy while it places a batch without holding a
// lock on the session or racing the node's next heartbeat.
type Snapshot struct {
	NodeID       string
	Labels       []string
	FreeSlots    int32
	RunningTasks int32
	LastSeen     time.Time
	Draining     bool
	Capacity     store.NodeCapacity
	// FreeCPU and FreeMemoryMB are what the node advertised minus what it is already
	// committed to. A capacity of zero means "unmeasured", not "none", and is treated as
	// no constraint: refusing every task because gopsutil could not count the cores would
	// be a worse failure than over-committing one.
	FreeCPU        float64
	FreeMemoryMB   int64
	LastAssignedAt time.Time
}

func (s *Session) snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	var usedCPU float64
	var usedMem int64
	for _, c := range s.running {
		usedCPU += c.CPU
		usedMem += c.MemoryMB
	}
	snap := Snapshot{
		NodeID:         s.nodeID,
		Labels:         append([]string(nil), s.labels...),
		FreeSlots:      s.freeSlots,
		RunningTasks:   int32(len(s.running)),
		LastSeen:       s.lastHeartbeat,
		Draining:       s.draining,
		Capacity:       s.capacity,
		LastAssignedAt: s.lastAssignedAt,
	}
	// MaxTasks on a snapshot is the budget, not the advertisement: everything that reads one
	// is asking how much work this node takes.
	snap.Capacity.MaxTasks = s.budget()
	if s.capacity.CPUCores > 0 {
		snap.FreeCPU = float64(s.capacity.CPUCores) - usedCPU
	}
	if s.capacity.MemoryMB > 0 {
		snap.FreeMemoryMB = s.capacity.MemoryMB - usedMem
	}
	return snap
}

// Registry holds the live node sessions of this server process. It is deliberately in memory
// and single-process: a node's stream terminates on one server, so that server is the only one
// that can reach it.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Session)}
}

// add installs sess as the node's session and closes whatever it replaced. A node that
// reconnects before the server noticed the old stream died must not end up with two.
func (r *Registry) add(sess *Session) {
	r.mu.Lock()
	old := r.sessions[sess.nodeID]
	r.sessions[sess.nodeID] = sess
	r.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// remove drops sess and reports whether it was still the current session for that node. A
// stream that was replaced by a reconnect removes nothing, which is what keeps it from marking
// a node that is online again as unreachable.
func (r *Registry) remove(sess *Session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[sess.nodeID] != sess {
		return false
	}
	delete(r.sessions, sess.nodeID)
	return true
}

// Get returns the live session of a node.
func (r *Registry) Get(nodeID string) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[nodeID]
	return s, ok
}

// Snapshot returns one Snapshot per connected node.
func (r *Registry) Snapshot() []Snapshot {
	r.mu.Lock()
	sessions := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()

	out := make([]Snapshot, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.snapshot())
	}
	return out
}

// SnapshotOf returns the live snapshot of one node.
func (r *Registry) SnapshotOf(nodeID string) (Snapshot, bool) {
	s, ok := r.Get(nodeID)
	if !ok {
		return Snapshot{}, false
	}
	return s.snapshot(), true
}

// Drain marks a node's live session draining (or undraining) and reports whether it had one.
func (r *Registry) Drain(nodeID string, draining bool) bool {
	s, ok := r.Get(nodeID)
	if !ok {
		return false
	}
	s.setDraining(draining)
	return true
}

// SetLabels replaces a node's live labels and reports whether it had a session. A node that
// is not connected needs nothing: the next stream reads the labels off its row.
func (r *Registry) SetLabels(nodeID string, labels []string) bool {
	s, ok := r.Get(nodeID)
	if !ok {
		return false
	}
	s.setLabels(labels)
	return true
}

// Forget drops a task from whichever session claims it, without crediting a slot back. It
// is what reconciliation does with a container the control plane no longer owns.
func (r *Registry) Forget(nodeID, taskID string) {
	if s, ok := r.Get(nodeID); ok {
		s.forget(taskID)
	}
}

// Release frees the slot whichever session is holding taskID booked for it. A task no session
// holds is a no-op: replayed terminal events are routine, and so is a node that reconnected and
// rebuilt its running set from Hello in the meantime.
func (r *Registry) Release(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.sessions {
		if s.release(taskID) {
			return
		}
	}
}

// CloseAll ends every session. Graceful shutdown calls it so node stream handlers return
// before the HTTP server waits on them; the nodes reconnect to whoever comes back up.
func (r *Registry) CloseAll() {
	r.mu.Lock()
	sessions := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
}
