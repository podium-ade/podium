package linear

import "github.com/podium-ade/podium/internal/agent/conductor"

// actionFor is the activity one runtime activity becomes, or false for one this source does
// not show (conductor.DescribeActivity says which).
//
// A tool call is an action: its verb, and what it was pointed at as the parameter. Reasoning
// is a thought: its first line, cut short.
func actionFor(text string) (ActivityContent, bool) {
	step, ok := conductor.DescribeActivity(text)
	if !ok {
		return ActivityContent{}, false
	}
	if step.Thought {
		return ActivityContent{Type: ActivityThought, Body: step.Target}, true
	}
	out := ActivityContent{Type: ActivityAction, Action: step.Verb, Parameter: step.Target}
	if step.Failed {
		out.Result = "Failed"
	}
	return out, true
}
