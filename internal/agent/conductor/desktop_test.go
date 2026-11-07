package conductor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/pkg/spec"
)

func desktopSpec(t *testing.T, pb profiles.Playbook) *spec.TaskSpec {
	t.Helper()
	c := skillsConductor(t, "", pb)
	playbook := c.profiles.Current().Playbooks["coder"]
	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})
	return c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, nil, choice, "", "")
}

// A desktop playbook's turn runs its command under podium-desktop, on every placement — the
// image's CMD is never what Podium runs — publishes voxl's port as "desktop" for the server to
// stream, holds nothing afterwards worth holding, and gets no headless browser beside it.
func TestADesktopTurnRunsUnderPodiumDesktopWithItsPortExposed(t *testing.T) {
	sp := desktopSpec(t, profiles.Playbook{Image: profiles.LocalDesktopImage, Desktop: true})

	assert.Equal(t, []string{"podium-desktop", "--", "node", "/opt/podium-agent/dist/main.js"}, sp.Command)
	require.NotNil(t, sp.Expose)
	assert.Equal(t, map[string]spec.ExposedPort{"desktop": {Port: 6901}}, sp.Expose.Ports)
	assert.Equal(t, spec.Duration(desktopHold), sp.Expose.TTL,
		"the desktop goes with its command: nothing is left to look at after it")
	assert.Empty(t, sp.Expose.Via, "the placement chooses: a sandbox's proxy or a LAN address")
	assert.Empty(t, sp.Sidecars, "the browser is on the desktop, not beside it")
	require.NoError(t, sp.Validate())

	// The runtime command itself is not changed for everyone else.
	assert.Equal(t, []string{"node", "/opt/podium-agent/dist/main.js"}, runtimeCommand)
}

// A desktop playbook that also exposes ports keeps them, and its own ttl.
func TestADesktopPlaybooksOwnExposeIsKept(t *testing.T) {
	sp := desktopSpec(t, profiles.Playbook{
		Image: profiles.LocalDesktopImage, Desktop: true,
		Expose: &spec.Expose{TTL: spec.Duration(20 * time.Minute), Ports: map[string]spec.ExposedPort{
			"web": {Port: 3000},
		}},
	})
	assert.Equal(t, map[string]spec.ExposedPort{"web": {Port: 3000}, "desktop": {Port: 6901}}, sp.Expose.Ports)
	assert.Equal(t, spec.Duration(20*time.Minute), sp.Expose.TTL)
	require.NoError(t, sp.Validate())
}

// The same command in a cloud sandbox, where there is no image entrypoint to fall back on.
func TestADesktopTurnInASandboxStillRunsUnderPodiumDesktop(t *testing.T) {
	sp := desktopSpec(t, profiles.Playbook{Image: profiles.LocalDesktopImage, Desktop: true, RunsOn: "modal"})
	assert.Equal(t, desktopCommand, sp.Command)
	require.NoError(t, sp.Validate())
}

// A desktop turn waits longer for its next message, unless the playbook says otherwise.
func TestADesktopTurnWaitsLongerForTheNextMessage(t *testing.T) {
	s := &spec.TaskSpec{Image: "alpine:3"}
	applyWorkspace(s, store.Session{ID: "sess_1"}, profiles.Playbook{Desktop: true})
	assert.Equal(t, spec.Duration(DefaultDesktopWarm), s.WorkspaceWarm)

	s = &spec.TaskSpec{Image: "alpine:3"}
	applyWorkspace(s, store.Session{ID: "sess_1"}, profiles.Playbook{Desktop: true, Env: map[string]string{workspaceWarmEnv: "2h"}})
	assert.Equal(t, spec.Duration(2*time.Hour), s.WorkspaceWarm)
}

// The desktop's port is the server's to stream: its raw URL is no use to a person, so it is
// left out of what the thread and the brief are told.
func TestThePreviewNoteLeavesTheDesktopPortOut(t *testing.T) {
	expires := timestamppb.New(time.Date(2026, 11, 5, 18, 5, 0, 0, time.UTC))
	only := &podiumv1.Task{Id: "task_1", Preview: &podiumv1.TaskPreview{
		ExpiresAt: expires, Urls: map[string]string{"desktop": "https://sandbox.modal.host"},
	}}
	assert.Empty(t, previewNote(only, false), "a desktop alone leaves nothing to announce")

	both := &podiumv1.Task{Id: "task_1", Preview: &podiumv1.TaskPreview{
		ExpiresAt: expires,
		Urls:      map[string]string{"desktop": "https://sandbox.modal.host", "web": "http://10.0.0.5:3000"},
	}}
	note := previewNote(both, false)
	assert.Contains(t, note, "web: http://10.0.0.5:3000")
	assert.NotContains(t, note, "desktop")
	assert.NotContains(t, note, "modal.host")
}

// A desktop turn's brief says it is one, so the runtime can tell the model the desktop is up
// and watched; its browser tools point at the desktop's own Chromium. The menu a host turn
// delegates from says which playbook has a desktop.
func TestADesktopTurnsBriefSaysItRunsOnTheDesktop(t *testing.T) {
	c := skillsConductor(t, "", profiles.Playbook{Image: profiles.LocalDesktopImage, Desktop: true})
	playbook := c.profiles.Current().Playbooks["coder"]
	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})
	b := c.brief(t.Context(), store.Session{ID: "sess_1"}, playbookJob(playbook), "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "open it"}, nil, nil, nil, choice)

	assert.True(t, b.Desktop)
	require.NotNil(t, b.Browser)
	assert.Equal(t, desktopCDPURL, b.Browser.CDPURL)

	menu := c.DelegablePlaybooks()
	require.Len(t, menu, 1)
	assert.True(t, menu[0].Desktop)

	plain := skillsConductor(t, "", profiles.Playbook{})
	pb := plain.profiles.Current().Playbooks["coder"]
	b = plain.brief(t.Context(), store.Session{ID: "sess_1"}, playbookJob(pb), "turn_1",
		InboundEvent{SourceKind: SourceChat, Ref: "chat_1", Text: "go"}, nil, nil, nil,
		plain.profiles.Current().Resolve(pb, profiles.Override{}))
	assert.False(t, b.Desktop)
	assert.False(t, plain.DelegablePlaybooks()[0].Desktop)
}
