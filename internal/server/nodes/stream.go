package nodes

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/server/store"
	"github.com/alvaroibarguen/podium/internal/transport"
)

// received is one result of stream.Receive, handed to the handler by the reader goroutine so
// the handler can also wait on the session ending.
type received struct {
	msg *podiumv1.NodeMessage
	err error
}

// Stream is the steady-state node stream. The first message must be a Hello carrying a valid
// node key within five seconds; after that the node heartbeats and pushes task events, and the
// server pushes assignments, acks and cancels.
func (s *Service) Stream(
	ctx context.Context,
	stream *connect.BidiStream[podiumv1.NodeMessage, podiumv1.ServerMessage],
) error {
	// Receive blocks on the request body, which nothing but the handler returning can
	// interrupt, so it runs in its own goroutine and the handler selects.
	incoming := make(chan received, 1)
	readerDone := make(chan struct{})
	defer close(readerDone)
	go func() {
		for {
			msg, err := stream.Receive()
			select {
			case incoming <- received{msg, err}:
			case <-readerDone:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	hello, err := awaitHello(ctx, incoming)
	if err != nil {
		return err
	}
	node, err := s.authenticate(ctx, hello)
	if err != nil {
		return err
	}
	if err := s.checkDeviceBinding(ctx, node); err != nil {
		return err
	}

	sess := newSession(node.ID, node.Labels, capacityOf(hello, node), hello.GetRunningTaskIds(), node.Draining)
	s.reg.add(sess)
	defer s.endSession(ctx, sess)

	online := store.NodeOnline
	if node.Draining {
		online = store.NodeDraining
	}
	if err := s.store.UpdateNodeHeartbeat(ctx, node.ID, online, ptrCapacity(hello), hello.GetVersion()); err != nil {
		s.logger.WarnContext(ctx, "marking node online failed", "node_id", node.ID, "error", err)
	}
	s.logger.InfoContext(ctx, "node stream open",
		"node_id", node.ID, "labels", node.Labels, "version", hello.GetVersion(),
		"running_tasks", len(hello.GetRunningTaskIds()), "draining", node.Draining)

	writerErr := make(chan error, 1)
	go func() { writerErr <- writeLoop(ctx, stream, sess) }()

	// Reconciliation is the first thing on the wire after Hello, and the node waits for it
	// before it adopts anything: only the control plane knows what it has committed. The
	// HelloAck goes out before the Cancels it implies, so a node reading its stream in
	// order always learns the verdict before it is told to act on one.
	checkpoints, orphans := s.reconcile(ctx, sess, node, hello.GetRunningTaskIds())
	ack := &podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_HelloAck{
		HelloAck: &podiumv1.HelloAck{Tasks: checkpoints},
	}}
	if err := sess.Send(ctx, ack); err != nil {
		s.logger.WarnContext(ctx, "sending HelloAck failed", "node_id", node.ID, "error", err)
	}
	for _, taskID := range orphans {
		if err := s.Cancel(ctx, node.ID, taskID, CancelReasonNotOurs); err != nil {
			s.logger.WarnContext(ctx, "could not ask a node to drop an orphan",
				"node_id", node.ID, "task_id", taskID, "error", err)
		}
	}
	if node.Draining {
		if err := sess.Send(ctx, &podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_Drain{
			Drain: &podiumv1.Drain{},
		}}); err != nil {
			s.logger.WarnContext(ctx, "re-sending drain to a draining node failed", "node_id", node.ID, "error", err)
		}
	}

	err = s.readLoop(ctx, sess, incoming)
	sess.Close()
	<-writerErr
	return err
}

// awaitHello reads the opening message and insists it is a Hello.
func awaitHello(ctx context.Context, incoming <-chan received) (*podiumv1.Hello, error) {
	timer := time.NewTimer(helloDeadline)
	defer timer.Stop()
	select {
	case r := <-incoming:
		if r.err != nil {
			return nil, r.err
		}
		hello := r.msg.GetHello()
		if hello == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("stream: first message must be Hello"))
		}
		return hello, nil
	case <-timer.C:
		return nil, connect.NewError(connect.CodeDeadlineExceeded,
			fmt.Errorf("stream: no Hello within %s", helloDeadline))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// authenticate resolves the node key Hello presented and checks it names the node it claims.
func (s *Service) authenticate(ctx context.Context, hello *podiumv1.Hello) (store.Node, error) {
	if hello.GetNodeId() == "" || hello.GetNodeKey() == "" {
		return store.Node{}, connect.NewError(connect.CodeUnauthenticated,
			errors.New("stream: Hello needs node_id and node_key"))
	}
	node, err := s.store.GetNodeByKeyHash(ctx, store.HashToken(hello.GetNodeKey()))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Node{}, connect.NewError(connect.CodeUnauthenticated,
				errors.New("stream: unknown node key"))
		}
		return store.Node{}, connect.NewError(connect.CodeInternal, fmt.Errorf("stream: %w", err))
	}
	if node.ID != hello.GetNodeId() {
		return store.Node{}, connect.NewError(connect.CodeUnauthenticated,
			errors.New("stream: node key does not belong to that node"))
	}
	return node, nil
}

// checkDeviceBinding enforces that a node keeps reconnecting from the Tailscale device it
// enrolled from. The node key alone is a bearer credential: anyone who copies identity.json owns
// the node. Pinning the device means a stolen key is only usable from the machine it was issued
// to, which is a machine the operator already controls.
//
// A node with no binding is bound on the first tailnet Hello it sends — that is how a node that
// enrolled over the dev transport, or one an admin has rekeyed, picks up its device. Under the
// dev transport there is no device to bind to, so nothing happens at all.
func (s *Service) checkDeviceBinding(ctx context.Context, node store.Node) error {
	id, _ := transport.From(ctx)
	switch decideBinding(node.TSStableID, id.NodeStableID) {
	case bindingBind:
		if err := s.store.SetNodeTSStableID(ctx, node.ID, id.NodeStableID); err != nil {
			s.logger.WarnContext(ctx, "binding node to its tailscale device failed",
				"node_id", node.ID, "error", err)
			return nil
		}
		s.logger.InfoContext(ctx, "node bound to a tailscale device",
			"node_id", node.ID, "ts_stable_id", id.NodeStableID)
	case bindingReject:
		s.logger.WarnContext(ctx, "rejecting a node key presented from the wrong tailscale device",
			"node_id", node.ID, "enrolled_from", node.TSStableID, "presented_from", id.NodeStableID,
			"remote_addr", id.RemoteAddr)
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"stream: node %s is bound to another Tailscale device; if this machine really "+
				"replaces it, run `podium node rekey %s` and reconnect", node.ID, node.ID))
	case bindingNoop:
	}
	return nil
}

// bindingDecision is what a Hello does to a node's device binding.
type bindingDecision int

const (
	// bindingNoop: nothing to bind and nothing to check — the dev transport, or a node
	// already on its own device.
	bindingNoop bindingDecision = iota
	// bindingBind: the node has no device yet and the caller has one. Trust on first use.
	bindingBind
	// bindingReject: the node key arrived from a device that is not the one it is bound to.
	bindingReject
)

// decideBinding is the whole rule, in one place so the table of cases is testable.
//
// stored is nodes.ts_stable_id; presented is the Tailscale device the current connection came
// from, empty under the dev transport. An unbound node binds to whatever device it first
// arrives from — that is how a node enrolled over the dev transport, or one an admin has
// rekeyed, picks up its device. A bound node must keep arriving from the same one.
func decideBinding(stored, presented string) bindingDecision {
	switch {
	case presented == "":
		return bindingNoop
	case stored == "":
		return bindingBind
	case stored != presented:
		return bindingReject
	default:
		return bindingNoop
	}
}

// endSession unregisters the stream and, unless it was already replaced by a reconnect, marks
// the node unreachable. The watchdog decides when it is offline and expires its leases.
func (s *Service) endSession(ctx context.Context, sess *Session) {
	sess.Close()
	if !s.reg.remove(sess) {
		return
	}
	// The request context is already dead on a disconnect, and on shutdown it is cancelled.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.SetNodeStatus(bg, sess.nodeID, store.NodeUnreachable); err != nil {
		s.logger.WarnContext(bg, "marking node unreachable failed", "node_id", sess.nodeID, "error", err)
	}
	s.logger.InfoContext(bg, "node stream closed", "node_id", sess.nodeID)
}

// writeLoop drains the session's outbound queue onto the stream.
func writeLoop(ctx context.Context, stream *connect.BidiStream[podiumv1.NodeMessage, podiumv1.ServerMessage], sess *Session) error {
	for {
		select {
		case msg := <-sess.outbound():
			if err := stream.Send(msg); err != nil {
				sess.Close()
				return err
			}
		case <-sess.Done():
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// readLoop handles node messages until the stream ends. Task events are coalesced into batches
// (100ms or 64KB, whichever comes first) so a chatty task costs one transaction per batch and
// one Ack per batch rather than per line.
func (s *Service) readLoop(ctx context.Context, sess *Session, incoming <-chan received) error {
	b := newBatcher()
	for {
		var flush <-chan time.Time
		if b.pending() {
			// Measured from the first buffered event, so a node that never stops talking
			// still gets a flush every 100ms.
			flush = time.After(time.Until(b.since.Add(flushInterval)))
		}
		select {
		case r := <-incoming:
			if r.err != nil {
				// io.EOF is the node closing its half cleanly; anything else is the
				// connection breaking. Either way the stream is over and the deferred
				// endSession marks the node unreachable.
				s.flush(ctx, sess, b)
				return nil
			}
			switch {
			case r.msg.GetHeartbeat() != nil:
				s.handleHeartbeat(ctx, sess, r.msg.GetHeartbeat())
			case r.msg.GetTaskEvent() != nil:
				b.add(r.msg.GetTaskEvent())
				if b.bytes >= flushBytes {
					s.flush(ctx, sess, b)
				}
			case r.msg.GetHello() != nil:
				s.logger.WarnContext(ctx, "ignoring second Hello on an open stream", "node_id", sess.nodeID)
			default:
				s.logger.WarnContext(ctx, "ignoring unknown node message", "node_id", sess.nodeID)
			}
		case <-flush:
			s.flush(ctx, sess, b)
		case <-sess.Done():
			s.flush(ctx, sess, b)
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Service) handleHeartbeat(ctx context.Context, sess *Session, hb *podiumv1.Heartbeat) {
	sess.observeHeartbeat(hb)
	status := store.NodeOnline
	if sess.snapshot().Draining {
		status = store.NodeDraining
	}
	if err := s.store.UpdateNodeHeartbeat(ctx, sess.nodeID, status, nil, ""); err != nil {
		s.logger.WarnContext(ctx, "heartbeat update failed", "node_id", sess.nodeID, "error", err)
	}
}

// flush persists every buffered batch and acks each task's highest seq. An ingest that fails is
// not acked, so the node keeps the events in its replay buffer and sends them again.
func (s *Service) flush(ctx context.Context, sess *Session, b *batcher) {
	for _, taskID := range b.order {
		events := b.byTask[taskID]
		ackSeq, err := s.logs.Ingest(ctx, taskID, events)
		if err != nil {
			s.logger.ErrorContext(ctx, "ingesting task events failed",
				"node_id", sess.nodeID, "task_id", taskID, "events", len(events), "error", err)
			continue
		}
		ack := &podiumv1.ServerMessage{Msg: &podiumv1.ServerMessage_Ack{
			Ack: &podiumv1.Ack{TaskId: taskID, Seq: ackSeq},
		}}
		if err := sess.Send(ctx, ack); err != nil && !errors.Is(err, ErrSessionClosed) {
			s.logger.WarnContext(ctx, "sending ack failed",
				"node_id", sess.nodeID, "task_id", taskID, "seq", ackSeq, "error", err)
		}
	}
	b.reset()
}

// batcher groups the task events of one stream by task, preserving arrival order across tasks.
type batcher struct {
	byTask map[string][]*podiumv1.TaskEvent
	order  []string
	bytes  int
	since  time.Time
}

func newBatcher() *batcher {
	return &batcher{byTask: make(map[string][]*podiumv1.TaskEvent)}
}

func (b *batcher) add(e *podiumv1.TaskEvent) {
	if !b.pending() {
		b.since = time.Now()
	}
	id := e.GetTaskId()
	if _, ok := b.byTask[id]; !ok {
		b.order = append(b.order, id)
	}
	b.byTask[id] = append(b.byTask[id], e)
	b.bytes += len(e.GetLog().GetBytes()) + 64
}

func (b *batcher) pending() bool { return len(b.order) > 0 }

func (b *batcher) reset() {
	b.byTask = make(map[string][]*podiumv1.TaskEvent)
	b.order = b.order[:0]
	b.bytes = 0
}

// capacityOf is what the node advertised in Hello, falling back to what enrollment recorded.
func capacityOf(hello *podiumv1.Hello, node store.Node) store.NodeCapacity {
	c := ptrCapacity(hello)
	if c == nil {
		return node.Capacity
	}
	return *c
}

func ptrCapacity(hello *podiumv1.Hello) *store.NodeCapacity {
	c := hello.GetCapacity()
	if c == nil {
		return nil
	}
	return &store.NodeCapacity{
		MaxTasks: c.GetMaxTasks(),
		CPUCores: c.GetCpuCores(),
		MemoryMB: c.GetMemoryMb(),
	}
}
