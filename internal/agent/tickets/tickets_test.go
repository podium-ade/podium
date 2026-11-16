package tickets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/linear"
	"github.com/podium-ade/podium/internal/agent/store"
)

type fakeStore struct {
	rows       []store.LinearIssue
	touched    map[string][]string // source key -> logins
	identities []store.Identity
}

func (f *fakeStore) FindLinearIssue(_ context.Context, identifier, id string) (store.LinearIssue, error) {
	for _, r := range f.rows {
		if (identifier != "" && strings.EqualFold(r.Identifier, identifier)) || (id != "" && r.LinearID == id) {
			return r, nil
		}
	}
	return store.LinearIssue{}, fmt.Errorf("%w: none", store.ErrNotFound)
}

func (f *fakeStore) LinearIssueTeamKeys(context.Context) ([]string, error) {
	var keys []string
	for _, r := range f.rows {
		keys = append(keys, r.TeamKey)
	}
	return keys, nil
}

func (f *fakeStore) LinearTouched(_ context.Context, key, login string) (bool, error) {
	for _, l := range f.touched[key] {
		if l == login {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) IdentitiesOf(_ context.Context, logins []string) ([]store.Identity, error) {
	var out []store.Identity
	for _, id := range f.identities {
		for _, l := range logins {
			if id.Login == l {
				out = append(out, id)
			}
		}
	}
	return out, nil
}

type fakeApp struct {
	tickets map[string]linear.Ticket
	members map[string][]string
	teams   []string
	down    bool
	asked   []string
}

func (f *fakeApp) Ticket(_ context.Context, ref string) (linear.Ticket, error) {
	f.asked = append(f.asked, ref)
	if f.down {
		return linear.Ticket{}, errors.New("linear is down")
	}
	for _, t := range f.tickets {
		if strings.EqualFold(t.Identifier, ref) || t.ID == ref {
			return t, nil
		}
	}
	return linear.Ticket{}, linear.ErrNoIssue
}

func (f *fakeApp) MemberTeams(_ context.Context, account string) ([]string, error) {
	return f.members[account], nil
}

func (f *fakeApp) TeamKeys(context.Context) ([]string, error) { return f.teams, nil }

const org = "org-1"

func fixture() (*fakeStore, *fakeApp) {
	st := &fakeStore{
		rows: []store.LinearIssue{{
			SourceKey: "linear:issue-6", Kind: "issue", LinearID: "issue-6", Identifier: "EME-6",
			Title: "Cached title", TeamKey: "EME", StateName: "Todo", StateType: "unstarted",
			UpdatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		}},
		touched: map[string][]string{"linear:issue-6": {"toucher@acme.com"}},
		identities: []store.Identity{
			{Login: "member@acme.com", Provider: store.ProviderLinear, ExternalID: org + "/u-member"},
			{Login: "outsider@acme.com", Provider: store.ProviderLinear, ExternalID: org + "/u-outsider"},
			{Login: "slacker@acme.com", Provider: "slack", ExternalID: org + "/u-member"},
		},
	}
	app := &fakeApp{
		tickets: map[string]linear.Ticket{
			"issue-6": {ID: "issue-6", Identifier: "EME-6", Title: "Live title", TeamKey: "EME", StateName: "In Progress",
				Assignee: "ada", Description: "the description"},
			"issue-9": {ID: "issue-9", Identifier: "SEC-9", Title: "Secret", TeamKey: "SEC", StateName: "Todo"},
		},
		members: map[string][]string{org + "/u-member": {"EME"}, org + "/u-outsider": {"ENG"}},
		teams:   []string{"EME", "SEC", "ENG"},
	}
	return st, app
}

func reader(st *fakeStore, app *fakeApp) *Reader {
	return New(Options{Store: st, App: func() App {
		if app == nil {
			return nil
		}
		return app
	}})
}

func TestMayRead(t *testing.T) {
	assert.True(t, MayRead(true, false, nil, "EME"), "an admin reads every issue")
	assert.True(t, MayRead(false, true, nil, "EME"), "somebody who touched it reads it")
	assert.True(t, MayRead(false, false, []string{"eng", "eme"}, "EME"), "a member of its team reads it")
	assert.False(t, MayRead(false, false, []string{"ENG"}, "EME"), "a member of another team does not")
	assert.False(t, MayRead(false, false, nil, "EME"), "nobody in particular does not")
	assert.False(t, MayRead(false, false, []string{""}, ""), "an issue of no known team is nobody's")
}

func TestLookupShowsAnIssueOnlyToThoseWhoCouldSeeItInLinear(t *testing.T) {
	ctx := context.Background()
	ref := linear.IssueRef{Identifier: "EME-6"}
	cases := []struct {
		name   string
		viewer Viewer
		ok     bool
	}{
		{"an admin", Viewer{Login: "admin@acme.com", Admin: true}, true},
		{"somebody who touched it", Viewer{Login: "toucher@acme.com"}, true},
		{"a member of its team", Viewer{Login: "member@acme.com"}, true},
		{"a member of another team", Viewer{Login: "outsider@acme.com"}, false},
		{"somebody whose Slack account has that id", Viewer{Login: "slacker@acme.com"}, false},
		{"somebody with no Linear account", Viewer{Login: "nobody@acme.com"}, false},
		{"nobody", Viewer{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, app := fixture()
			got, err := reader(st, app).Lookup(ctx, tc.viewer, ref, true)
			if !tc.ok {
				assert.ErrorIs(t, err, ErrNotFound)
				assert.Empty(t, got.Title, "nothing about the issue is told")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "Live title", got.Title)
		})
	}
}

func TestLookupOfAnIssueThatDoesNotExistIsTheSameAsOneNotShown(t *testing.T) {
	st, app := fixture()
	r := reader(st, app)
	_, missing := r.Lookup(context.Background(), Viewer{Login: "member@acme.com"}, linear.IssueRef{Identifier: "EME-404"}, false)
	_, hidden := r.Lookup(context.Background(), Viewer{Login: "member@acme.com"}, linear.IssueRef{Identifier: "SEC-9"}, false)
	assert.ErrorIs(t, missing, ErrNotFound)
	assert.ErrorIs(t, hidden, ErrNotFound)
	assert.Equal(t, missing.Error(), hidden.Error())
}

func TestLookupForAChipTakesWhatTheWebhooksSaid(t *testing.T) {
	st, app := fixture()
	got, err := reader(st, app).Lookup(context.Background(), Viewer{Admin: true}, linear.IssueRef{Identifier: "eme-6"}, false)
	require.NoError(t, err)
	assert.True(t, got.Cached)
	assert.Equal(t, "Cached title", got.Title)
	assert.Empty(t, app.asked, "Linear is not asked for a chip the webhooks described")

	// One the webhooks never named is asked of Linear.
	got, err = reader(st, app).Lookup(context.Background(), Viewer{Admin: true}, linear.IssueRef{Identifier: "SEC-9"}, false)
	require.NoError(t, err)
	assert.False(t, got.Cached)
	assert.Equal(t, []string{"SEC-9"}, app.asked)
}

func TestLookupFallsBackToWhatTheWebhooksSaidWhenLinearIsDown(t *testing.T) {
	st, app := fixture()
	app.down = true
	got, err := reader(st, app).Lookup(context.Background(), Viewer{Login: "toucher@acme.com"}, linear.IssueRef{Identifier: "EME-6"}, true)
	require.NoError(t, err)
	assert.True(t, got.Cached)
	assert.Equal(t, "Cached title", got.Title)

	_, err = reader(st, app).Lookup(context.Background(), Viewer{Admin: true}, linear.IssueRef{Identifier: "SEC-9"}, true)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotFound, "a failure to ask is not a missing issue")
}

func TestLookupWithNoAppShowsOnlyTheRecordAndNoTeamMembership(t *testing.T) {
	st, _ := fixture()
	r := reader(st, nil)
	_, err := r.Lookup(context.Background(), Viewer{Login: "member@acme.com"}, linear.IssueRef{Identifier: "EME-6"}, true)
	assert.ErrorIs(t, err, ErrNotFound, "team membership cannot be asked without the app")
	got, err := r.Lookup(context.Background(), Viewer{Login: "toucher@acme.com"}, linear.IssueRef{Identifier: "EME-6"}, true)
	require.NoError(t, err)
	assert.True(t, got.Cached)
}

func TestBriefTicketsAreThoseTheSpeakerMaySeeAtMostThree(t *testing.T) {
	st, app := fixture()
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("issue-e%d", i)
		app.tickets[id] = linear.Ticket{ID: id, Identifier: fmt.Sprintf("EME-%d", 100+i), Title: "t", TeamKey: "EME"}
	}
	r := reader(st, app)
	got := r.BriefTickets(context.Background(), "member@acme.com", "",
		"SEC-9 is secret; EME-6, UTF-8, EME-101 EME-102 EME-103 EME-104")
	var ids []string
	for _, bt := range got {
		ids = append(ids, bt.Identifier)
	}
	assert.Equal(t, []string{"EME-6", "EME-101", "EME-102"}, ids)
	assert.Equal(t, "the description", got[0].Description)

	got = r.BriefTickets(context.Background(), "member@acme.com", "linear:issue-6", "about EME-6 and EME-101")
	require.Len(t, got, 1, "the conversation's own issue is left out")
	assert.Equal(t, "EME-101", got[0].Identifier)

	assert.Empty(t, r.BriefTickets(context.Background(), "outsider@acme.com", "", "EME-6"))
}

func TestBriefTicketIsTrimmedToItsBudget(t *testing.T) {
	var comments []linear.TicketComment
	for i := range 8 {
		comments = append(comments, linear.TicketComment{
			Author: "ada", Body: fmt.Sprintf("comment %d ", i) + strings.Repeat("é", 2000), At: time.Unix(int64(i), 0),
		})
	}
	issue := Issue{Ticket: linear.Ticket{
		ID: "i", Identifier: "EME-1", Title: "line one\nline two", Description: strings.Repeat("x", 10_000), Comments: comments,
	}}
	bt := BriefTicket(issue, maxTicketsBytes)
	assert.True(t, bt.Trimmed)
	assert.Equal(t, "line one line two", bt.Title)
	assert.LessOrEqual(t, len(bt.Description), maxTicketDescription+len("…"))
	require.NotEmpty(t, bt.Comments)
	assert.LessOrEqual(t, len(bt.Comments), maxTicketComments)
	assert.True(t, strings.HasPrefix(bt.Comments[len(bt.Comments)-1].Body, "comment 7"), "the latest comment is kept")
	for _, c := range bt.Comments {
		assert.LessOrEqual(t, len(c.Body), maxTicketComment+len("…"))
		assert.True(t, strings.ToValidUTF8(c.Body, "?") == c.Body, "cut on a rune boundary")
	}
	assert.LessOrEqual(t, briefSize(bt), maxTicketsBytes)

	small := BriefTicket(issue, 600)
	assert.Empty(t, small.Comments, "a spent budget keeps no comments")
	assert.LessOrEqual(t, len(small.Description), 300+len("…"))
}
