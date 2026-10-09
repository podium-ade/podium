package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/connections"
	"github.com/podium-ade/podium/internal/agent/github"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// Connecting a person's own GitHub account, through the GitHub App's user authorization.
// The flow is the MCP sign-in's shape: the browser carries the code back to the web UI and
// hands it over the authenticated API, and the PKCE verifier never leaves this process. The
// token is stored as that person's personal secret github.token, so a playbook names it in
// user_secrets like any other personal secret.

const (
	defaultGitHubURL    = "https://github.com"
	defaultGitHubAPIURL = "https://api.github.com"
	githubCallbackPath  = "/agent/github/callback"
)

// githubFlow is one connection this conductor started and is waiting on.
type githubFlow struct {
	login        string
	clientID     string
	clientSecret string
	redirectURI  string
	verifier     string
	state        string
	expiresAt    time.Time
}

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

// GetGitHubAccount answers for the caller only.
func (s *AgentService) GetGitHubAccount(
	ctx context.Context, _ *connect.Request[agentv1.GetGitHubAccountRequest],
) (*connect.Response[agentv1.GetGitHubAccountResponse], error) {
	login, err := githubAccountOwner(ctx)
	if err != nil {
		return nil, err
	}
	account, err := s.githubAccountView(ctx, login)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.GetGitHubAccountResponse{Account: account}), nil
}

// StartGitHubOAuth answers with the URL to send the caller's browser to.
func (s *AgentService) StartGitHubOAuth(
	ctx context.Context, req *connect.Request[agentv1.StartGitHubOAuthRequest],
) (*connect.Response[agentv1.StartGitHubOAuthResponse], error) {
	login, err := githubAccountOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.secrets == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no secret store, so it cannot keep a GitHub token"))
	}
	redirectURI, err := validateCallbackURI(req.Msg.GetRedirectUri(), githubCallbackPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	app, err := s.githubApp(ctx)
	if err != nil {
		return nil, err
	}
	id, err := flowID()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	f := &githubFlow{
		login:        login,
		clientID:     app.ClientID,
		clientSecret: app.ClientSecret,
		redirectURI:  redirectURI,
		verifier:     randomToken(),
		state:        randomToken(),
		expiresAt:    time.Now().Add(flowTTL),
	}
	s.sweepGitHubFlows()
	s.putGitHubFlow(id, f)

	q := url.Values{
		"client_id":             {f.clientID},
		"redirect_uri":          {f.redirectURI},
		"state":                 {f.state},
		"code_challenge":        {challengeOf(f.verifier)},
		"code_challenge_method": {"S256"},
		"allow_signup":          {"false"},
	}
	s.logger.InfoContext(ctx, "a github account connection was started",
		"client_id", f.clientID, "redirect_uri", redirectURI, "login", login)
	return connect.NewResponse(&agentv1.StartGitHubOAuthResponse{
		FlowId:       id,
		AuthorizeUrl: s.githubURL + "/login/oauth/authorize?" + q.Encode(),
		State:        f.state,
		ExpiresAt:    timestamppb.New(f.expiresAt),
	}), nil
}

// CompleteGitHubOAuth trades the code for a user token and stores it under the person who
// started the flow, which must also be the person completing it.
func (s *AgentService) CompleteGitHubOAuth(
	ctx context.Context, req *connect.Request[agentv1.CompleteGitHubOAuthRequest],
) (*connect.Response[agentv1.CompleteGitHubOAuthResponse], error) {
	login, err := githubAccountOwner(ctx)
	if err != nil {
		return nil, err
	}
	id := req.Msg.GetFlowId()
	f, ok := s.githubFlow(id)
	if !ok || f.expired(time.Now()) || f.login != login {
		if ok && f.expired(time.Now()) {
			s.dropGitHubFlow(id)
		}
		return nil, connect.NewError(connect.CodeDeadlineExceeded,
			errors.New("this GitHub connection has expired or was never started; start it again"))
	}
	if subtle.ConstantTimeCompare([]byte(req.Msg.GetState()), []byte(f.state)) != 1 {
		s.logger.WarnContext(ctx, "a github callback carried the wrong state; nothing was exchanged",
			"login", login)
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("this callback does not belong to that GitHub connection"))
	}
	code := strings.TrimSpace(req.Msg.GetCode())
	if code == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("complete github oauth: code is required"))
	}
	// A code is single-use at GitHub, so the flow is too.
	s.dropGitHubFlow(id)

	callCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	tok, err := s.exchangeGitHubCode(callCtx, f, code)
	if err != nil {
		s.logger.WarnContext(ctx, "exchanging a github authorization code failed",
			"login", login, "code_len", len(code), "error", err)
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	if tok.ExpiresAt.IsZero() || tok.RefreshToken == "" {
		// A token that never expires is a leak that never ends. Refuse it, where someone can
		// fix the App.
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(
			"this GitHub App issues user tokens that never expire. An admin should turn on "+
				"\"User-to-server token expiration\" under the App's Optional features, then connect again"))
	}
	token, err := json.Marshal(tok)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	defer zero(token)
	user, err := s.githubUser(callCtx, tok.AccessToken)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.secrets.SetPersonalSecret(ctx, login, profiles.GitHubTokenSecret, token); err != nil {
		return nil, connect.NewError(connect.CodeOf(err),
			fmt.Errorf("GitHub connected but storing its token failed: %w", err))
	}
	if err := s.store.PutGitHubAccount(ctx, store.GitHubAccount{
		Login: login, GitHubID: user.ID, GitHubLogin: user.Login, Name: user.Name,
		ConnectedAt: time.Now().UTC(),
	}); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a github account was connected", "login", login,
		"github_login", user.Login, "github_id", user.ID)

	account, err := s.githubAccountView(ctx, login)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.CompleteGitHubOAuthResponse{Account: account}), nil
}

// DisconnectGitHubAccount deletes the caller's token and record. Doing it twice is not an
// error.
func (s *AgentService) DisconnectGitHubAccount(
	ctx context.Context, _ *connect.Request[agentv1.DisconnectGitHubAccountRequest],
) (*connect.Response[agentv1.DisconnectGitHubAccountResponse], error) {
	login, err := githubAccountOwner(ctx)
	if err != nil {
		return nil, err
	}
	if s.secrets == nil || s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no secret store or database"))
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.secrets.DeletePersonalSecret(ctx, login, profiles.GitHubTokenSecret); err != nil &&
		connect.CodeOf(err) != connect.CodeNotFound {
		return nil, connect.NewError(connect.CodeOf(err), err)
	}
	if err := s.store.DeleteGitHubAccount(ctx, login); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a github account was disconnected", "login", login)
	account, err := s.githubAccountView(ctx, login)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.DisconnectGitHubAccountResponse{Account: account}), nil
}

// githubAccountOwner is the signed-in person a connection belongs to. The dev token and
// the conductor are not a person and have no personal secrets.
func githubAccountOwner(ctx context.Context) (string, error) {
	login := mcpPersonalOwner(Login(ctx))
	if login == "" {
		return "", connect.NewError(connect.CodeFailedPrecondition,
			errors.New("only a signed-in person can connect a GitHub account"))
	}
	return login, nil
}

// githubApp is the saved App, refused unless it has an OAuth client.
func (s *AgentService) githubApp(ctx context.Context) (*connections.GitHub, error) {
	_, app, err := s.loadConnections(ctx)
	if err != nil {
		return nil, err
	}
	if !app.UserAuth() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(
			"the GitHub App has no client id and secret, so accounts cannot be connected. "+
				"An admin can add them under Settings → Connections"))
	}
	return app, nil
}

func (s *AgentService) githubAccountView(ctx context.Context, login string) (*agentv1.GitHubAccount, error) {
	_, app, err := s.loadConnections(ctx)
	if err != nil {
		return nil, err
	}
	out := &agentv1.GitHubAccount{Available: app.UserAuth()}
	row, err := s.store.GitHubAccount(ctx, login)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, storeError(err)
	}
	out.Connected = true
	out.NeedsReconnect = row.NeedsReconnect
	out.GithubLogin = row.GitHubLogin
	out.GithubId = row.GitHubID
	out.Name = row.Name
	out.ConnectedAt = timestamppb.New(row.ConnectedAt)
	return out, nil
}

func (s *AgentService) exchangeGitHubCode(ctx context.Context, f *githubFlow, code string) (github.UserToken, error) {
	form := url.Values{
		"client_id":     {f.clientID},
		"client_secret": {f.clientSecret},
		"code":          {code},
		"redirect_uri":  {f.redirectURI},
		"code_verifier": {f.verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.githubURL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return github.UserToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		return github.UserToken{}, fmt.Errorf("GitHub could not be reached: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	return github.ParseUserToken(res, time.Now())
}

func (s *AgentService) githubUser(ctx context.Context, token string) (githubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.githubAPIURL+"/user", nil)
	if err != nil {
		return githubUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := s.http.Do(req)
	if err != nil {
		return githubUser{}, fmt.Errorf("GitHub could not be reached: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return githubUser{}, fmt.Errorf("GitHub answered %s when asked who the token belongs to", res.Status)
	}
	var u githubUser
	if err := json.NewDecoder(io.LimitReader(res.Body, discoveryBodyLimit)).Decode(&u); err != nil {
		return githubUser{}, fmt.Errorf("decoding the GitHub user: %w", err)
	}
	if u.ID == 0 || u.Login == "" {
		return githubUser{}, errors.New("GitHub did not say who the token belongs to")
	}
	return u, nil
}

func (f *githubFlow) expired(now time.Time) bool { return now.After(f.expiresAt) }

func (s *AgentService) putGitHubFlow(id string, f *githubFlow) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	if s.githubFlows == nil {
		s.githubFlows = map[string]*githubFlow{}
	}
	s.githubFlows[id] = f
}

func (s *AgentService) githubFlow(id string) (*githubFlow, bool) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	f, ok := s.githubFlows[id]
	return f, ok
}

func (s *AgentService) dropGitHubFlow(id string) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	delete(s.githubFlows, id)
}

func (s *AgentService) sweepGitHubFlows() {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	now := time.Now()
	for id, f := range s.githubFlows {
		if f.expired(now) {
			delete(s.githubFlows, id)
		}
	}
}
