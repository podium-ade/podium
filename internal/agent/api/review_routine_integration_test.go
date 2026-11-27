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
	assert.Equal(t, []string{"pull_request.opened", "pull_request.ready_for_review", store.GitHubCommandEvent}, r.Events)
	assert.Equal(t, store.RoutineChatModeSubject, r.ChatMode)
	assert.Equal(t, store.RoutineReviewPlaybook, r.Playbook)
	assert.Equal(t, store.RoutineGitHubFilters{
		AllowedPeople: []string{"alice"}, IgnoredPeople: []string{"mallory"}, IgnoreBots: true,
		ExcludedBranches: []string{"release/*"}, SkipDrafts: true, Commands: []string{"review"},
	}, r.GitHubFilters, "the triggers the review was asked through, drafts left until they are ready, and /review")

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

// A deployment whose App answered /review before commands were routines gets "Review when
// asked", once: on the command alone, and for people in Podium only when the identity policy
// refused everybody else. One that already has a routine on a command gets nothing.
func TestTheReviewCommandBecomesARoutineOnce(t *testing.T) {
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := connections.GitHub{AppID: "1", WebhookListen: "127.0.0.1:9000"}

	for name, tc := range map[string]struct {
		policy      map[string]any
		podiumsOnly bool
	}{
		"anybody could ask":          {nil, false},
		"outsiders were refused":     {map[string]any{"github_outsiders": "refuse", "slack_outsiders": "answer"}, true},
		"outsiders could ask, saved": {map[string]any{"github_outsiders": "review"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			st := newStore(t)
			require.NoError(t, st.PutSetting(ctx, connections.GitHubKey, app))
			if tc.policy != nil {
				require.NoError(t, st.PutSetting(ctx, store.IdentityPolicyKey, tc.policy))
			}
			require.NoError(t, connections.EnsureCommandRoutine(ctx, st, quiet))
			require.NoError(t, connections.EnsureCommandRoutine(ctx, st, quiet))
			all, err := st.ListRoutines(ctx, "")
			require.NoError(t, err)
			require.Len(t, all, 1, "made once")
			r := all[0]
			assert.Equal(t, "Review when asked", r.Name)
			assert.Equal(t, []string{store.GitHubCommandEvent}, r.Events)
			assert.Equal(t, store.RoutineReviewPlaybook, r.Playbook)
			assert.Equal(t, store.RoutineChatModeSubject, r.ChatMode)
			assert.Equal(t, store.RoutineGitHubFilters{Commands: []string{"review"}, PodiumPeopleOnly: tc.podiumsOnly}, r.GitHubFilters)

			// The policy still reads, without the field it no longer has.
			_, err = st.GetIdentityPolicy(ctx)
			require.NoError(t, err)
		})
	}

	t.Run("a routine already answers a command", func(t *testing.T) {
		st := newStore(t)
		require.NoError(t, st.PutSetting(ctx, connections.GitHubKey, app))
		require.NoError(t, connections.EnsureReviewRoutine(ctx, st, quiet))
		require.NoError(t, connections.EnsureCommandRoutine(ctx, st, quiet))
		all, err := st.ListRoutines(ctx, "")
		require.NoError(t, err)
		require.Len(t, all, 1)
		assert.Equal(t, "Review pull requests", all[0].Name, "the template's routine answers /review already")
	})

	// The order the conductor runs them in, on a deployment that refused outsiders the command:
	// the review on open keeps reviewing anybody's pull request, and /review is its own routine
	// for people in Podium, so the refusal is not lost to the template's /review.
	t.Run("outsiders were refused, and the review routine is made first", func(t *testing.T) {
		st := newStore(t)
		require.NoError(t, st.PutSetting(ctx, connections.GitHubKey, app))
		require.NoError(t, st.PutSetting(ctx, store.IdentityPolicyKey, map[string]any{"github_outsiders": "refuse"}))
		require.NoError(t, connections.EnsureReviewRoutine(ctx, st, quiet))
		require.NoError(t, connections.EnsureCommandRoutine(ctx, st, quiet))
		all, err := st.ListRoutines(ctx, "")
		require.NoError(t, err)
		byName := map[string]store.Routine{}
		for _, r := range all {
			byName[r.Name] = r
		}
		require.Len(t, byName, 2)
		onOpen := byName["Review pull requests"]
		assert.Equal(t, []string{"pull_request.opened", "pull_request.ready_for_review"}, onOpen.Events)
		assert.Empty(t, onOpen.GitHubFilters.Commands)
		assert.False(t, onOpen.GitHubFilters.PodiumPeopleOnly, "anybody's pull request is still reviewed when it opens")
		asked := byName["Review when asked"]
		assert.Equal(t, []string{store.GitHubCommandEvent}, asked.Events)
		assert.Equal(t, store.RoutineGitHubFilters{Commands: []string{"review"}, PodiumPeopleOnly: true}, asked.GitHubFilters)
	})

	t.Run("no webhook", func(t *testing.T) {
		st := newStore(t)
		require.NoError(t, st.PutSetting(ctx, connections.GitHubKey, connections.GitHub{AppID: "1"}))
		require.NoError(t, connections.EnsureCommandRoutine(ctx, st, quiet))
		all, err := st.ListRoutines(ctx, "")
		require.NoError(t, err)
		assert.Empty(t, all)
	})
}
