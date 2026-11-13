//go:build integration

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/internal/proto/podium/agent/v1/agentv1connect"
)

// adminClient is a signed-in admin or owner: the proxy marks the request infra.
func (f chatFixture) adminClient(login string) agentv1connect.AgentServiceClient {
	return agentv1connect.NewAgentServiceClient(http.DefaultClient, f.url,
		connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", "Bearer "+chatToken)
				req.Header().Set(LoginHeader, login)
				req.Header().Set(InfraHeader, "1")
				return next(ctx, req)
			}
		})))
}

func ciFixerDraft() *agentv1.RoutineDraft {
	return &agentv1.RoutineDraft{
		Name: "Fix failing CI", Schedule: "*/5 * * * *", Timezone: "UTC",
		Instruction: "Look for failed checks on open pull requests in the API repo and report what you would change.",
		DailyCapUsd: 5,
	}
}

// A system routine is an admin's to set up, runs as nobody, and is seen by admins alone.
func TestASystemRoutineIsSetUpAndSeenByAdminsAlone(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)

	_, err := f.clientAs("bob").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{
		Draft: ciFixerDraft(), Scope: store.RoutineScopeSystem,
	}))
	assert.Equal(t, "permission_denied", codeOf(err))

	created, err := f.adminClient("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{
		Draft: ciFixerDraft(), Scope: store.RoutineScopeSystem,
	}))
	require.NoError(t, err)
	r := created.Msg.GetRoutine()
	assert.Equal(t, store.RoutineScopeSystem, r.GetScope())
	assert.Empty(t, r.GetOwnerLogin(), "a system routine is nobody's")
	assert.Equal(t, "alice", r.GetCreatedBy())
	assert.Empty(t, r.GetRepos(), "the instruction names its repositories; nobody lists them")
	assert.InDelta(t, 5.0, r.GetDailyCapUsd(), 0.001)

	// Every five minutes is under a person's floor, and fine for a system routine.
	_, err = f.clientAs("bob").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{
		Draft: &agentv1.RoutineDraft{Name: "x", Schedule: "*/5 * * * *", Instruction: "y"},
	}))
	assert.Equal(t, "invalid_argument", codeOf(err))

	// Another admin sees it beside their own; a member does not.
	listed, err := f.adminClient("carol").ListRoutines(ctx, connect.NewRequest(&agentv1.ListRoutinesRequest{}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetRoutines(), 1)
	assert.Equal(t, r.GetId(), listed.Msg.GetRoutines()[0].GetId())
	listed, err = f.clientAs("bob").ListRoutines(ctx, connect.NewRequest(&agentv1.ListRoutinesRequest{}))
	require.NoError(t, err)
	assert.Empty(t, listed.Msg.GetRoutines())
	_, err = f.clientAs("bob").GetRoutine(ctx, connect.NewRequest(&agentv1.GetRoutineRequest{Id: r.GetId()}))
	assert.Equal(t, "not_found", codeOf(err))

	// Its chat: admins read it, nobody writes in it, and a member cannot open it.
	chat, err := f.store.GetChat(ctx, r.GetChatId())
	require.NoError(t, err)
	assert.Empty(t, chat.Login)
	role, err := f.svc.chatRole(adminCtx("carol"), chat, "carol")
	require.NoError(t, err)
	assert.Equal(t, store.RoleViewer, role)
	assert.False(t, store.MayWrite(role))
	// Not even the dev token writes in it.
	role, err = f.svc.chatRole(context.WithValue(loginCtx("local"), scopeKey{}, ScopeAll), chat, "local")
	require.NoError(t, err)
	assert.Equal(t, store.RoleViewer, role)
	role, err = f.svc.chatRole(loginCtx("bob"), chat, "bob")
	require.NoError(t, err)
	assert.Empty(t, role)
	// Opening it does not make the admin a member of it.
	role, err = f.svc.openChat(adminCtx("carol"), chat, "carol", "")
	require.NoError(t, err)
	assert.Equal(t, store.RoleViewer, role)
	joined, err := f.store.ChatVisibleTo(ctx, chat.ID, "carol")
	require.NoError(t, err)
	assert.False(t, joined)

	// A system routine cannot speak with a person's assistant.
	d := ciFixerDraft()
	d.PersonalityId = "pers_x"
	_, err = f.adminClient("carol").UpdateRoutine(ctx, connect.NewRequest(&agentv1.UpdateRoutineRequest{Id: r.GetId(), Draft: d}))
	assert.Equal(t, "invalid_argument", codeOf(err))

	// Admins edit it; the change is in its history and the audit log.
	d = ciFixerDraft()
	d.Instruction = "Fix failed checks on open pull requests: one fix per failing commit."
	_, err = f.adminClient("carol").UpdateRoutine(ctx, connect.NewRequest(&agentv1.UpdateRoutineRequest{Id: r.GetId(), Draft: d}))
	require.NoError(t, err)
	revs, err := f.adminClient("alice").ListRoutineRevisions(ctx, connect.NewRequest(&agentv1.ListRoutineRevisionsRequest{Id: r.GetId()}))
	require.NoError(t, err)
	require.Len(t, revs.Msg.GetRevisions(), 2)
	assert.Equal(t, "carol", revs.Msg.GetRevisions()[0].GetChangedBy())
	assert.Contains(t, revs.Msg.GetRevisions()[0].GetInstruction(), "one fix per failing commit")
	assert.Equal(t, "alice", revs.Msg.GetRevisions()[1].GetChangedBy())

	seen := strings.Join(f.audit.seen(), "\n")
	assert.Contains(t, seen, "alice routine.create "+r.GetId())
	assert.Contains(t, seen, "carol routine.update "+r.GetId())
}

// Repositories are named in the instruction, so a draft's list is ignored, not stored.
func TestARoutineDraftsRepositoriesAreIgnored(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)
	d := digestDraft()
	d.Repos = []string{"acme/api"}
	created, err := f.clientAs("bob").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: d}))
	require.NoError(t, err)
	assert.Empty(t, created.Msg.GetRoutine().GetRepos())
}

// An admin sees everybody's routines as metadata, may pause one, and the owner is told.
func TestAnAdminSeesPersonalRoutinesAsMetadataAndMayPauseOne(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)
	created, err := f.clientAs("bob").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: digestDraft()}))
	require.NoError(t, err)
	r := created.Msg.GetRoutine()

	_, err = f.clientAs("dana").ListMemberRoutines(ctx, connect.NewRequest(&agentv1.ListMemberRoutinesRequest{}))
	assert.Equal(t, "permission_denied", codeOf(err))

	list, err := f.adminClient("alice").ListMemberRoutines(ctx, connect.NewRequest(&agentv1.ListMemberRoutinesRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetRoutines(), 1)
	m := list.Msg.GetRoutines()[0]
	assert.Equal(t, "bob", m.GetOwnerLogin())
	assert.Equal(t, "weekdays at 09:00", m.GetScheduleText())

	// The admin still cannot read it or its chat.
	_, err = f.adminClient("alice").GetRoutine(ctx, connect.NewRequest(&agentv1.GetRoutineRequest{Id: r.GetId()}))
	assert.Equal(t, "not_found", codeOf(err))
	chat, err := f.store.GetChat(ctx, r.GetChatId())
	require.NoError(t, err)
	role, err := f.svc.chatRole(adminCtx("alice"), chat, "alice")
	require.NoError(t, err)
	assert.Empty(t, role, "an admin never reads a person's routine chat")

	paused, err := f.adminClient("alice").PauseRoutine(ctx, connect.NewRequest(&agentv1.PauseRoutineRequest{Id: r.GetId()}))
	require.NoError(t, err)
	assert.False(t, paused.Msg.GetRoutine().GetEnabled())
	assert.Empty(t, paused.Msg.GetRoutine().GetInstruction(), "an admin gets metadata back, not the routine")
	assert.Equal(t, "Paused by an admin.", paused.Msg.GetRoutine().GetPausedReason())

	inbox, err := f.clientAs("bob").ListNotifications(ctx, connect.NewRequest(&agentv1.ListNotificationsRequest{}))
	require.NoError(t, err)
	require.Len(t, inbox.Msg.GetNotifications(), 1)
	n := inbox.Msg.GetNotifications()[0]
	assert.Equal(t, "routine", n.GetKind())
	assert.Equal(t, "alice", n.GetActor())
	assert.Equal(t, "Paused by an admin.", n.GetDetail())

	// The owner resumes it.
	_, err = f.clientAs("bob").ResumeRoutine(ctx, connect.NewRequest(&agentv1.ResumeRoutineRequest{Id: r.GetId()}))
	require.NoError(t, err)
}

// The per-person limits: admins set them, and a person's count follows them.
func TestAdminsSetHowManyRoutinesAPersonMayHave(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)

	got, err := f.clientAs("bob").GetRoutineLimits(ctx, connect.NewRequest(&agentv1.GetRoutineLimitsRequest{}))
	require.NoError(t, err)
	assert.Equal(t, int32(10), got.Msg.GetLimits().GetMaxPerPerson())

	_, err = f.clientAs("bob").SetRoutineLimits(ctx, connect.NewRequest(&agentv1.SetRoutineLimitsRequest{
		Limits: &agentv1.RoutineLimits{MaxPerPerson: 1},
	}))
	assert.Equal(t, "permission_denied", codeOf(err))
	_, err = f.adminClient("alice").SetRoutineLimits(ctx, connect.NewRequest(&agentv1.SetRoutineLimitsRequest{
		Limits: &agentv1.RoutineLimits{MaxPerPerson: 0},
	}))
	assert.Equal(t, "invalid_argument", codeOf(err))
	_, err = f.adminClient("alice").SetRoutineLimits(ctx, connect.NewRequest(&agentv1.SetRoutineLimitsRequest{
		Limits: &agentv1.RoutineLimits{MaxPerPerson: 1, PersonDailyCapUsd: 3},
	}))
	require.NoError(t, err)

	_, err = f.clientAs("bob").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: digestDraft()}))
	require.NoError(t, err)
	_, err = f.clientAs("bob").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: digestDraft()}))
	assert.Equal(t, "resource_exhausted", codeOf(err))
	assert.Contains(t, strings.Join(f.audit.seen(), "\n"), "alice routine.limits routines")
}

func triageDraft() *agentv1.RoutineDraft {
	return &agentv1.RoutineDraft{
		Name: "Triage new issues", Timezone: "UTC", Trigger: store.RoutineKindLinear,
		Events: []string{"Issue.create", "Comment.create"}, Teams: []string{"eng"},
		Instruction: "Label each new issue by its area, and say which you could not place.",
	}
}

// A routine on Linear events is an admin's to set, in either scope: the events carry issues
// from every team the app can see. It has no schedule and no next run.
func TestARoutineOnLinearEventsIsAnAdminsToSet(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)

	for _, scope := range []string{store.RoutineScopePersonal, store.RoutineScopeSystem} {
		_, err := f.clientAs("bob").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{
			Draft: triageDraft(), Scope: scope,
		}))
		assert.Equal(t, "permission_denied", codeOf(err), scope)
	}

	created, err := f.adminClient("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{
		Draft: triageDraft(),
	}))
	require.NoError(t, err)
	r := created.Msg.GetRoutine()
	assert.Equal(t, store.RoutineKindLinear, r.GetTrigger())
	assert.Equal(t, []string{"Issue.create", "Comment.create"}, r.GetEvents())
	assert.Equal(t, []string{"ENG"}, r.GetTeams())
	assert.Empty(t, r.GetSchedule())
	assert.Equal(t, "On Linear: issue created, comment created (ENG)", r.GetScheduleText())
	assert.Nil(t, r.GetNextRunAt(), "a routine on Linear events has no next run to name")
	assert.Zero(t, r.GetPendingEvents())

	// Both is neither: a schedule beside the events is refused.
	both := triageDraft()
	both.Schedule = "0 9 * * *"
	_, err = f.adminClient("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: both}))
	assert.Equal(t, "invalid_argument", codeOf(err))
	none := triageDraft()
	none.Events = nil
	_, err = f.adminClient("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: none}))
	assert.Equal(t, "invalid_argument", codeOf(err))

	// A member's own scheduled routine cannot be moved onto Linear events.
	mine, err := f.clientAs("bob").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{
		Draft: &agentv1.RoutineDraft{Name: "Digest", Schedule: "0 9 * * *", Instruction: "Summarise."},
	}))
	require.NoError(t, err)
	_, err = f.clientAs("bob").UpdateRoutine(ctx, connect.NewRequest(&agentv1.UpdateRoutineRequest{
		Id: mine.Msg.GetRoutine().GetId(), Draft: triageDraft(),
	}))
	assert.Equal(t, "permission_denied", codeOf(err))

	// Paused and resumed, it has no schedule to read.
	_, err = f.adminClient("alice").PauseRoutine(ctx, connect.NewRequest(&agentv1.PauseRoutineRequest{Id: r.GetId()}))
	require.NoError(t, err)
	resumed, err := f.adminClient("alice").ResumeRoutine(ctx, connect.NewRequest(&agentv1.ResumeRoutineRequest{Id: r.GetId()}))
	require.NoError(t, err)
	assert.True(t, resumed.Msg.GetRoutine().GetEnabled())

	// Events waiting for it are counted on it.
	n, err := f.store.StoreRoutineEvents(ctx, store.LinearDelivery{
		DeliveryID: "delivery-1", Event: "Issue.create", TeamKey: "ENG",
		Payload: []byte(`{"type":"Issue","action":"create","data":{"identifier":"ENG-1"}}`),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	got, err := f.adminClient("alice").GetRoutine(ctx, connect.NewRequest(&agentv1.GetRoutineRequest{Id: r.GetId()}))
	require.NoError(t, err)
	assert.Equal(t, int32(1), got.Msg.GetRoutine().GetPendingEvents())
}

// A routine on Linear events may run in a chat per ticket. A ticket's chat is the routine's in
// every way a person meets it: its card names the ticket, and it is never shared.
func TestARoutineMayRunInAChatPerTicket(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)

	sched := &agentv1.RoutineDraft{Name: "Digest", Schedule: "0 9 * * *", Instruction: "Summarise.", ChatMode: "subject"}
	_, err := f.adminClient("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: sched}))
	assert.Equal(t, "invalid_argument", codeOf(err), "a routine on a schedule always runs in its own chat")

	d := triageDraft()
	d.ChatMode = store.RoutineChatModeSubject
	created, err := f.adminClient("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: d}))
	require.NoError(t, err)
	r := created.Msg.GetRoutine()
	assert.Equal(t, store.RoutineChatModeSubject, r.GetChatMode())
	plain, err := f.adminClient("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: triageDraft()}))
	require.NoError(t, err)
	assert.Equal(t, store.RoutineChatModeRoutine, plain.Msg.GetRoutine().GetChatMode(), "its own chat unless asked")

	routine, err := f.store.GetRoutine(ctx, r.GetId())
	require.NoError(t, err)
	chatID, _, err := f.store.EnsureRoutineSubjectChat(ctx, routine, "Issue:iss-12", "ENG-12 Fix login")
	require.NoError(t, err)

	got, err := f.clientAs("alice").GetRoutine(ctx, connect.NewRequest(&agentv1.GetRoutineRequest{ChatId: chatID}))
	require.NoError(t, err)
	assert.Equal(t, r.GetId(), got.Msg.GetRoutine().GetId())
	assert.Equal(t, r.GetChatId(), got.Msg.GetRoutine().GetChatId(), "the card links the routine's own chat")
	assert.Equal(t, "ENG-12 Fix login", got.Msg.GetSubjectTitle())
	own, err := f.clientAs("alice").GetRoutine(ctx, connect.NewRequest(&agentv1.GetRoutineRequest{ChatId: r.GetChatId()}))
	require.NoError(t, err)
	assert.Empty(t, own.Msg.GetSubjectTitle())

	_, err = f.clientAs("alice").ShareChat(ctx, connect.NewRequest(&agentv1.ShareChatRequest{
		ChatId: chatID, Login: "bob", Role: store.RoleViewer,
	}))
	assert.Equal(t, "failed_precondition", codeOf(err))
	_, err = f.clientAs("alice").UpdateChatSharing(ctx, connect.NewRequest(&agentv1.UpdateChatSharingRequest{
		ChatId: chatID, GeneralAccess: store.AccessMembers, GeneralRole: store.RoleViewer,
	}))
	assert.Equal(t, "failed_precondition", codeOf(err))

	// Switched back, its later runs are in its own chat; the ticket's chat stays where it is.
	back := triageDraft()
	updated, err := f.adminClient("alice").UpdateRoutine(ctx, connect.NewRequest(&agentv1.UpdateRoutineRequest{
		Id: r.GetId(), Draft: back,
	}))
	require.NoError(t, err)
	assert.Equal(t, store.RoutineChatModeRoutine, updated.Msg.GetRoutine().GetChatMode())
	still, err := f.clientAs("alice").GetRoutine(ctx, connect.NewRequest(&agentv1.GetRoutineRequest{ChatId: chatID}))
	require.NoError(t, err)
	assert.Equal(t, r.GetId(), still.Msg.GetRoutine().GetId())
}
