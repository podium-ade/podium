package slack

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/ghreview"
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

// A reply in a thread is not a turn unless the bot is @-mentioned. Having already
// participated is not enough. It is still conversation: it is offered as RecordOnly, which
// the conductor records into a thread it already holds and never acts on.
func TestInnerRecordsAThreadReplyWithoutAMention(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.MessageEvent{
				Channel:         "C1",
				User:            "U1",
				Text:            "and then?",
				TimeStamp:       "100.2",
				ThreadTimeStamp: "100.1",
				ChannelType:     "channel",
			},
		},
	})

	ev := takeInbound(t, s)
	assert.True(t, ev.RecordOnly, "chatter is recorded, never a turn")
	assert.False(t, ev.Mentioned)
	assert.Equal(t, "slack:C1:100.1", ev.SourceKey)
	assert.Equal(t, "100.2", ev.ExternalID)
	assert.Equal(t, "https://podium.slack.com/archives/C1/p1002", ev.ExternalURL)
	assert.Equal(t, "https://podium.slack.com/archives/C1/p1001", ev.ThreadURL, "the thread's own link, for the chat's surface chip")
	assert.Equal(t, "and then?", ev.Text)
	assertNoInbound(t, s)
}

// A thread that asked for a pull-request review is a door into the PR's conversation, so its
// chatter, edits and deletes are recorded there and not as a Slack thread of their own.
func TestInnerRecordsABoundThreadIntoThePullRequest(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"
	s.review = &fakeReview{bound: map[string]string{"C1/100.1": "github:acme/api#12"}}

	s.inner(t.Context(), slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{
		Data: &slackevents.MessageEvent{
			Channel: "C1", User: "U1", Text: "and then?",
			TimeStamp: "100.2", ThreadTimeStamp: "100.1", ChannelType: "channel",
		},
	}})
	ev := takeInbound(t, s)
	assert.True(t, ev.RecordOnly)
	assert.Equal(t, "github:acme/api#12", ev.MirrorKey)
	assert.Equal(t, "100.2", ev.ExternalID)

	s.inner(t.Context(), slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{
		Data: &slackevents.MessageEvent{
			Channel: "C1", SubType: "message_changed", ChannelType: "channel",
			Message:         &slack.Msg{User: "U1", Text: "and then what?", Timestamp: "100.2", ThreadTimestamp: "100.1"},
			PreviousMessage: &slack.Msg{User: "U1", Text: "and then?", Timestamp: "100.2", ThreadTimestamp: "100.1"},
		},
	}})
	edit := takeInbound(t, s)
	assert.Equal(t, conductor.ChangeEdited, edit.Change)
	assert.Equal(t, "github:acme/api#12", edit.MirrorKey)

	s.inner(t.Context(), slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{
		Data: &slackevents.MessageEvent{
			Channel: "C1", SubType: "message_deleted", ChannelType: "channel", DeletedTimeStamp: "100.2",
			PreviousMessage: &slack.Msg{User: "U1", Text: "and then what?", Timestamp: "100.2", ThreadTimestamp: "100.1"},
		},
	}})
	del := takeInbound(t, s)
	assert.Equal(t, conductor.ChangeDeleted, del.Change)
	assert.Equal(t, "github:acme/api#12", del.MirrorKey)

	// A thread that is not bound is its own conversation, as before.
	s.inner(t.Context(), slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{
		Data: &slackevents.MessageEvent{
			Channel: "C2", User: "U1", Text: "elsewhere",
			TimeStamp: "300.2", ThreadTimeStamp: "300.1", ChannelType: "channel",
		},
	}})
	other := takeInbound(t, s)
	assert.Empty(t, other.MirrorKey)
	assertNoInbound(t, s)
}

// A message that mentions the bot arrives as app_mention AND as message. The message copy
// must not be recorded as chatter: the conductor would then find the mention already
// recorded and drop it as a duplicate, and the turn would never start.
func TestInnerLeavesAThreadMentionToAppMention(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"

	s.inner(t.Context(), slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{
		Data: &slackevents.MessageEvent{
			Channel: "C1", User: "U1", Text: "<@UBOT> and then?",
			TimeStamp: "100.2", ThreadTimeStamp: "100.1", ChannelType: "channel",
		},
	}})

	assertNoInbound(t, s)
}

// A top-level channel message is not part of any thread Podium could hold, and a message
// from a bot is never conversation input.
func TestInnerIgnoresChannelChatterAndBots(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"

	for _, ev := range []*slackevents.MessageEvent{
		{Channel: "C1", User: "U1", Text: "lunch?", TimeStamp: "100.1", ChannelType: "channel"},
		{Channel: "C1", BotID: "B9", User: "U9", Text: "deploy done", TimeStamp: "100.3", ThreadTimeStamp: "100.1", ChannelType: "channel"},
		{Channel: "C1", User: "U1", SubType: "channel_join", TimeStamp: "100.4", ThreadTimeStamp: "100.1", ChannelType: "channel"},
	} {
		s.inner(t.Context(), slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{Data: ev}})
	}

	assertNoInbound(t, s)
}

// An edit is offered as a change of the message it edits, with the new text converted.
// An "edit" that changed nothing a person typed (an unfurl) is not offered at all.
func TestInnerOffersEditsAndDeletes(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"

	s.inner(t.Context(), slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{
		Data: &slackevents.MessageEvent{
			Channel: "C1", SubType: "message_changed", ChannelType: "channel", EventTimeStamp: "200.0",
			Message:         &slack.Msg{User: "U1", Text: "now *bold*", Timestamp: "100.2", ThreadTimestamp: "100.1"},
			PreviousMessage: &slack.Msg{User: "U1", Text: "now bold", Timestamp: "100.2", ThreadTimestamp: "100.1"},
		},
	}})
	edit := takeInbound(t, s)
	assert.Equal(t, conductor.ChangeEdited, edit.Change)
	assert.Equal(t, "100.2", edit.ExternalID)
	assert.Equal(t, "slack:C1:100.1", edit.SourceKey)
	assert.Equal(t, "now **bold**", edit.Text)

	s.inner(t.Context(), slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{
		Data: &slackevents.MessageEvent{
			Channel: "C1", SubType: "message_changed", ChannelType: "channel",
			Message:         &slack.Msg{User: "U1", Text: "same", Timestamp: "100.3", ThreadTimestamp: "100.1"},
			PreviousMessage: &slack.Msg{User: "U1", Text: "same", Timestamp: "100.3", ThreadTimestamp: "100.1"},
		},
	}})
	assertNoInbound(t, s)

	s.inner(t.Context(), slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{
		Data: &slackevents.MessageEvent{
			Channel: "C1", SubType: "message_deleted", ChannelType: "channel", DeletedTimeStamp: "100.2",
			PreviousMessage: &slack.Msg{User: "U1", Text: "now *bold*", Timestamp: "100.2", ThreadTimestamp: "100.1"},
		},
	}})
	del := takeInbound(t, s)
	assert.Equal(t, conductor.ChangeDeleted, del.Change)
	assert.Equal(t, "100.2", del.ExternalID)
	assert.Equal(t, "slack:C1:100.1", del.SourceKey)
}

// History is the thread before the mention that brought Podium in: people only, in order,
// and never the mention itself or anything after it.
func TestHistoryIsTheThreadBeforeTheTrigger(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("conversations.replies", `{"ok":true,"messages":[
		{"user":"U1","ts":"100.1","text":"the deploy is *stuck*"},
		{"user":"UBOT","bot_id":"BBOT","ts":"100.2","text":"an older answer"},
		{"user":"U2","ts":"100.3","text":"same here"},
		{"user":"U2","ts":"100.4","subtype":"channel_join","text":""},
		{"user":"U1","ts":"100.5","text":"<@UBOT> help?"},
		{"user":"U2","ts":"100.6","text":"after"}]}`)
	s := testSource(t, f)
	s.users["U1"] = "alice"
	s.users["U2"] = "bob"

	got, err := s.History(t.Context(), Ref("C1", "100.1", "100.5"))

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "100.1", got[0].ExternalID)
	assert.Equal(t, "the deploy is **stuck**", got[0].Text)
	assert.True(t, got[0].RecordOnly)
	assert.Equal(t, "100.3", got[1].ExternalID)

	none, err := s.History(t.Context(), Ref("C1", "100.1", "100.1"))
	require.NoError(t, err)
	assert.Empty(t, none, "a mention that started the thread has nothing before it")
}

// The same words with an @-mention are a turn, and the reply goes in that thread.
func TestInnerEmitsAThreadMention(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.AppMentionEvent{
				Channel:         "C1",
				User:            "U1",
				Text:            "<@UBOT> and then?",
				TimeStamp:       "100.2",
				ThreadTimeStamp: "100.1",
			},
		},
	})

	ev := takeInbound(t, s)
	assert.Equal(t, "C1", ev.Channel)
	assert.Equal(t, "and then?", ev.Text, "the mention is stripped")
	assert.Equal(t, Ref("C1", "100.1", "100.2"), ev.Ref)
}

type fakeReview struct {
	bound    map[string]string
	ingested []ghreview.SlackMention
}

func (f *fakeReview) LookupSlack(_ context.Context, slackRef string) (string, bool, error) {
	k, ok := f.bound[slackRef]
	return k, ok, nil
}

func (f *fakeReview) BindSlack(_ context.Context, slackRef, sourceKey string) error {
	if f.bound == nil {
		f.bound = map[string]string{}
	}
	if existing, ok := f.bound[slackRef]; ok && existing != sourceKey {
		return ghreview.ErrBoundToOther
	}
	f.bound[slackRef] = sourceKey
	return nil
}

func (f *fakeReview) IngestSlack(_ context.Context, m ghreview.SlackMention) error {
	f.ingested = append(f.ingested, m)
	return nil
}

func TestAMentionWithOnePRURLIsForwardedToGitHub(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"
	rev := &fakeReview{}
	s.SetReview(rev)

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.AppMentionEvent{
				Channel:   "C1",
				User:      "U1",
				Text:      "<@UBOT> review https://github.com/acme/repo/pull/12",
				TimeStamp: "100.1",
			},
		},
	})

	assertNoInbound(t, s)
	require.Len(t, rev.ingested, 1)
	assert.Equal(t, "github:acme/repo#12", ghreview.SourceKeyPR(rev.ingested[0].PR))
	assert.Equal(t, "C1/100.1", func() string {
		for k := range rev.bound {
			return k
		}
		return ""
	}())
}

func TestASecondMentionInABoundThreadNeedsNoURL(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"
	rev := &fakeReview{bound: map[string]string{"C1/100.1": "github:acme/repo#12"}}
	s.SetReview(rev)

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.AppMentionEvent{
				Channel:         "C1",
				User:            "U1",
				Text:            "<@UBOT> look at the tests",
				TimeStamp:       "100.2",
				ThreadTimeStamp: "100.1",
			},
		},
	})

	assertNoInbound(t, s)
	require.Len(t, rev.ingested, 1)
	assert.Equal(t, 12, rev.ingested[0].PR.Number)
	assert.Equal(t, "look at the tests", rev.ingested[0].Text)
}

func TestTwoPRURLsAreRefusedRatherThanBound(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"
	rev := &fakeReview{}
	s.SetReview(rev)

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.AppMentionEvent{
				Channel:   "C1",
				User:      "U1",
				Text:      "<@UBOT> https://github.com/acme/repo/pull/12 and https://github.com/acme/repo/pull/13",
				TimeStamp: "100.1",
			},
		},
	})

	assertNoInbound(t, s)
	assert.Empty(t, rev.ingested)
	assert.Empty(t, rev.bound)
	assert.Contains(t, f.methods(), "chat.postMessage")
}

func TestADifferentPRInABoundThreadIsRefused(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"
	rev := &fakeReview{bound: map[string]string{"C1/100.1": "github:acme/repo#12"}}
	s.SetReview(rev)

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.AppMentionEvent{
				Channel:         "C1",
				User:            "U1",
				Text:            "<@UBOT> now https://github.com/acme/repo/pull/13",
				TimeStamp:       "100.2",
				ThreadTimeStamp: "100.1",
			},
		},
	})

	assertNoInbound(t, s)
	assert.Empty(t, rev.ingested)
	assert.Contains(t, f.methods(), "chat.postMessage")
}

func TestAMentionWithAPRURLIsANormalSlackEventWhenGitHubIsOff(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.AppMentionEvent{
				Channel:   "C1",
				User:      "U1",
				Text:      "<@UBOT> review https://github.com/acme/repo/pull/12",
				TimeStamp: "100.1",
			},
		},
	})

	ev := takeInbound(t, s)
	assert.Equal(t, "slack:C1:100.1", ev.SourceKey)
	assert.Contains(t, ev.Text, "https://github.com/acme/repo/pull/12")
}

// A DM is a conversation of its own and does not need a mention.
func TestInnerEmitsADirectMessage(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.MessageEvent{
				Channel:     "D1",
				User:        "U1",
				Text:        "hello",
				TimeStamp:   "100.1",
				ChannelType: "im",
			},
		},
	})

	ev := takeInbound(t, s)
	assert.Equal(t, "D1", ev.Channel)
	assert.Equal(t, "hello", ev.Text)
	assert.Equal(t, Ref("D1", "100.1", "100.1"), ev.Ref, "a DM starts a thread at its own ts")
}

// A top-level channel mention still starts a turn. That path is app_mention, not message.
func TestInnerEmitsATopLevelMention(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)
	s.users["U1"] = "alice"

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.AppMentionEvent{
				Channel:   "C1",
				User:      "U1",
				Text:      "<@UBOT> what does this repo do?",
				TimeStamp: "100.1",
			},
		},
	})

	ev := takeInbound(t, s)
	assert.Equal(t, "what does this repo do?", ev.Text)
	assert.Equal(t, Ref("C1", "100.1", "100.1"), ev.Ref)
}

func TestInnerResolvesTheChannelName(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("conversations.info", `{"ok":true,"channel":{"id":"C1","name":"support"}}`)
	s := testSource(t, f)
	s.users["U1"] = "alice"
	// The account check is fresh, so it does not cost a users.info either.
	s.accounts = map[string]slackAccount{"U1": {login: "alice@acme.com", externalID: "T1/U1", at: time.Now()}}

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.AppMentionEvent{
				Channel:   "C1",
				User:      "U1",
				Text:      "<@UBOT> hello",
				TimeStamp: "100.1",
			},
		},
	})

	ev := takeInbound(t, s)
	assert.Equal(t, "C1", ev.Channel)
	assert.Equal(t, "support", ev.ChannelName)
	// The speaker as the conductor will resolve them: a candidate email, the account, and
	// the fact that they addressed the bot.
	assert.Equal(t, "alice@acme.com", ev.Login)
	assert.Equal(t, conductor.SpeakerSlack, ev.SpeakerProvider)
	assert.Equal(t, "T1/U1", ev.SpeakerID)
	assert.True(t, ev.Mentioned)
	assert.Equal(t, []string{"conversations.info"}, f.methods())
}

// Whether a conversation is public is Slack's word, read with its name in the same call and
// cached with it: the share dialog warns about sharing a private one.
func TestInnerReportsTheChannelPrivacy(t *testing.T) {
	for _, tc := range []struct {
		channel, info, want string
	}{
		{"C1", `{"ok":true,"channel":{"id":"C1","name":"support"}}`, conductor.ChannelPublic},
		{"C2", `{"ok":true,"channel":{"id":"C2","name":"secret","is_private":true}}`, conductor.ChannelPrivate},
		{"D3", `{"ok":true,"channel":{"id":"D3","is_im":true,"user":"U1"}}`, conductor.ChannelIM},
		{"G4", `{"ok":true,"channel":{"id":"G4","name":"mpdm-a--b","is_mpim":true,"is_private":true}}`, conductor.ChannelMPIM},
	} {
		t.Run(tc.want, func(t *testing.T) {
			f := newFakeSlack(t)
			f.reply("conversations.info", tc.info)
			s := testSource(t, f)
			s.users["U1"] = "alice"
			s.accounts = map[string]slackAccount{"U1": {login: "alice@acme.com", externalID: "T1/U1", at: time.Now()}}

			for _, ts := range []string{"100.1", "100.2"} {
				s.inner(t.Context(), slackevents.EventsAPIEvent{InnerEvent: slackevents.EventsAPIInnerEvent{
					Data: &slackevents.AppMentionEvent{Channel: tc.channel, User: "U1", Text: "<@UBOT> hello", TimeStamp: ts},
				}})
				ev := takeInbound(t, s)
				assert.Equal(t, tc.want, ev.ChannelPrivacy)
			}
			assert.Equal(t, []string{"conversations.info"}, f.methods(), "one lookup, cached")
		})
	}
}

func TestInnerLeavesTheChannelNameEmptyWhenSlackWillNotResolveIt(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("conversations.info", `{"ok":false,"error":"channel_not_found"}`)
	s := testSource(t, f)
	s.users["U1"] = "alice"

	s.inner(t.Context(), slackevents.EventsAPIEvent{
		InnerEvent: slackevents.EventsAPIInnerEvent{
			Data: &slackevents.AppMentionEvent{
				Channel:   "C1",
				User:      "U1",
				Text:      "<@UBOT> hello",
				TimeStamp: "100.1",
			},
		},
	})

	ev := takeInbound(t, s)
	assert.Equal(t, "C1", ev.Channel)
	assert.Empty(t, ev.ChannelName)
}

// ---------------------------------------------------------------------------
// the Web API methods
//
// Everything below drives the four ways the conductor talks back, plus the thread read,
// against a fake Slack. Until Options.apiURL existed none of it was covered by anything:
// the conductor's tests use a fake source, so this package's own calls to Slack had never
// been executed. The connection itself is still untested — that needs Socket Mode.
// ---------------------------------------------------------------------------

// apiCall is one request the fake saw.
type apiCall struct {
	method string
	form   url.Values
}

// fakeSlack is Slack's Web API as far as these tests are concerned. Every method answers
// {"ok":true} unless the test queued something richer, and every call is recorded in order
// so a test can assert what was NOT called as well as what was.
type fakeSlack struct {
	server *httptest.Server

	mu     sync.Mutex
	calls  []apiCall
	queued map[string][]string
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()
	f := &fakeSlack{queued: map[string][]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeSlack) handle(w http.ResponseWriter, r *http.Request) {
	// The file upload's middle step is a multipart POST to a URL the fake handed out; every
	// Web API method is form-encoded.
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		_ = r.ParseMultipartForm(8 << 20)
	} else {
		_ = r.ParseForm()
	}
	method := strings.TrimPrefix(r.URL.Path, "/")

	f.mu.Lock()
	f.calls = append(f.calls, apiCall{method: method, form: r.Form})
	body := `{"ok":true}`
	if q := f.queued[method]; len(q) > 0 {
		body, f.queued[method] = q[0], q[1:]
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

// reply queues one response body for one method. Queued bodies are consumed in order, so
// two calls to the same method can be answered differently.
func (f *fakeSlack) reply(method, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queued[method] = append(f.queued[method], body)
}

// methods is every call in order, which is what makes "it did not remove a reaction that
// was never there" an assertion rather than a hope.
func (f *fakeSlack) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method)
	}
	return out
}

// form is the form of the nth call to one method.
func (f *fakeSlack) form(t *testing.T, method string, n int) url.Values {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := 0
	for _, c := range f.calls {
		if c.method != method {
			continue
		}
		if seen == n {
			return c.form
		}
		seen++
	}
	t.Fatalf("no call %d to %s; calls were %v", n, method, f.methods())
	return nil
}

// testSource is a Source pointed at the fake, holding the identity Run would have learned
// from auth.test. The limiters are replaced: the real ones are a second per write, and a
// test that waited on them would prove nothing about the code under test.
func testSource(t *testing.T, f *fakeSlack) *Source {
	t.Helper()
	s, err := New(Options{
		AppToken: "xapp-test",
		BotToken: "xoxb-test",
		Logger:   slog.New(slog.DiscardHandler),
		apiURL:   f.server.URL + "/",
	})
	require.NoError(t, err)
	s.botUserID = "UBOT"
	s.botID = "BBOT"
	s.mentionRE = regexp.MustCompile(`<@UBOT(\|[^>]*)?>`)
	s.archiveBase = "https://podium.slack.com"
	s.writes = rate.NewLimiter(rate.Inf, 1)
	s.reads = rate.NewLimiter(rate.Inf, 1)
	return s
}

// Starting work is a ⏳ on the trigger and nothing posted. The conductor still offers the
// "working…" placeholder (every source sees the same Post); Slack drops it.
func TestStartingWorkReactsAndPostsNoPlaceholder(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)

	id, err := s.Post(t.Context(), Ref("C1", "100.1", "100.1"),
		conductor.Outbound{Type: conductor.OutProgress, Text: conductor.Placeholder})
	require.NoError(t, err)
	assert.Empty(t, id)
	require.NoError(t, s.React(t.Context(), Ref("C1", "100.1", "100.1"), conductor.ReactionWorking))

	assert.Equal(t, []string{"reactions.add"}, f.methods())
	assert.Equal(t, emojiWorking, f.form(t, "reactions.add", 0).Get("name"))
	assert.Equal(t, "100.1", f.form(t, "reactions.add", 0).Get("timestamp"))
}

// The three emoji, pinned by name. They are the whole of what a reader sees about a turn's
// state — Slack posts no placeholder, so the running mark is the ONLY thing that says work
// has started — and the assertions above are symbolic, so nothing else would notice if one
// of them changed.
func TestTheThreeStatesAreHourglassCheckAndCross(t *testing.T) {
	assert.Equal(t, "hourglass_flowing_sand", emojiWorking, "running: ⏳")
	assert.Equal(t, "white_check_mark", emojiDone, "succeeded: ✅")
	assert.Equal(t, "x", emojiFailed, "failed: ❌")
}

// The working mark is the first thing a turn does and it ADDS ONLY. The trigger is a
// message a human has just sent, so there is nothing of the bot's on it to remove, and the
// two removals this used to send were a second each of silence in front of the working mark.
func TestReactWorkingOnlyAdds(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)

	require.NoError(t, s.React(t.Context(), Ref("C1", "100.1", "100.1"), conductor.ReactionWorking))

	assert.Equal(t, []string{"reactions.add"}, f.methods())
	form := f.form(t, "reactions.add", 0)
	assert.Equal(t, emojiWorking, form.Get("name"))
	assert.Equal(t, "C1", form.Get("channel"))
	assert.Equal(t, "100.1", form.Get("timestamp"))
}

// An outcome replaces the working mark, so it removes exactly that one and adds its own.
func TestReactOutcomeReplacesTheWorkingMark(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kind  conductor.Reaction
		emoji string
	}{
		{"done", conductor.ReactionDone, emojiDone},
		{"failed", conductor.ReactionFailed, emojiFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSlack(t)
			s := testSource(t, f)

			require.NoError(t, s.React(t.Context(), Ref("C1", "100.1", "100.1"), tc.kind))

			assert.Equal(t, []string{"reactions.remove", "reactions.add"}, f.methods())
			assert.Equal(t, emojiWorking, f.form(t, "reactions.remove", 0).Get("name"))
			assert.Equal(t, tc.emoji, f.form(t, "reactions.add", 0).Get("name"))
		})
	}
}

// The reaction goes on the message that TRIGGERED the turn, not on the thread it replies in.
func TestReactMarksTheTriggerNotTheThread(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)

	require.NoError(t, s.React(t.Context(), Ref("C1", "100.1", "100.9"), conductor.ReactionWorking))

	assert.Equal(t, "100.9", f.form(t, "reactions.add", 0).Get("timestamp"))
}

func TestReactRefusesAnUnknownKind(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)

	require.Error(t, s.React(t.Context(), Ref("C1", "100.1", "100.1"), conductor.Reaction("shrug")))
	assert.Empty(t, f.methods(), "an unknown reaction must not reach Slack")
}

// "Already reacted" and "no reaction" mean the world is how we asked for it, which is not a
// failure of the turn.
func TestReactToleratesSlackSayingItIsAlreadyDone(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("reactions.remove", `{"ok":false,"error":"no_reaction"}`)
	f.reply("reactions.add", `{"ok":false,"error":"already_reacted"}`)
	s := testSource(t, f)

	assert.NoError(t, s.React(t.Context(), Ref("C1", "100.1", "100.1"), conductor.ReactionDone))
}

// A real refusal of the ADD is the turn's problem, because the mark is what a human reads.
func TestReactReportsARealFailure(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("reactions.add", `{"ok":false,"error":"channel_not_found"}`)
	s := testSource(t, f)

	require.ErrorContains(t,
		s.React(t.Context(), Ref("C1", "100.1", "100.1"), conductor.ReactionWorking),
		"channel_not_found")
}

func TestPostRepliesInTheThread(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	s := testSource(t, f)

	id, err := s.Post(t.Context(), Ref("C1", "100.1", "100.9"),
		conductor.Outbound{Type: conductor.OutFinal, Text: "the answer"})

	require.NoError(t, err)
	assert.Equal(t, "200.1", id)
	form := f.form(t, "chat.postMessage", 0)
	assert.Equal(t, "C1", form.Get("channel"))
	assert.Equal(t, "100.1", form.Get("thread_ts"), "a reply goes in the thread, not on the trigger")
	assert.Equal(t, "the answer", form.Get("text"))
}

// Over Slack's limit the answer arrives as consecutive messages, and the id returned is the
// FIRST one's, because that is the message progress edits address.
func TestPostSplitsLongTextAndReturnsTheFirstID(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	f.reply("chat.postMessage", `{"ok":true,"channel":"C1","ts":"200.2"}`)
	s := testSource(t, f)

	head, tail := strings.Repeat("a", MaxMessageChars), strings.Repeat("b", 10)
	id, err := s.Post(t.Context(), Ref("C1", "100.1", "100.1"),
		conductor.Outbound{Type: conductor.OutFinal, Text: head + "\n" + tail})

	require.NoError(t, err)
	assert.Equal(t, "200.1", id)
	assert.Equal(t, []string{"chat.postMessage", "chat.postMessage"}, f.methods())
	assert.Equal(t, head, f.form(t, "chat.postMessage", 0).Get("text"))
	assert.Equal(t, tail, f.form(t, "chat.postMessage", 1).Get("text"))
	assert.Equal(t, "100.1", f.form(t, "chat.postMessage", 1).Get("thread_ts"))
}

func TestEditReplacesTheMessage(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.update", `{"ok":true,"channel":"C1","ts":"200.1","text":"⏳ still going"}`)
	s := testSource(t, f)

	require.NoError(t, s.Edit(t.Context(), Ref("C1", "100.1", "100.1"), "200.1",
		conductor.Outbound{Type: conductor.OutProgress, Text: "⏳ still going"}))

	form := f.form(t, "chat.update", 0)
	assert.Equal(t, "C1", form.Get("channel"))
	assert.Equal(t, "200.1", form.Get("ts"))
	assert.Equal(t, "⏳ still going", form.Get("text"))
}

// Only the first part of a split message is ever edited, which is what the placeholder is.
func TestEditSendsOnlyTheFirstPart(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("chat.update", `{"ok":true,"channel":"C1","ts":"200.1"}`)
	s := testSource(t, f)

	head := strings.Repeat("a", MaxMessageChars)
	require.NoError(t, s.Edit(t.Context(), Ref("C1", "100.1", "100.1"), "200.1",
		conductor.Outbound{Type: conductor.OutFinal, Text: head + "\n" + strings.Repeat("b", 10)}))

	assert.Equal(t, head, f.form(t, "chat.update", 0).Get("text"))
}

func TestEditRefusesWithNoMessageToEdit(t *testing.T) {
	f := newFakeSlack(t)
	s := testSource(t, f)

	require.Error(t, s.Edit(t.Context(), Ref("C1", "100.1", "100.1"), "",
		conductor.Outbound{Text: "hello"}))
	assert.Empty(t, f.methods())
}

// A thread longer than one page is read to the end, and the cursor Slack hands back is the
// one the next call carries.
func TestFetchTranscriptPagesUntilSlackStops(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("conversations.replies", `{"ok":true,"has_more":true,
		"messages":[{"user":"U1","ts":"100.1","text":"first"}],
		"response_metadata":{"next_cursor":"page2"}}`)
	f.reply("conversations.replies", `{"ok":true,
		"messages":[{"user":"U1","ts":"100.2","text":"second"}]}`)
	f.reply("users.info", `{"ok":true,"user":{"id":"U1","name":"alice","profile":{"display_name":"alice"}}}`)
	s := testSource(t, f)

	entries, err := s.FetchTranscript(t.Context(), Ref("C1", "100.1", "100.2"))

	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "first", entries[0].Text)
	assert.Equal(t, "second", entries[1].Text)

	first := f.form(t, "conversations.replies", 0)
	assert.Equal(t, "C1", first.Get("channel"))
	assert.Equal(t, "100.1", first.Get("ts"), "the thread's parent, not the trigger")
	assert.Empty(t, first.Get("cursor"))
	assert.Equal(t, "page2", f.form(t, "conversations.replies", 1).Get("cursor"))
}

// The bot's own scaffolding is not conversation: the placeholder and every progress edit
// are dropped, and what it actually said is kept as the assistant.
func TestFetchTranscriptDropsTheBotsOwnScaffolding(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("conversations.replies", `{"ok":true,"messages":[
		{"user":"U1","ts":"100.1","text":"<@UBOT> what does this repo do?"},
		{"user":"UBOT","bot_id":"BBOT","ts":"100.2","text":"👀 working…"},
		{"user":"UBOT","bot_id":"BBOT","ts":"100.3","text":"⏳ reading the tree"},
		{"user":"UBOT","bot_id":"BBOT","ts":"100.4","text":"It is a task runner."},
		{"user":"U2","ts":"100.5","text":"","subtype":"channel_join"},
		{"user":"U1","ts":"100.6","text":"and the node?"}]}`)
	f.reply("users.info", `{"ok":true,"user":{"id":"U1","name":"alice","profile":{"display_name":"alice"}}}`)
	s := testSource(t, f)

	entries, err := s.FetchTranscript(t.Context(), Ref("C1", "100.1", "100.6"))

	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, conductor.RoleUser, entries[0].Role)
	assert.Equal(t, "what does this repo do?", entries[0].Text, "the mention is stripped")
	assert.Equal(t, "alice", entries[0].Author)
	assert.Equal(t, conductor.RoleAssistant, entries[1].Role)
	assert.Equal(t, "It is a task runner.", entries[1].Text)
	assert.Equal(t, conductor.RoleUser, entries[2].Role)
	assert.Equal(t, "and the node?", entries[2].Text)
}

// Somebody else's mention reaches the model as their name, not their ID.
func TestFetchTranscriptResolvesUserMentions(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("conversations.replies", `{"ok":true,"messages":[
		{"user":"U1","ts":"100.1","text":"<@UBOT> add <@U2>’s suggestion, and <@U3|carol>’s"}]}`)
	// The text is resolved before the author, so U2's answer comes first.
	f.reply("users.info", `{"ok":true,"user":{"id":"U2","name":"bob","profile":{"display_name":"Bob"}}}`)
	f.reply("users.info", `{"ok":true,"user":{"id":"U1","name":"alice","profile":{"display_name":"alice"}}}`)
	s := testSource(t, f)

	entries, err := s.FetchTranscript(t.Context(), Ref("C1", "100.1", "100.1"))

	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "add @Bob’s suggestion, and @carol’s", entries[0].Text)
}

// A name the agent was shown can be used to tag that person back.
func TestLinkMentionsTagsAResolvedName(t *testing.T) {
	s := &Source{users: map[string]string{}}
	s.mu.Lock()
	s.rememberLocked("Bob", "U2")
	s.rememberLocked("Ana", "U3")
	s.rememberLocked("Ana Maria", "U4")
	s.rememberLocked("Sam", "U5")
	s.rememberLocked("Sam", "U6")
	s.mu.Unlock()

	assert.Equal(t, "ok <@U2>, done", s.linkMentions("ok @Bob, done"))
	assert.Equal(t, "<@U2> <@U3>", s.linkMentions("@Bob @Ana"))
	assert.Equal(t, "cc <@U4>", s.linkMentions("cc @Ana Maria"))
	// Not a whole name, an email, or a name two people share: left as text.
	assert.Equal(t, "@Bobby bob@Bob @Sam", s.linkMentions("@Bobby bob@Bob @Sam"))
}

// The label form of a mention is enough to learn the name, with no users.info call.
func TestResolveMentionsRemembersALabel(t *testing.T) {
	s := &Source{users: map[string]string{}}
	assert.Equal(t, "ask @carol", s.resolveMentions(t.Context(), "ask <@U3|carol>"))
	assert.Equal(t, "thanks <@U3>", s.linkMentions("thanks @carol"))
}

// One author costs one users.info however many times they spoke: the name cache is what
// keeps a long thread from spending a call per message.
func TestFetchTranscriptResolvesAnAuthorOnce(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("conversations.replies", `{"ok":true,"messages":[
		{"user":"U1","ts":"100.1","text":"one"},
		{"user":"U1","ts":"100.2","text":"two"},
		{"user":"U1","ts":"100.3","text":"three"}]}`)
	f.reply("users.info", `{"ok":true,"user":{"id":"U1","name":"alice","profile":{"display_name":"alice"}}}`)
	s := testSource(t, f)

	entries, err := s.FetchTranscript(t.Context(), Ref("C1", "100.1", "100.3"))

	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, []string{"conversations.replies", "users.info"}, f.methods())
	for _, e := range entries {
		assert.Equal(t, "alice", e.Author)
	}
}

// A name Slack will not resolve is not worth failing a turn over; the raw id is a worse
// transcript than a display name and a much better one than no transcript.
func TestFetchTranscriptFallsBackToTheUserID(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("conversations.replies", `{"ok":true,"messages":[{"user":"U1","ts":"100.1","text":"one"}]}`)
	f.reply("users.info", `{"ok":false,"error":"user_not_found"}`)
	s := testSource(t, f)

	entries, err := s.FetchTranscript(t.Context(), Ref("C1", "100.1", "100.1"))

	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "U1", entries[0].Author)
}

// A thread that cannot be read is the turn's problem to report, not to paper over.
func TestFetchTranscriptReportsARefusal(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("conversations.replies", `{"ok":false,"error":"channel_not_found"}`)
	s := testSource(t, f)

	_, err := s.FetchTranscript(t.Context(), Ref("C1", "100.1", "100.1"))
	require.ErrorContains(t, err, "channel_not_found")
}

func TestASlackEmailBecomesTheSpeakersLogin(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("users.info", `{"ok":true,"user":{"id":"U1","team_id":"T1","name":"alice","is_email_confirmed":true,"profile":{"display_name":"Alice","email":"Alice@Acme.com","image_72":"https://a/72.png"}}}`)
	s := testSource(t, f)
	s.botTeamID = "T1"

	assert.Equal(t, "Alice", s.displayName(t.Context(), "U1"))
	acct := s.account(t.Context(), "U1")
	assert.Equal(t, "alice@acme.com", acct.login)
	assert.Equal(t, "T1/U1", acct.externalID)
	assert.Equal(t, "https://a/72.png", acct.avatar)
}

// Every way somebody could carry an email on our domain without being one of us is refused.
// A refusal is an empty login, never an error: the message is still heard and mirrored.
func TestVerifiedEmailRefusesAnybodyWhoCouldBePretending(t *testing.T) {
	member := func() *slack.User {
		u := &slack.User{ID: "U1", TeamID: "T1", IsEmailConfirmed: true}
		u.Profile.Email = "ada@acme.com"
		return u
	}
	require.Equal(t, "ada@acme.com", verifiedEmail(member(), "T1", ""))

	for name, mutate := range map[string]func(*slack.User){
		"another workspace":    func(u *slack.User) { u.TeamID = "T2" },
		"slack connect":        func(u *slack.User) { u.IsStranger = true },
		"multi-channel guest":  func(u *slack.User) { u.IsRestricted = true },
		"single-channel guest": func(u *slack.User) { u.IsUltraRestricted = true },
		"deactivated":          func(u *slack.User) { u.Deleted = true },
		"bot":                  func(u *slack.User) { u.IsBot = true },
		"app user":             func(u *slack.User) { u.IsAppUser = true },
		"unconfirmed email":    func(u *slack.User) { u.IsEmailConfirmed = false },
		"no email":             func(u *slack.User) { u.Profile.Email = "" },
	} {
		u := member()
		mutate(u)
		assert.Empty(t, verifiedEmail(u, "T1", ""), name)
	}
	// A bot that has not learned its own workspace proves nobody.
	assert.Empty(t, verifiedEmail(member(), "", ""))
	// Enterprise Grid: another workspace of the same org is the same company.
	grid := member()
	grid.TeamID = "T9"
	grid.Enterprise.EnterpriseID = "E1"
	assert.Equal(t, "ada@acme.com", verifiedEmail(grid, "T1", "E1"))
	assert.Empty(t, verifiedEmail(grid, "T1", "E2"))
}

// PODIUM_SLACK_TEAM_IDS adds workspaces and Grid orgs whose members may match, and nothing
// else: a guest or a Slack Connect stranger in a listed workspace is still nobody.
func TestVerifiedEmailAcceptsTheListedTeams(t *testing.T) {
	member := func(team, enterprise string) *slack.User {
		u := &slack.User{ID: "U1", TeamID: team, IsEmailConfirmed: true}
		u.Profile.Email = "ada@acme.com"
		u.Enterprise.EnterpriseID = enterprise
		return u
	}
	assert.Equal(t, "ada@acme.com", verifiedEmail(member("T2", ""), "T1", "", "T2"), "a listed workspace")
	assert.Equal(t, "ada@acme.com", verifiedEmail(member("T7", "E5"), "T1", "", "E5"), "a listed Grid org")
	assert.Empty(t, verifiedEmail(member("T3", ""), "T1", "", "T2"), "an unlisted workspace")
	assert.Equal(t, "ada@acme.com", verifiedEmail(member("T2", ""), "", "", "T2"),
		"the list alone is enough when auth.test has not run")
	assert.Empty(t, verifiedEmail(member("T2", ""), "", "", ""), "an empty entry matches nothing")

	guest := member("T2", "")
	guest.IsRestricted = true
	assert.Empty(t, verifiedEmail(guest, "T1", "", "T2"), "a guest in a listed workspace")
	stranger := member("T2", "")
	stranger.IsStranger = true
	assert.Empty(t, verifiedEmail(stranger, "T1", "", "T2"), "Slack Connect in a listed workspace")
}

// The account check is asked again once accountTTL has passed: a member who becomes a guest
// stops matching without a restart.
func TestAccountIsCheckedAgainAfterItsTTL(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("users.info", `{"ok":true,"user":{"id":"U1","team_id":"T1","is_restricted":true,"is_email_confirmed":true,"profile":{"email":"ada@acme.com"}}}`)
	s := testSource(t, f)
	s.botTeamID = "T1"
	s.accounts = map[string]slackAccount{"U1": {login: "ada@acme.com", externalID: "T1/U1", at: time.Now().Add(-accountTTL)}}

	assert.Empty(t, s.account(t.Context(), "U1").login)
}

func TestPodiumLoginRejectsAnythingButAnEmail(t *testing.T) {
	assert.Equal(t, "ada@acme.com", podiumLogin(" Ada@Acme.com "))
	assert.Empty(t, podiumLogin("U123"))
	assert.Empty(t, podiumLogin(""))
	assert.Empty(t, podiumLogin("ada@"))
	assert.Empty(t, podiumLogin("@acme.com"))
	assert.Empty(t, podiumLogin("ada@@acme.com"))
}

// The modern upload is three steps: ask for a URL, put the bytes there, then share it into
// the thread. The share is the step that has to name the channel and the thread.
func TestAttachUploadsAndSharesIntoTheThread(t *testing.T) {
	f := newFakeSlack(t)
	f.reply("files.getUploadURLExternal",
		`{"ok":true,"upload_url":"`+f.server.URL+`/upload","file_id":"F1"}`)
	f.reply("files.completeUploadExternal", `{"ok":true,"files":[{"id":"F1","title":"shot.png"}]}`)
	s := testSource(t, f)

	err := s.Attach(t.Context(), Ref("C1", "100.1", "100.9"), conductor.Attachment{
		Name:        "shot.png",
		ContentType: "image/png",
		Size:        4,
		Body:        strings.NewReader("data"),
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"files.getUploadURLExternal", "upload", "files.completeUploadExternal"},
		f.methods())
	assert.Equal(t, "shot.png", f.form(t, "files.getUploadURLExternal", 0).Get("filename"))
	assert.Equal(t, "4", f.form(t, "files.getUploadURLExternal", 0).Get("length"))
	share := f.form(t, "files.completeUploadExternal", 0)
	assert.Equal(t, "C1", share.Get("channel_id"))
	assert.Equal(t, "100.1", share.Get("thread_ts"))
}

// Slack's upload flow declares the length before the bytes move, so an attachment that
// cannot say how big it is uploads nothing at all — silently, if it were not refused here.
func TestAttachRefusesWhatSlackCannotBeTold(t *testing.T) {
	body := func() io.Reader { return strings.NewReader("data") }
	for _, tc := range []struct {
		name string
		file conductor.Attachment
	}{
		{"no name", conductor.Attachment{Size: 4, Body: body()}},
		{"no size", conductor.Attachment{Name: "shot.png", Body: body()}},
		{"no body", conductor.Attachment{Name: "shot.png", Size: 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSlack(t)
			s := testSource(t, f)

			require.Error(t, s.Attach(t.Context(), Ref("C1", "100.1", "100.1"), tc.file))
			assert.Empty(t, f.methods(), "nothing should reach Slack")
		})
	}
}

func takeInbound(t *testing.T, s *Source) conductor.InboundEvent {
	t.Helper()
	select {
	case ev := <-s.Events():
		return ev
	default:
		t.Fatal("expected an inbound event")
		return conductor.InboundEvent{}
	}
}

func assertNoInbound(t *testing.T, s *Source) {
	t.Helper()
	select {
	case ev := <-s.Events():
		t.Fatalf("unexpected inbound event: %+v", ev)
	default:
	}
}
