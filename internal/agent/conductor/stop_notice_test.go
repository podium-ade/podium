package conductor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
)

// hearer is a source that hears stops, and nothing else.
type hearer struct{ stops []string }

func (s *hearer) Kind() string                                           { return SourceLinear }
func (s *hearer) Events() <-chan InboundEvent                            { return nil }
func (s *hearer) Post(context.Context, string, Outbound) (string, error) { return "", nil }
func (s *hearer) Edit(context.Context, string, string, Outbound) error   { return nil }
func (s *hearer) Attach(context.Context, string, Attachment) error       { return nil }
func (s *hearer) React(context.Context, string, Reaction) error          { return nil }
func (s *hearer) FetchTranscript(context.Context, string) ([]BriefEntry, error) {
	return nil, nil
}
func (s *hearer) StoppedElsewhere(sourceKey, taskID string) {
	s.stops = append(s.stops, sourceKey+"|"+taskID)
}

// The cancellation a run posts carries the control plane's record of why, and nothing else
// does: the source recognises a stop by it, and a failure's reason is never handed on.
func TestOnlyACancelledTaskCarriesItsCancelReason(t *testing.T) {
	cancelled := classify(&podiumv1.Task{
		Id: "task_1", Status: podiumv1.TaskStatus_TASK_STATUS_CANCELLED,
		FailureReason: "stopped from the Linear agent session",
	}, time.Minute)
	assert.Equal(t, "stopped from the Linear agent session", cancelled.CancelReason)
	assert.NotContains(t, cancelled.Post, cancelled.CancelReason, "it is never said")

	failed := classify(&podiumv1.Task{
		Id: "task_2", Status: podiumv1.TaskStatus_TASK_STATUS_FAILED, FailureReason: "oom",
	}, time.Minute)
	assert.Empty(t, failed.CancelReason)
}

// A stop reaches every source that hears one: this conductor's own as it stops the turn, and
// another conductor's through the notice — which, coming back to the conductor that sent it,
// is not passed on twice.
func TestAStopReachesTheSourcesThatHearOneOnEveryConductor(t *testing.T) {
	src := &hearer{}
	c := &Conductor{replicaID: "here", sources: []Source{src}}

	c.announceStop(context.Background(), "linear:session:s-1", "task_1")
	assert.Equal(t, []string{"linear:session:s-1|task_1"}, src.stops, "this conductor's own source, at once")

	notice := func(from string) string {
		raw, err := json.Marshal(stopNotice{From: from, Key: "linear:session:s-2", TaskID: "task_2"})
		require.NoError(t, err)
		return string(raw)
	}
	c.heardStop(notice("here"))
	c.heardStop("not json")
	assert.Len(t, src.stops, 1, "its own notice, coming back, is not passed on again")

	c.heardStop(notice("there"))
	assert.Equal(t, []string{"linear:session:s-1|task_1", "linear:session:s-2|task_2"}, src.stops)
}
