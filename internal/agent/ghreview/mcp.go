package ghreview

// The pull request a host turn is answering, as read-only MCP tools.
//
// The assistant answers a question about a pull request on the host, with no repository. Most
// questions are about the diff, and starting a container to read one is seconds and money for
// something GitHub will hand over in one call. So the conductor serves these tools itself, on
// its turn listener, to the turn whose token it minted — and only about that turn's own pull
// request. They read; nothing here writes, comments or approves.
//
// It speaks the subset of MCP's streamable HTTP transport a harness needs: one JSON-RPC
// request per POST, answered with one JSON response. There is no session and no stream.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	// MCPPath is where the server is mounted on the conductor's turn listener.
	MCPPath = "/mcp/pull-request"

	mcpProtocol    = "2025-06-18"
	mcpMaxRequest  = 1 << 20
	mcpMaxResult   = 64 << 10
	rpcParseError  = -32700
	rpcBadRequest  = -32600
	rpcNoMethod    = -32601
	rpcBadParams   = -32602
	bearerPrefix   = "Bearer "
	mcpServerTitle = "podium-pull-request"
)

var mcpProtocols = map[string]bool{"2024-11-05": true, "2025-03-26": true, "2025-06-18": true, "2025-11-25": true}

// TurnLookup is the pull request a live turn token belongs to, as its source key. False for a
// token that names no live turn, or a turn that is not on a pull request.
type TurnLookup func(token string) (sourceKey string, ok bool)

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

var mcpTools = []mcpTool{
	{
		Name:        "files",
		Description: "The files this pull request changes: status, path, lines added and removed.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
	},
	{
		Name: "diff",
		Description: "This pull request's diff, file by file. Give a path for one file's part; the whole " +
			"diff is cut at 64 KB.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "One changed file's path."},
			},
			"additionalProperties": false,
		},
	},
	{
		Name: "file",
		Description: "One file as it is at this pull request's head commit, with line numbers. Give a " +
			"line range for a large file; the result is cut at 64 KB.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string"},
				"start_line": map[string]any{"type": "integer", "minimum": 1},
				"end_line":   map[string]any{"type": "integer", "minimum": 1},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
	},
}

// MCPHandler serves the tools. current is the GitHub source now; nil when no App is saved, and
// it can change while the conductor runs, so it is asked on every call.
func MCPHandler(turns TurnLookup, current func() *Source) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
		key, ok := "", false
		if found && token != "" {
			key, ok = turns(token)
		}
		if !ok {
			http.Error(w, "not a turn on a pull request", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpMaxRequest))
		if err != nil {
			writeRPC(w, nil, nil, &rpcError{Code: rpcParseError, Message: "request too large"})
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeRPC(w, nil, nil, &rpcError{Code: rpcParseError, Message: "not a JSON-RPC request"})
			return
		}
		if len(req.ID) == 0 {
			// A notification — "initialized" is the one a client sends — is answered with
			// nothing.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result, rerr := mcpCall(r.Context(), key, current(), req.Method, req.Params)
		writeRPC(w, req.ID, result, rerr)
	})
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rerr *rpcError) {
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if id == nil {
		out["id"] = nil
	}
	if rerr != nil {
		out["error"] = rerr
	} else {
		out["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func mcpCall(ctx context.Context, key string, src *Source, method string, raw json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(raw, &p)
		version := mcpProtocol
		if mcpProtocols[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": mcpServerTitle, "version": "1"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &rpcError{Code: rpcBadParams, Message: "tools/call needs a name and arguments"}
		}
		text, err := src.mcpTool(ctx, key, p.Name, p.Arguments)
		if err != nil {
			// A tool that ran and failed is a result, so the model reads why.
			return toolResult(err.Error(), true), nil
		}
		return toolResult(text, false), nil
	case "":
		return nil, &rpcError{Code: rpcBadRequest, Message: "no method"}
	default:
		return nil, &rpcError{Code: rpcNoMethod, Message: "unknown method " + method}
	}
}

func toolResult(text string, failed bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": failed,
	}
}

func (s *Source) mcpTool(ctx context.Context, key, name string, raw json.RawMessage) (string, error) {
	if s == nil {
		return "", errors.New("no GitHub App is configured on this conductor")
	}
	owner, repo, number, ok := ParseSourceKey(key)
	if !ok {
		return "", errors.New("this conversation is not a pull request")
	}
	var args struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("arguments: %w", err)
		}
	}
	install, err := s.client.RepoInstallation(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	switch name {
	case "files":
		files, err := s.client.PullFiles(ctx, install, owner, repo, number)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, f := range files {
			fmt.Fprintf(&b, "%s %s +%d -%d\n", f.Status, f.Filename, f.Additions, f.Deletions)
		}
		if b.Len() == 0 {
			return "This pull request changes no files.", nil
		}
		return b.String(), nil
	case "diff":
		files, err := s.client.PullFiles(ctx, install, owner, repo, number)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, f := range files {
			if args.Path != "" && f.Filename != args.Path {
				continue
			}
			fmt.Fprintf(&b, "diff --git a/%s b/%s\n", f.Filename, f.Filename)
			if f.Patch == "" {
				b.WriteString("(no diff shown: binary, or too large for GitHub to show)\n")
				continue
			}
			b.WriteString(f.Patch)
			b.WriteString("\n")
		}
		if b.Len() == 0 {
			return "", fmt.Errorf("%s is not a file this pull request changes", args.Path)
		}
		return capped(b.String(), "ask for one path"), nil
	case "file":
		if args.Path == "" {
			return "", errors.New("path is required")
		}
		info, err := s.client.Pull(ctx, install, owner, repo, number)
		if err != nil {
			return "", err
		}
		raw, err := s.client.FileAt(ctx, install, owner, repo, args.Path, info.SHA)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
				return "", fmt.Errorf("%s does not exist at the head commit %s", args.Path, info.SHA)
			}
			return "", err
		}
		return capped(numbered(string(raw), args.StartLine, args.EndLine), "ask for a line range"), nil
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// numbered is the lines start..end of text, each with its number. Zero means from the first
// or to the last.
func numbered(text string, start, end int) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if start < 1 {
		start = 1
	}
	if end < 1 || end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return fmt.Sprintf("(the file has %d lines)\n", len(lines))
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteString(": ")
		b.WriteString(lines[i-1])
		b.WriteByte('\n')
	}
	return b.String()
}

// capped cuts text at mcpMaxResult, saying how much is missing and what to ask for instead.
func capped(text, instead string) string {
	if len(text) <= mcpMaxResult {
		return text
	}
	return text[:mcpMaxResult] + fmt.Sprintf("\n(cut: %d more bytes — %s)\n", len(text)-mcpMaxResult, instead)
}
