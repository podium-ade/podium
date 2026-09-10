package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/version"
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
			cfg, err := LoadConfig(serverFlag, tokenFlag)
			if err != nil {
				// `version` still answers when there is nowhere to ask: it reports the
				// client's own build and says it could not reach a control plane.
				if cmd.Name() == "version" || cmd.Name() == "help" {
					return nil
				}
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
		newArtifactsCommand(e),
		newArtifactCommand(e),
		newVersionCommand(e),
	)
	return root
}

func newVersionCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the client and control plane versions",
		Long: "Print the client and control plane versions.\n\n" +
			"A client and a server from different releases can disagree about the wire, so\n" +
			"a mismatch is reported as a warning on stderr. The command still exits 0: it is\n" +
			"a diagnostic, not a gate.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(e.stdout, "podium %s\n", version.String())
			if e.client == nil {
				e.note("no control plane configured: pass --server and --token, "+
					"set PODIUM_SERVER and PODIUM_TOKEN, or write %s", ConfigPath())
				return nil
			}
			res, err := e.client.identity.WhoAmI(cmd.Context(), connect.NewRequest(&podiumv1.WhoAmIRequest{}))
			if err != nil {
				e.note("control plane %s unreachable: %v", e.cfg.Server, err)
				return nil
			}
			serverVersion, serverCommit := res.Msg.GetServerVersion(), res.Msg.GetServerCommit()
			if serverVersion == "" {
				// A control plane older than this field. Say so rather than printing a blank.
				fmt.Fprintf(e.stdout, "server   (does not report a version)\n")
				return nil
			}
			fmt.Fprintf(e.stdout, "server %s (%s)\n", serverVersion, serverCommit)
			if warning := skewWarning(version.Version, serverVersion); warning != "" {
				e.note("%s", warning)
			}
			return nil
		},
	}
}

// skewWarning returns the sentence to print when a client and a control plane are not the
// same build, and "" when they agree. Two unstamped "dev" builds are not a skew worth
// mentioning: that is every developer checkout.
func skewWarning(client, server string) string {
	if client == server {
		return ""
	}
	return fmt.Sprintf("version skew: client %s, control plane %s. "+
		"The wire is only guaranteed between matching releases; upgrade whichever is older.",
		client, server)
}

// note prints a lifecycle line on stderr, dimmed on a terminal, so that piping a task's
// stdout somewhere never picks up Podium's own commentary.
func (e *env) note(format string, args ...any) {
	e.noteLine("→ " + fmt.Sprintf(format, args...))
}

// noteLine is note without the arrow, for the continuation lines of a multi-line note.
func (e *env) noteLine(msg string) {
	if isTerminal(e.stderr) {
		msg = "\x1b[2m" + msg + "\x1b[0m"
	}
	fmt.Fprintln(e.stderr, msg)
}

// noteMessage prints what a task said. It goes to stderr like every other progress line:
// stdout is the task's own output, byte for byte, and a message is Podium's commentary on
// it however much a relay cares about it.
//
// The text is untrusted content from an untrusted process — it is relayed, never
// interpreted — so it is printed as it arrived, one indented line at a time.
func (e *env) noteMessage(m *podiumv1.Message) {
	if m == nil {
		return
	}
	lines := strings.Split(m.GetText(), "\n")
	e.note("message (%s): %s", m.GetType(), lines[0])
	for _, line := range lines[1:] {
		e.noteLine("  " + line)
	}
	if names := m.GetAttachments(); len(names) > 0 {
		e.noteLine("  (attachments: " + strings.Join(names, ", ") + ")")
	}
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
