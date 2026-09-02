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
	running       map[string]struct{}
}

func newSession(nodeID string, labels []string, capacity store.NodeCapacity, running []string) *Session {
	s := &Session{
		nodeID:   nodeID,
		send:     make(chan *podiumv1.ServerMessage, sendBuffer),
		done:     make(chan struct{}),
		labels:   append([]string(nil), labels...),
		capacity: capacity,
		running:  make(map[string]struct{}, len(running)),
	}
	for _, id := range running {
		s.running[id] = struct{}{}
	}
	s.freeSlots = capacity.MaxTasks - int32(len(s.running))
	if s.freeSlots < 0 {
		s.freeSlots = 0
	}
	s.lastHeartbeat = time.Now().UTC()
	return s
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
	if n := hb.GetLoad().GetRunningTasks(); n >= 0 {
		s.capacity.MaxTasks = max(s.capacity.MaxTasks, n)
	}
}

// reserve books a slot for taskID. The scheduler assigns faster than the node heartbeats, so
// without this a single 1s tick could hand one node every queued task.
func (s *Session) reserve(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running[taskID] = struct{}{}
	if s.freeSlots > 0 {
		s.freeSlots--
	}
}

// Snapshot is what the scheduler and ListNodes read off a live session.
type Snapshot struct {
	NodeID       string
	Labels       []string
	FreeSlots    int32
	RunningTasks int32
	LastSeen     time.Time
}

func (s *Session) snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		NodeID:       s.nodeID,
		Labels:       append([]string(nil), s.labels...),
		FreeSlots:    s.freeSlots,
		RunningTasks: int32(len(s.running)),
		LastSeen:     s.lastHeartbeat,
	}
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
