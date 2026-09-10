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
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/config"
	"github.com/podium-ade/podium/internal/agent/memory"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
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
	// SkillsDir is PODIUM_AGENT_SKILLS_DIR: the Agent Skills on this host, which the skill
	// RPCs report and refuse to write over. Empty means the stored half is the only source,
	// which is a supported configuration — it is what an install with no shell access to the
	// conductor looks like.
	SkillsDir string
	// Profiles is the profile in force, swapped whenever a stored playbook changes. Nil makes
	// the playbook and profile RPCs answer FailedPrecondition.
	Profiles *profiles.Live
	// ProfileDir is PODIUM_AGENT_PROFILE_DIR, the directory Profiles' file half was read
	// from. It is what ReloadProfileDir re-reads. Empty means this conductor has no
	// directory to go back to and that RPC answers FailedPrecondition.
	ProfileDir string
	// Chat is the web chat's write path and live fan-out. Nil makes the chat RPCs answer
	// FailedPrecondition.
	Chat ChatSource
	// Tasks stops a running Podium task. DeleteChat uses it so a conversation that still
	// has a turn in flight does not leave a container running after it is gone. Nil is a
	// supported configuration: tests that never start a turn omit it.
	Tasks TaskCanceller
	// Turns stops the work a conversation owns that no task id can reach: the host turn
	// answering it, which runs in this process, and the tasks that turn delegated. Nil is
	// a conductor with no host turns, and DeleteChat then has nothing extra to stop.
	Turns  TurnStopper
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
	oauth      map[string]*oauthClient
	http       *http.Client
	memory     memory.Client
	skillsDir  string
	profiles   *profiles.Live
	profileDir string
	chat       ChatSource
	tasks      TaskCanceller
	turns      TurnStopper
	logger     *slog.Logger

	// flows are the subscription sign-ins this process has started and not finished, and
	// mcpFlows the MCP ones. Two maps rather than one because they are two different flows
	// — a device code and an authorization code — and nothing ever looks a sign-in up
	// without already knowing which kind it wanted.
	//
	// Both are process memory on purpose. A flow is a few hundred bytes with a PKCE
	// verifier in it, it is worthless thirty minutes later, and a restart mid-sign-in
	// should invalidate it rather than resume it.
	flowMu   sync.Mutex
	flows    map[string]*oauthFlow
	mcpFlows map[string]*mcpFlow

	// writeMu serialises the read-validate-write of a playbook, an override or an MCP
	// registration, so two browsers saving at once cannot each validate against a set the
	// other is changing. An MCP write holds it across the call that stores the token in the
	// control plane, which is what keeps "is this name taken" and "write this secret" one
	// decision rather than two.
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
		skillsDir:  opts.SkillsDir,
		profiles:   opts.Profiles,
		profileDir: opts.ProfileDir,
		chat:       opts.Chat,
		tasks:      opts.Tasks,
		turns:      opts.Turns,
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

// DefaultUsageDays is the range GetUsage reads when the request names neither end.
const DefaultUsageDays = 30

// GetUsage reports what the conductor spent over a range: one row per day for a calendar,
// and the per-task cost of each turn inside it.
func (s *AgentService) GetUsage(
	ctx context.Context, req *connect.Request[agentv1.GetUsageRequest],
) (*connect.Response[agentv1.GetUsageResponse], error) {
	to := time.Now().UTC()
	if ts := req.Msg.GetTo(); ts.IsValid() {
		to = ts.AsTime()
	}
	from := to.AddDate(0, 0, -DefaultUsageDays)
	if ts := req.Msg.GetFrom(); ts.IsValid() {
		from = ts.AsTime()
	}
	// An offset beyond the real ones on Earth would shift days by an arbitrary amount, so it
	// is refused rather than clamped: it can only be a bug in the caller.
	tz := int(req.Msg.GetTzOffsetMinutes())
	if tz < -12*60 || tz > 14*60 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("get usage: tz_offset_minutes %d is not a real time zone offset", tz))
	}

	var compareFrom time.Time
	if ts := req.Msg.GetCompareFrom(); ts.IsValid() {
		compareFrom = ts.AsTime()
	}

	u, err := s.store.Usage(ctx, store.UsageQuery{
		From:            from,
		To:              to,
		CompareFrom:     compareFrom,
		TZOffsetMinutes: tz,
		Limit:           int(req.Msg.GetLimit()),
	})
	if err != nil {
		return nil, storeError(err)
	}

	days := make([]*agentv1.UsageDay, 0, len(u.Days))
	for _, d := range u.Days {
		days = append(days, &agentv1.UsageDay{
			Date:       d.Date,
			CostUsd:    d.CostUSD,
			Turns:      int32(d.Turns),
			ModelTurns: int32(d.ModelTurns),
			Unpriced:   int32(d.Unpriced),
		})
	}
	costs := make([]*agentv1.TaskCost, 0, len(u.Costs))
	for _, c := range u.Costs {
		costs = append(costs, turnCostToProto(c))
	}
	backends := make([]*agentv1.UsageBackend, 0, len(u.Backends))
	for _, b := range u.Backends {
		backends = append(backends, &agentv1.UsageBackend{
			Provider:   b.Provider,
			Agent:      b.Agent,
			Model:      b.Model,
			Effort:     b.Effort,
			CostUsd:    b.CostUSD,
			Turns:      int32(b.Turns),
			ModelTurns: int32(b.ModelTurns),
			Unpriced:   int32(b.Unpriced),
		})
	}
	return connect.NewResponse(&agentv1.GetUsageResponse{
		Days:            days,
		Costs:           costs,
		TotalCostUsd:    u.TotalCostUSD,
		TotalTurns:      int32(u.TotalTurns),
		TotalModelTurns: int32(u.TotalModelTurns),
		Unpriced:        int32(u.Unpriced),
		Backends:        backends,
	}), nil
}

func turnCostToProto(c store.TurnCost) *agentv1.TaskCost {
	out := &agentv1.TaskCost{
		TaskId:     c.TaskID,
		TurnId:     c.TurnID,
		SessionId:  c.SessionID,
		SourceKind: c.SourceKind,
		SourceKey:  c.SourceKey,
		Playbook:   c.Playbook,
		Profile:    c.Profile,
		Status:     c.Status,
		StartedAt:  timestamppb.New(c.StartedAt),
		CostUsd:    c.CostUSD,
		Agent:      c.Backend.Agent,
		Model:      c.Backend.Model,
		Effort:     c.Backend.Effort,
		Provider:   c.Backend.Provider,
	}
	if c.FinishedAt != nil {
		out.FinishedAt = timestamppb.New(*c.FinishedAt)
	}
	if c.NumTurns != nil {
		n := int32(*c.NumTurns)
		out.NumTurns = &n
	}
	return out
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
