package conductor

// A host turn is the same runtime, running the same brief, as a child of this process
// instead of inside a task container.
//
// Why it exists: a chat message is one exchange in a conversation, and a container per
// message pays an image pull, a clone and a cold start before the first word. A host turn
// answers in seconds and hands the work that actually needs a container — a repository, a
// Docker daemon, a browser — to a task, through the delegation tools.
//
// What it gives up is the fence, and that decides everything else in this file. There is no
// container: the runtime is a child of the conductor, on the machine that holds the master
// key, the provider credential and the operator's own files. So a host turn gets hostTools
// and nothing else — no filesystem tools, no repositories to read — a HOME of its own, and
// an environment built up from empty rather than inherited, so nothing this process was
// started with lands within a model's reach.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/skills"
	"github.com/alvaroibarguen/podium/internal/agent/store"
)

// TurnTokenEnv holds a host turn's delegation token in its runtime's environment, and its
// name is what the brief carries so the runtime can find it. Written by the conductor and
// read by agent/runtime/src/mcp.ts; an operator never sets it.
const TurnTokenEnv = "PODIUM_TURN_TOKEN"

// The environment a host turn's runtime is given to find its way back here. Both are
// literals rather than imports: the first is internal/runner.EnvEventsSock and the second is
// RunnerPathEnv in agent/runtime/src/emit.ts, and this package deliberately depends on
// neither the task-side CLI nor the runtime's source.
const (
	hostEventsSockEnv = "PODIUM_EVENTS_SOCK"
	hostRunnerPathEnv = "PODIUM_RUNNER_PATH"
)

// ErrCredentialStale means a provider has a credential stored but not in a form a host turn
// can spend. It is exported because the function that reads credentials lives in the api
// package, on the other side of HostRuntime.Credential, and the two answers need telling
// apart: nothing stored is an operator who has not finished setting up, and this is one who
// has and now has to save it again.
var ErrCredentialStale = errors.New("the stored credential predates host turns and cannot be read back")

// hostKindMessage is the one runner event kind a host turn reads. The others describe a
// container it does not have.
const hostKindMessage = "message"

// hostSockPathMax is the portable AF_UNIX sun_path budget, as internal/node/docker uses:
// 104 bytes on Darwin and 108 on Linux, less the NUL and some slack. A jail whose socket
// path would not fit is made in the OS temp directory instead.
const hostSockPathMax = 100

// hostGrace is how long a cancelled host turn has to report what it managed before it is
// killed. It matches the node's grace for a task, because it buys the same thing: the
// runtime forwards SIGTERM to the harness and still emits its final.
const hostGrace = 30 * time.Second

// hostMaxLine caps one line of the event protocol, as the node's reader does.
const hostMaxLine = 64 * 1024

// hostRecordGrace is how long a cancelled turn gets to record that it was cancelled, on a
// context of its own because the one it was cancelled with can no longer write anything.
const hostRecordGrace = 5 * time.Second

// hostTools is every tool a host turn may use.
//
// It is SHORT ON PURPOSE and it is not the playbook's list. A host turn has no container
// around it, so `bash` here is a shell on the conductor's own machine and `read` is a
// window onto ~/.podium — the master key, the credential row, the operator's home. It also
// has no workspace: a host turn clones nothing, so a filesystem tool has nothing legitimate
// to point at and every path it could reach belongs to somebody else.
//
// What is left is enough to hold a conversation — the transcript is in the brief, memory and
// the delegation tools arrive as MCP servers — and anything that needs to touch a disk
// becomes a delegated task, which is the whole design.
var hostTools = []string{"webfetch", "todoread", "todowrite"}

// Everything a host turn ever says about its own failures. None of them names a path, a
// binary or a provider: that is what the log is for.
const (
	hostFailedToStart = "Something went wrong on my side and I could not run this. An operator should check the logs."
	hostNoCredential  = "I have no model credential to answer with. An operator needs to set one on the Settings tab."
	hostSaidNothing   = "I ran this and it ended without saying anything at all. An operator should check the logs."
	// hostCredentialStale is the case worth telling apart from hostNoCredential: a
	// credential IS set, so "set one" sends an operator to look at a screen that already
	// says Connected. It happens to every credential stored before host turns existed —
	// those live only in Podium's secret store, which has no read endpoint, and a turn
	// running in this process has no node to resolve one for it. Saving it again writes
	// the copy this process can spend.
	hostCredentialStale = "The stored credential predates turns running on this host, so I cannot spend it. " +
		"An operator needs to save it again on the Settings tab. The value has not changed and only has to be re-entered."
)

// HostRuntime is what the conductor needs to run a turn itself. Nil in Options means every
// turn is a task, which is the only thing a conductor whose host has no runtime can do.
type HostRuntime struct {
	// Node is the node binary that runs Entry, "node" if empty.
	Node string
	// Entry is the built runtime's entrypoint on this host (agent/runtime/dist/main.js).
	Entry string
	// Runner is podium-runner on this host. The runtime invokes it once per message, and on
	// a host there is no node to bind-mount one in.
	Runner string
	// StateDir is where a turn's jail is made. Empty means the OS temp directory.
	StateDir string
	// MemoryMCPURL is the memory server as THIS HOST reaches it, which is not the URL a task
	// is given: a task is told host.docker.internal, which resolves in a container and
	// nowhere else.
	MemoryMCPURL string
	// MemoryAPIKey is the bearer for it. A task gets this as a Podium secret; a host turn
	// has no node to resolve one, so the value is handed to the child directly.
	MemoryAPIKey string
	// Credential answers with the bearer for a provider — the same one the task's secret
	// holds. Without it a host turn cannot start, because the runtime refuses a turn whose
	// provider key is unset.
	Credential func(ctx context.Context, provider string) (string, error)
	// TurnURL is the conductor's own address as a host turn reaches it — loopback, the same
	// listener the operator API is on, with TurnService mounted beside it. Empty means this
	// conductor offers no delegation: a host turn then answers with what it has and cannot
	// start a task, which is a working configuration and a poor one.
	TurnURL string
}

// validate reports what is missing, naming the environment variable that supplies it.
func (h *HostRuntime) validate() error {
	switch {
	case h.Entry == "":
		return errors.New("conductor: a host runtime needs its entrypoint (PODIUM_AGENT_HOST_RUNTIME)")
	case h.Runner == "":
		return errors.New("conductor: a host runtime needs podium-runner (PODIUM_AGENT_RUNNER_BIN)")
	case h.Credential == nil:
		return errors.New("conductor: a host runtime needs a credential source")
	}
	return nil
}

// node is the binary that runs the runtime.
func (h *HostRuntime) node() string {
	if h.Node == "" {
		return "node"
	}
	return h.Node
}

// fenceForHost is what makes a brief a HOST brief: the short tool list, no repositories, no
// browser, and memory reached the way this host reaches it. It is applied after
// Conductor.brief, so one function still builds every brief there is.
func (c *Conductor) fenceForHost(b *Brief, menu []DelegablePlaybook) {
	b.RunsOn = RunsOnHost
	b.Playbook.AllowedTools = append([]string(nil), hostTools...)
	b.Repos = nil
	b.Browser = nil
	// What it may delegate to. Taking the tools away above is only defensible because this
	// is here: the work a host turn cannot do itself is work it hands to a container.
	if c.host.TurnURL != "" && len(menu) > 0 {
		b.Delegation = &BriefDelegation{
			URL:       c.host.TurnURL,
			TokenEnv:  TurnTokenEnv,
			Playbooks: menu,
		}
	}
	if b.Memory == nil {
		return
	}
	if c.host.MemoryMCPURL == "" {
		// Better no memory than an address that only resolves inside a container: the
		// runtime fails a turn whose memory server it cannot reach.
		b.Memory = nil
		return
	}
	b.Memory = &BriefMemory{MCPURL: c.host.MemoryMCPURL, APIKeyEnv: b.Memory.APIKeyEnv}
}

// hostRun is one turn running on this host. The relay, the accounting and the bookkeeping
// are turnRun's — only the transport differs — so this holds one and adds a process.
type hostRun struct {
	r *turnRun
	// encoded is the brief, already base64'd, exactly as a task spec would carry it.
	encoded string
	bundles []skills.Bundle
	// provider is where this turn's requests go and which variable its credential lands in.
	provider *BriefProvider
	// memoryKeyEnv is the variable the memory server's bearer has to land in, taken from
	// the brief this turn actually carries rather than from the constant it was built from:
	// a brief that names one variable and an environment that sets another is a failed turn.
	memoryKeyEnv string
	// devEnv is extra environment the TEST-ONLY dev source asked for. Empty for every real
	// source, exactly as on a task spec.
	devEnv map[string]string
	// menu is the playbooks this turn may delegate to. It is both what the brief showed the
	// model and what the turn's token will accept, because they are the same list.
	menu []DelegablePlaybook
	// token is this turn's authority to delegate, minted on start and revoked when it ends.
	token string
}

// run starts the runtime, relays what it says, and records how it ended. Like turnRun.run it
// posts its own failures and returns nothing.
func (h *hostRun) run(ctx context.Context) {
	r := h.r
	c := r.c

	jail, err := hostJail(c.host.StateDir, r.turn.ID)
	if err != nil {
		c.logger.ErrorContext(ctx, "preparing a host turn's directory failed",
			"turn_id", r.turn.ID, "error", err)
		h.giveUp(ctx, hostFailedToStart)
		return
	}
	defer func() {
		if err := os.RemoveAll(jail.dir); err != nil {
			c.logger.WarnContext(ctx, "removing a host turn's directory failed",
				"turn_id", r.turn.ID, "dir", jail.dir, "error", err)
		}
	}()

	env, err := h.env(ctx, jail)
	if err != nil {
		// The reason names a provider and a missing credential, which is an operator's
		// business and not the chat's.
		c.logger.ErrorContext(ctx, "a host turn has no credential to spend",
			"turn_id", r.turn.ID, "provider", h.provider.ID, "error", err)
		said := hostNoCredential
		if errors.Is(err, ErrCredentialStale) {
			said = hostCredentialStale
		}
		h.giveUp(ctx, said)
		return
	}

	link, err := listenHost(jail.sock)
	if err != nil {
		c.logger.ErrorContext(ctx, "listening for a host turn's messages failed",
			"turn_id", r.turn.ID, "sock", jail.sock, "error", err)
		h.giveUp(ctx, hostFailedToStart)
		return
	}

	// A cancel of its own, so something outside the turn loop can stop this turn without
	// stopping the conductor: a chat deleted while its agent is still talking.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The turn's authority to delegate, for exactly as long as the turn. Revoked below
	// whatever happens: a token that outlived its turn would be a way to start tasks in a
	// conversation nobody is having.
	if h.token != "" {
		defer c.revokeTurnToken(h.token)
	}

	cmd := exec.CommandContext(runCtx, c.host.node(), c.host.Entry)
	cmd.Dir = jail.work
	cmd.Env = env
	// SIGTERM and not a kill: the runtime forwards it to the harness, which is what lets a
	// cancelled turn still say what it managed. WaitDelay is the backstop for a child that
	// ignores it.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = hostGrace
	// The harness's own diagnostics. They are the only trace a host turn leaves — there is
	// no task log to read afterwards — so they go to this process's log.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		link.close()
		c.logger.ErrorContext(ctx, "a host turn's stderr could not be piped",
			"turn_id", r.turn.ID, "error", err)
		h.giveUp(ctx, hostFailedToStart)
		return
	}

	// Registered BEFORE the fork, not after it. A cancel that arrives in between — a chat
	// deleted the moment after it was written to — would otherwise find nothing to stop and
	// leave a turn running that nobody is waiting for. Cancelling a CommandContext that has
	// not started yet makes Start fail, which is the outcome we want.
	c.registerHost(r.ref, cancel)
	defer c.unregisterHost(r.ref)

	if err := cmd.Start(); err != nil {
		link.close()
		c.logger.ErrorContext(ctx, "starting a host turn's runtime failed",
			"turn_id", r.turn.ID, "node", c.host.node(), "entry", c.host.Entry, "error", err)
		h.giveUp(ctx, hostFailedToStart)
		return
	}
	c.logger.InfoContext(ctx, "host turn started", "turn_id", r.turn.ID, "session_id", r.sess.ID,
		"playbook", r.playbook.Name, "pid", cmd.Process.Pid, "source", r.src.Kind())

	// stderr is drained to EOF before Wait, because Wait closes the pipe; and the link is
	// closed once the child is gone, because that is what ends the relay loop below.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		h.logStderr(ctx, stderr)
	}()
	waited := make(chan error, 1)
	go func() {
		<-drained
		err := cmd.Wait()
		link.close()
		waited <- err
	}()

	// The relay. Every message the runtime sent has been read by the time this loop ends:
	// the runtime awaits each `podium-runner message` before it exits, so the connections
	// are finished before the child is.
	for ev := range link.events {
		if ev.Kind != hostKindMessage {
			continue
		}
		if ev.Type == MsgAccounting {
			r.readAccounting(ctx, acctFromMessage, []byte(ev.Text))
			continue
		}
		r.deliver(ctx, ev.Type, ev.Text, ev.Attachments)
	}

	waitErr := <-waited
	cancelled := runCtx.Err() != nil
	if bad := link.unreadable(); len(bad) > 0 {
		c.logger.WarnContext(ctx, "a host turn's event socket carried lines this process could not read",
			"turn_id", r.turn.ID, "errors", bad)
	}

	// A cancelled turn still has to be written down, and the context it was cancelled with
	// cannot write anything: every store call and every post on it fails. A task turn can
	// leave the row alone because the recovery pass resumes it from its task; a HOST turn
	// has no task and died with this process, so if this does not record it, nothing does
	// until the next start finds an orphan.
	done := ctx
	if cancelled {
		var stop context.CancelFunc
		done, stop = context.WithTimeout(context.WithoutCancel(ctx), hostRecordGrace)
		defer stop()
	}

	r.flushProgress(done)

	status := hostOutcome(cmd.ProcessState.ExitCode(), cancelled)
	if status != store.TurnSucceeded {
		c.logger.WarnContext(done, "a host turn did not succeed", "turn_id", r.turn.ID,
			"exit_code", cmd.ProcessState.ExitCode(), "status", status, "error", waitErr)
	}
	// The runtime says its own piece on every failure it can report, and says "the turn
	// ended without an answer" when it has nothing else. Silence means not even that
	// arrived — and the placeholder above it reads "working on it", which is what somebody
	// would otherwise be left looking at for ever.
	if !r.said() {
		c.post(done, r.src, r.ref, Outbound{Type: OutFailure, Text: hostSaidNothing})
	}
	r.finish(done, status)
}

// giveUp posts one sentence and fails the turn. It is for the failures that happen before
// the runtime is running and can speak for itself.
func (h *hostRun) giveUp(ctx context.Context, say string) {
	h.r.c.post(ctx, h.r.src, h.r.ref, Outbound{Type: OutFailure, Text: say})
	h.r.finish(ctx, store.TurnFailed)
}

// env is the child's whole environment, built from empty.
//
// Inheriting this process's would hand a model the Podium API token, the agent database URL
// and everything else podium-agent was started with, in a turn that has no container around
// it. So the list below is the entire environment a host turn gets, and every entry in it is
// here for a stated reason.
func (h *hostRun) env(ctx context.Context, jail hostPaths) ([]string, error) {
	c := h.r.c
	key, err := c.host.Credential(ctx, h.provider.ID)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("no credential is stored for %s", h.provider.ID)
	}

	env := []string{
		// The harness is found on PATH, and so are node's own child processes.
		"PATH=" + os.Getenv("PATH"),
		// A HOME of its own: the runtime installs this playbook's Agent Skills under
		// $HOME/.config/opencode/skills, and with the operator's HOME that would write into
		// the operator's own harness configuration.
		"HOME=" + jail.home,
		"TMPDIR=" + jail.tmp,
		hostEventsSockEnv + "=" + jail.sock,
		hostRunnerPathEnv + "=" + c.host.Runner,
		BriefEnv + "=" + h.encoded,
		h.provider.APIKeyEnv + "=" + key,
	}
	if h.token != "" {
		// The token, and only the token: the address it is used against is in the brief,
		// where a document can carry it, and the token is not.
		env = append(env, TurnTokenEnv+"="+h.token)
	}
	if h.memoryKeyEnv != "" && c.host.MemoryAPIKey != "" {
		env = append(env, h.memoryKeyEnv+"="+c.host.MemoryAPIKey)
	}
	for _, b := range h.bundles {
		env = append(env, b.Env+"="+b.Encoded)
	}
	// Last, so a dry run can be driven without any of the above being reachable.
	for k, v := range h.devEnv {
		env = append(env, k+"="+v)
	}
	return env, nil
}

// logStderr copies the runtime's diagnostics into the conductor's log, one line at a time.
func (h *hostRun) logStderr(ctx context.Context, pipe io.Reader) {
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 0, 4096), hostMaxLine)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		h.r.c.logger.DebugContext(ctx, "host turn runtime", "turn_id", h.r.turn.ID, "line", line)
	}
}

// hostOutcome maps the runtime's exit onto a turn status. The runtime's codes are its
// external contract — 0 finished, 2 the brief was unusable, 3 out of turns, 4 the harness or
// the API failed — and it reports the last three in its own words as a final, so nothing
// here needs to distinguish them.
func hostOutcome(exitCode int, cancelled bool) string {
	switch {
	case cancelled:
		return store.TurnCancelled
	case exitCode == 0:
		return store.TurnSucceeded
	default:
		return store.TurnFailed
	}
}

// hostPaths is one turn's jail: a directory nothing else writes into, removed when the turn
// ends.
type hostPaths struct {
	dir  string
	home string
	work string
	tmp  string
	sock string
}

// hostJail makes the directories a host turn runs in.
//
// The socket's path is the constraint: AF_UNIX has room for about a hundred bytes, so a
// StateDir deep enough to overflow it falls back to the OS temp directory rather than
// failing a turn over a path length.
func hostJail(stateDir, turnID string) (hostPaths, error) {
	root := stateDir
	if root == "" {
		root = os.TempDir()
	}
	dir, err := os.MkdirTemp(root, "turn-"+turnID+"-")
	if err != nil {
		return hostPaths{}, err
	}
	if len(filepath.Join(dir, "events.sock")) > hostSockPathMax {
		_ = os.RemoveAll(dir)
		if dir, err = os.MkdirTemp("", "podium-turn-"); err != nil {
			return hostPaths{}, err
		}
	}
	p := hostPaths{
		dir:  dir,
		home: filepath.Join(dir, "home"),
		work: filepath.Join(dir, "work"),
		tmp:  filepath.Join(dir, "tmp"),
		sock: filepath.Join(dir, "events.sock"),
	}
	for _, d := range []string{p.home, p.work, p.tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return hostPaths{}, err
		}
	}
	return p, nil
}

// hostLink is the conductor's end of one host turn's event socket. It is the node's
// runnerLink with the parts a host does not have taken out: no artifacts to collect, no
// started or exited events to reconcile against a Docker API, and one turn per listener.
//
// It accepts more than one connection for the same reason the node's does: every
// `podium-runner message` is a new process dialling in.
type hostLink struct {
	ln     net.Listener
	events chan hostEvent
	conns  sync.WaitGroup
	once   sync.Once

	mu   sync.Mutex
	errs []error
}

// bad records a line that could not be read. They are reported once, when the turn ends: a
// reader goroutine has no context and no turn id, and a log line per malformed byte would
// bury the turn's own messages.
func (l *hostLink) bad(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.errs) < 8 {
		l.errs = append(l.errs, err)
	}
}

// unreadable is what the socket could not read, for the log at the end of the turn.
func (l *hostLink) unreadable() []error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.errs
}

// hostEvent is the fields of the runner's newline-JSON protocol that a host turn uses.
// Unknown kinds decode into it and are ignored.
type hostEvent struct {
	Kind        string   `json:"kind"`
	Type        string   `json:"type"`
	Text        string   `json:"text"`
	Attachments []string `json:"attachments"`
}

func listenHost(sock string) (*hostLink, error) {
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, err
	}
	l := &hostLink{ln: ln, events: make(chan hostEvent, 16)}
	go l.serve()
	return l, nil
}

func (l *hostLink) serve() {
	defer func() {
		l.conns.Wait()
		close(l.events)
	}()
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			return
		}
		l.conns.Add(1)
		go func() {
			defer l.conns.Done()
			defer func() { _ = conn.Close() }()
			sc := bufio.NewScanner(conn)
			sc.Buffer(make([]byte, 0, 4096), hostMaxLine)
			for sc.Scan() {
				var ev hostEvent
				if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
					// A line this process cannot read is a bug in the runner, not a failed
					// turn. The turn's own messages are what matter and they keep coming.
					l.bad(fmt.Errorf("undecodable event: %w", err))
					continue
				}
				l.events <- ev
			}
			// A line past hostMaxLine ends the scan silently, which would lose a message
			// and say nothing about it. podium-runner refuses one that long, so this means
			// something else is writing to the socket.
			if err := sc.Err(); err != nil {
				l.bad(err)
			}
		}()
	}
}

// close stops accepting. The events channel closes once the connections already open have
// been drained, which is what ends the relay loop.
func (l *hostLink) close() {
	l.once.Do(func() { _ = l.ln.Close() })
}

// registerHost records a host turn's cancel, so something outside the turn loop can stop it:
// a chat being deleted while its agent is still talking.
func (c *Conductor) registerHost(ref string, cancel func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hostRuns == nil {
		c.hostRuns = map[string]func(){}
	}
	c.hostRuns[ref] = cancel
}

func (c *Conductor) unregisterHost(ref string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.hostRuns, ref)
}

// CancelHostTurn stops the host turn answering ref and reports whether there was one. A task
// turn is cancelled through the control plane instead; this is the half of that story that
// never had a task to cancel.
func (c *Conductor) CancelHostTurn(ref string) bool {
	c.mu.Lock()
	cancel := c.hostRuns[ref]
	c.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}
