package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// The providers this control plane knows. A provider is a set of credentials and an
// endpoint; profiles.Backends is what says which agent backend spends which of them.
const (
	ProviderAnthropic = profiles.ProviderAnthropic
	ProviderXAI       = profiles.ProviderXAI
)

// How a stored credential was obtained. The distinction is not cosmetic: an API key does
// not expire and an access token does, so only one of the two is refreshed and only one of
// the two has a hint worth showing.
const (
	AuthAPIKey = "api_key"
	AuthOAuth  = "oauth"
)

// validateTimeout bounds the call to the provider. An operator is waiting on a button.
const validateTimeout = 10 * time.Second

// keyHintLen is how much of a key is kept. It is computed once, at save time, and is the
// only form in which any part of the key is ever read back.
const keyHintLen = 4

// refreshInterval is how often the background pass looks for a token about to expire, and
// refreshLead is how far ahead of expiry it acts. The lead is generous on purpose: a turn
// runs for up to half an hour with the token it was handed at admission, so a token that is
// merely "not expired yet" when a turn starts is not good enough.
const (
	refreshInterval = 5 * time.Minute
	refreshLead     = 45 * time.Minute
)

// flowTTL bounds how long a started sign-in is remembered, whatever the provider said. A
// flow nobody finished is a few hundred bytes; a map of them that is never swept is a leak.
const flowTTL = 30 * time.Minute

// providerSpec is one provider's differences from the others. Everything else about a
// credential — validate, then store, then a metadata row — is the same code below.
type providerSpec struct {
	// name is the wire value: "anthropic", "xai".
	name string
	// label is the provider's own name, as it appears in a sentence shown to an operator.
	label string
	// secret is the reserved Podium secret the credential is stored as.
	secret string
	// validate asks the provider whether a credential works and what models it can see.
	validate func(ctx context.Context, s *AgentService, key []byte) ([]string, error)
	// unusual is the note a save carries when a key does not look like one.
	unusual func(key []byte) string
	// oauth is true when this provider can be signed in to instead of keyed.
	oauth bool
}

// providerSpecs is the registry, in the order GetSettings reports them.
var providerSpecs = []providerSpec{{
	name:   ProviderAnthropic,
	label:  "Anthropic",
	secret: profiles.AnthropicKeySecret,
	validate: func(ctx context.Context, s *AgentService, key []byte) ([]string, error) {
		return validateAnthropicKey(ctx, s.http, s.baseURL, key)
	},
	unusual: unusualFormat,
}, {
	name:   ProviderXAI,
	label:  "xAI",
	secret: profiles.XAIKeySecret,
	validate: func(ctx context.Context, s *AgentService, key []byte) ([]string, error) {
		return validateXAIKey(ctx, s.http, s.xaiBaseURL, key)
	},
	unusual: unusualXAIFormat,
	oauth:   true,
}}

// findProvider resolves a request's provider field. An empty one is Anthropic, so a client
// written before there was more than one provider still works.
func findProvider(name string) (providerSpec, error) {
	if name == "" {
		name = ProviderAnthropic
	}
	for _, p := range providerSpecs {
		if p.name == name {
			return p, nil
		}
	}
	known := make([]string, 0, len(providerSpecs))
	for _, p := range providerSpecs {
		known = append(known, p.name)
	}
	return providerSpec{}, connect.NewError(connect.CodeInvalidArgument,
		fmt.Errorf("unsupported provider %q: this control plane knows %s",
			name, strings.Join(known, ", ")))
}

// providerSettingKey is the settings row one provider's metadata lives in.
func providerSettingKey(provider string) string { return "provider." + provider }

// providerRow is the jsonb of the settings row.
//
// It holds no part of an API key beyond the hint. It DOES hold the refresh token of an
// OAuth sign-in, and that is the one credential this conductor's own database contains —
// see docs/security.md. It is here rather than in Podium's encrypted secret store for a
// blunt reason: the secret store has no read endpoint, by design, so a value put there
// cannot be read back to refresh with. Treat podium_agent's database as holding a
// credential, because it does.
type providerRow struct {
	KeyHint       string    `json:"key_hint"`
	SetBy         string    `json:"set_by"`
	SetAt         time.Time `json:"set_at"`
	SecretVersion int32     `json:"secret_version"`
	// AuthKind is AuthAPIKey or AuthOAuth. An empty value is a row written before there
	// was more than one, which was always an API key.
	AuthKind string `json:"auth_kind,omitempty"`
	// Account is who the provider says signed in. Shown, never authorised on.
	Account string `json:"account,omitempty"`
	// ExpiresAt is when the stored access token stops working. Zero for an API key.
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	// RefreshToken is SENSITIVE. It never leaves this process except to the provider's own
	// token endpoint, it is never put in a brief, in a task, or in a log, and it is never
	// copied into the proto.
	RefreshToken string `json:"refresh_token,omitempty"`
}

// authKind is the row's kind, defaulting an old row to what it must have been.
func (r providerRow) authKind() string {
	if r.AuthKind == "" {
		return AuthAPIKey
	}
	return r.AuthKind
}

// oauthFlow is one sign-in the conductor started and is waiting on. The device code stays
// here and is never handed to a browser: a flow id is a name for a sign-in, not a bearer of
// one, so a browser that is shown a start response still cannot complete it by itself.
type oauthFlow struct {
	provider   string
	deviceCode string
	interval   time.Duration
	expiresAt  time.Time
	startedBy  string
}

// GetSettings reports what the conductor is configured with.
//
// key_set means "an agent turn will find a credential", so it comes from the **control
// plane**, not from the metadata row: the secret is the control plane's and `podium secret
// rm` can take it away without the conductor hearing. The row only supplies the hint, the
// login and the time, and only when it still describes the version that is actually stored.
// Nothing here reads a value — there is no endpoint that could.
func (s *AgentService) GetSettings(
	ctx context.Context, _ *connect.Request[agentv1.GetSettingsRequest],
) (*connect.Response[agentv1.GetSettingsResponse], error) {
	out := &agentv1.GetSettingsResponse{}
	for _, p := range providerSpecs {
		ps, err := s.providerState(ctx, p)
		if err != nil {
			return nil, err
		}
		out.Providers = append(out.Providers, ps)
		if p.name == ProviderAnthropic {
			// The field the clients that only ever knew about one provider read.
			out.Provider = ps
		}
	}
	return connect.NewResponse(out), nil
}

// providerState is one provider's row as the UI sees it.
func (s *AgentService) providerState(ctx context.Context, p providerSpec) (*agentv1.ProviderSettings, error) {
	row, hasRow, err := s.providerRow(ctx, p)
	if err != nil {
		return nil, err
	}
	out := &agentv1.ProviderSettings{Provider: p.name, Model: s.currentModel()}
	version, verr := s.secretVersion(ctx, p.secret)
	switch {
	case verr != nil:
		// The control plane could not be asked. Reporting the last thing we knew beats a
		// broken page, and the log says the answer may be stale.
		s.logger.WarnContext(ctx, "could not confirm a provider credential with the control "+
			"plane; reporting the last known state", "provider", p.name, "error", verr)
		out.KeySet = hasRow
	case version == 0:
		// No secret. A row here is stale — somebody removed the credential with the CLI —
		// so it is deliberately not shown: saying "connected" would be a lie every turn
		// disproves.
		out.KeySet = false
	default:
		out.KeySet = true
	}

	// The hint describes one version of one credential. Anything else and it is withheld
	// rather than shown next to a credential it is not about.
	if out.KeySet && hasRow && (version == row.SecretVersion || verr != nil) {
		out.KeyHint = row.KeyHint
		out.SetBy = row.SetBy
		out.AuthKind = row.authKind()
		out.Account = row.Account
		out.Refreshable = row.RefreshToken != ""
		if !row.SetAt.IsZero() {
			out.SetAt = timestamppb.New(row.SetAt)
		}
		if !row.ExpiresAt.IsZero() {
			out.ExpiresAt = timestamppb.New(row.ExpiresAt)
		}
	}
	return out, nil
}

// secretVersion is the stored version of a reserved secret, or 0 when there is none. A
// control plane with no master key cannot hold a secret at all, which is 0 rather than a
// failure: the operator finds out with the real message when they try to save.
func (s *AgentService) secretVersion(ctx context.Context, name string) (int32, error) {
	v, err := s.secrets.SecretVersion(ctx, name)
	if connect.CodeOf(err) == connect.CodeFailedPrecondition {
		return 0, nil
	}
	return v, err
}

// SetProviderKey validates a key with the provider and, only then, stores it.
//
// The order is the contract: validate, write the secret, write the metadata. An operator who
// reads "saved" has to be able to trust that agents will run, so an unvalidated key is never
// written, and a metadata row never claims a key the secret store does not hold.
func (s *AgentService) SetProviderKey(
	ctx context.Context, req *connect.Request[agentv1.SetProviderKeyRequest],
) (*connect.Response[agentv1.SetProviderKeyResponse], error) {
	p, err := findProvider(req.Msg.GetProvider())
	if err != nil {
		return nil, err
	}
	if s.secrets == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no Podium API client, so it cannot store a key"))
	}

	// One []byte, zeroed on the way out. The proto's string still exists in this process's
	// memory until the request is collected — that is what a proto string costs — but
	// nothing downstream of here ever holds a second copy.
	key := []byte(trimPastedKey(req.Msg.GetKey()))
	defer zero(key)
	if len(key) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("the key is empty"))
	}

	login := Login(ctx)
	// The request is logged through a redacting wrapper. Logging req.Msg directly would
	// print every field, the key included: protobuf's String() does not know what a secret
	// is. There is no log line anywhere on this path that carries the key.
	s.logger.InfoContext(ctx, "setting a provider key", "request", redactedKeyRequest{req.Msg},
		"login", login)

	validateCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	models, err := p.validate(validateCtx, s, key)
	// What the provider said, scrubbed once and used twice: the same bounded, key-free
	// sentence goes to the log and to the operator, so a key a provider echoed back cannot
	// reach either. "" when there was nothing to say, which includes success.
	said := operatorDetail(validationDetail(err), key)
	switch {
	case errors.Is(err, errKeyRefused):
		s.logger.WarnContext(ctx, "the provider refused a key",
			"provider", p.name, "login", login, "provider_message", said)
		return nil, providerKeyError(connect.CodePermissionDenied,
			//nolint:staticcheck // the provider's name is a proper noun and this string is
			// the sentence the web UI renders verbatim
			fmt.Errorf("%s rejected this key", p.label), said)
	case err != nil:
		s.logger.WarnContext(ctx, "a provider key could not be validated",
			"provider", p.name, "login", login, "provider_message", said)
		return nil, providerKeyError(connect.CodeUnavailable,
			fmt.Errorf("could not validate the key with %s; nothing was saved", p.label), said)
	}

	row, err := s.storeCredential(ctx, p, key, providerRow{
		KeyHint:  keyHint(key),
		SetBy:    login,
		SetAt:    time.Now().UTC(),
		AuthKind: AuthAPIKey,
	})
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "a provider key was validated and stored",
		"provider", p.name, "login", login, "key_hint", row.KeyHint,
		"secret_version", row.SecretVersion, "models", len(models))

	return connect.NewResponse(&agentv1.SetProviderKeyResponse{
		Provider: providerSettings(p.name, row, s.currentModel()),
		Models:   models,
		Status:   p.unusual(key),
	}), nil
}

// storeCredential writes the credential and then its metadata row, and is where the two
// auth kinds stop differing: after this both are one bearer token in one Podium secret.
//
// Switching kinds cleans up after the other one. A key that replaces a sign-in must not
// leave a refresh token behind that the background pass would keep using — it would
// overwrite the key the operator just pasted.
func (s *AgentService) storeCredential(
	ctx context.Context, p providerSpec, key []byte, row providerRow,
) (providerRow, error) {
	version, err := s.secrets.SetSecret(ctx, p.secret, key)
	if err != nil {
		// Verbatim: the control plane's own words are what an operator needs here — a
		// missing master key reads very differently from a network failure.
		return providerRow{}, connect.NewError(connect.CodeOf(err),
			fmt.Errorf("the credential is valid but storing it failed: %w", err))
	}
	row.SecretVersion = version
	if err := s.store.PutSetting(ctx, providerSettingKey(p.name), row); err != nil {
		return providerRow{}, storeError(err)
	}
	return row, nil
}

// ClearProviderKey removes the credential and the metadata. The goal state is "no
// credential", so a NotFound from the control plane is success: calling it twice is not an
// error.
func (s *AgentService) ClearProviderKey(
	ctx context.Context, req *connect.Request[agentv1.ClearProviderKeyRequest],
) (*connect.Response[agentv1.ClearProviderKeyResponse], error) {
	p, err := findProvider(req.Msg.GetProvider())
	if err != nil {
		return nil, err
	}
	if s.secrets == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no Podium API client, so it cannot remove a key"))
	}
	login := Login(ctx)
	if err := s.secrets.DeleteSecret(ctx, p.secret); err != nil &&
		connect.CodeOf(err) != connect.CodeNotFound {
		return nil, connect.NewError(connect.CodeOf(err), fmt.Errorf("removing the key failed: %w", err))
	}
	// The row carries the refresh token, so deleting the row is what actually signs out:
	// leaving it would let the background pass mint a new access token for a provider the
	// operator has just disconnected.
	if err := s.store.DeleteSetting(ctx, providerSettingKey(p.name)); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a provider credential was removed", "provider", p.name, "login", login)
	return connect.NewResponse(&agentv1.ClearProviderKeyResponse{}), nil
}

// StartProviderOAuth asks the provider for a code a human can approve, and remembers the
// device code that goes with it. It stores no credential: only a poll that comes back
// authorised does that.
func (s *AgentService) StartProviderOAuth(
	ctx context.Context, req *connect.Request[agentv1.StartProviderOAuthRequest],
) (*connect.Response[agentv1.StartProviderOAuthResponse], error) {
	p, err := findProvider(req.Msg.GetProvider())
	if err != nil {
		return nil, err
	}
	client, err := s.oauthFor(p)
	if err != nil {
		return nil, err
	}

	startCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	dev, err := client.startDevice(startCtx)
	if err != nil {
		s.logger.WarnContext(ctx, "starting a subscription sign-in failed",
			"provider", p.name, "error", err)
		return nil, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("could not start a %s sign-in: %w", p.label, err))
	}

	interval := time.Duration(dev.Interval) * time.Second
	if interval < minPollInterval {
		interval = defaultPollInterval
	}
	expires := time.Now().Add(flowTTL)
	if dev.ExpiresIn > 0 {
		if d := time.Now().Add(time.Duration(dev.ExpiresIn) * time.Second); d.Before(expires) {
			expires = d
		}
	}

	id, err := flowID()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.putFlow(id, &oauthFlow{
		provider:   p.name,
		deviceCode: dev.DeviceCode,
		interval:   interval,
		expiresAt:  expires,
		startedBy:  Login(ctx),
	})
	s.logger.InfoContext(ctx, "a subscription sign-in was started",
		"provider", p.name, "login", Login(ctx), "expires_at", expires)

	return connect.NewResponse(&agentv1.StartProviderOAuthResponse{
		FlowId:                  id,
		UserCode:                dev.UserCode,
		VerificationUri:         dev.VerificationURI,
		VerificationUriComplete: dev.VerificationURIComplete,
		Interval:                int32(interval / time.Second),
		ExpiresAt:               timestamppb.New(expires),
	}), nil
}

// PollProviderOAuth asks the provider whether the human has approved yet, and stores the
// credential on the one call that comes back authorised.
//
// The token is validated against the provider's own API before it is stored, exactly as a
// pasted key is. That is not ceremony: xAI's OAuth surface has its own allow-list, so a
// sign-in can succeed and still produce a token that cannot call the API — and finding that
// out now, with the provider's own sentence, beats finding it out on the first turn.
func (s *AgentService) PollProviderOAuth(
	ctx context.Context, req *connect.Request[agentv1.PollProviderOAuthRequest],
) (*connect.Response[agentv1.PollProviderOAuthResponse], error) {
	p, err := findProvider(req.Msg.GetProvider())
	if err != nil {
		return nil, err
	}
	client, err := s.oauthFor(p)
	if err != nil {
		return nil, err
	}
	flow, ok := s.flow(req.Msg.GetFlowId())
	if !ok || flow.provider != p.name {
		// An unknown flow is one that expired, one that already finished, or one from
		// before a restart. All three mean the same thing to a human: start again.
		return connect.NewResponse(&agentv1.PollProviderOAuthResponse{State: OAuthExpired}), nil
	}
	if time.Now().After(flow.expiresAt) {
		s.dropFlow(req.Msg.GetFlowId())
		return connect.NewResponse(&agentv1.PollProviderOAuthResponse{State: OAuthExpired}), nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	state, tok, detail, err := client.pollDevice(pollCtx, flow.deviceCode)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			fmt.Errorf("could not ask %s about the sign-in: %w", p.label, err))
	}

	out := &agentv1.PollProviderOAuthResponse{
		State:    state,
		Interval: int32(flow.interval / time.Second),
		Detail:   operatorDetail(detail, nil),
	}
	switch state {
	case OAuthSlowDown:
		// RFC 8628: back off by a second and keep the new interval for the next poll.
		flow.interval += time.Second
		s.putFlow(req.Msg.GetFlowId(), flow)
		out.Interval = int32(flow.interval / time.Second)
		return connect.NewResponse(out), nil
	case OAuthPending:
		return connect.NewResponse(out), nil
	case OAuthDenied, OAuthExpired:
		s.dropFlow(req.Msg.GetFlowId())
		s.logger.WarnContext(ctx, "a subscription sign-in ended without a credential",
			"provider", p.name, "state", state, "provider_message", out.Detail)
		return connect.NewResponse(out), nil
	}

	// Authorised. The flow is done whatever happens next: the device code is single-use.
	s.dropFlow(req.Msg.GetFlowId())

	token := []byte(tok.AccessToken)
	defer zero(token)
	validateCtx, cancelValidate := context.WithTimeout(ctx, validateTimeout)
	defer cancelValidate()
	models, err := p.validate(validateCtx, s, token)
	said := operatorDetail(validationDetail(err), token)
	switch {
	case errors.Is(err, errKeyRefused):
		s.logger.WarnContext(ctx, "a subscription sign-in produced a token the API refuses",
			"provider", p.name, "provider_message", said)
		return nil, providerKeyError(connect.CodePermissionDenied, fmt.Errorf(
			"%s signed you in, but the token it issued is not allowed to call the API. "+
				"Paste an API key instead", p.label), said)
	case err != nil:
		return nil, providerKeyError(connect.CodeUnavailable, fmt.Errorf(
			"signed in, but %s could not be reached to check the token; nothing was saved", p.label), said)
	}

	row := providerRow{
		SetBy:        flow.startedBy,
		SetAt:        time.Now().UTC(),
		AuthKind:     AuthOAuth,
		Account:      account(tok),
		ExpiresAt:    expiryOf(tok),
		RefreshToken: tok.RefreshToken,
	}
	stored, err := s.storeCredential(ctx, p, token, row)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "a subscription sign-in was stored", "provider", p.name,
		"login", flow.startedBy, "account", stored.Account, "expires_at", stored.ExpiresAt,
		"refreshable", stored.RefreshToken != "", "models", len(models))

	out.Provider = providerSettings(p.name, stored, s.currentModel())
	return connect.NewResponse(out), nil
}

// ListAgents is the picker's catalogue: the backends this conductor can run a turn on, the
// models each offers, and whether a credential for it is actually stored.
func (s *AgentService) ListAgents(
	ctx context.Context, _ *connect.Request[agentv1.ListAgentsRequest],
) (*connect.Response[agentv1.ListAgentsResponse], error) {
	ready := map[string]bool{}
	for _, p := range providerSpecs {
		if s.secrets == nil {
			continue
		}
		v, err := s.secretVersion(ctx, p.secret)
		if err != nil {
			// Readiness is a hint on a picker, not a gate. An unreachable control plane
			// costs a badge, not the screen.
			s.logger.DebugContext(ctx, "could not check a provider credential for the picker",
				"provider", p.name, "error", err)
			continue
		}
		ready[p.name] = v > 0
	}

	out := &agentv1.ListAgentsResponse{DefaultAgent: profiles.DefaultAgent}
	for _, b := range profiles.Backends {
		ab := &agentv1.AgentBackend{
			Id:           b.ID,
			DisplayName:  b.DisplayName,
			Provider:     b.Provider,
			Note:         b.Note,
			DefaultModel: b.DefaultModel,
			Ready:        ready[b.Provider],
		}
		for _, m := range b.Models {
			ab.Models = append(ab.Models, &agentv1.AgentModel{
				Id:            m.ID,
				DisplayName:   m.DisplayName,
				Note:          m.Note,
				ContextTokens: int32(m.ContextTokens),
				Efforts:       m.Efforts,
			})
		}
		out.Agents = append(out.Agents, ab)
	}
	return connect.NewResponse(out), nil
}

// RefreshTokens keeps stored OAuth credentials alive until the process stops.
//
// It runs as its own goroutine because nothing else is watching: a token issued for an hour
// would otherwise stop working an hour after a sign-in, and the first anybody would hear of
// it is a turn failing. A refresh that fails is logged and retried on the next tick — the
// stored token is left alone, because a token that still has thirty minutes on it is more
// use than no token at all.
func (s *AgentService) RefreshTokens(ctx context.Context) {
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	s.refreshOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshOnce(ctx)
		}
	}
}

func (s *AgentService) refreshOnce(ctx context.Context) {
	s.sweepFlows()
	for _, p := range providerSpecs {
		if !p.oauth || s.secrets == nil || s.store == nil {
			continue
		}
		client, err := s.oauthFor(p)
		if err != nil {
			continue
		}
		row, ok, err := s.providerRow(ctx, p)
		if err != nil || !ok || row.authKind() != AuthOAuth || row.RefreshToken == "" {
			continue
		}
		if !row.ExpiresAt.IsZero() && time.Until(row.ExpiresAt) > refreshLead {
			continue
		}

		callCtx, cancel := context.WithTimeout(ctx, validateTimeout)
		tok, err := client.refresh(callCtx, row.RefreshToken)
		cancel()
		if err != nil {
			s.logger.WarnContext(ctx, "refreshing a subscription token failed; the stored one "+
				"is kept and this will be tried again. Sign in again if turns start failing",
				"provider", p.name, "expires_at", row.ExpiresAt, "error", err)
			continue
		}

		token := []byte(tok.AccessToken)
		next := row
		next.ExpiresAt = expiryOf(tok)
		if tok.RefreshToken != "" {
			// A provider that rotates refresh tokens invalidates the old one, so keeping
			// the old one would mean the next refresh fails.
			next.RefreshToken = tok.RefreshToken
		}
		if a := account(tok); a != "" {
			next.Account = a
		}
		if _, err := s.storeCredential(ctx, p, token, next); err != nil {
			s.logger.ErrorContext(ctx, "a subscription token was refreshed but could not be "+
				"stored; turns will keep using the previous one until it expires",
				"provider", p.name, "error", err)
		} else {
			s.logger.InfoContext(ctx, "a subscription token was refreshed",
				"provider", p.name, "expires_at", next.ExpiresAt)
		}
		zero(token)
	}
}

// oauthFor is the provider's OAuth client, or the reason there is not one.
func (s *AgentService) oauthFor(p providerSpec) (*oauthClient, error) {
	if !p.oauth {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s does not offer a subscription sign-in on this control plane; it takes an API key",
			p.label))
	}
	c := s.oauth[p.name]
	if c == nil {
		// There is a client id by default, so reaching this means somebody set the variable
		// to empty on purpose. The message says so rather than telling them to set a thing
		// they have already decided not to set.
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%w: PODIUM_AGENT_XAI_OAUTH_CLIENT_ID is set to the empty string, which turns "+
				"the %s sign-in off. Unset it to use the default client, or paste an API key",
			errOAuthUnconfigured, p.label))
	}
	return c, nil
}

// providerRow reads a provider's metadata row. The second result is false when nothing was
// ever set for it.
func (s *AgentService) providerRow(ctx context.Context, p providerSpec) (providerRow, bool, error) {
	var row providerRow
	err := s.store.GetSetting(ctx, providerSettingKey(p.name), &row)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return providerRow{}, false, nil
	case err != nil:
		return providerRow{}, false, storeError(err)
	}
	return row, true, nil
}

// The in-flight sign-in map. It is per process and is deliberately not persisted: a device
// code outlives neither its own expiry nor a restart, and a restart mid-sign-in is a
// "start again", not a state to recover.
func (s *AgentService) putFlow(id string, f *oauthFlow) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	if s.flows == nil {
		s.flows = map[string]*oauthFlow{}
	}
	s.flows[id] = f
}

func (s *AgentService) flow(id string) (*oauthFlow, bool) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	f, ok := s.flows[id]
	return f, ok
}

func (s *AgentService) dropFlow(id string) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	delete(s.flows, id)
}

func (s *AgentService) sweepFlows() {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	now := time.Now()
	for id, f := range s.flows {
		if now.After(f.expiresAt) {
			delete(s.flows, id)
		}
	}
}

// flowID is a name for a sign-in. It is random because a guessable one would let another
// caller on this bearer finish somebody else's flow.
func flowID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating a sign-in id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// expiryOf is when an access token stops working, or the zero time when the provider did
// not say. An unknown expiry is treated as "refresh it on the next pass", which is what the
// zero time means to refreshOnce.
func expiryOf(tok *tokenResponse) time.Time {
	if tok.ExpiresIn <= 0 {
		return time.Time{}
	}
	return time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second)
}

// providerKeyError is a credential failure with the provider's own explanation attached as
// a Connect error detail.
//
// The code and the message are unchanged by it: the code is still the classification a UI
// switches on — permission_denied refused, unavailable not asked — and the message is still
// the conductor's own sentence. The detail is additive, so a client that does not read it
// sees exactly what it saw before, and a detail that will not marshal is dropped rather than
// replacing the error the operator actually needs.
func providerKeyError(code connect.Code, err error, detail string) *connect.Error {
	cerr := connect.NewError(code, err)
	if detail == "" {
		return cerr
	}
	d, derr := connect.NewErrorDetail(&agentv1.ProviderKeyError{ProviderMessage: detail})
	if derr != nil {
		return cerr
	}
	cerr.AddDetail(d)
	return cerr
}

// providerSettings is the row as the UI sees it, for the callers that have just written it
// and therefore know it is current. The refresh token is not in ProviderSettings and must
// never be added to it.
func providerSettings(provider string, row providerRow, model string) *agentv1.ProviderSettings {
	out := &agentv1.ProviderSettings{
		Provider:    provider,
		KeySet:      true,
		KeyHint:     row.KeyHint,
		Model:       model,
		SetBy:       row.SetBy,
		AuthKind:    row.authKind(),
		Account:     row.Account,
		Refreshable: row.RefreshToken != "",
	}
	if !row.SetAt.IsZero() {
		out.SetAt = timestamppb.New(row.SetAt)
	}
	if !row.ExpiresAt.IsZero() {
		out.ExpiresAt = timestamppb.New(row.ExpiresAt)
	}
	return out
}

// redactedKeyRequest is how a SetProviderKey request reaches a log statement. Every field
// but the key, and the key as a length.
type redactedKeyRequest struct {
	msg *agentv1.SetProviderKeyRequest
}

func (r redactedKeyRequest) LogValue() slog.Value {
	provider := r.msg.GetProvider()
	if provider == "" {
		provider = ProviderAnthropic
	}
	return slog.GroupValue(
		slog.String("provider", provider),
		slog.String("key", "[redacted]"),
		slog.Int("key_len", len(strings.TrimSpace(r.msg.GetKey()))),
	)
}

// trimPastedKey is the shape of a key as a human supplies it. People copy out of a .env
// file or a chat message, so the surrounding whitespace and quotes come along.
func trimPastedKey(raw string) string {
	s := strings.TrimSpace(raw)
	for len(s) >= 2 {
		q := s[0]
		if (q == '"' || q == '\'') && s[len(s)-1] == q {
			s = strings.TrimSpace(s[1 : len(s)-1])
			continue
		}
		break
	}
	return s
}

// keyHint is the last four characters of a key. Four is short enough to be useless to
// anybody who does not already have the key and long enough to tell two keys apart.
func keyHint(key []byte) string {
	if len(key) <= keyHintLen {
		return string(key)
	}
	return string(key[len(key)-keyHintLen:])
}

// unusualFormat is the note the response carries when a key does not look like one.
// Anthropic has changed prefixes before, so an unfamiliar prefix is worth saying and is not
// worth refusing — the provider itself has already agreed the key works.
func unusualFormat(key []byte) string {
	if strings.HasPrefix(string(key), "sk-ant-") {
		return ""
	}
	return "key format looks unusual; validated anyway"
}

// zero overwrites b. It is the three lines of internal/server/secrets.Zero, copied rather
// than imported: internal/agent may not depend on internal/server.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
