package linear

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ticketData is an issue as PodiumTicket answers for one, its comments newest first as Linear
// sends them.
func ticketData(id, identifier string) map[string]any {
	return map[string]any{"issue": map[string]any{
		"id": id, "identifier": identifier, "title": "Fix the thing", "description": "It is broken.",
		"url":       "https://linear.app/acme/issue/" + identifier,
		"updatedAt": "2026-11-15T10:00:00Z",
		"state":     map[string]any{"id": "s1", "name": "In Progress", "type": "started"},
		"team":      map[string]any{"id": "t1", "key": "EME", "name": "Emergere"},
		"assignee":  map[string]any{"id": "u1", "name": "Ada Lovelace", "displayName": "ada"},
		"labels":    map[string]any{"nodes": []any{map[string]any{"name": "Bug"}}},
		"attachments": map[string]any{"nodes": []any{
			map[string]any{"title": "acme/app#12", "url": "https://github.com/acme/app/pull/12", "sourceType": "github"},
			map[string]any{"title": "nothing", "url": ""},
		}},
		"comments": map[string]any{"nodes": []any{
			map[string]any{"id": "c2", "body": "second", "createdAt": "2026-11-15T09:00:00Z",
				"user": map[string]any{"id": "u2", "name": "Grace", "displayName": "grace"}},
			map[string]any{"id": "c1", "body": "first", "createdAt": "2026-11-14T09:00:00Z",
				"user": nil, "botActor": map[string]any{"id": "b", "name": "GitHub"}},
		}},
	}}
}

func TestTicketReadsAnIssueAndRemembersItForAMinute(t *testing.T) {
	h := newAppHarness(t)
	h.stub.reply("PodiumTicket", ticketData("issue-1", "EME-6"))

	got, err := h.src.Ticket(context.Background(), "eme-6")
	require.NoError(t, err)
	assert.Equal(t, "EME-6", got.Identifier)
	assert.Equal(t, "issue-1", got.ID)
	assert.Equal(t, "EME", got.TeamKey)
	assert.Equal(t, "Emergere", got.TeamName)
	assert.Equal(t, "In Progress", got.StateName)
	assert.Equal(t, "started", got.StateType)
	assert.Equal(t, "ada", got.Assignee)
	assert.Equal(t, []string{"Bug"}, got.Labels)
	assert.Equal(t, []TicketLink{{Title: "acme/app#12", URL: "https://github.com/acme/app/pull/12", Source: "github"}}, got.Links)
	require.Len(t, got.Comments, 2)
	assert.Equal(t, "first", got.Comments[0].Body, "oldest first")
	assert.Equal(t, "GitHub", got.Comments[0].Author)
	assert.Equal(t, "grace", got.Comments[1].Author)

	// By identifier, then by id: both are the one read.
	_, err = h.src.Ticket(context.Background(), "EME-6")
	require.NoError(t, err)
	_, err = h.src.Ticket(context.Background(), "issue-1")
	require.NoError(t, err)
	assert.Len(t, h.stub.callsOf("PodiumTicket"), 1)

	h.clock.advance(ticketTTL)
	_, err = h.src.Ticket(context.Background(), "EME-6")
	require.NoError(t, err)
	assert.Len(t, h.stub.callsOf("PodiumTicket"), 2)
}

func TestTicketThatLinearDoesNotHaveIsErrNoIssue(t *testing.T) {
	h := newAppHarness(t)
	h.stub.on("PodiumTicket", func(map[string]any) stubReply {
		return stubReply{Message: "Entity not found: Issue", Code: "INVALID_INPUT"}
	})
	_, err := h.src.Ticket(context.Background(), "EME-404")
	assert.ErrorIs(t, err, ErrNoIssue)

	h.stub.reply("PodiumTicket", map[string]any{"issue": nil})
	_, err = h.src.Ticket(context.Background(), "EME-405")
	assert.ErrorIs(t, err, ErrNoIssue)
}

func TestTicketFailureIsNotRemembered(t *testing.T) {
	h := newAppHarness(t)
	h.stub.on("PodiumTicket", func(map[string]any) stubReply {
		return stubReply{Message: "down", Code: "INTERNAL_SERVER_ERROR", Status: 500}
	})
	_, err := h.src.Ticket(context.Background(), "EME-6")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoIssue)

	h.stub.reply("PodiumTicket", ticketData("issue-1", "EME-6"))
	_, err = h.src.Ticket(context.Background(), "EME-6")
	require.NoError(t, err)
}

func TestMemberTeamsReadsOnlyThisWorkspacesActiveAccounts(t *testing.T) {
	h := newAppHarness(t)
	active := true
	h.stub.on("PodiumUserTeams", func(vars map[string]any) stubReply {
		return stubReply{Data: map[string]any{"user": map[string]any{
			"id": vars["id"], "active": active,
			"teams": map[string]any{"nodes": []any{map[string]any{"key": "eme"}, map[string]any{"key": "ENG"}}},
		}}}
	})
	ctx := context.Background()

	keys, err := h.src.MemberTeams(ctx, fixtureOrg+"/u1")
	require.NoError(t, err)
	assert.Equal(t, []string{"EME", "ENG"}, keys)

	keys, err = h.src.MemberTeams(ctx, "another-org/u1")
	require.NoError(t, err)
	assert.Empty(t, keys, "an account of another workspace is on none of this one's teams")
	keys, err = h.src.MemberTeams(ctx, "not-an-account")
	require.NoError(t, err)
	assert.Empty(t, keys)
	assert.Len(t, h.stub.callsOf("PodiumUserTeams"), 1)

	active = false
	h.clock.advance(memberTTL + time.Second)
	keys, err = h.src.MemberTeams(ctx, fixtureOrg+"/u1")
	require.NoError(t, err)
	assert.Empty(t, keys, "a suspended account is on no team")
}

func TestTeamKeysAreTheCatalogsUpperCased(t *testing.T) {
	h := newAppHarness(t)
	h.stub.reply("PodiumCatalog", map[string]any{
		"teams":       map[string]any{"nodes": []any{map[string]any{"key": "eme", "name": "Emergere"}}},
		"issueLabels": map[string]any{"nodes": []any{}},
	})
	keys, err := h.src.TeamKeys(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"EME"}, keys)
	_, err = h.src.TeamKeys(context.Background())
	require.NoError(t, err)
	assert.Len(t, h.stub.callsOf("PodiumCatalog"), 1)
}
