package conductor

// The tools a chat-host turn calls: MCP servers on this conductor, one per name, at
// /chat-host/mcp/<conversation token>/<server>. Each answers for the turn the conversation is
// running, and refuses between turns. They are the runtime's delegation and title servers
// (agent/runtime/src/mcp.ts, title.ts) — the same tools, with the same names and the same
// words, because the prompt the runtime renders talks about them — served here because there
// is no runtime process to run them in.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/podium-ade/podium/internal/agent/store"
)

// The servers a chat-host location names on this conductor.
const (
	chatDelegateServer = "podium"
	chatTitleServer    = "title"
)

const chatMCPProtocol = "2025-06-18"

// chatTitleMax matches the runtime's MaxTitleRunes; the chat clamps again.
const chatTitleMax = 48

type chatTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func idOnly() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"delegation_id": map[string]any{"type": "string"}},
		"required":             []string{"delegation_id"},
		"additionalProperties": false,
	}
}

var chatDelegateTools = []chatTool{
	{Name: "delegate", Description: "Run work in a Podium task, on a machine with a repository checked out and, depending " +
		"on the playbook, a Docker daemon and a browser. Use it for anything you cannot do " +
		"from this conversation: reading or changing a repository, running a build or a test " +
		"suite, driving a browser, or any command at all. Returns immediately with a " +
		"delegation id — the task then runs for minutes or hours, and its progress and its " +
		"answer appear in this conversation as they happen, so do not repeat them. Poll " +
		"check_delegation for the outcome. The instruction is the ONLY thing the task is told " +
		"beyond this conversation's transcript, so write it as a complete brief.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"playbook":    map[string]any{"type": "string", "description": "One of the playbooks your brief listed under delegation.playbooks."},
				"instruction": map[string]any{"type": "string", "description": "The complete brief for the task: what to do, and how you will know it worked."},
				"read_only": map[string]any{"type": "boolean", "description": "True when the task only reads — to answer a question about the code. It starts " +
					"from this conversation's workspace and keeps nothing it changes, so it never waits " +
					"for, or interrupts, a task that is changing that workspace."},
			},
			"required":             []string{"playbook", "instruction"},
			"additionalProperties": false,
		}},
	{Name: "check_delegation", Description: "Where a delegated task has got to, and its answer once it has one. status is " +
		"running, succeeded, failed, lost, cancelled or timeout. While it is running you also " +
		"get the last thing the task said, which is how you tell work from a hang. A task can " +
		"take a long time: poll rather than waiting, and tell the person what it is doing.",
		InputSchema: idOnly()},
	{Name: "list_delegations", Description: "Delegated tasks of this conversation: everything this turn started, plus any still " +
		"running from an earlier turn. Use it to find an in-flight task before injecting.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
	{Name: "inject_delegation", Description: "Send more instruction into a running delegated task of this conversation, as a new " +
		"user message in the same container. Use it when the person is correcting or adding " +
		"to work already in flight — \"actually change xyz\", \"use main\" — instead of " +
		"starting a second task. The workspace is kept. The task must still be running.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"delegation_id": map[string]any{"type": "string"},
				"text":          map[string]any{"type": "string", "description": "What the person said, as the task should hear it."},
			},
			"required":             []string{"delegation_id", "text"},
			"additionalProperties": false,
		}},
	{Name: "cancel_delegation", Description: "Stop a delegated task. Use it when the work is no longer wanted or is plainly stuck — " +
		"a task nobody is waiting for still holds a machine and still costs money.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"delegation_id": map[string]any{"type": "string"},
				"reason":        map[string]any{"type": "string", "description": "Why, for the operator's log. Optional."},
			},
			"required":             []string{"delegation_id"},
			"additionalProperties": false,
		}},
	{Name: "release_preview", Description: "Take down the preview a finished delegated task left up — its containers and its " +
		"URLs. Use it when the person says they are done with it.",
		InputSchema: idOnly()},
}

var chatTitleTools = []chatTool{{
	Name: "set",
	Description: "Name this conversation. Call it once, on this first turn, with a 3–6 word title: " +
		"one line, no quotes, no trailing punctuation. Do not mention the title or this tool " +
		"in your answer to the person.",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string", "description": "The conversation's name. 3–6 words, one line, no quotes."},
		},
		"required":             []string{"title"},
		"additionalProperties": false,
	},
}}

// ChatHostMCP is the chat host's tool servers. pullRequest is the conductor's read-only
// pull-request server, which takes a turn's own token as its bearer; it is served here for
// the turn the conversation is running. Nil leaves that server out.
func (c *Conductor) ChatHostMCP(pullRequest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, _ := strings.CutPrefix(r.URL.Path, chatMCPPath)
		token, server, _ := strings.Cut(rest, "/")
		_, turn := c.chatByToken(token)
		if turn == nil {
			http.Error(w, "no turn is running in this conversation", http.StatusUnauthorized)
			return
		}
		if server == PullRequestMCPName {
			if pullRequest == nil || turn.delegationToken == "" {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer "+turn.delegationToken)
			pullRequest.ServeHTTP(w, r)
			return
		}
		var tools []chatTool
		switch server {
		case chatDelegateServer:
			if turn.delegationToken == "" {
				http.NotFound(w, r)
				return
			}
			tools = chatDelegateTools
		case chatTitleServer:
			tools = chatTitleTools
		default:
			http.NotFound(w, r)
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
				"serverInfo":      map[string]any{"name": server, "version": "1"},
			}, nil)
		case "ping":
			chatRPC(w, req.ID, map[string]any{}, nil)
		case "tools/list":
			chatRPC(w, req.ID, map[string]any{"tools": tools}, nil)
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				chatRPC(w, req.ID, nil, &chatRPCError{Code: -32602, Message: "tools/call needs a name and arguments"})
				return
			}
			var text string
			var callErr error
			if server == chatTitleServer {
				text, callErr = c.chatSetTitle(token, p.Name, p.Arguments)
			} else {
				text, callErr = c.chatDelegateTool(r.Context(), turn.delegationToken, p.Name, p.Arguments)
			}
			if callErr != nil {
				chatRPC(w, req.ID, chatToolResult(callErr.Error(), true), nil)
				return
			}
			chatRPC(w, req.ID, chatToolResult(text, false), nil)
		default:
			chatRPC(w, req.ID, nil, &chatRPCError{Code: -32601, Message: "unknown method " + req.Method})
		}
	})
}

type chatRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func chatRPC(w http.ResponseWriter, id json.RawMessage, result any, rerr *chatRPCError) {
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

func chatToolResult(text string, failed bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": failed}
}

func argString(args map[string]any, key string) (string, error) {
	v, _ := args[key].(string)
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

// chatDelegateTool runs one delegation tool for the turn token holds, and says what happened
// in the words the runtime's own server uses.
func (c *Conductor) chatDelegateTool(ctx context.Context, token, name string, args map[string]any) (string, error) {
	switch name {
	case "delegate":
		playbook, err := argString(args, "playbook")
		if err != nil {
			return "", err
		}
		instruction, err := argString(args, "instruction")
		if err != nil {
			return "", err
		}
		readOnly, _ := args["read_only"].(bool)
		d, err := c.Delegate(ctx, token, playbook, instruction, DelegateOptions{ReadOnly: readOnly})
		if err != nil {
			return "", err
		}
		task := d.TaskID
		if task == "" {
			task = "(starting)"
		}
		return fmt.Sprintf("Delegated to `%s`.\ndelegation_id: %s\ntask: %s\n\n"+
			"Its progress and its answer go into this conversation on their own. Poll "+
			"check_delegation for the outcome, and tell the person what it is doing rather "+
			"than waiting in silence.", d.Playbook, d.ID, task), nil
	case "check_delegation":
		id, err := argString(args, "delegation_id")
		if err != nil {
			return "", err
		}
		d, progress, err := c.GetDelegation(ctx, token, id)
		if err != nil {
			return "", err
		}
		return describeDelegation(d, progress), nil
	case "list_delegations":
		all, err := c.Delegations(ctx, token)
		if err != nil {
			return "", err
		}
		if len(all) == 0 {
			return "No delegated tasks in this conversation.", nil
		}
		parts := make([]string, 0, len(all))
		for _, d := range all {
			parts = append(parts, describeDelegation(d, ""))
		}
		return strings.Join(parts, "\n\n"), nil
	case "inject_delegation":
		id, err := argString(args, "delegation_id")
		if err != nil {
			return "", err
		}
		text, err := argString(args, "text")
		if err != nil {
			return "", err
		}
		d, err := c.InjectDelegation(ctx, token, id, text)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Sent that to the running `%s` task (%s). It will pick it up in the same "+
			"container. Do not start another task for this.", d.Playbook, d.ID), nil
	case "cancel_delegation":
		id, err := argString(args, "delegation_id")
		if err != nil {
			return "", err
		}
		reason, _ := args["reason"].(string)
		d, err := c.CancelDelegation(ctx, token, id, reason)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Asked the node to stop %s (task %s).", d.ID, orUnknown(d.TaskID)), nil
	case "release_preview":
		id, err := argString(args, "delegation_id")
		if err != nil {
			return "", err
		}
		d, err := c.ReleaseDelegationPreview(ctx, token, id)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Released the preview of %s (task %s). Its URLs no longer answer.", d.ID, orUnknown(d.TaskID)), nil
	}
	return "", fmt.Errorf("unknown tool %s", name)
}

func orUnknown(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func describeDelegation(d store.Delegation, progress string) string {
	lines := []string{fmt.Sprintf("%s — `%s` — %s", d.ID, d.Playbook, d.Status)}
	if d.TaskID != "" {
		lines = append(lines, "task: "+d.TaskID)
	}
	if d.Status == "running" && strings.TrimSpace(progress) != "" {
		lines = append(lines, "last said: "+strings.TrimSpace(progress))
	}
	if strings.TrimSpace(d.FinalText) != "" {
		lines = append(lines, "", strings.TrimSpace(d.FinalText))
	}
	return strings.Join(lines, "\n")
}

// chatSetTitle records the chat's name for the running turn. The turn applies it when it
// succeeds, as a host turn applies the file its runtime wrote.
func (c *Conductor) chatSetTitle(token, name string, args map[string]any) (string, error) {
	if name != "set" {
		return "", fmt.Errorf("unknown tool %s", name)
	}
	title, err := argString(args, "title")
	if err != nil {
		return "", err
	}
	title = strings.TrimSpace(strings.SplitN(title, "\n", 2)[0])
	title = strings.Trim(title, "\"'“”")
	if utf8.RuneCountInString(title) > chatTitleMax {
		title = string([]rune(title)[:chatTitleMax])
	}
	c.chat.mu.Lock()
	defer c.chat.mu.Unlock()
	cv := c.chat.byToken[token]
	if cv == nil || cv.turn == nil {
		return "", fmt.Errorf("no turn is running in this conversation")
	}
	cv.turn.title = title
	return "Named.", nil
}
