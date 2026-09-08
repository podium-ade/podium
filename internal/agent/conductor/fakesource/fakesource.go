// Package fakesource is an in-memory conductor.Source: a channel of inbound events and an
// ordered record of everything the conductor said back. The conductor's own tests drive it
// directly, and the TEST-ONLY dev source (internal/agent/api) wraps it in two HTTP routes so
// an end-to-end test can inject a message and read the whole conversation back without a
// Slack workspace.
package fakesource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
)

// Record is one thing the conductor did.
type Record struct {
	Seq         int64  `json:"seq"`
	Action      string `json:"action"`
	Ref         string `json:"ref"`
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	MessageID   string `json:"message_id,omitempty"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Reaction    string `json:"reaction,omitempty"`
}

// The actions a Record can carry.
const (
	ActionPost   = "post"
	ActionEdit   = "edit"
	ActionAttach = "attach"
	ActionReact  = "react"
	// ActionLinkPullRequest is a pull request the turn's answer named. Text is its URL.
	ActionLinkPullRequest = "link_pull_request"
)

// Source is the in-memory source.
type Source struct {
	kind   string
	events chan conductor.InboundEvent

	mu sync.Mutex
	// transcript is what has been said in each ref, in order: the injected messages and
	// the conductor's own finals.
	transcript map[string][]conductor.BriefEntry
	// pulls is every pull request linked to a ref, which the web chat would have stored.
	pulls   map[string][]conductor.PullRequest
	records []Record
	// mirror makes this a source whose conversation lives somewhere else, so the conductor
	// keeps a readable copy of it. Off by default: the web chat must NOT be mirrored,
	// because it already stores its own messages and a second writer would double them.
	mirror  bool
	seq     int64
	nextMsg int64
	closed  bool
}

var _ conductor.Source = (*Source)(nil)

// New returns a source announcing itself as kind.
func New(kind string) *Source {
	return &Source{
		kind:       kind,
		events:     make(chan conductor.InboundEvent, 32),
		transcript: map[string][]conductor.BriefEntry{},
		pulls:      map[string][]conductor.PullRequest{},
	}
}

// Kind implements conductor.Source.
func (s *Source) Kind() string { return s.kind }

// Events implements conductor.Source.
func (s *Source) Events() <-chan conductor.InboundEvent { return s.events }

// Close stops the source. It is idempotent.
func (s *Source) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.events)
}

// Send offers one inbound event and records it in the ref's transcript, which is what makes
// a second message show up as history in the next turn's brief.
func (s *Source) Send(ctx context.Context, ev conductor.InboundEvent) error {
	s.mu.Lock()
	s.transcript[ev.Ref] = append(s.transcript[ev.Ref], conductor.BriefEntry{
		Role:   conductor.RoleUser,
		Author: ev.Author,
		TS:     conductor.BriefTimestamp(ev.TS),
		Text:   ev.Text,
	})
	s.mu.Unlock()

	select {
	case s.events <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FetchTranscript implements conductor.Source.
func (s *Source) FetchTranscript(_ context.Context, ref string) ([]conductor.BriefEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]conductor.BriefEntry(nil), s.transcript[ref]...), nil
}

// Post implements conductor.Source. A final joins the transcript; a placeholder and a
// progress line do not, exactly as the Slack source leaves out its own noise.
func (s *Source) Post(_ context.Context, ref string, out conductor.Outbound) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextMsg++
	id := "msg-" + strconv.FormatInt(s.nextMsg, 10)
	s.record(Record{Action: ActionPost, Ref: ref, Type: out.Type, Text: out.Text, MessageID: id})
	if out.Type == conductor.OutFinal {
		s.transcript[ref] = append(s.transcript[ref], conductor.BriefEntry{
			Role:   conductor.RoleAssistant,
			Author: "bot",
			TS:     conductor.BriefTimestamp(time.Now()),
			Text:   out.Text,
		})
	}
	return id, nil
}

// Edit implements conductor.Source.
func (s *Source) Edit(_ context.Context, ref, msgID string, out conductor.Outbound) error {
	if msgID == "" {
		return errors.New("fakesource: no message to edit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(Record{Action: ActionEdit, Ref: ref, Type: out.Type, Text: out.Text, MessageID: msgID})
	return nil
}

// Attach implements conductor.Source. The bytes are read and dropped: what matters is that
// the download path ran and how much came out of it.
func (s *Source) Attach(_ context.Context, ref string, file conductor.Attachment) error {
	n, err := io.Copy(io.Discard, file.Body)
	if err != nil {
		return fmt.Errorf("fakesource: read %s: %w", file.Name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(Record{Action: ActionAttach, Ref: ref, Name: file.Name, ContentType: file.ContentType, Size: n})
	return nil
}

// LinkPullRequests implements the conductor's optional pull-request half of Source, so a
// test can see which pull requests a turn's answer produced. The real one is the web chat,
// which stores them; this one keeps the list and answers with it.
func (s *Source) LinkPullRequests(_ context.Context, ref string, prs []conductor.PullRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pr := range prs {
		s.record(Record{Action: ActionLinkPullRequest, Ref: ref, Text: pr.URL})
		s.pulls[ref] = append(s.pulls[ref], pr)
	}
	return nil
}

// PullRequests is every pull request linked to one ref, in order.
func (s *Source) PullRequests(ref string) []conductor.PullRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]conductor.PullRequest(nil), s.pulls[ref]...)
}

// React implements conductor.Source.
func (s *Source) React(_ context.Context, ref string, kind conductor.Reaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record(Record{Action: ActionReact, Ref: ref, Reaction: string(kind)})
	return nil
}

// record appends one action. The caller holds the lock.
func (s *Source) record(rec Record) {
	s.seq++
	rec.Seq = s.seq
	s.records = append(s.records, rec)
}

// Records is everything the conductor said, in order.
func (s *Source) Records() []Record {
	return s.RecordsSince(0)
}

// RecordsSince is everything with a seq above since.
func (s *Source) RecordsSince(since int64) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, rec := range s.records {
		if rec.Seq > since {
			out = append(out, rec)
		}
	}
	return out
}

// Mirrors makes this source one whose conversation lives elsewhere, the way Slack's does.
func (s *Source) Mirrors() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mirror = true
}

// MirrorKey implements the conductor's mirrored-source half: the session key a ref belongs
// to. False unless Mirrors was called, which is the same "this source is not mirrored"
// answer the web chat gives by not implementing the method at all.
func (s *Source) MirrorKey(ref string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.mirror {
		return "", false
	}
	// The same shape a session key has, because that is what the real thing returns: the
	// channel and the thread, never the triggering message.
	channel, thread, _ := strings.Cut(ref, "/")
	return s.kind + ":" + channel + ":" + thread, true
}
