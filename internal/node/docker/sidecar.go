package docker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

const (
	// sidecarStopGrace is how long a sidecar gets between its stop signal and SIGKILL
	// during teardown. Shorter than a task's cancel grace: a database that has not shut
	// down in ten seconds is not going to.
	sidecarStopGrace = 10 * time.Second

	// sidecarFailureLogLines is how much of a failed sidecar's output travels in the
	// error, so an operator sees their database's real complaint and not "task failed".
	sidecarFailureLogLines = 100

	// sidecarLogDrainTimeout bounds how long the run waits for a sidecar's log stream to
	// end once it has been told to stop.
	sidecarLogDrainTimeout = 3 * time.Second

	// stepSidecarPrefix names the step events a sidecar produces: sidecar/<name>.
	stepSidecarPrefix = "sidecar/"

	// Step statuses reported for a sidecar.
	stepStarted = "started"
	stepReady   = "ready"
	stepFailed  = "failed"
)

// errSidecarNotReady marks a run that never got past provisioning because a sidecar did
// not come up. Retrying it elsewhere would fail the same way, so it is not retryable.
var errSidecarNotReady = errors.New("sidecar not ready")

// errPrivilegedNotAllowed marks a spec that asks for a privileged sidecar on a node whose
// operator did not allow one.
//
// It is not retryable, and that is a judgement call rather than a certainty: another node
// with the flag on would run it happily. But Podium places tasks on labels alone and knows
// nothing about which nodes allow privilege, so a requeue draws from the same pool and
// almost certainly lands somewhere configured identically — burning the task's attempts to
// print the same sentence three times. Failing once, in words that name the sidecar and
// the flag, is what actually reaches the operator. The pairing that makes placement work
// is a label: start the node with `--allow-privileged-sidecars --labels privileged` and
// have the spec require `labels: [privileged]`.
var errPrivilegedNotAllowed = errors.New("privileged sidecar not allowed on this node")

// sidecarSet is the handle on a task's running sidecars: enough to stop their log streams
// in the right place in the event order. Removing the containers is Teardown's job.
type sidecarSet struct {
	cancelLogs context.CancelFunc

	mu       sync.Mutex
	logsDone []<-chan struct{}
	stopped  bool
}

// startSidecars brings up every sidecar in the spec, attaches to their logs and waits for
// all of them to become ready in parallel. It returns a handle even on failure, so the
// caller's deferred stopLogs still runs; the containers themselves are removed by
// Teardown, which the failure path calls.
func (e *Executor) startSidecars(ctx context.Context, req Request, netID string, em *emitter) (*sidecarSet, error) {
	names := make([]string, 0, len(req.Spec.Sidecars))
	for name := range req.Spec.Sidecars {
		names = append(names, name)
	}
	sort.Strings(names)

	logsCtx, cancelLogs := context.WithCancel(ctx)
	set := &sidecarSet{cancelLogs: cancelLogs}
	if len(names) == 0 {
		return set, nil
	}

	// Before the first pull, because refusing the task costs nothing and fetching a dind
	// image this node will never start costs a gigabyte.
	for _, name := range names {
		if req.Spec.Sidecars[name].Privileged && !e.allowPrivilegedSidecars {
			return set, fmt.Errorf("%w: sidecar %s asks for privileged: true, and this node was started "+
				"without --allow-privileged-sidecars (PODIUM_NODE_ALLOW_PRIVILEGED_SIDECARS). "+
				"That container would be root on this machine's kernel; turn the flag on only on a node "+
				"dedicated to it, label that node, and make the spec require the label",
				errPrivilegedNotAllowed, name)
		}
	}

	// Pull first and in order: two sidecars from the same image would otherwise race for
	// the same layers, and the pulling events would interleave into nonsense.
	for _, name := range names {
		if err := e.ensureImage(ctx, req.Spec.Sidecars[name].Image, em); err != nil {
			return set, fmt.Errorf("sidecar %s: %w", name, err)
		}
	}

	ids := make([]string, len(names))
	starts := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := e.startSidecar(ctx, req, netID, name, req.Spec.Sidecars[name])
			ids[i], starts[i] = id, err
		}()
	}
	wg.Wait()

	for i, name := range names {
		if starts[i] != nil {
			return set, fmt.Errorf("sidecar %s: %w", name, starts[i])
		}
		em.emit(KindStep, StepPayload{Name: stepSidecarPrefix + name, Status: stepStarted})
		set.addLogs(e.streamSidecarLogs(logsCtx, ids[i], name, em))
	}

	// Readiness in parallel: two sidecars that each take ten seconds cost ten, not twenty.
	ready := make([]error, len(names))
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready[i] = e.waitReady(ctx, ids[i], name, req.Spec.Sidecars[name].Readiness)
			if ready[i] == nil {
				em.emit(KindStep, StepPayload{Name: stepSidecarPrefix + name, Status: stepReady})
			}
		}()
	}
	wg.Wait()

	// Every sidecar that failed says so, then the first one explains why: the error is
	// what fails the task, and one diagnosis is enough to act on.
	failed := -1
	for i, name := range names {
		if ready[i] == nil {
			continue
		}
		em.emit(KindStep, StepPayload{Name: stepSidecarPrefix + name, Status: stepFailed})
		if failed < 0 {
			failed = i
		}
	}
	if failed >= 0 {
		return set, fmt.Errorf("%w: sidecar %s not ready: %w\n%s", errSidecarNotReady,
			names[failed], ready[failed], e.sidecarTail(ctx, ids[failed], names[failed]))
	}
	return set, nil
}

// startSidecar creates and starts one sidecar container on the task network, where the
// task reaches it by the name it is keyed under.
func (e *Executor) startSidecar(ctx context.Context, req Request, netID, name string, sc spec.Sidecar) (string, error) {
	labels := sidecarLabels(req.TaskID, req.LeaseID, name)
	cfg := &container.Config{
		Image:  sc.Image,
		Cmd:    sc.Command,
		Env:    sidecarEnv(sc.Env),
		Labels: labels,
	}
	hostCfg := &container.HostConfig{
		AutoRemove:    false,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
	}
	if sc.ShareWorkspace {
		// The same named volume the task has, at the same path. A nested Docker daemon
		// resolves a bind-mount source against its OWN filesystem, so `docker run -v
		// /workspace/x:/x` or a `docker compose` build context under /workspace reaches
		// it only if it sees /workspace at that literal path too.
		hostCfg.Mounts = append(hostCfg.Mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Source: volumeName(req.TaskID),
			Target: workspacePath,
		})
	}
	applyResources(hostCfg, sc.Resources)
	applySidecarHardening(hostCfg, sc.Privileged)

	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
		networkName(req.TaskID): {NetworkID: netID, Aliases: []string{name}},
	}}

	created, err := e.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, sidecarContainerName(req.TaskID, name))
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	if err := e.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return created.ID, fmt.Errorf("start container: %w", err)
	}
	return created.ID, nil
}

// streamSidecarLogs forwards a sidecar's output as log events tagged with its name, so a
// reader can tell the database's complaints from the task's own.
func (e *Executor) streamSidecarLogs(ctx context.Context, cid, name string, em *emitter) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		rc, err := e.cli.ContainerLogs(ctx, cid, container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Tail:       "all",
		})
		if err != nil {
			e.log.Error("attach sidecar logs", "sidecar", name, "container", cid, "error", err)
			return
		}
		defer func() { _ = rc.Close() }()

		w := &logWriter{em: em, stream: StreamSidecar, sidecar: name}
		if _, err := stdcopy.StdCopy(w, w, rc); err != nil && !errors.Is(err, context.Canceled) {
			e.log.Debug("demultiplex sidecar logs", "sidecar", name, "container", cid, "error", err)
		}
	}()
	return done
}

// sidecarTail reads the end of a sidecar's log for an error message. It is best effort:
// a failure to read it must not replace the readiness error that prompted the read.
func (e *Executor) sidecarTail(ctx context.Context, cid, name string) string {
	rc, err := e.cli.ContainerLogs(ctx, cid, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprint(sidecarFailureLogLines),
	})
	if err != nil {
		return fmt.Sprintf("(could not read %s's log: %v)", name, err)
	}
	defer func() { _ = rc.Close() }()

	var out strings.Builder
	if _, err := stdcopy.StdCopy(&out, &out, rc); err != nil {
		e.log.Debug("read sidecar log tail", "sidecar", name, "error", err)
	}
	text := strings.TrimRight(out.String(), "\n")
	if text == "" {
		return fmt.Sprintf("(%s produced no output)", name)
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = "[" + name + "] " + l
	}
	return strings.Join(lines, "\n")
}

func (s *sidecarSet) addLogs(done <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logsDone = append(s.logsDone, done)
}

// stopLogs ends every sidecar log stream and waits for the goroutines to finish, so no
// sidecar log event can be emitted after the run's exited/finished pair. It is idempotent
// and safe to call from a defer as well as from the success path.
func (s *sidecarSet) stopLogs() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	done := s.logsDone
	s.mu.Unlock()

	s.cancelLogs()
	deadline := time.After(sidecarLogDrainTimeout)
	for _, ch := range done {
		select {
		case <-ch:
		case <-deadline:
			return
		}
	}
}

// stopSidecars stops a task's sidecar containers before its task container is removed,
// giving each the grace period the design asks for. Docker sends the image's stop signal
// (SIGTERM unless the image declares another — postgres declares SIGINT, its fast
// shutdown) and escalates to SIGKILL when the grace runs out.
func (e *Executor) stopSidecars(ctx context.Context, summaries []container.Summary) []error {
	var errs []error
	grace := int(sidecarStopGrace.Seconds())
	var wg sync.WaitGroup
	out := make([]error, len(summaries))
	for i, s := range summaries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.cli.ContainerStop(ctx, s.ID, container.StopOptions{Timeout: &grace}); err != nil &&
				!cerrdefs.IsNotFound(err) {
				out[i] = fmt.Errorf("stop sidecar %s: %w", s.Labels[LabelSidecar], err)
			}
		}()
	}
	wg.Wait()
	for _, err := range out {
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

func sidecarContainerName(taskID, name string) string { return "podium-" + taskID + "-" + name }

func sidecarLabels(taskID, leaseID, name string) map[string]string {
	return map[string]string{
		LabelTask:    taskID,
		LabelLease:   leaseID,
		LabelRole:    RoleSidecar,
		LabelSidecar: name,
	}
}

// sidecarEnv is the sidecar's environment, sorted so a container's configuration is a
// function of its spec and nothing else.
func sidecarEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
