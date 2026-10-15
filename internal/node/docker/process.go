package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/network"

	"github.com/podium-ade/podium/pkg/spec"
)

// ProcessOptions configures NewProcess.
type ProcessOptions struct {
	// DataDir holds the executor's own state.
	DataDir string
	// User is the account the task runs as. Empty runs it as this process's own user.
	User string
	// Sudo prefixes every privileged step with `sudo -n`, for an executor that is not root
	// but may become it.
	Sudo bool
	// Sidecars is the machine's Docker engine, which runs the task's sidecars. Nil means
	// this machine runs none.
	Sidecars *Executor
	Logger   *slog.Logger
}

// ProcessExecutor runs a task's command directly on this machine instead of in a
// container: the machine is already the task's own, a cloud sandbox made for it. It keeps
// the container contract a task sees — podium-runner as the parent, the event and inbox
// sockets, /workspace, /podium/secrets, PODIUM_* — and the event stream the node relays.
type ProcessExecutor struct {
	base   *Executor
	o      ProcessOptions
	runner string
}

const (
	// processRoot is where the runner and sockets live: the same /podium a container has.
	processRoot = "/podium"
	processSock = processRoot + "/run"
)

// NewProcess prepares a process executor.
func NewProcess(o ProcessOptions) (*ProcessExecutor, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	p := &ProcessExecutor{o: o}
	for _, dir := range []string{processRoot, processSock} {
		if err := p.privileged("install", "-d", "-m", "0755", dir); err != nil {
			return nil, err
		}
	}
	// The sockets are made by this process and dialled by the task's user.
	if err := p.privileged("chown", strconv.Itoa(os.Getuid()), processSock); err != nil {
		return nil, err
	}
	data, err := runnerbin.ReadFile("runnerbin/runner-linux-" + runtime.GOARCH)
	if err != nil {
		return nil, fmt.Errorf("process executor: no podium-runner embedded for linux/%s: %w", runtime.GOARCH, err)
	}
	p.runner = processRoot + "/runner"
	if err := p.privilegedStdin(bytes.NewReader(data), "install", "-m", "0755", "/dev/stdin", p.runner); err != nil {
		return nil, err
	}
	p.base = &Executor{dataDir: o.DataDir, sockDir: processSock, log: o.Logger, runs: map[string]*runState{}}
	return p, nil
}

func (p *ProcessExecutor) privileged(name string, args ...string) error {
	return p.privilegedStdin(nil, name, args...)
}

func (p *ProcessExecutor) privilegedStdin(stdin io.Reader, name string, args ...string) error {
	if p.o.Sudo {
		args = append([]string{"-n", name}, args...)
		name = "sudo"
	}
	cmd := exec.Command(name, args...) //nolint:gosec // fixed commands
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

func (p *ProcessExecutor) owner() string {
	if p.o.User == "" {
		return strconv.Itoa(os.Getuid())
	}
	return p.o.User
}

// Inject delivers text to the task's inbox, as Executor.Inject does.
func (p *ProcessExecutor) Inject(taskID, text string) error { return p.base.Inject(taskID, text) }

// HeldExit is the exit of a held task's command, which the runner wrote down.
func (p *ProcessExecutor) HeldExit(string) (int, bool) {
	b, err := os.ReadFile(processRoot + "/hold/" + holdExitRel)
	if err != nil {
		return 0, false
	}
	return parseHoldExit(b)
}

// Cancel sends the task SIGTERM, then SIGKILL after the same grace a container gets.
func (p *ProcessExecutor) Cancel(taskID string) {
	rs := p.base.lookup(taskID)
	if rs == nil {
		return
	}
	rs.mu.Lock()
	rs.cancelled = true
	pid, _ := strconv.Atoi(rs.containerID)
	rs.mu.Unlock()
	if pid > 0 {
		p.kill(rs, pid)
	}
}

func (p *ProcessExecutor) kill(rs *runState, pid int) {
	rs.once.Do(func() {
		go func() {
			// sudo passes a signal on to its command, so the wrapper's pid is enough.
			_ = syscall.Kill(pid, syscall.SIGTERM)
			select {
			case <-rs.done:
			case <-time.After(cancelGrace):
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}()
	})
}

// Run runs one task as a process.
func (p *ProcessExecutor) Run(ctx context.Context, req Request, events chan<- Event) (Result, error) {
	em := newEmitter(ctx, events)
	em.emit(KindProvisioning, nil)
	rs, err := p.base.register(req.TaskID)
	if err != nil {
		em.emit(KindError, ErrorPayload{Message: err.Error(), AbortsRun: true})
		return Result{}, err
	}
	defer p.base.unregister(req.TaskID, rs)

	res, err := p.run(ctx, req, em, rs)
	if err != nil {
		em.emit(KindError, ErrorPayload{Message: err.Error(), Retryable: !errors.Is(err, errSpec) && !errors.Is(err, errSidecarNotReady), AbortsRun: true})
		return Result{}, err
	}
	return res, nil
}

func (p *ProcessExecutor) run(ctx context.Context, req Request, em *emitter, rs *runState) (Result, error) {
	sp := req.Spec
	if len(sp.Command) == 0 {
		return Result{}, fmt.Errorf("%w: a task run as a process has no image to take a command from; give it one", errSpec)
	}
	if sp.Hardening.ReadOnlyRootfs || len(sp.Hardening.Capabilities) > 0 {
		return Result{}, fmt.Errorf("%w: read_only_rootfs and capabilities are container settings; a sandbox task runs unprivileged instead", errSpec)
	}
	if len(sp.Sidecars) > 0 && p.o.Sidecars == nil {
		return Result{}, fmt.Errorf("%w: sidecars need Docker, which this sandbox does not have", errSpec)
	}
	for name, sc := range sp.Sidecars {
		if sc.ShareWorkspace {
			return Result{}, fmt.Errorf("%w: sidecar %s: share_workspace is not supported for a task run as a process", errSpec, name)
		}
	}
	workdir := sp.WorkingDir
	if workdir == "" {
		workdir = spec.DefaultWorkingDir
	}
	if err := p.privileged("install", "-d", "-m", "0755", "-o", p.owner(), workspacePath); err != nil {
		return Result{}, err
	}
	if workdir != workspacePath {
		if err := p.privileged("install", "-d", "-m", "0755", "-o", p.owner(), workdir); err != nil {
			return Result{}, err
		}
	}
	if err := p.restoreWorkspace(ctx, req); err != nil {
		return Result{}, err
	}

	if len(sp.Sidecars) > 0 {
		set, err := p.startSidecars(ctx, req, em)
		if set != nil {
			defer set.stopLogs()
		}
		if err != nil {
			return Result{}, err
		}
	}

	expose := sp.Expose
	switch {
	case expose.Publishes():
		if req.Preview == nil {
			return Result{}, fmt.Errorf("%w: the task exposes ports and this node publishes no previews", errPreviewNotSupported)
		}
		em.emit(KindPreview, PreviewPayload{Via: req.Preview.Via, Address: req.Preview.Address, URLs: req.Preview.URLs(expose)})
	case expose != nil:
		em.emit(KindPreview, PreviewPayload{})
	}

	link, err := p.base.listenRunner(req.TaskID)
	if err != nil {
		return Result{}, err
	}
	defer link.close()
	inbox, err := p.base.listenInbox(req.TaskID)
	if err != nil {
		return Result{}, err
	}
	defer inbox.close()
	rs.mu.Lock()
	rs.inbox = inbox
	rs.mu.Unlock()

	env := containerEnv(req, workdir)
	for i, kv := range env {
		switch {
		case strings.HasPrefix(kv, "PODIUM_EVENTS_SOCK="):
			env[i] = "PODIUM_EVENTS_SOCK=" + link.path
		case strings.HasPrefix(kv, "PODIUM_INBOX_SOCK="):
			env[i] = "PODIUM_INBOX_SOCK=" + inbox.path
		}
	}
	if expose != nil {
		if err := p.privileged("install", "-d", "-m", "0755", "-o", p.owner(), processRoot+"/hold"); err != nil {
			return Result{}, err
		}
		env = append(env, holdEnvVar+"=1")
	}
	if expose.Publishes() {
		env = append(env, req.Preview.Env(expose)...)
	}
	secretEnv, err := p.stageSecrets(req.Secrets)
	defer func() {
		for i := range req.Secrets {
			zero(req.Secrets[i].Value)
		}
		for i := range req.Registries {
			zero(req.Registries[i].Password)
		}
	}()
	if err != nil {
		return Result{}, err
	}
	env = append(env, secretEnv...)
	home := "/home/" + p.o.User
	if p.o.User == "" {
		home = os.Getenv("HOME")
	}
	env = append(env, "HOME="+home, "USER="+p.owner(), "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")

	argv := append(p.launch(sp.Resources, workdir, env), p.runner, "--")
	argv = append(argv, sp.Command...)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // the task's own command
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout := &logWriter{em: em, stream: StreamStdout}
	stderr := &logWriter{em: em, stream: StreamStderr}
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	cmd.Stdout, cmd.Stderr = outW, errW
	copied := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stdout, outR); copied <- struct{}{} }()
	go func() { _, _ = io.Copy(stderr, errR); copied <- struct{}{} }()

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start task: %w", err)
	}
	rs.mu.Lock()
	rs.containerID = strconv.Itoa(cmd.Process.Pid)
	pending := rs.cancelled
	rs.mu.Unlock()
	if pending {
		p.kill(rs, cmd.Process.Pid)
	}
	if _, ok := link.awaitStarted(runnerConnectTimeout); !ok {
		p.base.log.Warn("no runner started event", "task", req.TaskID)
	}
	em.emit(KindStarted, nil)

	artifacts := &artifactCollector{e: p.base, req: req, em: em, copyFrom: p.copyFrom}
	heldCh := make(chan int, 1)
	drained := link.drain(req.TaskID, em, func(name, path, contentType string) {
		artifacts.submit(ctx, name, path, contentType)
	}, func(code int) { heldCh <- code })
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	held, exitCode := false, 0
	select {
	case exitCode = <-heldCh:
		held = true
	case werr := <-waitCh:
		exitCode = exitCodeOf(werr)
	}
	link.stopAccepting()
	if held {
		link.hangUp()
	} else {
		_ = outW.Close()
		_ = errW.Close()
		for range 2 {
			select {
			case <-copied:
			case <-time.After(logDrainTimeout):
			}
		}
	}
	select {
	case <-drained:
	case <-time.After(logDrainTimeout):
	}
	artifacts.wait(artifactUploadTimeout)
	artifacts.collectDir(ctx, AutoArtifactDir)
	if err := p.saveWorkspace(ctx, req); err != nil {
		p.base.log.Warn("save workspace", "task", req.TaskID, "error", err)
	}
	p.shredSecrets(req.Secrets)

	usage := Usage{WallMS: time.Since(startedAt).Milliseconds()}
	em.emit(KindExited, ExitedPayload{ExitCode: exitCode})
	em.emit(KindFinished, FinishedPayload{ExitCode: exitCode, Usage: usage, Held: held})
	return Result{ExitCode: exitCode, Usage: usage, Held: held}, nil
}

// launch is the argv that starts the runner as the task's user, inside a systemd scope
// carrying the task's limits when this machine has systemd. The caller appends the runner
// and the command.
func (p *ProcessExecutor) launch(r spec.Resources, workdir string, env []string) []string {
	var argv []string
	if p.o.Sudo {
		argv = append(argv, "sudo", "-n")
	}
	if _, err := exec.LookPath("systemd-run"); err == nil && (p.o.Sudo || os.Getuid() == 0) {
		argv = append(argv, "systemd-run", "--scope", "--quiet", "--collect")
		if r.MemoryMB > 0 {
			argv = append(argv, "-p", fmt.Sprintf("MemoryMax=%dM", r.MemoryMB), "-p", "MemorySwapMax=0")
		}
		if r.CPU > 0 {
			argv = append(argv, "-p", fmt.Sprintf("CPUQuota=%d%%", int(r.CPU*100)))
		}
		if r.PIDs > 0 {
			argv = append(argv, "-p", fmt.Sprintf("TasksMax=%d", r.PIDs))
		}
		argv = append(argv, "--")
	}
	if p.o.User != "" {
		argv = append(argv, "setpriv", "--reuid="+p.o.User, "--regid="+p.o.User, "--init-groups", "--no-new-privs", "--")
	}
	argv = append(argv, "env", "-i", "-C", workdir)
	return append(argv, env...)
}

// stageSecrets turns env secrets into environment entries and writes file secrets where
// they ask to be, readable by the task's user only. /podium/secrets is a tmpfs.
func (p *ProcessExecutor) stageSecrets(secrets []Secret) ([]string, error) {
	var env []string
	mounted := false
	for _, s := range secrets {
		if s.Target != spec.SecretTargetFile {
			env = append(env, s.Key+"="+string(s.Value))
			continue
		}
		if !mounted {
			if err := p.privileged("install", "-d", "-m", "0700", "-o", p.owner(), spec.SecretsMount); err != nil {
				return nil, err
			}
			if err := p.privileged("mount", "-t", "tmpfs", "-o", "size=1m,mode=0700,uid="+p.uid()+",noexec,nosuid", "tmpfs", spec.SecretsMount); err != nil {
				p.base.log.Warn("no tmpfs for secrets; they are on disk until the task ends", "error", err)
			}
			mounted = true
		}
		if err := p.privileged("install", "-d", "-m", "0700", "-o", p.owner(), filepath.Dir(s.Key)); err != nil {
			return nil, err
		}
		if err := p.privilegedStdin(bytes.NewReader(s.Value), "install", "-m", "0400", "-o", p.owner(), "/dev/stdin", s.Key); err != nil {
			return nil, fmt.Errorf("write secret %s: %w", s.Name, err)
		}
	}
	return env, nil
}

func (p *ProcessExecutor) shredSecrets(secrets []Secret) {
	for _, s := range secrets {
		if s.Target == spec.SecretTargetFile {
			_ = p.privileged("rm", "-f", s.Key)
		}
	}
	_ = p.privileged("umount", spec.SecretsMount)
}

func (p *ProcessExecutor) uid() string {
	if p.o.User == "" {
		return strconv.Itoa(os.Getuid())
	}
	out, err := exec.Command("id", "-u", p.o.User).Output()
	if err != nil {
		return "0"
	}
	return strings.TrimSpace(string(out))
}

// restoreWorkspace extracts the session tar at /, the way the container path copies it onto
// the volume, and gives it to the task's user.
func (p *ProcessExecutor) restoreWorkspace(ctx context.Context, req Request) error {
	if req.Workspace == nil || req.Workspace.Restore == nil {
		return nil
	}
	body, found, err := req.Workspace.Restore(ctx)
	if err != nil {
		return fmt.Errorf("restore workspace: %w", err)
	}
	if !found || body == nil {
		return nil
	}
	defer func() { _ = body.Close() }()
	if err := p.privilegedStdin(body, "tar", "-x", "-f", "-", "-C", "/", "--no-same-owner"); err != nil {
		return fmt.Errorf("restore workspace: %w", err)
	}
	return p.privileged("chown", "-R", p.owner(), workspacePath)
}

func (p *ProcessExecutor) saveWorkspace(ctx context.Context, req Request) error {
	if req.Workspace == nil || req.Workspace.Save == nil {
		return nil
	}
	f, err := os.CreateTemp("", "podium-ws-"+req.TaskID+"-*.tar")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	args := []string{"tar", "-c", "-f", "-", "-C", "/", strings.TrimPrefix(workspacePath, "/")}
	if p.o.Sudo {
		args = append([]string{"sudo", "-n"}, args...)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // fixed command
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = f, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tar workspace: %w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	st, err := f.Stat()
	if err != nil {
		return err
	}
	return req.Workspace.Save(ctx, f.Name(), st.Size())
}

// copyFrom is the artifact collector's source: a tar of a path on this machine, read with
// the privilege it takes to see a file the task's user made private.
func (p *ProcessExecutor) copyFrom(ctx context.Context, path string) (io.ReadCloser, error) {
	if _, err := os.Lstat(path); err != nil && errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	args := []string{"tar", "-c", "-f", "-", "-C", filepath.Dir(path), filepath.Base(path)}
	if p.o.Sudo {
		args = append([]string{"sudo", "-n"}, args...)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // fixed command
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmdReader{out, cmd}, nil
}

type cmdReader struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (r cmdReader) Close() error {
	_ = r.ReadCloser.Close()
	return r.cmd.Wait()
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return -1
}

// startSidecars runs the task's sidecars on this machine's engine, on a network of their
// own, and names each one in /etc/hosts so the task reaches it the way a container would.
func (p *ProcessExecutor) startSidecars(ctx context.Context, req Request, em *emitter) (*sidecarSet, error) {
	d := p.o.Sidecars
	net, err := d.cli.NetworkCreate(ctx, networkName(req.TaskID), network.CreateOptions{
		Driver: "bridge",
		Labels: taskLabels(req.TaskID, req.LeaseID),
	})
	if err != nil {
		return nil, fmt.Errorf("create network %s: %w", networkName(req.TaskID), err)
	}
	set, err := d.startSidecars(ctx, req, net.ID, em)
	if err != nil {
		return set, err
	}
	var hosts strings.Builder
	for name := range req.Spec.Sidecars {
		insp, err := d.cli.ContainerInspect(ctx, sidecarContainerName(req.TaskID, name))
		if err != nil {
			return set, fmt.Errorf("sidecar %s: %w", name, err)
		}
		ep := insp.NetworkSettings.Networks[networkName(req.TaskID)]
		if ep == nil || ep.IPAddress == "" {
			return set, fmt.Errorf("sidecar %s has no address on the task network", name)
		}
		fmt.Fprintf(&hosts, "%s %s # podium sidecar\n", ep.IPAddress, name)
	}
	if err := p.privilegedStdin(strings.NewReader(hosts.String()), "tee", "-a", "/etc/hosts"); err != nil {
		return set, err
	}
	return set, nil
}
