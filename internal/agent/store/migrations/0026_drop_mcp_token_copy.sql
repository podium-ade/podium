-- The readable copy of each MCP server's access token goes. An assistant turn now reads the
-- token from Podium's encrypted secret store, which answers the conductor for its own
-- credentials, so a copy in clear here is no longer needed. See docs/security.md.
alter table mcp_servers drop column if exists token;
