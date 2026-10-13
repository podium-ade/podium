package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// ListPersonalities returns the caller's voices. Podium is not one of them.
func (s *AgentService) ListPersonalities(
	ctx context.Context, _ *connect.Request[agentv1.ListPersonalitiesRequest],
) (*connect.Response[agentv1.ListPersonalitiesResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListPersonalities(ctx, login)
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.Personality, 0, len(rows))
	for _, row := range rows {
		out = append(out, personalityToProto(row))
	}
	return connect.NewResponse(&agentv1.ListPersonalitiesResponse{Personalities: out}), nil
}

// CreatePersonality stores a voice for the caller. The request name is ignored. The store
// generates one from the display name.
func (s *AgentService) CreatePersonality(
	ctx context.Context, req *connect.Request[agentv1.CreatePersonalityRequest],
) (*connect.Response[agentv1.CreatePersonalityResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	draft := draftFromRequest(req.Msg.GetName(), req.Msg.GetDisplayName(), req.Msg.GetInstructions(), req.Msg.GetAgent(), req.Msg.GetModel(), req.Msg.GetEffort())
	if err := s.checkPersonalityModel(draft); err != nil {
		return nil, err
	}
	row, err := s.store.CreatePersonality(ctx, login, draft)
	if err != nil {
		return nil, personalityError(err)
	}
	s.logger.InfoContext(ctx, "a personality was created", "personality_id", row.ID, "login", login)
	return connect.NewResponse(&agentv1.CreatePersonalityResponse{Personality: personalityToProto(row)}), nil
}

// UpdatePersonality replaces the display name, instructions, and model of one of the caller's
// voices. The request name is ignored, and the stored name stays. Another login's row is not found.
func (s *AgentService) UpdatePersonality(
	ctx context.Context, req *connect.Request[agentv1.UpdatePersonalityRequest],
) (*connect.Response[agentv1.UpdatePersonalityResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("update personality: an id is required"))
	}
	draft := draftFromRequest(req.Msg.GetName(), req.Msg.GetDisplayName(), req.Msg.GetInstructions(), req.Msg.GetAgent(), req.Msg.GetModel(), req.Msg.GetEffort())
	if err := s.checkPersonalityModel(draft); err != nil {
		return nil, err
	}
	row, err := s.store.UpdatePersonality(ctx, req.Msg.GetId(), login, draft)
	if err != nil {
		return nil, personalityError(err)
	}
	s.logger.InfoContext(ctx, "a personality was updated", "personality_id", row.ID, "login", login)
	return connect.NewResponse(&agentv1.UpdatePersonalityResponse{Personality: personalityToProto(row)}), nil
}

// DeletePersonality removes one of the caller's voices. Chats that used it stay.
func (s *AgentService) DeletePersonality(
	ctx context.Context, req *connect.Request[agentv1.DeletePersonalityRequest],
) (*connect.Response[agentv1.DeletePersonalityResponse], error) {
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("delete personality: an id is required"))
	}
	if err := s.store.DeletePersonality(ctx, req.Msg.GetId(), login); err != nil {
		return nil, personalityError(err)
	}
	s.logger.InfoContext(ctx, "a personality was deleted", "personality_id", req.Msg.GetId(), "login", login)
	return connect.NewResponse(&agentv1.DeletePersonalityResponse{}), nil
}

func draftFromRequest(name, display, instructions, agentID, model, effort string) store.PersonalityDraft {
	return store.PersonalityDraft{
		Name: name, DisplayName: display, Instructions: instructions,
		Agent: agentID, Model: model, Effort: effort,
	}
}

// checkPersonalityModel refuses a default the catalogue cannot run. An empty triple
// follows Podium and is not checked: there is nothing of this voice's own to refuse.
func (s *AgentService) checkPersonalityModel(draft store.PersonalityDraft) error {
	choice := profiles.Override{Agent: draft.Agent, Model: draft.Model, Effort: draft.Effort}
	if choice.Empty() {
		return nil
	}
	return s.checkOverride(choice)
}

func personalityError(err error) error {
	switch {
	case errors.Is(err, store.ErrInvalidPersonality):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, store.ErrConflict):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return storeError(err)
	}
}

func personalityToProto(row store.Personality) *agentv1.Personality {
	return &agentv1.Personality{
		Id:           row.ID,
		Name:         row.Name,
		DisplayName:  row.DisplayName,
		Instructions: row.Instructions,
		Agent:        row.Agent,
		Model:        row.Model,
		Effort:       row.Effort,
		UpdatedAt:    timestamppb.New(row.UpdatedAt),
	}
}

// personalityForCaller reads one voice and refuses a row this login does not own.
// The refusal is not found, the same answer as a missing row.
func (s *AgentService) personalityForCaller(ctx context.Context, id, login string) (store.Personality, error) {
	row, err := s.store.GetPersonality(ctx, id)
	if err != nil {
		return store.Personality{}, err
	}
	if row.Login != login {
		return store.Personality{}, fmt.Errorf("%w: personality %s", store.ErrNotFound, id)
	}
	return row, nil
}
