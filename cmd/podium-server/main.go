// Command podium-server is the Podium control plane: the Connect API, the node streams and the
// scheduler. Run it with no arguments (or `serve`) to start serving.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/alvaroibarguen/podium/internal/server"
	"github.com/alvaroibarguen/podium/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:           "podium-server",
		Short:         "Podium control plane server",
		Version:       version.String(),
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")

	serve := newServeCommand()
	root.AddCommand(serve, newInitCommand(), newGenMasterKeyCommand(), newRotateMasterKeyCommand())
	// A bare `podium-server` serves: that is what the deployment docs and the compose file run.
	root.RunE = serve.RunE

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "podium-server: %v\n", err)
		os.Exit(1)
	}
}

func newServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve the API, node streams and scheduler",
		Long: "Serve the API, node streams and scheduler.\n\n" +
			"Configuration is environment only: PODIUM_DATABASE_URL, PODIUM_TRANSPORT,\n" +
			"PODIUM_LOCAL_LISTEN (loopback by default), PODIUM_LOCAL_TOKEN and PODIUM_MASTER_KEY_FILE.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			slog.SetDefault(logger)

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			srv, err := server.New(ctx, server.ConfigFromEnv(), logger)
			if err != nil {
				return err
			}
			defer srv.Close()
			return srv.Run(ctx)
		},
	}
}
