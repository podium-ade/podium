package store

// A Linear ticket's conversation in a person's chat rail, and who each of its turns ran as
// (0049, docs/linear-agent-plan.md "Ticket chats").

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	db "github.com/podium-ade/podium/internal/agent/store/db"
)

// A person's one preference for a chat in their rail (chat_rail_prefs, 0055). Setting one
// replaces the other: watching a hidden chat un-hides it, hiding a watched one unwatches it.
const (
	RailWatched = "watched"
	RailHidden  = "hidden"
)

// SetChatRailHidden takes a chat out of login's chat rail, or puts it back. A hidden chat comes
// back by itself once something new is said in it (ListChats).
func (s *Store) SetChatRailHidden(ctx context.Context, chatID, login string, hidden bool, at time.Time) error {
	return s.setRailPref(ctx, chatID, login, RailHidden, hidden, at)
}

// SetChatWatched keeps a chat in login's chat rail, or stops. It grants nothing: the rail lists a
// watched chat only while login may read it, and checking that they may watch it at all is the
// caller's (the API's chatRole).
func (s *Store) SetChatWatched(ctx context.Context, chatID, login string, watched bool, at time.Time) error {
	return s.setRailPref(ctx, chatID, login, RailWatched, watched, at)
}

// ChatWatched reports whether login watches a chat.
func (s *Store) ChatWatched(ctx context.Context, chatID, login string) (bool, error) {
	if chatID == "" || login == "" {
		return false, nil
	}
	pref, err := s.q.ChatRailPref(ctx, db.ChatRailPrefParams{ChatID: chatID, Login: login})
	if err != nil {
		return false, fmt.Errorf("read chat %s's place in %s's rail: %w", chatID, login, err)
	}
	return pref == RailWatched, nil
}

func (s *Store) setRailPref(ctx context.Context, chatID, login, pref string, on bool, at time.Time) error {
	if chatID == "" || login == "" {
		return fmt.Errorf("%w: a chat's place in a rail needs a chat and a login", ErrNotFound)
	}
	var err error
	if on {
		err = s.q.SetChatRailPref(ctx, db.SetChatRailPrefParams{ChatID: chatID, Login: login, Pref: pref, SetAt: at.UTC()})
	} else {
		err = s.q.ClearChatRailPref(ctx, db.ClearChatRailPrefParams{ChatID: chatID, Login: login, Pref: pref})
	}
	if err != nil {
		return fmt.Errorf("set chat %s %s in %s's rail: %w", chatID, pref, login, err)
	}
	return nil
}

// TurnRunner is one turn of a conversation as a reader of it needs it: when it started, who it
// ran as, and the tasks it ran.
type TurnRunner struct {
	TurnID    string
	StartedAt time.Time
	// Login is the Podium person whose credentials the turn ran with (turns.started_by), "" when
	// it was nobody's: a system routine's run, a review the GitHub App ran, a turn from before
	// started_by was recorded.
	Login string
	// Routine is the routine whose run this was, by name, "" for any other turn. RoutineSystem
	// is a system routine's, which runs as the company.
	Routine       string
	RoutineSystem bool
	// Tasks is the task the turn ran itself and every task it delegated.
	Tasks []string
}

// ChatTurnRunners is every turn of the conversation chatID records, oldest first.
func (s *Store) ChatTurnRunners(ctx context.Context, chatID string) ([]TurnRunner, error) {
	rows, err := s.q.ListChatTurnRunners(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("list the turns of chat %s: %w", chatID, err)
	}
	out := make([]TurnRunner, 0, len(rows))
	for _, r := range rows {
		t := TurnRunner{
			TurnID:        r.ID,
			StartedAt:     r.StartedAt.UTC(),
			Login:         runnerLogin(r.StartedBy),
			Routine:       r.RoutineName,
			RoutineSystem: r.RoutineSystem,
		}
		if r.TaskID != "" {
			t.Tasks = append(t.Tasks, r.TaskID)
		}
		for _, id := range r.Delegated {
			if id != "" {
				t.Tasks = append(t.Tasks, id)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// runnerLogin is the person turns.started_by names, or "" for one of the markers that name
// nobody (TurnStartedByApp, TurnStartedByRoutine).
func runnerLogin(startedBy string) string {
	if startedBy == TurnStartedByApp || strings.HasPrefix(startedBy, "routine:") {
		return ""
	}
	return startedBy
}

// TurnIndex places what the bot said in a conversation on the turn that said it: a row with a
// task id on the turn that ran that task — its own, or one it delegated — and the assistant's
// own rows on the turn that was running when they were said. A conversation runs one turn at a
// time, and a turn's row is written before it says anything.
type TurnIndex struct {
	turns  []TurnRunner // oldest first
	byTask map[string]int
}

// NewTurnIndex indexes a conversation's turns.
func NewTurnIndex(turns []TurnRunner) TurnIndex {
	sorted := append([]TurnRunner(nil), turns...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].StartedAt.Before(sorted[j].StartedAt) })
	ix := TurnIndex{turns: sorted, byTask: map[string]int{}}
	for i, t := range sorted {
		for _, id := range t.Tasks {
			ix.byTask[id] = i
		}
	}
	return ix
}

// TurnOf is the turn a row said at at, with taskID, belongs to; nil when none can be named — a
// task no turn of the conversation ran, or a row from before its first turn.
func (ix TurnIndex) TurnOf(taskID string, at time.Time) *TurnRunner {
	if taskID != "" {
		if i, ok := ix.byTask[taskID]; ok {
			return &ix.turns[i]
		}
		return nil
	}
	i := sort.Search(len(ix.turns), func(i int) bool { return ix.turns[i].StartedAt.After(at) })
	if i == 0 {
		return nil
	}
	return &ix.turns[i-1]
}

// Latest is the newest turn, nil for a conversation with none.
func (ix TurnIndex) Latest() *TurnRunner {
	if len(ix.turns) == 0 {
		return nil
	}
	return &ix.turns[len(ix.turns)-1]
}
