// Command podium-runner is PID 1 inside a Podium task container. The node bind-mounts it
// at /podium/runner and starts the container as `/podium/runner -- <task command>`.
//
// It deliberately does not use the flag package: everything after the first `--` belongs
// to the task, and parsing it would mangle a command such as `sh -c 'ls -l'`.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alvaroibarguen/podium/internal/runner"
	"github.com/alvaroibarguen/podium/internal/version"
)

func main() {
	args := os.Args[1:]
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version") {
		fmt.Printf("podium-runner %s\n", version.String())
		return
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: podium-runner -- COMMAND [ARG...]")
		os.Exit(2)
	}
	os.Exit(runner.Run(context.Background(), runner.ConfigFromEnv(), args))
}
