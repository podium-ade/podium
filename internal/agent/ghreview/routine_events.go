package ghreview

// Routines on GitHub events (docs/agent.md, "On a GitHub event"). Every delivery the App's
// webhook verifies is offered to the routines that run on GitHub events, before and apart from
// whatever the review source does with it: a workflow run that failed is nothing the review
// source acts on, and is what a "fix failing CI" routine is for.
//
// What is kept is not the delivery. GitHub's payloads repeat the whole repository, the sender
// and the installation in every one, and a workflow run's is tens of kilobytes; a run is
// briefed with a few kilobytes of each event at most. So each kind is read into the fields a
// routine acts on — which pull request, which commit, what failed and where to read it — in
// the shape a Linear event is kept in (type, action, url, createdAt, actor.name, data), so the
// conductor briefs both alike.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/podium-ade/podium/internal/agent/store"
)

// RoutineSink keeps a delivery for the routines that run on it, and reports how many it was
// kept for. The conductor's store implements it; nil keeps nothing.
type RoutineSink func(ctx context.Context, d store.GitHubDelivery) (int, error)

// maxRoutineBodyRunes bounds a pull request's, review's or issue's text in what is kept: the
// brief trims an event to a few kilobytes anyway, and the run can read the rest from GitHub.
const maxRoutineBodyRunes = 2000

// keepForRoutines offers one verified delivery to the routines that run on GitHub events, and
// reports how many it was kept for. A delivery no routine could ask for costs no database round
// trip.
func (s *Source) keepForRoutines(ctx context.Context, event, delivery string, body []byte) int {
	if s.routines == nil {
		return 0
	}
	d, ok := RoutineDelivery(event, delivery, body, s.isAppUser, s.mentionRE, s.now())
	if !ok {
		return 0
	}
	if d.Facts.Command && d.Facts.Base == "" {
		s.completeCommand(ctx, body, &d)
	}
	n, err := s.routines(ctx, d)
	if err != nil {
		// GitHub was answered already and does not retry a delivery it was given a 200 for.
		s.logger.ErrorContext(ctx, "github: keeping a delivery for routines failed; it is lost to them",
			"event", event, "delivery", delivery, "repo", d.Repo, "events", d.Events, "error", err)
		return 0
	}
	if n > 0 {
		s.logger.InfoContext(ctx, "github: kept a delivery for routines",
			"event", event, "delivery", delivery, "repo", d.Repo, "events", d.Events, "routines", n)
	}
	return n
}

// completeCommand reads the pull request a command on its conversation tab is about, which
// that delivery does not describe: its base branch, which a routine's excluded branches are
// matched against, whether it is a draft, and its head. Only when a routine asks for commands
// in the repository, so a comment costs no API call otherwise. A read that fails leaves the
// base unknown, which a routine with excluded branches turns away.
func (s *Source) completeCommand(ctx context.Context, body []byte, d *store.GitHubDelivery) {
	if s.client == nil {
		return
	}
	if s.routinesWant != nil {
		if want, err := s.routinesWant(ctx, d.Repo, d.Events); err != nil || !want {
			return
		}
	}
	var p struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	_ = json.Unmarshal(body, &p)
	owner, repo, _ := strings.Cut(d.Repo, "/")
	var ev routineEvent
	if json.Unmarshal(d.Payload, &ev) != nil {
		return
	}
	number, _ := ev.Data["number"].(float64)
	info, err := s.client.Pull(ctx, p.Installation.ID, owner, repo, int(number))
	if err != nil {
		s.logger.WarnContext(ctx, "github: reading the pull request a command is on failed; its base branch is unknown",
			"repo", d.Repo, "pr", int(number), "error", err)
		return
	}
	d.Facts.Base, d.Facts.Draft = info.Base, info.Draft
	ev.Data["base_branch"], ev.Data["head_branch"], ev.Data["head_sha"] = info.Base, info.Head, info.SHA
	if payload, err := json.Marshal(ev); err == nil {
		d.Payload = payload
	}
}

// RoutineDelivery reads a delivery as routines on GitHub events see it: the names in
// store.RoutineGitHubEvents it answers to, most specific first, and the part of it a run is
// briefed with. ok is false for a delivery no routine can run on — another event or action,
// or a change the App made itself to a pull request or an issue, which would otherwise let a
// routine that acts on pull requests start itself again. A push the App made to a pull request,
// and a workflow run or check suite it set off, are kept: a fix landing, or failing again, is
// what a routine that reviews or repairs it needs to see. The runs-an-hour guard bounds a
// routine that pushes on its own pushes.
//
// A comment is kept only when it is a command (Commands), as "pull_request.command"; mention is
// the App's mention pattern, nil when its slug is unknown.
func RoutineDelivery(event, delivery string, body []byte, isApp func(login, typ string) bool, mention *regexp.Regexp, now time.Time) (store.GitHubDelivery, bool) {
	if strings.TrimSpace(delivery) == "" {
		return store.GitHubDelivery{}, false
	}
	var head struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Sender User `json:"sender"`
	}
	if json.Unmarshal(body, &head) != nil || head.Repository.FullName == "" {
		return store.GitHubDelivery{}, false
	}
	repo := head.Repository.FullName
	ev := routineEvent{
		Source: "github", Type: event, Action: head.Action, Actor: &routineActor{Name: head.Sender.Login},
		facts: store.GitHubEventFacts{Actor: head.Sender.Login, ActorBot: isBot(head.Sender)},
	}
	if head.Sender.ID != 0 {
		ev.facts.ActorID = fmt.Sprint(head.Sender.ID)
	}
	var names []string
	var err error
	switch event {
	case "workflow_run":
		names, err = ev.workflowRun(repo, body)
	case "check_suite":
		names, err = ev.checkSuite(repo, body)
	case "pull_request", "pull_request_review", "issues", "issue_comment", "pull_request_review_comment":
		// New commits the App pushed are kept, as its CI is: a playbook's fix landing on a pull
		// request is what a routine that reviews it again runs on. Anything else the App did to
		// a pull request or an issue is not, so a routine acting on them cannot start itself.
		pushed := event == "pull_request" && head.Action == "synchronize"
		if !pushed && isApp != nil && isApp(head.Sender.Login, head.Sender.Type) {
			return store.GitHubDelivery{}, false
		}
		switch event {
		case "pull_request":
			names, err = ev.pullRequest(repo, body)
		case "pull_request_review":
			names, err = ev.review(repo, body)
		case "issue_comment", "pull_request_review_comment":
			names, err = ev.command(repo, body, mention)
		default:
			names, err = ev.issue(repo, body)
		}
	}
	if err != nil || len(names) == 0 {
		return store.GitHubDelivery{}, false
	}
	if ev.Actor.Name == "" {
		ev.Actor = nil
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return store.GitHubDelivery{}, false
	}
	out := store.GitHubDelivery{
		DeliveryID: delivery, Events: names, Repo: repo, Payload: payload, ReceivedAt: now.UTC(),
		Facts: ev.facts,
	}
	if ev.pull > 0 {
		// The pull request's conversation is keyed by the repository as GitHub names it, so
		// the run joins the chat its reviews are in (SourceKey).
		out.SubjectKey = store.GitHubPullSubject(repo, ev.pull)
		out.SubjectTitle = ChatTitle(ev.pull, ev.pullTitle)
	}
	return out, true
}

// command is a comment on a pull request that asks with a command, from either tab: the
// conversation (issue_comment) or the diff (pull_request_review_comment). A new comment only;
// an edit that adds a command is not a request, as an edit is never a turn.
func (e *routineEvent) command(repo string, body []byte, mention *regexp.Regexp) ([]string, error) {
	var p struct {
		Comment struct {
			Body      string `json:"body"`
			HTMLURL   string `json:"html_url"`
			User      User   `json:"user"`
			CreatedAt string `json:"created_at"`
			Path      string `json:"path"`
			Line      int    `json:"line"`
		} `json:"comment"`
		// The conversation tab's comment names its pull request as an issue.
		Issue struct {
			Number      int             `json:"number"`
			Title       string          `json:"title"`
			Draft       bool            `json:"draft"`
			User        User            `json:"user"`
			PullRequest json.RawMessage `json:"pull_request"`
		} `json:"issue"`
		PullRequest struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Draft  bool   `json:"draft"`
			User   User   `json:"user"`
			Head   struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	if e.Action != "created" {
		return nil, nil
	}
	c := p.Comment
	commands := Commands(c.Body, mention)
	if len(commands) == 0 {
		return nil, nil
	}
	number, title, draft, author := p.PullRequest.Number, p.PullRequest.Title, p.PullRequest.Draft, p.PullRequest.User
	data := map[string]any{}
	if e.Type == "issue_comment" {
		if len(p.Issue.PullRequest) == 0 || string(p.Issue.PullRequest) == "null" {
			return nil, nil
		}
		number, title, draft, author = p.Issue.Number, p.Issue.Title, p.Issue.Draft, p.Issue.User
	} else {
		data["base_branch"], data["head_branch"], data["head_sha"] = p.PullRequest.Base.Ref, p.PullRequest.Head.Ref, p.PullRequest.Head.SHA
		data["path"], data["line"] = c.Path, c.Line
		e.facts.Base = p.PullRequest.Base.Ref
	}
	if number <= 0 {
		return nil, nil
	}
	e.URL, e.CreatedAt = c.HTMLURL, c.CreatedAt
	e.pull, e.pullTitle = number, title
	if c.User.Login != "" {
		// Who asked is the comment's author; the sender is the same account.
		e.Actor = &routineActor{Name: c.User.Login}
		e.facts.Actor, e.facts.ActorBot = c.User.Login, isBot(c.User)
	}
	if c.User.ID != 0 {
		e.facts.ActorID = fmt.Sprint(c.User.ID)
	}
	e.facts.Pull, e.facts.Draft = true, draft
	e.facts.Command, e.facts.Commands = true, commands
	data["repo"], data["number"], data["title"] = repo, number, clip(title, 300)
	data["commands"], data["comment"] = commands, clip(c.Body, maxRoutineBodyRunes)
	data["asked_by"], data["author"], data["author_id"] = c.User.Login, author.Login, author.ID
	e.Data = data
	return []string{store.GitHubCommandEvent}, nil
}

// routineEvent is a kept delivery, in the shape conductor.BriefEvents reads.
type routineEvent struct {
	Source    string         `json:"source"`
	Type      string         `json:"type"`
	Action    string         `json:"action"`
	URL       string         `json:"url,omitempty"`
	CreatedAt string         `json:"createdAt,omitempty"`
	Actor     *routineActor  `json:"actor,omitempty"`
	Data      map[string]any `json:"data"`

	// pull is the pull request the event is about, 0 for none, and pullTitle its title when
	// the delivery says it. A workflow run or check suite is about the first pull request
	// GitHub lists for its commit.
	pull      int
	pullTitle string
	// facts are what a routine's conditions are asked about the event.
	facts store.GitHubEventFacts
}

type routineActor struct {
	Name string `json:"name"`
}

// ghPullRef is a pull request as a workflow run or check suite names it: GitHub lists the
// open pull requests whose head is the commit, from the same repository only.
type ghPullRef struct {
	Number int `json:"number"`
	Head   struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghCommit struct {
	Message string `json:"message"`
}

func pullRefs(repo string, prs []ghPullRef) []map[string]any {
	out := make([]map[string]any, 0, len(prs))
	for _, p := range prs {
		if p.Number <= 0 {
			continue
		}
		owner, name, _ := strings.Cut(repo, "/")
		out = append(out, map[string]any{
			"number": p.Number, "url": PRURL(owner, name, p.Number),
			"head_branch": p.Head.Ref, "base_branch": p.Base.Ref,
		})
	}
	return out
}

func labelNames(in []ghLabel) []string {
	out := make([]string, 0, len(in))
	for _, l := range in {
		if l.Name != "" {
			out = append(out, l.Name)
		}
	}
	return out
}

// failed reports whether a completed run's conclusion is a failure.
func failed(conclusion string) bool {
	return slices.Contains(store.GitHubFailedConclusions, conclusion)
}

// completedNames is what a completed workflow run or check suite answers to.
func completedNames(kind, action, conclusion string) []string {
	if action != "completed" {
		return nil
	}
	if failed(conclusion) {
		return []string{kind + ".failed", kind + ".completed"}
	}
	return []string{kind + ".completed"}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return clip(line, 200)
}

func clip(s string, runes int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > runes {
		return string(r[:runes]) + "… [trimmed]"
	}
	return s
}

func (e *routineEvent) workflowRun(repo string, body []byte) ([]string, error) {
	var p struct {
		WorkflowRun struct {
			ID           int64       `json:"id"`
			Name         string      `json:"name"`
			DisplayTitle string      `json:"display_title"`
			RunNumber    int         `json:"run_number"`
			RunAttempt   int         `json:"run_attempt"`
			Event        string      `json:"event"`
			Status       string      `json:"status"`
			Conclusion   string      `json:"conclusion"`
			HeadBranch   string      `json:"head_branch"`
			HeadSHA      string      `json:"head_sha"`
			HTMLURL      string      `json:"html_url"`
			UpdatedAt    string      `json:"updated_at"`
			PullRequests []ghPullRef `json:"pull_requests"`
			HeadCommit   *ghCommit   `json:"head_commit"`
		} `json:"workflow_run"`
		Workflow struct {
			Path string `json:"path"`
		} `json:"workflow"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	w := p.WorkflowRun
	names := completedNames("workflow_run", e.Action, w.Conclusion)
	if len(names) == 0 || w.ID == 0 {
		return nil, nil
	}
	e.URL, e.CreatedAt = w.HTMLURL, w.UpdatedAt
	if len(w.PullRequests) > 0 {
		e.pull = w.PullRequests[0].Number
		e.facts.Pull, e.facts.Base = true, w.PullRequests[0].Base.Ref
		if w.Event == "pull_request" || w.Event == "pull_request_target" {
			// A run a pull request set off is titled with the pull request's title.
			e.pullTitle = w.DisplayTitle
		}
	}
	e.Data = map[string]any{
		"repo": repo, "workflow": w.Name, "workflow_file": p.Workflow.Path, "run_id": w.ID,
		"run_number": w.RunNumber, "run_attempt": w.RunAttempt, "title": clip(w.DisplayTitle, 200),
		"triggered_by": w.Event, "status": w.Status, "conclusion": w.Conclusion,
		"head_branch": w.HeadBranch, "head_sha": w.HeadSHA,
		"pull_requests": pullRefs(repo, w.PullRequests),
	}
	if w.HeadCommit != nil {
		e.Data["commit_message"] = firstLine(w.HeadCommit.Message)
	}
	return names, nil
}

func (e *routineEvent) checkSuite(repo string, body []byte) ([]string, error) {
	var p struct {
		CheckSuite struct {
			ID         int64  `json:"id"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HeadBranch string `json:"head_branch"`
			HeadSHA    string `json:"head_sha"`
			UpdatedAt  string `json:"updated_at"`
			Runs       int    `json:"latest_check_runs_count"`
			App        struct {
				Name string `json:"name"`
				Slug string `json:"slug"`
			} `json:"app"`
			PullRequests []ghPullRef `json:"pull_requests"`
			HeadCommit   *ghCommit   `json:"head_commit"`
		} `json:"check_suite"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	c := p.CheckSuite
	names := completedNames("check_suite", e.Action, c.Conclusion)
	if len(names) == 0 || c.ID == 0 {
		return nil, nil
	}
	// A check suite has no page of its own: its commit's checks are where it is read.
	if c.HeadSHA != "" {
		e.URL = fmt.Sprintf("https://github.com/%s/commit/%s/checks", repo, c.HeadSHA)
	}
	e.CreatedAt = c.UpdatedAt
	if len(c.PullRequests) > 0 {
		e.pull = c.PullRequests[0].Number
		e.facts.Pull, e.facts.Base = true, c.PullRequests[0].Base.Ref
	}
	e.Data = map[string]any{
		"repo": repo, "app": c.App.Name, "app_slug": c.App.Slug, "check_suite_id": c.ID,
		"status": c.Status, "conclusion": c.Conclusion, "check_runs": c.Runs,
		"head_branch": c.HeadBranch, "head_sha": c.HeadSHA,
		"pull_requests": pullRefs(repo, c.PullRequests),
	}
	if c.HeadCommit != nil {
		e.Data["commit_message"] = firstLine(c.HeadCommit.Message)
	}
	return names, nil
}

func (e *routineEvent) pullRequest(repo string, body []byte) ([]string, error) {
	var p struct {
		PullRequest struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			Body    string `json:"body"`
			State   string `json:"state"`
			Draft   bool   `json:"draft"`
			Merged  bool   `json:"merged"`
			HTMLURL string `json:"html_url"`
			User    User   `json:"user"`
			Head    struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
			Additions    int       `json:"additions"`
			Deletions    int       `json:"deletions"`
			ChangedFiles int       `json:"changed_files"`
			Labels       []ghLabel `json:"labels"`
			UpdatedAt    string    `json:"updated_at"`
			MergedBy     *User     `json:"merged_by"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	pr := p.PullRequest
	if pr.Number <= 0 {
		return nil, nil
	}
	var names []string
	switch e.Action {
	case "opened", "ready_for_review", "synchronize":
		names = []string{"pull_request." + e.Action}
	case "closed":
		names = []string{"pull_request.closed"}
		if pr.Merged {
			names = []string{"pull_request.merged", "pull_request.closed"}
		}
	default:
		return nil, nil
	}
	e.URL, e.CreatedAt = pr.HTMLURL, pr.UpdatedAt
	e.pull, e.pullTitle = pr.Number, pr.Title
	e.facts.Pull, e.facts.Base, e.facts.Draft = true, pr.Base.Ref, pr.Draft
	e.Data = map[string]any{
		"repo": repo, "number": pr.Number, "title": clip(pr.Title, 300), "body": clip(pr.Body, maxRoutineBodyRunes),
		"author": pr.User.Login, "author_id": pr.User.ID, "state": pr.State, "draft": pr.Draft, "merged": pr.Merged,
		"head_branch": pr.Head.Ref, "head_sha": pr.Head.SHA, "base_branch": pr.Base.Ref,
		"additions": pr.Additions, "deletions": pr.Deletions, "changed_files": pr.ChangedFiles,
		"labels": labelNames(pr.Labels),
	}
	if pr.MergedBy != nil && pr.MergedBy.Login != "" {
		e.Data["merged_by"] = pr.MergedBy.Login
	}
	return names, nil
}

func (e *routineEvent) review(repo string, body []byte) ([]string, error) {
	var p struct {
		Review struct {
			State       string `json:"state"`
			Body        string `json:"body"`
			HTMLURL     string `json:"html_url"`
			User        User   `json:"user"`
			CommitID    string `json:"commit_id"`
			SubmittedAt string `json:"submitted_at"`
		} `json:"review"`
		PullRequest struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Draft  bool   `json:"draft"`
			User   User   `json:"user"`
			Head   struct {
				Ref string `json:"ref"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	if e.Action != "submitted" || p.PullRequest.Number <= 0 {
		return nil, nil
	}
	r, pr := p.Review, p.PullRequest
	e.URL, e.CreatedAt = r.HTMLURL, r.SubmittedAt
	e.pull, e.pullTitle = pr.Number, pr.Title
	e.facts.Pull, e.facts.Base, e.facts.Draft = true, pr.Base.Ref, pr.Draft
	e.Data = map[string]any{
		"repo": repo, "number": pr.Number, "title": clip(pr.Title, 300), "author": pr.User.Login,
		"author_id": pr.User.ID, "reviewer": r.User.Login, "state": strings.ToLower(r.State), "body": clip(r.Body, maxRoutineBodyRunes),
		"commit_sha": r.CommitID, "head_branch": pr.Head.Ref, "base_branch": pr.Base.Ref,
	}
	return []string{"pull_request_review.submitted"}, nil
}

func (e *routineEvent) issue(repo string, body []byte) ([]string, error) {
	var p struct {
		Issue struct {
			Number    int       `json:"number"`
			Title     string    `json:"title"`
			Body      string    `json:"body"`
			HTMLURL   string    `json:"html_url"`
			User      User      `json:"user"`
			Labels    []ghLabel `json:"labels"`
			CreatedAt string    `json:"created_at"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	if e.Action != "opened" || p.Issue.Number <= 0 {
		return nil, nil
	}
	i := p.Issue
	e.URL, e.CreatedAt = i.HTMLURL, i.CreatedAt
	e.Data = map[string]any{
		"repo": repo, "number": i.Number, "title": clip(i.Title, 300), "body": clip(i.Body, maxRoutineBodyRunes),
		"author": i.User.Login, "labels": labelNames(i.Labels),
	}
	return []string{"issues.opened"}, nil
}
