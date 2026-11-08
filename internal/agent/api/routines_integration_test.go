//go:build integration

package api

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

func digestDraft() *agentv1.RoutineDraft {
	return &agentv1.RoutineDraft{
		Name: "PR digest", Schedule: "0 9 * * 1-5", Timezone: "America/Toronto",
		Instruction: "Report pull requests whose CI failed.",
	}
}

// A routine is its owner's: created with its own chat, listed for them alone, and nobody
// else's to read, run or delete.
func TestARoutineIsItsOwnersAlone(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)

	created, err := f.clientAs("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: digestDraft()}))
	require.NoError(t, err)
	r := created.Msg.GetRoutine()
	assert.Equal(t, "weekdays at 09:00", r.GetScheduleText())
	assert.Equal(t, "alice", r.GetOwnerLogin())
	assert.True(t, r.GetEnabled())
	require.NotEmpty(t, r.GetChatId())

	// Its chat is in alice's list, marked as the routine's.
	chats := f.listAs(t, "alice", "")
	require.Contains(t, chats, r.GetChatId())
	assert.Equal(t, r.GetId(), chats[r.GetChatId()].GetRoutineId())

	byChat, err := f.clientAs("alice").GetRoutine(ctx, connect.NewRequest(&agentv1.GetRoutineRequest{ChatId: r.GetChatId()}))
	require.NoError(t, err)
	assert.Equal(t, r.GetId(), byChat.Msg.GetRoutine().GetId())

	list, err := f.clientAs("bob").ListRoutines(ctx, connect.NewRequest(&agentv1.ListRoutinesRequest{}))
	require.NoError(t, err)
	assert.Empty(t, list.Msg.GetRoutines())
	_, err = f.clientAs("bob").GetRoutine(ctx, connect.NewRequest(&agentv1.GetRoutineRequest{Id: r.GetId()}))
	assert.Equal(t, "not_found", codeOf(err))
	_, err = f.clientAs("bob").GetRoutine(ctx, connect.NewRequest(&agentv1.GetRoutineRequest{ChatId: r.GetChatId()}))
	assert.Equal(t, "not_found", codeOf(err))
	_, err = f.clientAs("bob").DeleteRoutine(ctx, connect.NewRequest(&agentv1.DeleteRoutineRequest{Id: r.GetId()}))
	assert.Equal(t, "not_found", codeOf(err))
	_, err = f.clientAs("bob").PauseRoutine(ctx, connect.NewRequest(&agentv1.PauseRoutineRequest{Id: r.GetId()}))
	assert.Equal(t, "not_found", codeOf(err))

	paused, err := f.clientAs("alice").PauseRoutine(ctx, connect.NewRequest(&agentv1.PauseRoutineRequest{Id: r.GetId()}))
	require.NoError(t, err)
	assert.False(t, paused.Msg.GetRoutine().GetEnabled())
	resumed, err := f.clientAs("alice").ResumeRoutine(ctx, connect.NewRequest(&agentv1.ResumeRoutineRequest{Id: r.GetId()}))
	require.NoError(t, err)
	assert.True(t, resumed.Msg.GetRoutine().GetEnabled())
}

// A routine's chat is never shared, by name, by members or by link.
func TestARoutinesChatIsNeverShared(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)
	created, err := f.clientAs("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: digestDraft()}))
	require.NoError(t, err)
	chatID := created.Msg.GetRoutine().GetChatId()

	_, err = f.clientAs("alice").ShareChat(ctx, connect.NewRequest(&agentv1.ShareChatRequest{
		ChatId: chatID, Login: "bob", Role: store.RoleViewer,
	}))
	assert.Equal(t, "failed_precondition", codeOf(err))
	_, err = f.clientAs("alice").UpdateChatSharing(ctx, connect.NewRequest(&agentv1.UpdateChatSharingRequest{
		ChatId: chatID, GeneralAccess: store.AccessMembers, GeneralRole: store.RoleViewer,
	}))
	assert.Equal(t, "failed_precondition", codeOf(err))
	assert.NotContains(t, f.listAs(t, "bob", ""), chatID)
}

// The schedule is checked by the server: a cron it cannot read, a timezone that is not one, and
// anything tighter than every 15 minutes are refused in words a person can act on.
func TestARoutinesScheduleIsChecked(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)
	for _, tc := range []struct{ schedule, timezone, want string }{
		{"every morning", "UTC", "not a cron expression"},
		{"0 9 * * *", "Mars/Olympus", "not a timezone"},
		{"*/5 * * * *", "UTC", "at most every 15 minutes"},
	} {
		d := digestDraft()
		d.Schedule, d.Timezone = tc.schedule, tc.timezone
		_, err := f.clientAs("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: d}))
		require.Error(t, err, tc.schedule)
		assert.Equal(t, "invalid_argument", codeOf(err), tc.schedule)
		assert.Contains(t, err.Error(), tc.want, tc.schedule)
	}
	list, err := f.clientAs("alice").ListRoutines(ctx, connect.NewRequest(&agentv1.ListRoutinesRequest{}))
	require.NoError(t, err)
	assert.Empty(t, list.Msg.GetRoutines(), "a refused routine leaves nothing behind")
}

// A proposed note is the owner's to keep or dismiss.
func TestAProposedNoteIsKeptOrDismissed(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)
	created, err := f.clientAs("alice").CreateRoutine(ctx, connect.NewRequest(&agentv1.CreateRoutineRequest{Draft: digestDraft()}))
	require.NoError(t, err)
	id := created.Msg.GetRoutine().GetId()
	require.NoError(t, f.store.ProposeRoutineNote(ctx, id, "skip drafts"))

	kept, err := f.clientAs("alice").ResolveRoutineNote(ctx, connect.NewRequest(&agentv1.ResolveRoutineNoteRequest{Id: id, Accept: true}))
	require.NoError(t, err)
	assert.Equal(t, "- skip drafts", kept.Msg.GetRoutine().GetNotes())
	assert.Empty(t, kept.Msg.GetRoutine().GetProposedNote())

	require.NoError(t, f.store.ProposeRoutineNote(ctx, id, "also check acme/web"))
	dismissed, err := f.clientAs("alice").ResolveRoutineNote(ctx, connect.NewRequest(&agentv1.ResolveRoutineNoteRequest{Id: id}))
	require.NoError(t, err)
	assert.Equal(t, "- skip drafts", dismissed.Msg.GetRoutine().GetNotes())
	assert.Empty(t, dismissed.Msg.GetRoutine().GetProposedNote())
}

// A routine an assistant sets up for a person is checked as their own would be, and a refusal
// is the reason alone, in words the assistant can pass on.
func TestARoutineSetUpForAPersonIsCheckedAsTheirOwn(t *testing.T) {
	ctx := context.Background()
	f := newSharingFixture(t)

	_, err := f.svc.CreateRoutineFor(ctx, "alice", store.RoutineDraft{
		Name: "Too often", Schedule: "*/5 * * * *", Instruction: "Look.",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most every 15 minutes")
	assert.NotContains(t, err.Error(), "invalid_argument")

	r, err := f.svc.CreateRoutineFor(ctx, "alice", store.RoutineDraft{
		Name: "PR digest", Schedule: "0 9 * * 1-5", Instruction: "Report failing PRs.",
	})
	require.NoError(t, err)
	assert.Equal(t, "alice", r.OwnerLogin)
	assert.Equal(t, "UTC", r.Timezone)
	list, err := f.clientAs("alice").ListRoutines(ctx, connect.NewRequest(&agentv1.ListRoutinesRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetRoutines(), 1)
}
