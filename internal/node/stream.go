package node

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/version"
)

// Stream loop timings. The heartbeat interval is the canonical 10s; flushTick is how
// often buffered events are pushed when nothing signalled, and retransmitAfter is how
// long an unacked send is left alone before it is sent again — an Ingest that failed is
// never acked, and the connection it failed on may stay up indefinitely.
const (
	backoffMin        = time.Second
	backoffMax        = 30 * time.Second
	heartbeatInterval = 10 * time.Second
	flushTick         = 25 * time.Millisecond
	retransmitTick    = 5 * time.Second
	retransmitAfter   = 15 * time.Second
	// stableSession is how long a stream must last before the backoff is considered
	// healthy again, so a server that accepts and immediately drops does not get
	// hammered at one connection per second.
	stableSession = 30 * time.Second
	// helloAckWait is how long a reconnecting node waits for the control plane's answer
	// to Hello before it adopts whatever it found from its own on-disk bookmark. It only
	// matters against a server too old to send one.
	helloAckWait = 15 * time.Second
)

// received is one result of stream.Receive, handed over by the reader goroutine because
// Receive blocks on the response body and nothing else can interrupt it.
type received struct {
	msg *podiumv1.ServerMessage
	err error
}

// Run starts the health endpoint, adopts anything the previous incarnation left behind,
// and then holds a stream to the control plane until ctx is cancelled — reconnecting with
// exponential backoff, and re-sending Hello and every unacked event each time.
func (n *Node) Run(ctx context.Context) error {
	if err := n.serveMetrics(ctx); err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		n.stopMetrics(stopCtx)
	}()

	// The containers a previous incarnation left behind are found now but adopted later:
	// only the control plane knows how much of their output it already has.
	n.discoverOwned(ctx)
	go n.runPruneLoop(ctx)

	backoff := backoffMin
	for {
		if ctx.Err() != nil {
			return nil
		}
		started := time.Now()
		err := n.session(ctx)
		n.connected.Store(false)

		switch {
		case errors.Is(err, errDrained):
			n.logger.Info("node drained; exiting")
			return nil
		case ctx.Err() != nil:
			return nil
		}
		if time.Since(started) >= stableSession {
			backoff = backoffMin
		}
		wait := jitter(backoff)
		n.logger.Warn("control plane stream ended; reconnecting", "error", err, "in", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil
		}
		if backoff < backoffMax {
			backoff = min(backoff*2, backoffMax)
		}
	}
}

// jitter spreads reconnects over the last quarter of the interval so a fleet that lost
// the server together does not come back in lockstep.
func jitter(d time.Duration) time.Duration {
	return d - time.Duration(rand.Int64N(int64(d/4)+1))
}

// session holds one stream: Hello, then heartbeats, event flushes and whatever the server
// pushes, until either side ends it.
func (n *Node) session(ctx context.Context) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream := n.client.Stream(sctx)
	defer func() {
		_ = stream.CloseRequest()
		_ = stream.CloseResponse()
	}()

	// Everything the server never acked goes out again on this stream, before anything
	// new: the replay is what makes a reconnect invisible to a client following the task.
	for _, b := range n.buffers() {
		b.rewind()
	}

	if err := stream.Send(n.hello()); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	incoming := make(chan received, 64)
	go func() {
		for {
			msg, err := stream.Receive()
			select {
			case incoming <- received{msg: msg, err: err}:
			case <-sctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	n.connected.Store(true)
	n.logger.InfoContext(sctx, "control plane stream open",
		"server", n.cfg.Server, "running_tasks", n.runningCount(), "free_slots", n.freeSlots())

	if err := n.sendHeartbeat(sctx, stream); err != nil {
		return err
	}

	hb := time.NewTicker(heartbeatInterval)
	defer hb.Stop()
	flush := time.NewTicker(flushTick)
	defer flush.Stop()
	retransmit := time.NewTicker(retransmitTick)
	defer retransmit.Stop()
	reconcileDeadline := time.NewTimer(helloAckWait)
	defer reconcileDeadline.Stop()

	for {
		if err := n.flushEvents(stream); err != nil {
			return err
		}
		select {
		case r := <-incoming:
			if r.err != nil {
				return r.err
			}
			if err := n.handle(sctx, stream, r.msg); err != nil {
				return err
			}
		case <-flush.C:
		case <-n.wake:
		case <-hb.C:
			if err := n.sendHeartbeat(sctx, stream); err != nil {
				return err
			}
		case <-reconcileDeadline.C:
			if n.hasPending() {
				n.adoptStranded(sctx)
			}
		case <-retransmit.C:
			for _, b := range n.buffers() {
				if b.stalled(retransmitAfter) {
					n.logger.WarnContext(sctx, "no ack for buffered events; sending them again",
						"task_id", b.taskID)
					b.rewind()
				}
			}
		case <-n.drainDone:
			return errDrained
		case <-sctx.Done():
			return sctx.Err()
		}
	}
}

// hello is the opening message and the reconciliation input: which tasks this node still
// holds containers for, so the server's free-slot arithmetic starts out right.
func (n *Node) hello() *podiumv1.NodeMessage {
	return &podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_Hello{Hello: &podiumv1.Hello{
		NodeId:  n.id.NodeID,
		NodeKey: n.id.NodeKey,
		Labels:  n.cfg.Labels,
		// MaxTasks is this machine's OWN configuration and not the number in force: the
		// control plane holds any override and re-sends it after the HelloAck, and it needs
		// to be able to show an operator both.
		Capacity: &podiumv1.NodeCapacity{
			MaxTasks: int32(n.cfg.MaxTasks),
			CpuCores: n.facts.CPUCores,
			MemoryMb: n.facts.MemoryMB,
		},
		RunningTaskIds: n.runningTaskIDs(),
		Version:        version.String(),
	}}}
}

func (n *Node) sendHeartbeat(
	ctx context.Context,
	stream *connect.BidiStreamForClient[podiumv1.NodeMessage, podiumv1.ServerMessage],
) error {
	load := sampleLoad(ctx, n.cfg.DataDir, n.logger)
	n.maybePrune(ctx, load)
	err := stream.Send(&podiumv1.NodeMessage{Msg: &podiumv1.NodeMessage_Heartbeat{
		Heartbeat: &podiumv1.Heartbeat{
			Load: &podiumv1.NodeLoad{
				RunningTasks: n.runningCount(),
				CpuPct:       load.CPUPct,
				MemPct:       load.MemPct,
			},
			FreeSlots:     n.freeSlots(),
			DiskFreeBytes: load.DiskFreeBytes,
			Ts:            timestamppb.New(time.Now().UTC()),
		},
	}})
	if err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}
	return nil
}

// flushEvents pushes everything the task goroutines have buffered and not yet handed to
// this stream. A send that fails ends the session; the events stay buffered and go out
// again on the next one.
func (n *Node) flushEvents(
	stream *connect.BidiStreamForClient[podiumv1.NodeMessage, podiumv1.ServerMessage],
) error {
	for _, b := range n.buffers() {
		for _, e := range b.pending() {
			if err := stream.Send(&podiumv1.NodeMessage{
				Msg: &podiumv1.NodeMessage_TaskEvent{TaskEvent: e},
			}); err != nil {
				return fmt.Errorf("send task event %s/%d: %w", e.GetTaskId(), e.GetSeq(), err)
			}
			b.markSent(e.GetSeq())
		}
	}
	return nil
}

// handle dispatches one server message. Only the session goroutine sends on the stream,
// so replying from here is safe.
func (n *Node) handle(
	ctx context.Context,
	stream *connect.BidiStreamForClient[podiumv1.NodeMessage, podiumv1.ServerMessage],
	msg *podiumv1.ServerMessage,
) error {
	switch {
	case msg.GetAssign() != nil:
		return n.handleAssign(ctx, stream, msg.GetAssign())
	case msg.GetAck() != nil:
		n.handleAck(msg.GetAck())
	case msg.GetHelloAck() != nil:
		n.applyCheckpoints(ctx, msg.GetHelloAck().GetTasks())
	case msg.GetCancel() != nil:
		c := msg.GetCancel()
		n.logger.InfoContext(ctx, "cancel requested", "task_id", c.GetTaskId(), "reason", c.GetReason())
		// A container found at startup has no run to interrupt; the control plane
		// disowning it means tear it down, not signal it.
		if !n.dropPendingContainer(ctx, c.GetTaskId(), c.GetReason()) {
			n.exec.Cancel(c.GetTaskId())
		}
	case msg.GetInject() != nil:
		in := msg.GetInject()
		if err := n.exec.Inject(in.GetTaskId(), in.GetText()); err != nil {
			n.logger.WarnContext(ctx, "inject into task failed",
				"task_id", in.GetTaskId(), "error", err)
		}
	case msg.GetSlots() != nil:
		// Every stream carries one of these, so only a change is worth saying out loud.
		if inForce, changed := n.setSlots(msg.GetSlots().GetMaxTasks()); changed {
			n.logger.InfoContext(ctx, "slot count changed by the control plane",
				"max_tasks", inForce, "configured", n.cfg.MaxTasks, "running_tasks", n.runningCount())
		}
	case msg.GetDrain() != nil:
		d := msg.GetDrain()
		n.logger.InfoContext(ctx, "drain state received",
			"undo", d.GetUndo(), "running_tasks", n.runningCount(), "exit_on_drain", n.cfg.ExitOnDrain)
		n.setDraining(!d.GetUndo())
	default:
		n.logger.WarnContext(ctx, "ignoring unknown server message")
	}
	return nil
}

func (n *Node) handleAssign(
	ctx context.Context,
	stream *connect.BidiStreamForClient[podiumv1.NodeMessage, podiumv1.ServerMessage],
	a *podiumv1.Assign,
) error {
	switch {
	case n.isDraining():
		return n.rejectAssign(ctx, stream, a, "node draining")
	case n.freeSlots() == 0:
		return n.rejectAssign(ctx, stream, a, "node full")
	}
	n.startTask(a)
	return nil
}

func (n *Node) handleAck(ack *podiumv1.Ack) {
	n.mu.Lock()
	b := n.tasks[ack.GetTaskId()]
	n.mu.Unlock()
	if b == nil {
		return
	}
	b.ack(ack.GetSeq())
}
