package conductor

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// MaxStepTargetRunes is the longest thing a tool call is said to be pointed at, and
// MaxStepThoughtRunes the longest a thought is said in. A step is one line ("Reading
// internal/agent/linear/app.go"), not a log: the transcript artifact on the task keeps every
// tool call whole for anybody who needs more.
const (
	MaxStepTargetRunes  = 120
	MaxStepThoughtRunes = 280
)

// ActivityStep is one activity document said in a line: what a source that shows a turn's
// steps — a Linear agent session, a Slack thread — draws for it.
type ActivityStep struct {
	// Thought is true for a block of reasoning, false for a tool call.
	Thought bool
	// Verb is what a tool call did ("Reading"), or "Using" for a tool not known here. Empty
	// for a thought.
	Verb string
	// Target is what a tool call was pointed at — a path, a command, a pattern — or a tool
	// not known here by its name. For a thought it is the thought's first line. Cut to its
	// first line and to MaxStepTargetRunes or MaxStepThoughtRunes; possibly empty for a tool.
	Target string
	// Failed is a tool call that ended in an error.
	Failed bool
}

// Line is the step as one line of text: "Reading internal/x.go", or the thought.
func (s ActivityStep) Line() string {
	return strings.TrimSpace(s.Verb + " " + s.Target)
}

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

// toolAction is how one harness tool is said: the verb, and the field of its input that
// names what it was pointed at. The fields are the ones the web chat summarises a tool call
// by (web/src/lib/activity.ts), so every surface says the same thing about one call.
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

// DescribeActivity is the step one activity document becomes, or false for one no source
// shows: a document that does not parse, or a kind not known here.
//
// A tool call's target is the one input field that names what the tool was pointed at. Never
// the rest of the input, which for a write is the file's contents and for an MCP tool can be
// anything, and never the output. A tool not known here is shown by its name alone, for the
// same reason.
//
// A thought is its first line, cut short. The point is that somebody watching sees the turn
// is thinking about something, not what it is thinking in full.
func DescribeActivity(text string) (ActivityStep, bool) {
	var a runtimeActivity
	if err := json.Unmarshal([]byte(text), &a); err != nil {
		return ActivityStep{}, false
	}
	switch a.Kind {
	case "reasoning":
		// A model often heads its reasoning with a bold title, whose asterisks would show.
		line := Clip(strings.TrimSpace(strings.Trim(FirstLine(a.Text), "*")), MaxStepThoughtRunes)
		if line == "" {
			return ActivityStep{}, false
		}
		return ActivityStep{Thought: true, Target: line}, true
	case "tool":
		if a.Tool == "" {
			return ActivityStep{}, false
		}
		out := ActivityStep{Verb: "Using", Target: a.Tool, Failed: a.Status == "error"}
		if known, ok := toolActions[a.Tool]; ok {
			out.Verb = known.verb
			out.Target = Clip(FirstLine(inputField(a.Input, known.field)), MaxStepTargetRunes)
		}
		return out, true
	default:
		return ActivityStep{}, false
	}
}

// inputField is the first of fields that is a non-empty string in a tool's input, which the
// runtime sends as JSON text. Anything else — input that is not an object, a field that is
// not a string — is nothing: the step is then shown by its verb alone.
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

// FirstLine is s's first non-blank-trimmed line.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// Clip cuts s to n runes, marking the cut.
func Clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
