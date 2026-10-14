package ghreview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestALargePullStillYieldsItsHead(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/podium-ade/podium/pulls/174", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"body": strings.Repeat("n", restErrorBody),
			"head": map[string]any{
				"sha":  sha,
				"repo": map[string]any{"name": strings.Repeat("r", restErrorBody)},
			},
		})
	}))
	t.Cleanup(srv.Close)

	got, err := testRest(t, srv.URL).PullHeadSHA(context.Background(), 7, "podium-ade", "podium", 174)
	require.NoError(t, err)
	require.Equal(t, sha, got)
}

func TestALongCommentPageStillDecodes(t *testing.T) {
	body := strings.Repeat("c", restErrorBody+200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Comment{{ID: 9, Body: body, User: User{Login: "ada"}}})
	}))
	t.Cleanup(srv.Close)

	got, err := testRest(t, srv.URL).ListIssueComments(context.Background(), 7, "podium-ade", "podium", 174)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, body, got[0].Body)
}

func TestAnErrorPageStaysShort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, strings.Repeat("e", restErrorBody+80), http.StatusUnprocessableEntity)
	}))
	t.Cleanup(srv.Close)

	err := testRest(t, srv.URL).do(context.Background(), 7, http.MethodGet, "/repos/o/r/pulls/1", nil, nil)
	var api *APIError
	require.ErrorAs(t, err, &api)
	require.Equal(t, http.StatusUnprocessableEntity, api.Status)
	require.LessOrEqual(t, len(api.Body), restErrorBody+len("…"))
	require.True(t, strings.HasSuffix(api.Body, "…"))
}

func testRest(t *testing.T, base string) *restClient {
	t.Helper()
	c, err := newRest(restOptions{BaseURL: base, AppID: "1", PrivateKey: testKey(t)})
	require.NoError(t, err)
	c.tokens[7] = cachedToken{token: "ghs_test", expires: time.Now().Add(time.Hour)}
	return c
}
