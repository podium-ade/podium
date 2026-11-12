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

// A stop another conductor announced reaches the sources that hear one; this conductor's own
// announcement, coming back on the same channel, does not.
func TestAStopFromAnotherConductorReachesTheSourcesThatHearOne(t *testing.T) {
	src := &hearer{}
	c := &Conductor{replicaID: "here", sources: []Source{src}}

	notice := func(from string) string {
		raw, err := json.Marshal(stopNotice{From: from, Key: "linear:session:s-1", TaskID: "task_1"})
		require.NoError(t, err)
		return string(raw)
	}
	c.heardStop(notice("here"))
	assert.Empty(t, src.stops, "the conductor that stopped it already told its own source")
	c.heardStop("not json")
	assert.Empty(t, src.stops)

	c.heardStop(notice("there"))
	assert.Equal(t, []string{"linear:session:s-1|task_1"}, src.stops)
}
