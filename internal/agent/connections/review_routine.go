package connections

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/podium-ade/podium/internal/agent/store"
)

// The review a pull request got when it was opened, or marked ready, was the GitHub App's own
// until it became a routine (0058): "Review pull requests", a system routine on those two
// events that starts the repository's reviewer right away. It is a template in the
// new-routine dialog, and a deployment that was reviewing pull requests before gets one made
// for it, once, from what its GitHub triggers said, so nothing stops being reviewed.

// ReviewRoutineMadeKey is the setting that records the review routine was made, or that there
// was nothing to make it from. Whoever writes it first makes it.
const ReviewRoutineMadeKey = "routines.review_routine_made"

// ReviewRoutineDraft is the "Review pull requests" routine, as the template and the one-off
// migration both make it: on a pull request opened or marked ready, not a draft, or asked for
// with /review, in each pull request's conversation, starting the repository's reviewer with
// no assistant's turn first.
func ReviewRoutineDraft(filters store.RoutineGitHubFilters) store.RoutineDraft {
	filters.SkipDrafts = true
	filters.Commands = []string{ReviewCommand}
	return store.RoutineDraft{
		Name:          "Review pull requests",
		Kind:          store.RoutineKindGitHub,
		Events:        []string{"pull_request.opened", "pull_request.ready_for_review", store.GitHubCommandEvent},
		ChatMode:      store.RoutineChatModeSubject,
		Timezone:      "UTC",
		Instruction:   ReviewRoutineInstruction,
		Playbook:      store.RoutineReviewPlaybook,
		GitHubFilters: filters,
	}
}

// ReviewRoutineInstruction is what the review routine asks its reviewer. Posting the review is
// in it because nothing else posts one: the playbook does, with the GitHub token its task has,
// and the instruction is where anybody reads and changes how.
const ReviewRoutineInstruction = "Review this pull request: what it changes, whether it does what it says, " +
	"and what should be fixed before it merges. Post the review on the pull request yourself with `gh pr review` " +
	"(--approve, --request-changes or --comment), and put a finding about one place in the diff on that line " +
	"(`gh api repos/{owner}/{repo}/pulls/{number}/reviews` with comments). If you were asked to review it before, " +
	"look only at what changed since your last review."

// ReviewCommand is the command a review is asked for with: `/review`, or `@<app> review`.
const ReviewCommand = "review"

// CommandRoutineMadeKey records that the review command became a routine. Whoever writes it
// first makes it.
const CommandRoutineMadeKey = "routines.review_command_made"

// EnsureCommandRoutine makes the review command a routine, once, for a deployment whose GitHub
// App answered `/review` before commands were routines: "Review when asked", on that command
// alone, so the routine that reviews pull requests when they open is left as its owner made
// it. Somebody who had not connected GitHub could ask unless the identity policy refused them,
// and that refusal becomes the routine's "Who counts: People in Podium". Nothing is made where a
// routine already answers a command: a deployment that made its review routine from the
// template since has /review in it.
func EnsureCommandRoutine(ctx context.Context, st *store.Store, logger *slog.Logger) error {
	var made struct{}
	if err := st.GetSetting(ctx, CommandRoutineMadeKey, &made); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var app GitHub
	if err := st.GetSetting(ctx, GitHubKey, &app); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	answering := strings.TrimSpace(app.AppID) != "" && strings.TrimSpace(app.WebhookListen) != ""
	if answering {
		all, err := st.ListRoutines(ctx, "")
		if err != nil {
			return err
		}
		for _, r := range all {
			if r.Kind == store.RoutineKindGitHub && slices.Contains(r.Events, store.GitHubCommandEvent) {
				answering = false
			}
		}
	}
	refused, err := outsidersWereRefused(ctx, st)
	if err != nil {
		return err
	}
	claimed, err := st.ClaimSetting(ctx, CommandRoutineMadeKey, map[string]any{"made": answering})
	if err != nil || !claimed || !answering {
		return err
	}
	draft := store.RoutineDraft{
		Name:        "Review when asked",
		Kind:        store.RoutineKindGitHub,
		Events:      []string{store.GitHubCommandEvent},
		ChatMode:    store.RoutineChatModeSubject,
		Timezone:    "UTC",
		Instruction: ReviewRoutineInstruction,
		Playbook:    store.RoutineReviewPlaybook,
		GitHubFilters: store.RoutineGitHubFilters{
			Commands:         []string{ReviewCommand},
			PodiumPeopleOnly: refused,
		},
	}
	r, err := st.CreateSystemRoutine(ctx, "podium", draft)
	if err != nil {
		if derr := st.DeleteSetting(context.WithoutCancel(ctx), CommandRoutineMadeKey); derr != nil {
			logger.WarnContext(ctx, "releasing the command routine's claim failed", "error", derr)
		}
		return fmt.Errorf("making the review command's routine: %w", err)
	}
	logger.InfoContext(ctx, "the review command is now a routine", "routine_id", r.ID, "name", r.Name)
	return nil
}

// outsidersWereRefused reports whether the identity policy refused the review command to
// somebody who had not connected GitHub, from the policy as it was saved: the store no longer
// reads the field.
func outsidersWereRefused(ctx context.Context, st *store.Store) (bool, error) {
	var legacy struct {
		GitHubOutsiders string `json:"github_outsiders"`
	}
	if err := st.GetSetting(ctx, store.IdentityPolicyKey, &legacy); err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	return legacy.GitHubOutsiders == store.OutsidersRefuse, nil
}

// EnsureReviewRoutine makes the review routine for a deployment that reviewed pull requests
// when they were opened before that was a routine: one with the GitHub App's webhook set up and
// the review on open not turned off. It does that once, on whichever conductor gets there
// first, and never again: a routine an admin then deletes stays deleted.
func EnsureReviewRoutine(ctx context.Context, st *store.Store, logger *slog.Logger) error {
	var made struct{}
	if err := st.GetSetting(ctx, ReviewRoutineMadeKey, &made); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var app GitHub
	err := st.GetSetting(ctx, GitHubKey, &app)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	filters, err := LoadGitHubFilters(ctx, st)
	if err != nil {
		return err
	}
	reviewing := strings.TrimSpace(app.AppID) != "" && strings.TrimSpace(app.WebhookListen) != "" && !filters.NoAutoReview
	refused, err := outsidersWereRefused(ctx, st)
	if err != nil {
		return err
	}
	claimed, err := st.ClaimSetting(ctx, ReviewRoutineMadeKey, map[string]any{"made": reviewing})
	if err != nil || !claimed || !reviewing {
		return err
	}
	draft := ReviewRoutineDraft(store.RoutineGitHubFilters{
		AllowedPeople: filters.AllowedActors, IgnoredPeople: filters.IgnoredActors,
		IgnoreBots: filters.IgnoreBots, ExcludedBranches: filters.ExcludedBranches,
	})
	if refused {
		// Its conditions are about whose pull requests are reviewed when they open, which
		// anybody's were. Who could ask with /review was narrower: only people in Podium. So the
		// command is left out here, and EnsureCommandRoutine, which runs next, makes it a routine
		// of its own with People in Podium, rather than this one answering anybody.
		draft.Events = slices.DeleteFunc(draft.Events, func(e string) bool { return e == store.GitHubCommandEvent })
		draft.GitHubFilters.Commands = nil
	}
	r, err := st.CreateSystemRoutine(ctx, "podium", draft)
	if err != nil {
		// The next start tries again.
		if derr := st.DeleteSetting(context.WithoutCancel(ctx), ReviewRoutineMadeKey); derr != nil {
			logger.WarnContext(ctx, "releasing the review routine's claim failed", "error", derr)
		}
		return fmt.Errorf("making the review routine: %w", err)
	}
	logger.InfoContext(ctx, "the review on a pull request being opened is now a routine",
		"routine_id", r.ID, "name", r.Name)
	return nil
}
