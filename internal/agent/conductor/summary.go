package conductor

// A conversation's summary.
//
// A turn is handed the conversation so far, and a long one costs more with every turn: the
// whole history is input again each time, and past MaxBriefBytes its oldest part is simply
// cut. So a conversation nobody has written to for summaryIdle is summarised — on the chat
// host, through the same gateway as its turns — and from then on a turn gets the summary in
// place of everything it covers, and the recent messages after it as they were.
//
// The summary is a convenience, never the record: the source still holds every message, and a
// summary that cannot be made or read leaves turns exactly as they were.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/chathost"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
)

const (
	// summaryIdle is how long a conversation is left before it is summarised.
	summaryIdle = 10 * time.Minute
	// summaryKeep is how many of the latest messages stay word for word.
	summaryKeep = 8
	// summaryMin is the fewest messages worth summarising: below it the transcript is short.
	summaryMin = summaryKeep + 6
	// summaryLocation is the chat host location summaries are written in.
	summaryLocation = "summaries"
	summarySystem   = `You condense a conversation between people and an assistant so the assistant can pick it ` +
		`up later without the full history. Write a summary of at most 300 words: what was asked, ` +
		`what was decided, what was done (tasks started, their outcome, pull requests, links), and ` +
		`anything still open. Keep names, numbers, paths and identifiers exactly. No preamble.`
)

// sessionSummary is a summary as stored: the text, how many of the oldest messages it covers,
// and the timestamp of the last of them exactly as the source wrote it. The count is what
// places it — a source's timestamps are only to the second, and a conversation says several
// things in one — and the timestamp is how a transcript that changed since is noticed.
type sessionSummary struct {
	Text      string `json:"text"`
	Covers    int    `json:"covers"`
	ThroughTS string `json:"through_ts"`
}

func summaryKey(sessionID string) string { return "session.summary." + sessionID }

// withSummary is entries with the conversation's summary in place of what it covers. Without
// a summary, or with one that cannot be read, entries are returned as they were.
func (c *Conductor) withSummary(ctx context.Context, sessionID string, entries []BriefEntry) []BriefEntry {
	head, covers, ok := c.summaryFor(ctx, sessionID, entries)
	if !ok {
		return entries
	}
	return append([]BriefEntry{head}, entries[covers:]...)
}

// withSummaryLines is withSummary for transcript lines, keeping what the budget needs to know
// about each. The summary itself is always kept: it is the only trace of what it covers.
func (c *Conductor) withSummaryLines(ctx context.Context, sessionID string, lines []transcriptLine) []transcriptLine {
	head, covers, ok := c.summaryFor(ctx, sessionID, entriesOf(lines))
	if !ok {
		return lines
	}
	return append([]transcriptLine{{BriefEntry: head, keep: true}}, lines[covers:]...)
}

// summaryFor is the conversation's summary as an entry and how many of entries it stands in
// for, or false when there is none that lines up with entries.
func (c *Conductor) summaryFor(ctx context.Context, sessionID string, entries []BriefEntry) (BriefEntry, int, bool) {
	if c.store == nil || sessionID == "" {
		return BriefEntry{}, 0, false
	}
	var s sessionSummary
	if err := c.store.GetSetting(ctx, summaryKey(sessionID), &s); err != nil || s.Text == "" || s.Covers <= 0 {
		return BriefEntry{}, 0, false
	}
	// A transcript that no longer lines up with what was summarised — a message deleted or
	// edited away — is handed whole rather than with a summary of something else.
	if len(entries) < s.Covers || entries[s.Covers-1].TS != s.ThroughTS {
		return BriefEntry{}, 0, false
	}
	return BriefEntry{
		Role:   "assistant",
		Author: "summary",
		TS:     s.ThroughTS,
		Text:   "Summary of this conversation up to here: " + s.Text,
	}, s.Covers, true
}

// noteChatTurn records where a conversation's history is read from, so it can be summarised
// once it goes quiet.
func (c *Conductor) noteChatTurn(sessionID string, src Source, ref string) {
	c.chat.mu.Lock()
	defer c.chat.mu.Unlock()
	if cv := c.chat.byID[sessionID]; cv != nil {
		cv.src, cv.ref, cv.summarised = src, ref, false
	}
}

// summarise writes summaries for the conversations that have gone quiet. It runs beside the
// reaper, once a minute.
func (c *Conductor) summarise(ctx context.Context) {
	c.chat.mu.Lock()
	var due []*chatConv
	for _, cv := range c.chat.byID {
		if cv.turn == nil && !cv.summarised && cv.src != nil && time.Since(cv.lastUsed) > summaryIdle {
			cv.summarised = true
			due = append(due, cv)
		}
	}
	c.chat.mu.Unlock()
	for _, cv := range due {
		if err := c.summariseOne(ctx, cv); err != nil && ctx.Err() == nil {
			c.logger.WarnContext(ctx, "summarising a conversation failed; its turns keep the whole history",
				"session_id", cv.sessionID, "error", err)
		}
	}
}

func (c *Conductor) summariseOne(ctx context.Context, cv *chatConv) error {
	// The same transcript a turn reads, so the count the summary covers lines up with it.
	sourceKey := ""
	if c.store != nil {
		if sess, err := c.store.GetSession(ctx, cv.sessionID); err == nil {
			sourceKey = sess.SourceKey
		}
	}
	lines, err := c.transcriptFor(ctx, cv.src, cv.ref, sourceKey)
	if err != nil {
		return err
	}
	entries := entriesOf(lines)
	if len(entries) < summaryMin {
		return nil
	}
	// The summary so far stands in for what it covers, so a summary is of the summary and
	// what came after it: each one is short, however long the conversation gets.
	covers := len(entries) - summaryKeep
	old := c.withSummary(ctx, cv.sessionID, entries[:covers])
	var b strings.Builder
	for _, e := range old {
		fmt.Fprintf(&b, "[%s] %s (%s): %s\n", e.TS, e.Author, e.Role, e.Text)
	}
	text, err := c.chatComplete(ctx, cv.sessionID, summarySystem, b.String())
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("the summary came back empty")
	}
	return c.store.PutSetting(ctx, summaryKey(cv.sessionID), sessionSummary{
		Text: strings.TrimSpace(text), Covers: covers, ThroughTS: entries[covers-1].TS,
	})
}

// chatComplete is one model call on the chat host with no tools, through the gateway, on the
// assistant's own backend. It is for the conductor's own housekeeping, never a person's turn,
// and what it cost is recorded against sessionID's conversation as a summary.
func (c *Conductor) chatComplete(ctx context.Context, sessionID, system, prompt string) (string, error) {
	return c.chatCompleteIn(ctx, summaryLocation, sessionID, store.UsageKindSummary, system, prompt)
}

// chatCompleteIn is chatComplete in a location of its own, so two kinds of housekeeping do not
// wait on each other's one turn. purpose names the spend on the usage screen.
func (c *Conductor) chatCompleteIn(ctx context.Context, location, sessionID, purpose, system, prompt string) (string, error) {
	choice := c.profiles.Current().ResolveAssistant(profiles.Override{})
	provider := c.providerFor(choice.Agent, "")
	started := time.Now()
	cv, err := c.chatConvFor(location)
	if err != nil {
		return "", err
	}
	h := &hostRun{provider: provider}
	loc, err := c.chatLocation(ctx, cv, h)
	if err != nil {
		return "", err
	}
	c.chat.mu.Lock()
	cv.turn = &chatTurn{id: "summary"}
	c.chat.mu.Unlock()
	defer func() {
		c.chat.mu.Lock()
		cv.turn = nil
		cv.lastUsed = time.Now()
		c.chat.mu.Unlock()
	}()
	if err := c.chatHost.Open(ctx, loc); err != nil {
		return "", err
	}
	res, err := c.chatHost.Answer(ctx, chathost.Turn{
		Location: cv.location,
		Model:    chathost.Model{Provider: provider.ID, ID: choice.Model},
		System:   system,
		Prompt:   prompt,
	})
	if err != nil {
		return "", err
	}
	c.recordHousekeeping(ctx, sessionID, purpose, started, choice, h, res)
	if res.Outcome == "failed" {
		return "", fmt.Errorf("the model failed: %s", res.Error)
	}
	return res.Text, nil
}

// recordHousekeeping writes what one housekeeping call cost. It used to be nowhere: a summary
// is a model call every few minutes per quiet conversation, and the usage screen's total was
// short by all of them. Recording is best effort: the summary matters more than its bill.
func (c *Conductor) recordHousekeeping(
	ctx context.Context, sessionID, purpose string, started time.Time,
	choice profiles.Choice, h *hostRun, res chathost.Result,
) {
	if c.store == nil || sessionID == "" {
		return
	}
	status := store.TurnSucceeded
	if res.Outcome == "failed" {
		status = store.TurnFailed
	}
	steps, cost := res.Steps, res.CostUSD
	err := c.store.RecordHousekeeping(ctx, store.Housekeeping{
		SessionID:  sessionID,
		Purpose:    purpose,
		Status:     status,
		StartedAt:  started,
		FinishedAt: time.Now(),
		NumTurns:   &steps,
		CostUSD:    &cost,
		Backend: store.Backend{
			Agent: choice.Agent, Model: choice.Model, Effort: choice.Effort, Provider: h.provider.ID,
		},
		Credential: c.credentialFor(h.provider.ID, "", c.claudeAgentTurn(h)),
		Tokens:     storeTokens(res.Tokens),
	})
	if err != nil {
		c.logger.WarnContext(ctx, "recording what a housekeeping call cost failed",
			"session_id", sessionID, "purpose", purpose, "error", err)
	}
}
