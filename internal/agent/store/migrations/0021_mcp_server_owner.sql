-- An MCP server is the bot's (owner empty) or one signed-in person's.
-- Existing rows stay the Slack bot list. Two people may register the same name;
-- they do not share a row, and neither shares it with the bot.

alter table mcp_servers add column owner text not null default '';

alter table mcp_servers drop constraint mcp_servers_pkey;
alter table mcp_servers add primary key (owner, name);
