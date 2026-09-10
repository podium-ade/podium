// Command podium-agent is the Podium conductor: it holds the bot's identity in Slack, turns
// a mention into one Podium task running the agent runtime image, relays what the agent
// says back into the conversation and records the turn. Run it with no arguments (or
// `serve`) to start.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/podium-ade/podium/internal/agent"
	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:           "podium-agent",
		Short:         "Podium conductor: Slack, Linear and chat turned into Podium tasks",
		Version:       version.String(),
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")

	serve := newServeCommand()
	root.AddCommand(serve, newVersionCommand())
	// A bare `podium-agent` serves: that is what the compose file and the docs run.
	root.RunE = serve.RunE

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "podium-agent: %v\n", err)
		os.Exit(1)
	}
}

// newVersionCommand exists because the deployment docs and the acceptance script both run
// `podium-agent version`, and cobra's own --version is not a subcommand. It prints the same
// build identity, which is also what `podium version` reports for the CLI.
func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the build identity",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "podium-agent %s\n", version.String())
		},
	}
}

func newServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Hold the bot's sources open and run turns",
		Long: "Hold the bot's sources open and run turns.\n\n" +
			"Configuration is environment only: PODIUM_AGENT_SERVER, PODIUM_AGENT_API_TOKEN,\n" +
			"PODIUM_AGENT_DATABASE_URL, PODIUM_AGENT_LISTEN, PODIUM_AGENT_TOKEN,\n" +
			"PODIUM_AGENT_PROFILE_DIR and the two PODIUM_AGENT_SLACK_* tokens.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			slog.SetDefault(logger)

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			a, err := agent.New(ctx, config.FromEnv(), logger)
			if err != nil {
				return err
			}
			defer a.Close()
			return a.Run(ctx)
		},
	}
}
