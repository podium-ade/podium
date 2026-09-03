package slack

import (
	"regexp"
	"strings"
	"testing"

	"github.com/slack-go/slack/slackevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitLeavesShortTextAlone(t *testing.T) {
	assert.Equal(t, []string{"hello"}, Split("hello", MaxMessageChars))
	assert.Equal(t, []string{""}, Split("", MaxMessageChars))
}

// The split prefers the last newline that fits, so a paragraph is not cut mid-sentence.
func TestSplitPrefersANewline(t *testing.T) {
	text := strings.Repeat("a", 8) + "\n" + strings.Repeat("b", 8)
	got := Split(text, 12)
	require.Len(t, got, 2)
	assert.Equal(t, strings.Repeat("a", 8), got[0])
	assert.Equal(t, strings.Repeat("b", 8), got[1])
}

// With no newline in reach it splits at the limit rather than refusing.
func TestSplitFallsBackToTheHardLimit(t *testing.T) {
	got := Split(strings.Repeat("x", 25), 10)
	require.Len(t, got, 3)
	assert.Equal(t, 10, len(got[0]))
	assert.Equal(t, 10, len(got[1]))
	assert.Equal(t, 5, len(got[2]))
	assert.Equal(t, strings.Repeat("x", 25), strings.Join(got, ""))
}

// The limit is characters, and a multi-byte one must never be cut in half.
func TestSplitCountsRunesNotBytes(t *testing.T) {
	text := strings.Repeat("é", 10)
	got := Split(text, 4)
	require.Len(t, got, 3)
	for _, part := range got {
		assert.True(t, len([]rune(part)) <= 4)
		assert.True(t, strings.Count(part, "é")*2 == len(part), "no rune may be cut in half: %q", part)
	}
	assert.Equal(t, text, strings.Join(got, ""))
}

// Every part has to be one Slack accepts, which is the whole point of the function.
func TestSplitNeverExceedsTheLimit(t *testing.T) {
	text := strings.Repeat("word ", 4000)
	for _, part := range Split(text, MaxMessageChars) {
		assert.LessOrEqual(t, len([]rune(part)), MaxMessageChars)
	}
}

func TestStripMentionRemovesEveryOccurrence(t *testing.T) {
	s := &Source{mentionRE: regexp.MustCompile(`<@U0BOT(\|[^>]*)?>`)}
	assert.Equal(t, "what does this repo do?",
		s.stripMention("<@U0BOT> what does this repo do? <@U0BOT>"))
	// Slack sometimes sends the label form.
	assert.Equal(t, "hi", s.stripMention("<@U0BOT|podium> hi"))
	// Somebody else's mention is left exactly as it was: it is part of what they said.
	assert.Equal(t, "<@U0ALICE> please look", s.stripMention("<@U0BOT> <@U0ALICE> please look"))
}

func TestRefRoundTrips(t *testing.T) {
	ref := Ref("C0123", "1725000000.000100", "1725000001.000200")
	channel, thread, trigger, err := ParseRef(ref)
	require.NoError(t, err)
	assert.Equal(t, "C0123", channel)
	assert.Equal(t, "1725000000.000100", thread)
	assert.Equal(t, "1725000001.000200", trigger)
}

func TestParseRefRefusesAnythingElse(t *testing.T) {
	for _, ref := range []string{"", "C1", "C1/1.1", "C1//1.1", "C1/1.1/1.1/extra"} {
		_, _, _, err := ParseRef(ref)
		assert.Error(t, err, "%q must not parse", ref)
	}
}

// A mention that starts a thread has an empty thread_ts and is its own parent, which is
// what conversations.replies needs as its Timestamp.
func TestThreadOf(t *testing.T) {
	assert.Equal(t, "1.1", threadOf("", "1.1"))
	assert.Equal(t, "1.1", threadOf("1.1", "2.2"))
}

func TestSourceKeyIsTheThreadNotTheMessage(t *testing.T) {
	assert.Equal(t, "slack:C1:1.1", sourceKey("C1", "1.1"))
}

func TestParseTS(t *testing.T) {
	got := parseTS("1725000000.000100")
	assert.Equal(t, int64(1725000000), got.Unix())
	assert.Equal(t, 100_000, got.Nanosecond(), "the fraction is microseconds")
	assert.False(t, parseTS("nonsense").IsZero(), "an unparseable ts must not be the zero time")
}

// Slack delivers a channel mention as BOTH an app_mention and a message event, so one
// message must start one turn.
func TestFirstSightingDeduplicatesOneMessage(t *testing.T) {
	s, err := New(Options{AppToken: "xapp-x", BotToken: "xoxb-x"})
	require.NoError(t, err)
	assert.True(t, s.firstSighting("C1", "1.1"))
	assert.False(t, s.firstSighting("C1", "1.1"), "the second delivery of one message is dropped")
	assert.True(t, s.firstSighting("C1", "1.2"), "a different message is a different message")
	assert.True(t, s.firstSighting("C2", "1.1"), "so is the same ts in another channel")
}

func TestNewRefusesHalfTheCredentials(t *testing.T) {
	_, err := New(Options{AppToken: "xapp-x"})
	require.Error(t, err)
	_, err = New(Options{BotToken: "xoxb-x"})
	require.Error(t, err)
}

// The bot's own placeholder and progress messages are scaffolding, not conversation.
func TestIsNoise(t *testing.T) {
	assert.True(t, isNoise("👀 working…"))
	assert.True(t, isNoise("⏳ reading the handler"))
	assert.False(t, isNoise("the handler dereferences the first label"))
	assert.False(t, isNoise("Something went wrong on my side. Task `task_01`."))
}

// The concrete inner event types are POINTERS: slackevents builds them with reflect.New, so
// a value-typed type switch compiles and silently never matches. This pins the shape the
// router depends on.
func TestInnerEventsArrivePointerTyped(t *testing.T) {
	raw := []byte(`{"token":"t","team_id":"T1","api_app_id":"A1","type":"event_callback",
		"event":{"type":"app_mention","user":"U1","text":"<@U0BOT> hi","ts":"1.1",
		"channel":"C1","event_ts":"1.1"}}`)
	ev, err := slackevents.ParseEvent(raw, slackevents.OptionNoVerifyToken())
	require.NoError(t, err)

	mention, ok := ev.InnerEvent.Data.(*slackevents.AppMentionEvent)
	require.True(t, ok, "app_mention must arrive as *slackevents.AppMentionEvent")
	assert.Equal(t, "C1", mention.Channel)
	assert.Equal(t, "1.1", mention.TimeStamp, "the field is TimeStamp, capital S, unlike slack.Msg")
	assert.Empty(t, mention.ThreadTimeStamp, "a top-level mention starts a thread at its own ts")

	_, valueTyped := ev.InnerEvent.Data.(slackevents.AppMentionEvent) //nolint:staticcheck // the point of the test
	assert.False(t, valueTyped, "a value-typed case would compile and never match")
}
