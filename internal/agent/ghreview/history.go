package ghreview

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/conductor"
)

// History is what the pull request said before the message ref names, oldest first, as
// conversation: every human comment, inline comment and review with words in it, none of the
// App's own, and never the message ref names. The conductor reads it once, when Podium's
// record of the pull request is opened, so the record starts where the conversation did —
// the same thing the Slack source's History does for a thread — and a turn no longer reads
// GitHub's copy again on every turn.
//
// Each event carries the external ID the webhook gives the same message (kind:id), so a
// comment seeded here and then redelivered by GitHub is one row, not two. A ref that names
// no comment (a pull request opened, marked ready, or a review asked for from Slack) takes
// everything said so far.
func (s *Source) History(ctx context.Context, ref string) ([]conductor.InboundEvent, error) {
	p, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	install, err := s.installation(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("github: history of %s: %w", PRURL(p.Owner, p.Repo, p.Number), err)
	}
	issues, err := s.client.ListIssueComments(ctx, install, p.Owner, p.Repo, p.Number)
	if err != nil {
		return nil, err
	}
	reviews, err := s.client.ListReviews(ctx, install, p.Owner, p.Repo, p.Number)
	if err != nil {
		return nil, err
	}
	inline, err := s.client.ListReviewComments(ctx, install, p.Owner, p.Repo, p.Number)
	if err != nil {
		return nil, err
	}

	items := make([]historyItem, 0, len(issues)+len(reviews)+len(inline))
	for _, c := range issues {
		items = append(items, historyItem{KindIssueComment, c.ID, c.Body, c.HTMLURL, c.CreatedAt, c.User})
	}
	for _, c := range inline {
		items = append(items, historyItem{KindReviewComment, c.ID, c.Body, c.HTMLURL, c.CreatedAt, c.User})
	}
	for _, r := range reviews {
		items = append(items, historyItem{KindReview, r.ID, r.Body, r.HTMLURL, r.SubmittedAt, r.User})
	}
	return s.historyEvents(p, items), nil
}

// historyItem is one thing said on a pull request, whichever of GitHub's three lists it came
// from.
type historyItem struct {
	kind string
	id   int64
	body string
	url  string
	at   time.Time
	user User
}

// historyEvents is History's choice and order, separated from the three API calls so it can
// be pinned without a GitHub.
func (s *Source) historyEvents(p Parsed, items []historyItem) []conductor.InboundEvent {
	// The cutoff is the triggering message's own time. When it is not in the lists (it is
	// not a comment, or GitHub has not listed it yet), everything said so far is earlier.
	cutoff := time.Time{}
	if p.CommentID != 0 {
		for _, it := range items {
			if it.kind == p.Kind && it.id == p.CommentID {
				cutoff = it.at
				break
			}
		}
	}

	var keep []historyItem
	for _, it := range items {
		if it.kind == p.Kind && it.id == p.CommentID {
			continue
		}
		if strings.TrimSpace(it.body) == "" || it.at.IsZero() {
			// A review with no words (an approval, a bare request for changes) says nothing
			// the record could show; the webhook skips it for the same reason.
			continue
		}
		if s.isAppUser(it.user.Login, it.user.Type) {
			continue
		}
		if !cutoff.IsZero() && !it.at.Before(cutoff) {
			continue
		}
		keep = append(keep, it)
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].at.Before(keep[j].at) })

	out := make([]conductor.InboundEvent, 0, len(keep))
	for _, it := range keep {
		speakerID := ""
		if it.user.ID != 0 {
			speakerID = strconv.FormatInt(it.user.ID, 10)
		}
		body := strings.TrimSpace(it.body)
		out = append(out, conductor.InboundEvent{
			SourceKind: Kind,
			SourceKey:  SourceKey(p.Owner, p.Repo, p.Number),
			Ref: Ref(Parsed{
				Owner: p.Owner, Repo: p.Repo, Number: p.Number, InstallID: p.InstallID,
				Kind: it.kind, CommentID: it.id,
			}),
			Author:          it.user.Login,
			SpeakerProvider: conductor.SpeakerGitHub,
			SpeakerID:       speakerID,
			SpeakerAvatar:   it.user.AvatarURL,
			SpeakerHandle:   it.user.Login,
			ExternalID:      it.kind + ":" + strconv.FormatInt(it.id, 10),
			ExternalURL:     it.url,
			RecordText:      body,
			Text:            body,
			TS:              it.at,
			URL:             PRURL(p.Owner, p.Repo, p.Number),
			BriefKind:       conductor.SourceGitHub,
			RecordOnly:      true,
		})
	}
	return out
}
