package conductor

// The assistant, wherever it is placed.
//
// It used to have one home: a child of this process (host.go). It now has the same choice a
// playbook has — the fleet's own nodes, or a sandbox provider — plus that one, and which it
// gets is the assistant.runs_on setting. What it IS does not change with where it runs: the
// same brief, the same fixed tool list, the same delegation menu, no workspace. Only the
// transport differs, and that is the whole of this file.
//
// The conductor knows exactly one distinction: host or task. Every placement that is not
// the host is a task with a runs_on, and which provider runs it — Modal, boat, whatever comes
// next — is the node's business, behind internal/sandbox.Provider. Nothing here names one.
//
// An assistant task is NOT a playbook task with things taken away. It has no repositories, no
// session workspace (so nothing is restored and nothing is snapshotted: the transcript is its
// continuity), no Docker daemon and no browser, because the assistant has none of those
// anywhere. It does stay warm after it answers, so the next message of the same conversation
// is injected into it rather than paying a cold start.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/mcp"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/skills"
	"github.com/podium-ade/podium/internal/agent/store"
	"github.com/podium-ade/podium/pkg/spec"
)

// assistantTaskGrace is added to the assistant's wall clock to make its task's timeout. The
// runtime enforces the assistant's own timeout from inside; the task's is the backstop for a
// runtime that does not, and it must not cut short the warm window or the final's upload.
const assistantTaskGrace = 2 * time.Minute

// TurnTokenSecret is the per-turn secret an assistant task's delegation token travels in.
func TurnTokenSecret(turnID string) string {
	return profiles.TurnTokenPrefix + turnID
}

// assistantPlacement is where the assistant runs for a turn starting now: profiles.RunsOnHost,
// "nodes" or a sandbox provider's name. Empty means this conductor has no assistant at all — no
// host runtime and nowhere else configured — which is what every conductor without a host
// runtime was before the assistant could be placed.
//
// The setting wins over the option, which is only the install's default. A setting that names
// the host on a conductor with no host runtime cannot be honoured and falls back to the default
// rather than refusing every conversation, and says so.
func (c *Conductor) assistantPlacement(ctx context.Context) string {
	if c.store != nil {
		var row profiles.RunsOn
		err := c.store.GetSetting(ctx, profiles.AssistantRunsOnKey, &row)
		switch {
		case err != nil && !errors.Is(err, store.ErrNotFound):
			c.logger.WarnContext(ctx, "reading where the assistant runs failed; using the default", "error", err)
		case row.RunsOn == profiles.RunsOnHost && c.host == nil:
			c.logger.WarnContext(ctx, "the assistant is set to run on this host, which has no "+
				"host runtime; using the default")
		case row.RunsOn != "":
			return row.RunsOn
		}
	}
	if c.assistantRunsOn != "" {
		return c.assistantRunsOn
	}
	if c.host != nil {
		return profiles.RunsOnHost
	}
	return ""
}

// hasAssistant reports whether a conversation can be answered here at all.
func (c *Conductor) hasAssistant(ctx context.Context) bool {
	return c.assistantPlacement(ctx) != ""
}

// turnSurfaceURL is this conductor as a turn running on runsOn reaches it: where it delegates,
// reads its pull request and redeems a git capability. Each placement has a different vantage
// point, which is why there is more than one address:
//
//   - the host: loopback, because the turn is a child of this process and its token is not
//     something to send over a network;
//   - the fleet's nodes: GitTaskURL (PODIUM_AGENT_TASK_URL), which resolves from a container
//     on a Docker host;
//   - a sandbox provider: SandboxTaskURL (PODIUM_AGENT_SANDBOX_URL), because a cloud sandbox
//     is on nobody's Docker network and host.docker.internal resolves to nothing there. Unset,
//     it is GitTaskURL, which is right for a provider inside the operator's own network.
//
// Empty means a turn there has no way back here.
func (c *Conductor) turnSurfaceURL(runsOn string) string {
	switch runsOn {
	case profiles.RunsOnHost:
		if c.host == nil {
			return ""
		}
		return c.host.TurnURL
	case "", profiles.RunsOnNodes:
		return c.gitTaskURL
	default:
		if c.sandboxTaskURL != "" {
			return c.sandboxTaskURL
		}
		return c.gitTaskURL
	}
}

// placedRunsOn is where this job's turn runs, as turnSurfaceURL needs it: the assistant's own
// placement, or the playbook's runs_on resolved against the conductor's default.
func (c *Conductor) placedRunsOn(ctx context.Context, j job) string {
	switch {
	case j.onHost:
		return profiles.RunsOnHost
	case j.assistant:
		return j.runsOn
	default:
		return c.placed(ctx, j.playbook).RunsOn
	}
}

// assistantBrief is what makes a brief an ASSISTANT's: the delegation menu, what this
// conversation already has running, and the pull request's own MCP server. A host turn gets
// hostBrief, which adds what only a child of this process needs; a task gets this alone.
//
// It never sets RunsOn: a task's brief leaves it absent, which is what tells the runtime the
// turn is in a container with a /workspace and an inbox. Assistant is what tells it the turn
// is the assistant, so the prompt is the assistant's and not a playbook's.
func (c *Conductor) assistantBrief(
	b *Brief, runsOn string, menu []DelegablePlaybook, running []BriefRunningDelegation, previews []BriefPreview,
) {
	b.Assistant = true
	surface := c.turnSurfaceURL(runsOn)
	if surface != "" && len(menu) > 0 {
		b.Delegation = &BriefDelegation{
			URL:       surface,
			TokenEnv:  TurnTokenEnv,
			Playbooks: menu,
			Running:   running,
			Previews:  previews,
		}
	}
	if b.Delegation != nil && b.Source.Kind == SourceGitHub && surface != "" {
		b.Playbook.MCPServers = append(b.Playbook.MCPServers, BriefMCPServer{
			Name: PullRequestMCPName, URL: strings.TrimSuffix(surface, "/") + pullRequestMCPPath, TokenEnv: TurnTokenEnv,
		})
	}
}

// pullRequestMCPPath is ghreview.MCPPath. A literal, because this package does not import the
// GitHub source it serves; agent.go mounts the handler on the same path, and a test holds
// the two together.
const pullRequestMCPPath = "/mcp/pull-request"

// assistantWarmFor is how long an assistant task waits for its conversation's next message.
func (c *Conductor) assistantWarmFor() time.Duration {
	switch {
	case c.assistantWarm < 0:
		return 0
	case c.assistantWarm == 0:
		return DefaultWorkspaceWarm
	default:
		return c.assistantWarm
	}
}

// assistantTaskSpec is the task an assistant turn placed off this host runs in. It is built by
// taskSpec, from a playbook that describes exactly what the assistant has and nothing more: an
// image, a wall clock and a placement. Everything else a playbook could bring — repositories,
// sidecars, secrets of its own, exposed ports — is absent because the assistant has none, so
// there is nothing here to strip.
func (c *Conductor) assistantTaskSpec(
	src Source, j job, encoded string, ev InboundEvent, bundles []skills.Bundle,
	servers []mcp.Server, choice profiles.Choice, tokenSecret, login string,
) *spec.TaskSpec {
	shape := profiles.Playbook{
		Image:   c.assistantImage,
		Timeout: spec.Duration(j.assistantTimeout() + assistantTaskGrace),
		RunsOn:  j.runsOn,
	}
	if shape.Image == "" {
		shape.Image = profiles.DefaultRuntimeImage()
	}
	s := c.taskSpec(src, shape, encoded, ev, bundles, servers, choice, "", login)
	if tokenSecret != "" {
		s.Secrets = append(s.Secrets, spec.SecretRef{
			Name:   tokenSecret,
			Target: spec.SecretTargetEnv,
			Key:    TurnTokenEnv,
		})
	}
	// No WorkspaceSession: the assistant has no workspace to restore or keep, so the most
	// frequent task in the system never touches a snapshot. The warm window is on its own.
	if warm := c.assistantWarmFor(); warm > 0 {
		s.WorkspaceWarm = spec.Duration(warm)
	}
	return s
}

// runAssistantTask runs one assistant turn as a task on j.runsOn and follows it to the end.
// The relay, the accounting, the warm window and the injection of a follow-up are the task
// path's own — turnRun — so this only builds the task and the authority it carries.
func (c *Conductor) runAssistantTask(
	ctx context.Context, run *turnRun, brief *Brief, menu []DelegablePlaybook,
	ev InboundEvent, bundles []skills.Bundle, servers []mcp.Server, choice profiles.Choice,
	login, speaker string,
) {
	src, sess, j, turn := run.src, run.sess, run.job, run.turn

	// The token, before the brief is encoded: a brief that offers delegation to a turn that
	// cannot authenticate is a turn whose every delegation fails.
	var token, tokenSecret string
	if brief.Delegation != nil {
		var err error
		token, err = c.mintTurnToken(turnGrant{
			turnID:    turn.ID,
			sessionID: sess.ID,
			ref:       ev.Ref,
			src:       src,
			playbooks: playbookNames(menu),
			login:     speaker,
		})
		if err == nil {
			// A secret and not an environment entry: a spec's env is stored with the task
			// and the token is not something anything that can read a task should read.
			tokenSecret = TurnTokenSecret(turn.ID)
			if _, err = c.podium.SetSecret(ctx, tokenSecret, []byte(token)); err != nil {
				c.revokeTurnToken(token)
				token, tokenSecret = "", ""
			}
		}
		if err != nil {
			c.logger.ErrorContext(ctx, "preparing an assistant task's delegation token failed; "+
				"it will run without being able to delegate", "turn_id", turn.ID, "error", err)
			brief.Delegation = nil
			brief.Playbook.MCPServers = withoutServer(brief.Playbook.MCPServers, PullRequestMCPName)
		}
	}
	// Whatever happens below, the authority ends with the turn. On a context of its own,
	// because the turn's may be the one that was cancelled.
	defer func() {
		if token == "" {
			return
		}
		c.revokeTurnToken(token)
		done, stop := context.WithTimeout(context.WithoutCancel(ctx), hostRecordGrace)
		defer stop()
		if err := c.podium.DeleteSecret(done, tokenSecret); err != nil {
			c.logger.WarnContext(done, "deleting an assistant task's token secret failed; the "+
				"token itself is already revoked", "turn_id", turn.ID, "secret", tokenSecret, "error", err)
		}
	}()

	encoded, err := brief.Encode()
	if err != nil {
		c.logger.WarnContext(ctx, "the turn brief does not fit", "turn_id", turn.ID, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: "This conversation is too large for me to take in at once. Start a new thread with just the question."})
		c.failTurn(ctx, src, sess, j, turn, ev.Ref, run.startedAt, store.TurnFailed)
		return
	}

	taskSpec := c.assistantTaskSpec(src, j, encoded, ev, bundles, servers, choice, tokenSecret, login)
	task, err := c.podium.CreateTask(ctx, taskSpec, 0)
	if err != nil {
		c.logger.WarnContext(ctx, "creating the assistant's task failed",
			"turn_id", turn.ID, "runs_on", j.runsOn, "image", taskSpec.Image, "error", err)
		c.post(ctx, src, ev.Ref, Outbound{Type: OutFailure, Text: hostFailedToStart})
		c.failTurn(ctx, src, sess, j, turn, ev.Ref, run.startedAt, store.TurnFailed)
		return
	}
	if err := c.store.SetTurnTask(ctx, turn.ID, task.GetId()); err != nil {
		c.logger.ErrorContext(ctx, "binding the turn to its task failed",
			"turn_id", turn.ID, "task_id", task.GetId(), "error", err)
	}
	turn.TaskID = task.GetId()
	run.turn = turn
	run.taskID = task.GetId()
	run.warm = taskSpec.WorkspaceWarm > 0
	c.logger.InfoContext(ctx, "assistant turn started", "turn_id", turn.ID, "session_id", sess.ID,
		"runs_on", cmpRunsOn(j.runsOn), "task_id", task.GetId(), "source", src.Kind())

	run.run(ctx)
	c.clearWarm(sess.ID, task.GetId())
	// Whatever this turn started and the conversation has not been told about, after the
	// turn's own words, exactly as a host turn does.
	c.sayAnnouncementsFor(context.WithoutCancel(ctx), turn.ID)
}

// cmpRunsOn names a placement for a log line: the empty one is the fleet's own nodes.
func cmpRunsOn(runsOn string) string {
	if runsOn == "" {
		return profiles.RunsOnNodes
	}
	return runsOn
}

// withoutServer is servers minus the one named name.
func withoutServer(servers []BriefMCPServer, name string) []BriefMCPServer {
	out := servers[:0:0]
	for _, s := range servers {
		if s.Name != name {
			out = append(out, s)
		}
	}
	return out
}

// AssistantPlacement is where the assistant runs for a turn starting now. See
// assistantPlacement; this is the API's view of it.
func (c *Conductor) AssistantPlacement(ctx context.Context) string {
	return c.assistantPlacement(ctx)
}

// AssistantDefault is where the assistant runs when nothing is saved: the option, else the
// host when there is a host runtime, else nowhere.
func (c *Conductor) AssistantDefault() string {
	if c.assistantRunsOn != "" {
		return c.assistantRunsOn
	}
	if c.host != nil {
		return profiles.RunsOnHost
	}
	return ""
}

// HostAvailable reports whether this conductor can run the assistant as a child of itself.
func (c *Conductor) HostAvailable() bool {
	return c.host != nil
}
