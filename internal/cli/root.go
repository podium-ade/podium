package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
	"github.com/alvaroibarguen/podium/internal/version"
)

// Exit codes the CLI is contractual about; see docs/cli.md.
const (
	// ExitInfra is anything that stopped the task from producing an exit code: the
	// server was unreachable, the image would not pull, the node failed the task.
	ExitInfra = 125
	// ExitCancelled is a task that was cancelled, matching the shell's 128+SIGINT.
	ExitCancelled = 130
	// ExitUsage is a bad invocation or a rejected request.
	ExitUsage = 1
)

// ExitError carries a specific process exit status out of a command. `podium run` uses it
// to hand the task's own exit code back to the shell.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// env is everything a subcommand needs: the resolved configuration, the clients built
// from it, and the three streams, which are fields so tests can capture them.
type env struct {
	cfg    Config
	client *clients
	stdout io.Writer
	stderr io.Writer
	stdin  io.Reader
}

// NewRootCommand builds the whole CLI. The global --server and --token flags win over
// PODIUM_SERVER/PODIUM_TOKEN, which win over ~/.config/podium/config.yaml.
func NewRootCommand() *cobra.Command {
	var serverFlag, tokenFlag string
	e := &env{stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin}

	root := &cobra.Command{
		Use:           "podium",
		Short:         "Podium CLI — submit and inspect tasks",
		Version:       version.String(),
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Name() == "version" || cmd.Name() == "help" {
				return nil
			}
			cfg, err := LoadConfig(serverFlag, tokenFlag)
			if err != nil {
				return err
			}
			e.cfg = cfg
			e.client = newClients(cfg)
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	root.PersistentFlags().StringVar(&serverFlag, "server", "", "control plane base URL (env PODIUM_SERVER)")
	root.PersistentFlags().StringVar(&tokenFlag, "token", "", "API token (env PODIUM_TOKEN)")

	root.AddCommand(
		newRunCommand(e),
		newTasksCommand(e),
		newTaskCommand(e),
		newLogsCommand(e),
		newNodesCommand(e),
		newNodeCommand(e),
		newSecretCommand(e),
		newVersionCommand(e),
	)
	return root
}

func newVersionCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(e.stdout, "podium %s\n", version.String())
			return err
		},
	}
}

// note prints a lifecycle line on stderr, dimmed on a terminal, so that piping a task's
// stdout somewhere never picks up Podium's own commentary.
func (e *env) note(format string, args ...any) {
	msg := "→ " + fmt.Sprintf(format, args...)
	if isTerminal(e.stderr) {
		msg = "\x1b[2m" + msg + "\x1b[0m"
	}
	fmt.Fprintln(e.stderr, msg)
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// newTable is the one table style the CLI uses.
func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
}

var taskStatusNames = map[podiumv1.TaskStatus]string{
	podiumv1.TaskStatus_TASK_STATUS_QUEUED:       "queued",
	podiumv1.TaskStatus_TASK_STATUS_SCHEDULED:    "scheduled",
	podiumv1.TaskStatus_TASK_STATUS_PROVISIONING: "provisioning",
	podiumv1.TaskStatus_TASK_STATUS_RUNNING:      "running",
	podiumv1.TaskStatus_TASK_STATUS_SUCCEEDED:    "succeeded",
	podiumv1.TaskStatus_TASK_STATUS_FAILED:       "failed",
	podiumv1.TaskStatus_TASK_STATUS_CANCELLED:    "cancelled",
	podiumv1.TaskStatus_TASK_STATUS_LOST:         "lost",
}

var taskStatusValues = func() map[string]podiumv1.TaskStatus {
	m := make(map[string]podiumv1.TaskStatus, len(taskStatusNames))
	for k, v := range taskStatusNames {
		m[v] = k
	}
	return m
}()

var nodeStatusNames = map[podiumv1.NodeStatus]string{
	podiumv1.NodeStatus_NODE_STATUS_ONLINE:      "online",
	podiumv1.NodeStatus_NODE_STATUS_UNREACHABLE: "unreachable",
	podiumv1.NodeStatus_NODE_STATUS_OFFLINE:     "offline",
	podiumv1.NodeStatus_NODE_STATUS_DRAINING:    "draining",
}

func taskStatusName(s podiumv1.TaskStatus) string {
	if name, ok := taskStatusNames[s]; ok {
		return name
	}
	return "unknown"
}

func nodeStatusName(s podiumv1.NodeStatus) string {
	if name, ok := nodeStatusNames[s]; ok {
		return name
	}
	return "unknown"
}

// parseTaskStatus accepts the canonical lowercase status names.
func parseTaskStatus(v string) (podiumv1.TaskStatus, error) {
	if s, ok := taskStatusValues[strings.ToLower(strings.TrimSpace(v))]; ok {
		return s, nil
	}
	names := make([]string, 0, len(taskStatusValues))
	for name := range taskStatusValues {
		names = append(names, name)
	}
	return 0, fmt.Errorf("unknown status %q (want one of: %s)", v, strings.Join(sorted(names), ", "))
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ago renders a timestamp as a short relative age, which is what an operator scanning a
// table actually wants.
func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
