package conductor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/profiles"
	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/pkg/spec"
)

// A playbook's expose reaches the turn's task as it was written, next to the sidecars it
// names, and the spec the conductor builds is one the control plane accepts.
func TestAPlaybooksExposeReachesTheTurnsSpec(t *testing.T) {
	c := skillsConductor(t, "", profiles.Playbook{
		Image:  "podium-agent-runtime:dev",
		Docker: true,
		Expose: &spec.Expose{Ports: map[string]spec.ExposedPort{
			"web": {Port: 3000, From: dockerSidecarName},
		}},
	})
	playbook := c.profiles.Current().Playbooks["coder"]
	choice := c.profiles.Current().Resolve(playbook, profiles.Override{})

	sp := c.taskSpec(chatSource{}, playbook, "encoded-brief", InboundEvent{}, nil, nil, choice, "", "")
	require.NotNil(t, sp.Expose)
	assert.Equal(t, spec.ExposedPort{Port: 3000, From: dockerSidecarName}, sp.Expose.Ports["web"])
	assert.Equal(t, spec.Duration(spec.DefaultExposeTTL), sp.Expose.TTL)
	require.NoError(t, sp.Validate())

	sp.Expose.Ports["web"] = spec.ExposedPort{Port: 1}
	assert.Equal(t, 3000, c.profiles.Current().Playbooks["coder"].Expose.Ports["web"].Port,
		"a turn's spec must not share its ports map with the loaded playbook")
}

func TestPreviewNote(t *testing.T) {
	expires := time.Date(2026, 9, 30, 18, 5, 0, 0, time.UTC)
	task := &podiumv1.Task{Id: "task_1", Preview: &podiumv1.TaskPreview{
		Via: "tailnet", Address: "100.64.0.1", ExpiresAt: timestamppb.New(expires),
		Urls: map[string]string{"web": "http://100.64.0.1:3000", "api": "http://100.64.0.1:5011"},
	}}
	note := previewNote(task)
	assert.Contains(t, note, "until 18:05 UTC")
	assert.Contains(t, note, "podium task release task_1")
	assert.Regexp(t, `api: http://100\.64\.0\.1:5011\n• web: http://100\.64\.0\.1:3000$`, note)

	task.Preview.ReleasedAt = timestamppb.Now()
	assert.Empty(t, previewNote(task), "a released preview is not announced")
	assert.Empty(t, previewNote(&podiumv1.Task{}), "nor is a task without one")
}
