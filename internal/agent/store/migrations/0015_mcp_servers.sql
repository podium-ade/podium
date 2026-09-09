-- MCP servers an operator registered in the web UI.
--
-- A playbook's mcp_servers list names rows in this table, and the conductor turns each name
-- into one entry in the turn's harness config. There is no file half: unlike a playbook or
-- an Agent Skill, an MCP server is an address and a credential rather than a document
-- somebody would keep in version control, and the credential does not live here at all.
--
-- What is stored is metadata. The TOKEN is a Podium secret in the control plane's encrypted
-- store, under mcp.TokenSecret(name), and cannot be read back from there by anything —
-- including this conductor. token_hint is the last four characters, kept at save time,
-- because "is the thing I pasted the thing that is stored" is a question an operator asks
-- and the only honest way to answer it.
--
-- There is no column for how the token is presented. The MCP authorization specification
-- says a bearer token goes in the Authorization header, so `Authorization: Bearer <token>`
-- is what every server gets and there is nothing to configure. A server that wants something
-- else is out of spec, and this is where the columns would go if one ever has to be humoured.
--
-- token_secret_version is what keeps the hint honest. The secret belongs to the control
-- plane and `podium secret rm` is a thing an operator can do without this conductor hearing
-- about it, so a hint is only shown while the control plane still holds the version it was
-- taken from.
create table mcp_servers (
  name         text        primary key,
  url          text        not null,
  description  text        not null default '',
  -- enabled is how a server is taken out of service without being deleted, and without
  -- editing every playbook that names it. A playbook naming a disabled server fails its
  -- turns saying so; it is not silently dropped, because a turn running with fewer tools
  -- than its playbook describes is the one outcome nobody can diagnose afterwards.
  enabled      boolean     not null default true,
  token_hint   text        not null default '',
  token_set_by text        not null default '',
  token_set_at timestamptz,
  token_secret_version integer not null default 0,
  -- auth_kind is '' for a server with no credential, 'token' for one somebody pasted, and
  -- 'oauth' for one this conductor signed in for. The access token of a sign-in lives in
  -- exactly the same Podium secret a pasted one does, which is why nothing downstream of
  -- here — not the brief, not the task spec, not the runtime — knows the difference.
  auth_kind    text        not null default '',
  -- oauth is everything a sign-in needs to be REFRESHED: the authorization server, its token
  -- endpoint, the client this conductor registered dynamically, and the refresh token.
  --
  -- IT HOLDS CREDENTIALS. The refresh token and, where the authorization server issued one,
  -- a client secret are in this column in clear. That is the same trade providerRow already
  -- makes for the subscription sign-in and for the same blunt reason: Podium's secret store
  -- deliberately has no read endpoint, so a value put there cannot be read back to refresh
  -- with. Treat podium_agent's database as holding credentials, because it does — see
  -- docs/security.md.
  --
  -- One jsonb rather than nine columns because it is one bag of discovery output that is
  -- read and written whole, and because the shape follows whatever the authorization server
  -- answered rather than anything this schema decides.
  oauth        jsonb,
  created_by   text        not null,
  updated_by   text        not null,
  updated_at   timestamptz not null
);
