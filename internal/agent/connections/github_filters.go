package connections

import (
	"context"
	"errors"

	"github.com/podium-ade/podium/internal/agent/store"
)

// GitHubFiltersKey is the settings row the GitHub App's triggers were saved in, before they
// were taken out: whose comments start a turn is no longer a setting (a mention, /review, or a
// reply in a thread the App is in, from anyone the conductor's gate admits), and the review on
// a pull request being opened is a routine with conditions of its own. The row is only read,
// once, to make that routine (EnsureReviewRoutine) from what it said.
const GitHubFiltersKey = "connection.github.filters"

// GitHubFilters is what the triggers row held that the review routine is made from.
type GitHubFilters struct {
	AllowedActors    []string `json:"allowed_actors,omitempty"`
	IgnoredActors    []string `json:"ignored_actors,omitempty"`
	IgnoreBots       bool     `json:"ignore_bots,omitempty"`
	ExcludedBranches []string `json:"excluded_branches,omitempty"`
	// NoAutoReview is the review on open having been turned off.
	NoAutoReview bool `json:"no_auto_review,omitempty"`
}

// LoadGitHubFilters reads the old triggers row. No row is the zero value.
func LoadGitHubFilters(ctx context.Context, st *store.Store) (GitHubFilters, error) {
	var row GitHubFilters
	err := st.GetSetting(ctx, GitHubFiltersKey, &row)
	if errors.Is(err, store.ErrNotFound) {
		return GitHubFilters{}, nil
	}
	if err != nil {
		return GitHubFilters{}, err
	}
	return row, nil
}
