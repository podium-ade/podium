// Command podium-server is a Podium binary. Real wiring arrives in later steps.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/alvaroibarguen/podium/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:           "podium-server",
		Short:         "Podium control plane server",
		Version:       version.String(),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "podium-server: %v\n", err)
		os.Exit(1)
	}
}
