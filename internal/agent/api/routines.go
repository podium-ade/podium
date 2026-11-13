package api

// Routines (docs/agent.md, "Routines"). A personal routine is its owner's alone: every handler
// here answers only for routines the caller owns, and the dev token for all of them. Another
// person's routine is not found, the same answer every other personal read gives.
//
// A system routine runs as nobody. Admins and owners (Infra) create, edit and run it with the
// same handlers; a member never sees one. An admin also sees every person's routines as
// metadata (ListMemberRoutines) and may pause one, never read it.
//
// A routine that runs on Linear events is for admins and owners too, in either scope: the
// events carry issues from every team the Linear app can see, so a member's routine would read
// teams the member is not in. Only an admin may set a draft's trigger to Linear.

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
	var rows []store.Routine
	if SeesAll(ctx) {
		rows, err = s.store.ListRoutines(ctx, "")
	} else {
		rows, err = s.store.ListVisibleRoutines(ctx, login, Infra(ctx))
	}
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
	var r store.Routine
	switch strings.TrimSpace(req.Msg.GetScope()) {
	case "", store.RoutineScopePersonal:
		r, err = s.createRoutine(ctx, login, req.Msg.GetDraft())
	case store.RoutineScopeSystem:
		r, err = s.createSystemRoutine(ctx, login, req.Msg.GetDraft())
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("scope %q is not personal or system", req.Msg.GetScope()))
	}
	if err != nil {
		return nil, err
	}
	s.auditRoutine(ctx, login, AuditRoutineCreate, r, triggerDetails(r, map[string]any{"scope": r.Scope}))
	return connect.NewResponse(&agentv1.CreateRoutineResponse{Routine: s.routineToProto(ctx, r)}), nil
}

// createSystemRoutine opens a routine that runs as nobody, for an admin or owner.
func (s *AgentService) createSystemRoutine(ctx context.Context, login string, in *agentv1.RoutineDraft) (store.Routine, error) {
	if !Infra(ctx) {
		return store.Routine{}, connect.NewError(connect.CodePermissionDenied,
			errors.New("only admins and owners can set up a system routine"))
	}
	if err := s.chatEnabled(); err != nil {
		return store.Routine{}, err
	}
	draft, _, err := s.routineDraft(ctx, in, "", "", store.RoutineScopeSystem)
	if err != nil {
		return store.Routine{}, err
	}
	r, err := s.store.CreateSystemRoutine(ctx, login, draft)
	if err != nil {
		return store.Routine{}, routineError(err)
	}
	s.logger.InfoContext(ctx, "a system routine was created", "routine_id", r.ID, "login", login, "schedule", r.When())
	return r, nil
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
	limits, err := s.store.GetRoutineLimits(ctx)
	if err != nil {
		return store.Routine{}, storeError(err)
	}
	have, err := s.store.CountRoutines(ctx, login)
	if err != nil {
		return store.Routine{}, storeError(err)
	}
	if have >= limits.MaxPerPerson {
		return store.Routine{}, connect.NewError(connect.CodeResourceExhausted,
			fmt.Errorf("you have %d routines, which is as many as one person may; delete one first", have))
	}
	draft, display, err := s.routineDraft(ctx, in, login, "", store.RoutineScopePersonal)
	if err != nil {
		return store.Routine{}, err
	}
	r, err := s.store.CreateRoutine(ctx, login, draft, display)
	if err != nil {
		return store.Routine{}, routineError(err)
	}
	s.logger.InfoContext(ctx, "a routine was created", "routine_id", r.ID, "login", login, "schedule", r.When())
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
	draft, _, err := s.routineDraft(ctx, req.Msg.GetDraft(), r.OwnerLogin, r.ID, r.Scope)
	if err != nil {
		return nil, err
	}
	if draft.PersonalityID != r.PersonalityID {
		// The chat was opened with one voice and stays labelled with it; a routine changing
		// voices would leave the chat saying the wrong name.
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a routine's assistant is chosen when it is created and cannot be changed"))
	}
	updated, err := s.store.UpdateRoutine(ctx, r.ID, login, draft)
	if err != nil {
		return nil, routineError(err)
	}
	s.auditRoutine(ctx, login, AuditRoutineUpdate, updated, triggerDetails(updated, map[string]any{
		"instruction_changed": r.Instruction != updated.Instruction,
		"notes_changed":       r.Notes != updated.Notes,
	}))
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
	s.auditRoutine(ctx, login, AuditRoutineDelete, r, nil)
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
	reason := ""
	if err != nil {
		// An admin may pause anybody's routine, never read it: the routine is found here only
		// to be paused, and what goes back is its metadata.
		other, oerr := s.store.GetRoutine(ctx, req.Msg.GetId())
		if !Infra(ctx) || oerr != nil {
			return nil, err
		}
		r, reason = other, "Paused by an admin."
	}
	r, err = s.store.SetRoutineEnabled(ctx, r.ID, false, reason, r.NextRunAt)
	if err != nil {
		return nil, routineError(err)
	}
	s.auditRoutine(ctx, login, AuditRoutinePause, r, nil)
	if reason != "" && r.OwnerLogin != "" && r.OwnerLogin != login {
		if err := s.store.NotifyRoutine(ctx, r.OwnerLogin, r.ChatID, login, reason); err != nil {
			s.logger.WarnContext(ctx, "telling a routine's owner it was paused failed", "routine_id", r.ID, "error", err)
		}
		return connect.NewResponse(&agentv1.PauseRoutineResponse{Routine: memberView(s.routineToProto(ctx, r))}), nil
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
	// A routine on Linear events has no schedule; the events that waited while it was paused
	// run on the next tick.
	next := time.Now()
	if !r.OnLinear() {
		sched, err := routines.Parse(r.Schedule, r.Timezone)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		next = sched.NextFor(r.ID, next)
	}
	r, err = s.store.SetRoutineEnabled(ctx, r.ID, true, "", next)
	if err != nil {
		return nil, routineError(err)
	}
	s.auditRoutine(ctx, login, AuditRoutineResume, r, nil)
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
	s.auditRoutine(ctx, login, AuditRoutineRun, r, map[string]any{"run_id": run.ID})
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
	r, err = s.store.SetRoutineNotes(ctx, r.ID, login, notes, "")
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

// ListRoutineRevisions is the history of what one of the caller's routines was told to do.
func (s *AgentService) ListRoutineRevisions(
	ctx context.Context, req *connect.Request[agentv1.ListRoutineRevisionsRequest],
) (*connect.Response[agentv1.ListRoutineRevisionsResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.ownedRoutine(ctx, req.Msg.GetId(), login)
	if err != nil {
		return nil, err
	}
	revs, err := s.store.ListRoutineRevisions(ctx, r.ID, 50)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.RoutineRevision, 0, len(revs))
	for _, v := range revs {
		out = append(out, &agentv1.RoutineRevision{
			Id: v.ID, Instruction: v.Instruction, Notes: v.Notes, Schedule: v.Schedule, Timezone: v.Timezone,
			ChangedBy: v.ChangedBy, ChangedAt: timestamppb.New(v.ChangedAt),
		})
	}
	return connect.NewResponse(&agentv1.ListRoutineRevisionsResponse{Revisions: out}), nil
}

// CancelRoutineRun stops the run going now in one of the caller's routines. It is how an admin
// stops a system routine's run, whose chat nobody may write in.
func (s *AgentService) CancelRoutineRun(
	ctx context.Context, req *connect.Request[agentv1.CancelRoutineRunRequest],
) (*connect.Response[agentv1.CancelRoutineRunResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.ownedRoutine(ctx, req.Msg.GetId(), login)
	if err != nil {
		return nil, err
	}
	if s.canceller == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("cancel routine run: no conductor is wired in"))
	}
	stopped, err := s.canceller.CancelChatTurn(ctx, r.ChatID)
	if err != nil {
		return nil, storeError(err)
	}
	if !stopped {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the routine is not running"))
	}
	s.auditRoutine(ctx, login, AuditRoutineCancel, r, nil)
	return connect.NewResponse(&agentv1.CancelRoutineRunResponse{Routine: s.routineToProto(ctx, r)}), nil
}

// ListMemberRoutines is every person's routines as an admin sees them: metadata only.
func (s *AgentService) ListMemberRoutines(
	ctx context.Context, _ *connect.Request[agentv1.ListMemberRoutinesRequest],
) (*connect.Response[agentv1.ListMemberRoutinesResponse], error) {
	if _, err := requireLogin(ctx); err != nil {
		return nil, err
	}
	if !Infra(ctx) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only admins and owners see other people's routines"))
	}
	rows, err := s.store.ListPersonalRoutines(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	last := s.lastRuns(ctx, rows)
	since := time.Now().Add(-24 * time.Hour)
	out := make([]*agentv1.MemberRoutine, 0, len(rows))
	for _, r := range rows {
		m := &agentv1.MemberRoutine{
			Id: r.ID, Name: r.Name, OwnerLogin: r.OwnerLogin, ScheduleText: scheduleText(r),
			Timezone: r.Timezone, Enabled: r.Enabled, PausedReason: r.PausedReason,
		}
		if !r.OnLinear() {
			m.NextRunAt = timestamppb.New(r.NextRunAt)
		}
		if spend, err := s.store.RoutineSpendSince(ctx, r.ID, since); err == nil {
			m.SpendTodayUsd = spend
		}
		if l, ok := last[r.ID]; ok {
			m.LastRunStatus, m.LastRunAt = l.Status, timestamppb.New(l.At)
		}
		out = append(out, m)
	}
	return connect.NewResponse(&agentv1.ListMemberRoutinesResponse{Routines: out}), nil
}

// GetRoutineLimits is the per-person limits, for anyone: a person may want to know theirs.
func (s *AgentService) GetRoutineLimits(
	ctx context.Context, _ *connect.Request[agentv1.GetRoutineLimitsRequest],
) (*connect.Response[agentv1.GetRoutineLimitsResponse], error) {
	if _, err := requireLogin(ctx); err != nil {
		return nil, err
	}
	l, err := s.store.GetRoutineLimits(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.GetRoutineLimitsResponse{Limits: limitsToProto(l)}), nil
}

// SetRoutineLimits sets the per-person limits. Admins and owners only.
func (s *AgentService) SetRoutineLimits(
	ctx context.Context, req *connect.Request[agentv1.SetRoutineLimitsRequest],
) (*connect.Response[agentv1.SetRoutineLimitsResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if !Infra(ctx) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only admins and owners set routine limits"))
	}
	in := req.Msg.GetLimits()
	l, err := s.store.PutRoutineLimits(ctx, store.RoutineLimits{
		MaxPerPerson: int(in.GetMaxPerPerson()), PersonDailyCapUSD: in.GetPersonDailyCapUsd(),
	})
	if err != nil {
		return nil, routineError(err)
	}
	s.auditSubject(ctx, login, AuditRoutineLimits, "routines", map[string]any{
		"max_per_person": l.MaxPerPerson, "person_daily_cap_usd": l.PersonDailyCapUSD,
	})
	return connect.NewResponse(&agentv1.SetRoutineLimitsResponse{Limits: limitsToProto(l)}), nil
}

func limitsToProto(l store.RoutineLimits) *agentv1.RoutineLimits {
	return &agentv1.RoutineLimits{MaxPerPerson: int32(l.MaxPerPerson), PersonDailyCapUsd: l.PersonDailyCapUSD}
}

// memberView is what an admin who paused somebody else's routine gets back: no instruction,
// notes, state or chat.
func memberView(r *agentv1.Routine) *agentv1.Routine {
	return &agentv1.Routine{
		Id: r.Id, Name: r.Name, Schedule: r.Schedule, Timezone: r.Timezone, ScheduleText: r.ScheduleText,
		Enabled: r.Enabled, PausedReason: r.PausedReason, NextRunAt: r.NextRunAt, LastRunAt: r.LastRunAt,
		OwnerLogin: r.OwnerLogin, SpendTodayUsd: r.SpendTodayUsd, Scope: r.Scope, Trigger: r.Trigger,
	}
}

func (s *AgentService) lastRuns(ctx context.Context, rows []store.Routine) map[string]store.RoutineLastRun {
	idsOf := make([]string, 0, len(rows))
	for _, r := range rows {
		idsOf = append(idsOf, r.ID)
	}
	last, err := s.store.LastRoutineRuns(ctx, idsOf)
	if err != nil {
		s.logger.WarnContext(ctx, "reading routines' last runs failed", "error", err)
		return map[string]store.RoutineLastRun{}
	}
	return last
}

// Audit actions on routines. The control plane takes these from the conductor (audit.go).
const (
	AuditRoutineCreate = "routine.create"
	AuditRoutineUpdate = "routine.update"
	AuditRoutineDelete = "routine.delete"
	AuditRoutinePause  = "routine.pause"
	AuditRoutineResume = "routine.resume"
	AuditRoutineRun    = "routine.run"
	AuditRoutineCancel = "routine.cancel"
	AuditRoutineLimits = "routine.limits"
)

// auditRoutine records one change a person made to a routine. details never carry the
// instruction or notes themselves: those are in the routine's own history.
func (s *AgentService) auditRoutine(ctx context.Context, actor, action string, r store.Routine, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	details["name"], details["scope"] = r.Name, r.Scope
	if r.OwnerLogin != "" {
		details["owner"] = r.OwnerLogin
	}
	s.auditSubject(ctx, actor, action, r.ID, details)
}

func (s *AgentService) auditSubject(ctx context.Context, actor, action, subject string, details map[string]any) {
	if SeesAll(ctx) {
		actor = "dev-token"
	}
	s.auditChat(ctx, actor, action, subject, details)
}

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
	switch {
	case SeesAll(ctx):
		return true
	case r.System():
		return Infra(ctx)
	default:
		return r.OwnerLogin != "" && r.OwnerLogin == login
	}
}

// routineDraft checks a draft from the wire and works out its next run. owner is whose
// personal assistant it may name; id is the routine's own id for its jitter, "" for a new one;
// scope is the routine's, which sets its schedule floor and what it may name.
func (s *AgentService) routineDraft(
	ctx context.Context, in *agentv1.RoutineDraft, owner, id, scope string,
) (store.RoutineDraft, string, error) {
	if in == nil {
		return store.RoutineDraft{}, "", connect.NewError(connect.CodeInvalidArgument, errors.New("a draft is required"))
	}
	d := store.RoutineDraft{
		Name: in.GetName(), Schedule: in.GetSchedule(), Timezone: in.GetTimezone(),
		Instruction: in.GetInstruction(), Notes: in.GetNotes(), PersonalityID: strings.TrimSpace(in.GetPersonalityId()),
		Model: in.GetModel(), Effort: in.GetEffort(),
		Kind: strings.TrimSpace(in.GetTrigger()), Events: in.GetEvents(), Teams: in.GetTeams(),
	}
	if c := in.GetDailyCapUsd(); c != 0 {
		d.DailyCapUSD = &c
	}
	floor := routines.MinPersonalInterval
	if scope == store.RoutineScopeSystem {
		floor = routines.MinSystemInterval
		if d.PersonalityID != "" {
			return store.RoutineDraft{}, "", connect.NewError(connect.CodeInvalidArgument,
				errors.New("a system routine runs as Podium; it cannot use a personal assistant"))
		}
	}
	if strings.TrimSpace(d.Timezone) == "" {
		d.Timezone = "UTC"
	}
	if d.Kind == store.RoutineKindLinear {
		// Linear events carry issues from every team the app can see, so a routine on them is
		// an admin's to set, whoever it runs as. It has no schedule to parse or floor to hold:
		// how often it runs is what Linear sends, bounded by the scheduler's guards.
		if !Infra(ctx) {
			return store.RoutineDraft{}, "", connect.NewError(connect.CodePermissionDenied,
				errors.New("only admins and owners can set a routine to run on Linear events: they carry issues "+
					"from every team the Linear app can see"))
		}
		if _, err := time.LoadLocation(strings.TrimSpace(d.Timezone)); err != nil {
			return store.RoutineDraft{}, "", connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("%w: %q is not a timezone (use an IANA name such as America/Toronto)", routines.ErrInvalidSchedule, d.Timezone))
		}
		d.NextRunAt = time.Now()
	} else {
		sched, err := routines.Parse(d.Schedule, d.Timezone)
		if err != nil {
			return store.RoutineDraft{}, "", connect.NewError(connect.CodeInvalidArgument, err)
		}
		now := time.Now()
		if err := sched.CheckFloor(floor, now); err != nil {
			return store.RoutineDraft{}, "", connect.NewError(connect.CodeInvalidArgument, err)
		}
		// A new routine's jitter is worked out from a placeholder id; the routine's own takes
		// over from its first run.
		d.NextRunAt = sched.NextFor(cmpOr(id, d.Name), now)
	}
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

// scheduleText is what starts a routine's runs, in words.
func scheduleText(r store.Routine) string {
	if r.OnLinear() {
		return store.DescribeLinearTrigger(r.Events, r.Teams)
	}
	return routines.Describe(r.Schedule)
}

// triggerDetails adds what starts a routine's runs to an audit entry's details.
func triggerDetails(r store.Routine, details map[string]any) map[string]any {
	if r.OnLinear() {
		details["trigger"], details["events"], details["teams"] = r.Kind, r.Events, r.Teams
		return details
	}
	details["schedule"] = r.Schedule
	return details
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
		ScheduleText:  scheduleText(r),
		Trigger:       r.Kind,
		Events:        r.Events,
		Teams:         r.Teams,
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
		IdleCount:     int32(r.IdleCount),
		OwnerLogin:    r.OwnerLogin,
		CreatedAt:     timestamppb.New(r.CreatedAt),
		Scope:         r.Scope,
		Repos:         r.Repos,
		CreatedBy:     r.CreatedBy,
	}
	if r.OnLinear() {
		// Its runs come when Linear's events do; there is no next one to name.
		if pending, err := s.store.PendingRoutineEvents(ctx, []string{r.ID}); err == nil {
			out.PendingEvents = int32(pending[r.ID])
		}
	} else {
		out.NextRunAt = timestamppb.New(r.NextRunAt)
	}
	if r.DailyCapUSD != nil {
		out.DailyCapUsd = *r.DailyCapUSD
	}
	if r.PausedUntil != nil {
		out.PausedUntil = timestamppb.New(*r.PausedUntil)
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
	if last, err := s.store.LastRoutineRuns(ctx, []string{r.ID}); err == nil {
		out.LastRunStatus = last[r.ID].Status
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
