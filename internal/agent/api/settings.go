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

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/pkg/spec"
)

// The providers this control plane knows. A provider is a set of credentials and an
// endpoint; profiles.Backends is what says which agent backend spends which of them.
const (
	ProviderAnthropic = profiles.ProviderAnthropic
	ProviderXAI       = profiles.ProviderXAI
	ProviderOpenAI    = profiles.ProviderOpenAI
	ProviderGoogle    = profiles.ProviderGoogle
)

// How a stored credential was obtained. The distinction is not cosmetic: an API key does
// not expire and an access token does, so only one of the two is refreshed and only one of
// the two has a hint worth showing.
const (
	AuthAPIKey = "api_key"
	AuthOAuth  = "oauth"
)

// Whose credential a turn spends, as ListAgents reports it.
const (
	CredentialPersonal = "personal"
	CredentialGlobal   = "global"
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
	// oauthEnv is the variable an operator sets to turn the subscription tab on, named in
	// the error when it is off so they are not left hunting.
	oauthEnv string
	// validateOAuth is how a subscription token is checked. Nil means the same validate
	// as a pasted key — xAI, where both are bearers for one endpoint. OpenAI needs a
	// second one: a ChatGPT token is refused by api.openai.com.
	validateOAuth func(ctx context.Context, s *AgentService, key []byte) ([]string, error)
	// oauthOff replaces the generic "set the client id" sentence when the sign-in is off.
	oauthOff string
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
	// A Claude subscription is spent only by the chat host's claude-agent provider, so the
	// sign-in is offered only where the chat host can run Claude Code.
	oauth:    true,
	oauthEnv: "PODIUM_AGENT_CHAT_HOST_CLAUDE",
	oauthOff: "Claude subscriptions need the chat host and Claude Code: set PODIUM_AGENT_CHAT_HOST " +
		"and PODIUM_AGENT_CHAT_HOST_CLAUDE, or paste an API key instead",
	validateOAuth: func(ctx context.Context, s *AgentService, key []byte) ([]string, error) {
		return validateClaudeOAuthToken(ctx, s.http, s.baseURL, key)
	},
}, {
	name:   ProviderXAI,
	label:  "xAI",
	secret: profiles.XAIKeySecret,
	validate: func(ctx context.Context, s *AgentService, key []byte) ([]string, error) {
		return validateXAIKey(ctx, s.http, s.xaiBaseURL, key)
	},
	unusual:  unusualXAIFormat,
	oauth:    true,
	oauthEnv: "PODIUM_AGENT_XAI_OAUTH_CLIENT_ID",
}, {
	name:   ProviderOpenAI,
	label:  "OpenAI",
	secret: profiles.OpenAIKeySecret,
	validate: func(ctx context.Context, s *AgentService, key []byte) ([]string, error) {
		return validateOpenAIKey(ctx, s.http, s.openaiBaseURL, key)
	},
	unusual:  unusualOpenAIFormat,
	oauth:    true,
	oauthEnv: "PODIUM_AGENT_OPENAI_OAUTH_CLIENT_ID",
	validateOAuth: func(ctx context.Context, s *AgentService, key []byte) ([]string, error) {
		return validateOpenAICodexToken(ctx, s.http, s.openaiCodexBaseURL, key)
	},
}, {
	name:   ProviderGoogle,
	label:  "Google",
	secret: profiles.GoogleKeySecret,
	validate: func(ctx context.Context, s *AgentService, key []byte) ([]string, error) {
		return validateGoogleKey(ctx, s.http, s.googleBaseURL, key)
	},
	unusual: unusualGoogleFormat,
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

// credTarget is one stored credential: a provider's, and whose. An empty owner is the
// global credential an admin set, which every turn without one of its person's own spends;
// a login is that person's own, kept on their personal secret list.
type credTarget struct {
	p     providerSpec
	owner string
}

func globalTarget(p providerSpec) credTarget { return credTarget{p: p} }

// secret is the Podium secret the bearer lives in.
func (t credTarget) secret() string { return profiles.CredentialSecretFor(t.p.name, t.owner) }

// refreshName is where a sign-in's refresh token lives.
func (t credTarget) refreshName() string {
	if t.owner == "" {
		return refreshSecret(t.p)
	}
	return spec.PersonalModelRefreshSecretName(t.p.name)
}

// signIn is whether this credential can be a sign-in, so it may have a refresh token to
// clean up. A Claude sign-in is only ever a person's: the global Anthropic key is a key.
func (t credTarget) signIn() bool {
	return t.p.oauth && (t.owner != "" || t.p.name != ProviderAnthropic)
}

// settingKey is the settings row this credential's metadata lives in.
func (t credTarget) settingKey() string { return profiles.ProviderSettingKey(t.p.name, t.owner) }

// The four secret operations, each routed to the global list or the owner's personal one.
func (s *AgentService) setCredSecret(ctx context.Context, t credTarget, name string, value []byte) (int32, error) {
	if t.owner == "" {
		return s.secrets.SetSecret(ctx, name, value)
	}
	return s.secrets.SetPersonalSecret(ctx, t.owner, name, value)
}

func (s *AgentService) deleteCredSecret(ctx context.Context, t credTarget, name string) error {
	var err error
	if t.owner == "" {
		err = s.secrets.DeleteSecret(ctx, name)
	} else {
		err = s.secrets.DeletePersonalSecret(ctx, t.owner, name)
	}
	if connect.CodeOf(err) == connect.CodeNotFound {
		return nil
	}
	return err
}

func (s *AgentService) credSecretVersion(ctx context.Context, t credTarget) (int32, error) {
	if t.owner == "" {
		return s.secretVersion(ctx, t.secret())
	}
	return s.personalSecretVersion(ctx, t.owner, t.secret())
}

// providerOwner is whose credential a provider mutation touches. A signed-in person writes
// their own unless they ask for the global one, which only an admin or owner may. A caller
// that is not a person — the dev token, the local transport — has nobody to own a
// credential, so it writes the global one, which is what this control plane always did.
func providerOwner(ctx context.Context, global bool) (string, error) {
	person := mcpPersonalOwner(Login(ctx))
	if person == "" {
		return "", nil
	}
	if global {
		if !Infra(ctx) {
			return "", connect.NewError(connect.CodePermissionDenied,
				errors.New("only an admin or owner can set the global key; save your own key instead"))
		}
		return "", nil
	}
	return person, nil
}

// providerRow is the jsonb of the settings row.
//
// It holds no credential. The bearer a turn spends is the provider's Podium secret, and a
// sign-in's refresh token is another (refreshSecret). The conductor reads both back from the
// secret store, which answers it for its own names. A row written before that may still
// carry them, and providerRow moves them.
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
	// RefreshToken is SENSITIVE and held only in memory: putProviderRow stores it as
	// refreshSecret and writes the row without it. It never leaves this process except to the
	// provider's own token endpoint.
	RefreshToken string `json:"refresh_token,omitempty"`
	// Refreshable records that a refresh token is stored.
	Refreshable bool `json:"refreshable,omitempty"`
	// Credential is a copy of the bearer that rows from before the secret store answered the
	// conductor carried in clear. It is read only to be removed.
	Credential string `json:"credential,omitempty"`
}

// refreshSecret is where a provider sign-in's refresh token is stored.
func refreshSecret(p providerSpec) string {
	return spec.ConductorReservedPrefix + "oauth." + p.name + ".refresh_token"
}

// refreshable is whether the row's sign-in can be refreshed.
func (r providerRow) refreshable() bool { return r.Refreshable || r.RefreshToken != "" }

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
	userCode   string
	interval   time.Duration
	expiresAt  time.Time
	startedBy  string
	// owner is whose credential the sign-in stores: the person who started it, or empty for
	// a caller that is not a person.
	owner string
	// codePaste, state and code are for a flow the human finishes by pasting a code
	// (claudeClient): deviceCode is then the PKCE verifier, state what the code must carry,
	// and code what this poll was given. code is never stored on the flow.
	codePaste bool
	state     string
	code      string
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
	person := mcpPersonalOwner(Login(ctx))
	for _, p := range providerSpecs {
		ps, err := s.providerState(ctx, globalTarget(p))
		if err != nil {
			return nil, err
		}
		out.Providers = append(out.Providers, ps)
		if p.name == ProviderAnthropic {
			// The field the clients that only ever knew about one provider read.
			out.Provider = ps
		}
		if person != "" {
			mine, err := s.providerState(ctx, credTarget{p: p, owner: person})
			if err != nil {
				return nil, err
			}
			out.Personal = append(out.Personal, mine)
		}
	}
	return connect.NewResponse(out), nil
}

// providerState is one provider's row as the UI sees it.
func (s *AgentService) providerState(ctx context.Context, t credTarget) (*agentv1.ProviderSettings, error) {
	p := t.p
	row, hasRow, err := s.providerRow(ctx, t)
	if err != nil {
		return nil, err
	}
	out := &agentv1.ProviderSettings{Provider: p.name, Model: s.currentModel(), Global: t.owner == ""}
	if s.secrets == nil {
		return out, nil
	}
	version, verr := s.credSecretVersion(ctx, t)
	switch {
	case verr != nil:
		// The control plane could not be asked. Reporting the last thing we knew beats a
		// broken page, and the log says the answer may be stale.
		s.logger.WarnContext(ctx, "could not confirm a provider credential with the control "+
			"plane; reporting the last known state", "provider", p.name, "owner", t.owner, "error", verr)
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
		out.Refreshable = row.refreshable()
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
	owner, err := providerOwner(ctx, req.Msg.GetGlobal())
	if err != nil {
		return nil, err
	}
	t := credTarget{p: p, owner: owner}

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

	row, err := s.storeCredential(ctx, t, key, providerRow{
		KeyHint:  keyHint(key),
		SetBy:    login,
		SetAt:    time.Now().UTC(),
		AuthKind: AuthAPIKey,
	})
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "a provider key was validated and stored",
		"provider", p.name, "login", login, "global", owner == "", "key_hint", row.KeyHint,
		"secret_version", row.SecretVersion, "models", len(models))

	return connect.NewResponse(&agentv1.SetProviderKeyResponse{
		Provider: providerSettings(t, row, s.currentModel()),
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
	ctx context.Context, t credTarget, key []byte, row providerRow,
) (providerRow, error) {
	version, err := s.setCredSecret(ctx, t, t.secret(), key)
	if err != nil {
		// Verbatim: the control plane's own words are what an operator needs here — a
		// missing master key reads very differently from a network failure.
		return providerRow{}, connect.NewError(connect.CodeOf(err),
			fmt.Errorf("the credential is valid but storing it failed: %w", err))
	}
	row.SecretVersion = version
	return s.putProviderRow(ctx, t, row)
}

// putProviderRow stores a sign-in's refresh token in the secret store, or removes it when
// the row no longer has one, and then writes the row without any credential.
func (s *AgentService) putProviderRow(ctx context.Context, t credTarget, row providerRow) (providerRow, error) {
	switch {
	case row.RefreshToken != "":
		refresh := []byte(row.RefreshToken)
		_, err := s.setCredSecret(ctx, t, t.refreshName(), refresh)
		zero(refresh)
		if err != nil {
			return providerRow{}, connect.NewError(connect.CodeOf(err),
				fmt.Errorf("storing the refresh token failed: %w", err))
		}
		row.Refreshable = true
	case !row.Refreshable && t.signIn():
		if err := s.deleteCredSecret(ctx, t, t.refreshName()); err != nil {
			return providerRow{}, connect.NewError(connect.CodeOf(err),
				fmt.Errorf("removing the refresh token failed: %w", err))
		}
	}
	row.RefreshToken, row.Credential = "", ""
	if err := s.store.PutSetting(ctx, t.settingKey(), row); err != nil {
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
	owner, err := providerOwner(ctx, req.Msg.GetGlobal())
	if err != nil {
		return nil, err
	}
	t := credTarget{p: p, owner: owner}
	if err := s.deleteCredSecret(ctx, t, t.secret()); err != nil {
		return nil, connect.NewError(connect.CodeOf(err), fmt.Errorf("removing the key failed: %w", err))
	}
	// The refresh token goes too, and that is what actually signs out: leaving it would let
	// the background pass mint a new access token for a provider the operator has just
	// disconnected.
	if t.signIn() {
		if err := s.deleteCredSecret(ctx, t, t.refreshName()); err != nil {
			return nil, connect.NewError(connect.CodeOf(err), fmt.Errorf("removing the refresh token failed: %w", err))
		}
	}
	if err := s.store.DeleteSetting(ctx, t.settingKey()); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a provider credential was removed", "provider", p.name, "login", login,
		"global", owner == "")
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
	if s.secrets == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no Podium API client, so it cannot store a sign-in"))
	}
	// A subscription is the person's who signed in, so a sign-in is always stored as theirs.
	// There is no global one to start: the global credential is an API key an admin pastes.
	owner, err := providerOwner(ctx, false)
	if err != nil {
		return nil, err
	}
	if owner == "" && p.name == ProviderAnthropic {
		// A global Anthropic credential is spent by every kind of turn, and only the chat
		// host's Claude Code may spend a Claude sign-in. So it is only ever a person's own.
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(
			"a Claude subscription is one person's: sign in to Podium as yourself and connect it "+
				"under Yours, or paste an API key"))
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
		userCode:   dev.UserCode,
		interval:   interval,
		expiresAt:  expires,
		startedBy:  Login(ctx),
		owner:      owner,
		codePaste:  dev.CodePaste,
		state:      dev.State,
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
		CodePaste:               dev.CodePaste,
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
	// A flow is finished only by the person who started it: it stores a credential on their
	// list, and a flow id seen by somebody else must not let them finish it.
	if !ok || flow.provider != p.name || flow.startedBy != Login(ctx) {
		// An unknown flow is one that expired, one that already finished, or one from
		// before a restart. All three mean the same thing to a human: start again.
		return connect.NewResponse(&agentv1.PollProviderOAuthResponse{State: OAuthExpired}), nil
	}
	if time.Now().After(flow.expiresAt) {
		s.dropFlow(req.Msg.GetFlowId())
		return connect.NewResponse(&agentv1.PollProviderOAuthResponse{State: OAuthExpired}), nil
	}

	if flow.codePaste {
		// A copy: the pasted code belongs to this poll, not to the remembered flow.
		pasted := *flow
		pasted.code = strings.TrimSpace(req.Msg.GetCode())
		flow = &pasted
	}
	pollCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	state, tok, detail, err := client.pollFlow(pollCtx, flow)
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
	validate := p.validate
	if p.validateOAuth != nil {
		validate = p.validateOAuth
	}
	models, err := validate(validateCtx, s, token)
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
	t := credTarget{p: p, owner: flow.owner}
	stored, err := s.storeCredential(ctx, t, token, row)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "a subscription sign-in was stored", "provider", p.name,
		"login", flow.startedBy, "global", flow.owner == "", "account", stored.Account,
		"expires_at", stored.ExpiresAt, "refreshable", stored.Refreshable, "models", len(models))

	out.Provider = providerSettings(t, stored, s.currentModel())
	return connect.NewResponse(out), nil
}

// ListAgents is the picker's catalogue: the backends this conductor can run a turn on, the
// models each offers, and whether a credential for it is actually stored.
func (s *AgentService) ListAgents(
	ctx context.Context, _ *connect.Request[agentv1.ListAgentsRequest],
) (*connect.Response[agentv1.ListAgentsResponse], error) {
	// source is whose credential a turn this caller asks for would spend, per provider: the
	// same choice the conductor makes when the turn starts.
	source := map[string]string{}
	person := mcpPersonalOwner(Login(ctx))
	for _, p := range providerSpecs {
		if s.secrets == nil {
			continue
		}
		if person != "" {
			v, err := s.credSecretVersion(ctx, credTarget{p: p, owner: person})
			if err != nil {
				s.logger.DebugContext(ctx, "could not check a personal provider credential for the picker",
					"provider", p.name, "error", err)
			} else if v > 0 {
				source[p.name] = CredentialPersonal
				continue
			}
		}
		v, err := s.secretVersion(ctx, p.secret)
		if err != nil {
			// Readiness is a hint on a picker, not a gate. An unreachable control plane
			// costs a badge, not the screen.
			s.logger.DebugContext(ctx, "could not check a provider credential for the picker",
				"provider", p.name, "error", err)
			continue
		}
		if v > 0 {
			source[p.name] = CredentialGlobal
		}
	}

	out := &agentv1.ListAgentsResponse{DefaultAgent: profiles.DefaultAgent}
	for _, b := range profiles.Backends {
		ab := &agentv1.AgentBackend{
			Id:               b.ID,
			DisplayName:      b.DisplayName,
			Provider:         b.Provider,
			Note:             b.Note,
			DefaultModel:     b.DefaultModel,
			Ready:            source[b.Provider] != "",
			CredentialSource: source[b.Provider],
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

// MoveCredentialsIntoStore moves the credentials a database written before the secret store
// answered the conductor still holds in clear: providers' refresh tokens and bearer copies,
// and MCP sign-ins' refresh tokens and client secrets. A failure is logged and the move is
// tried again on the next start, or for a provider on its next read.
func (s *AgentService) MoveCredentialsIntoStore(ctx context.Context) {
	if s.store == nil || s.secrets == nil {
		return
	}
	for _, p := range providerSpecs {
		if _, _, err := s.providerRow(ctx, globalTarget(p)); err != nil {
			s.logger.WarnContext(ctx, "reading a provider row to move its credential failed",
				"provider", p.name, "error", err)
		}
	}
	rows, err := s.store.ListMcpServers(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "reading the mcp registry to move its sign-ins failed", "error", err)
		return
	}
	for _, row := range rows {
		if row.OAuth == nil || (row.OAuth.RefreshToken == "" && row.OAuth.ClientSecret == "") {
			continue
		}
		o, err := s.vaultMcpOAuth(ctx, row.Owner, row.Name, *row.OAuth)
		if err == nil {
			err = s.store.RefreshMcpServerOAuth(ctx, row.Owner, row.Name, row.TokenSecretVersion, o)
		}
		if err != nil {
			s.logger.WarnContext(ctx, "moving an mcp sign-in's secrets into the secret store failed; "+
				"it is tried again on the next start", "mcp_server", row.Name, "error", err)
			continue
		}
		s.logger.InfoContext(ctx, "moved an mcp sign-in's secrets into the secret store", "mcp_server", row.Name)
	}
}

// RefreshTokens keeps stored OAuth credentials alive until the process stops — the
// subscription sign-in's, and every signed-in MCP server's.
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
	// The MCP registry's sign-ins are on the same tick and the same lead. They are a
	// different credential in a different table, but "a token issued for an hour stops
	// working an hour later and the first anybody hears of it is a turn failing" is the
	// same problem — see refreshMcpOnce.
	s.refreshMcpOnce(ctx)
	if s.secrets == nil || s.store == nil {
		return
	}
	for _, t := range s.signedInTargets(ctx) {
		client, err := s.oauthFor(t.p)
		if err != nil {
			continue
		}
		row, ok, err := s.providerRow(ctx, t)
		if err != nil || !ok || row.authKind() != AuthOAuth || !row.refreshable() {
			continue
		}
		if !row.ExpiresAt.IsZero() && time.Until(row.ExpiresAt) > refreshLead {
			continue
		}
		s.refreshProvider(ctx, client, t, row)
	}
}

// signedInTargets is every stored credential a refresh pass looks at: each subscription
// provider's global row — a sign-in stored before sign-ins became personal, still kept
// alive — and every person's own row for it.
func (s *AgentService) signedInTargets(ctx context.Context) []credTarget {
	var out []credTarget
	for _, p := range providerSpecs {
		if !p.oauth {
			continue
		}
		if globalTarget(p).signIn() {
			out = append(out, globalTarget(p))
		}
		prefix := profiles.ProviderSettingKey(p.name, "") + "@"
		rows, err := s.store.SettingsWithPrefix(ctx, prefix)
		if err != nil {
			s.logger.WarnContext(ctx, "listing personal sign-ins to refresh failed; this will be "+
				"tried again", "provider", p.name, "error", err)
			continue
		}
		for key := range rows {
			if owner := strings.TrimPrefix(key, prefix); owner != "" {
				out = append(out, credTarget{p: p, owner: owner})
			}
		}
	}
	return out
}

// refreshProvider trades one stored sign-in's refresh token for a new access token and
// stores both.
func (s *AgentService) refreshProvider(ctx context.Context, client oauthSession, t credTarget, row providerRow) {
	p := t.p
	refresh := row.RefreshToken
	if refresh == "" {
		raw, err := s.secrets.ReadSecret(ctx, t.owner, t.refreshName())
		if err != nil {
			s.logger.WarnContext(ctx, "reading a subscription's refresh token failed; this "+
				"will be tried again", "provider", p.name, "owner", t.owner, "error", err)
			return
		}
		refresh = string(raw)
		zero(raw)
	}

	callCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	tok, err := client.refresh(callCtx, refresh)
	cancel()
	if err != nil {
		s.logger.WarnContext(ctx, "refreshing a subscription token failed; the stored one "+
			"is kept and this will be tried again. Sign in again if turns start failing",
			"provider", p.name, "owner", t.owner, "expires_at", row.ExpiresAt, "error", err)
		return
	}

	token := []byte(tok.AccessToken)
	defer zero(token)
	next := row
	next.RefreshToken = refresh
	next.ExpiresAt = expiryOf(tok)
	if tok.RefreshToken != "" {
		// A provider that rotates refresh tokens invalidates the old one, so keeping
		// the old one would mean the next refresh fails.
		next.RefreshToken = tok.RefreshToken
	}
	if a := account(tok); a != "" {
		next.Account = a
	}
	if _, err := s.storeCredential(ctx, t, token, next); err != nil {
		s.logger.ErrorContext(ctx, "a subscription token was refreshed but could not be "+
			"stored; turns will keep using the previous one until it expires",
			"provider", p.name, "owner", t.owner, "error", err)
		return
	}
	s.logger.InfoContext(ctx, "a subscription token was refreshed",
		"provider", p.name, "owner", t.owner, "expires_at", next.ExpiresAt)
}

// oauthFor is the provider's OAuth client, or the reason there is not one.
func (s *AgentService) oauthFor(p providerSpec) (oauthSession, error) {
	if !p.oauth {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%s does not offer a subscription sign-in on this control plane; it takes an API key",
			p.label))
	}
	c := s.oauth[p.name]
	if c == nil && p.oauthOff != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%w: %s", errOAuthUnconfigured, p.oauthOff))
	}
	if c == nil {
		env := p.oauthEnv
		if env == "" {
			env = "the OAuth client id"
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%w: set %s to the OAuth client id of a public desktop client registered with %s, "+
				"or paste an API key instead",
			errOAuthUnconfigured, env, p.label))
	}
	return c, nil
}

// providerRow reads a provider's metadata row. The second result is false when nothing was
// ever set for it.
//
// A row that still carries a credential in clear is moved: the refresh token into the secret
// store, and the bearer copy dropped, since the provider's secret already holds it. If the
// move fails the row is returned as it was and the move is tried on the next read.
func (s *AgentService) providerRow(ctx context.Context, t credTarget) (providerRow, bool, error) {
	p := t.p
	var row providerRow
	err := s.store.GetSetting(ctx, t.settingKey(), &row)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return providerRow{}, false, nil
	case err != nil:
		return providerRow{}, false, storeError(err)
	}
	if (row.RefreshToken != "" || row.Credential != "") && s.secrets != nil {
		moved, err := s.putProviderRow(ctx, t, row)
		if err != nil {
			s.logger.WarnContext(ctx, "moving a provider's stored credential into the secret store "+
				"failed; it is tried again on the next read", "provider", p.name, "error", err)
			return row, true, nil
		}
		return moved, true, nil
	}
	return row, true, nil
}

// HostCredential is the bearer a host turn spends for one provider, read back from the
// provider's global Podium secret: the same one a container turn is given. A person's own is
// PersonalHostCredential.
func (s *AgentService) HostCredential(ctx context.Context, provider string) (string, error) {
	p, err := findProvider(provider)
	if err != nil {
		return "", err
	}
	value, err := s.secrets.ReadSecret(ctx, "", p.secret)
	if connect.CodeOf(err) == connect.CodeNotFound {
		return "", fmt.Errorf("no credential is stored for %s", p.name)
	}
	if err != nil {
		return "", fmt.Errorf("reading the %s credential: %w", p.name, err)
	}
	return string(value), nil
}

// PersonalHostCredential is login's own key for provider — the personal secret
// spec.PersonalModelKeySecretName — or "" when they set none, which is not an error: the
// company's key is spent instead.
func (s *AgentService) PersonalHostCredential(ctx context.Context, login, provider string) (string, error) {
	if s.secrets == nil || login == "" {
		return "", nil
	}
	value, err := s.secrets.ReadSecret(ctx, login, spec.PersonalModelKeySecretName(provider))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s's own %s key: %w", login, provider, err)
	}
	return strings.TrimSpace(string(value)), nil
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
	if tok.ExpiresIn > 0 {
		return time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	if t := jwtExp(tok.AccessToken); !t.IsZero() {
		return t
	}
	return time.Time{}
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
func providerSettings(t credTarget, row providerRow, model string) *agentv1.ProviderSettings {
	out := &agentv1.ProviderSettings{
		Provider:    t.p.name,
		Global:      t.owner == "",
		KeySet:      true,
		KeyHint:     row.KeyHint,
		Model:       model,
		SetBy:       row.SetBy,
		AuthKind:    row.authKind(),
		Account:     row.Account,
		Refreshable: row.refreshable(),
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
