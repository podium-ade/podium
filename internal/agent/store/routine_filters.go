package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The conditions a routine on GitHub events puts on whose events it is given and about which
// pull requests (docs/agent.md, "On a GitHub event"). They are the GitHub App's triggers for its
// review, made a routine's own: the review that once started whenever a pull request opened is
// now a routine, and every routine on GitHub events may say the same things. An event they turn
// away is never kept for the routine, so it costs no run.

// Limits on a routine's GitHub conditions, as the App's triggers have.
const (
	MaxFilterPeople  = 200
	MaxFilterBranch  = 50
	maxBranchPattern = 200
)

// RoutineGitHubFilters are a routine's conditions on GitHub events. The zero value turns
// nothing away.
type RoutineGitHubFilters struct {
	// AllowedPeople are the only GitHub logins whose events count; empty is anyone. An event
	// whose actor is not known does not count while there is anyone in it.
	AllowedPeople []string `json:"allowed_people,omitempty"`
	// IgnoredPeople never count, whatever else matches.
	IgnoredPeople []string `json:"ignored_people,omitempty"`
	// IgnoreBots drops an event a bot account set off.
	IgnoreBots bool `json:"ignore_bots,omitempty"`
	// ExcludedBranches are patterns (CompileBranchPattern) for base branches whose pull
	// requests' events do not count. An event about a pull request whose base is not known
	// does not count while there are any.
	ExcludedBranches []string `json:"excluded_branches,omitempty"`
	// SkipDrafts drops an event about a draft pull request. Marking it ready is its own event.
	SkipDrafts bool `json:"skip_drafts,omitempty"`
}

// IsZero reports whether f turns nothing away.
func (f RoutineGitHubFilters) IsZero() bool {
	return len(f.AllowedPeople) == 0 && len(f.IgnoredPeople) == 0 && !f.IgnoreBots &&
		len(f.ExcludedBranches) == 0 && !f.SkipDrafts
}

// GitHubEventFacts are what a delivery says about who set it off and which pull request it
// is about, as a routine's conditions read them.
type GitHubEventFacts struct {
	// Actor is the login that set the event off: who opened, pushed or reviewed, or the author
	// of the pull request a commit's checks are about. "" when it is not known.
	Actor string
	// ActorBot is whether Actor is a bot account.
	ActorBot bool
	// Pull is whether the event is about a pull request; Base is its base branch, "" when not
	// known, and Draft whether it is a draft.
	Pull  bool
	Base  string
	Draft bool
}

// Admits reports whether an event with these facts counts for a routine with conditions f,
// and why not when it does not.
func (f RoutineGitHubFilters) Admits(e GitHubEventFacts) (bool, string) {
	actor := normalizeLogin(e.Actor)
	if f.IgnoreBots && (e.ActorBot || strings.HasSuffix(actor, "[bot]")) {
		return false, "bot account"
	}
	if actor != "" && containsLogin(f.IgnoredPeople, actor) {
		return false, "ignored person"
	}
	if len(f.AllowedPeople) > 0 && (actor == "" || !containsLogin(f.AllowedPeople, actor)) {
		return false, "person not allowed"
	}
	if !e.Pull {
		return true, ""
	}
	if f.SkipDrafts && e.Draft {
		return false, "draft"
	}
	if len(f.ExcludedBranches) > 0 {
		if e.Base == "" {
			return false, "base branch unknown"
		}
		for _, p := range f.ExcludedBranches {
			if re, err := CompileBranchPattern(p); err == nil && re.MatchString(e.Base) {
				return false, "excluded branch"
			}
		}
	}
	return true, ""
}

// NormalizeGitHubFilters trims and dedupes f's lists and refuses what an event could never
// match: a login GitHub would not issue, a branch pattern that does not compile.
func NormalizeGitHubFilters(f RoutineGitHubFilters) (RoutineGitHubFilters, error) {
	var err error
	if f.AllowedPeople, err = NormalizeLogins("allowed people", f.AllowedPeople); err != nil {
		return RoutineGitHubFilters{}, fmt.Errorf("%w: %v", ErrInvalidRoutine, err)
	}
	if f.IgnoredPeople, err = NormalizeLogins("ignored people", f.IgnoredPeople); err != nil {
		return RoutineGitHubFilters{}, fmt.Errorf("%w: %v", ErrInvalidRoutine, err)
	}
	if f.ExcludedBranches, err = NormalizeBranchPatterns(f.ExcludedBranches); err != nil {
		return RoutineGitHubFilters{}, fmt.Errorf("%w: %v", ErrInvalidRoutine, err)
	}
	return f, nil
}

// githubFiltersJSON is f as routines.github_filters holds it.
func githubFiltersJSON(f RoutineGitHubFilters) []byte {
	raw, err := json.Marshal(f)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// githubFiltersFrom reads routines.github_filters. A value that does not read is no
// conditions; nothing writes one.
func githubFiltersFrom(raw []byte) RoutineGitHubFilters {
	var f RoutineGitHubFilters
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &f)
	}
	return f
}

// loginRe is a GitHub login, with the [bot] suffix an App's account carries.
var loginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)

// NormalizeLogins trims, dedupes and checks a list of GitHub logins; what names the list in
// an error.
func NormalizeLogins(what string, in []string) ([]string, error) {
	if len(in) > MaxFilterPeople {
		return nil, fmt.Errorf("the %s list holds at most %d logins", what, MaxFilterPeople)
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

// NormalizeBranchPatterns trims, dedupes and compiles a list of branch patterns.
func NormalizeBranchPatterns(in []string) ([]string, error) {
	if len(in) > MaxFilterBranch {
		return nil, fmt.Errorf("there can be at most %d excluded branches", MaxFilterBranch)
	}
	seen := map[string]bool{}
	var out []string
	for _, b := range in {
		b = strings.TrimSpace(b)
		if b == "" || seen[b] {
			continue
		}
		if utf8.RuneCountInString(b) > maxBranchPattern {
			return nil, fmt.Errorf("the branch pattern %q is longer than %d characters", b, maxBranchPattern)
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

func containsLogin(list []string, login string) bool {
	login = normalizeLogin(login)
	if login == "" {
		return false
	}
	for _, l := range list {
		if normalizeLogin(l) == login {
			return true
		}
	}
	return false
}

func normalizeLogin(login string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(login), "@"))
}
