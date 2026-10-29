package connections

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/podium-ade/podium/internal/agent/store"
)

// GitHubFiltersKey is the settings row for the webhook filters. It is apart from the App
// row because nothing in it is a credential and it applies without a restart.
const GitHubFiltersKey = "connection.github.filters"

// Reply modes, as ghreview names them.
const (
	ReplyBotThreads = "bot_threads"
	ReplyAnyThread  = "any_thread"
	ReplyAlways     = "always"
)

const (
	maxFilterActors  = 200
	maxFilterPhrases = 20
	maxPhraseRunes   = 100
	maxBranches      = 50
	maxBranchRunes   = 200
)

// loginRe is a GitHub login, with the [bot] suffix an App's account carries.
var loginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)

// GitHubFilters decide which webhook deliveries start a turn. The zero value is the
// default: anyone, the @mention only, replies in the App's own threads, and the review
// when a pull request opens.
type GitHubFilters struct {
	AllowedActors  []string `json:"allowed_actors,omitempty"`
	IgnoredActors  []string `json:"ignored_actors,omitempty"`
	IgnoreBots     bool     `json:"ignore_bots,omitempty"`
	TriggerPhrases []string `json:"trigger_phrases,omitempty"`
	ReplyMode      string   `json:"reply_mode,omitempty"`
	NoAutoReview   bool     `json:"no_auto_review,omitempty"`
	// ExcludedBranches are patterns for base branches whose pull requests start nothing.
	// See CompileBranchPattern.
	ExcludedBranches []string  `json:"excluded_branches,omitempty"`
	SetBy            string    `json:"set_by,omitempty"`
	SetAt            time.Time `json:"set_at"`
}

// LoadGitHubFilters reads the saved filters. No row is the zero value.
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

// SaveGitHubFilters writes the filters.
func SaveGitHubFilters(ctx context.Context, st *store.Store, row GitHubFilters) error {
	return st.PutSetting(ctx, GitHubFiltersKey, row)
}

// NormalizeGitHubFilters trims and dedupes the lists and refuses what a delivery could
// never match: a login GitHub would not issue, a blank or overlong phrase, an unknown mode.
func NormalizeGitHubFilters(f GitHubFilters) (GitHubFilters, error) {
	var err error
	if f.AllowedActors, err = normalizeLogins("allowed actors", f.AllowedActors); err != nil {
		return GitHubFilters{}, err
	}
	if f.IgnoredActors, err = normalizeLogins("ignored actors", f.IgnoredActors); err != nil {
		return GitHubFilters{}, err
	}
	if f.TriggerPhrases, err = normalizePhrases(f.TriggerPhrases); err != nil {
		return GitHubFilters{}, err
	}
	if f.ExcludedBranches, err = normalizeBranches(f.ExcludedBranches); err != nil {
		return GitHubFilters{}, err
	}
	switch f.ReplyMode {
	case "":
		f.ReplyMode = ReplyBotThreads
	case ReplyBotThreads, ReplyAnyThread, ReplyAlways:
	default:
		return GitHubFilters{}, fmt.Errorf("the reply mode %q is not one of %s, %s or %s",
			f.ReplyMode, ReplyBotThreads, ReplyAnyThread, ReplyAlways)
	}
	return f, nil
}

func normalizeLogins(what string, in []string) ([]string, error) {
	if len(in) > maxFilterActors {
		return nil, fmt.Errorf("the %s list holds at most %d logins", what, maxFilterActors)
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range in {
		l = strings.TrimPrefix(strings.TrimSpace(l), "@")
		if l == "" {
			continue
		}
		if !loginRe.MatchString(l) {
			return nil, fmt.Errorf("%q in %s is not a GitHub login", l, what)
		}
		k := strings.ToLower(l)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, l)
	}
	return out, nil
}

func normalizePhrases(in []string) ([]string, error) {
	if len(in) > maxFilterPhrases {
		return nil, fmt.Errorf("there can be at most %d trigger phrases", maxFilterPhrases)
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		p = strings.Join(strings.Fields(p), " ")
		if p == "" {
			continue
		}
		if utf8.RuneCountInString(p) > maxPhraseRunes {
			return nil, fmt.Errorf("the trigger phrase %q is longer than %d characters", p, maxPhraseRunes)
		}
		k := strings.ToLower(p)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	return out, nil
}

func normalizeBranches(in []string) ([]string, error) {
	if len(in) > maxBranches {
		return nil, fmt.Errorf("there can be at most %d excluded branches", maxBranches)
	}
	seen := map[string]bool{}
	var out []string
	for _, b := range in {
		b = strings.TrimSpace(b)
		if b == "" || seen[b] {
			continue
		}
		if utf8.RuneCountInString(b) > maxBranchRunes {
			return nil, fmt.Errorf("the branch pattern %q is longer than %d characters", b, maxBranchRunes)
		}
		if _, err := CompileBranchPattern(b); err != nil {
			return nil, err
		}
		seen[b] = true
		out = append(out, b)
	}
	return out, nil
}

// CompileBranchPattern turns a branch pattern into a regexp over the whole branch name.
// A pattern between slashes, /like this/, is a regular expression and may match any part
// of the name unless it anchors itself. Anything else is a glob, as GitHub Actions reads
// one: * is any run of characters but /, ** is any run at all, ? is one character but /.
// Both are case sensitive, as branch names are.
func CompileBranchPattern(p string) (*regexp.Regexp, error) {
	if len(p) >= 2 && strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/") {
		re, err := regexp.Compile(p[1 : len(p)-1])
		if err != nil {
			return nil, fmt.Errorf("the branch pattern %q is not a regular expression: %w", p, err)
		}
		return re, nil
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
