package conductor

// The transcript a turn is handed.
//
// Phase 3 of docs/agent.md makes chat_messages the one record of every
// conversation, whichever surface it lives on: a Slack thread, a pull request and a web chat
// are all read back from the same table, so a turn sees what was said in Podium beside what
// was said in the thread, labelled with who said it and where. A source's own FetchTranscript
// is only the seed for a conversation with no record yet — one Podium started hearing before
// it kept a record, or a store that is not there (tests, a database-less dev run).
//
// The record is also bigger than the old transcript ever was: it keeps every reply in a
// thread, not only the ones put to Podium. So the transcript has a budget. Past it, the
// messages that matter to the turn — everything put to Podium, its answers, and the latest
// few word for word — are kept, and the side conversation that was never addressed is
// condensed into one entry by a Summarizer. Brief.Encode's own cut (oldest first, to fit the
// environment variable) still applies after this; the budget is what keeps the cut from
// landing on the question the turn is answering.

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/podium-ade/podium/internal/agent/store"
)

const (
	// DefaultTranscriptBudgetBytes is how much text a turn's transcript holds before old
	// unaddressed chatter is condensed. It is under half of MaxBriefBytes' 72 KiB of JSON, so
	// the prompts, the instruction and the condensed entry still fit beside it.
	DefaultTranscriptBudgetBytes = 32 * 1024
	// DefaultTranscriptKeepRecent is how many of the latest messages are always kept word for
	// word. A conversation's end is what the turn is answering.
	DefaultTranscriptKeepRecent = 20
	// transcriptEntryOverhead approximates what one entry costs beyond its text: the JSON
	// keys, the role, the timestamp. The budget is a cost knob, not a byte-exact limit.
	transcriptEntryOverhead = 64
)

// transcriptLine is one entry with what the budget needs to know about it.
type transcriptLine struct {
	BriefEntry
	// addressed is a human message put to Podium. An answer is the bot's reply to one.
	addressed bool
	// keep forces the line through the budget: the summary of what came before.
	keep bool
}

// Summarizer condenses the messages a budget leaves out into one entry's text, at most
// limit bytes. It is an interface so a model-backed one can replace the extractive default;
// the conductor's model path (summary.go's chatComplete) runs on the chat host, which a turn
// is not always placed on, and a turn must not wait on a second model call to start.
type Summarizer interface {
	Condense(dropped []BriefEntry, limit int) string
}

// extractiveSummarizer is the default Summarizer: who said how much, and the opening words of
// each message, newest last, cut to fit. It makes no model call and is the same every time,
// which is what a brief that is rebuilt on every turn wants.
type extractiveSummarizer struct{}

const summaryLineRunes = 140

func (extractiveSummarizer) Condense(dropped []BriefEntry, limit int) string {
	if len(dropped) == 0 || limit <= 0 {
		return ""
	}
	counts := map[string]int{}
	var order []string
	for _, e := range dropped {
		if counts[e.Author] == 0 {
			order = append(order, e.Author)
		}
		counts[e.Author]++
	}
	var who []string
	for _, a := range order {
		who = append(who, fmt.Sprintf("%s ×%d", a, counts[a]))
	}
	head := fmt.Sprintf("%d earlier messages that were not put to Podium, condensed (%s):",
		len(dropped), strings.Join(who, ", "))
	var b strings.Builder
	b.WriteString(head)
	// The newest condensed lines are the most likely to matter, so when the limit cuts,
	// it cuts from the front.
	lines := make([]string, 0, len(dropped))
	for _, e := range dropped {
		text := strings.Join(strings.Fields(e.Text), " ")
		lines = append(lines, fmt.Sprintf("\n- %s: %s", e.Author, truncateRunes(text, summaryLineRunes)))
	}
	room := limit - b.Len()
	start := len(lines)
	for start > 0 && room-len(lines[start-1]) >= 0 {
		start--
		room -= len(lines[start])
	}
	if start > 0 {
		fmt.Fprintf(&b, "\n- (%d older not shown)", start)
	}
	for _, l := range lines[start:] {
		b.WriteString(l)
	}
	return b.String()
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// transcriptFor is the conversation behind sourceKey as a turn reads it: the record when
// there is one, and the source's own transcript when there is not.
func (c *Conductor) transcriptFor(ctx context.Context, src Source, ref, sourceKey string) ([]transcriptLine, error) {
	if c.store != nil && sourceKey != "" {
		chat, err := c.store.ChatBySourceKey(ctx, sourceKey)
		if err == nil {
			msgs, err := c.store.ListChatMessages(ctx, chat.ID, 0)
			if err != nil {
				return nil, err
			}
			lines := recordLines(msgs, chat, c.botName())
			if len(lines) > 0 {
				if chat.Origin == SourceGitHub && src != nil {
					// A pull request's record starts the first time Podium heard it, which
					// can be long after the conversation started; its history is not seeded
					// the way a Slack thread's is. What GitHub holds from before the first
					// recorded message is read and put in front, once per turn, which is no
					// more than the turn read before the record existed.
					lines = c.withEarlierHistory(ctx, src, ref, lines)
				}
				return lines, nil
			}
		}
	}
	if src == nil {
		return nil, nil
	}
	entries, err := src.FetchTranscript(ctx, ref)
	if err != nil {
		return nil, err
	}
	lines := make([]transcriptLine, 0, len(entries))
	for _, e := range entries {
		// A source's transcript is what it chose to show a turn, which has only ever been
		// what was put to the bot and what it said back.
		lines = append(lines, transcriptLine{BriefEntry: e, addressed: e.Role == RoleUser})
	}
	return lines, nil
}

func (c *Conductor) withEarlierHistory(ctx context.Context, src Source, ref string, lines []transcriptLine) []transcriptLine {
	first, err := time.Parse(time.RFC3339, lines[0].TS)
	if err != nil {
		return lines
	}
	entries, err := src.FetchTranscript(ctx, ref)
	if err != nil {
		c.logger.WarnContext(ctx, "reading a pull request's history from before its record failed; "+
			"the turn has the record only", "ref", ref, "error", err)
		return lines
	}
	var older []transcriptLine
	for _, e := range entries {
		ts, err := time.Parse(time.RFC3339, e.TS)
		if err != nil || !ts.Before(first) {
			continue
		}
		older = append(older, transcriptLine{BriefEntry: e, addressed: e.Role == RoleUser})
	}
	return append(older, lines...)
}

// botName is how the bot's own words are attributed in a transcript.
func (c *Conductor) botName() string {
	if c.profiles != nil {
		if p := c.profiles.Current(); p != nil && p.DisplayName != "" {
			return p.DisplayName
		}
	}
	return "Podium"
}

// recordLines turns a chat's record into transcript lines. Progress and activity are left out
// — a turn's half-finished thoughts are not history, the same rule the web chat always had —
// and so is anything deleted on its surface: the record keeps the text, the model never sees
// it. An edited message is read as it stands now.
func recordLines(msgs []store.ChatMessage, chat store.Chat, bot string) []transcriptLine {
	out := make([]transcriptLine, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == store.RoleProgress || m.Role == store.RoleActivity || m.DeletedAt != nil {
			continue
		}
		line := transcriptLine{BriefEntry: BriefEntry{
			Role: m.Role,
			TS:   BriefTimestamp(m.TS),
			Text: m.Text,
		}}
		if m.Role == store.RoleUser {
			line.Author = recordAuthor(m, chat)
			line.addressed = m.Addressed
		} else {
			line.Author = bot
		}
		out = append(out, line)
	}
	return out
}

// recordAuthor names a human message's author for the model: who, where, and whether they
// are in Podium. "Dana (slack, not in Podium)" is how a turn knows that a request in the
// transcript came from somebody it does not act for. A web message carries no origin label,
// because the web chat is where the record lives.
func recordAuthor(m store.ChatMessage, chat store.Chat) string {
	name := strings.TrimSpace(m.Author)
	if name == "" {
		name = m.AuthorLogin
	}
	if name == "" && (m.Via == "" || m.Via == store.ViaWeb) {
		// A web message from before authors were recorded is the chat's owner's.
		name = chat.Login
	}
	if name == "" {
		name = "someone"
	}
	var tags []string
	if m.Via != "" && m.Via != store.ViaWeb {
		tags = append(tags, m.Via)
		if m.AuthorLogin == "" {
			tags = append(tags, "not in Podium")
		}
	}
	if len(tags) == 0 {
		return name
	}
	return name + " (" + strings.Join(tags, ", ") + ")"
}

// budgetTranscript fits lines to budget bytes. Under budget, every line goes through as it
// is. Over it, a line is kept when it is one of the latest keepRecent, put to Podium, an
// answer to something put to Podium, or forced (the summary); everything else is handed to
// the summarizer and comes back as one entry where the first of them was.
func budgetTranscript(lines []transcriptLine, budget, keepRecent int, s Summarizer) []BriefEntry {
	size := 0
	for _, l := range lines {
		size += lineSize(l)
	}
	if budget <= 0 || size <= budget {
		return entriesOf(lines)
	}
	keep := make([]bool, len(lines))
	answering := false
	for i, l := range lines {
		switch {
		case l.keep || i >= len(lines)-keepRecent:
			keep[i] = true
		case l.Role == RoleUser:
			keep[i] = l.addressed
		default:
			keep[i] = answering
		}
		// The bot's lines after an addressed message, up to the next human one, are its answer.
		if l.Role == RoleUser {
			answering = l.addressed
		}
	}
	var dropped []BriefEntry
	at := -1
	kept := 0
	for i, l := range lines {
		if keep[i] {
			kept += lineSize(l)
			continue
		}
		if at < 0 {
			at = i
		}
		dropped = append(dropped, l.BriefEntry)
	}
	if len(dropped) == 0 {
		return entriesOf(lines)
	}
	// The condensed entry gets what the kept lines leave of the budget, and never less than
	// a little: a turn told "there was a side conversation" knows to ask about it.
	room := max(budget-kept-transcriptEntryOverhead, 1024)
	text := s.Condense(dropped, room)
	out := make([]BriefEntry, 0, len(lines)-len(dropped)+1)
	for i, l := range lines {
		if i == at && text != "" {
			out = append(out, BriefEntry{
				Role:   RoleUser,
				Author: "earlier conversation",
				TS:     dropped[0].TS,
				Text:   text,
			})
		}
		if keep[i] {
			out = append(out, l.BriefEntry)
		}
	}
	return out
}

func lineSize(l transcriptLine) int {
	return len(l.Text) + len(l.Author) + transcriptEntryOverhead
}

func entriesOf(lines []transcriptLine) []BriefEntry {
	out := make([]BriefEntry, len(lines))
	for i, l := range lines {
		out[i] = l.BriefEntry
	}
	return out
}

// turnTranscript is the transcript one turn is handed: the record (or the source's seed), the
// session's summary in place of what it covers, and the budget.
func (c *Conductor) turnTranscript(ctx context.Context, src Source, ref string, sess store.Session) ([]BriefEntry, error) {
	lines, err := c.transcriptFor(ctx, src, ref, sess.SourceKey)
	if err != nil {
		return nil, err
	}
	lines = c.withSummaryLines(ctx, sess.ID, lines)
	return budgetTranscript(lines, c.transcriptBudgetBytes(), c.transcriptKeepRecent(), c.summarizerOrDefault()), nil
}

func (c *Conductor) transcriptBudgetBytes() int {
	if c.transcriptBudget > 0 {
		return c.transcriptBudget
	}
	return DefaultTranscriptBudgetBytes
}

func (c *Conductor) transcriptKeepRecent() int {
	if c.transcriptKeep > 0 {
		return c.transcriptKeep
	}
	return DefaultTranscriptKeepRecent
}

func (c *Conductor) summarizerOrDefault() Summarizer {
	if c.summarizer != nil {
		return c.summarizer
	}
	return extractiveSummarizer{}
}
