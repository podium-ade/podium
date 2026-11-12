//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// switchableAnthropic is fakeAnthropic whose good key can be revoked: what the console does
// to a key, or what an expiry date does to it, while the conductor is running.
type switchableAnthropic struct {
	srv   *httptest.Server
	good  atomic.Value // string
	calls atomic.Int32
}

func newSwitchableAnthropic(t *testing.T, good string) *switchableAnthropic {
	t.Helper()
	a := &switchableAnthropic{}
	a.good.Store(good)
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("x-api-key") != a.good.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":` +
				strconv.Quote("invalid x-api-key") + `}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5"}],"has_more":false}`))
	}))
	t.Cleanup(a.srv.Close)
	return a
}

type alerted struct{ login, subject, body string }

type alertLog struct {
	mu  sync.Mutex
	got []alerted
}

func (l *alertLog) alert(_ context.Context, login, subject, body string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, alerted{login, subject, body})
	return nil
}

func (l *alertLog) all() []alerted {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]alerted(nil), l.got...)
}

func TestARevokedKeyIsNoticedRecordedAndToldOnce(t *testing.T) {
	ctx := context.Background()
	anth := newSwitchableAnthropic(t, fakeKey)
	f := newSettingsFixture(t, anth.srv.URL, anth.srv.Client())
	alerts := &alertLog{}
	f.svc.SetKeyAlert(alerts.alert)

	_, err := f.svc.SetProviderKey(adminCtx("alice@example.com"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Global: true, Provider: ProviderAnthropic, Key: fakeKey}))
	require.NoError(t, err)

	// The first pass checks a key it has never checked, and finds it working.
	f.svc.checkKeysOnce(ctx)
	refused, err := f.svc.KeyRefused(ctx, ProviderAnthropic)
	require.NoError(t, err)
	assert.False(t, refused)
	got, err := f.svc.GetSettings(adminCtx("bob"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.NotNil(t, got.Msg.GetProvider().GetCheckedAt())
	assert.Nil(t, got.Msg.GetProvider().GetRefusedAt())

	// Within the hour, the pass leaves the provider alone.
	calls := anth.calls.Load()
	f.svc.checkKeysOnce(ctx)
	assert.Equal(t, calls, anth.calls.Load(), "a key checked minutes ago is not asked about again")

	// The key is revoked. A turn refused on it asks for a check, which does not wait out
	// the hour.
	anth.good.Store("sk-ant-some-other-key")
	f.svc.checkKey(ctx, globalTarget(providerSpecs[0]), 0)
	refused, err = f.svc.KeyRefused(ctx, ProviderAnthropic)
	require.NoError(t, err)
	assert.True(t, refused)

	got, err = f.svc.GetSettings(adminCtx("bob"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	p := got.Msg.GetProvider()
	require.NotNil(t, p.GetRefusedAt())
	assert.Equal(t, "invalid x-api-key", p.GetRefusal())
	assert.True(t, p.GetKeySet(), "a refused key is still the stored key")

	// The person who saved it is told, once, with no byte of the key but its hint.
	sent := alerts.all()
	require.Len(t, sent, 1)
	assert.Equal(t, "alice@example.com", sent[0].login)
	assert.Contains(t, sent[0].subject, "Anthropic API key stopped working")
	assert.Contains(t, sent[0].body, "…abcd")
	assert.Contains(t, sent[0].body, "invalid x-api-key")
	assert.NotContains(t, sent[0].body, fakeKey)

	first := p.GetRefusedAt().AsTime()
	f.svc.checkKey(ctx, globalTarget(providerSpecs[0]), 0)
	assert.Len(t, alerts.all(), 1, "a key still refused is not news")
	got, err = f.svc.GetSettings(adminCtx("bob"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.Equal(t, first, got.Msg.GetProvider().GetRefusedAt().AsTime(), "refused_at is when it started")

	// Saving a working key clears it.
	const fresh = "sk-ant-not-a-real-key-1111wxyz"
	anth.good.Store(fresh)
	_, err = f.svc.SetProviderKey(adminCtx("alice@example.com"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Global: true, Provider: ProviderAnthropic, Key: fresh}))
	require.NoError(t, err)
	refused, err = f.svc.KeyRefused(ctx, ProviderAnthropic)
	require.NoError(t, err)
	assert.False(t, refused)
}

// A key replaced with `podium secret set` never touched the row, so the row's refusal is
// about a key that is gone and is not believed.
func TestARefusalIsOnlyAboutTheKeyItWasRecordedFor(t *testing.T) {
	ctx := context.Background()
	anth := newSwitchableAnthropic(t, fakeKey)
	f := newSettingsFixture(t, anth.srv.URL, anth.srv.Client())
	_, err := f.svc.SetProviderKey(adminCtx("alice@example.com"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Global: true, Provider: ProviderAnthropic, Key: fakeKey}))
	require.NoError(t, err)

	anth.good.Store("sk-ant-replacement")
	f.svc.checkKey(ctx, globalTarget(providerSpecs[0]), 0)
	refused, err := f.svc.KeyRefused(ctx, ProviderAnthropic)
	require.NoError(t, err)
	require.True(t, refused)

	f.secrets.setBehindTheUI(profiles.AnthropicKeySecret, "sk-ant-replacement")
	refused, err = f.svc.KeyRefused(ctx, ProviderAnthropic)
	require.NoError(t, err)
	assert.False(t, refused)
}

// A provider that cannot be reached says nothing about the key: no verdict is recorded, and
// nobody is told anything.
func TestAnUnreachableProviderRecordsNothing(t *testing.T) {
	ctx := context.Background()
	anth := newSwitchableAnthropic(t, fakeKey)
	f := newSettingsFixture(t, anth.srv.URL, anth.srv.Client())
	alerts := &alertLog{}
	f.svc.SetKeyAlert(alerts.alert)
	_, err := f.svc.SetProviderKey(adminCtx("alice@example.com"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Global: true, Provider: ProviderAnthropic, Key: fakeKey}))
	require.NoError(t, err)

	anth.srv.Close()
	f.svc.checkKey(ctx, globalTarget(providerSpecs[0]), 0)
	refused, err := f.svc.KeyRefused(ctx, ProviderAnthropic)
	require.NoError(t, err)
	assert.False(t, refused)
	assert.Empty(t, alerts.all())
	got, err := f.svc.GetSettings(adminCtx("bob"), connect.NewRequest(&agentv1.GetSettingsRequest{}))
	require.NoError(t, err)
	assert.Nil(t, got.Msg.GetProvider().GetCheckedAt())
}

// SuspectKey runs the check on its own and returns at once; a burst of refused turns is
// one check.
func TestSuspectKeyChecksNowAndOnce(t *testing.T) {
	ctx := context.Background()
	anth := newSwitchableAnthropic(t, fakeKey)
	f := newSettingsFixture(t, anth.srv.URL, anth.srv.Client())
	_, err := f.svc.SetProviderKey(adminCtx("alice@example.com"),
		connect.NewRequest(&agentv1.SetProviderKeyRequest{Global: true, Provider: ProviderAnthropic, Key: fakeKey}))
	require.NoError(t, err)
	anth.good.Store("sk-ant-other")

	before := anth.calls.Load()
	f.svc.SuspectKey(ProviderAnthropic)
	require.Eventually(t, func() bool {
		refused, err := f.svc.KeyRefused(ctx, ProviderAnthropic)
		return err == nil && refused
	}, 10*time.Second, 20*time.Millisecond)
	calls := anth.calls.Load()
	assert.Equal(t, before+1, calls)

	// Inside keySuspectFloor of that check, another suspicion asks the provider nothing.
	f.svc.SuspectKey(ProviderAnthropic)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, calls, anth.calls.Load())
}
