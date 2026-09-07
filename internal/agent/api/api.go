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
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alvaroibarguen/podium/internal/agent/config"
	"github.com/alvaroibarguen/podium/internal/agent/memory"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
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

// SecretStore is the part of the Podium API the settings handlers need. It is an interface
// rather than the client itself so a test needs no control plane, and so this package keeps
// depending on nothing but a name, some bytes and a version.
//
// SecretVersion is what keeps the conductor honest. The secret belongs to the control plane
// and an operator can set or remove it with `podium secret set`/`rm` without the conductor
// hearing about it, so the metadata row here is never taken as proof that a key exists —
// the control plane is asked. ListSecrets carries metadata only; no value is ever read back.
type SecretStore interface {
	SetSecret(ctx context.Context, name string, value []byte) (version int32, err error)
	DeleteSecret(ctx context.Context, name string) error
	SecretVersion(ctx context.Context, name string) (int32, error)
}

// AgentServiceOptions is what the handlers need. Everything but Store and Secrets is
// optional; without Secrets the settings RPCs answer FailedPrecondition rather than panic.
type AgentServiceOptions struct {
	Store   *store.Store
	Secrets SecretStore
	// Model is the model reported by GetSettings when no profile is loaded. The profile's
	// own model wins when there is one, because it is what a turn will actually run.
	Model string
	// AnthropicBaseURL is where SetProviderKey validates an Anthropic key.
	AnthropicBaseURL string
	// XAIBaseURL is where it validates an xAI credential, key or token.
	XAIBaseURL string
	// XAIOAuthIssuer, XAIOAuthClientID and XAIOAuthScopes configure the subscription
	// sign-in. An empty client id means this control plane offers API keys only, which is
	// a supported configuration and not an error.
	XAIOAuthIssuer   string
	XAIOAuthClientID string
	XAIOAuthScopes   string
	// HTTPClient validates the key. Nil means a client with a timeout of its own.
	HTTPClient *http.Client
	// Memory is the shared-memory client. Nil is a supported configuration: the three
	// memory RPCs then answer FailedPrecondition and the UI says memory is not configured.
	Memory memory.Client
	// Profiles is the profile in force, swapped whenever a stored playbook changes. Nil makes
	// the playbook and profile RPCs answer FailedPrecondition.
	Profiles *profiles.Live
	// Chat is the web chat's write path and live fan-out. Nil makes the chat RPCs answer
	// FailedPrecondition.
	Chat   ChatSource
	Logger *slog.Logger
}

// AgentService implements podium.agent.v1.AgentService.
type AgentService struct {
	store      *store.Store
	secrets    SecretStore
	model      string
	baseURL    string
	xaiBaseURL string
	// oauth is one client per provider that has one configured, keyed by provider name. A
	// provider with no entry offers API keys only.
	oauth    map[string]*oauthClient
	http     *http.Client
	memory   memory.Client
	profiles *profiles.Live
	chat     ChatSource
	logger   *slog.Logger

	// flows are the subscription sign-ins this process has started and not finished.
	flowMu sync.Mutex
	flows  map[string]*oauthFlow

	// writeMu serialises the read-validate-write of a playbook or an override, so two
	// browsers saving at once cannot each validate against a set the other is changing.
	writeMu sync.Mutex
	// stale is why the last rebuild of the profile failed, or "". GetProfile reports it:
	// a conductor running a profile older than its database has to say so.
	stale atomic.Pointer[string]
}

// NewAgentService returns the handlers.
func NewAgentService(opts AgentServiceOptions) *AgentService {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: validateTimeout}
	}
	if opts.AnthropicBaseURL == "" {
		opts.AnthropicBaseURL = config.DefaultAnthropicBaseURL
	}
	if opts.XAIBaseURL == "" {
		opts.XAIBaseURL = config.DefaultXAIBaseURL
	}
	if opts.XAIOAuthIssuer == "" {
		opts.XAIOAuthIssuer = config.DefaultXAIOAuthIssuer
	}
	if opts.XAIOAuthScopes == "" {
		opts.XAIOAuthScopes = config.DefaultXAIOAuthScopes
	}
	oauth := map[string]*oauthClient{}
	// newOAuthClient returns nil without a client id, and a nil entry is never stored: the
	// map having no key for a provider is what "API keys only" means to oauthFor.
	if c := newOAuthClient(opts.XAIOAuthIssuer, opts.XAIOAuthClientID, opts.XAIOAuthScopes,
		opts.HTTPClient); c != nil {
		oauth[ProviderXAI] = c
	}
	return &AgentService{
		store:      opts.Store,
		secrets:    opts.Secrets,
		model:      opts.Model,
		baseURL:    opts.AnthropicBaseURL,
		xaiBaseURL: opts.XAIBaseURL,
		oauth:      oauth,
		http:       opts.HTTPClient,
		memory:     opts.Memory,
		profiles:   opts.Profiles,
		chat:       opts.Chat,
		logger:     opts.Logger,
	}
}

// currentModel is the model a turn would run on: the profile's, which an operator can
// change on the Profile screen, and only then the one this process was configured with.
func (s *AgentService) currentModel() string {
	if p := s.profiles.Current(); p != nil && p.Model != "" {
		return p.Model
	}
	return s.model
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
		Playbook:   s.Playbook,
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
