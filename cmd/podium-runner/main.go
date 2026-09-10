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

	"github.com/podium-ade/podium/internal/runner"
	"github.com/podium-ade/podium/internal/version"
)

func main() {
	args := os.Args[1:]
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version") {
		fmt.Printf("podium-runner %s\n", version.String())
		return
	}
	// `podium-runner artifact add PATH …` is the in-container helper: it writes one line
	// to the event socket and exits. It is checked before the `--` strip because it is a
	// subcommand of the runner, not a task command.
	if len(args) >= 2 && args[0] == "artifact" && args[1] == "add" {
		os.Exit(runner.AddArtifact(runner.ConfigFromEnv(), args[2:]))
	}
	// `podium-runner message …` is the other in-container helper: one structured line for a
	// human, or for whatever is relaying the task's output somewhere.
	if len(args) >= 1 && args[0] == "message" {
		os.Exit(runner.AddMessage(runner.ConfigFromEnv(), args[1:], os.Stdin))
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: podium-runner -- COMMAND [ARG...]\n"+
			"       podium-runner artifact add PATH [--name NAME] [--type CONTENT_TYPE]\n"+
			"       podium-runner message [--type TYPE] [--attach NAME]... TEXT")
		os.Exit(2)
	}
	os.Exit(runner.Run(context.Background(), runner.ConfigFromEnv(), args))
}
