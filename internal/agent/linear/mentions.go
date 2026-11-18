package linear

// Who an issue's description or a comment @mentions, which puts the issue in their Issues list
// when they may read its conversation (docs/agent.md, "The Issues view"). A mention alone grants
// no reading: anybody can @mention anybody, a private team's issue included.
//
// Linear writes a person's mention into markdown as the plain URL of their profile,
// https://linear.app/<workspace>/profiles/<displayName> ("mentions can be created in Markdown by
// using the plain URL of the resource", linear.app/developers/graphql; a user's own url field is
// the same, profiles/ada for Ada Lovelace in testdata/), and the editor shows it as
// @displayName. A comment's body also carries a mention as typed, "@podium what is the time?"
// (testdata/session_created_mention.json, and stripMention), so both are taken. Neither names the user's id, so a handle is resolved through the workspace's
// users, read at most once a usersTTL (usersByHandle), never once per delivery.
//
// The text is somebody else's, so it is only scanned: the handles found are looked up, and
// nothing in it is followed, fetched or run. A handle no user of the workspace has is dropped.

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ViaPersonMentioned is a person @mentioned in an issue's description or a comment, as
// IssuePerson.Via. ViaMentioned is somebody who @mentioned the app.
const ViaPersonMentioned = "person_mentioned"

// usersTTL is how long the workspace's users, by handle, are trusted; usersRetry is how soon a
// handle the list lacks asks again, so a new member counts within a minute without a delivery
// naming a stranger asking Linear every time.
const (
	usersTTL   = 10 * time.Minute
	usersRetry = time.Minute
)

// Bounds on what one text is read for. Past them the rest is not looked at: a description is
// not a list of everybody in the workspace.
const (
	maxMentionText = 64 << 10
	maxMentions    = 50
)

var (
	// profileMention is a profile URL: the workspace's url key, then the handle.
	profileMention = regexp.MustCompile(`https?://linear\.app/[A-Za-z0-9_-]+/profiles/([A-Za-z0-9._%-]+)`)
	// atMention is @handle at the start of the text or after anything that cannot be part of an
	// address or a path, so ada@acme.com and acme.com/@ada are not mentions.
	atMention = regexp.MustCompile(`(?:^|[^A-Za-z0-9._%+/@-])@([A-Za-z0-9][A-Za-z0-9._-]*)`)
	// fenced and inline are code, where an @ is text about a mention, not one.
	fenced = regexp.MustCompile("(?s)```.*?```")
	inline = regexp.MustCompile("`[^`\n]*`")
)

// mentionHandles is every handle text @mentions, lower-cased, once each, in order.
func mentionHandles(text string) []string {
	if len(text) > maxMentionText {
		text = text[:maxMentionText]
	}
	text = inline.ReplaceAllString(fenced.ReplaceAllString(text, " "), " ")
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		if u, err := url.PathUnescape(h); err == nil {
			h = u
		}
		h = strings.ToLower(strings.TrimRight(h, ".-_"))
		if h == "" || seen[h] || len(out) >= maxMentions {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	for _, m := range profileMention.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	// A profile URL's own "/profiles/" is no @, so the two never find the same mention twice;
	// [@ada](https://linear.app/acme/profiles/ada) finds ada twice, and seen keeps one.
	for _, m := range atMention.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	return out
}

// handleCache is the workspace's users by lower-cased handle (display name), as one read of the
// users list left it.
type handleCache struct {
	ids map[string]string
	// at is when the list was read, and tried when it was last asked for, read or not.
	at, tried time.Time
}

// usersByHandle is the workspace's users by handle, read again when the list is older than
// usersTTL, or when it lacks one of want and was not asked for within usersRetry. A failure to
// read leaves what was read before.
func (s *AppSource) usersByHandle(ctx context.Context, want []string) map[string]string {
	now := s.now()
	s.mu.Lock()
	c := s.handles
	s.mu.Unlock()
	stale := c.at.IsZero() || now.Sub(c.at) >= usersTTL
	if !stale {
		for _, h := range want {
			if _, ok := c.ids[h]; !ok {
				stale = true
				break
			}
		}
	}
	if !stale || (!c.tried.IsZero() && now.Sub(c.tried) < usersRetry) {
		return c.ids
	}
	users, err := s.client.Users(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handles.tried = now
	if err != nil {
		s.logger.WarnContext(ctx, "linear: reading the workspace's users for @mentions failed", "error", err)
		return s.handles.ids
	}
	ids := make(map[string]string, len(users))
	for _, u := range users {
		if h := strings.ToLower(strings.TrimSpace(u.DisplayName)); h != "" && u.ID != "" {
			ids[h] = u.ID
		}
	}
	s.handles.ids, s.handles.at = ids, now
	return ids
}

// mentionedPeople is the people the record's texts @mention, as IssuePeople.
func (s *AppSource) mentionedPeople(ctx context.Context, org string, handles []string) []IssuePerson {
	// The app's own handle is nobody, and asks Linear nothing: it is in most comments the app sees.
	app := strings.ToLower(strings.ReplaceAll(s.appName, " ", ""))
	handles = slices.DeleteFunc(slices.Clone(handles), func(h string) bool { return app != "" && h == app })
	if len(handles) == 0 || org == "" {
		return nil
	}
	ids := s.usersByHandle(ctx, handles)
	var out []IssuePerson
	for _, h := range handles {
		if id, ok := ids[h]; ok {
			out = append(out, s.person(org, id, ViaPersonMentioned)...)
		}
	}
	return out
}
