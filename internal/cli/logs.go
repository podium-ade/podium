package cli

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// idleStop is how long `podium logs` without -f waits for another event before deciding
// it has caught up with a task that is still running. A terminal task never needs it: the
// server ends the stream by itself.
const idleStop = time.Second

func newLogsCommand(e *env) *cobra.Command {
	var follow bool
	var fromSeq uint64

	cmd := &cobra.Command{
		Use:   "logs TASK_ID",
		Short: "Print a task's output",
		Long: "Print a task's output.\n\n" +
			"Chunks are written to the stream they came from, so stdout and stderr stay\n" +
			"separated. Without -f the command stops once it has caught up; with -f it\n" +
			"follows until the task is terminal, reconnecting from the last event it saw.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if follow {
				return followLogs(cmd.Context(), e, args[0], fromSeq)
			}
			return printLogs(cmd.Context(), e, args[0], fromSeq)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming until the task ends")
	cmd.Flags().Uint64Var(&fromSeq, "from-seq", 0, "resume after this sequence number (exclusive)")
	return cmd
}

func followLogs(ctx context.Context, e *env, taskID string, fromSeq uint64) error {
	out := newLogPrinter(e.stdout, e.stderr)
	defer out.flush()

	f := &follower{
		tasks:   e.client.tasks,
		taskID:  taskID,
		lastSeq: fromSeq,
		onEvent: out.write,
	}
	if err := f.follow(ctx); err != nil {
		return &ExitError{Code: ExitInfra, Err: err}
	}
	return nil
}

// printLogs drains what the server already has. A running task has no end of stream to
// wait for, so the read stops after idleStop with nothing new.
func printLogs(ctx context.Context, e *env, taskID string, fromSeq uint64) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := e.client.tasks.StreamTaskEvents(ctx, connect.NewRequest(&podiumv1.StreamTaskEventsRequest{
		TaskId:  taskID,
		FromSeq: fromSeq,
	}))
	if err != nil {
		return &ExitError{Code: ExitInfra, Err: fmt.Errorf("read logs of %s: %w", taskID, err)}
	}
	defer func() { _ = stream.Close() }()

	events := make(chan *podiumv1.TaskEvent, 64)
	go func() {
		defer close(events)
		for stream.Receive() {
			select {
			case events <- stream.Msg():
			case <-ctx.Done():
				return
			}
		}
	}()

	out := newLogPrinter(e.stdout, e.stderr)
	defer out.flush()

	idle := time.NewTimer(idleStop)
	defer idle.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			out.write(ev)
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(idleStop)
		case <-idle.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
