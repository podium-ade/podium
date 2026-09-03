// Package api is the conductor's own HTTP surface: the AgentService handlers, the bearer
// that guards them, and the health and metrics endpoints. There is no other way in — the
// listener is loopback by default and podium-server is what a browser reaches it through.
package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alvaroibarguen/podium/internal/agent/store"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// LoginHeader is what podium-server's proxy sets to the calling operator's login. Any
// client-supplied copy is stripped there, so what arrives here is the server's word.
const LoginHeader = "X-Podium-Login"

type loginKey struct{}

// RequireBearer refuses everything that does not present the conductor's token, and puts
// the proxied login in the request context. The comparison is constant time: the token is
// the only thing standing between a local process and every session the bot has had.
func RequireBearer(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if token == "" || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		login := strings.TrimSpace(r.Header.Get(LoginHeader))
		if login == "" {
			login = "unknown"
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), loginKey{}, login)))
	})
}

// Login is the operator the server says is calling, or "unknown" when a handler was
// reached without the middleware.
func Login(ctx context.Context) string {
	if v, ok := ctx.Value(loginKey{}).(string); ok && v != "" {
		return v
	}
	return "unknown"
}

// AgentService implements podium.agent.v1.AgentService. It is read-only in this step.
type AgentService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewAgentService returns the handlers.
func NewAgentService(st *store.Store, logger *slog.Logger) *AgentService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AgentService{store: st, logger: logger}
}

// ListSessions returns conversations newest first.
func (s *AgentService) ListSessions(
	ctx context.Context, req *connect.Request[agentv1.ListSessionsRequest],
) (*connect.Response[agentv1.ListSessionsResponse], error) {
	page := req.Msg.GetPage()
	rows, next, err := s.store.ListSessions(ctx, int(page.GetLimit()), page.GetCursor())
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, sessionToProto(r))
	}
	return connect.NewResponse(&agentv1.ListSessionsResponse{Sessions: out, NextCursor: next}), nil
}

// GetSession reads one session by id.
func (s *AgentService) GetSession(
	ctx context.Context, req *connect.Request[agentv1.GetSessionRequest],
) (*connect.Response[agentv1.GetSessionResponse], error) {
	id := req.Msg.GetSessionId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("get session: session_id is required"))
	}
	row, err := s.store.GetSession(ctx, id)
	if err != nil {
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.GetSessionResponse{Session: sessionToProto(row)}), nil
}

// ListTurns returns one session's turns, newest first.
func (s *AgentService) ListTurns(
	ctx context.Context, req *connect.Request[agentv1.ListTurnsRequest],
) (*connect.Response[agentv1.ListTurnsResponse], error) {
	id := req.Msg.GetSessionId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("list turns: session_id is required"))
	}
	rows, err := s.store.ListTurns(ctx, id, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.Turn, 0, len(rows))
	for _, r := range rows {
		out = append(out, turnToProto(r))
	}
	return connect.NewResponse(&agentv1.ListTurnsResponse{Turns: out}), nil
}

func sessionToProto(s store.Session) *agentv1.Session {
	out := &agentv1.Session{
		Id:         s.ID,
		SourceKind: s.SourceKind,
		SourceKey:  s.SourceKey,
		Profile:    s.Profile,
		Skill:      s.Skill,
		CreatedAt:  timestamppb.New(s.CreatedAt),
	}
	if s.LastTurnAt != nil {
		out.LastTurnAt = timestamppb.New(*s.LastTurnAt)
	}
	return out
}

func turnToProto(t store.Turn) *agentv1.Turn {
	out := &agentv1.Turn{
		Id:         t.ID,
		SessionId:  t.SessionID,
		TaskId:     t.TaskID,
		TriggerRef: t.TriggerRef,
		Status:     t.Status,
		StartedAt:  timestamppb.New(t.StartedAt),
		FinalText:  t.FinalText,
	}
	if t.FinishedAt != nil {
		out.FinishedAt = timestamppb.New(*t.FinishedAt)
	}
	if t.NumTurns != nil {
		n := int32(*t.NumTurns)
		out.NumTurns = &n
	}
	if t.CostUSD != nil {
		out.CostUsd = t.CostUSD
	}
	return out
}

// storeError maps the store's sentinels onto Connect codes.
func storeError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("agent store: %w", err))
}
