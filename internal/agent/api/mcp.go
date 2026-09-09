package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alvaroibarguen/podium/internal/agent/mcp"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// The MCP server registry, as an API.
//
// There is one source, unlike playbooks and Agent Skills: a server is an address and a
// credential rather than a document anybody would keep in version control, so there is no
// file half on the conductor's host to shadow a row or be shadowed by one.
//
// A registration is not a grant. Registering `linear` here says this conductor CAN reach
// Linear; a playbook's mcp_servers list is what says which turns do. That split is the whole
// security model — see docs/security.md — and it is why the listing reports which playbooks
// name each server: an operator about to store a write-capable token should be able to see
// where it will end up.
//
// No handler here ever reads a token back. SetMcpServerToken writes it to the control plane's
// encrypted store and keeps four characters; everything else works from the metadata.

// ListMcpServers reports every registered server.
func (s *AgentService) ListMcpServers(
	ctx context.Context, _ *connect.Request[agentv1.ListMcpServersRequest],
) (*connect.Response[agentv1.ListMcpServersResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no database"))
	}
	rows, err := s.store.ListMcpServers(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	users := s.playbooksByMcpServer()
	out := make([]*agentv1.McpServer, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.mcpServerToProto(ctx, row, users))
	}
	return connect.NewResponse(&agentv1.ListMcpServersResponse{
		Servers:        out,
		MaxPerPlaybook: int32(mcp.MaxServers),
	}), nil
}

// CreateMcpServer registers a new server, and stores its token when one came with it.
//
// The order is the registration first and the token second, which is the opposite of
// SetProviderKey's. A provider key is validated with the provider before anything is
// written; an MCP server cannot be asked whether it is there without also being handed the
// credential, so there is nothing to validate against and no reason to write the secret
// before the row that owns it.
func (s *AgentService) CreateMcpServer(
	ctx context.Context, req *connect.Request[agentv1.CreateMcpServerRequest],
) (*connect.Response[agentv1.CreateMcpServerResponse], error) {
	srv, err := mcpServerFromProto(req.Msg.GetServer())
	if err != nil {
		return nil, err
	}
	if err := s.requireMcpStore(); err != nil {
		return nil, err
	}
	login := Login(ctx)
	s.logger.InfoContext(ctx, "registering an mcp server",
		"request", redactedMcpTokenRequest{name: srv.Name, token: req.Msg.GetToken()}, "login", login)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.store.InsertMcpServer(ctx, srv, login); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("an MCP server named %q is already registered", srv.Name))
		}
		return nil, storeError(err)
	}
	if token := trimPastedKey(req.Msg.GetToken()); token != "" {
		if err := s.storeMcpToken(ctx, srv.Name, []byte(token), login); err != nil {
			// The row is already in. Rolling it back would be the wrong repair: the
			// registration is what the operator asked for and it is correct, and a server
			// with no token is a state the model supports. They are told what failed and
			// the token button is where they retry.
			return nil, err
		}
	}
	return connect.NewResponse(&agentv1.CreateMcpServerResponse{
		Server: s.readMcpServer(ctx, srv.Name),
	}), nil
}

// UpdateMcpServer replaces a registration. The token is untouched: it belongs to the name,
// and the name is what cannot be edited here.
func (s *AgentService) UpdateMcpServer(
	ctx context.Context, req *connect.Request[agentv1.UpdateMcpServerRequest],
) (*connect.Response[agentv1.UpdateMcpServerResponse], error) {
	srv, err := mcpServerFromProto(req.Msg.GetServer())
	if err != nil {
		return nil, err
	}
	if err := s.requireMcpStore(); err != nil {
		return nil, err
	}
	login := Login(ctx)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.store.UpdateMcpServer(ctx, srv, login); err != nil {
		return nil, s.mcpNotFound(err, srv.Name)
	}
	s.logger.InfoContext(ctx, "an mcp server was updated", "mcp_server", srv.Name,
		"url", srv.URL, "enabled", srv.Enabled, "playbooks", s.playbooksByMcpServer()[srv.Name],
		"login", login)
	return connect.NewResponse(&agentv1.UpdateMcpServerResponse{
		Server: s.readMcpServer(ctx, srv.Name),
	}), nil
}

// DeleteMcpServer removes a registration and the token with it.
//
// A server a playbook still names is deletable, exactly as a skill is: profiles deliberately
// does not check that a named server exists — a playbook file has to load on a machine with
// no database — so refusing here would be the only place in Podium where the two disagreed.
// The playbook's turns then fail naming the server, and ListMcpServers reports which
// playbooks name each one so a human can see that before pressing the button.
//
// The secret goes first. A row that is gone with a credential left behind it is a credential
// nothing in the UI can see, name or remove.
func (s *AgentService) DeleteMcpServer(
	ctx context.Context, req *connect.Request[agentv1.DeleteMcpServerRequest],
) (*connect.Response[agentv1.DeleteMcpServerResponse], error) {
	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("delete mcp server: name is required"))
	}
	if err := s.requireMcpStore(); err != nil {
		return nil, err
	}
	login := Login(ctx)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	users := s.playbooksByMcpServer()
	if err := s.deleteMcpToken(ctx, name); err != nil {
		return nil, err
	}
	if err := s.store.DeleteMcpServer(ctx, name); err != nil {
		return nil, s.mcpNotFound(err, name)
	}
	s.logger.InfoContext(ctx, "an mcp server was deleted", "mcp_server", name,
		"playbooks", users[name], "login", login)
	return connect.NewResponse(&agentv1.DeleteMcpServerResponse{}), nil
}

// SetMcpServerToken stores the credential a server authenticates with.
//
// It is not validated, because there is no way to validate it. An MCP server has no
// unauthenticated "are you there" call, so the only test of a token is a turn using it —
// which is why the failure an operator sees for a bad token is a turn's, and why the hint
// exists at all.
func (s *AgentService) SetMcpServerToken(
	ctx context.Context, req *connect.Request[agentv1.SetMcpServerTokenRequest],
) (*connect.Response[agentv1.SetMcpServerTokenResponse], error) {
	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("set mcp server token: name is required"))
	}
	if err := s.requireMcpStore(); err != nil {
		return nil, err
	}
	// One []byte, zeroed on the way out. The proto's string still exists in this process's
	// memory until the request is collected — that is what a proto string costs — but
	// nothing downstream of here ever holds a second copy.
	token := []byte(trimPastedKey(req.Msg.GetToken()))
	defer zero(token)
	if len(token) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("the token is empty; use ClearMcpServerToken to remove one"))
	}
	login := Login(ctx)
	// Logged through a redacting wrapper. Logging req.Msg directly would print the token:
	// protobuf's String() does not know what a secret is.
	s.logger.InfoContext(ctx, "setting an mcp server token",
		"request", redactedMcpTokenRequest{name: name, token: req.Msg.GetToken()}, "login", login)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Read first so a token is never written for a name nobody registered: the secret would
	// then be a credential with no row to describe it or delete it.
	if _, err := s.store.McpServer(ctx, name); err != nil {
		return nil, s.mcpNotFound(err, name)
	}
	if err := s.storeMcpToken(ctx, name, token, login); err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.SetMcpServerTokenResponse{
		Server: s.readMcpServer(ctx, name),
	}), nil
}

// ClearMcpServerToken removes the credential and leaves the registration. The goal state is
// "no token", so a NotFound from the control plane is success: calling it twice is not an
// error.
func (s *AgentService) ClearMcpServerToken(
	ctx context.Context, req *connect.Request[agentv1.ClearMcpServerTokenRequest],
) (*connect.Response[agentv1.ClearMcpServerTokenResponse], error) {
	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("clear mcp server token: name is required"))
	}
	if err := s.requireMcpStore(); err != nil {
		return nil, err
	}
	login := Login(ctx)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.deleteMcpToken(ctx, name); err != nil {
		return nil, err
	}
	if err := s.store.ClearMcpServerTokenMeta(ctx, name, login); err != nil {
		return nil, s.mcpNotFound(err, name)
	}
	s.logger.InfoContext(ctx, "an mcp server token was removed", "mcp_server", name, "login", login)
	return connect.NewResponse(&agentv1.ClearMcpServerTokenResponse{
		Server: s.readMcpServer(ctx, name),
	}), nil
}

// StartMcpOAuth discovers a server's OAuth, registers this conductor as a client of it, and
// answers with the URL to send the operator's browser to.
//
// Everything expensive happens here rather than on the callback, so that a server which
// advertises no OAuth, or refuses a registration, is a refusal the operator gets while
// looking at the button they pressed — not a dead end after a round trip through a consent
// screen.
func (s *AgentService) StartMcpOAuth(
	ctx context.Context, req *connect.Request[agentv1.StartMcpOAuthRequest],
) (*connect.Response[agentv1.StartMcpOAuthResponse], error) {
	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("start mcp oauth: name is required"))
	}
	if err := s.requireMcpStore(); err != nil {
		return nil, err
	}
	redirectURI, err := validateRedirectURI(req.Msg.GetRedirectUri())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	row, err := s.store.McpServer(ctx, name)
	if err != nil {
		return nil, s.mcpNotFound(err, name)
	}

	login := Login(ctx)
	discoverCtx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()

	as, err := s.discoverMcpAuth(discoverCtx, row.URL)
	if err != nil {
		// The server's own answer, or the lack of one. It is the whole of what an operator
		// can act on, and it came from the server over TLS rather than from a task
		// container — so it is shown rather than withheld.
		s.logger.WarnContext(ctx, "an mcp server's oauth could not be discovered",
			"mcp_server", name, "url", row.URL, "login", login, "error", err)
		code := connect.CodeFailedPrecondition
		if errors.Is(err, errNoAuthServer) {
			code = connect.CodeUnimplemented
		}
		return nil, connect.NewError(code, err)
	}

	scope := strings.TrimSpace(req.Msg.GetScope())
	if scope == "" {
		scope = as.scope
	}
	reg, err := s.registerMcpClient(discoverCtx, as.meta, redirectURI, scope)
	if err != nil {
		s.logger.WarnContext(ctx, "registering as a client of an mcp server's authorization "+
			"server failed", "mcp_server", name, "issuer", as.meta.Issuer, "login", login,
			"error", err)
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	id, err := flowID()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	f := &mcpFlow{
		name:          name,
		issuer:        as.meta.Issuer,
		tokenEndpoint: as.meta.TokenEndpoint,
		clientID:      reg.ClientID,
		clientSecret:  reg.ClientSecret,
		redirectURI:   redirectURI,
		scope:         scope,
		resource:      as.resource,
		verifier:      randomToken(),
		state:         randomToken(),
		expiresAt:     time.Now().Add(flowTTL),
		startedBy:     login,
	}
	s.sweepMcpFlows()
	s.putMcpFlow(id, f)

	s.logger.InfoContext(ctx, "an mcp server sign-in was started", "mcp_server", name,
		"issuer", f.issuer, "client_id", f.clientID, "scope", scope,
		"redirect_uri", redirectURI, "login", login)

	return connect.NewResponse(&agentv1.StartMcpOAuthResponse{
		FlowId:       id,
		AuthorizeUrl: authorizeURL(as.meta, f.clientID, redirectURI, scope, f.state, f.verifier, f.resource),
		State:        f.state,
		Issuer:       f.issuer,
		Scope:        scope,
		ExpiresAt:    timestamppb.New(f.expiresAt),
	}), nil
}

// CompleteMcpOAuth trades the code for a token and stores it as the server's credential.
//
// From here on there is no difference between a sign-in and a pasted token: the access token
// goes into the same Podium secret, so the brief, the task spec and the runtime cannot tell
// them apart. What the sign-in leaves extra is the bag that lets it be refreshed.
func (s *AgentService) CompleteMcpOAuth(
	ctx context.Context, req *connect.Request[agentv1.CompleteMcpOAuthRequest],
) (*connect.Response[agentv1.CompleteMcpOAuthResponse], error) {
	id := req.Msg.GetFlowId()
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("complete mcp oauth: flow_id is required"))
	}
	if err := s.requireMcpStore(); err != nil {
		return nil, err
	}
	f, ok := s.mcpFlow(id)
	if !ok || f.expired(time.Now()) {
		if ok {
			s.dropMcpFlow(id)
		}
		return nil, connect.NewError(connect.CodeDeadlineExceeded,
			errors.New("this sign-in has expired or was never started; start it again"))
	}
	// Constant time, and it is the comparison that counts: the browser's copy of the state
	// is a convenience for telling one tab from another, and this is the check that a
	// callback belongs to the flow it claims to.
	if subtle.ConstantTimeCompare([]byte(req.Msg.GetState()), []byte(f.state)) != 1 {
		s.logger.WarnContext(ctx, "an mcp sign-in callback carried the wrong state; nothing "+
			"was exchanged", "mcp_server", f.name, "login", Login(ctx))
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("this callback does not belong to that sign-in"))
	}
	code := strings.TrimSpace(req.Msg.GetCode())
	if code == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("complete mcp oauth: code is required"))
	}

	login := Login(ctx)
	// The code is a credential for as long as it is unredeemed. It is logged as a length,
	// like every other one on this path.
	s.logger.InfoContext(ctx, "completing an mcp server sign-in", "mcp_server", f.name,
		"issuer", f.issuer, "code_len", len(code), "login", login)

	// One use, whatever happens next: an authorization code is single-use at the
	// authorization server, so a flow that has been exchanged must not be exchangeable
	// again — including after a failure, where a retry would only ever be refused.
	s.dropMcpFlow(id)

	exchangeCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	tok, err := s.exchangeMcpCode(exchangeCtx, f, code)
	if err != nil {
		s.logger.WarnContext(ctx, "exchanging an mcp authorization code failed",
			"mcp_server", f.name, "issuer", f.issuer, "login", login, "error", err)
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}

	o := mcpOAuthOf(f, tok)
	token := []byte(tok.AccessToken)
	defer zero(token)
	version, err := s.secrets.SetSecret(ctx, mcp.TokenSecret(f.name), token)
	if err != nil {
		return nil, connect.NewError(connect.CodeOf(err),
			fmt.Errorf("the sign-in worked but storing its token failed: %w", err))
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.store.SetMcpServerOAuth(ctx, f.name, login, version, o); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "an mcp server was signed in to", "mcp_server", f.name,
		"issuer", f.issuer, "account", o.Account, "scope", o.Scope,
		"expires_at", o.ExpiresAt, "refreshable", o.RefreshToken != "",
		"secret_version", version, "login", login)

	return connect.NewResponse(&agentv1.CompleteMcpOAuthResponse{
		Server: s.readMcpServer(ctx, f.name),
	}), nil
}

// RefreshMcpTokens keeps signed-in MCP servers alive, on the same schedule and with the same
// rules as the subscription sign-in's refresh: a failure is logged and retried, and the
// stored token is left alone — one with thirty minutes left on it is more use than none.
func (s *AgentService) refreshMcpOnce(ctx context.Context) {
	s.sweepMcpFlows()
	if s.store == nil || s.secrets == nil {
		return
	}
	rows, err := s.store.ListMcpServers(ctx)
	if err != nil {
		s.logger.WarnContext(ctx, "reading the mcp registry for a token refresh failed", "error", err)
		return
	}
	for _, row := range rows {
		if !row.Refreshable() {
			continue
		}
		o := *row.OAuth
		if !o.ExpiresAt.IsZero() && time.Until(o.ExpiresAt) > refreshLead {
			continue
		}

		callCtx, cancel := context.WithTimeout(ctx, validateTimeout)
		tok, err := s.refreshMcpToken(callCtx, o)
		cancel()
		if err != nil {
			s.logger.WarnContext(ctx, "refreshing an mcp server's token failed; the stored one "+
				"is kept and this will be tried again. Sign in again if turns start failing",
				"mcp_server", row.Name, "issuer", o.Issuer, "expires_at", o.ExpiresAt, "error", err)
			continue
		}

		next := o
		next.ExpiresAt = expiryOf(tok)
		if tok.RefreshToken != "" {
			// A server that rotates refresh tokens invalidates the old one, so keeping the
			// old one would mean the next refresh fails.
			next.RefreshToken = tok.RefreshToken
		}
		if tok.Scope != "" {
			next.Scope = tok.Scope
		}
		if a := account(tok); a != "" {
			next.Account = a
		}

		token := []byte(tok.AccessToken)
		version, err := s.secrets.SetSecret(ctx, mcp.TokenSecret(row.Name), token)
		if err != nil {
			s.logger.ErrorContext(ctx, "an mcp token was refreshed but could not be stored; "+
				"turns will keep using the previous one until it expires",
				"mcp_server", row.Name, "error", err)
			zero(token)
			continue
		}
		if err := s.store.RefreshMcpServerOAuth(ctx, row.Name, version, next); err != nil {
			s.logger.ErrorContext(ctx, "an mcp token was refreshed and stored but the row "+
				"could not be updated; the next refresh will use the previous refresh token",
				"mcp_server", row.Name, "error", err)
		} else {
			s.logger.InfoContext(ctx, "an mcp server's token was refreshed",
				"mcp_server", row.Name, "expires_at", next.ExpiresAt)
		}
		zero(token)
	}
}

// storeMcpToken writes the secret and then the metadata that describes it, in that order:
// a row claiming a token the control plane does not hold is the one state an operator
// cannot diagnose, because the UI would say "connected" and every turn would disagree.
func (s *AgentService) storeMcpToken(ctx context.Context, name string, token []byte, login string) error {
	version, err := s.secrets.SetSecret(ctx, mcp.TokenSecret(name), token)
	if err != nil {
		// Verbatim: the control plane's own words are what an operator needs here — a
		// missing master key reads very differently from a network failure.
		return connect.NewError(connect.CodeOf(err),
			fmt.Errorf("the MCP server is registered but storing its token failed: %w", err))
	}
	if err := s.store.SetMcpServerTokenMeta(ctx, name, keyHint(token), login, version); err != nil {
		return storeError(err)
	}
	s.logger.InfoContext(ctx, "an mcp server token was stored", "mcp_server", name,
		"secret", mcp.TokenSecret(name), "secret_version", version, "login", login)
	return nil
}

// deleteMcpToken removes the secret. NotFound is success — the goal state is that the
// control plane does not hold it.
func (s *AgentService) deleteMcpToken(ctx context.Context, name string) error {
	err := s.secrets.DeleteSecret(ctx, mcp.TokenSecret(name))
	if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
		return connect.NewError(connect.CodeOf(err),
			fmt.Errorf("removing the MCP server's token failed: %w", err))
	}
	return nil
}

// readMcpServer is the row as the API reports it, after a write. A read that fails after a
// successful write is not the write failing: the caller is answered with what it asked for
// and the fresher copy is the next listing's job.
func (s *AgentService) readMcpServer(ctx context.Context, name string) *agentv1.McpServer {
	row, err := s.store.McpServer(ctx, name)
	if err != nil {
		s.logger.WarnContext(ctx, "reading an mcp server back after a write failed",
			"mcp_server", name, "error", err)
		return &agentv1.McpServer{Name: name}
	}
	return s.mcpServerToProto(ctx, row, s.playbooksByMcpServer())
}

func (s *AgentService) requireMcpStore() error {
	switch {
	case s.store == nil:
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no database, so it has no MCP server registry"))
	case s.secrets == nil:
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no Podium API client, so it cannot store a token"))
	}
	return nil
}

// mcpNotFound turns a store miss into the answer an operator needs.
func (s *AgentService) mcpNotFound(err error, name string) error {
	if !errors.Is(err, store.ErrNotFound) {
		return storeError(err)
	}
	return connect.NewError(connect.CodeNotFound,
		fmt.Errorf("no MCP server named %q is registered", name))
}

// playbooksByMcpServer is which playbooks name each server, out of the profile in force. It
// is what makes a disable or a delete a decision rather than a surprise.
func (s *AgentService) playbooksByMcpServer() map[string][]string {
	out := map[string][]string{}
	if s.profiles == nil {
		return out
	}
	profile := s.profiles.Current()
	if profile == nil {
		return out
	}
	for _, name := range profile.PlaybookNames() {
		for _, srv := range profile.Playbooks[name].MCPServers {
			out[srv] = append(out[srv], name)
		}
	}
	return out
}

// mcpServerFromProto reads the fields a client owns and refuses a registration that breaks a
// rule. Everything else on the message — the token metadata, the provenance, the playbook
// list — is the conductor's and is ignored rather than trusted.
func mcpServerFromProto(msg *agentv1.McpServer) (mcp.Server, error) {
	if msg == nil {
		return mcp.Server{}, connect.NewError(connect.CodeInvalidArgument,
			errors.New("server is required"))
	}
	srv := mcp.Server{
		Name:        strings.TrimSpace(msg.GetName()),
		URL:         strings.TrimSpace(msg.GetUrl()),
		Description: strings.TrimSpace(msg.GetDescription()),
		Enabled:     msg.GetEnabled(),
	}
	if err := srv.Validate(); err != nil {
		return mcp.Server{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return srv, nil
}

// mcpServerToProto renders one row. token_set is asked of the CONTROL PLANE and not taken
// from the row: the secret belongs there, `podium secret rm` is a thing an operator can do
// without this conductor hearing about it, and a UI saying "connected" about a credential
// that is gone is a lie every turn disproves.
func (s *AgentService) mcpServerToProto(
	ctx context.Context, row mcp.Server, users map[string][]string,
) *agentv1.McpServer {
	out := &agentv1.McpServer{
		Name:        row.Name,
		Url:         row.URL,
		Description: row.Description,
		Enabled:     row.Enabled,
		Playbooks:   users[row.Name],
		CreatedBy:   row.CreatedBy,
		UpdatedBy:   row.UpdatedBy,
		TokenEnv:    mcp.TokenEnv(row.Name),
		TokenSecret: mcp.TokenSecret(row.Name),
	}
	if !row.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(row.UpdatedAt)
	}
	if o := row.OAuth; o != nil {
		// Shown, never authorised on: the account came out of an id_token whose signature
		// nothing here verifies, because nothing here decides anything with it.
		out.Account = o.Account
		out.Refreshable = o.RefreshToken != ""
		out.OauthSupported = true
		if !o.ExpiresAt.IsZero() {
			out.ExpiresAt = timestamppb.New(o.ExpiresAt)
		}
	}
	version, verr := s.secretVersion(ctx, mcp.TokenSecret(row.Name))
	if verr != nil {
		// The control plane could not be asked. The row is what is left, and it is reported
		// as-is rather than as "no token": a listing that silently disowned every credential
		// because one call failed would send an operator to paste them all again.
		s.logger.WarnContext(ctx, "could not ask the control plane about an mcp server's token",
			"mcp_server", row.Name, "error", verr)
		out.TokenSet = row.TokenSecretVersion > 0
	} else {
		out.TokenSet = version > 0
	}
	// The hint describes one version of one credential. Anything else and it is withheld
	// rather than shown next to a credential it is not about. The same rule decides the
	// auth kind: a row saying "signed in" about a secret the control plane no longer holds
	// is a claim every turn disproves.
	if out.GetTokenSet() && row.TokenSecretVersion > 0 &&
		(verr != nil || version == row.TokenSecretVersion) {
		out.TokenHint = row.TokenHint
		out.TokenSetBy = row.TokenSetBy
		out.AuthKind = row.Kind()
		if !row.TokenSetAt.IsZero() {
			out.TokenSetAt = timestamppb.New(row.TokenSetAt)
		}
	}
	return out
}

// redactedMcpTokenRequest is how a request carrying a token reaches a log statement: the
// name, and the token as a length.
type redactedMcpTokenRequest struct {
	name  string
	token string
}

func (r redactedMcpTokenRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("mcp_server", r.name),
		slog.String("token", "[redacted]"),
		slog.Int("token_len", len(strings.TrimSpace(r.token))),
	)
}
