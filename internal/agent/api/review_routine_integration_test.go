//go:build integration

package api

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/connections"
	"github.com/podium-ade/podium/internal/agent/store"
)

// A deployment that reviewed pull requests when they were opened, before that was a routine,
// gets the "Review pull requests" routine once, from its GitHub triggers, so nothing stops
// being reviewed. A deployment that was not reviewing gets none, and neither is asked again.
func TestTheReviewOnOpenBecomesARoutineOnce(t *testing.T) {
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	st := newStore(t)
	require.NoError(t, st.PutSetting(ctx, connections.GitHubKey, connections.GitHub{AppID: "1", WebhookListen: "127.0.0.1:9000"}))
	// The triggers row as the Connections page saved it, with fields that are no longer read.
	require.NoError(t, st.PutSetting(ctx, connections.GitHubFiltersKey, map[string]any{
		"allowed_actors": []string{"alice"}, "ignored_actors": []string{"mallory"}, "ignore_bots": true,
		"excluded_branches": []string{"release/*"}, "trigger_phrases": []string{"/review"}, "reply_mode": "bot_threads",
	}))
	require.NoError(t, connections.EnsureReviewRoutine(ctx, st, quiet))
	require.NoError(t, connections.EnsureReviewRoutine(ctx, st, quiet))
	all, err := st.ListRoutines(ctx, "")
	require.NoError(t, err)
	require.Len(t, all, 1, "made once")
	r := all[0]
	assert.Equal(t, "Review pull requests", r.Name)
	assert.True(t, r.System())
	assert.Equal(t, store.RoutineKindGitHub, r.Kind)
	assert.Equal(t, []string{"pull_request.opened", "pull_request.ready_for_review"}, r.Events)
	assert.Equal(t, store.RoutineChatModeSubject, r.ChatMode)
	assert.Equal(t, store.RoutineReviewPlaybook, r.Playbook)
	assert.Equal(t, store.RoutineGitHubFilters{
		AllowedPeople: []string{"alice"}, IgnoredPeople: []string{"mallory"}, IgnoreBots: true,
		ExcludedBranches: []string{"release/*"}, SkipDrafts: true,
	}, r.GitHubFilters, "the triggers the review was asked through, and drafts left until they are ready")

	// Deleted, it stays deleted.
	require.NoError(t, st.DeleteRoutine(ctx, r.ID))
	require.NoError(t, connections.EnsureReviewRoutine(ctx, st, quiet))
	all, err = st.ListRoutines(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, all)

	for name, setup := range map[string]func(st *store.Store){
		"no GitHub App": func(*store.Store) {},
		"no webhook": func(st *store.Store) {
			require.NoError(t, st.PutSetting(ctx, connections.GitHubKey, connections.GitHub{AppID: "1"}))
		},
		"the review on open turned off": func(st *store.Store) {
			require.NoError(t, st.PutSetting(ctx, connections.GitHubKey, connections.GitHub{AppID: "1", WebhookListen: ":9000"}))
			require.NoError(t, st.PutSetting(ctx, connections.GitHubFiltersKey, map[string]any{"no_auto_review": true}))
		},
	} {
		t.Run(name, func(t *testing.T) {
			st := newStore(t)
			setup(st)
			require.NoError(t, connections.EnsureReviewRoutine(ctx, st, quiet))
			all, err := st.ListRoutines(ctx, "")
			require.NoError(t, err)
			assert.Empty(t, all)
		})
	}
}
