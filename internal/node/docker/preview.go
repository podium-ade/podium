package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/go-connections/nat"

	"github.com/podium-ade/podium/pkg/spec"
)

const (
	// RoleGateway marks the forwarder a node runs beside a task that exposes ports.
	RoleGateway = "gateway"

	// LabelPreviewVia is the kind of preview, on the gateway container. The gateway's labels
	// are how a restarted node knows what it was keeping up and where, without a file of its
	// own to lose.
	LabelPreviewVia = "podium.preview.via"
	// LabelPreviewAddress is where the preview's ports are reached.
	LabelPreviewAddress = "podium.preview.address"
	// LabelPreviewSlot is the tailnet slot a preview holds; absent for a LAN one.
	LabelPreviewSlot = "podium.preview.slot"

	// gatewayAlias is the gateway's name on the task network. Nothing dials it; it is
	// there so `docker network inspect` says what the extra container is.
	gatewayAlias = "podium-gateway"

	// holdEnvVar and holdDir mirror runner.EnvHold and runner.HoldDir: the env var that
	// makes the runner stay up after the command, and the directory it writes the command's
	// exit to for a node that lost the event socket. The directory is a bind mount of one in
	// the task's state dir, so a restarted node reads the file off its own disk — the
	// engine's copy API cannot see into a tmpfs.
	holdEnvVar  = "PODIUM_HOLD"
	holdDir     = "/podium/hold"
	holdSubdir  = "hold"
	holdExitRel = "exit"

	gatewayMemoryMB = 64
	gatewayPIDs     = 256
)

// Preview is how the node wants a task's exposed ports published. The node picks it; the
// executor only carries it out.
type Preview struct {
	Via string
	// Address is where people reach the ports, and what PODIUM_URL_* are built from.
	Address string
	// BindIP is the host address the gateway's ports are published on.
	BindIP string
	// SamePorts publishes every port under its own number. Without it the engine picks a
	// free host port for each, which is what a tailnet slot forwards to over loopback.
	SamePorts bool
	// Slot is the tailnet slot the preview holds, empty for any other kind.
	Slot string
}

// URLs is every exposed port's address, keyed by its name in the spec.
func (p Preview) URLs(x *spec.Expose) map[string]string {
	out := make(map[string]string, len(x.Ports))
	for name, port := range x.Ports {
		u := url.URL{Scheme: "http", Host: net.JoinHostPort(p.Address, strconv.Itoa(port.Port))}
		out[name] = u.String()
	}
	return out
}

// Env is what the task sees about its own preview: PODIUM_EXPOSE_HOST and one
// PODIUM_URL_<NAME> per port, sorted so the container's environment is reproducible.
func (p Preview) Env(x *spec.Expose) []string {
	urls := p.URLs(x)
	names := make([]string, 0, len(urls))
	for name := range urls {
		names = append(names, name)
	}
	sort.Strings(names)
	env := []string{"PODIUM_EXPOSE_HOST=" + p.Address}
	for _, name := range names {
		env = append(env, spec.URLEnv(name)+"="+urls[name])
	}
	return env
}

// PreviewPayload is the body of a KindPreview event: where the ports are.
type PreviewPayload struct {
	Via     string
	Address string
	URLs    map[string]string
}

func gatewayContainerName(taskID string) string { return "podium-" + taskID + "-gateway" }

// gatewayRoutes is the gateway's argv: one PORT=HOST:PORT per exposed port, the host being
// the owning sidecar's name on the task network or the task container's own alias.
func gatewayRoutes(x *spec.Expose) []string {
	names := make([]string, 0, len(x.Ports))
	for name := range x.Ports {
		names = append(names, name)
	}
	sort.Strings(names)
	routes := make([]string, 0, len(names))
	for _, name := range names {
		p := x.Ports[name]
		host := p.From
		if host == "" {
			host = networkAlias
		}
		port := strconv.Itoa(p.Port)
		routes = append(routes, port+"="+net.JoinHostPort(host, port))
	}
	return routes
}

// startGateway runs the forwarder that publishes a task's exposed ports. It uses the task's
// own image, which is already on this engine, with the runner binary as its entrypoint: the
// runner is static, so any Linux image of the node's architecture can run it, and no second
// image has to be pulled or trusted.
//
// It is hardened like the task container and then some — no capabilities, no new
// privileges, a read-only rootfs and a small memory cap — because all it does is copy bytes.
// It starts before the task container and dials lazily, so a service that only comes up
// once the command has built it is simply refused until then.
func (e *Executor) startGateway(ctx context.Context, req Request, netID string) error {
	x := req.Spec.Expose
	p := req.Preview
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, port := range x.Ports {
		cp := nat.Port(strconv.Itoa(port.Port) + "/tcp")
		exposed[cp] = struct{}{}
		hostPort := ""
		if p.SamePorts {
			hostPort = strconv.Itoa(port.Port)
		}
		bindings[cp] = []nat.PortBinding{{HostIP: p.BindIP, HostPort: hostPort}}
	}

	labels := taskLabels(req.TaskID, req.LeaseID)
	labels[LabelRole] = RoleGateway
	labels[LabelPreviewVia] = p.Via
	labels[LabelPreviewAddress] = p.Address
	if p.Slot != "" {
		labels[LabelPreviewSlot] = p.Slot
	}

	cfg := &container.Config{
		Image:        req.Spec.Image,
		Entrypoint:   strslice.StrSlice{},
		Cmd:          append([]string{runnerTarget, "gateway"}, gatewayRoutes(x)...),
		Labels:       labels,
		ExposedPorts: exposed,
	}
	pids := int64(gatewayPIDs)
	hostCfg := &container.HostConfig{
		Mounts: []mount.Mount{{
			Type:     mount.TypeBind,
			Source:   e.runnerPath,
			Target:   runnerTarget,
			ReadOnly: true,
		}},
		PortBindings:   bindings,
		AutoRemove:     false,
		RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyDisabled},
		SecurityOpt:    []string{noNewPrivileges},
		CapDrop:        []string{"ALL"},
		ReadonlyRootfs: true,
		Resources: container.Resources{
			Memory:     gatewayMemoryMB * megabyte,
			MemorySwap: gatewayMemoryMB * megabyte,
			PidsLimit:  &pids,
		},
	}
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
		networkName(req.TaskID): {NetworkID: netID, Aliases: []string{gatewayAlias}},
	}}
	created, err := e.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, gatewayContainerName(req.TaskID))
	if err != nil {
		return fmt.Errorf("create preview gateway: %w", err)
	}
	if err := e.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		// A published port someone else already holds fails here, not at create.
		return fmt.Errorf("%w: start preview gateway on %s: %w", errPreviewUnavailable, p.BindIP, err)
	}
	return nil
}

// errPreviewUnavailable marks a preview the node could not publish: its address is taken.
// Another node, or this one later, may well have room, so it stays retryable.
var errPreviewUnavailable = errors.New("preview address unavailable")

// errPreviewNotSupported marks a spec that exposes ports reaching a node that publishes no
// previews. The node refuses those before they get here; this is the executor's backstop.
var errPreviewNotSupported = errors.New("previews not supported")

// holds reports whether a task container was started to stay up after its command.
func holds(cfg *container.Config) bool {
	if cfg == nil {
		return false
	}
	for _, kv := range cfg.Env {
		if kv == holdEnvVar+"=1" {
			return true
		}
	}
	return false
}

// holdExitPoll is how often an adopted held task's exit file is looked for.
const holdExitPoll = 2 * time.Second

// holdDirFor is the host side of a held task's holdDir.
func (e *Executor) holdDirFor(taskID string) string {
	return filepath.Join(e.taskDir(taskID), holdSubdir)
}

// mkHoldDir creates holdDirFor, writable by whichever user the task image runs as.
func (e *Executor) mkHoldDir(taskID string) (string, error) {
	dir := e.holdDirFor(taskID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create hold dir for %s: %w", taskID, err)
	}
	if err := os.Chmod(dir, 0o777); err != nil { //nolint:gosec // one exit code, written by the task's own runner
		return "", fmt.Errorf("chmod hold dir for %s: %w", taskID, err)
	}
	return dir, nil
}

// pollHoldExit reports the exit code the runner wrote to the hold dir, once it exists.
func (e *Executor) pollHoldExit(ctx context.Context, taskID string) <-chan int {
	out := make(chan int, 1)
	go func() {
		t := time.NewTicker(holdExitPoll)
		defer t.Stop()
		for {
			if code, ok := e.readHoldExit(taskID); ok {
				out <- code
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return out
}

// HeldExit is the exit code of a held task's command, and whether it has exited: a task
// container that is still running with this file written is a preview, not a run.
func (e *Executor) HeldExit(taskID string) (int, bool) { return e.readHoldExit(taskID) }

func (e *Executor) readHoldExit(taskID string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(e.holdDirFor(taskID), holdExitRel))
	if err != nil {
		return 0, false
	}
	var ev runnerEvent
	if err := json.Unmarshal(b, &ev); err != nil {
		return 0, false
	}
	return ev.ExitCode, true
}

// HeldPreview is a gateway this engine is still running, as its labels describe it.
type HeldPreview struct {
	TaskID  string
	Via     string
	Address string
	Slot    string
	// Ports maps each exposed container port to the host port it is published on.
	Ports map[int]int
}

// ListPreviews is every preview gateway on this engine, running or not.
func (e *Executor) ListPreviews(ctx context.Context) ([]HeldPreview, error) {
	list, err := e.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", LabelRole+"="+RoleGateway)),
	})
	if err != nil {
		return nil, fmt.Errorf("list preview gateways: %w", err)
	}
	out := make([]HeldPreview, 0, len(list))
	for _, c := range list {
		hp := HeldPreview{
			TaskID:  c.Labels[LabelTask],
			Via:     c.Labels[LabelPreviewVia],
			Address: c.Labels[LabelPreviewAddress],
			Slot:    c.Labels[LabelPreviewSlot],
			Ports:   map[int]int{},
		}
		for _, p := range c.Ports {
			if p.PublicPort != 0 {
				hp.Ports[int(p.PrivatePort)] = int(p.PublicPort)
			}
		}
		out = append(out, hp)
	}
	return out, nil
}

// GatewayPorts is where the engine published a task's exposed ports: container port to
// host port. With SamePorts they are equal; otherwise this is how a tailnet slot finds them.
func (e *Executor) GatewayPorts(ctx context.Context, taskID string) (map[int]int, error) {
	insp, err := e.cli.ContainerInspect(ctx, gatewayContainerName(taskID))
	if err != nil {
		return nil, fmt.Errorf("inspect preview gateway of %s: %w", taskID, err)
	}
	out := map[int]int{}
	if insp.NetworkSettings == nil {
		return out, nil
	}
	for cp, binds := range insp.NetworkSettings.Ports {
		for _, b := range binds {
			hp, err := strconv.Atoi(b.HostPort)
			if err == nil && hp > 0 {
				out[cp.Int()] = hp
				break
			}
		}
	}
	return out, nil
}
