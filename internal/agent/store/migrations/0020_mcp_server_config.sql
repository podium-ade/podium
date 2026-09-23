-- config is an operator's YAML for what the MCP form does not show: harness options such as
-- headers (see mcp.ParseConfig). It is stored as typed, so the form shows it back as written, and
-- it is no place for a credential — it travels in the brief.
alter table mcp_servers add column if not exists config text not null default '';
