-- fallback is only read on a global server (owner empty). A person's turn that has
-- no server of this name uses the global one when it is true. A turn with no person
-- uses the global list either way. Existing rows stay off: a company token is not
-- spent for someone until an admin allows it.

alter table mcp_servers add column fallback boolean not null default false;
