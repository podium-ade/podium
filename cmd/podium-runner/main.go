// Command podium-runner is PID 1 inside a Podium task container.
// It stays on plain flag to keep the binary tiny. Real wiring arrives in later steps.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/alvaroibarguen/podium/internal/version"
)

func main() {
	fs := flag.NewFlagSet("podium-runner", flag.ContinueOnError)
	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}

	if *showVersion {
		fmt.Printf("podium-runner %s\n", version.String())
		return
	}

	fs.Usage()
}
