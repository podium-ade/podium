package linear

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// maxParameterRunes is the longest parameter an action carries, and maxThoughtRunes the
// longest reasoning thought. An action is one line beside the session ("Reading
// internal/agent/linear/app.go"), not a log: the transcript artifact on the task keeps every
// tool call whole for anybody who needs more.
const (
	maxParameterRunes = 120
	maxThoughtRunes   = 280
)

// runtimeActivity is the document the runtime sends as an `activity` message
// (agent/runtime/src/activity.ts): one finished tool call, or one block of reasoning. It is a
// task's output and untrusted; only these fields are read, and only as strings.
type runtimeActivity struct {
	Kind   string `json:"kind"`
	Tool   string `json:"tool"`
	Status string `json:"status"`
	Input  string `json:"input"`
	Text   string `json:"text"`
}

// toolAction is how one harness tool is said in a session: the verb, and the field of its
// input that names what it was pointed at. The fields are the ones the web chat summarises a
// tool call by (web/src/lib/activity.ts), so both surfaces say the same thing about one call.
type toolAction struct {
	verb  string
	field []string
}

var toolActions = map[string]toolAction{
	"bash":     {"Running", []string{"command"}},
	"read":     {"Reading", []string{"filePath", "path"}},
	"edit":     {"Editing", []string{"filePath", "path"}},
	"patch":    {"Editing", []string{"filePath", "path"}},
	"write":    {"Writing", []string{"filePath", "path"}},
	"list":     {"Listing", []string{"path"}},
	"grep":     {"Searching for", []string{"pattern"}},
	"glob":     {"Finding", []string{"pattern"}},
	"webfetch": {"Fetching", []string{"url"}},
	"task":     {"Delegating", []string{"description"}},
}

// actionFor is the activity one runtime activity becomes, or false for one this source does
// not show: a document that does not parse, or a kind it does not know.
//
// A tool call is an action. Its parameter is the one input field that names what the tool
// was pointed at, cut to its first line and to maxParameterRunes: a path, a command, a
// pattern. Never the rest of the input, which for a write is the file's contents and for an
// MCP tool can be anything, and never the output. A tool this source does not know is shown
// by its name alone, for the same reason.
//
// Reasoning is a thought: its first line, cut short. The point is that somebody watching the
// session sees the turn is thinking about something, not what it is thinking in full.
func actionFor(text string) (ActivityContent, bool) {
	var a runtimeActivity
	if err := json.Unmarshal([]byte(text), &a); err != nil {
		return ActivityContent{}, false
	}
	switch a.Kind {
	case "reasoning":
		// A model often heads its reasoning with a bold title, which Linear would show with
		// its asterisks.
		line := clip(strings.TrimSpace(strings.Trim(firstLine(a.Text), "*")), maxThoughtRunes)
		if line == "" {
			return ActivityContent{}, false
		}
		return ActivityContent{Type: ActivityThought, Body: line}, true
	case "tool":
		if a.Tool == "" {
			return ActivityContent{}, false
		}
		out := ActivityContent{Type: ActivityAction, Action: "Using", Parameter: a.Tool}
		if known, ok := toolActions[a.Tool]; ok {
			out.Action = known.verb
			out.Parameter = clip(firstLine(inputField(a.Input, known.field)), maxParameterRunes)
		}
		if a.Status == "error" {
			out.Result = "Failed"
		}
		return out, true
	default:
		return ActivityContent{}, false
	}
}

// inputField is the first of fields that is a non-empty string in a tool's input, which the
// runtime sends as JSON text. Anything else — input that is not an object, a field that is
// not a string — is nothing: the action is then shown by its verb alone.
func inputField(input string, fields []string) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(input), &obj); err != nil {
		return ""
	}
	for _, f := range fields {
		if v, ok := obj[f].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// clip cuts s to n runes, marking the cut.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
