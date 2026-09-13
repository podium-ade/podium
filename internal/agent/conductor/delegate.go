package conductor

// Delegation: the task a host turn asks for when the work needs a container.
//
// A host turn runs in this process with no container around it and a deliberately short
// tool list (host.go). Everything it cannot do itself — a repository, a Docker daemon, a
// browser, anything that touches a disk — it delegates, and this is the half of that which
// lives in the conductor: mint a turn a token, take a playbook name and an instruction from
// it, start an ordinary Podium task, and then own that task.
//
// Owning it is the part worth reading. A turn is one exchange and a delegated task can run
// for hours, so the task outlives the turn that asked for it. Everything below is arranged
// so the CONVERSATION owns the work rather than the turn: the task's progress and its answer
// are relayed into the chat as they arrive, the row in the delegations table survives this
// process dying, and the recovery pass picks up every delegation that was still running.
// A turn that has already finished is not needed for any of that — it is only what asked.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// The reasons a delegation call is refused. The API layer maps them onto Connect codes and
// nothing else in this package interprets them.
var (
	// ErrNoTurn means the token names no live turn: it was never minted, or the turn it
	// belonged to has ended. Both are "you are not a turn I am talking to".
	ErrNoTurn = errors.New("conductor: no turn is running under that token")
	// ErrPlaybookNotOffered means the name is not one this turn's brief listed. A turn may
	// only delegate to the playbooks it was shown, so a name cannot be reached by guessing.
	ErrPlaybookNotOffered = errors.New("conductor: that playbook was not offered to this turn")
	// ErrDelegationNotFound covers both "no such delegation" and "not this conversation's",
	// which are the same answer on purpose: one turn may not learn about another
	// conversation's work by probing ids. In-flight work of THIS conversation is visible,
	// including from an earlier turn — a follow-up has to be able to inject into it.
	ErrDelegationNotFound = errors.New("conductor: no such delegation for this conversation")
	// ErrDelegationOver means the delegation is already terminal, so there is nothing to
	// cancel.
	ErrDelegationOver = errors.New("conductor: that delegation has already finished")
	// ErrNoDelegation means this conductor cannot delegate at all, because it has no host
	// runtime and therefore no host turns to delegate from.
	ErrNoDelegation = errors.New("conductor: this conductor runs no host turns, so nothing can delegate")
)

// turnTokenBytes is the size of a turn token. It is a bearer for one conversation, so it is
// a full-strength random string rather than an id: 32 bytes, hex, from crypto/rand.
const turnTokenBytes = 32

// turnGrant is what one turn token stands for: which turn, which conversation its work is
// announced in, and the exact set of playbooks that turn was offered.
//
// The playbook list is an ALLOW-LIST, not a hint. It is built from the same profile snapshot
// the turn's brief was built from, so what the model was shown and what it may ask for
// cannot drift apart between the fork and the call.
type turnGrant struct {
	turnID    string
	sessionID string
	ref       string
	src       Source
	playbooks []string
}

// mintTurnToken issues a token for one host turn. It lives until revokeTurnToken, which
// host.go calls when the turn ends — a turn's authority does not outlive the turn.
func (c *Conductor) mintTurnToken(g turnGrant) (string, error) {
	raw := make([]byte, turnTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("conductor: minting a turn token failed: %w", err)
	}
	token := hex.EncodeToString(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.turnTokens == nil {
		c.turnTokens = map[string]turnGrant{}
	}
	c.turnTokens[token] = g
	return token, nil
}

func (c *Conductor) revokeTurnToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.turnTokens, token)
}

func (c *Conductor) grantFor(token string) (turnGrant, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	g, ok := c.turnTokens[token]
	return g, ok
}

// DelegablePlaybooks is the menu a host turn is shown, and therefore the only playbooks it
// may delegate to.
//
// EVERY playbook in the profile is on it, including the one the conversation itself is
// running. An operator writing a playbook is the decision about what may run, and a second
// list here would only be a way for the two to disagree. Nor is the conversation's own
// playbook a loop: running it as a task is a container with a repository and a shell, which
// is exactly the thing a host turn cannot do itself and the reason delegation exists.
func (c *Conductor) DelegablePlaybooks() []DelegablePlaybook {
	profile := c.profiles.Current()
	out := make([]DelegablePlaybook, 0, len(profile.Playbooks))
	for _, name := range profile.PlaybookNames() {
		p := profile.Playbooks[name]
		out = append(out, DelegablePlaybook{
			Name:    name,
			Summary: playbookSummary(p),
			Docker:  p.Docker,
			Browser: p.Browser,
			Repos:   repoNames(p),
		})
	}
	return out
}

// DelegablePlaybook is one entry on that menu, as the brief carries it. It says what the
// playbook can reach rather than describing it: a model choosing between playbooks needs to
// know which one has a Docker daemon and which one has the repository checked out.
type DelegablePlaybook struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary,omitempty"`
	Docker  bool     `json:"docker,omitempty"`
	Browser bool     `json:"browser,omitempty"`
	Repos   []string `json:"repos,omitempty"`
}

// playbookSummary is the one line a menu entry carries. A playbook has no description field,
// so this is derived from what it actually is: the first line of its system prompt is what
// the operator wrote about the job, and it is the best short answer available.
func playbookSummary(p profiles.Playbook) string {
	return firstLine(p.SystemPrompt, maxSummaryRunes)
}

// maxSummaryRunes bounds a menu entry. A whole system prompt is kilobytes and the brief has
// a hard cap, so what goes in the menu is a sentence.
const maxSummaryRunes = 160

func repoNames(p profiles.Playbook) []string {
	if len(p.Repos) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.Repos))
	for _, r := range p.Repos {
		out = append(out, r.Name)
	}
	return out
}

// Delegate starts a task for the conversation the token belongs to and returns as soon as the
// control plane has accepted it. Nothing here waits for the task: it is followed in the
// background, and a turn asks GetDelegation for where it got to.
func (c *Conductor) Delegate(ctx context.Context, token, playbookName, instruction string) (store.Delegation, error) {
	g, ok := c.grantFor(token)
	if !ok {
		return store.Delegation{}, ErrNoTurn
	}
	if instruction == "" {
		return store.Delegation{}, errors.New("conductor: a delegation needs an instruction")
	}
	if !containsString(g.playbooks, playbookName) {
		return store.Delegation{}, fmt.Errorf("%w: %q; this turn was offered %v",
			ErrPlaybookNotOffered, playbookName, g.playbooks)
	}
	profile := c.profiles.Current()
	playbook, ok := profile.Playbooks[playbookName]
	if !ok {
		// The menu was built from a profile snapshot and this is a later one: a playbook
		// deleted in the web UI between the fork and the call.
		return store.Delegation{}, fmt.Errorf("%w: %q no longer exists", ErrPlaybookNotOffered, playbookName)
	}
	sess, err := c.store.GetSession(ctx, g.sessionID)
	if err != nil {
		return store.Delegation{}, fmt.Errorf("conductor: the delegating turn has no session: %w", err)
	}

	// The playbook's own backend, model and effort. A delegated task carries no override:
	// the turn that asked for it may itself be running on a model somebody picked in the
	// chat, and that choice is about the conversation, not about how a container should
	// build and test.
	//
	// Resolved ONCE, here, and used for the row and for the task: two resolutions a profile
	// reload apart would credit the spend to a model the task did not run on.
	choice := profile.Resolve(playbook, profiles.Override{})

	// The row first, and before the task: a task the control plane accepts must never be
	// one this database has never heard of, because the row is what makes it owned.
	dlg, err := c.store.CreateDelegation(ctx, store.NewDelegation{
		SessionID:   g.sessionID,
		TurnID:      g.turnID,
		TriggerRef:  g.ref,
		Playbook:    playbookName,
		Instruction: instruction,
		Backend: store.Backend{
			Agent:    choice.Agent,
			Model:    choice.Model,
			Effort:   choice.Effort,
			Provider: c.providerFor(choice.Agent).ID,
		},
	})
	if err != nil {
		return store.Delegation{}, err
	}

	task, err := c.startDelegatedTask(ctx, g, sess, playbook, dlg, choice)
	if err != nil {
		if ferr := c.store.FinishDelegation(ctx, dlg.ID, store.TurnFailed, "", nil, nil); ferr != nil {
			c.logger.ErrorContext(ctx, "recording a delegation that never started failed",
				"delegation_id", dlg.ID, "error", ferr)
		}
		return store.Delegation{}, err
	}
	dlg.TaskID = task.GetId()
	if err := c.store.SetDelegationTask(ctx, dlg.ID, dlg.TaskID); err != nil {
		// The task is running and the row does not know its id, which the recovery pass
		// would read as a delegation that never started. Say so loudly; the turn still gets
		// its answer through the live run below.
		c.logger.ErrorContext(ctx, "binding a delegation to its task failed",
			"delegation_id", dlg.ID, "task_id", dlg.TaskID, "error", err)
	}
	c.logger.InfoContext(ctx, "delegated a task", "delegation_id", dlg.ID, "turn_id", g.turnID,
		"session_id", g.sessionID, "playbook", playbookName, "task_id", dlg.TaskID)

	// HELD, not said. See holdAnnouncement.
	c.holdAnnouncement(announcement{
		dlgID:  dlg.ID,
		turnID: g.turnID,
		src:    g.src,
		ref:    g.ref,
		taskID: dlg.TaskID,
		text: fmt.Sprintf("Working on this in a `%s` task: %s",
			playbookName, firstLine(instruction, maxAnnouncedRunes)),
	})

	c.followDelegation(ctx, g.src, dlg, playbook, 0)
	return dlg, nil
}

// maxAnnouncedRunes bounds the echo of an instruction in the announcement. The instruction
// is a model's words and can be long; the announcement is a line or two in a conversation.
//
// Note what actually decides how much shows: firstLine stops at the first NEWLINE before
// this cap applies, so raising it only ever reveals more of the first line. A multi-paragraph
// instruction is bounded by its own first line whatever this says. It is 300 rather than a
// line's worth because the first line is usually the whole ask, and being cut mid-word two
// thirds of the way through it reads like something went wrong.
const maxAnnouncedRunes = 300

// announcement is a delegation the conversation has not been told about yet.
type announcement struct {
	dlgID  string
	turnID string
	src    Source
	ref    string
	taskID string
	text   string
}

// holdAnnouncement records that a task was started without saying so yet.
//
// It used to be said here, synchronously, inside the tool call — and it arrived BEFORE the
// assistant's own sentence about what it was doing, every time. The reason is in the
// harness: `opencode --format json` reports a tool only once it has COMPLETED (every
// tool_use event carries status `completed`; there is no pending one), so the runtime cannot
// release the text it is holding until after the tool has run. The text was in the runtime's
// hands before the tool ran and could not be published until after it. Observed as a 769ms
// inversion, which read as the container talking about work nobody had asked for yet.
//
// So the announcement waits for whichever comes first:
//
//   - the delegated task's own first word, because the announcement has to precede it — it
//     is what says which task is talking; or
//   - the end of the delegating turn, because by then the assistant has said its piece and
//     the announcement can only follow it.
//
// Held after the task exists, so the path where creation failed never holds one. A process
// that dies in between loses the line: the recovery pass resumes the delegation, and its own
// progress is what the conversation gets.
func (c *Conductor) holdAnnouncement(a announcement) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = append(c.pending, a)
}

// sayAnnouncement posts the held line for one delegation, if it is still held. Called before
// that task's first relayed word.
func (c *Conductor) sayAnnouncement(ctx context.Context, dlgID string) {
	c.sayHeld(ctx, func(a announcement) bool { return a.dlgID == dlgID })
}

// sayAnnouncementsFor posts whatever one turn started and never announced, in the order it
// started them. Called when that turn ends.
func (c *Conductor) sayAnnouncementsFor(ctx context.Context, turnID string) {
	c.sayHeld(ctx, func(a announcement) bool { return a.turnID == turnID })
}

// sayHeld takes the matching announcements out under the lock and posts them outside it: a
// post reaches a source, and holding the conductor's mutex across that would serialise every
// session behind one slow conversation.
func (c *Conductor) sayHeld(ctx context.Context, match func(announcement) bool) {
	c.mu.Lock()
	kept := c.pending[:0:0]
	var say []announcement
	for _, a := range c.pending {
		if match(a) {
			say = append(say, a)
			continue
		}
		kept = append(kept, a)
	}
	c.pending = kept
	c.mu.Unlock()

	for _, a := range say {
		c.post(ctx, a.src, a.ref, Outbound{Type: OutProgress, TaskID: a.taskID, Text: a.text})
	}
}

// startDelegatedTask builds the brief and creates the task. The brief is the ordinary one —
// the playbook's own image, tools, skills, secrets and repositories — with two deliberate
// differences: the instruction is what the turn asked for, and there is NO delegation block,
// so a delegated task cannot delegate again.
func (c *Conductor) startDelegatedTask(
	ctx context.Context, g turnGrant, sess store.Session, playbook profiles.Playbook,
	dlg store.Delegation, choice profiles.Choice,
) (*podiumv1.Task, error) {
	entries, err := g.src.FetchTranscript(ctx, g.ref)
	if err != nil {
		// The conversation is context, not the instruction. A task with none is a worse
		// task and a much better outcome than no task at all.
		c.logger.WarnContext(ctx, "reading the conversation for a delegated task failed; running it without history",
			"delegation_id", dlg.ID, "ref", g.ref, "error", err)
	}
	j := playbookJob(playbook)
	bundles, err := c.skillBundles(ctx, j)
	if err != nil {
		return nil, fmt.Errorf("conductor: the %s playbook's skills could not be prepared: %w", playbook.Name, err)
	}
	ev := InboundEvent{
		SourceKind: sess.SourceKind,
		SourceKey:  sess.SourceKey,
		Ref:        g.ref,
		Text:       dlg.Instruction,
		TS:         time.Now().UTC(),
		BriefKind:  briefKindFor(sess.SourceKind),
		Playbook:   playbook.Name,
	}
	// The DELEGATION's id as the brief's turn id: a delegated task is its own unit of work,
	// and this is what makes a task's own logs and its turn.json traceable back to the row
	// that owns it rather than to the turn that happened to ask.
	// The delegated playbook's own MCP servers, resolved the same way a turn's are. The
	// host turn that asked for this has none of its own — it delegates to a playbook that
	// has what it needs, which is exactly what this line is.
	servers, err := c.mcpServers(ctx, j)
	if err != nil {
		return nil, fmt.Errorf("conductor: the delegated task's mcp servers: %w", err)
	}
	brief := c.brief(sess, j, dlg.ID, ev, entries, bundles, servers, choice)
	encoded, err := brief.Encode()
	if err != nil {
		return nil, fmt.Errorf("conductor: the delegated task's brief does not fit: %w", err)
	}
	// Keyed by the delegation id, which is what this task's brief carries as its turn id
	// and therefore what Mint checks the status of.
	capabilitySecret, err := c.provisionGitCapability(ctx, dlg.ID, playbook)
	if err != nil {
		return nil, err
	}
	task, err := c.podium.CreateTask(ctx,
		c.taskSpec(g.src, playbook, encoded, ev, bundles, servers, choice, capabilitySecret),
		int32(playbook.Priority))
	if err != nil {
		if capabilitySecret != "" {
			c.dropGitCapability(ctx, dlg.ID)
		}
		return nil, fmt.Errorf("conductor: creating the delegated task failed: %w", err)
	}
	return task, nil
}

// followDelegation starts the goroutine that relays a delegated task and records how it
// ended. It is used both by Delegate and by the recovery pass on start.
func (c *Conductor) followDelegation(
	_ context.Context, src Source, dlg store.Delegation, playbook profiles.Playbook, lastSeq uint64,
) {
	run := &delegationRun{
		sink: &sink{
			c:   c,
			src: src,
			ref: dlg.TriggerRef,
			// No placeholder: the conversation has nothing of this task's on screen to
			// replace, so its progress arrives as its own messages.
			taskID: dlg.TaskID,
		},
		dlg:      dlg,
		playbook: playbook,
		lastSeq:  lastSeq,
	}
	c.mu.Lock()
	if c.liveDelegations == nil {
		c.liveDelegations = map[string]*delegationRun{}
	}
	c.liveDelegations[dlg.ID] = run
	c.mu.Unlock()

	// The conductor's own context, not the caller's: Delegate is an RPC and its context is
	// cancelled as soon as the response is written, whereas the task being followed here
	// runs for as long as it runs. See Conductor.background.
	runCtx := c.background()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() {
			c.mu.Lock()
			delete(c.liveDelegations, dlg.ID)
			c.mu.Unlock()
		}()
		run.run(runCtx)
	}()
}

// GetDelegation is where a delegated task has got to, and the last thing it said on the way.
// A turn may ask about work of this conversation, including a task an earlier turn started
// that is still running — that is how a follow-up injects into it.
func (c *Conductor) GetDelegation(ctx context.Context, token, id string) (store.Delegation, string, error) {
	g, ok := c.grantFor(token)
	if !ok {
		return store.Delegation{}, "", ErrNoTurn
	}
	dlg, err := c.store.GetDelegation(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.Delegation{}, "", ErrDelegationNotFound
	case err != nil:
		return store.Delegation{}, "", err
	case dlg.TriggerRef != g.ref:
		// Deliberately the same answer as "no such delegation": one conversation does not
		// get to discover another's work by probing ids.
		return store.Delegation{}, "", ErrDelegationNotFound
	}
	return dlg, c.delegationProgress(id), nil
}

// Delegations is this conversation's delegated tasks: everything this turn has started,
// plus any still running from an earlier turn of the same conversation, oldest first.
func (c *Conductor) Delegations(ctx context.Context, token string) ([]store.Delegation, error) {
	g, ok := c.grantFor(token)
	if !ok {
		return nil, ErrNoTurn
	}
	mine, err := c.store.DelegationsForTurn(ctx, g.turnID)
	if err != nil {
		return nil, err
	}
	running, err := c.store.RunningDelegationsForRef(ctx, g.ref)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(mine))
	out := make([]store.Delegation, 0, len(mine)+len(running))
	for _, dlg := range mine {
		seen[dlg.ID] = struct{}{}
		out = append(out, dlg)
	}
	for _, dlg := range running {
		if _, ok := seen[dlg.ID]; ok {
			continue
		}
		out = append(out, dlg)
	}
	return out, nil
}

// InjectDelegation delivers one human message into a running delegated task of this
// conversation. The container stays up.
func (c *Conductor) InjectDelegation(ctx context.Context, token, id, text string) (store.Delegation, error) {
	dlg, _, err := c.GetDelegation(ctx, token, id)
	if err != nil {
		return store.Delegation{}, err
	}
	if dlg.Status != store.TurnRunning || dlg.TaskID == "" {
		return store.Delegation{}, ErrDelegationOver
	}
	if err := c.podium.InjectTask(ctx, dlg.TaskID, text); err != nil {
		return store.Delegation{}, fmt.Errorf("conductor: injecting into the delegated task failed: %w", err)
	}
	return dlg, nil
}

// CancelDelegation stops a delegated task. It is the same cancel `podium task cancel`
// performs, so the node sends the same SIGTERM and honours the same grace, and the run
// following it records the outcome as it would for any other ending.
func (c *Conductor) CancelDelegation(ctx context.Context, token, id, reason string) (store.Delegation, error) {
	dlg, _, err := c.GetDelegation(ctx, token, id)
	if err != nil {
		return store.Delegation{}, err
	}
	if dlg.Status != store.TurnRunning {
		return store.Delegation{}, ErrDelegationOver
	}
	if dlg.TaskID == "" {
		return store.Delegation{}, ErrDelegationOver
	}
	if reason == "" {
		reason = "the turn that delegated this cancelled it"
	}
	if err := c.podium.CancelTask(ctx, dlg.TaskID, reason); err != nil {
		return store.Delegation{}, fmt.Errorf("conductor: cancelling the delegated task failed: %w", err)
	}
	return dlg, nil
}

// delegationProgress is the last thing a live delegated task said on its way to an answer,
// or "" for one that is not running in this process.
func (c *Conductor) delegationProgress(id string) string {
	c.mu.Lock()
	run := c.liveDelegations[id]
	c.mu.Unlock()
	if run == nil {
		return ""
	}
	return run.progressText()
}

// delegationRun is one delegated task being followed. It is the turnRun of a thing that is
// not a turn: the same relay, the same artifact resolution and the same classification, with
// the delegations table where the turns table would be and no reactions, no accounting and
// no memory retain — none of which belong to a task somebody's turn asked for.
type delegationRun struct {
	*sink
	dlg      store.Delegation
	playbook profiles.Playbook
	lastSeq  uint64

	mu       sync.Mutex
	progress string
	// acct is what the task said it cost, nil until it says so. Unlike a turn's there is no
	// turn.json fallback: settle has no artifact to read it from, so the message is the only
	// route and a task that never sends one is recorded as unpriced.
	acct *accounting
}

// readAccounting records what a delegated task said it cost. A document that does not parse
// is a bug in the runtime, not a failed task, so it is logged and dropped.
func (r *delegationRun) readAccounting(ctx context.Context, raw []byte) {
	var a accounting
	if err := json.Unmarshal(raw, &a); err != nil {
		r.c.logger.WarnContext(ctx, "a delegated task's accounting does not parse",
			"delegation_id", r.dlg.ID, "task_id", r.dlg.TaskID, "error", err)
		return
	}
	r.acct = &a
}

func (r *delegationRun) run(ctx context.Context) {
	c := r.c
	f := &follower{
		tasks:       c.podium.Tasks,
		taskID:      r.dlg.TaskID,
		lastSeq:     r.lastSeq,
		onEvent:     r.onEvent,
		onReconnect: c.metrics.FollowReconnects.Inc,
	}
	followErr := f.follow(ctx)
	r.flushProgress(ctx)
	if ctx.Err() != nil {
		// A shutdown mid-task. The row stays running and the recovery pass picks it up on
		// the next start — which is the whole reason the row exists.
		c.logger.InfoContext(ctx, "stopping while a delegated task is in flight; it will be resumed",
			"delegation_id", r.dlg.ID, "task_id", r.dlg.TaskID)
		return
	}
	if followErr != nil {
		c.logger.WarnContext(ctx, "following a delegated task ended early",
			"delegation_id", r.dlg.ID, "task_id", r.dlg.TaskID, "error", followErr)
		// Same rule as a turn's task: one that keeps running with nobody listening holds a
		// node slot, spends money, and can finish by opening a pull request that was never
		// announced to the person who asked for it.
		if err := c.podium.CancelTask(ctx, r.dlg.TaskID, fmt.Sprintf(
			"the conductor stopped following this delegated task: %v", followErr)); err != nil {
			c.logger.ErrorContext(ctx, "cancelling an abandoned delegated task failed",
				"delegation_id", r.dlg.ID, "task_id", r.dlg.TaskID, "error", err)
		}
	}

	task, err := getTaskWithRetry(ctx, c.podium.Tasks, r.dlg.TaskID, terminalStatusBudget)
	if err != nil {
		c.logger.ErrorContext(ctx, "reading a delegated task back failed",
			"delegation_id", r.dlg.ID, "task_id", r.dlg.TaskID, "error", err)
		c.post(ctx, r.src, r.ref, Outbound{Type: OutFailure, TaskID: r.dlg.TaskID, Text: fmt.Sprintf(
			"I lost track of the `%s` task I started for this. Task `%s`.", r.dlg.Playbook, r.dlg.TaskID)})
		r.finish(ctx, store.TurnFailed)
		return
	}

	r.settle(ctx, task)
	result := classify(task, r.playbook.Timeout.Std())
	if result.Post != "" {
		c.logger.WarnContext(ctx, "a delegated task did not succeed", "delegation_id", r.dlg.ID,
			"task_id", task.GetId(), "status", task.GetStatus().String(),
			"exit_code", task.GetExitCode(), "failure_reason", task.GetFailureReason())
		c.post(ctx, r.src, r.ref, Outbound{Type: OutFailure, TaskID: r.dlg.TaskID, Text: result.Post})
	}
	r.finish(ctx, result.Status)
}

// onEvent is the relay, and it reads the accounting rather than dropping it.
//
// It used to drop it, on the grounds that a delegated task's cost belonged to no turn row.
// That was true and it was the wrong conclusion: a conversation is answered by the assistant
// and the container does the work, so a delegation IS the unit of spend for everything a
// chat asks for. The cost went to a Debug log line — which is off — and from there nowhere.
// The row carries it now, and the usage screen reads it beside a turn's.
func (r *delegationRun) onEvent(ctx context.Context, e *podiumv1.TaskEvent) {
	if e.GetKind() != podiumv1.TaskEventKind_TASK_EVENT_KIND_MESSAGE {
		return
	}
	msg := e.GetMessage()
	if msg == nil {
		return
	}
	if msg.GetType() == MsgAccounting {
		r.readAccounting(ctx, []byte(msg.GetText()))
		return
	}
	// Before the task's first word, so "Working on this in a `podium` task" cannot arrive
	// after the task has already started talking about it.
	r.c.sayAnnouncement(ctx, r.dlg.ID)

	// The same rule the sink applies: anything that is not a final is progress. It is
	// recorded here as well as relayed, because a turn polling GetDelegation wants to see
	// movement rather than only "running".
	if msg.GetType() != OutFinal && msg.GetType() != OutQuestion && msg.GetType() != MsgQuestion {
		r.setProgress(msg.GetText())
	}
	r.relay(ctx, e)
	if msg.GetType() == MsgQuestion || msg.GetType() == OutQuestion {
		r.c.setAwaiting(r.dlg.SessionID, r.dlg.TaskID)
		if err := r.src.React(ctx, r.ref, ReactionAwaiting); err != nil {
			r.c.logger.WarnContext(ctx, "marking a delegated task as awaiting a reply failed",
				"delegation_id", r.dlg.ID, "error", err)
		}
		return
	}
	if r.c.clearAwaiting(r.dlg.SessionID, r.dlg.TaskID) {
		if err := r.src.React(ctx, r.ref, ReactionWorking); err != nil {
			r.c.logger.WarnContext(ctx, "marking a delegated task as working again failed",
				"delegation_id", r.dlg.ID, "error", err)
		}
	}
}

// settle resolves the files the task's answer asked for. Unlike a turn's settle there is no
// turn.json to fall back on and no chat title to apply: neither is a delegated task's job.
func (r *delegationRun) settle(ctx context.Context, task *podiumv1.Task) {
	if len(r.attachments) == 0 {
		return
	}
	list, err := r.c.podium.ListArtifacts(ctx, task.GetId())
	if err != nil {
		r.c.logger.WarnContext(ctx, "listing a delegated task's artifacts failed",
			"delegation_id", r.dlg.ID, "task_id", task.GetId(), "error", err)
		return
	}
	byName := make(map[string]*podiumv1.Artifact, len(list))
	for _, a := range list {
		byName[a.GetName()] = a
	}
	r.attachAll(ctx, byName)
}

func (r *delegationRun) finish(ctx context.Context, status string) {
	r.c.clearAwaiting(r.dlg.SessionID, r.dlg.TaskID)
	answer := r.answer()
	var numTurns *int
	var cost *float64
	if r.acct != nil {
		numTurns, cost = r.acct.NumTurns, r.acct.TotalCostUSD
	} else if status == store.TurnSucceeded {
		// The same hole a turn reports, for the same reason: a task that ran and spent money
		// and recorded neither is money the usage screen cannot show.
		r.c.logger.WarnContext(ctx, "a delegated task succeeded but reported no accounting, "+
			"so its num_turns and cost_usd are unrecorded",
			"delegation_id", r.dlg.ID, "task_id", r.dlg.TaskID, "playbook", r.dlg.Playbook)
		r.c.metrics.TurnsWithoutAccounting.Inc()
	}
	// As turnRun.finish does, and for the same reason.
	r.c.dropGitCapability(ctx, r.dlg.ID)
	if err := r.c.store.FinishDelegation(ctx, r.dlg.ID, status, answer, numTurns, cost); err != nil {
		r.c.logger.ErrorContext(ctx, "recording how a delegated task ended failed",
			"delegation_id", r.dlg.ID, "status", status, "error", err)
	}
	// The pull requests THIS answer named. A delegated task is where a pull request now
	// comes from — the assistant has no repository to open one from — so a conversation
	// whose only link was read off the turn's own final never carried one at all.
	r.c.linkPullRequests(ctx, r.src, r.ref, answer,
		"delegation_id", r.dlg.ID, "task_id", r.dlg.TaskID)
	r.c.metrics.Delegations.WithLabelValues(r.dlg.Playbook, status).Inc()
	r.c.logger.InfoContext(ctx, "a delegated task finished", "delegation_id", r.dlg.ID,
		"task_id", r.dlg.TaskID, "playbook", r.dlg.Playbook, "status", status)
}

func (r *delegationRun) setProgress(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = text
}

func (r *delegationRun) progressText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.progress
}

// recoverDelegations resumes following every delegated task that was in flight when this
// process stopped. A delegation's task runs on a NODE and does not care that the conductor
// died, so the alternative to this is a container finishing work nobody is listening for.
func (c *Conductor) recoverDelegations(ctx context.Context) {
	running, err := c.store.RunningDelegations(ctx)
	if err != nil {
		c.logger.ErrorContext(ctx, "reading the delegations that were in flight failed", "error", err)
		return
	}
	profile := c.profiles.Current()
	for _, dlg := range running {
		sess, err := c.store.GetSession(ctx, dlg.SessionID)
		if err != nil {
			c.logger.ErrorContext(ctx, "a running delegation has no session",
				"delegation_id", dlg.ID, "error", err)
			continue
		}
		src := c.sourceOf(sess.SourceKind)
		if dlg.TaskID == "" {
			// Recorded, and its task never was. Nothing is running.
			c.logger.WarnContext(ctx, "a delegation was recorded but its task never was; failing it",
				"delegation_id", dlg.ID)
			if err := c.store.FinishDelegation(ctx, dlg.ID, store.TurnFailed, "", nil, nil); err != nil {
				c.logger.ErrorContext(ctx, "failing an orphaned delegation failed",
					"delegation_id", dlg.ID, "error", err)
			}
			continue
		}
		if src == nil {
			c.logger.WarnContext(ctx, "a running delegation's source is not configured; leaving it alone",
				"delegation_id", dlg.ID, "source_kind", sess.SourceKind)
			continue
		}
		lastSeq, err := c.store.MaxRelayedSeq(ctx, dlg.TaskID)
		if err != nil {
			c.logger.ErrorContext(ctx, "reading the relay high-water mark of a delegated task failed",
				"delegation_id", dlg.ID, "task_id", dlg.TaskID, "error", err)
			continue
		}
		c.logger.InfoContext(ctx, "resuming a delegated task that was in flight",
			"delegation_id", dlg.ID, "task_id", dlg.TaskID, "from_seq", lastSeq)
		c.followDelegation(ctx, src, dlg, profile.Playbooks[dlg.Playbook], lastSeq)
	}
}

// CancelDelegationsForRef stops every delegated task of one conversation and reports how
// many it asked to stop. It is what deleting a chat has to do: the chat is the owner, so
// deleting it ends the work it owns.
func (c *Conductor) CancelDelegationsForRef(ctx context.Context, ref, reason string) (int, error) {
	running, err := c.store.RunningDelegationsForRef(ctx, ref)
	if err != nil {
		return 0, err
	}
	stopped := 0
	for _, dlg := range running {
		if dlg.TaskID == "" {
			continue
		}
		if err := c.podium.CancelTask(ctx, dlg.TaskID, reason); err != nil {
			c.logger.WarnContext(ctx, "cancelling a delegated task of a deleted conversation failed",
				"delegation_id", dlg.ID, "task_id", dlg.TaskID, "error", err)
			continue
		}
		stopped++
	}
	return stopped, nil
}

func containsString(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}
