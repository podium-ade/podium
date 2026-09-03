// Command podium-node is the Podium worker daemon: it enrolls with the control plane,
// holds one bidirectional stream to it, and runs assigned tasks as containers on the
// local Docker engine.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/alvaroibarguen/podium/internal/node"
	"github.com/alvaroibarguen/podium/internal/version"
)

func main() {
	root := newRootCommand()
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "podium-node: %v\n", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	var configPath string
	var logLevel string
	var exitOnDrain bool

	root := &cobra.Command{
		Use:   "podium-node",
		Short: "Podium worker node daemon",
		Long: "Podium worker node daemon.\n\n" +
			"Configuration is " + node.DefaultConfigPath + " (or --config) overlaid by the\n" +
			"PODIUM_NODE_* environment variables: PODIUM_NODE_SERVER, PODIUM_NODE_TRANSPORT,\n" +
			"PODIUM_NODE_DEV_TOKEN, PODIUM_NODE_ENROLL_TOKEN, PODIUM_NODE_DATA_DIR,\n" +
			"PODIUM_NODE_LABELS, PODIUM_NODE_MAX_TASKS, PODIUM_NODE_METRICS_LISTEN.\n\n" +
			"The config file is optional: an environment-only node is a supported deployment.",
		Args:          cobra.NoArgs,
		Version:       version.String(),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger := newLogger(logLevel)
			slog.SetDefault(logger)

			cfg, err := node.LoadConfig(configPath)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("exit-on-drain") {
				cfg.ExitOnDrain = exitOnDrain
			}

			// SIGTERM ends the stream and the process, and deliberately does not cancel
			// the running containers: cancelling a run makes the executor tear its
			// container down, and a restarted daemon is meant to adopt it instead.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			n, err := node.New(ctx, cfg, logger)
			if err != nil {
				return err
			}
			defer func() { _ = n.Close() }()

			logger.Info("podium-node starting",
				"node_id", n.NodeID(), "server", cfg.Server, "data_dir", cfg.DataDir,
				"max_tasks", cfg.MaxTasks, "labels", cfg.Labels, "version", version.String())
			return n.Run(ctx)
		},
	}
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	root.Flags().StringVar(&configPath, "config", "", "path to node.yaml (default "+node.DefaultConfigPath+")")
	root.Flags().StringVar(&logLevel, "log-level", "info", "debug, info, warn or error")
	root.Flags().BoolVar(&exitOnDrain, "exit-on-drain", false,
		"exit 0 once the control plane has drained this node and its last task has finished")
	return root
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
