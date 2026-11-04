package conductor

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

func TestTheRecordIsLabelledWithWhoSaidItAndWhere(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	deleted := at
	chat := store.Chat{Login: "owner@acme.com"}
	msgs := []store.ChatMessage{
		{Role: store.RoleUser, Text: "legacy web line", Via: store.ViaWeb, TS: at},
		{Role: store.RoleUser, Text: "hi from web", Author: "Alvaro", AuthorLogin: "alvaro@acme.com", Via: store.ViaWeb, TS: at, Addressed: true},
		{Role: store.RoleUser, Text: "a side remark", Author: "Dana", Via: store.ViaSlack, TS: at},
		{Role: store.RoleUser, Text: "please review", Author: "Bob", AuthorLogin: "bob@acme.com", Via: store.ViaGitHub, TS: at, Addressed: true},
		{Role: store.RoleProgress, Text: "thinking", Via: store.ViaBot, TS: at},
		{Role: store.RoleActivity, Text: "{}", Via: store.ViaBot, TS: at},
		{Role: store.RoleUser, Text: "gone", Author: "Eve", Via: store.ViaSlack, TS: at, DeletedAt: &deleted},
		{Role: store.RoleAssistant, Text: "the answer", Via: store.ViaBot, TS: at},
	}
	lines := recordLines(msgs, chat, "Podium")
	require.Len(t, lines, 5, "progress, activity and a message deleted on its surface are not history")

	assert.Equal(t, "owner@acme.com", lines[0].Author, "an old web message with no author is the owner's")
	assert.Equal(t, "Alvaro", lines[1].Author, "the web chat carries no origin label")
	assert.Equal(t, "Dana (slack, not in Podium)", lines[2].Author)
	assert.False(t, lines[2].addressed)
	assert.Equal(t, "Bob (github)", lines[3].Author, "a linked GitHub person is not 'not in Podium'")
	assert.True(t, lines[3].addressed)
	assert.Equal(t, "Podium", lines[4].Author)
	assert.Equal(t, RoleAssistant, lines[4].Role)
	assert.Equal(t, BriefTimestamp(at), lines[4].TS)
}

func line(role, author, text string, addressed bool) transcriptLine {
	return transcriptLine{BriefEntry: BriefEntry{Role: role, Author: author, Text: text, TS: "t"}, addressed: addressed}
}

func TestUnderBudgetTheTranscriptGoesThroughWhole(t *testing.T) {
	lines := []transcriptLine{
		line(RoleUser, "a", "one", false),
		line(RoleUser, "b", "two", true),
		line(RoleAssistant, "bot", "three", false),
	}
	out := budgetTranscript(lines, 1<<20, 1, extractiveSummarizer{})
	require.Len(t, out, 3)
	assert.Equal(t, entriesOf(lines), out)
}

func TestOverBudgetTheChatterIsCondensedAndWhatWasAskedIsKept(t *testing.T) {
	long := strings.Repeat("blah ", 200)
	lines := []transcriptLine{
		{BriefEntry: BriefEntry{Role: RoleAssistant, Author: "summary", Text: "earlier summary", TS: "t0"}, keep: true},
		line(RoleUser, "Dana (slack, not in Podium)", "chatter one "+long, false), // condensed
		line(RoleUser, "Bob", "the question", true),                               // kept: addressed
		line(RoleAssistant, "Podium", "the answer", false),                        // kept: its answer
		line(RoleUser, "Dana (slack, not in Podium)", "chatter two "+long, false), // condensed
		line(RoleAssistant, "Podium", "an unprompted line "+long, false),          // condensed: answers nothing addressed
		line(RoleUser, "Eve", "recent one", false),                                // kept: recent
		line(RoleUser, "Bob", "the trigger", true),                                // kept: recent
	}
	out := budgetTranscript(lines, 2000, 2, extractiveSummarizer{})

	var texts []string
	for _, e := range out {
		texts = append(texts, e.Text)
	}
	require.Len(t, out, 6, "summary, condensed note, question, answer, two recent: %q", texts)
	assert.Equal(t, "earlier summary", out[0].Text, "a forced line is never condensed")
	assert.Equal(t, "earlier conversation", out[1].Author, "the condensed note stands where the first condensed line was")
	assert.Contains(t, out[1].Text, "3 earlier messages that were not put to Podium")
	assert.Contains(t, out[1].Text, "Dana (slack, not in Podium) ×2")
	assert.Equal(t, "the question", out[2].Text)
	assert.Equal(t, "the answer", out[3].Text)
	assert.Equal(t, "recent one", out[4].Text)
	assert.Equal(t, "the trigger", out[5].Text)
}

func TestTheCondensedNoteFitsItsLimitAndKeepsTheNewest(t *testing.T) {
	var dropped []BriefEntry
	for i := range 50 {
		dropped = append(dropped, BriefEntry{Author: "Dana", Text: strings.Repeat("x", 100) + string(rune('A'+i%26))})
	}
	text := extractiveSummarizer{}.Condense(dropped, 1200)
	assert.LessOrEqual(t, len(text), 1200+40, "the note stays near its limit")
	assert.Contains(t, text, "older not shown", "what does not fit is counted, not silently lost")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(text), dropped[len(dropped)-1].Text),
		"the newest condensed line survives the cut")
	assert.NotContains(t, text, dropped[0].Text+"\n", "the oldest is the one cut")
	assert.Empty(t, extractiveSummarizer{}.Condense(nil, 100))
}

func TestTheDeliveryBackoffDoublesAndIsCapped(t *testing.T) {
	assert.Equal(t, 10*time.Second, outboxBackoff(1))
	assert.Equal(t, 20*time.Second, outboxBackoff(2))
	assert.Equal(t, 40*time.Second, outboxBackoff(3))
	assert.Equal(t, outboxMaxDelay, outboxBackoff(30))
	assert.Equal(t, 10*time.Second, outboxBackoff(0), "no attempt yet is the first delay")
	c := &Conductor{outboxBase: -1}
	assert.Zero(t, c.retryDelay(3), "a negative base retries at once")
}

func TestOnlyWhatAPersonMustSeeIsOwed(t *testing.T) {
	assert.True(t, durableOut(OutFinal))
	assert.True(t, durableOut(OutFailure))
	assert.True(t, durableOut(OutQuestion))
	assert.False(t, durableOut(OutProgress), "progress is superseded by the answer")
	assert.False(t, durableOut(OutActivity))
}

func TestTheRequestedByNoteNamesThePerson(t *testing.T) {
	note := requestedByNote("Dana Smith", "Dana Smith <dana@acme.com>")
	assert.Contains(t, note, "`Requested-by: Dana Smith <dana@acme.com>`")
	assert.Contains(t, note, `"requested by Dana Smith"`)

	b := &Brief{Instruction: "fix it", GitCredentials: &BriefGitCredentials{URL: "u", TokenEnv: "T"}}
	c := &Conductor{}
	c.withRequestedBy(t.Context(), b, "dana@acme.com", nil)
	assert.True(t, strings.HasPrefix(b.Instruction, "fix it\n\n(Podium note:"), b.Instruction)
	assert.Contains(t, b.Instruction, "Requested-by: dana@acme.com")

	asPerson := &Brief{Instruction: "fix it", GitCredentials: &BriefGitCredentials{URL: "u", TokenEnv: "T"}}
	c.withRequestedBy(t.Context(), asPerson, "dana@acme.com", &BriefGit{Name: "Dana"})
	assert.Equal(t, "fix it", asPerson.Instruction, "a turn committing as the person needs no note")

	noGit := &Brief{Instruction: "fix it"}
	c.withRequestedBy(t.Context(), noGit, "dana@acme.com", nil)
	assert.Equal(t, "fix it", noGit.Instruction, "a turn that cannot write to GitHub needs no note")

	nobody := &Brief{Instruction: "fix it", GitCredentials: &BriefGitCredentials{URL: "u", TokenEnv: "T"}}
	c.withRequestedBy(t.Context(), nobody, "", nil)
	assert.Equal(t, "fix it", nobody.Instruction)
}
