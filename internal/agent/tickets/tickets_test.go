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

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/linear"
	"github.com/podium-ade/podium/internal/agent/store"
)

type fakeStore struct {
	rows       []store.LinearIssue
	touched    map[string][]string // source key -> logins
	identities []store.Identity
	chats      map[string]store.Chat
	grants     map[string][]store.ChatGrant
	people     map[string][]store.ChatParticipant
}

func (f *fakeStore) GetLinearIssue(_ context.Context, key string) (store.LinearIssue, error) {
	for _, r := range f.rows {
		if r.SourceKey == key {
			return r, nil
		}
	}
	return store.LinearIssue{}, fmt.Errorf("%w: none", store.ErrNotFound)
}

func (f *fakeStore) GetChat(_ context.Context, id string) (store.Chat, error) {
	c, ok := f.chats[id]
	if !ok {
		return store.Chat{}, fmt.Errorf("%w: none", store.ErrNotFound)
	}
	return c, nil
}

func (f *fakeStore) ListChatAccess(_ context.Context, id string) ([]store.ChatGrant, error) {
	return f.grants[id], nil
}

func (f *fakeStore) ChatParticipants(_ context.Context, id string) ([]store.ChatParticipant, error) {
	return f.people[id], nil
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
		chats: map[string]store.Chat{
			"mine": {ID: "mine", Login: "member@acme.com", GeneralAccess: store.AccessRestricted},
		},
		grants: map[string][]store.ChatGrant{},
		people: map[string][]store.ChatParticipant{"mine": {{Login: "member@acme.com"}}},
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
	got, withheld := r.BriefTickets(context.Background(), "member@acme.com", "chat:mine",
		"SEC-9 is secret; EME-6, UTF-8, EME-101 EME-102 EME-103 EME-104")
	assert.Equal(t, []string{"EME-6", "EME-101", "EME-102"}, identifiers(got))
	assert.Equal(t, "the description", got[0].Description)
	assert.Zero(t, withheld, "a ticket the speaker may not see is not counted either")

	got, _ = r.BriefTickets(context.Background(), "member@acme.com", "linear:issue-6", "about EME-6 and EME-101")
	require.Len(t, got, 1, "the conversation's own issue is left out")
	assert.Equal(t, "EME-101", got[0].Identifier)

	st.chats["theirs"] = store.Chat{ID: "theirs", Login: "outsider@acme.com"}
	got, withheld = r.BriefTickets(context.Background(), "outsider@acme.com", "chat:theirs", "EME-6")
	assert.Empty(t, got)
	assert.Zero(t, withheld)
}

func identifiers(bts []conductor.BriefTicket) []string {
	var ids []string
	for _, bt := range bts {
		ids = append(ids, bt.Identifier)
	}
	return ids
}

// shared is the fixture with SEC-9 visible to the speaker (an SEC member) and a chat of theirs
// shared with somebody on EME only.
func shared() (*fakeStore, *fakeApp, *Reader) {
	st, app := fixture()
	st.identities = append(st.identities, store.Identity{Login: "both@acme.com", Provider: store.ProviderLinear, ExternalID: org + "/u-both"})
	app.members[org+"/u-both"] = []string{"EME", "SEC"}
	st.chats["team"] = store.Chat{ID: "team", Login: "both@acme.com", GeneralAccess: store.AccessRestricted}
	st.grants["team"] = []store.ChatGrant{{Login: "member@acme.com", Role: store.RoleViewer}}
	st.people["team"] = []store.ChatParticipant{{Login: "both@acme.com"}}
	return st, app, reader(st, app)
}

func TestAPrivateChatIsBriefedWithEveryTicketTheSpeakerMaySee(t *testing.T) {
	st, _, r := shared()
	st.chats["solo"] = store.Chat{ID: "solo", Login: "both@acme.com", GeneralAccess: store.AccessRestricted}
	got, withheld := r.BriefTickets(context.Background(), "both@acme.com", "chat:solo", "EME-6 and SEC-9")
	assert.Equal(t, []string{"EME-6", "SEC-9"}, identifiers(got))
	assert.Zero(t, withheld)
}

func TestASharedChatIsBriefedOnlyWithTicketsEveryReaderMaySee(t *testing.T) {
	st, _, r := shared()
	ctx := context.Background()

	got, withheld := r.BriefTickets(ctx, "both@acme.com", "chat:team", "EME-6 and SEC-9")
	assert.Equal(t, []string{"EME-6"}, identifiers(got), "the reader it is shared with is not on SEC")
	assert.Equal(t, 1, withheld)

	// A participant counts the same as a grant, and so does the owner when somebody else asks.
	st.grants["team"] = nil
	st.people["team"] = []store.ChatParticipant{{Login: "both@acme.com"}, {Login: "outsider@acme.com"}}
	got, withheld = r.BriefTickets(ctx, "both@acme.com", "chat:team", "EME-6 and SEC-9")
	assert.Empty(t, got, "the participant is on neither team")
	assert.Equal(t, 2, withheld)

	// A reader who touched the issue may see it whatever their team.
	st.touched["linear:issue-6"] = append(st.touched["linear:issue-6"], "outsider@acme.com")
	got, _ = r.BriefTickets(ctx, "both@acme.com", "chat:team", "EME-6 and SEC-9")
	assert.Equal(t, []string{"EME-6"}, identifiers(got))
}

func TestAChatWhoseReadersCannotBeNamedIsBriefedWithNothing(t *testing.T) {
	ctx := context.Background()
	for name, change := range map[string]func(*fakeStore){
		"open to every member": func(st *fakeStore) {
			c := st.chats["team"]
			c.GeneralAccess = store.AccessMembers
			st.chats["team"] = c
		},
		"open to its link": func(st *fakeStore) {
			c := st.chats["team"]
			c.GeneralAccess = store.AccessLink
			st.chats["team"] = c
		},
		"an author who is not in Podium": func(st *fakeStore) {
			st.people["team"] = append(st.people["team"], store.ChatParticipant{Name: "Someone on Slack"})
		},
		"more readers than are checked": func(st *fakeStore) {
			for i := range MaxBriefReaders + 1 {
				login := fmt.Sprintf("p%d@acme.com", i)
				st.grants["team"] = append(st.grants["team"], store.ChatGrant{Login: login})
				st.touched["linear:issue-6"] = append(st.touched["linear:issue-6"], login)
			}
		},
		"no such chat": func(st *fakeStore) { delete(st.chats, "team") },
	} {
		st, _, r := shared()
		st.grants["team"] = nil // only the speaker, until the change
		change(st)
		got, withheld := r.BriefTickets(ctx, "both@acme.com", "chat:team", "EME-6")
		assert.Empty(t, got, name)
		assert.Equal(t, 1, withheld, name)
	}
}

func TestAChatOfAsManyReadersAsAreCheckedIsBriefed(t *testing.T) {
	st, _, r := shared()
	st.grants["team"] = nil
	for i := range MaxBriefReaders {
		login := fmt.Sprintf("p%d@acme.com", i)
		st.grants["team"] = append(st.grants["team"], store.ChatGrant{Login: login})
		st.touched["linear:issue-6"] = append(st.touched["linear:issue-6"], login)
	}
	got, withheld := r.BriefTickets(context.Background(), "both@acme.com", "chat:team", "EME-6")
	assert.Equal(t, []string{"EME-6"}, identifiers(got))
	assert.Zero(t, withheld)
}

func TestALinearIssueConversationIsBriefedOnlyWithTicketsOfItsTeam(t *testing.T) {
	st, app, r := shared()
	app.tickets["issue-7"] = linear.Ticket{ID: "issue-7", Identifier: "EME-7", Title: "t", TeamKey: "EME"}
	ctx := context.Background()

	got, withheld := r.BriefTickets(ctx, "both@acme.com", "linear:issue-6", "EME-7 and SEC-9")
	assert.Equal(t, []string{"EME-7"}, identifiers(got), "the answer is a comment on EME-6, read by whoever sees EME")
	assert.Equal(t, 1, withheld)

	// An issue whose team the webhooks never said, and a project's conversation, can't be told.
	st.rows[0].TeamKey = ""
	got, withheld = r.BriefTickets(ctx, "both@acme.com", "linear:issue-6", "EME-7 and SEC-9")
	assert.Empty(t, got)
	assert.Equal(t, 2, withheld)
	got, _ = r.BriefTickets(ctx, "both@acme.com", "linear:project:p-1", "EME-7")
	assert.Empty(t, got)
}

func TestAConversationOutsidePodiumIsBriefedWithNothing(t *testing.T) {
	_, _, r := shared()
	ctx := context.Background()
	for _, key := range []string{"slack:C1:1700000000.000100", "github:acme/app#4", "linear:session:s-1", ""} {
		got, withheld := r.BriefTickets(ctx, "both@acme.com", key, "EME-6 and SEC-9")
		assert.Empty(t, got, key)
		assert.Equal(t, 2, withheld, key)
	}
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
