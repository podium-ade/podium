package conductor

// A routine that starts a playbook right away (docs/agent.md, "Routines"). Its run is no
// assistant's turn deciding what to do: the conductor delegates the routine's playbook itself,
// with the instruction, the notes and the events as the task's brief, as it delegates a pull
// request's review (acceptAutomatic). The run is still its routine's — its owner's, or nobody's
// for a system routine — and it is linked to the turn the delegation hangs off, so the
// scheduler settles it, and its spend is counted, as any run's.
//
// A run in a pull request's conversation is that conversation's GitHub turn, not the web
// chat's, so its task works on the pull request's workspace and is briefed as one on a pull
// request. Nobody on GitHub asked for it, so its answer stays in Podium: a review playbook
// posts its review on the pull request itself. That is what lets the review on a pull request
// being opened be a routine.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/podium-ade/podium/internal/agent/store"
)

// RoutinePlaybookRun is a run of a routine that starts its playbook right away, as the
// scheduler hands it over.
type RoutinePlaybookRun struct {
	RoutineID string
	RunID     string
	// Playbook is the routine's: a playbook's name, or store.RoutineReviewPlaybook.
	Playbook string
	// Login is the routine's owner, "" for a system routine.
	Login string
	// ChatID is the chat the run is in: the routine's own, or the conversation's.
	ChatID string
	// ConversationKey is the conversation the run is a turn of — a pull request's
	// (github:owner/repo#N), a Linear issue's — and "" for the routine's own chat.
	ConversationKey string
	// Title names the conversation ("#42: Fix login").
	Title string
}

// reviewFollowUp is a source that can say where a pull request's last review stopped: the
// GitHub source.
type reviewFollowUp interface {
	ReviewFollowUp(ctx context.Context, sourceKey string) string
}

// refForKey is a source that names a conversation by its key alone.
type refForKey interface {
	RefForKey(sourceKey string) (string, bool)
}

// ErrNoRoutinePlaybook is a routine's playbook that cannot run: it is not in the profile, or
// no playbook reviews the pull request's repository.
var ErrNoRoutinePlaybook = errors.New("the routine's playbook cannot run")

// RunRoutinePlaybook starts run's playbook. It returns once the run is handed over; the
// delegation and its turn go on in the background. An error is a run that did not start.
func (c *Conductor) RunRoutinePlaybook(ctx context.Context, run RoutinePlaybookRun) error {
	if run.RunID == "" || run.RoutineID == "" || run.ChatID == "" {
		return errors.New("run routine playbook: a routine, a run and a chat are required")
	}
	r, err := c.store.GetRoutine(ctx, run.RoutineID)
	if err != nil {
		return err
	}
	src, ref, key := c.sourceOf(SourceChat), run.ChatID, store.ChatSourceKey(run.ChatID)
	if run.ConversationKey != "" {
		key = run.ConversationKey
	}
	if store.IsGitHubConversation(key) {
		// The pull request's own conversation, so its task answers on the pull request.
		if gh, ok := c.sourceOf(SourceGitHub).(refForKey); ok {
			if ghRef, ok := gh.RefForKey(key); ok {
				src, ref = c.sourceOf(SourceGitHub), ghRef
			}
		}
	}
	if src == nil {
		return errors.New("run routine playbook: no source can carry the run")
	}
	profile := c.profiles.Current()
	name := run.Playbook
	reviewer := ""
	if repo := pullRequestRepo(key); repo != "" {
		reviewer = profile.GitHubPlaybookFor(repo)
	}
	if name == store.RoutineReviewPlaybook {
		if reviewer == "" {
			return fmt.Errorf("%w: no playbook reviews pull requests in %s. Name it in a playbook's "+
				"github_repos, or mark one with github: true", ErrNoRoutinePlaybook, cmpOrString(pullRequestRepo(key), "this repository"))
		}
		name = reviewer
	}
	if _, ok := profile.Playbooks[name]; !ok {
		return fmt.Errorf("%w: there is no playbook named %q", ErrNoRoutinePlaybook, name)
	}
	text := c.routinePlaybookBrief(ctx, r, run.RunID)
	if name == reviewer && src.Kind() == SourceGitHub {
		// A review of a pull request reviewed before starts where that review stopped.
		if f, ok := src.(reviewFollowUp); ok {
			if note := f.ReviewFollowUp(ctx, key); note != "" {
				text = note + "\n\n" + text
			}
		}
	}
	ev := InboundEvent{
		SourceKind: src.Kind(),
		SourceKey:  key,
		Ref:        ref,
		Login:      run.Login,
		Text:       text,
		TS:         c.now().UTC(),
		Title:      run.Title,
		BriefKind:  src.Kind(),
		Automatic:  true,
		Playbook:   name,
		ChatID:     run.ChatID,
		Routine:    &RoutineRef{RoutineID: run.RoutineID, RunID: run.RunID},
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.acceptAutomatic(c.background(), src, profile, ev)
	}()
	return nil
}

// routinePlaybookBrief is what a routine's playbook is asked: the instruction, the notes and
// the events that started the run.
func (c *Conductor) routinePlaybookBrief(ctx context.Context, r store.Routine, runID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This task was started by the routine %q", r.Name)
	if r.OnEvents() {
		fmt.Fprintf(&b, " (%s)", store.DescribeEventTrigger(r.Kind, r.Events, r.Teams))
	}
	b.WriteString(". Do what its instruction asks.\n\nInstruction:\n")
	b.WriteString(strings.TrimSpace(r.Instruction))
	if notes := strings.TrimSpace(r.Notes); notes != "" {
		b.WriteString("\n\nThe routine's standing notes:\n")
		b.WriteString(notes)
	}
	if r.OnEvents() {
		if events := c.briefRoutineEvents(ctx, runID); len(events) > 0 {
			if raw, err := json.MarshalIndent(events, "", "  "); err == nil {
				b.WriteString("\n\nThe events that started this run:\n```json\n")
				b.Write(raw)
				b.WriteString("\n```")
			}
		}
	}
	return b.String()
}

func cmpOrString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
