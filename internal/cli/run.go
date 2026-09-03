package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// stepReattached mirrors docker.StepReattached. The CLI never links the Docker SDK — it
// talks only to the server — so the one string is spelled twice rather than dragging the
// engine client into a binary that has no engine.
const stepReattached = "node/reattached"

func newRunCommand(e *env) *cobra.Command {
	var (
		image       string
		labels      []string
		envVars     []string
		secretRefs  []string
		timeout     time.Duration
		specFile    string
		workdir     string
		detach      bool
		maxAttempts int
		retryOnLoss bool
	)

	cmd := &cobra.Command{
		Use:   "run [flags] -- COMMAND [ARG...]",
		Short: "Create a task and follow it until it exits",
		Long: "Create a task and follow it until it exits.\n\n" +
			"The task's stdout and stderr are written to this process's stdout and stderr;\n" +
			"Podium's own progress lines go to stderr. The exit status is the task's own\n" +
			"exit code, or 125 when the task never produced one, or 130 when it was cancelled.",
		Example: "  podium run --image alpine:3 -- sh -c 'echo hi; exit 3'\n" +
			"  podium run --image alpine:3 --secret DB_PASSWORD -- sh -c 'echo $DB_PASSWORD'\n" +
			"  podium run --spec task.yaml --detach",
		RunE: func(cmd *cobra.Command, args []string) error {
			command := args
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				command = args[dash:]
			}
			taskSpec, err := buildSpec(specFile, image, workdir, labels, envVars, secretRefs, timeout, command)
			if err != nil {
				return &ExitError{Code: ExitUsage, Err: err}
			}
			if cmd.Flags().Changed("max-attempts") {
				taskSpec.MaxAttempts = maxAttempts
			}
			if cmd.Flags().Changed("retry-on-node-loss") {
				taskSpec.RetryOnNodeLoss = retryOnLoss
			}
			if err := taskSpec.Validate(); err != nil {
				return &ExitError{Code: ExitUsage, Err: err}
			}
			return runTask(cmd.Context(), e, taskSpec, detach)
		},
	}

	cmd.Flags().StringVar(&image, "image", "", "container image to run")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "node label the task requires (repeatable)")
	cmd.Flags().StringArrayVar(&envVars, "env", nil, "environment variable as KEY=VALUE (repeatable)")
	cmd.Flags().StringArrayVar(&secretRefs, "secret", nil,
		"secret to inject as NAME, NAME:env:KEY or NAME:file:/absolute/path (repeatable)")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "task timeout (default 1h)")
	cmd.Flags().StringVar(&specFile, "spec", "", "task spec YAML file; flags override its fields")
	cmd.Flags().StringVar(&workdir, "working-dir", "", "working directory inside the container (default /workspace)")
	cmd.Flags().BoolVar(&detach, "detach", false, "print the task ID and return immediately")
	cmd.Flags().IntVar(&maxAttempts, "max-attempts", 0, "how many times the task may be assigned (default 1)")
	cmd.Flags().BoolVar(&retryOnLoss, "retry-on-node-loss", false,
		"re-run the task on another node if the one running it goes offline, instead of marking it lost")
	return cmd
}

// buildSpec merges the spec file, if any, with the flags. Flags win, so a spec file is a
// starting point rather than a straitjacket.
func buildSpec(
	specFile, image, workdir string,
	labels, envVars, secretRefs []string,
	timeout time.Duration,
	command []string,
) (*spec.TaskSpec, error) {
	out := &spec.TaskSpec{}
	if specFile != "" {
		f, err := os.Open(specFile) //nolint:gosec // the user names their own spec file
		if err != nil {
			return nil, fmt.Errorf("open spec %s: %w", specFile, err)
		}
		defer func() { _ = f.Close() }()
		parsed, err := spec.ParseTaskSpec(f)
		if err != nil {
			return nil, err
		}
		out = parsed
	}

	if image != "" {
		out.Image = image
	}
	if workdir != "" {
		out.WorkingDir = workdir
	}
	if len(command) > 0 {
		out.Command = command
	}
	if len(labels) > 0 {
		out.Labels = append(out.Labels, labels...)
	}
	if timeout > 0 {
		out.Timeout = spec.Duration(timeout)
	}
	for _, kv := range envVars {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("--env %q is not KEY=VALUE", kv)
		}
		if out.Env == nil {
			out.Env = make(map[string]string)
		}
		out.Env[k] = v
	}
	for _, raw := range secretRefs {
		ref, err := parseSecretRef(raw)
		if err != nil {
			return nil, err
		}
		out.Secrets = append(out.Secrets, ref)
	}

	out.ApplyDefaults()
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("invalid task: %w", err)
	}
	return out, nil
}

func runTask(ctx context.Context, e *env, taskSpec *spec.TaskSpec, detach bool) error {
	created, err := e.client.tasks.CreateTask(ctx, connect.NewRequest(&podiumv1.CreateTaskRequest{
		Spec: taskSpec.ToProto(),
	}))
	if err != nil {
		return &ExitError{Code: ExitInfra, Err: fmt.Errorf("create task: %w", err)}
	}
	task := created.Msg.GetTask()

	if detach {
		fmt.Fprintln(e.stdout, task.GetId())
		return nil
	}

	e.note("task %s", task.GetId())
	return followToExit(ctx, e, task.GetId())
}

// followToExit renders a task's events until the server closes the stream, then reads the
// task back for its terminal status. The stream ends by itself once the task is terminal
// and everything has been delivered, so a clean end of stream is the signal to look up
// the exit code — not a reason to poll.
func followToExit(ctx context.Context, e *env, taskID string) error {
	started := time.Now()
	scheduled := false
	var wallMS int64
	var failure string

	out := newLogPrinter(e.stdout, e.stderr)
	defer out.flush()

	f := &follower{
		tasks:  e.client.tasks,
		taskID: taskID,
		onEvent: func(ev *podiumv1.TaskEvent) {
			switch ev.GetKind() {
			case podiumv1.TaskEventKind_TASK_EVENT_KIND_LOG:
				out.write(ev)
			case podiumv1.TaskEventKind_TASK_EVENT_KIND_PROVISIONING:
				if !scheduled {
					scheduled = true
					e.note("scheduled on %s", nodeOf(ctx, e, taskID))
				}
			case podiumv1.TaskEventKind_TASK_EVENT_KIND_STEP:
				switch name := ev.GetStep().GetName(); {
				case strings.HasPrefix(name, "sidecar/"):
					e.note("%s %s", name, ev.GetStep().GetStatus())
				case name == stepReattached:
					e.note("the node restarted; this task was re-adopted")
				}
			case podiumv1.TaskEventKind_TASK_EVENT_KIND_STARTED:
				e.note("running")
			case podiumv1.TaskEventKind_TASK_EVENT_KIND_FINISHED:
				wallMS = ev.GetFinished().GetUsage().GetWallMs()
			case podiumv1.TaskEventKind_TASK_EVENT_KIND_ERROR:
				if !ev.GetError().GetRetryable() {
					failure = ev.GetError().GetMessage()
				}
				e.note("error: %s", ev.GetError().GetMessage())
			}
		},
	}
	if err := f.follow(ctx); err != nil {
		return &ExitError{Code: ExitInfra, Err: err}
	}

	task, err := getTaskWithRetry(ctx, e.client.tasks, taskID, followGrace)
	if err != nil {
		return &ExitError{Code: ExitInfra, Err: fmt.Errorf("read task %s back: %w", taskID, err)}
	}

	elapsed := time.Duration(wallMS) * time.Millisecond
	if elapsed == 0 {
		elapsed = time.Since(started)
	}

	switch task.GetStatus() {
	case podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED, podiumv1.TaskStatus_TASK_STATUS_FAILED:
		// A task that failed before its container ran — a sidecar that never became ready,
		// an image that does not exist — is terminal with no exit code at all. exit_code is
		// optional on the wire for exactly this reason, and reading it as 0 would report a
		// failed task as a success.
		if task.ExitCode == nil {
			if failure == "" {
				failure = task.GetFailureReason()
			}
			if failure == "" {
				failure = "task failed without an exit code"
			}
			e.note("failed: %s", failure)
			return &ExitError{Code: ExitInfra}
		}
		code := int(task.GetExitCode())
		e.note("finished exit %d in %s", code, elapsed.Round(100*time.Millisecond))
		if code == 0 {
			return nil
		}
		return &ExitError{Code: code}
	case podiumv1.TaskStatus_TASK_STATUS_CANCELLED:
		e.note("cancelled after %s", elapsed.Round(100*time.Millisecond))
		return &ExitError{Code: ExitCancelled}
	default:
		if failure == "" {
			failure = task.GetFailureReason()
		}
		if failure == "" {
			failure = "task ended " + taskStatusName(task.GetStatus()) + " without an exit code"
		}
		e.note("failed: %s", failure)
		return &ExitError{Code: ExitInfra}
	}
}

// nodeOf reads back which node the task landed on, for the "scheduled on …" line. It is
// cosmetic, so a failure degrades to "a node" rather than aborting the run.
func nodeOf(ctx context.Context, e *env, taskID string) string {
	res, err := e.client.tasks.GetTask(ctx, connect.NewRequest(&podiumv1.GetTaskRequest{TaskId: taskID}))
	if err != nil || res.Msg.GetTask().GetNodeId() == "" {
		return "a node"
	}
	return res.Msg.GetTask().GetNodeId()
}
