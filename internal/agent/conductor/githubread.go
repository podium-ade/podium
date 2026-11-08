package conductor

// The GitHub reader a routine's turn gets (docs/agent.md, "Routines"): pull requests, their
// checks, a failed job's log, and recent commits, read as the routine's OWNER with the GitHub
// account they connected under Settings → Account. Never as the App: a routine must not let a
// person read a repository they cannot read themselves.
//
// It exists so a routine's check costs a model turn on this host and nothing more. Without it,
// "are there failed PRs?" is a container started every fifteen minutes to run `gh`, because a
// host turn has no shell and no network of its own.
//
// It reads, and only reads: every tool is a GET. Changing anything is a delegated playbook's
// work, with its own credential.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/podium-ade/podium/internal/agent/mcp"
)

// GitHubReadMCPName is the server's name as a routine's turn sees it.
const GitHubReadMCPName = mcp.ReservedGitHub

// githubLogTail is how much of a job's log a call returns at most: the end, where a failure is.
const githubLogTail = 48 * 1024

var githubReadTools = []chatTool{
	{Name: "list_pull_requests", Description: "List pull requests in a repository you can read, newest first. " +
		"Returns number, title, author, head and base branch, head commit, draft, labels and when it was updated.",
		InputSchema: objectSchema(map[string]any{
			"repo":  str("owner/name, e.g. acme/api"),
			"state": map[string]any{"type": "string", "enum": []string{"open", "closed", "all"}, "description": "Default open."},
			"label": str("Only pull requests carrying this label. Optional."),
			"limit": map[string]any{"type": "integer", "description": "At most this many, 1–50. Default 20."},
		}, "repo")},
	{Name: "get_pull_request", Description: "One pull request: its description, branches, head commit, mergeability, " +
		"and the files it changes.",
		InputSchema: objectSchema(map[string]any{"repo": str("owner/name"), "number": map[string]any{"type": "integer"}}, "repo", "number")},
	{Name: "list_check_runs", Description: "The CI check runs on a commit (a sha or a branch name): name, status, " +
		"conclusion, and the job id to read a failed one's log with get_job_log.",
		InputSchema: objectSchema(map[string]any{"repo": str("owner/name"), "ref": str("A commit sha or a branch name.")}, "repo", "ref")},
	{Name: "get_job_log", Description: "The end of a GitHub Actions job's log, where a failure usually is.",
		InputSchema: objectSchema(map[string]any{"repo": str("owner/name"), "job_id": map[string]any{"type": "integer"}}, "repo", "job_id")},
	{Name: "list_commits", Description: "Recent commits on a branch: sha, author, date and the first line of the message.",
		InputSchema: objectSchema(map[string]any{
			"repo":   str("owner/name"),
			"branch": str("Default: the repository's default branch."),
			"since":  str("Only commits after this ISO 8601 time. Optional."),
			"limit":  map[string]any{"type": "integer", "description": "1–50. Default 20."},
		}, "repo")},
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// GitHubReadMCP serves the reader to the turn whose token is the request's bearer. Only a turn
// in a routine's chat has it.
func (c *Conductor) GitHubReadMCP() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || !c.inRoutineChat(r.Context(), token) {
			http.Error(w, "this reader is for a routine's turn", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			chatRPC(w, nil, nil, &chatRPCError{Code: -32700, Message: "request too large"})
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			chatRPC(w, nil, nil, &chatRPCError{Code: -32700, Message: "not a JSON-RPC request"})
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		switch req.Method {
		case "initialize":
			chatRPC(w, req.ID, map[string]any{
				"protocolVersion": chatMCPProtocol,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": GitHubReadMCPName, "version": "1"},
			}, nil)
		case "ping":
			chatRPC(w, req.ID, map[string]any{}, nil)
		case "tools/list":
			chatRPC(w, req.ID, map[string]any{"tools": githubReadTools}, nil)
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				chatRPC(w, req.ID, nil, &chatRPCError{Code: -32602, Message: "tools/call needs a name and arguments"})
				return
			}
			text, err := c.githubRead(r.Context(), token, p.Name, p.Arguments)
			if err != nil {
				chatRPC(w, req.ID, chatToolResult(err.Error(), true), nil)
				return
			}
			chatRPC(w, req.ID, chatToolResult(text, false), nil)
		default:
			chatRPC(w, req.ID, nil, &chatRPCError{Code: -32601, Message: "unknown method " + req.Method})
		}
	})
}

// githubReader is the person's token and the client to read with.
type githubReader struct {
	get     func(ctx context.Context, path string, out any) error
	getText func(ctx context.Context, path string, limit int64) (string, error)
}

func (c *Conductor) githubReaderFor(ctx context.Context, token string) (githubReader, error) {
	g, ok := c.grantFor(ctx, token)
	if !ok {
		return githubReader{}, ErrNoTurn
	}
	login := turnOwner(g.login)
	if login == "" {
		return githubReader{}, errors.New("this routine has no owner to read GitHub as")
	}
	app := c.githubApp()
	if app == nil {
		return githubReader{}, errors.New("GitHub is not connected to Podium (Settings → Connections), so there is nothing to read with")
	}
	tokens, err := c.userTokenClient(ctx)
	if err != nil {
		return githubReader{}, err
	}
	personal, err := c.personToken(ctx, login, tokens)
	if err != nil {
		return githubReader{}, fmt.Errorf("reading GitHub as the routine's owner needs their GitHub account "+
			"connected under Settings → Account: %w", err)
	}
	return githubReader{
		get: func(ctx context.Context, path string, out any) error { return app.GetAs(ctx, personal, path, out) },
		getText: func(ctx context.Context, path string, limit int64) (string, error) {
			return app.GetTextAs(ctx, personal, path, limit)
		},
	}, nil
}

func repoArg(args map[string]any) (string, error) {
	repo, _ := args["repo"].(string)
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("repo must be owner/name, not %q", repo)
	}
	return url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

func intArg(args map[string]any, key string, def, lo, hi int) int {
	v, ok := args[key].(float64)
	if !ok {
		return def
	}
	return min(max(int(v), lo), hi)
}

func (c *Conductor) githubRead(ctx context.Context, token, name string, args map[string]any) (string, error) {
	repo, err := repoArg(args)
	if err != nil {
		return "", err
	}
	gh, err := c.githubReaderFor(ctx, token)
	if err != nil {
		return "", err
	}
	switch name {
	case "list_pull_requests":
		state, _ := args["state"].(string)
		if state != "closed" && state != "all" {
			state = "open"
		}
		limit := intArg(args, "limit", 20, 1, 50)
		label, _ := args["label"].(string)
		var pulls []ghPull
		per := limit
		if label != "" {
			per = 100
		}
		if err := gh.get(ctx, fmt.Sprintf("/repos/%s/pulls?state=%s&sort=updated&direction=desc&per_page=%d", repo, state, per), &pulls); err != nil {
			return "", err
		}
		out := make([]map[string]any, 0, limit)
		for _, p := range pulls {
			if label != "" && !p.hasLabel(label) {
				continue
			}
			out = append(out, p.summary())
			if len(out) == limit {
				break
			}
		}
		return jsonText(out)
	case "get_pull_request":
		n := intArg(args, "number", 0, 0, 1<<30)
		if n == 0 {
			return "", errors.New("number is required")
		}
		var p ghPull
		if err := gh.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, n), &p); err != nil {
			return "", err
		}
		var files []struct {
			Filename  string `json:"filename"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Deletions int    `json:"deletions"`
		}
		if err := gh.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100", repo, n), &files); err != nil {
			return "", err
		}
		out := p.summary()
		out["body"] = truncateRunes(p.Body, 8000)
		out["mergeable_state"] = p.MergeableState
		out["files"] = files
		return jsonText(out)
	case "list_check_runs":
		ref, _ := args["ref"].(string)
		if strings.TrimSpace(ref) == "" {
			return "", errors.New("ref is required: a commit sha or a branch name")
		}
		var res struct {
			CheckRuns []struct {
				ID          int64  `json:"id"`
				Name        string `json:"name"`
				Status      string `json:"status"`
				Conclusion  string `json:"conclusion"`
				DetailsURL  string `json:"details_url"`
				CompletedAt string `json:"completed_at"`
			} `json:"check_runs"`
		}
		if err := gh.get(ctx, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repo, url.PathEscape(strings.TrimSpace(ref))), &res); err != nil {
			return "", err
		}
		out := make([]map[string]any, 0, len(res.CheckRuns))
		for _, r := range res.CheckRuns {
			// A GitHub Actions check run's id is its job's id, which is what get_job_log takes.
			out = append(out, map[string]any{"job_id": r.ID, "name": r.Name, "status": r.Status,
				"conclusion": r.Conclusion, "url": r.DetailsURL, "completed_at": r.CompletedAt})
		}
		return jsonText(out)
	case "get_job_log":
		id := intArg(args, "job_id", 0, 0, 1<<62)
		if id == 0 {
			return "", errors.New("job_id is required")
		}
		return gh.getText(ctx, fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, id), githubLogTail)
	case "list_commits":
		q := url.Values{"per_page": {strconv.Itoa(intArg(args, "limit", 20, 1, 50))}}
		if b, _ := args["branch"].(string); strings.TrimSpace(b) != "" {
			q.Set("sha", strings.TrimSpace(b))
		}
		if s, _ := args["since"].(string); strings.TrimSpace(s) != "" {
			q.Set("since", strings.TrimSpace(s))
		}
		var commits []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
				Author  struct {
					Name string `json:"name"`
					Date string `json:"date"`
				} `json:"author"`
			} `json:"commit"`
		}
		if err := gh.get(ctx, fmt.Sprintf("/repos/%s/commits?%s", repo, q.Encode()), &commits); err != nil {
			return "", err
		}
		out := make([]map[string]any, 0, len(commits))
		for _, cm := range commits {
			out = append(out, map[string]any{"sha": cm.SHA, "author": cm.Commit.Author.Name, "date": cm.Commit.Author.Date,
				"message": firstLine(cm.Commit.Message, 200)})
		}
		return jsonText(out)
	}
	return "", fmt.Errorf("unknown tool %s", name)
}

type ghPull struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	HTMLURL        string `json:"html_url"`
	Body           string `json:"body"`
	UpdatedAt      string `json:"updated_at"`
	MergeableState string `json:"mergeable_state"`
	User           struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (p ghPull) hasLabel(name string) bool {
	for _, l := range p.Labels {
		if strings.EqualFold(l.Name, name) {
			return true
		}
	}
	return false
}

func (p ghPull) summary() map[string]any {
	labels := make([]string, 0, len(p.Labels))
	for _, l := range p.Labels {
		labels = append(labels, l.Name)
	}
	return map[string]any{"number": p.Number, "title": p.Title, "author": p.User.Login, "state": p.State,
		"draft": p.Draft, "head": p.Head.Ref, "head_sha": p.Head.SHA, "base": p.Base.Ref, "labels": labels,
		"updated_at": p.UpdatedAt, "url": p.HTMLURL}
}

func jsonText(v any) (string, error) {
	raw, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
