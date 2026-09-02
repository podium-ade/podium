package cli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

func newTasksCommand(e *env) *cobra.Command {
	var statuses []string
	var limit int32
	var nodeID string

	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "List tasks, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter := &podiumv1.TaskFilter{NodeId: nodeID}
			for _, s := range statuses {
				status, err := parseTaskStatus(s)
				if err != nil {
					return &ExitError{Code: ExitUsage, Err: err}
				}
				filter.Status = append(filter.Status, status)
			}
			res, err := e.client.tasks.ListTasks(cmd.Context(), connect.NewRequest(&podiumv1.ListTasksRequest{
				Filter: filter,
				Page:   &podiumv1.Page{Limit: limit},
			}))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("list tasks: %w", err)}
			}
			return printTasks(e, res.Msg.GetTasks())
		},
	}
	cmd.Flags().StringArrayVar(&statuses, "status", nil, "filter by status (repeatable)")
	cmd.Flags().Int32Var(&limit, "limit", 0, "maximum number of tasks (default 50)")
	cmd.Flags().StringVar(&nodeID, "node", "", "filter by node ID")
	return cmd
}

func printTasks(e *env, tasks []*podiumv1.Task) error {
	w := newTable(e.stdout)
	fmt.Fprintln(w, "ID\tSTATUS\tEXIT\tIMAGE\tNODE\tCREATED")
	for _, t := range tasks {
		exit := "-"
		if t.ExitCode != nil {
			exit = strconv.Itoa(int(t.GetExitCode()))
		}
		node := t.GetNodeId()
		if node == "" {
			node = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			t.GetId(), taskStatusName(t.GetStatus()), exit, t.GetSpec().GetImage(), node,
			ago(t.GetCreatedAt().AsTime()))
	}
	return w.Flush()
}

func newTaskCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Inspect and control a single task",
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newTaskGetCommand(e), newTaskCancelCommand(e))
	return cmd
}

func newTaskGetCommand(e *env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "get TASK_ID",
		Short: "Show one task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := e.client.tasks.GetTask(cmd.Context(), connect.NewRequest(&podiumv1.GetTaskRequest{
				TaskId: args[0],
			}))
			if err != nil {
				return &ExitError{Code: ExitInfra, Err: fmt.Errorf("get task %s: %w", args[0], err)}
			}
			task := res.Msg.GetTask()
			if asJSON {
				raw, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(task)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(e.stdout, string(raw))
				return err
			}
			return printTaskDetail(e, task)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the task as JSON")
	return cmd
}

func printTaskDetail(e *env, t *podiumv1.Task) error {
	w := newTable(e.stdout)
	line := func(k string, v any) { fmt.Fprintf(w, "%s:\t%v\n", k, v) }

	line("id", t.GetId())
	line("status", taskStatusName(t.GetStatus()))
	if t.ExitCode != nil {
		line("exit_code", t.GetExitCode())
	}
	line("image", t.GetSpec().GetImage())
	if cmdArgs := t.GetSpec().GetCommand(); len(cmdArgs) > 0 {
		line("command", cmdArgs)
	}
	line("working_dir", t.GetSpec().GetWorkingDir())
	if labels := t.GetSpec().GetLabels(); len(labels) > 0 {
		line("labels", labels)
	}
	line("node", orDash(t.GetNodeId()))
	line("attempts", t.GetAttempts())
	line("requested_by", orDash(t.GetRequestedBy()))
	line("created_at", stamp(t.GetCreatedAt().AsTime(), t.GetCreatedAt() != nil))
	line("started_at", stamp(t.GetStartedAt().AsTime(), t.GetStartedAt() != nil))
	line("finished_at", stamp(t.GetFinishedAt().AsTime(), t.GetFinishedAt() != nil))
	if u := t.GetUsage(); u != nil {
		line("usage", fmt.Sprintf("cpu %.2fs, peak %d MB, wall %s",
			u.GetCpuSeconds(), u.GetPeakMemoryMb(), time.Duration(u.GetWallMs())*time.Millisecond))
	}
	return w.Flush()
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func stamp(t time.Time, present bool) string {
	if !present {
		return "-"
	}
	return t.Local().Format(time.RFC3339)
}

func newTaskCancelCommand(e *env) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "cancel TASK_ID",
		Short: "Ask the node to stop a task",
		Long: "Ask the node to stop a task.\n\n" +
			"Cancellation is asynchronous: the node sends SIGTERM, waits 30 seconds and then\n" +
			"SIGKILLs the container, so the task reaches its terminal status some time after\n" +
			"this command returns.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cancelTask(cmd.Context(), e, args[0], reason)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "why the task is being cancelled")
	return cmd
}

func cancelTask(ctx context.Context, e *env, taskID, reason string) error {
	res, err := e.client.tasks.CancelTask(ctx, connect.NewRequest(&podiumv1.CancelTaskRequest{
		TaskId: taskID,
		Reason: reason,
	}))
	if err != nil {
		return &ExitError{Code: ExitInfra, Err: fmt.Errorf("cancel task %s: %w", taskID, err)}
	}
	task := res.Msg.GetTask()
	if task.GetStatus() == podiumv1.TaskStatus_TASK_STATUS_CANCELLED {
		fmt.Fprintf(e.stdout, "%s cancelled\n", task.GetId())
		return nil
	}
	fmt.Fprintf(e.stdout, "%s cancelling (still %s; the container gets 30s to stop)\n",
		task.GetId(), taskStatusName(task.GetStatus()))
	return nil
}
