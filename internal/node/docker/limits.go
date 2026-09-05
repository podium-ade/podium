package docker

import (
	"github.com/docker/docker/api/types/container"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

// The task container's sandbox, applied to every run and not configurable by a spec.
const (
	// noNewPrivileges stops a setuid binary inside the container from raising the
	// process's privileges. It applies to sidecars too.
	noNewPrivileges = "no-new-privileges:true"

	// secretsPath is the tmpfs every task gets for secret files. Step 08 creates it
	// empty; step 09 writes into it. noexec/nosuid because nothing in it is a program,
	// and 1 MB because a secret that does not fit is not a secret.
	secretsPath  = "/podium/secrets"
	secretsTmpfs = "noexec,nosuid,size=1m"

	// tmpPath is the writable scratch a read-only rootfs still needs.
	tmpPath  = "/tmp"
	tmpTmpfs = "size=1g"

	// hostGatewayEntry makes the control-plane host resolvable by name from inside a task
	// container. `host-gateway` is the engine's own placeholder for the bridge gateway
	// address; Docker Desktop provides the name anyway, a native Linux engine does not.
	hostGatewayEntry = "host.docker.internal:host-gateway"
)

const megabyte = 1024 * 1024

// applyResources turns a spec's limits into the engine's. A zero CPU or memory means
// unlimited; PIDs is defaulted to 4096 by spec.ApplyDefaults, so a zero here only happens
// for a spec that never went through it, and means unlimited as the engine defines it.
//
// MemorySwap is pinned to Memory so a container never swaps: a task that would swap is a
// task whose memory limit is wrong, and it should be OOM-killed and reported as such.
func applyResources(hc *container.HostConfig, r spec.Resources) {
	if r.CPU > 0 {
		hc.NanoCPUs = int64(r.CPU * 1e9)
	}
	if r.MemoryMB > 0 {
		hc.Memory = int64(r.MemoryMB) * megabyte
		hc.MemorySwap = hc.Memory
	}
	if r.PIDs > 0 {
		pids := int64(r.PIDs)
		hc.PidsLimit = &pids
	}
}

// applyTaskHardening locks the task container down: every capability dropped and only the
// spec's allow-listed ones added back, no new privileges, the engine's default seccomp
// profile (never unconfined), and an optional read-only rootfs.
//
// The /podium/secrets tmpfs is created here for every task, empty. /workspace is a volume
// and stays writable whatever the rootfs does; /tmp gets a tmpfs when the rootfs is
// read-only, because too much software assumes it can write there.
func applyTaskHardening(hc *container.HostConfig, h spec.Hardening) {
	hc.SecurityOpt = append(hc.SecurityOpt, noNewPrivileges)
	hc.CapDrop = []string{"ALL"}
	for _, c := range h.Capabilities {
		hc.CapAdd = append(hc.CapAdd, spec.NormalizeCapability(c))
	}
	hc.ReadonlyRootfs = h.ReadOnlyRootfs

	if hc.Tmpfs == nil {
		hc.Tmpfs = make(map[string]string, 2)
	}
	hc.Tmpfs[secretsPath] = secretsTmpfs
	if h.ReadOnlyRootfs {
		hc.Tmpfs[tmpPath] = tmpTmpfs
	}
}

// applySidecarHardening is deliberately thinner than applyTaskHardening. A sidecar is a
// stock database or cache image whose entrypoint usually chowns a data directory and drops
// to an unprivileged user, so dropping every capability breaks it. no-new-privileges costs
// nothing and is kept.
//
// privileged is the one hole in that, and only a node's operator can open it: it hands the
// container every capability and the host's devices, which is what a Docker daemon beside
// the task needs to make its own cgroups and mount its own overlay. no-new-privileges
// stays on even then — it was measured against docker:28-dind, which starts and runs
// nested containers under it, and dropping it would widen the hole for nothing.
func applySidecarHardening(hc *container.HostConfig, privileged bool) {
	hc.SecurityOpt = append(hc.SecurityOpt, noNewPrivileges)
	hc.Privileged = privileged
}
