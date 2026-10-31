//go:build integration

package conductor_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/conductor/fakesource"
	"github.com/podium-ade/podium/internal/agent/podium"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// onNodes places the assistant on the fleet's nodes, on a conductor with no host runtime at
// all: the assistant is a task, and nothing about a conversation needs this machine.
func onNodes(o *conductor.Options) {
	o.AssistantRunsOn = profiles.RunsOnNodes
	o.GitTaskURL = "http://conductor.test:8090"
	o.MintSecret = "conductor-bearer"
}

// TestTheAssistantRunsAsATaskOfItsOwn is the assistant placed off this host, end to end: the
// task it runs in, the brief it carries, the follow-up that is injected rather than started
// again, and a delegation through the token the task was given.
func TestTheAssistantRunsAsATaskOfItsOwn(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	release := fake.HoldTasks()
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		if taskID != "task_01" {
			return nil
		}
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "on it")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	ctx := context.Background()
	run := startWith(t, st, fake, src, onNodes)
	// The runtime uploads the title before it answers, so it is there when the answer is.
	fake.AddArtifact("task_01", "chat-title.txt", "text/plain", "Flaky test fix")

	require.NoError(t, src.Send(ctx, inbound("C1/1.1", "fix the flaky test")))
	waitFor(t, 30*time.Second, "the assistant's answer", func() bool {
		return len(posts(src.Records(), conductor.OutFinal)) >= 1
	})

	require.Len(t, fake.Specs(), 1)
	spec := fake.Specs()[0]
	b := decodeBrief(t, spec)
	assert.True(t, b.Assistant, "the brief says it is the assistant")
	assert.Empty(t, b.RunsOn, "and that it is in a task, with a container and an inbox")
	assert.Equal(t, conductor.AssistantName, b.Playbook.Name)
	assert.Equal(t, []string{"webfetch", "todoread", "todowrite"}, b.Playbook.AllowedTools,
		"the assistant's tools do not change with where it runs")
	require.NotNil(t, b.Delegation)
	assert.Equal(t, "http://conductor.test:8090", b.Delegation.URL, "delegation goes back through the task URL")
	assert.Empty(t, b.Repos)

	assert.Empty(t, spec.GetWorkspaceSession(), "the assistant has no workspace, so no snapshot")
	assert.Equal(t, 5*time.Minute, spec.GetWorkspaceWarm().AsDuration(), "it stays warm for the next message")
	assert.Nil(t, spec.GetSandbox(), "the fleet's own nodes take no sandbox block")
	assert.Empty(t, spec.GetSidecars())
	assert.Equal(t, profiles.DefaultRuntimeImage(), spec.GetImage())
	var tokenSecret string
	for _, ref := range spec.GetSecrets() {
		if ref.GetKey() == conductor.TurnTokenEnv {
			tokenSecret = ref.GetName()
		}
	}
	require.NotEmpty(t, tokenSecret, "the delegation token travels as a secret")
	assert.NotContains(t, spec.GetEnv(), conductor.TurnTokenEnv)

	waitFor(t, 10*time.Second, "the chat to be named while the task is still warm", func() bool {
		for _, r := range src.Records() {
			if r.Action == fakesource.ActionAutoTitle && r.Text == "Flaky test fix" {
				return true
			}
		}
		return false
	})

	turn := turnOf(t, st, conductor.KindDev+":C1:1.1")
	assert.Equal(t, "task_01", turn.TaskID, "an assistant task's turn has a task id, unlike a host turn")
	token, err := fake.vault.read("", tokenSecret)
	require.NoError(t, err)

	// A follow-up while the assistant task is warm is injected, not a second task.
	require.NoError(t, src.Send(ctx, inbound("C1/1.1", "also the other one")))
	waitFor(t, 30*time.Second, "the follow-up to be injected", func() bool { return len(fake.Injects()) == 1 })
	assert.Equal(t, "also the other one", fake.Injects()[0].GetText())

	// The task delegates through its token, as dist/mcp.js would.
	dlg, err := run.cond.Delegate(ctx, string(token), "coder", "fix the flaky test", conductor.DelegateOptions{})
	require.NoError(t, err)
	assert.Equal(t, turn.ID, dlg.TurnID)
	waitFor(t, 30*time.Second, "the delegated task", func() bool { return len(fake.Specs()) == 2 })
	assert.Equal(t, "write the code", decodeBrief(t, fake.Specs()[1]).Playbook.SystemPrompt)

	// A second conductor on the same database verifies the token without having minted it,
	// for as long as the turn runs.
	replica, err := conductor.New(conductor.Options{
		Store: st, Podium: podium.New(fake.URL(), "devtoken"), Profiles: profiles.NewLive(testProfile(t)),
		Sources: []conductor.Source{src}, Logger: quietLogger(),
		MintSecret: "conductor-bearer", GitTaskURL: "http://conductor.test:8090",
	})
	require.NoError(t, err)
	_, err = replica.Delegations(ctx, string(token))
	require.NoError(t, err, "a replica that never saw the token still serves the running turn")

	release()
	waitFor(t, 60*time.Second, "the turn to finish", func() bool {
		return turnStatus(st, conductor.KindDev+":C1:1.1") == store.TurnSucceeded
	})
	waitFor(t, 10*time.Second, "the token secret to be deleted", func() bool {
		return !fake.vault.has("", tokenSecret)
	})
	_, err = replica.Delegations(ctx, string(token))
	assert.ErrorIs(t, err, conductor.ErrNoTurn, "a finished turn's token is refused everywhere")
	_, err = run.cond.Delegations(ctx, string(token))
	assert.ErrorIs(t, err, conductor.ErrNoTurn)
}

// TestTheAssistantSettingMovesItWithoutARestart: the saved placement wins over the install's
// default, for the next turn.
func TestTheAssistantSettingMovesItWithoutARestart(t *testing.T) {
	st := newStore(t)
	fake := newFakePodium(t)
	fake.events = func(taskID string) []*podiumv1.TaskEvent {
		return []*podiumv1.TaskEvent{messageEvent(taskID, 1, conductor.OutFinal, "hi")}
	}
	src := fakesource.New(conductor.KindDev)
	t.Cleanup(src.Close)
	ctx := context.Background()
	startWith(t, st, fake, src, onNodes)

	require.NoError(t, st.PutSetting(ctx, profiles.AssistantRunsOnKey, profiles.RunsOn{RunsOn: "modal"}))
	require.NoError(t, src.Send(ctx, inbound("C1/1.1", "hello")))
	waitFor(t, 30*time.Second, "the answer", func() bool { return len(fake.Specs()) == 1 })
	require.NotNil(t, fake.Specs()[0].GetSandbox())
	assert.Equal(t, "modal", fake.Specs()[0].GetSandbox().GetProvider())
}
