// Package agentbin carries podium-sandbox-agent inside podium-node, the way the docker
// executor carries podium-runner. `make sandbox-agent-embed` builds it; a clone that has not
// run it still compiles, and a sandbox node reports the missing binary when it starts.
package agentbin

import (
	"embed"
	"fmt"
)

// The directory is not called bin: the repository ignores every bin/, and a .gitkeep under
// one could not be committed, which leaves a fresh clone with nothing for this to embed.
//
//go:embed all:linux
var bin embed.FS

// Linux returns the agent for linux/amd64, the architecture sandbox providers run.
func Linux() ([]byte, error) {
	b, err := bin.ReadFile("linux/podium-sandbox-agent-amd64")
	if err != nil {
		return nil, fmt.Errorf("no podium-sandbox-agent embedded; run `make sandbox-agent-embed`: %w", err)
	}
	return b, nil
}
