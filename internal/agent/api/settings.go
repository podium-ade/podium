package api

import (
	"context"
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

// ProviderAnthropic is the only provider this track knows. The provider field exists so
// BYOK later is a new case here, not a new RPC.
const ProviderAnthropic = "anthropic"

// providerSettingKey is the settings row the provider metadata lives in.
const providerSettingKey = "provider.anthropic"

// validateTimeout bounds the call to the provider. An operator is waiting on a button.
const validateTimeout = 10 * time.Second

// keyHintLen is how much of a key is kept. It is computed once, at save time, and is the
// only form in which any part of the key is ever read back.
const keyHintLen = 4

// providerRow is the jsonb of the settings row. It holds no part of the key beyond the hint,
// and SecretVersion is which version of the secret that hint describes: an operator who
// runs `podium secret set` behind the UI's back bumps the version, and then the hint here is
// about a key that no longer exists.
type providerRow struct {
	KeyHint       string    `json:"key_hint"`
	SetBy         string    `json:"set_by"`
	SetAt         time.Time `json:"set_at"`
	SecretVersion int32     `json:"secret_version"`
}

// GetSettings reports what the conductor is configured with.
//
// key_set means "an agent turn will find a key", so it comes from the **control plane**, not
// from the metadata row: the secret is the control plane's and `podium secret rm` can take it
// away without the conductor hearing. The row only supplies the hint, the login and the time,
// and only when it still describes the version that is actually stored. Nothing here reads a
// value — there is no endpoint that could.
func (s *AgentService) GetSettings(
	ctx context.Context, _ *connect.Request[agentv1.GetSettingsRequest],
) (*connect.Response[agentv1.GetSettingsResponse], error) {
	row, hasRow, err := s.providerRow(ctx)
	if err != nil {
		return nil, err
	}

	out := &agentv1.ProviderSettings{Provider: ProviderAnthropic, Model: s.currentModel()}
	version, err := s.secretVersion(ctx)
	switch {
	case err != nil:
		// The control plane could not be asked. Reporting the last thing we knew beats a
		// broken page, and the log says the answer may be stale.
		s.logger.WarnContext(ctx, "could not confirm the provider key with the control plane; "+
			"reporting the last known state", "error", err)
		out.KeySet = hasRow
	case version == 0:
		// No secret. A row here is stale — somebody removed the key with the CLI — so it is
		// deliberately not shown: saying "connected" would be a lie every turn disproves.
		out.KeySet = false
	default:
		out.KeySet = true
	}

	// The hint describes one version of one key. Anything else and it is withheld rather
	// than shown next to a key it is not about.
	if out.KeySet && hasRow && (version == row.SecretVersion || err != nil) {
		out.KeyHint = row.KeyHint
		out.SetBy = row.SetBy
		if !row.SetAt.IsZero() {
			out.SetAt = timestamppb.New(row.SetAt)
		}
	}
	return connect.NewResponse(&agentv1.GetSettingsResponse{Provider: out}), nil
}

// secretVersion is the stored version of the reserved secret, or 0 when there is none. A
// control plane with no master key cannot hold a secret at all, which is 0 rather than a
// failure: the operator finds out with the real message when they try to save.
func (s *AgentService) secretVersion(ctx context.Context) (int32, error) {
	v, err := s.secrets.SecretVersion(ctx, profiles.AnthropicKeySecret)
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
	if p := req.Msg.GetProvider(); p != "" && p != ProviderAnthropic {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unsupported provider %q: this control plane knows %q", p, ProviderAnthropic))
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
	models, err := validateAnthropicKey(validateCtx, s.http, s.baseURL, key)
	// What the provider said, scrubbed once and used twice: the same bounded, key-free
	// sentence goes to the log and to the operator, so a key a provider echoed back cannot
	// reach either. "" when there was nothing to say, which includes success.
	said := operatorDetail(validationDetail(err), key)
	switch {
	case errors.Is(err, errKeyRefused):
		s.logger.WarnContext(ctx, "the provider refused a key",
			"login", login, "provider_message", said)
		// ST1005 wants a lower-case error string; "Anthropic" is a proper noun and this
		// string is the sentence the web UI renders verbatim.
		return nil, providerKeyError(connect.CodePermissionDenied,
			errors.New("Anthropic rejected this key"), said) //nolint:staticcheck // a proper noun
	case err != nil:
		s.logger.WarnContext(ctx, "a provider key could not be validated",
			"login", login, "provider_message", said)
		return nil, providerKeyError(connect.CodeUnavailable,
			errors.New("could not validate the key with Anthropic; nothing was saved"), said)
	}

	version, err := s.secrets.SetSecret(ctx, profiles.AnthropicKeySecret, key)
	if err != nil {
		// Verbatim: the control plane's own words are what an operator needs here — a
		// missing master key reads very differently from a network failure.
		return nil, connect.NewError(connect.CodeOf(err),
			fmt.Errorf("the key is valid but storing it failed: %w", err))
	}

	row := providerRow{
		KeyHint:       keyHint(key),
		SetBy:         login,
		SetAt:         time.Now().UTC(),
		SecretVersion: version,
	}
	if err := s.store.PutSetting(ctx, providerSettingKey, row); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a provider key was validated and stored",
		"provider", ProviderAnthropic, "login", login, "key_hint", row.KeyHint,
		"secret_version", version, "models", len(models))

	return connect.NewResponse(&agentv1.SetProviderKeyResponse{
		Provider: providerSettings(row, s.currentModel()),
		Models:   models,
		Status:   unusualFormat(key),
	}), nil
}

// providerKeyError is a SetProviderKey failure with the provider's own explanation attached
// as a Connect error detail.
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

// ClearProviderKey removes the secret and the metadata. The goal state is "no key", so a
// NotFound from the control plane is success: calling it twice is not an error.
func (s *AgentService) ClearProviderKey(
	ctx context.Context, req *connect.Request[agentv1.ClearProviderKeyRequest],
) (*connect.Response[agentv1.ClearProviderKeyResponse], error) {
	if p := req.Msg.GetProvider(); p != "" && p != ProviderAnthropic {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unsupported provider %q: this control plane knows %q", p, ProviderAnthropic))
	}
	if s.secrets == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no Podium API client, so it cannot remove a key"))
	}
	login := Login(ctx)
	if err := s.secrets.DeleteSecret(ctx, profiles.AnthropicKeySecret); err != nil &&
		connect.CodeOf(err) != connect.CodeNotFound {
		return nil, connect.NewError(connect.CodeOf(err), fmt.Errorf("removing the key failed: %w", err))
	}
	if err := s.store.DeleteSetting(ctx, providerSettingKey); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a provider key was removed", "provider", ProviderAnthropic, "login", login)
	return connect.NewResponse(&agentv1.ClearProviderKeyResponse{}), nil
}

// providerRow reads the metadata row. The second result is false when no key was ever set.
func (s *AgentService) providerRow(ctx context.Context) (providerRow, bool, error) {
	var row providerRow
	err := s.store.GetSetting(ctx, providerSettingKey, &row)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return providerRow{}, false, nil
	case err != nil:
		return providerRow{}, false, storeError(err)
	}
	return row, true, nil
}

// providerSettings is the row as the UI sees it, for the one caller that has just written it
// and therefore knows it is current.
func providerSettings(row providerRow, model string) *agentv1.ProviderSettings {
	out := &agentv1.ProviderSettings{
		Provider: ProviderAnthropic,
		KeySet:   true,
		KeyHint:  row.KeyHint,
		Model:    model,
		SetBy:    row.SetBy,
	}
	if !row.SetAt.IsZero() {
		out.SetAt = timestamppb.New(row.SetAt)
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
