package linear

// Reading one issue for a person in Podium (docs/agent.md, "Tickets in a chat"): the chip and
// card a chat draws for an issue it names, the panel of an issue's conversation, and the ticket
// a person's message hands the assistant. The app's token reads every team, so this file only
// reads; whether the person asking may be shown what it read is internal/agent/tickets's to
// decide, from the issue's team and the teams the person's own Linear account is on.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrNoIssue is an issue Linear does not have, or does not show the app.
var ErrNoIssue = errors.New("linear: no such issue")

const (
	// ticketTTL is how long one issue read from Linear is reused. A chat that names the same
	// issue in twenty messages, or ten people reading one conversation, ask Linear once a
	// minute about it, not each time.
	ticketTTL = time.Minute
	// memberTTL is how long the teams one person is on are reused. Somebody taken off a team
	// stops seeing its issues within this.
	memberTTL = 2 * time.Minute
	// teamKeysTTL is how long the workspace's team keys are reused.
	teamKeysTTL = 5 * time.Minute
	// ticketComments is how many of an issue's latest comments are read with it.
	ticketComments = "5"
	// ticketLabels and ticketAttachments bound the rest of what is read.
	ticketLabels      = "20"
	ticketAttachments = "20"
	// userTeams bounds the teams one person is read to be on.
	userTeams = "250"
	// maxCached bounds each cache. Past it the cache is emptied rather than evicted with care:
	// it is a minute's convenience.
	maxCached = 2000
)

// Ticket is one issue as Podium shows it to a person.
type Ticket struct {
	ID          string
	Identifier  string
	Title       string
	Description string
	URL         string
	TeamKey     string
	TeamName    string
	StateName   string
	StateType   string
	Assignee    string
	UpdatedAt   time.Time
	Labels      []string
	// Links are the issue's attachments: pull requests, and anything else linked to it.
	Links []TicketLink
	// Comments are the latest comments, oldest first.
	Comments []TicketComment
}

// TicketLink is one attachment on an issue. Source is Linear's sourceType ("github", …).
type TicketLink struct {
	Title  string
	URL    string
	Source string
}

// TicketComment is one comment, with who wrote it as a name to show.
type TicketComment struct {
	Author string
	Body   string
	At     time.Time
}

const ticketQuery = `query PodiumTicket($id: String!) {
  issue(id: $id) {
    id
    identifier
    title
    description
    url
    updatedAt
    state { id name type }
    team { id key name }
    assignee { id name displayName }
    labels(first: ` + ticketLabels + `) { nodes { name } }
    attachments(first: ` + ticketAttachments + `) { nodes { title url sourceType } }
    comments(first: ` + ticketComments + `) {
      nodes {
        id
        body
        createdAt
        user { id name displayName }
        botActor { id name userDisplayName }
      }
    }
  }
}`

// Ticket reads one issue by identifier ("ENG-12") or id, with its latest comments. An issue
// Linear does not have is ErrNoIssue.
func (c *Client) Ticket(ctx context.Context, ref string) (Ticket, error) {
	var out struct {
		Issue *struct {
			ID          string        `json:"id"`
			Identifier  string        `json:"identifier"`
			Title       string        `json:"title"`
			Description *string       `json:"description"`
			URL         string        `json:"url"`
			UpdatedAt   time.Time     `json:"updatedAt"`
			State       WorkflowState `json:"state"`
			Team        struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"team"`
			Assignee *User `json:"assignee"`
			Labels   struct {
				Nodes []struct {
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"labels"`
			Attachments struct {
				Nodes []struct {
					Title      string `json:"title"`
					URL        string `json:"url"`
					SourceType string `json:"sourceType"`
				} `json:"nodes"`
			} `json:"attachments"`
			Comments struct {
				Nodes []Comment `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}
	if err := c.do(ctx, "PodiumTicket", ticketQuery, map[string]any{"id": ref}, &out); err != nil {
		if notFound(err) {
			return Ticket{}, ErrNoIssue
		}
		return Ticket{}, err
	}
	if out.Issue == nil || out.Issue.ID == "" {
		return Ticket{}, ErrNoIssue
	}
	in := out.Issue
	t := Ticket{
		ID: in.ID, Identifier: in.Identifier, Title: in.Title, URL: in.URL,
		TeamKey: in.Team.Key, TeamName: in.Team.Name,
		StateName: in.State.Name, StateType: in.State.Type,
		Assignee: in.Assignee.label(), UpdatedAt: in.UpdatedAt,
	}
	if in.Description != nil {
		t.Description = *in.Description
	}
	for _, l := range in.Labels.Nodes {
		t.Labels = append(t.Labels, l.Name)
	}
	for _, a := range in.Attachments.Nodes {
		if a.URL == "" {
			continue
		}
		t.Links = append(t.Links, TicketLink{Title: a.Title, URL: a.URL, Source: a.SourceType})
	}
	// Newest first from Linear; oldest first here, as a transcript reads.
	for i := len(in.Comments.Nodes) - 1; i >= 0; i-- {
		cm := in.Comments.Nodes[i]
		t.Comments = append(t.Comments, TicketComment{Author: cm.author(), Body: cm.Body, At: cm.CreatedAt})
	}
	return t, nil
}

// notFound is Linear saying it has no such entity. It says so in a 200 with an error, whose
// code has been seen as INVALID_INPUT and as ENTITY_NOT_FOUND, so the message is read too.
func notFound(err error) bool {
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) {
		return false
	}
	msg := strings.ToLower(gqlErr.Message)
	return gqlErr.Code == "ENTITY_NOT_FOUND" || strings.Contains(msg, "not found") ||
		strings.Contains(msg, "entity not found")
}

const userTeamsQuery = `query PodiumUserTeams($id: String!) {
  user(id: $id) {
    id
    active
    teams(first: ` + userTeams + `) { nodes { key } }
  }
}`

// UserTeams is the keys of the teams one of the workspace's people is on, and whether their
// account is active. A suspended account is on no team as far as Podium is concerned.
func (c *Client) UserTeams(ctx context.Context, userID string) (active bool, keys []string, err error) {
	var out struct {
		User *struct {
			ID     string `json:"id"`
			Active bool   `json:"active"`
			Teams  struct {
				Nodes []struct {
					Key string `json:"key"`
				} `json:"nodes"`
			} `json:"teams"`
		} `json:"user"`
	}
	if err := c.do(ctx, "PodiumUserTeams", userTeamsQuery, map[string]any{"id": userID}, &out); err != nil {
		if notFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}
	if out.User == nil || out.User.ID == "" {
		return false, nil, nil
	}
	for _, t := range out.User.Teams.Nodes {
		keys = append(keys, strings.ToUpper(t.Key))
	}
	return out.User.Active, keys, nil
}

// ticketCache is what the app has read lately for people in Podium: issues, the teams each
// person is on, and the workspace's team keys. Its own lock, so a slow read never holds the
// source's.
type ticketCache struct {
	mu       sync.Mutex
	tickets  map[string]cachedTicket
	members  map[string]cachedTeams
	teamKeys cachedTeams
}

type cachedTicket struct {
	t   Ticket
	err error
	at  time.Time
}

type cachedTeams struct {
	keys []string
	at   time.Time
}

func newTicketCache() *ticketCache {
	return &ticketCache{tickets: map[string]cachedTicket{}, members: map[string]cachedTeams{}}
}

func (c *ticketCache) lock()   { c.mu.Lock() }
func (c *ticketCache) unlock() { c.mu.Unlock() }

// Ticket is one issue by identifier or id, read from Linear at most once a ticketTTL. An issue
// Linear does not have is remembered as missing for as long.
func (s *AppSource) Ticket(ctx context.Context, ref string) (Ticket, error) {
	key := strings.ToUpper(strings.TrimSpace(ref))
	if key == "" {
		return Ticket{}, ErrNoIssue
	}
	now := s.now()
	c := s.ticketCache
	c.lock()
	hit, ok := c.tickets[key]
	c.unlock()
	if ok && now.Sub(hit.at) < ticketTTL {
		return hit.t, hit.err
	}
	t, err := s.client.Ticket(ctx, ref)
	if err != nil && !errors.Is(err, ErrNoIssue) {
		// A failure to ask is not remembered: the next look asks again.
		return Ticket{}, err
	}
	c.lock()
	if len(c.tickets) >= maxCached {
		c.tickets = map[string]cachedTicket{}
	}
	entry := cachedTicket{t: t, err: err, at: now}
	c.tickets[key] = entry
	if err == nil {
		// Asked by identifier, found by id next time too, and the other way round.
		c.tickets[strings.ToUpper(t.ID)] = entry
		c.tickets[strings.ToUpper(t.Identifier)] = entry
	}
	c.unlock()
	return t, err
}

// MemberTeams is the keys of the teams a Linear account is on, the account as identities keys
// it ("<organization>/<user>"). An account of another workspace, or a suspended one, is on none.
func (s *AppSource) MemberTeams(ctx context.Context, account string) ([]string, error) {
	org, user, ok := strings.Cut(account, "/")
	if !ok || user == "" || org != s.organizationID {
		return nil, nil
	}
	now := s.now()
	c := s.ticketCache
	c.lock()
	hit, cached := c.members[user]
	c.unlock()
	if cached && now.Sub(hit.at) < memberTTL {
		return hit.keys, nil
	}
	active, keys, err := s.client.UserTeams(ctx, user)
	if err != nil {
		return nil, err
	}
	if !active {
		keys = nil
	}
	c.lock()
	if len(c.members) >= maxCached {
		c.members = map[string]cachedTeams{}
	}
	c.members[user] = cachedTeams{keys: keys, at: now}
	c.unlock()
	return keys, nil
}

// TeamKeys is the workspace's team keys, upper-case, read at most once a teamKeysTTL.
func (s *AppSource) TeamKeys(ctx context.Context) ([]string, error) {
	now := s.now()
	c := s.ticketCache
	c.lock()
	hit := c.teamKeys
	c.unlock()
	if !hit.at.IsZero() && now.Sub(hit.at) < teamKeysTTL {
		return hit.keys, nil
	}
	teams, _, err := s.client.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(teams))
	for _, t := range teams {
		keys = append(keys, strings.ToUpper(t.Key))
	}
	c.lock()
	c.teamKeys = cachedTeams{keys: keys, at: now}
	c.unlock()
	return keys, nil
}
