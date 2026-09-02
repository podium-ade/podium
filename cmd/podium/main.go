// Command podium is the Podium CLI: it submits tasks, follows them, and inspects the
// control plane. It never talks to Docker.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/alvaroibarguen/podium/internal/cli"
)

func main() {
	err := cli.NewRootCommand().Execute()
	if err == nil {
		return
	}

	// `podium run` reports the task's own exit code, so an ExitError decides the status
	// and only says something when it carries a message of its own.
	var exit *cli.ExitError
	if errors.As(err, &exit) {
		if exit.Err != nil {
			fmt.Fprintf(os.Stderr, "podium: %v\n", exit.Err)
		}
		os.Exit(exit.Code)
	}
	fmt.Fprintf(os.Stderr, "podium: %v\n", err)
	os.Exit(cli.ExitUsage)
}
