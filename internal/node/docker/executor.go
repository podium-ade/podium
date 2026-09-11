package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/api/types/versions"
	"github.com/docker/docker/client"
)

// minAPIVersion is the oldest Docker Engine API this executor is written
// against.
const minAPIVersion = "1.43"

// cancelGrace is how long a cancelled container gets between SIGTERM and
// SIGKILL.
const cancelGrace = 30 * time.Second

// Container labels applied to every resource the executor creates.
const (
	LabelTask    = "podium.task"
	LabelLease   = "podium.lease"
	LabelRole    = "podium.role"
	LabelSidecar = "podium.sidecar"

	// RoleTask marks the single task container of a run.
	RoleTask = "task"
	// RoleSidecar marks a sibling container the task talks to by name.
	RoleSidecar = "sidecar"
)

// Options configures [New].
type Options struct {
	// DataDir is the node's state directory. It must live on the same host as
	// the Docker daemon; a remote DOCKER_HOST is unsupported.
	DataDir string
	// DockerHost overrides the daemon endpoint. Empty means use the
	// environment (DOCKER_HOST, DOCKER_CONTEXT, then the default socket).
	DockerHost string
	// AllowPrivilegedSidecars honours a spec's privileged sidecar on this engine. Off by
	// default: it hands that container root on the node's kernel, which is a decision
	// about the machine and never a spec author's to make. See internal/node.Config.
	AllowPrivilegedSidecars bool
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// Executor runs task containers on one Docker engine.
type Executor struct {
	cli           *client.Client
	dataDir       string
	serverVersion string
	// runnerPath is the extracted podium-runner for this engine's architecture, bind
	// mounted read-only into every task container as PID 1.
	runnerPath string
	// sockDir holds the runner event sockets when the data dir is too deep to fit an
	// AF_UNIX path; empty means they live in the task's own state directory.
	sockDir string
	// directDial is true when this process can reach a task network's bridge itself,
	// which is only so when the daemon runs natively on this Linux host. Readiness
	// probes fall back to ContainerExec everywhere else — notably Docker Desktop, where
	// the engine lives in a VM and a dial to a container address hangs rather than
	// failing.
	directDial bool
	// images is the record of what this node pulled: the LRU bookkeeping, and the
	// allow-list that makes it impossible to remove an image Podium did not fetch.
	images *ImageCache
	// allowPrivilegedSidecars is the operator's answer to Options.AllowPrivilegedSidecars.
	// A spec that asks for a privileged sidecar on a node where this is false fails at
	// provisioning rather than quietly running unprivileged.
	allowPrivilegedSidecars bool
	log                     *slog.Logger

	mu   sync.Mutex
	runs map[string]*runState
}

// runState is the cancellable handle for one in-flight [Executor.Run].
type runState struct {
	done chan struct{}

	mu          sync.Mutex
	containerID string
	cancelled   bool
	inbox       *inboxLink

	once sync.Once
}

// New connects to the Docker engine, verifies it is usable (API >= 1.43,
// cgroup v2) and prepares the executor's state directory.
func New(ctx context.Context, opts Options) (*Executor, error) {
	if opts.DataDir == "" {
		return nil, errors.New("docker executor: DataDir is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	clientOpts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if opts.DockerHost != "" {
		clientOpts = append(clientOpts, client.WithHost(opts.DockerHost))
	}
	cli, err := client.NewClientWithOpts(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("docker executor: new client: %w", err)
	}

	info, err := cli.Info(ctx)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("docker executor: query engine info: %w", err)
	}
	if err := checkEngine(cli.ClientVersion(), info); err != nil {
		_ = cli.Close()
		return nil, err
	}

	if err := os.MkdirAll(filepath.Join(opts.DataDir, "tasks"), 0o700); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("docker executor: create data dir: %w", err)
	}

	arch, err := runnerArch(info.Architecture)
	if err != nil {
		_ = cli.Close()
		return nil, err
	}
	runnerPath, err := extractRunner(opts.DataDir, arch)
	if err != nil {
		_ = cli.Close()
		return nil, err
	}
	sockDir, err := socketDirFor(opts.DataDir)
	if err != nil {
		_ = cli.Close()
		return nil, err
	}

	logger.Info("docker executor ready",
		"readiness_probe", probeStyle(canDialTaskNetworks(runtime.GOOS, info.OSType)),
		"allow_privileged_sidecars", opts.AllowPrivilegedSidecars,
		"api_version", cli.ClientVersion(),
		"server_version", info.ServerVersion,
		"cgroup_version", info.CgroupVersion,
		"os_type", info.OSType,
		"architecture", info.Architecture,
		"data_dir", opts.DataDir,
		"runner", runnerPath,
	)

	return &Executor{
		cli:                     cli,
		dataDir:                 opts.DataDir,
		serverVersion:           info.ServerVersion,
		runnerPath:              runnerPath,
		sockDir:                 sockDir,
		directDial:              canDialTaskNetworks(runtime.GOOS, info.OSType),
		images:                  NewImageCache(opts.DataDir),
		allowPrivilegedSidecars: opts.AllowPrivilegedSidecars,
		log:                     logger,
		runs:                    make(map[string]*runState),
	}, nil
}

// ServerVersion is the Docker Engine version this executor negotiated with, which the
// node reports at enrollment.
func (e *Executor) ServerVersion() string { return e.serverVersion }

// TaskDir is the per-task state directory inside the data dir. It is created by Run and
// removed by Teardown; the node daemon keeps its sequence-space bookmark there.
func (e *Executor) TaskDir(taskID string) string { return e.taskDir(taskID) }

// Close releases the Docker client and the private socket directory, if this executor
// needed one. It does not stop running tasks.
func (e *Executor) Close() error {
	if e.sockDir != "" {
		if err := os.RemoveAll(e.sockDir); err != nil {
			e.log.Warn("remove socket dir", "dir", e.sockDir, "error", err)
		}
	}
	if err := e.cli.Close(); err != nil {
		return fmt.Errorf("docker executor: close client: %w", err)
	}
	return nil
}

// checkEngine validates the negotiated API version and the engine's reported
// capabilities. It is pure so it can be unit-tested without a daemon: the
// cgroup version comes from the Docker API, not from probing /sys/fs/cgroup,
// because the daemon may run in a VM (Docker Desktop) where the host has no
// cgroup filesystem at all.
func checkEngine(apiVersion string, info system.Info) error {
	if apiVersion == "" {
		return fmt.Errorf("docker engine: could not determine API version; podium requires %s or newer", minAPIVersion)
	}
	if versions.LessThan(apiVersion, minAPIVersion) {
		return fmt.Errorf("docker engine: API version %s is too old; podium requires %s or newer (upgrade Docker Engine)", apiVersion, minAPIVersion)
	}
	switch info.CgroupVersion {
	case "2":
		return nil
	case "1":
		return errors.New("docker engine: cgroup v1 is not supported; podium requires cgroup v2 (boot the host with systemd.unified_cgroup_hierarchy=1, or upgrade to a distro that defaults to unified cgroups)")
	case "":
		return errors.New("docker engine: did not report a cgroup version; podium requires cgroup v2")
	default:
		return fmt.Errorf("docker engine: unsupported cgroup version %q; podium requires cgroup v2", info.CgroupVersion)
	}
}

// canDialTaskNetworks reports whether this process shares a network namespace with the
// engine's bridges, which is what makes a direct dial to a container's task-network
// address work. Only a Linux daemon on a Linux engine does; a darwin or Windows host
// talks to an engine inside a VM whose bridges it cannot route to at all.
func canDialTaskNetworks(hostOS, engineOS string) bool {
	return hostOS == "linux" && engineOS == "linux"
}

func probeStyle(direct bool) string {
	if direct {
		return "dial"
	}
	return "exec"
}

func networkName(taskID string) string { return "podium-" + taskID }

func volumeName(taskID string) string { return "podium-ws-" + taskID }

func containerName(taskID string) string { return "podium-" + taskID }

func (e *Executor) taskDir(taskID string) string {
	return filepath.Join(e.dataDir, "tasks", taskID)
}

func taskLabels(taskID, leaseID string) map[string]string {
	return map[string]string{
		LabelTask:  taskID,
		LabelLease: leaseID,
		LabelRole:  RoleTask,
	}
}

func (e *Executor) register(taskID string) (*runState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.runs[taskID]; ok {
		return nil, fmt.Errorf("docker executor: task %s is already running", taskID)
	}
	rs := &runState{done: make(chan struct{})}
	e.runs[taskID] = rs
	return rs, nil
}

func (e *Executor) unregister(taskID string, rs *runState) {
	e.mu.Lock()
	delete(e.runs, taskID)
	e.mu.Unlock()
	close(rs.done)
}

func (e *Executor) lookup(taskID string) *runState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runs[taskID]
}
