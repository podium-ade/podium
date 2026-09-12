package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/github"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/internal/proto/podium/agent/v1/agentv1connect"
)

// fakeMinter is the conductor, scripted. It records the capability it was given, because
// "which turn asked" is the whole of this service's authorisation.
type fakeMinter struct {
	seen []string
	cred conductor.GitCredential
	err  error
}

func (f *fakeMinter) MintGitToken(_ context.Context, capability string) (conductor.GitCredential, error) {
	f.seen = append(f.seen, capability)
	if f.err != nil {
		return conductor.GitCredential{}, f.err
	}
	return f.cred, nil
}

func mintRequest(capability string) *connect.Request[agentv1.MintTokenRequest] {
	req := connect.NewRequest(&agentv1.MintTokenRequest{})
	if capability != "" {
		req.Header().Set(TurnTokenHeader, capability)
	}
	return req
}

func TestMintToken(t *testing.T) {
	expires := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	credential := conductor.GitCredential{
		Token:     "ghs_minted",
		Username:  conductor.TokenUsername,
		ExpiresAt: expires,
		Identity: github.Identity{
			Name:  "podium-agent[bot]",
			Email: "987654+podium-agent[bot]@users.noreply.github.com",
		},
	}

	t.Run("returns the token and the identity commits should carry", func(t *testing.T) {
		minter := &fakeMinter{cred: credential}
		svc := NewGitCredentialService(minter, nil)

		res, err := svc.MintToken(t.Context(), mintRequest("turn_01abc.eyJ9.abcd"))
		require.NoError(t, err)
		assert.Equal(t, "ghs_minted", res.Msg.GetToken())
		assert.Equal(t, conductor.TokenUsername, res.Msg.GetUsername())
		assert.Equal(t, expires, res.Msg.GetExpiresAt().AsTime())
		assert.Equal(t, "podium-agent[bot]", res.Msg.GetAuthorName())
		assert.Equal(t, "987654+podium-agent[bot]@users.noreply.github.com", res.Msg.GetAuthorEmail())
	})

	// The capability rides in a header and never in the message: a credential in a request
	// body is a credential in every log that ever prints a request.
	t.Run("takes the capability from the header", func(t *testing.T) {
		minter := &fakeMinter{cred: credential}
		svc := NewGitCredentialService(minter, nil)

		_, err := svc.MintToken(t.Context(), mintRequest("  turn_01abc.eyJ9.abcd  "))
		require.NoError(t, err)
		assert.Equal(t, []string{"turn_01abc.eyJ9.abcd"}, minter.seen)
	})

	t.Run("refuses a call with no capability at all", func(t *testing.T) {
		minter := &fakeMinter{cred: credential}
		svc := NewGitCredentialService(minter, nil)

		_, err := svc.MintToken(t.Context(), mintRequest(""))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		assert.Empty(t, minter.seen, "an unauthenticated call never reaches the conductor")
	})

	// Forged, malformed and expired are one answer on purpose.
	t.Run("answers permission denied for a capability that is not valid", func(t *testing.T) {
		svc := NewGitCredentialService(&fakeMinter{err: conductor.ErrBadCapability}, nil)

		_, err := svc.MintToken(t.Context(), mintRequest("forged"))
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("says so rather than crashing when there is no app", func(t *testing.T) {
		svc := NewGitCredentialService(nil, nil)

		_, err := svc.MintToken(t.Context(), mintRequest("turn_01abc.eyJ9.abcd"))
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	})

	// GitHub being down is retryable and a turn can act on that, so it is Unavailable
	// rather than Internal.
	t.Run("passes a GitHub failure back as retryable", func(t *testing.T) {
		svc := NewGitCredentialService(&fakeMinter{err: errors.New("github: 502 Bad Gateway")}, nil)

		_, err := svc.MintToken(t.Context(), mintRequest("turn_01abc.eyJ9.abcd"))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		assert.Contains(t, err.Error(), "502")
	})
}

// The caller is a hand-written client — agent/runtime/src/gitcred.ts parses this with
// JSON.parse and no generated stub — so the field names on the wire are a contract. This
// test is what stops them changing silently.
func TestMintTokenWireShape(t *testing.T) {
	svc := NewGitCredentialService(&fakeMinter{cred: conductor.GitCredential{
		Token:     "ghs_minted",
		Username:  conductor.TokenUsername,
		ExpiresAt: time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC),
		Identity: github.Identity{
			Name:  "podium-agent[bot]",
			Email: "987654+podium-agent[bot]@users.noreply.github.com",
		},
	}}, nil)

	path, handler := agentv1connect.NewGitCredentialServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		srv.URL+"/"+agentv1connect.GitCredentialServiceName+"/MintToken", strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(TurnTokenHeader, "turn_01abc.eyJ9.abcd")

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	assert.Equal(t, "ghs_minted", body["token"])
	assert.Equal(t, conductor.TokenUsername, body["username"])
	assert.Equal(t, "podium-agent[bot]", body["authorName"])
	assert.Equal(t, "987654+podium-agent[bot]@users.noreply.github.com", body["authorEmail"])
	assert.Equal(t, "2026-09-09T13:00:00Z", body["expiresAt"])
}
