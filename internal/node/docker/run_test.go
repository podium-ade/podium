package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
)

// inspectOf builds the two pieces of a container inspect exitedOOM reads.
func inspectOf(exitCode int, oomFlag bool, memoryLimit int64) *container.InspectResponse {
	return &container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{
		State:      &container.State{ExitCode: exitCode, OOMKilled: oomFlag},
		HostConfig: &container.HostConfig{Resources: container.Resources{Memory: memoryLimit}},
	}}
}

// The engine's flag is the certain answer and the inference is the fallback, so the cases
// worth pinning are the ones where the flag is missing: which of them the node is willing
// to call an OOM kill, and which it refuses to.
func TestAMemoryLimitedSIGKILLIsAnOOMKillEvenWithoutTheEnginesFlag(t *testing.T) {
	const memory = 64 * megabyte

	tests := map[string]struct {
		insp           *container.InspectResponse
		exitCode       int
		killedByPodium bool
		want           bool
	}{
		"the engine says so": {
			insp: inspectOf(exitSIGKILL, true, memory), exitCode: exitSIGKILL, want: true,
		},
		"the engine says so with no limit of ours": {
			// A limit inherited from outside Podium, or the host's own OOM killer: the
			// engine saw the cgroup event and that is not ours to second-guess.
			insp: inspectOf(exitSIGKILL, true, 0), exitCode: exitSIGKILL, want: true,
		},
		"the flag never arrived": {
			insp: inspectOf(exitSIGKILL, false, memory), exitCode: exitSIGKILL, want: true,
		},
		"we sent the kill ourselves": {
			// A cancel or a timeout. Reporting the operator's own SIGKILL as an OOM kill
			// would be a worse answer than the one the control plane already has.
			insp: inspectOf(exitSIGKILL, false, memory), exitCode: exitSIGKILL,
			killedByPodium: true, want: false,
		},
		"no memory limit was set": {
			insp: inspectOf(exitSIGKILL, false, 0), exitCode: exitSIGKILL, want: false,
		},
		"the task was terminated, not killed": {
			// 143 is SIGTERM: the process was asked and agreed. The OOM killer never asks.
			insp: inspectOf(143, false, memory), exitCode: 143, want: false,
		},
		"the task simply failed": {
			insp: inspectOf(1, false, memory), exitCode: 1, want: false,
		},
		"the inspect did not come back": {
			insp: nil, exitCode: exitSIGKILL, want: false,
		},
		"the inspect has no state": {
			insp: &container.InspectResponse{}, exitCode: exitSIGKILL, want: false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, exitedOOM(tc.insp, tc.exitCode, tc.killedByPodium))
		})
	}
}
