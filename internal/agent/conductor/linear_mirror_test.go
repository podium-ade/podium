package conductor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

// issueSource is a Linear source as the transcript reads it: the issue and its comments.
type issueSource struct {
	entries []BriefEntry
}

func (s *issueSource) Kind() string                                           { return SourceLinear }
func (s *issueSource) Events() <-chan InboundEvent                            { return nil }
func (s *issueSource) Post(context.Context, string, Outbound) (string, error) { return "", nil }
func (s *issueSource) Edit(context.Context, string, string, Outbound) error   { return nil }
func (s *issueSource) Attach(context.Context, string, Attachment) error       { return nil }
func (s *issueSource) React(context.Context, string, Reaction) error          { return nil }
func (s *issueSource) FetchTranscript(context.Context, string) ([]BriefEntry, error) {
	return s.entries, nil
}

func TestALinearMessageIsRecordedAsSaidInLinear(t *testing.T) {
	src := &issueSource{}
	assert.Equal(t, store.ViaLinear, viaOf(src, InboundEvent{SpeakerProvider: store.ProviderLinear}))
	assert.Equal(t, store.ViaLinear, viaOf(src, InboundEvent{}), "the poller names no provider")
	assert.Equal(t, SpeakerLinear, store.ViaLinear, "the surface and the provider are spelled alike")
}

// An outbox retry hands the source the same cancel reason the first attempt had, so a stop is
// still recognised after a restart, when no source remembers it.
func TestTheCancelReasonSurvivesTheOutbox(t *testing.T) {
	out := Outbound{Type: OutFailure, Text: "This was cancelled.", TaskID: "task_1", CancelReason: "stopped"}
	raw, err := json.Marshal(payloadOf(out))
	require.NoError(t, err)
	var back store.OutboxPayload
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, out, outboundOf(back))
}

// A Linear session's record starts with the session. The issue and the comments before it are
// read from Linear in front of it, a comment made on the issue beside the session goes in where
// it was said, and what Linear wrote into the comments for the session itself — the prompts and
// the app's answers — is not read twice.
func TestALinearIssueIsReadAroundItsSessionsRecord(t *testing.T) {
	src := &issueSource{entries: []BriefEntry{
		{Role: RoleUser, Author: "Ada", TS: "2026-11-12T09:00:00Z", Text: "EME-5 — Test issue\n\nThe description."},
		{Role: RoleUser, Author: "Ada", TS: "2026-11-12T09:30:00Z", Text: "an earlier comment"},
		{Role: RoleAssistant, Author: "podium", TS: "2026-11-12T09:40:00Z", Text: "an earlier session's answer"},
		// The mention that opened the session: Linear's comment is a few hundred ms older
		// than the prompt, and the same words.
		{Role: RoleUser, Author: "Ada", TS: "2026-11-12T09:59:59Z", Text: "@podium what is the time?"},
		{Role: RoleAssistant, Author: "podium", TS: "2026-11-12T10:01:00Z", Text: "It is ten."},
		{Role: RoleUser, Author: "Grace", TS: "2026-11-12T10:02:00Z", Text: "said on the issue, not to the app"},
		{Role: RoleUser, Author: "Ada", TS: "2026-11-12T10:05:00Z", Text: "and tomorrow?"},
	}}
	record := []transcriptLine{
		{BriefEntry: BriefEntry{Role: RoleUser, Author: "Ada (linear)", TS: "2026-11-12T10:00:00Z", Text: "@podium what is the time?"}, addressed: true},
		{BriefEntry: BriefEntry{Role: RoleAssistant, Author: "Podium", TS: "2026-11-12T10:01:00Z", Text: "It is ten."}},
		{BriefEntry: BriefEntry{Role: RoleUser, Author: "Ada (linear)", TS: "2026-11-12T10:05:00Z", Text: "and tomorrow?"}, addressed: true},
	}
	c := &Conductor{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	got := c.withIssueHistory(context.Background(), src, "issue/team/EME-5/session", record)
	var texts []string
	for _, l := range got {
		texts = append(texts, l.Text)
	}
	assert.Equal(t, []string{
		"EME-5 — Test issue\n\nThe description.",
		"an earlier comment",
		"an earlier session's answer",
		"@podium what is the time?",
		"It is ten.",
		"said on the issue, not to the app",
		"and tomorrow?",
	}, texts)
	assert.Equal(t, "Ada (linear)", got[3].Author, "the session's own prompt is the record's line")
	assert.True(t, got[0].addressed, "the issue is what the session is about")
	assert.False(t, got[5].addressed, "a comment beside the session was not put to Podium")
}
