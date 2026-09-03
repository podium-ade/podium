package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/alvaroibarguen/podium/pkg/spec"
)

const (
	// workspacePath is where the task's workspace volume is mounted.
	workspacePath = "/workspace"
	// networkAlias is the DNS name the task container answers to on its own
	// network.
	networkAlias = "task"
	// maxLogChunk bounds a single log event's payload.
	maxLogChunk = 64 * 1024
	// pullProgressInterval throttles pulling events.
	pullProgressInterval = time.Second
	// statsInterval is how often container stats are sampled.
	statsInterval = 2 * time.Second
	// logDrainTimeout bounds how long we wait for the log stream to end after
	// the container has exited.
	logDrainTimeout = 5 * time.Second
	// exitSIGKILL is the exit status of a process the kernel killed with SIGKILL, which
	// is how an OOM kill looks from the outside.
	exitSIGKILL = 137
	// oomFlagGrace and oomFlagPoll bound the wait for the engine's OOM flag to catch up
	// with the container's exit. See exitedOOM.
	oomFlagGrace = time.Second
	oomFlagPoll  = 50 * time.Millisecond
)

// Request is one assignment to execute.
type Request struct {
	TaskID  string
	LeaseID string
	Spec    spec.TaskSpec
}

// Usage is a best-effort resource accounting for a finished task.
type Usage struct {
	CPUSeconds   float64
	PeakMemoryMB int64
	WallMS       int64
}

// Result is what a completed run reports back to the caller.
type Result struct {
	ExitCode  int
	OOMKilled bool
	Usage     Usage
}

// Run executes req to completion, emitting ordered events on events. It returns
// a Result for any run that produced an exit code, even a non-zero one; an
// error means the task could not be run at all, in which case an error event
// was emitted first and every resource this call created has been removed.
//
// Run never closes events; the channel belongs to the caller. On success the
// caller is responsible for calling Teardown.
func (e *Executor) Run(ctx context.Context, req Request, events chan<- Event) (Result, error) {
	if req.TaskID == "" {
		return Result{}, errors.New("docker executor: run: TaskID is required")
	}

	em := newEmitter(ctx, events)
	em.emit(KindProvisioning, nil)

	rs, err := e.register(req.TaskID)
	if err != nil {
		em.emit(KindError, ErrorPayload{Message: err.Error(), Retryable: false})
		return Result{}, err
	}
	defer e.unregister(req.TaskID, rs)

	res, err := e.run(ctx, req, em, rs)
	if err != nil {
		retryable := !errors.Is(err, errSpec) && !errors.Is(err, errSidecarNotReady)
		em.emit(KindError, ErrorPayload{Message: err.Error(), Retryable: retryable})
		// Never leak: drop anything this call created, on a context that
		// survives the caller cancelling.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		defer cancel()
		if terr := e.Teardown(cleanupCtx, req.TaskID, false); terr != nil {
			e.log.Error("teardown after failed run", "task", req.TaskID, "error", terr)
		}
		return Result{}, err
	}
	return res, nil
}

// errSpec marks failures caused by the task spec rather than by the engine or
// the network; they are not worth retrying.
var errSpec = errors.New("invalid task spec")

func (e *Executor) run(ctx context.Context, req Request, em *emitter, rs *runState) (Result, error) {
	if req.Spec.Image == "" {
		return Result{}, fmt.Errorf("%w: image is required", errSpec)
	}

	if err := e.ensureImage(ctx, req.Spec.Image, em); err != nil {
		return Result{}, err
	}

	command, err := e.resolveCommand(ctx, req.Spec)
	if err != nil {
		return Result{}, err
	}

	if err := e.mkTaskDir(req.TaskID); err != nil {
		return Result{}, err
	}

	labels := taskLabels(req.TaskID, req.LeaseID)

	netResp, err := e.cli.NetworkCreate(ctx, networkName(req.TaskID), network.CreateOptions{
		Driver:   "bridge",
		Internal: false,
		Labels:   labels,
	})
	if err != nil {
		return Result{}, fmt.Errorf("create network %s: %w", networkName(req.TaskID), err)
	}

	if _, err := e.cli.VolumeCreate(ctx, volume.CreateOptions{
		Name:   volumeName(req.TaskID),
		Labels: labels,
	}); err != nil {
		return Result{}, fmt.Errorf("create volume %s: %w", volumeName(req.TaskID), err)
	}

	// Sidecars are siblings on the task network, addressed by the name they are keyed
	// under. The task container is not created until every one of them is ready, and
	// their log streams are stopped again before this run emits its exit — a log event
	// after `finished` would break the ordering the whole event contract rests on.
	sidecars, serr := e.startSidecars(ctx, req, netResp.ID, em)
	defer sidecars.stopLogs()
	if serr != nil {
		return Result{}, serr
	}

	workdir := req.Spec.WorkingDir
	if workdir == "" {
		workdir = spec.DefaultWorkingDir
	}

	// The runner is PID 1 and the task command is its child. The image's own entrypoint is
	// cleared — a non-nil empty Entrypoint is how the Docker API says "ignore the image's"
	// — because anything it would have prepended is already in command.
	link, err := e.listenRunner(req.TaskID)
	if err != nil {
		return Result{}, err
	}
	defer link.close()

	initFalse := false
	cfg := &container.Config{
		Image:      req.Spec.Image,
		Entrypoint: strslice.StrSlice{},
		Cmd:        append([]string{runnerTarget, "--"}, command...),
		WorkingDir: workdir,
		Env:        containerEnv(req, workdir),
		Labels:     labels,
	}
	hostCfg := &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeVolume,
				Source: volumeName(req.TaskID),
				Target: workspacePath,
			},
			{
				Type:     mount.TypeBind,
				Source:   e.runnerPath,
				Target:   runnerTarget,
				ReadOnly: true,
			},
		},
		// The event socket goes in the legacy Binds form on purpose. Docker Desktop
		// validates a Mounts-style bind by stat-ing the source through its file-sharing
		// namespace, where Unix sockets are not visible, and rejects every socket source
		// with "bind source path does not exist". Binds is the path `docker run -v` takes
		// and the one that makes /var/run/docker.sock mountable; it works on both Docker
		// Desktop and a native Linux engine.
		Binds:         []string{link.path + ":" + eventsTarget},
		AutoRemove:    false,
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
		Init:          &initFalse,
	}
	applyResources(hostCfg, req.Spec.Resources)
	applyTaskHardening(hostCfg, req.Spec.Hardening)
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
		networkName(req.TaskID): {NetworkID: netResp.ID, Aliases: []string{networkAlias}},
	}}

	created, err := e.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, containerName(req.TaskID))
	if err != nil {
		return Result{}, fmt.Errorf("create container for task %s: %w", req.TaskID, err)
	}
	cid := created.ID

	// Subscribe to the exit before starting, so a container that exits
	// instantly is still observed. NextExit, not NotRunning: a container that
	// has been created but not started is already "not running", and
	// NotRunning would return exit code 0 immediately.
	waitCh, waitErrCh := e.cli.ContainerWait(ctx, cid, container.WaitConditionNextExit)

	startedAt := time.Now()
	if err := e.cli.ContainerStart(ctx, cid, container.StartOptions{}); err != nil {
		return Result{}, fmt.Errorf("start container for task %s: %w", req.TaskID, err)
	}

	// A Cancel that arrived before the container existed applies now.
	rs.mu.Lock()
	rs.containerID = cid
	pending := rs.cancelled
	rs.mu.Unlock()
	if pending {
		e.startKill(rs, cid)
	}

	// started is the runner's to declare: it means the task command has been forked, not
	// merely that the engine accepted the container. If no runner reports in within
	// runnerConnectTimeout the event is emitted anyway, because the status transition it
	// drives (provisioning -> running) must never go missing.
	if pid, ok := link.awaitStarted(runnerConnectTimeout); ok {
		e.log.Debug("runner started the task command", "task", req.TaskID, "pid", pid)
	} else {
		e.log.Warn("no runner started event; falling back to the docker api", "task", req.TaskID)
	}
	em.emit(KindStarted, nil)

	runnerDone := link.drain(req.TaskID, em)
	logsDone := e.streamLogs(ctx, cid, em)

	statsCtx, stopStats := context.WithCancel(ctx)
	defer stopStats()
	usageCh := e.sampleUsage(statsCtx, cid)

	var exitCode int
	select {
	case werr := <-waitErrCh:
		stopStats()
		if werr == nil {
			werr = errors.New("wait ended without a result")
		}
		return Result{}, fmt.Errorf("wait for container of task %s: %w", req.TaskID, werr)
	case wr := <-waitCh:
		exitCode = int(wr.StatusCode)
		if wr.Error != nil && wr.Error.Message != "" {
			e.log.Warn("container wait reported an error", "task", req.TaskID, "error", wr.Error.Message)
		}
		stopStats()
	}
	wallMS := time.Since(startedAt).Milliseconds()

	// Let both streams drain so every log and step event precedes exited.
	select {
	case <-logsDone:
	case <-time.After(logDrainTimeout):
		e.log.Warn("log stream did not close after exit", "task", req.TaskID)
	}
	select {
	case <-runnerDone:
	case <-time.After(logDrainTimeout):
		e.log.Warn("runner event stream did not close after exit", "task", req.TaskID)
	}

	// Nothing may emit after this point but exited and finished, so the sidecars' log
	// streams end here rather than when the run returns.
	sidecars.stopLogs()

	usage := <-usageCh
	usage.WallMS = wallMS

	oom := e.exitedOOM(ctx, cid, exitCode, req.Spec.Resources.MemoryMB > 0)

	em.emit(KindExited, ExitedPayload{ExitCode: exitCode, OOMKilled: oom})
	em.emit(KindFinished, FinishedPayload{ExitCode: exitCode, Usage: usage})

	return Result{ExitCode: exitCode, OOMKilled: oom, Usage: usage}, nil
}

// exitedOOM reports whether the container was killed for exceeding its memory limit.
//
// The engine learns of the exit and of the cgroup's OOM event on two different paths, and
// the OOM flag can land a few milliseconds after ContainerWait has already returned. A
// single inspect therefore reports a plain SIGKILL for a task that really was OOM-killed,
// which is exactly the difference this step exists to make visible. So a memory-limited
// task that died of SIGKILL is re-inspected for a moment before the flag is believed to be
// absent; every other exit is inspected once and answered immediately.
func (e *Executor) exitedOOM(ctx context.Context, cid string, exitCode int, memoryLimited bool) bool {
	deadline := time.Now()
	if memoryLimited && exitCode == exitSIGKILL {
		deadline = deadline.Add(oomFlagGrace)
	}
	for {
		insp, err := e.cli.ContainerInspect(ctx, cid)
		if err != nil {
			e.log.Warn("inspect after exit", "container", cid, "error", err)
			return false
		}
		if insp.State != nil && insp.State.OOMKilled {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-time.After(oomFlagPoll):
		case <-ctx.Done():
			return false
		}
	}
}

func (e *Executor) mkTaskDir(taskID string) error {
	if err := os.MkdirAll(e.taskDir(taskID), 0o700); err != nil {
		return fmt.Errorf("create task dir for %s: %w", taskID, err)
	}
	return nil
}

func containerEnv(req Request, workdir string) []string {
	keys := make([]string, 0, len(req.Spec.Env))
	for k := range req.Spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys)+3)
	for _, k := range keys {
		env = append(env, k+"="+req.Spec.Env[k])
	}
	env = append(env,
		"PODIUM_TASK_ID="+req.TaskID,
		"PODIUM_LEASE_ID="+req.LeaseID,
		"PODIUM_WORKDIR="+workdir,
		"PODIUM_EVENTS_SOCK="+eventsTarget,
	)
	return env
}

// resolveCommand is what the runner execs. A spec that names no command falls back to the
// image's own entrypoint and default arguments, which the runner has displaced.
func (e *Executor) resolveCommand(ctx context.Context, s spec.TaskSpec) ([]string, error) {
	if len(s.Command) > 0 {
		return s.Command, nil
	}
	cmd, err := e.imageCommand(ctx, s.Image)
	if err != nil {
		return nil, err
	}
	if len(cmd) == 0 {
		return nil, fmt.Errorf("%w: no command given and image %s defines none", errSpec, s.Image)
	}
	return cmd, nil
}

// ensureImage pulls the image unless the engine already has it, emitting
// throttled pulling events while it does.
func (e *Executor) ensureImage(ctx context.Context, ref string, em *emitter) error {
	if _, err := e.cli.ImageInspect(ctx, ref); err == nil {
		return nil
	}

	rc, err := e.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()

	type progressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	}
	type pullMessage struct {
		Status         string         `json:"status"`
		ID             string         `json:"id"`
		ProgressDetail progressDetail `json:"progressDetail"`
		Error          string         `json:"error"`
	}

	dec := json.NewDecoder(rc)
	var last time.Time
	for {
		var msg pullMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("pull image %s: read progress: %w", ref, err)
		}
		if msg.Error != "" {
			return fmt.Errorf("pull image %s: %s", ref, msg.Error)
		}
		if now := time.Now(); last.IsZero() || now.Sub(last) >= pullProgressInterval {
			last = now
			em.emit(KindPulling, PullingPayload{
				Image:   ref,
				Status:  msg.Status,
				LayerID: msg.ID,
				Current: msg.ProgressDetail.Current,
				Total:   msg.ProgressDetail.Total,
			})
		}
	}

	// The pull stream reports per-layer errors inline and only fails the HTTP
	// request for transport problems, so confirm the image really landed.
	if _, err := e.cli.ImageInspect(ctx, ref); err != nil {
		return fmt.Errorf("pull image %s: image not present after pull: %w", ref, err)
	}
	return nil
}

// streamLogs demultiplexes the container's log stream into log events. The
// returned channel is closed once the stream ends.
func (e *Executor) streamLogs(ctx context.Context, cid string, em *emitter) <-chan struct{} {
	return e.streamLogsFrom(ctx, cid, em, 0, 0)
}

// streamLogsFrom is streamLogs with a per-stream byte offset already accounted
// for. Docker replays a container's whole output on every attach and offers no
// cursor, so an adopting daemon reads from the beginning and discards the bytes
// a previous incarnation already delivered.
func (e *Executor) streamLogsFrom(ctx context.Context, cid string, em *emitter, skipStdout, skipStderr int64) <-chan struct{} {
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
			e.log.Error("attach container logs", "container", cid, "error", err)
			return
		}
		defer func() { _ = rc.Close() }()

		out := &logWriter{em: em, stream: StreamStdout, skip: skipStdout}
		errw := &logWriter{em: em, stream: StreamStderr, skip: skipStderr}
		if _, err := stdcopy.StdCopy(out, errw, rc); err != nil && !errors.Is(err, context.Canceled) {
			e.log.Warn("demultiplex container logs", "container", cid, "error", err)
		}
	}()
	return done
}

// logWriter turns each demultiplexed frame into one log event, splitting
// anything larger than maxLogChunk. skip discards that many leading bytes of
// the stream before anything is emitted. sidecar is set only for a sidecar's
// output and names which one it came from.
type logWriter struct {
	em      *emitter
	stream  string
	sidecar string
	skip    int64
}

func (w *logWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.skip > 0 {
		if w.skip >= int64(n) {
			w.skip -= int64(n)
			return n, nil
		}
		p = p[w.skip:]
		w.skip = 0
	}
	for off := 0; off < len(p); off += maxLogChunk {
		end := min(off+maxLogChunk, len(p))
		chunk := make([]byte, end-off)
		copy(chunk, p[off:end])
		w.em.emit(KindLog, LogPayload{Stream: w.stream, Sidecar: w.sidecar, Bytes: chunk})
	}
	return n, nil
}

// sampleUsage polls container stats until ctx is cancelled and reports the
// accumulated usage on the returned channel. Sampling is best-effort: a task
// shorter than the first sample simply reports zeros.
func (e *Executor) sampleUsage(ctx context.Context, cid string) <-chan Usage {
	out := make(chan Usage, 1)
	go func() {
		var usage Usage
		defer func() { out <- usage }()

		ticker := time.NewTicker(statsInterval)
		defer ticker.Stop()
		for {
			if cpu, mem, ok := e.sampleOnce(ctx, cid); ok {
				if cpu > usage.CPUSeconds {
					usage.CPUSeconds = cpu
				}
				if mb := mem / (1024 * 1024); mb > usage.PeakMemoryMB {
					usage.PeakMemoryMB = mb
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return out
}

func (e *Executor) sampleOnce(ctx context.Context, cid string) (cpuSeconds float64, memBytes int64, ok bool) {
	resp, err := e.cli.ContainerStatsOneShot(ctx, cid)
	if err != nil {
		return 0, 0, false
	}
	defer func() { _ = resp.Body.Close() }()

	var stats container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return 0, 0, false
	}
	// cgroup v2 reports page cache inside memory.current; subtract it the way
	// `docker stats` does so the number means something.
	mem := stats.MemoryStats.Usage
	if inactive, found := stats.MemoryStats.Stats["inactive_file"]; found && inactive < mem {
		mem -= inactive
	}
	return float64(stats.CPUStats.CPUUsage.TotalUsage) / 1e9, int64(mem), true
}
