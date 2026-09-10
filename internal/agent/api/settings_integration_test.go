//go:build integration

package api

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// postgresImage is pgvector's build of Postgres 16, as everywhere else under internal/agent.
const postgresImage = "pgvector/pgvector:pg16"

var (
	adminURL string
	dbSeq    atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("podium_agent"),
		postgres.WithUsername("podium"),
		postgres.WithPassword("podium"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres container: %v\n", err)
		os.Exit(1)
	}
	adminURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "postgres connection string: %v\n", err)
		_ = testcontainers.TerminateContainer(ctr)
		os.Exit(1)
	}
	code := m.Run()
	if err := testcontainers.TerminateContainer(ctr); err != nil {
		fmt.Fprintf(os.Stderr, "terminate postgres container: %v\n", err)
	}
	os.Exit(code)
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("podium_agent_api_%d", dbSeq.Add(1))

	conn, err := pgx.Connect(ctx, adminURL)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, fmt.Sprintf("create database %q", name))
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))

	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	u.Path = "/" + name

	st, err := store.New(ctx, u.String())
	require.NoError(t, err)
	t.Cleanup(st.Close)
	require.NoError(t, st.Migrate(ctx))
	return st
}

// setBehindTheUI is `podium secret set` happening without the conductor hearing: the value
// changes and the version bumps. It lives here rather than beside the rest of fakeSecrets
// because only the drift tests use it, and the unused linter runs without this build tag.
func (f *fakeSecrets) setBehindTheUI(name, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set[name] = []byte(value)
	f.versions[name]++
}

// removeBehindTheUI is `podium secret rm`, likewise.
func (f *fakeSecrets) removeBehindTheUI(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.set, name)
	delete(f.versions, name)
}

type settingsFixture struct {
	svc     *AgentService
	secrets *fakeSecrets
	log     *bytes.Buffer
}

func newSettingsFixture(t *testing.T, baseURL string, client *http.Client) settingsFixture {
	t.Helper()
	f := settingsFixture{secrets: newFakeSecrets(), log: &bytes.Buffer{}}
	f.svc = NewAgentService(AgentServiceOptions{
		Store:            newStore(t),
		Secrets:          f.secrets,
		Model:            "claude-opus-5",
		AnthropicBaseURL: baseURL,
		HTTPClient:       client,
		// Debug so the redaction assertion has everything to look at.
		Logger: slog.New(slog.NewTextHandler(f.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return f
}

func TestGetSettingsBeforeAnyKeyIsSet(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", http.DefaultClient)
	res, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	p := res.Msg.GetProvider()
	assert.Equal(t, ProviderAnthropic, p.GetProvider())
	assert.False(t, p.GetKeySet())
	assert.Empty(t, p.GetKeyHint())
	assert.Empty(t, p.GetSetBy())
	assert.Nil(t, p.GetSetAt())
	assert.Equal(t, "claude-opus-5", p.GetModel(), "the model is the profile's, reported for information")
}

func TestSetProviderKeyStoresTheSecretAndTheMetadata(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	f := newSettingsFixture(t, srv.URL, srv.Client())
	before := time.Now().Add(-time.Second)

	res, err := f.svc.SetProviderKey(loginCtx("alice@example.com"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Provider: ProviderAnthropic, Key: fakeKey}))
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-opus-5", "claude-sonnet-5"}, res.Msg.GetModels())
	assert.Empty(t, res.Msg.GetStatus(), "sk-ant- is the familiar prefix")

	p := res.Msg.GetProvider()
	assert.True(t, p.GetKeySet())
	assert.Equal(t, "abcd", p.GetKeyHint())
	assert.Equal(t, "alice@example.com", p.GetSetBy())
	assert.True(t, p.GetSetAt().AsTime().After(before))

	// The control plane got the key, as bytes, under the reserved name.
	assert.Equal(t, []byte(fakeKey), f.secrets.set[profiles.AnthropicKeySecret])

	// And GetSettings reports the same thing: the control plane confirms the secret at that
	// version, so the row's hint is about the key that is actually there.
	got, err := f.svc.GetSettings(loginCtx("bob"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.True(t, got.Msg.GetProvider().GetKeySet())
	assert.Equal(t, "abcd", got.Msg.GetProvider().GetKeyHint())
	assert.Equal(t, "alice@example.com", got.Msg.GetProvider().GetSetBy())
}

// The acceptance item, made real: everything logged around a successful SetProviderKey, at
// debug level, and not one byte sequence of the key in it.
func TestSetProviderKeyLogsNoByteOfTheKey(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	f := newSettingsFixture(t, srv.URL, srv.Client())
	_, err := f.svc.SetProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.NoError(t, err)

	out := f.log.String()
	require.NotEmpty(t, out, "the capture is empty, so it proves nothing")
	assert.NotContains(t, out, fakeKey)
	// Nor any run of the key long enough to be useful. The last four are deliberately
	// there — that is the stored hint — so the scan stops before them.
	for i := 0; i+8 <= len(fakeKey)-keyHintLen; i++ {
		assert.NotContains(t, out, fakeKey[i:i+8], "an 8-byte run of the key at offset %d", i)
	}
	assert.Contains(t, out, "[redacted]")
}

func TestSetProviderKeyAcceptsAnUnfamiliarPrefixAndSaysSo(t *testing.T) {
	const odd = "ant-api03-not-a-real-key-wxyz"
	srv := fakeAnthropic(t, odd)
	f := newSettingsFixture(t, srv.URL, srv.Client())

	res, err := f.svc.SetProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: odd}))
	require.NoError(t, err)
	assert.Contains(t, res.Msg.GetStatus(), "looks unusual")
	assert.Equal(t, "wxyz", res.Msg.GetProvider().GetKeyHint())
}

func TestSetProviderKeyTrimsWhatWasPasted(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	f := newSettingsFixture(t, srv.URL, srv.Client())
	_, err := f.svc.SetProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: "  \"" + fakeKey + "\"\n"}))
	require.NoError(t, err, "a key pasted out of a .env file must still work")
	assert.Equal(t, []byte(fakeKey), f.secrets.set[profiles.AnthropicKeySecret])
}

func TestSetProviderKeyWritesNothingWhenTheProviderIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	f := newSettingsFixture(t, srv.URL, srv.Client())

	_, err := f.svc.SetProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Empty(t, f.secrets.set)
	got, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetProvider().GetKeySet(), "nothing was saved")
}

func TestSetProviderKeyWritesNothingWhenTheProviderRefuses(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	f := newSettingsFixture(t, srv.URL, srv.Client())

	_, err := f.svc.SetProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: "sk-ant-someone-elses-key"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Empty(t, f.secrets.set)
	got, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetProvider().GetKeySet())
}

// A validated key that the control plane will not store must not leave a row claiming it is
// set: the metadata would be lying, and a turn would fail at admission with no explanation.
func TestSetProviderKeyLeavesNoRowWhenTheControlPlaneRefusesTheSecret(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	f := newSettingsFixture(t, srv.URL, srv.Client())
	f.secrets.setErr = connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("no master key configured"))

	_, err := f.svc.SetProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "no master key configured", "the server's own words reach the operator")

	got, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetProvider().GetKeySet())
}

// The drift the CLI can cause, and the reason key_set is not read out of the metadata row.
// `podium secret rm podium.agent.anthropic_api_key` is documented as the CLI equivalent of
// the UI's Remove, and the conductor does not hear about it — so a row left behind must not
// make the page say "Connected" while every turn fails at admission.
func TestGetSettingsFollowsTheControlPlaneNotItsOwnRow(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	f := newSettingsFixture(t, srv.URL, srv.Client())
	_, err := f.svc.SetProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.NoError(t, err)

	f.secrets.removeBehindTheUI(profiles.AnthropicKeySecret)

	got, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetProvider().GetKeySet(),
		"the secret is gone, so no key is set, whatever the row says")
	assert.Empty(t, got.Msg.GetProvider().GetKeyHint(),
		"a hint about a key that does not exist is worse than no hint")
}

// `podium secret set` behind the UI's back is the other half: the key exists, so turns will
// run, but the stored hint is about the key it replaced and must not be shown beside it.
func TestGetSettingsWithholdsAHintForAKeyItDidNotSet(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	f := newSettingsFixture(t, srv.URL, srv.Client())
	_, err := f.svc.SetProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.NoError(t, err)

	f.secrets.setBehindTheUI(profiles.AnthropicKeySecret, "sk-ant-a-different-fake-wxyz")

	got, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	p := got.Msg.GetProvider()
	assert.True(t, p.GetKeySet(), "a key is set; a turn will find one")
	assert.Empty(t, p.GetKeyHint(), "the hint describes the key that was replaced")
	assert.Empty(t, p.GetSetBy())
	assert.Nil(t, p.GetSetAt())
}

// A key set only with the CLI, on a conductor that has never seen the UI.
func TestGetSettingsReportsAKeyItHasNoRowFor(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", http.DefaultClient)
	f.secrets.setBehindTheUI(profiles.AnthropicKeySecret, fakeKey)

	got, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.True(t, got.Msg.GetProvider().GetKeySet())
	assert.Empty(t, got.Msg.GetProvider().GetKeyHint())
}

// A control plane that cannot be asked is not a broken page: the last known state is served
// and the log says it may be stale.
func TestGetSettingsFallsBackToTheRowWhenTheControlPlaneIsUnreachable(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	f := newSettingsFixture(t, srv.URL, srv.Client())
	_, err := f.svc.SetProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.NoError(t, err)

	f.secrets.listErr = connect.NewError(connect.CodeUnavailable, fmt.Errorf("control plane down"))
	got, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.True(t, got.Msg.GetProvider().GetKeySet())
	assert.Equal(t, "abcd", got.Msg.GetProvider().GetKeyHint())
	assert.Contains(t, f.log.String(), "could not confirm a provider credential")
}

// A control plane with no master key cannot hold a secret at all. That is "not set", not a
// failure: the real message arrives when the operator tries to save.
func TestGetSettingsTreatsNoMasterKeyAsNoKey(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", http.DefaultClient)
	f.secrets.listErr = connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("no master key configured"))
	got, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.False(t, got.Msg.GetProvider().GetKeySet())
}

func TestClearProviderKeyIsIdempotent(t *testing.T) {
	srv := fakeAnthropic(t, fakeKey)
	f := newSettingsFixture(t, srv.URL, srv.Client())
	_, err := f.svc.SetProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Key: fakeKey}))
	require.NoError(t, err)

	for i := range 2 {
		_, err := f.svc.ClearProviderKey(loginCtx("alice"),
			connect.NewRequest(&agentv1.ClearProviderKeyRequest{}))
		require.NoError(t, err, "call %d", i+1)
		got, err := f.svc.GetSettings(loginCtx("alice"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
		require.NoError(t, err)
		assert.False(t, got.Msg.GetProvider().GetKeySet())
		assert.Empty(t, got.Msg.GetProvider().GetKeyHint())
	}
	assert.Equal(t, []string{profiles.AnthropicKeySecret, profiles.AnthropicKeySecret}, f.secrets.deleted)
}

// "Already gone" is the goal state, so a NotFound from the control plane is success.
func TestClearProviderKeyTreatsAMissingSecretAsSuccess(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", http.DefaultClient)
	f.secrets.delErr = connect.NewError(connect.CodeNotFound, fmt.Errorf("no such secret"))
	_, err := f.svc.ClearProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.ClearProviderKeyRequest{}))
	require.NoError(t, err)
}

// Anything else from the control plane is not success: the key may still be there.
func TestClearProviderKeySurfacesARealFailure(t *testing.T) {
	f := newSettingsFixture(t, "https://example.invalid", http.DefaultClient)
	f.secrets.delErr = connect.NewError(connect.CodeUnavailable, fmt.Errorf("control plane down"))
	_, err := f.svc.ClearProviderKey(loginCtx("alice"),
		connect.NewRequest(&agentv1.ClearProviderKeyRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}
