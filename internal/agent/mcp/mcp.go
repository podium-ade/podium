// Package mcp is the conductor's registry of MCP servers: what one is, what a name may be,
// and which Podium secret and environment variable a server's credential travels in.
//
// A server here is REMOTE and only remote — an address the harness opens an HTTP connection
// to. A local server is a command line, and a command line an operator typed into a browser
// form is a process running inside the turn container with that turn's GitHub token and
// model credential. The two local servers a turn can get, memory's client and the browser's,
// are the conductor's own decision and stay that way.
//
// Nothing in this package holds a token. It holds the NAME of the secret one is stored under
// and the NAME of the variable it is delivered in, which is the same split the model
// credentials and memory's key follow: a brief is an environment variable on a task spec and
// is readable by anything that can read the spec.
package mcp

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// NameRE constrains a server name. It is the playbook rule with the same reasoning: the name
// is typed into a playbook's mcp_servers list, and it is also the prefix the harness gives
// this server's tools — `linear` is where `mcp__linear__*` comes from.
var NameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// The names the runtime already owns. A registration may not take one: the harness config is
// one map of server name to server, so a second `memory` would replace the shared memory a
// turn cannot opt out of, a second `browser` would replace the sidecar's client, and a second
// `podium` would replace the delegation a host turn does its work through.
//
// agent/runtime/src/opencode.ts holds the same list as ReservedServers and refuses one there
// too. Two checks because they fail differently: this one refuses a registration, with a
// sentence an operator reads while looking at the form; that one is the backstop, and its
// cost of being wrong is silent.
const (
	ReservedMemory   = "memory"
	ReservedBrowser  = "browser"
	ReservedDelegate = "podium"
	ReservedHuman    = "human"
)

// MaxServers is how many MCP servers one playbook may name. Each one is a connection the
// harness opens and a tool list it reads before the first model request, so this is a bound
// on how slow a turn can be to start as much as it is a bound on privilege.
const MaxServers = 8

// MaxDescriptionLen caps the operator's own note. It is shown in a browser and travels
// nowhere else.
const MaxDescriptionLen = 512

// HeaderName and TokenPrefix are how every server's credential is presented, and they are
// constants rather than columns: the MCP authorization specification says a bearer token
// goes in the Authorization header, so there is nothing here for an operator to get wrong.
// A server that wants something else is out of spec, and this is the one place that would
// have to grow to humour one.
const (
	HeaderName  = "Authorization"
	TokenPrefix = "Bearer "
)

// SecretPrefix and EnvPrefix are the namespaces a server's credential lives in.
//
// EnvPrefix is reserved on a playbook's env: the conductor writes one variable per server it
// is delivering, and a playbook that could set them itself could point a turn's Linear
// client at a token of its own choosing.
const (
	SecretPrefix = "podium.agent.mcp."
	EnvPrefix    = "PODIUM_MCP_"
)

// How a server's credential was obtained. The distinction is not cosmetic: a pasted token
// does not expire and an access token does, so only one of the two is refreshed and only one
// of the two has a hint worth showing.
//
// What it is NOT is a difference a turn can see. Both end up as the same bearer token in the
// same Podium secret, so the brief, the task spec and the runtime are identical either way.
const (
	AuthNone  = ""
	AuthToken = "token"
	AuthOAuth = "oauth"
)

// OAuth is what a sign-in leaves behind: enough to refresh it, and enough to say who it is.
//
// IT HOLDS CREDENTIALS — RefreshToken, and ClientSecret where the authorization server
// issued one. It is stored in the conductor's own database in clear, for the same reason the
// subscription sign-in's refresh token is: Podium's secret store deliberately has no read
// endpoint, so a value put there cannot be read back to refresh with. Neither field ever
// leaves this process except to the authorization server's own token endpoint, and neither
// is ever put in a brief, a task, a log or the proto.
type OAuth struct {
	// Issuer is the authorization server the protected-resource metadata named.
	Issuer string `json:"issuer"`
	// TokenEndpoint is where a refresh is POSTed. It is stored rather than re-discovered so
	// a background refresh does not depend on discovery still answering.
	TokenEndpoint string `json:"token_endpoint"`
	// ClientID is the client this conductor registered dynamically, and ClientSecret the
	// secret that registration issued, if any. A public client has none and uses PKCE alone.
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	// RefreshToken is what keeps the sign-in alive. Empty means it dies at ExpiresAt and a
	// human has to sign in again.
	RefreshToken string `json:"refresh_token,omitempty"`
	// ExpiresAt is when the stored access token stops working. Zero means the server did not
	// say, which is treated as "there is nothing to refresh on a schedule".
	ExpiresAt time.Time `json:"expires_at,omitzero"`
	// Scope is what was actually granted, as the token response reported it.
	Scope string `json:"scope,omitempty"`
	// Account is who the authorization server says signed in. Shown, never authorised on.
	Account string `json:"account,omitempty"`
	// Resource is the RFC 8707 resource indicator the token was issued for — the MCP
	// server's own URL. A refresh has to name the same one.
	Resource string `json:"resource,omitempty"`
}

// Server is one registered MCP server. It is the row, and the API message is built from it.
type Server struct {
	Name        string
	URL         string
	Description string
	Enabled     bool
	// TokenHint is the last four characters of the token, kept at save time. It is the only
	// form any part of a token is read back in.
	TokenHint  string
	TokenSetBy string
	TokenSetAt time.Time
	// TokenSecretVersion is the version of the Podium secret the hint describes. A hint is
	// only shown when the control plane still holds that version: an operator can remove a
	// secret with the CLI without this conductor hearing about it.
	TokenSecretVersion int32
	// AuthKind is AuthNone, AuthToken or AuthOAuth.
	AuthKind string
	// OAuth is set for AuthOAuth and nil otherwise. SENSITIVE — see the type.
	OAuth     *OAuth
	CreatedBy string
	UpdatedBy string
	UpdatedAt time.Time
}

// Kind is the row's auth kind, defaulting a row written before there was more than one —
// which was always a pasted token, and only where a secret version says there is one at all.
func (s Server) Kind() string {
	if s.AuthKind == "" && s.TokenSecretVersion > 0 {
		return AuthToken
	}
	return s.AuthKind
}

// Refreshable is whether the background pass has anything to work with.
func (s Server) Refreshable() bool {
	return s.Kind() == AuthOAuth && s.OAuth != nil && s.OAuth.RefreshToken != ""
}

// TokenSecret is the Podium secret one server's credential is stored as. A playbook may not
// name it: the conductor decides which turns get which server, and a playbook that could
// name the secret could hand the token to a container the registry never granted it to.
func TokenSecret(name string) string { return SecretPrefix + name + "_token" }

// TokenEnv is where that secret lands in a task container, and what the brief's token_env
// names. NameRE has already refused everything but lowercase alphanumerics and hyphens, so
// the mapping is one-to-one and no two names can collide here.
func TokenEnv(name string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_TOKEN"
}

// ValidateName reports whether a name may be registered or named by a playbook. It is
// exported because a playbook's mcp_servers list is checked with it long before any row is
// read — a playbook file has to validate on a machine with no database at all.
func ValidateName(name string) error {
	switch {
	case name == "":
		return errors.New("an MCP server name is required")
	case !NameRE.MatchString(name):
		return fmt.Errorf("MCP server name %q must match %s", name, NameRE.String())
	case name == ReservedMemory, name == ReservedBrowser, name == ReservedDelegate, name == ReservedHuman:
		return fmt.Errorf("MCP server name %q is reserved: the runtime gives that name to "+
			"a server it wires up itself", name)
	}
	return nil
}

// Validate holds a registration to the rules. It is what the API refuses on, and it is
// deliberately about the document and not about the server: whether the address answers, and
// whether the credential works, is found out by a turn. There is no way to ask an MCP server
// "are you there" that does not also mean "here is my token".
func (s Server) Validate() error {
	var errs []error
	if err := ValidateName(s.Name); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, validateURL(s.URL))
	if len(s.Description) > MaxDescriptionLen {
		errs = append(errs, fmt.Errorf("description is %d characters; the limit is %d",
			len(s.Description), MaxDescriptionLen))
	}
	return errors.Join(errs...)
}

// validateURL is the whole of what is assumed about an address: absolute, http or https, and
// a host. https is not required — a server on a tailnet or a loopback port is a real thing
// to register, and refusing it would push an operator towards a local command instead.
func validateURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url %q does not parse: %w", raw, err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("url %q must be http or https", raw)
	case u.Host == "":
		return fmt.Errorf("url %q names no host", raw)
	}
	return nil
}
