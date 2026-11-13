package conductor

// The Linear events a routine's run was given (docs/agent.md, "On a Linear event"). They are
// read from the database by the run's id when the brief is built, so the turn sees them on
// whichever conductor runs it, and they go into the brief's routine block as data: what people
// wrote in Linear, which the runtime fences off and tells the model not to obey.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/podium-ade/podium/internal/agent/store"
)

const (
	// maxEventDataBytes bounds one event's data in the brief, and maxEventUpdatedFromBytes
	// its updatedFrom. The whole brief must fit MaxBriefBytes beside the prompts, the
	// routine's state and the transcript, so an event is trimmed long before Linear's own
	// limit.
	maxEventDataBytes        = 8 * 1024
	maxEventUpdatedFromBytes = 2 * 1024
	// maxEventsBriefBytes bounds the events together. Past it an event is still named — what,
	// when, who and where — with its data left out, for the run to read from Linear.
	maxEventsBriefBytes = 20 * 1024
)

// BriefRoutineEvent is one Linear event a run was given. Everything in it but Event and At
// was written by people in Linear: it is data, never instructions.
type BriefRoutineEvent struct {
	// Event is "<Type>.<action>", as the routine names it ("Issue.create").
	Event string `json:"event"`
	// Label is the event in words ("issue created").
	Label string `json:"label"`
	// At is when Linear says it happened, or when it arrived when Linear does not say.
	At string `json:"at"`
	// Actor is the name of who made the change, "" when Linear names nobody.
	Actor string `json:"actor,omitempty"`
	URL   string `json:"url,omitempty"`
	// Data is the entity as Linear sent it, trimmed.
	Data json.RawMessage `json:"data"`
	// UpdatedFrom is, on an update, the changed fields' earlier values.
	UpdatedFrom json.RawMessage `json:"updated_from,omitempty"`
	// Trimmed says Data or UpdatedFrom is shorter than Linear sent it.
	Trimmed bool `json:"trimmed,omitempty"`
}

// eventPayload is a stored delivery, as much of it as a brief reads.
type eventPayload struct {
	Type        string          `json:"type"`
	Action      string          `json:"action"`
	Data        json.RawMessage `json:"data"`
	URL         string          `json:"url"`
	UpdatedFrom json.RawMessage `json:"updatedFrom"`
	CreatedAt   string          `json:"createdAt"`
	Actor       *struct {
		Name string `json:"name"`
	} `json:"actor"`
	TooLarge bool   `json:"tooLarge"`
	Note     string `json:"note"`
}

// briefRoutineEvents is the events run was given, as the brief carries them.
func (c *Conductor) briefRoutineEvents(ctx context.Context, runID string) []BriefRoutineEvent {
	events, err := c.store.RoutineEventsForRun(ctx, runID)
	if err != nil {
		c.logger.WarnContext(ctx, "reading a routine run's Linear events failed; briefing without them",
			"run_id", runID, "error", err)
		return nil
	}
	return BriefEvents(events)
}

// BriefEvents is events as a brief carries them, each trimmed and all of them within
// maxEventsBriefBytes.
func BriefEvents(events []store.RoutineEvent) []BriefRoutineEvent {
	out := make([]BriefRoutineEvent, 0, len(events))
	budget := maxEventsBriefBytes
	for _, e := range events {
		var p eventPayload
		_ = json.Unmarshal(e.Payload, &p)
		b := BriefRoutineEvent{
			Event: e.Event, Label: store.RoutineEventLabel(e.Event), At: BriefTimestamp(e.ReceivedAt), URL: p.URL,
		}
		if strings.TrimSpace(p.CreatedAt) != "" {
			b.At = p.CreatedAt
		}
		if p.Actor != nil {
			b.Actor = p.Actor.Name
		}
		switch {
		case p.TooLarge:
			b.Data, _ = json.Marshal(map[string]string{"note": p.Note})
			b.Trimmed = true
		default:
			var cut bool
			b.Data, cut = TrimJSON(p.Data, maxEventDataBytes)
			b.Trimmed = cut
			if len(p.UpdatedFrom) > 0 && string(p.UpdatedFrom) != "null" {
				b.UpdatedFrom, cut = TrimJSON(p.UpdatedFrom, maxEventUpdatedFromBytes)
				b.Trimmed = b.Trimmed || cut
			}
		}
		size := len(b.Data) + len(b.UpdatedFrom)
		if size > budget {
			b.Data, _ = json.Marshal(map[string]string{
				"note": "Left out: this run was given more Linear data than a brief holds. Read it from Linear.",
			})
			b.UpdatedFrom, b.Trimmed = nil, true
			size = len(b.Data)
		}
		budget -= size
		out = append(out, b)
	}
	return out
}

// TrimJSON is raw within limit bytes, and whether it had to be cut. Long strings are shortened
// and long lists cut first, more each pass; what still does not fit keeps its top-level
// scalars alone, and failing that a note. It is always valid JSON.
func TrimJSON(raw json.RawMessage, limit int) (json.RawMessage, bool) {
	var v any
	if len(strings.TrimSpace(string(raw))) == 0 || json.Unmarshal(raw, &v) != nil {
		return json.RawMessage("{}"), len(strings.TrimSpace(string(raw))) > 0
	}
	if b, err := json.Marshal(v); err == nil && len(b) <= limit {
		return b, false
	}
	for _, pass := range []struct{ runes, items int }{{2000, 50}, {500, 20}, {120, 5}} {
		if b, err := json.Marshal(shorten(v, pass.runes, pass.items)); err == nil && len(b) <= limit {
			return b, true
		}
	}
	if m, ok := v.(map[string]any); ok {
		kept := map[string]any{"_trimmed": "nested fields left out: too large to brief"}
		for k, val := range m {
			switch val.(type) {
			case map[string]any, []any:
			default:
				kept[k] = shorten(val, 120, 0)
			}
		}
		if b, err := json.Marshal(kept); err == nil && len(b) <= limit {
			return b, true
		}
	}
	return json.RawMessage(`{"_trimmed":"too large to brief; read it from Linear"}`), true
}

// shorten is a copy of v with every string at most runes long and every list at most items.
func shorten(v any, runes, items int) any {
	switch t := v.(type) {
	case string:
		if utf8.RuneCountInString(t) > runes {
			return string([]rune(t)[:runes]) + "… [trimmed]"
		}
		return t
	case []any:
		n := len(t)
		if n > items {
			n = items
		}
		out := make([]any, 0, n+1)
		for _, item := range t[:n] {
			out = append(out, shorten(item, runes, items))
		}
		if len(t) > n {
			out = append(out, fmt.Sprintf("… %d more", len(t)-n))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			out[k] = shorten(item, runes, items)
		}
		return out
	default:
		return v
	}
}

// eventSubject names what an event is about, for the run's divider: "ENG-12 created", "comment
// on ENG-12 created", "project “Launch” updated". It reads the identifier or name Linear sent,
// and falls back to the event in words.
func eventSubject(e store.RoutineEvent) string {
	var p struct {
		Type   string `json:"type"`
		Action string `json:"action"`
		Data   struct {
			Identifier string `json:"identifier"`
			Name       string `json:"name"`
			Issue      *struct {
				Identifier string `json:"identifier"`
			} `json:"issue"`
		} `json:"data"`
	}
	_ = json.Unmarshal(e.Payload, &p)
	label := store.RoutineEventLabel(e.Event)
	typ, _, _ := strings.Cut(e.Event, ".")
	verb := label[strings.LastIndex(label, " ")+1:]
	switch {
	case typ == "Issue" && p.Data.Identifier != "":
		return p.Data.Identifier + " " + verb
	case typ == "Comment" && p.Data.Issue != nil && p.Data.Issue.Identifier != "":
		return "comment on " + p.Data.Issue.Identifier + " " + verb
	case p.Data.Name != "" && utf8.RuneCountInString(p.Data.Name) <= 60:
		return strings.TrimSuffix(label, " "+verb) + " “" + p.Data.Name + "” " + verb
	}
	return label
}

// eventDivider is what a run's divider adds for the events it was given: "Linear: ENG-12
// created", "Linear: ENG-12 created and 3 more".
func eventDivider(events []store.RoutineEvent) string {
	if len(events) == 0 {
		return ""
	}
	out := "Linear: " + eventSubject(events[0])
	if len(events) > 1 {
		out += fmt.Sprintf(" and %d more", len(events)-1)
	}
	return out
}
