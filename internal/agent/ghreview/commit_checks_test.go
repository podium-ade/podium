package ghreview

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/store"
)

const checksSHA = "abc123"

// checksAPI is GitHub as a commit's checks are read from it: each path answers its body, or
// a 403 when it is in denied.
type checksAPI struct {
	mu     sync.Mutex
	bodies map[string]any
	denied map[string]bool
	reads  []string
}

func (a *checksAPI) set(path string, body any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bodies[path] = body
}

func (a *checksAPI) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/app/installations/") {
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "t", "expires_at": time.Now().Add(time.Hour)})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reads = append(a.reads, r.URL.Path)
	if a.denied[r.URL.Path] {
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
		return
	}
	body, ok := a.bodies[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func suite(app string, status string, runs int) map[string]any {
	return map[string]any{"status": status, "head_branch": "fix-login", "latest_check_runs_count": runs,
		"app": map[string]any{"slug": app}}
}

func checkRun(id int64, name, app, status, conclusion string) map[string]any {
	return map[string]any{"id": id, "name": name, "status": status, "conclusion": conclusion,
		"html_url": "https://github.com/acme/api/runs/" + name, "app": map[string]any{"slug": app, "name": app}}
}

// fiveJobs is a commit with five Actions jobs in their suites, the given one still running.
func fiveJobs(api *checksAPI, running string, failing ...string) {
	base := "/repos/acme/api/commits/" + checksSHA
	suiteStatus := "completed"
	if running != "" {
		suiteStatus = "in_progress"
	}
	api.set(base+"/check-suites", map[string]any{"total_count": 3, "check_suites": []any{
		suite("github-actions", "completed", 3),
		suite("github-actions", suiteStatus, 2),
		// An app that asked for a suite and never ran a check leaves it queued for good.
		suite("dependabot", "queued", 0),
		suite("podium", "in_progress", 1),
	}})
	var runs []any
	for i, name := range []string{"go", "web", "proto", "runtime", "snapshot"} {
		status, conclusion := "completed", "success"
		if name == running {
			status, conclusion = "in_progress", ""
		}
		for _, f := range failing {
			if f == name {
				conclusion = "failure"
			}
		}
		runs = append(runs, checkRun(int64(100+i), name, "github-actions", status, conclusion))
	}
	runs = append(runs, checkRun(900, "Podium review", "podium", "in_progress", ""))
	api.set(base+"/check-runs", map[string]any{"total_count": len(runs), "check_runs": runs})
	api.set(base+"/status", map[string]any{"state": "success", "statuses": []any{}})
	api.set(base+"/pulls", []any{
		map[string]any{"number": 7, "title": "Old one", "state": "closed", "head": map[string]any{"ref": "x", "sha": "zzz"}},
		map[string]any{"number": 42, "title": "Fix login", "state": "open", "user": map[string]any{"login": "pat", "type": "User"},
			"head": map[string]any{"ref": "fix-login", "sha": checksSHA}, "base": map[string]any{"ref": "main"}},
	})
}

type checksHarness struct {
	src  *Source
	api  *checksAPI
	mu   sync.Mutex
	kept []store.GitHubDelivery
	want bool
}

func newChecksHarness(t *testing.T) *checksHarness {
	t.Helper()
	h := &checksHarness{api: &checksAPI{bodies: map[string]any{}, denied: map[string]bool{}}, want: true}
	srv := httptest.NewServer(http.HandlerFunc(h.api.serve))
	t.Cleanup(srv.Close)
	src, err := NewSource(SourceOptions{
		AppID: "1", PrivateKey: testKey(t), WebhookSecret: testSecret, Listen: "127.0.0.1:0", APIURL: srv.URL,
		Session: func(context.Context, string) (time.Time, bool) { return time.Time{}, false },
		Logger:  slog.New(slog.DiscardHandler),
		Clock:   func() time.Time { return routineNow },
		Routines: func(_ context.Context, d store.GitHubDelivery) (int, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.kept = append(h.kept, d)
			return 1, nil
		},
		RoutinesWant: func(_ context.Context, repo string, events []string) (bool, error) {
			assert.Equal(t, "acme/api", repo)
			assert.Equal(t, CommitCheckEvents, events)
			return h.want, nil
		},
	})
	require.NoError(t, err)
	src.slug = "podium"
	src.checksSettle = 0
	h.src = src
	return h
}

func (h *checksHarness) finish(t *testing.T, event string, v map[string]any) {
	t.Helper()
	v["installation"] = map[string]any{"id": 42}
	h.src.watchCommitChecks(context.Background(), event, ghBody(t, v))
	h.src.checksWG.Wait()
}

func workflowDone() map[string]any {
	return map[string]any{"action": "completed", "workflow_run": map[string]any{
		"head_sha": checksSHA, "pull_requests": []any{map[string]any{"number": 42}},
	}}
}

func TestACommitIsKeptOnceItsLastCheckFinishesWithOneFailed(t *testing.T) {
	h := newChecksHarness(t)
	fiveJobs(h.api, "runtime", "web")
	h.finish(t, "workflow_run", workflowDone())
	require.Empty(t, h.kept, "a job still runs: the commit is not done")

	fiveJobs(h.api, "", "web")
	h.finish(t, "workflow_run", workflowDone())
	require.Len(t, h.kept, 1)
	d := h.kept[0]
	assert.Equal(t, []string{"checks.failed", "checks.completed"}, d.Events)
	assert.Equal(t, "checks:acme/api@abc123:failed", d.DeliveryID, "one per commit and outcome, whoever sees it done")
	assert.Equal(t, "PullRequest:acme/api#42", d.SubjectKey, "run in the pull request's chat")
	assert.Equal(t, "#42: Fix login", d.SubjectTitle, "the open pull request whose head the commit is")

	top, data := keptData(t, d)
	assert.Equal(t, "checks", top["type"])
	assert.Equal(t, "failed", top["action"])
	assert.Equal(t, "https://github.com/acme/api/pull/42/checks", top["url"])
	assert.Equal(t, float64(5), data["checks"], "the App's own check and an empty suite are not CI")
	assert.Equal(t, float64(4), data["checks_passed"])
	assert.Equal(t, float64(1), data["checks_failed"])
	assert.Equal(t, []any{map[string]any{
		"name": "web", "app": "github-actions", "conclusion": "failure",
		"url": "https://github.com/acme/api/runs/web", "job_id": float64(101),
	}}, data["failed"], "with the job whose log a run reads")
	assert.Equal(t, "fix-login", data["head_branch"])
	assert.Equal(t, store.GitHubEventFacts{Actor: "pat", Pull: true, Base: "main"}, d.Facts,
		"nobody set the finish off: a routine's conditions on people ask about the pull request's author")
}

func TestACommitWhoseChecksAllPassedIsOnlyCompleted(t *testing.T) {
	h := newChecksHarness(t)
	fiveJobs(h.api, "")
	h.finish(t, "check_suite", map[string]any{"action": "completed", "check_suite": map[string]any{"head_sha": checksSHA}})
	require.Len(t, h.kept, 1)
	assert.Equal(t, []string{"checks.completed"}, h.kept[0].Events)
	assert.Equal(t, "checks:acme/api@abc123:passed", h.kept[0].DeliveryID)
}

func TestACommitStatusStillPendingKeepsTheCommitWaiting(t *testing.T) {
	h := newChecksHarness(t)
	fiveJobs(h.api, "")
	base := "/repos/acme/api/commits/" + checksSHA
	h.api.set(base+"/status", map[string]any{"statuses": []any{map[string]any{"context": "ci/circleci", "state": "pending"}}})
	h.finish(t, "workflow_run", workflowDone())
	assert.Empty(t, h.kept)

	h.api.set(base+"/status", map[string]any{"statuses": []any{
		map[string]any{"context": "ci/circleci", "state": "failure", "target_url": "https://circleci.com/x"},
	}})
	h.finish(t, "status", map[string]any{"sha": checksSHA, "state": "failure"})
	require.Len(t, h.kept, 1)
	_, data := keptData(t, h.kept[0])
	assert.Equal(t, "ci/circleci", data["failed"].([]any)[0].(map[string]any)["name"])
}

func TestAnAppThatCannotReadStatusesWaitsOnlyForChecks(t *testing.T) {
	h := newChecksHarness(t)
	fiveJobs(h.api, "", "go")
	h.api.denied["/repos/acme/api/commits/"+checksSHA+"/status"] = true
	h.finish(t, "workflow_run", workflowDone())
	require.Len(t, h.kept, 1)
}

func TestNoRoutineOnFinishedChecksCostsNoRead(t *testing.T) {
	h := newChecksHarness(t)
	h.want = false
	fiveJobs(h.api, "", "go")
	h.finish(t, "workflow_run", workflowDone())
	assert.Empty(t, h.kept)
	assert.Empty(t, h.api.reads)
}

func TestOnlyAFinishIsReadAsOne(t *testing.T) {
	for name, tc := range map[string]struct {
		event string
		body  map[string]any
	}{
		"a run requested":   {"workflow_run", map[string]any{"action": "requested", "workflow_run": map[string]any{"head_sha": checksSHA}}},
		"a pending status":  {"status", map[string]any{"sha": checksSHA, "state": "pending"}},
		"a pull request":    {"pull_request", map[string]any{"action": "opened"}},
		"a run with no sha": {"workflow_run", map[string]any{"action": "completed", "workflow_run": map[string]any{}}},
	} {
		t.Run(name, func(t *testing.T) {
			tc.body["installation"] = map[string]any{"id": 42}
			_, ok := finishedOnCommit(tc.event, ghBody(t, tc.body))
			assert.False(t, ok)
		})
	}
}

func TestAFinishWhileTheCommitIsReadIsReadAgainAfter(t *testing.T) {
	h := newChecksHarness(t)
	fiveJobs(h.api, "runtime", "web")
	gate := make(chan struct{})
	h.src.routinesWant = func(context.Context, string, []string) (bool, error) {
		<-gate
		return true, nil
	}
	body := workflowDone()
	body["installation"] = map[string]any{"id": 42}
	raw := ghBody(t, body)
	h.src.watchCommitChecks(context.Background(), "workflow_run", raw)
	// The last job finishes while the first read waits.
	fiveJobs(h.api, "", "web")
	h.src.watchCommitChecks(context.Background(), "workflow_run", raw)
	h.src.watchCommitChecks(context.Background(), "workflow_run", raw)
	close(gate)
	h.src.checksWG.Wait()
	assert.Equal(t, 2, strings.Count(strings.Join(h.api.reads, " "), "/check-runs"),
		"the finishes that came in during a read cost one read more between them")
	require.NotEmpty(t, h.kept)
	for _, d := range h.kept {
		assert.Equal(t, "checks:acme/api@abc123:failed", d.DeliveryID, "which the store keeps once")
	}
}
