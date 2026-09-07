package conductor

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
)

// goldenBrief is step 16's fixture, and agent/runtime/src/brief.ts is the schema it follows.
const goldenBrief = "../../../agent/runtime/testdata/brief.example.json"

func minimalBrief() *Brief {
	return &Brief{
		Version:   BriefVersion,
		SessionID: "sess_01",
		TurnID:    "turn_01",
		Source:    BriefSource{Kind: SourceSlack, Ref: "C1/1.1/1.1"},
		Profile: BriefProfile{
			Name: "podium", DisplayName: "Podium", SystemPrompt: "be Podium",
			Model: "claude-opus-5",
		},
		Playbook: BriefPlaybook{
			Name: "general", SystemPrompt: "answer it", AllowedTools: []string{"read"}, MaxTurns: 20,
		},
		Transcript:  []BriefEntry{},
		Instruction: "hello",
		Provider: &BriefProvider{
			ID:        profiles.ProviderAnthropic,
			APIKeyEnv: profiles.KeyEnvFor(profiles.ProviderAnthropic),
		},
	}
}

// TestBriefMatchesRuntimeSchema is the honesty check between the Go mirror and
// agent/runtime/src/brief.ts. The schema is strict at EVERY level, so a field the Go struct
// has and the fixture does not means the runtime would refuse the brief, and a field the
// fixture has and the Go struct does not means the conductor cannot express something the
// runtime expects. Both directions are compared, key by key, all the way down.
func TestBriefMatchesRuntimeSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Clean(goldenBrief))
	require.NoError(t, err, "the golden brief from step 16 must exist")

	// Round trip 1: the fixture must decode into the Go mirror without loss. Unknown fields
	// are an error here for the same reason they are in the runtime.
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var brief Brief
	require.NoError(t, dec.Decode(&brief),
		"the Go mirror is missing a field agent/runtime/testdata/brief.example.json has")

	assert.Equal(t, 1, brief.Version)
	assert.Equal(t, SourceSlack, brief.Source.Kind)
	assert.True(t, brief.TranscriptTruncated, "the fixture exercises the truncation flag")
	assert.Len(t, brief.Transcript, 2)
	assert.Len(t, brief.Repos, 1)
	require.NotNil(t, brief.Memory)
	// The exact value is pinned by the whole-document comparison below. It is deliberately
	// not spelled here: deploy/env_test.go scans Go string literals for PODIUM_* names, and
	// a name that only a test mentions would look like an undocumented variable.
	assert.NotEmpty(t, brief.Memory.APIKeyEnv)
	assert.NotEmpty(t, brief.Memory.MCPURL)

	// Round trip 2: what the Go mirror writes back must have exactly the fixture's keys.
	// This is the direction that catches a field the mirror invented.
	encoded, err := brief.Encode()
	require.NoError(t, err)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)

	var want, got map[string]any
	require.NoError(t, json.Unmarshal(raw, &want))
	require.NoError(t, json.Unmarshal(decoded, &got))
	assert.Equal(t, want, got,
		"the Go brief and brief.example.json must agree field for field")
}

// The optional members must be ABSENT rather than null: the runtime's schema is strict and
// `null` is not `undefined`.
func TestAnOptionalFieldIsOmittedNotNulled(t *testing.T) {
	encoded, err := minimalBrief().Encode()
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	for _, key := range []string{"repos", "memory"} {
		_, present := got[key]
		assert.False(t, present, "%s must be omitted, not null", key)
	}
	_, present := got["source"].(map[string]any)["url"]
	assert.False(t, present, "source.url must be omitted, not null")

	// And the required ones must be there even when they are empty.
	assert.Equal(t, []any{}, got["transcript"], "transcript must be [] and never absent or null")
	assert.Equal(t, false, got["transcript_truncated"], "transcript_truncated is not optional")
}

// A nil transcript would marshal as null, which the runtime's z.array() refuses.
func TestANilTranscriptBecomesAnEmptyArray(t *testing.T) {
	b := minimalBrief()
	b.Transcript = nil
	b.Playbook.AllowedTools = nil
	encoded, err := b.Encode()
	require.NoError(t, err)
	raw, _ := base64.StdEncoding.DecodeString(encoded)
	assert.Contains(t, string(raw), `"transcript":[]`)
	assert.Contains(t, string(raw), `"allowed_tools":[]`)
}

// The encoder has to agree with examples/agent/brief.sh's `jq -cn`, which does not escape
// these three characters. json.Marshal would.
func TestTheEncoderDoesNotEscapeHTML(t *testing.T) {
	b := minimalBrief()
	b.Instruction = `a < b && c > d`
	encoded, err := b.Encode()
	require.NoError(t, err)
	raw, _ := base64.StdEncoding.DecodeString(encoded)
	assert.Contains(t, string(raw), `a < b && c > d`)
	assert.NotContains(t, string(raw), `\u003c`, "json.Marshal would escape this; json.Encoder must not")
	// Compact, and with no trailing newline: the base64 is the env var's whole value.
	assert.NotContains(t, string(raw), "\n")
	assert.False(t, strings.HasSuffix(encoded, "\n"))
}

// The cap is on the ENCODED brief, and truncation drops the OLDEST entries: the end of a
// conversation is what the turn is answering.
func TestEncodeTruncatesTheOldestEntriesFirst(t *testing.T) {
	b := minimalBrief()
	for i := range 400 {
		b.Transcript = append(b.Transcript, BriefEntry{
			Role:   RoleUser,
			Author: "alice",
			TS:     "2026-09-03T10:00:00Z",
			Text:   strings.Repeat(string(rune('a'+i%26)), 1024),
		})
	}
	kept := len(b.Transcript)

	encoded, err := b.Encode()
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), MaxBriefBytes, "the encoded brief must fit the cap")
	assert.True(t, b.TranscriptTruncated, "dropping history must be declared")
	assert.Less(t, len(b.Transcript), kept, "something must have been dropped")
	require.NotEmpty(t, b.Transcript)
	// The survivors are the newest, so the last entry is still the last entry.
	assert.Equal(t, strings.Repeat(string(rune('a'+399%26)), 1024), b.Transcript[len(b.Transcript)-1].Text)
	assert.NotEqual(t, "a", string(b.Transcript[0].Text[0]),
		"the first entry must no longer be the original first entry")
}

// A brief that does not fit with an empty transcript cannot be fixed by dropping history,
// so it fails loudly rather than being sent for the runtime to refuse.
func TestABriefThatCannotFitAtAllIsAnError(t *testing.T) {
	b := minimalBrief()
	b.Instruction = strings.Repeat("x", MaxBriefBytes)
	_, err := b.Encode()
	require.ErrorIs(t, err, ErrBriefTooLarge)
	assert.Empty(t, b.Transcript)
}

// A brief that already fits is left exactly as it was: nothing dropped, nothing declared.
func TestEncodeLeavesAFittingBriefAlone(t *testing.T) {
	b := minimalBrief()
	b.Transcript = []BriefEntry{{Role: RoleUser, Author: "alice", TS: "2026-09-03T10:00:00Z", Text: "hi"}}
	_, err := b.Encode()
	require.NoError(t, err)
	assert.False(t, b.TranscriptTruncated)
	assert.Len(t, b.Transcript, 1)
}
