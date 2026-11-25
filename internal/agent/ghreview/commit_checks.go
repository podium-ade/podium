package ghreview

// "All checks finished" (docs/agent.md, "On a GitHub event"). GitHub has no delivery that
// says every check on a commit is done: a repository's CI is any number of workflows, check
// suites and commit statuses, and each finishes on its own. A routine that fixes CI wants one
// run per commit, once the last of them is done — not one per workflow, most of them started
// while the rest still run.
//
// So whenever a workflow run, check suite or status on a commit finishes, the commit's checks
// are read from GitHub, and when none is still running the commit is kept for routines as one
// event, "checks.completed" — and "checks.failed" as well when at least one failed. Its
// delivery id is the commit and the outcome, so however many finishes see it done, each
// routine is given it once.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

// RoutineWants reports whether a routine on GitHub events asks for one of events on repo.
// The conductor's store implements it; nil asks nothing and reads every commit.
type RoutineWants func(ctx context.Context, repo string, events []string) (bool, error)

// CommitCheckEvents are the events a commit whose checks all finished answers to.
var CommitCheckEvents = []string{"checks.failed", "checks.completed"}

const (
	// checksSettle is how long after a finish the commit's checks are read: GitHub's API can
	// trail its webhook by a moment, and an Actions workflow finishing sends a workflow_run
	// and a check_suite delivery together, which then cost one read.
	checksSettle = 5 * time.Second
	// checksReadTimeout bounds one read of a commit's checks, every page of it.
	checksReadTimeout = time.Minute
	// maxCheckPages bounds the pages of check runs or suites read for one commit, 100 each.
	maxCheckPages = 10
	// maxFailedChecks is how many failed checks a run is told about; the rest are counted.
	maxFailedChecks = 30
)

// commitFinish is a delivery that says something on a commit finished.
type commitFinish struct {
	repo    string
	sha     string
	install int64
	// pulls is the pull requests the delivery names for the commit.
	pulls []ghPullRef
}

// finishedOnCommit reads a delivery as a finish on a commit: a completed workflow run or
// check suite, or a status that is no longer pending. ok is false for anything else.
func finishedOnCommit(event string, body []byte) (commitFinish, bool) {
	var p struct {
		Action       string `json:"action"`
		SHA          string `json:"sha"`
		State        string `json:"state"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		WorkflowRun struct {
			HeadSHA      string      `json:"head_sha"`
			PullRequests []ghPullRef `json:"pull_requests"`
		} `json:"workflow_run"`
		CheckSuite struct {
			HeadSHA      string      `json:"head_sha"`
			PullRequests []ghPullRef `json:"pull_requests"`
		} `json:"check_suite"`
	}
	if json.Unmarshal(body, &p) != nil || p.Repository.FullName == "" || p.Installation.ID == 0 {
		return commitFinish{}, false
	}
	f := commitFinish{repo: p.Repository.FullName, install: p.Installation.ID}
	switch event {
	case "workflow_run":
		if p.Action != "completed" {
			return commitFinish{}, false
		}
		f.sha, f.pulls = p.WorkflowRun.HeadSHA, p.WorkflowRun.PullRequests
	case "check_suite":
		if p.Action != "completed" {
			return commitFinish{}, false
		}
		f.sha, f.pulls = p.CheckSuite.HeadSHA, p.CheckSuite.PullRequests
	case "status":
		if p.State == "" || p.State == "pending" {
			return commitFinish{}, false
		}
		f.sha = p.SHA
	default:
		return commitFinish{}, false
	}
	if f.sha == "" {
		return commitFinish{}, false
	}
	return f, true
}

// watchCommitChecks reads a finished delivery's commit, after checksSettle, and keeps it for
// routines when its checks are all done. The webhook is not kept waiting for it. A finish on
// a commit already being read is read again once that read ends, so the last finish is
// never lost to one before it.
func (s *Source) watchCommitChecks(ctx context.Context, event string, body []byte) {
	if s.routines == nil {
		return
	}
	f, ok := finishedOnCommit(event, body)
	if !ok {
		return
	}
	key := strings.ToLower(f.repo) + "@" + f.sha
	s.mu.Lock()
	if s.checking == nil {
		s.checking = map[string]bool{}
	}
	if _, reading := s.checking[key]; reading {
		s.checking[key] = true
		s.mu.Unlock()
		return
	}
	s.checking[key] = false
	s.mu.Unlock()

	ctx = context.WithoutCancel(ctx)
	s.checksWG.Add(1)
	go func() {
		defer s.checksWG.Done()
		for {
			if s.checksSettle > 0 {
				time.Sleep(s.checksSettle)
			}
			s.keepFinishedCommit(ctx, f)
			s.mu.Lock()
			again := s.checking[key]
			if !again {
				delete(s.checking, key)
				s.mu.Unlock()
				return
			}
			s.checking[key] = false
			s.mu.Unlock()
		}
	}()
}

// keepFinishedCommit reads f's commit and keeps it for routines if every check is done.
func (s *Source) keepFinishedCommit(ctx context.Context, f commitFinish) {
	ctx, cancel := context.WithTimeout(ctx, checksReadTimeout)
	defer cancel()
	if s.routinesWant != nil {
		want, err := s.routinesWant(ctx, f.repo, CommitCheckEvents)
		if err != nil {
			s.logger.WarnContext(ctx, "github: asking whether routines want a commit's checks failed",
				"repo", f.repo, "sha", f.sha, "error", err)
			return
		}
		if !want {
			return
		}
	}
	get := func(ctx context.Context, path string, out any) error {
		return s.client.do(ctx, f.install, http.MethodGet, path, nil, out)
	}
	sum, err := readCommitChecks(ctx, get, f.repo, f.sha, s.slug)
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading a commit's checks failed; this finish is lost to routines",
			"repo", f.repo, "sha", f.sha, "error", err)
		return
	}
	if sum.pending > 0 || sum.finished() == 0 {
		s.logger.DebugContext(ctx, "github: a commit's checks are not all finished",
			"repo", f.repo, "sha", f.sha, "pending", sum.pending, "finished", sum.finished())
		return
	}
	pull := commitPull(ctx, get, f)
	d := sum.delivery(f.repo, f.sha, pull, s.now())
	n, err := s.routines(ctx, d)
	if err != nil {
		s.logger.ErrorContext(ctx, "github: keeping a commit's finished checks for routines failed",
			"repo", f.repo, "sha", f.sha, "error", err)
		return
	}
	if n > 0 {
		s.logger.InfoContext(ctx, "github: kept a commit's finished checks for routines",
			"repo", f.repo, "sha", f.sha, "events", d.Events, "failed", len(sum.failed), "routines", n)
	}
}

// getJSON is one GET of the GitHub API, decoded into out.
type getJSON func(ctx context.Context, path string, out any) error

// failedCheck is one check that failed, as a run is told about it.
type failedCheck struct {
	Name       string `json:"name"`
	App        string `json:"app,omitempty"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url,omitempty"`
	// JobID is a GitHub Actions check run's job, whose log get_job_log reads.
	JobID int64 `json:"job_id,omitempty"`
}

// commitChecks is what a commit's checks came to.
type commitChecks struct {
	pending int
	passed  int
	failed  []failedCheck
	branch  string
}

func (c commitChecks) finished() int { return c.passed + len(c.failed) }

// readCommitChecks reads every check on a commit: its check suites (an Actions workflow that
// has not started a job yet has a suite and no runs), its check runs, and its commit
// statuses. The App's own are left out: a check the App reports is not CI it should fix.
func readCommitChecks(ctx context.Context, get getJSON, repo, sha, ownSlug string) (commitChecks, error) {
	var out commitChecks
	base := fmt.Sprintf("/repos/%s/commits/%s", repo, sha)

	for page := 1; page <= maxCheckPages; page++ {
		var res struct {
			Total  int `json:"total_count"`
			Suites []struct {
				Status     string `json:"status"`
				HeadBranch string `json:"head_branch"`
				Runs       int    `json:"latest_check_runs_count"`
				App        struct {
					Slug string `json:"slug"`
				} `json:"app"`
			} `json:"check_suites"`
		}
		if err := get(ctx, fmt.Sprintf("%s/check-suites?per_page=100&page=%d", base, page), &res); err != nil {
			return out, err
		}
		for _, cs := range res.Suites {
			if out.branch == "" {
				out.branch = cs.HeadBranch
			}
			// An app that was asked for a suite and never ran a check leaves it queued for
			// good; only a suite with a check in it is waited for.
			if cs.App.Slug == ownSlug || cs.Runs == 0 {
				continue
			}
			if cs.Status != "completed" {
				out.pending++
			}
		}
		if len(res.Suites) < 100 || page*100 >= res.Total {
			break
		}
	}

	for page := 1; page <= maxCheckPages; page++ {
		var res struct {
			Total int `json:"total_count"`
			Runs  []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
				DetailsURL string `json:"details_url"`
				App        struct {
					Slug string `json:"slug"`
					Name string `json:"name"`
				} `json:"app"`
			} `json:"check_runs"`
		}
		if err := get(ctx, fmt.Sprintf("%s/check-runs?per_page=100&page=%d", base, page), &res); err != nil {
			return out, err
		}
		for _, r := range res.Runs {
			switch {
			case r.App.Slug == ownSlug:
			case r.Status != "completed":
				out.pending++
			case failed(r.Conclusion):
				fc := failedCheck{Name: r.Name, App: r.App.Name, Conclusion: r.Conclusion, URL: r.HTMLURL}
				if fc.URL == "" {
					fc.URL = r.DetailsURL
				}
				if r.App.Slug == "github-actions" {
					fc.JobID = r.ID
				}
				out.failed = append(out.failed, fc)
			default:
				out.passed++
			}
		}
		if len(res.Runs) < 100 || page*100 >= res.Total {
			break
		}
	}

	// Statuses are CI that predates checks. Reading them takes the commit statuses
	// permission; an App without it has none to wait for.
	var st struct {
		Statuses []struct {
			Context   string `json:"context"`
			State     string `json:"state"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := get(ctx, base+"/status", &st); err != nil {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || (apiErr.Status != http.StatusForbidden && apiErr.Status != http.StatusNotFound) {
			return out, err
		}
	}
	for _, s := range st.Statuses {
		switch s.State {
		case "pending":
			out.pending++
		case "failure", "error":
			out.failed = append(out.failed, failedCheck{Name: s.Context, Conclusion: s.State, URL: s.TargetURL})
		default:
			out.passed++
		}
	}
	return out, nil
}

// commitPull is the pull request a commit is the head of, with its title: GitHub lists a
// commit's pull requests, open ones with that head first. A pull request from a fork is
// found here though the delivery named none. nil when there is none.
func commitPull(ctx context.Context, get getJSON, f commitFinish) *commitPullRef {
	var prs []commitPullJSON
	if err := get(ctx, fmt.Sprintf("/repos/%s/commits/%s/pulls", f.repo, f.sha), &prs); err == nil {
		slices.SortStableFunc(prs, func(a, b commitPullJSON) int { return a.rank(f.sha) - b.rank(f.sha) })
		for _, p := range prs {
			if p.State == "open" && p.Number > 0 {
				return &commitPullRef{ref: p.ghPullRef, title: p.Title}
			}
		}
	}
	if len(f.pulls) > 0 && f.pulls[0].Number > 0 {
		return &commitPullRef{ref: f.pulls[0]}
	}
	return nil
}

// commitPullJSON is a pull request as GET commits/{sha}/pulls lists it.
type commitPullJSON struct {
	ghPullRef
	Title string `json:"title"`
	State string `json:"state"`
}

// rank orders a commit's pull requests: open before closed, and whose head the commit is
// before one it is only in.
func (p commitPullJSON) rank(sha string) int {
	r := 0
	if p.State != "open" {
		r += 2
	}
	if p.Head.SHA != sha {
		r++
	}
	return r
}

type commitPullRef struct {
	ref   ghPullRef
	title string
}

// delivery is the commit kept for routines. Its id is the commit and the outcome, so a
// commit is given to a routine once whatever the number of finishes that saw it done — and
// again only if a check run again turns its outcome around.
func (c commitChecks) delivery(repo, sha string, pull *commitPullRef, now time.Time) store.GitHubDelivery {
	outcome, action, names := "passed", "completed", []string{"checks.completed"}
	if len(c.failed) > 0 {
		outcome, action, names = "failed", "failed", CommitCheckEvents
	}
	owner, name, _ := strings.Cut(repo, "/")
	ev := routineEvent{
		Source: "github", Type: "checks", Action: action,
		URL:       fmt.Sprintf("https://github.com/%s/commit/%s/checks", repo, sha),
		CreatedAt: now.UTC().Format(time.RFC3339),
	}
	failedShown := c.failed
	if len(failedShown) > maxFailedChecks {
		failedShown = failedShown[:maxFailedChecks]
	}
	ev.Data = map[string]any{
		"repo": repo, "head_sha": sha, "head_branch": c.branch,
		"checks": c.finished(), "checks_passed": c.passed, "checks_failed": len(c.failed),
		"failed": failedShown, "pull_requests": []map[string]any{},
	}
	if pull != nil {
		ev.pull, ev.pullTitle = pull.ref.Number, pull.title
		ev.URL = PRURL(owner, name, pull.ref.Number) + "/checks"
		ev.Data["pull_requests"] = pullRefs(repo, []ghPullRef{pull.ref})
		if pull.ref.Head.Ref != "" {
			ev.Data["head_branch"] = pull.ref.Head.Ref
		}
		if pull.title != "" {
			ev.Data["pull_request_title"] = clip(pull.title, 200)
		}
	}
	payload, _ := json.Marshal(ev)
	d := store.GitHubDelivery{
		DeliveryID: "checks:" + strings.ToLower(repo) + "@" + sha + ":" + outcome,
		Events:     names, Repo: repo, Payload: payload, ReceivedAt: now.UTC(),
	}
	if ev.pull > 0 {
		d.SubjectKey = store.GitHubPullSubject(repo, ev.pull)
		d.SubjectTitle = ChatTitle(ev.pull, ev.pullTitle)
	}
	return d
}
