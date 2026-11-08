package api

// Routines (docs/agent.md, "Routines"). A routine is its owner's alone: every handler here
// answers only for routines the caller owns, and the dev token for all of them. Another
// person's routine is not found, the same answer every other personal read gives.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/routines"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// RoutineRunner starts a run outside a routine's schedule. The scheduler implements it.
type RoutineRunner interface {
	RunNow(ctx context.Context, routineID string) (store.RoutineRun, error)
}

// SetRoutines hands the service the scheduler, which is built after it.
func (s *AgentService) SetRoutines(runner RoutineRunner) {
	s.routineMu.Lock()
	defer s.routineMu.Unlock()
	s.routineRunner = runner
}

func (s *AgentService) routineDeps() RoutineRunner {
	s.routineMu.Lock()
	defer s.routineMu.Unlock()
	return s.routineRunner
}

// ListRoutines is the caller's routines; every routine for the dev token.
func (s *AgentService) ListRoutines(
	ctx context.Context, _ *connect.Request[agentv1.ListRoutinesRequest],
) (*connect.Response[agentv1.ListRoutinesResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	owner := login
	if SeesAll(ctx) {
		owner = ""
	}
	rows, err := s.store.ListRoutines(ctx, owner)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.Routine, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.routineToProto(ctx, r))
	}
	return connect.NewResponse(&agentv1.ListRoutinesResponse{Routines: out}), nil
}

// GetRoutine is one of the caller's routines, by id or by its chat.
func (s *AgentService) GetRoutine(
	ctx context.Context, req *connect.Request[agentv1.GetRoutineRequest],
) (*connect.Response[agentv1.GetRoutineResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	var r store.Routine
	switch {
	case req.Msg.GetId() != "":
		r, err = s.ownedRoutine(ctx, req.Msg.GetId(), login)
	case req.Msg.GetChatId() != "":
		r, err = s.store.RoutineByChat(ctx, req.Msg.GetChatId())
		if err == nil && !s.mayManageRoutine(ctx, r, login) {
			err = fmt.Errorf("%w: routine of chat %s", store.ErrNotFound, req.Msg.GetChatId())
		}
		err = routineError(err)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("an id or a chat_id is required"))
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.GetRoutineResponse{Routine: s.routineToProto(ctx, r)}), nil
}

// CreateRoutine opens a routine and its chat for the caller.
func (s *AgentService) CreateRoutine(
	ctx context.Context, req *connect.Request[agentv1.CreateRoutineRequest],
) (*connect.Response[agentv1.CreateRoutineResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.createRoutine(ctx, login, req.Msg.GetDraft())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.CreateRoutineResponse{Routine: s.routineToProto(ctx, r)}), nil
}

// CreateRoutineFor opens a routine for owner that an assistant set up in a conversation at
// their request (the create_routine tool). It is checked exactly as the person's own would be:
// the same schedule floor, the same count, the same assistants. The error's message is the
// reason alone, for the assistant to tell the person.
func (s *AgentService) CreateRoutineFor(ctx context.Context, owner string, d store.RoutineDraft) (store.Routine, error) {
	r, err := s.createRoutine(ctx, owner, &agentv1.RoutineDraft{
		Name: d.Name, Schedule: d.Schedule, Timezone: d.Timezone, Instruction: d.Instruction,
		Notes: d.Notes, PersonalityId: d.PersonalityID, Model: d.Model, Effort: d.Effort,
	})
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		return store.Routine{}, errors.New(cerr.Message())
	}
	return r, err
}

func (s *AgentService) createRoutine(ctx context.Context, login string, in *agentv1.RoutineDraft) (store.Routine, error) {
	if strings.TrimSpace(login) == "" {
		return store.Routine{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("a routine needs a person to own it"))
	}
	if err := s.chatEnabled(); err != nil {
		return store.Routine{}, err
	}
	existing, err := s.store.ListRoutines(ctx, login)
	if err != nil {
		return store.Routine{}, storeError(err)
	}
	if len(existing) >= MaxRoutinesPerPerson {
		return store.Routine{}, connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("you have %d routines, which is as many as one person may; delete one first", len(existing)))
	}
	draft, display, err := s.routineDraft(ctx, in, login, "")
	if err != nil {
		return store.Routine{}, err
	}
	r, err := s.store.CreateRoutine(ctx, login, draft, display)
	if err != nil {
		return store.Routine{}, routineError(err)
	}
	s.logger.InfoContext(ctx, "a routine was created", "routine_id", r.ID, "login", login, "schedule", r.Schedule)
	return r, nil
}

// UpdateRoutine replaces what the caller edits on one of their routines.
func (s *AgentService) UpdateRoutine(
	ctx context.Context, req *connect.Request[agentv1.UpdateRoutineRequest],
) (*connect.Response[agentv1.UpdateRoutineResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.ownedRoutine(ctx, req.Msg.GetId(), login)
	if err != nil {
		return nil, err
	}
	draft, _, err := s.routineDraft(ctx, req.Msg.GetDraft(), r.OwnerLogin, r.ID)
	if err != nil {
		return nil, err
	}
	if draft.PersonalityID != r.PersonalityID {
		// The chat was opened with one voice and stays labelled with it; a routine changing
		// voices would leave the chat saying the wrong name.
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a routine's assistant is chosen when it is created and cannot be changed"))
	}
	updated, err := s.store.UpdateRoutine(ctx, r.ID, draft)
	if err != nil {
		return nil, routineError(err)
	}
	return connect.NewResponse(&agentv1.UpdateRoutineResponse{Routine: s.routineToProto(ctx, updated)}), nil
}

// DeleteRoutine removes one of the caller's routines. Its chat stays.
func (s *AgentService) DeleteRoutine(
	ctx context.Context, req *connect.Request[agentv1.DeleteRoutineRequest],
) (*connect.Response[agentv1.DeleteRoutineResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.ownedRoutine(ctx, req.Msg.GetId(), login)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteRoutine(ctx, r.ID); err != nil {
		return nil, routineError(err)
	}
	s.logger.InfoContext(ctx, "a routine was deleted", "routine_id", r.ID, "login", login)
	return connect.NewResponse(&agentv1.DeleteRoutineResponse{}), nil
}

// PauseRoutine stops a routine running until it is resumed.
func (s *AgentService) PauseRoutine(
	ctx context.Context, req *connect.Request[agentv1.PauseRoutineRequest],
) (*connect.Response[agentv1.PauseRoutineResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.ownedRoutine(ctx, req.Msg.GetId(), login)
	if err != nil {
		return nil, err
	}
	r, err = s.store.SetRoutineEnabled(ctx, r.ID, false, "", r.NextRunAt)
	if err != nil {
		return nil, routineError(err)
	}
	return connect.NewResponse(&agentv1.PauseRoutineResponse{Routine: s.routineToProto(ctx, r)}), nil
}

// ResumeRoutine starts a paused routine again from the next time its schedule names. The runs
// it missed while paused are not made up.
func (s *AgentService) ResumeRoutine(
	ctx context.Context, req *connect.Request[agentv1.ResumeRoutineRequest],
) (*connect.Response[agentv1.ResumeRoutineResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.ownedRoutine(ctx, req.Msg.GetId(), login)
	if err != nil {
		return nil, err
	}
	sched, err := routines.Parse(r.Schedule, r.Timezone)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	r, err = s.store.SetRoutineEnabled(ctx, r.ID, true, "", sched.NextFor(r.ID, time.Now()))
	if err != nil {
		return nil, routineError(err)
	}
	return connect.NewResponse(&agentv1.ResumeRoutineResponse{Routine: s.routineToProto(ctx, r)}), nil
}

// RunRoutineNow starts a run at once.
func (s *AgentService) RunRoutineNow(
	ctx context.Context, req *connect.Request[agentv1.RunRoutineNowRequest],
) (*connect.Response[agentv1.RunRoutineNowResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.ownedRoutine(ctx, req.Msg.GetId(), login)
	if err != nil {
		return nil, err
	}
	runner := s.routineDeps()
	if runner == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("routines are not running on this conductor"))
	}
	run, err := runner.RunNow(ctx, r.ID)
	if errors.Is(err, routines.ErrBusy) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if err != nil {
		return nil, routineError(err)
	}
	return connect.NewResponse(&agentv1.RunRoutineNowResponse{Run: routineRunToProto(run)}), nil
}

// ResetRoutineState empties what the agent kept across runs.
func (s *AgentService) ResetRoutineState(
	ctx context.Context, req *connect.Request[agentv1.ResetRoutineStateRequest],
) (*connect.Response[agentv1.ResetRoutineStateResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.ownedRoutine(ctx, req.Msg.GetId(), login)
	if err != nil {
		return nil, err
	}
	r, err = s.store.SetRoutineState(ctx, r.ID, []byte("{}"))
	if err != nil {
		return nil, routineError(err)
	}
	return connect.NewResponse(&agentv1.ResetRoutineStateResponse{Routine: s.routineToProto(ctx, r)}), nil
}

// ResolveRoutineNote accepts the proposed note into the notes, or dismisses it.
func (s *AgentService) ResolveRoutineNote(
	ctx context.Context, req *connect.Request[agentv1.ResolveRoutineNoteRequest],
) (*connect.Response[agentv1.ResolveRoutineNoteResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.ownedRoutine(ctx, req.Msg.GetId(), login)
	if err != nil {
		return nil, err
	}
	notes := r.Notes
	if req.Msg.GetAccept() && strings.TrimSpace(r.ProposedNote) != "" {
		notes = strings.TrimSpace(strings.TrimSpace(notes) + "\n- " + strings.TrimPrefix(strings.TrimSpace(r.ProposedNote), "- "))
	}
	r, err = s.store.SetRoutineNotes(ctx, r.ID, notes, "")
	if err != nil {
		return nil, routineError(err)
	}
	return connect.NewResponse(&agentv1.ResolveRoutineNoteResponse{Routine: s.routineToProto(ctx, r)}), nil
}

// ListRoutineRuns pages one of the caller's routine's runs, newest first.
func (s *AgentService) ListRoutineRuns(
	ctx context.Context, req *connect.Request[agentv1.ListRoutineRunsRequest],
) (*connect.Response[agentv1.ListRoutineRunsResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.ownedRoutine(ctx, req.Msg.GetId(), login)
	if err != nil {
		return nil, err
	}
	page := req.Msg.GetPage()
	runs, next, err := s.store.ListRoutineRuns(ctx, r.ID, req.Msg.GetIncludeIdle(), page.GetCursor(), int(page.GetLimit()))
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.RoutineRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, routineRunToProto(run))
	}
	return connect.NewResponse(&agentv1.ListRoutineRunsResponse{Runs: out, NextCursor: next}), nil
}

// MaxRoutinesPerPerson bounds how many routines one person may have.
const MaxRoutinesPerPerson = 10

// ownedRoutine is a routine the caller may manage, or not found.
func (s *AgentService) ownedRoutine(ctx context.Context, id, login string) (store.Routine, error) {
	if strings.TrimSpace(id) == "" {
		return store.Routine{}, connect.NewError(connect.CodeInvalidArgument, errors.New("a routine id is required"))
	}
	r, err := s.store.GetRoutine(ctx, id)
	if err != nil {
		return store.Routine{}, routineError(err)
	}
	if !s.mayManageRoutine(ctx, r, login) {
		return store.Routine{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: routine %s", store.ErrNotFound, id))
	}
	return r, nil
}

func (s *AgentService) mayManageRoutine(ctx context.Context, r store.Routine, login string) bool {
	return SeesAll(ctx) || (r.Scope == store.RoutineScopePersonal && r.OwnerLogin != "" && r.OwnerLogin == login)
}

// routineDraft checks a draft from the wire and works out its next run. owner is whose
// personal assistant it may name; id is the routine's own id for its jitter, "" for a new one.
func (s *AgentService) routineDraft(
	ctx context.Context, in *agentv1.RoutineDraft, owner, id string,
) (store.RoutineDraft, string, error) {
	if in == nil {
		return store.RoutineDraft{}, "", connect.NewError(connect.CodeInvalidArgument, errors.New("a draft is required"))
	}
	d := store.RoutineDraft{
		Name: in.GetName(), Schedule: in.GetSchedule(), Timezone: in.GetTimezone(),
		Instruction: in.GetInstruction(), Notes: in.GetNotes(), PersonalityID: strings.TrimSpace(in.GetPersonalityId()),
		Model: in.GetModel(), Effort: in.GetEffort(),
	}
	if strings.TrimSpace(d.Timezone) == "" {
		d.Timezone = "UTC"
	}
	sched, err := routines.Parse(d.Schedule, d.Timezone)
	if err != nil {
		return store.RoutineDraft{}, "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	now := time.Now()
	if err := sched.CheckFloor(routines.MinPersonalInterval, now); err != nil {
		return store.RoutineDraft{}, "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	// A new routine's jitter is worked out from a placeholder id; the routine's own takes
	// over from its first run.
	d.NextRunAt = sched.NextFor(cmpOr(id, d.Name), now)
	if choice := (profiles.Override{Model: d.Model, Effort: d.Effort}); !choice.Empty() {
		if err := s.checkOverride(choice); err != nil {
			return store.RoutineDraft{}, "", err
		}
	}
	display := ""
	if d.PersonalityID != "" {
		voice, err := s.personalityForCaller(ctx, d.PersonalityID, owner)
		if err != nil {
			return store.RoutineDraft{}, "", personalityError(err)
		}
		display = voice.DisplayName
	}
	if _, err := store.PrepareRoutine(d); err != nil {
		return store.RoutineDraft{}, "", routineError(err)
	}
	return d, display, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func routineError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrInvalidRoutine), errors.Is(err, routines.ErrInvalidSchedule):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	return storeError(err)
}

func (s *AgentService) routineToProto(ctx context.Context, r store.Routine) *agentv1.Routine {
	out := &agentv1.Routine{
		Id:            r.ID,
		Name:          r.Name,
		Schedule:      r.Schedule,
		Timezone:      r.Timezone,
		ScheduleText:  routines.Describe(r.Schedule),
		Instruction:   r.Instruction,
		Notes:         r.Notes,
		ProposedNote:  r.ProposedNote,
		PersonalityId: r.PersonalityID,
		Model:         r.Model,
		Effort:        r.Effort,
		Enabled:       r.Enabled,
		PausedReason:  r.PausedReason,
		State:         string(r.State),
		ChatId:        r.ChatID,
		NextRunAt:     timestamppb.New(r.NextRunAt),
		IdleCount:     int32(r.IdleCount),
		OwnerLogin:    r.OwnerLogin,
		CreatedAt:     timestamppb.New(r.CreatedAt),
	}
	if r.LastRunAt != nil {
		out.LastRunAt = timestamppb.New(*r.LastRunAt)
	}
	if spend, err := s.store.RoutineSpendSince(ctx, r.ID, time.Now().Add(-24*time.Hour)); err == nil {
		out.SpendTodayUsd = spend
	}
	if running, err := s.store.ChatTurnRunning(ctx, r.ChatID); err == nil {
		out.Running = running
	}
	return out
}

func routineRunToProto(r store.RoutineRun) *agentv1.RoutineRun {
	out := &agentv1.RoutineRun{
		Id:           r.ID,
		ScheduledFor: timestamppb.New(r.ScheduledFor),
		Trigger:      r.Trigger,
		Status:       r.Status,
		Summary:      r.Summary,
		TurnId:       r.TurnID,
		CreatedAt:    timestamppb.New(r.CreatedAt),
	}
	if r.CostUSD != nil {
		out.CostUsd = *r.CostUSD
	}
	if r.FinishedAt != nil {
		out.FinishedAt = timestamppb.New(*r.FinishedAt)
	}
	return out
}
