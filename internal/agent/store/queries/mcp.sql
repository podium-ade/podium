-- name: ListMcpServers :many
select name, url, description, enabled, token_hint, token_set_by,
       token_set_at, token_secret_version, auth_kind, oauth, created_by, updated_by, updated_at
from mcp_servers
order by name;

-- name: GetMcpServer :one
select name, url, description, enabled, token_hint, token_set_by,
       token_set_at, token_secret_version, auth_kind, oauth, created_by, updated_by, updated_at
from mcp_servers
where name = @name;

-- Insert and update are separate statements rather than one upsert, exactly as the playbook
-- and skill queries are: the row count says whether the name was already taken, and whether
-- it was there to replace, without a read before the write.

-- name: InsertMcpServer :execrows
insert into mcp_servers (name, url, description, enabled, created_by, updated_by, updated_at)
values (@name, @url, @description, @enabled, @created_by, @updated_by, @updated_at)
on conflict (name) do nothing;

-- name: UpdateMcpServer :execrows
-- The token columns are not in the set list: an edit to a server's address or description
-- must not disturb the credential, and it must not be able to relabel one either.
update mcp_servers
set url = @url,
    description = @description,
    enabled = @enabled,
    updated_by = @updated_by,
    updated_at = @updated_at
where name = @name;

-- name: SetMcpServerTokenMeta :execrows
-- Also clears any OAuth state: a pasted token replaces a sign-in, and leaving the refresh
-- token behind would let the background pass overwrite the token somebody just pasted.
update mcp_servers
set token_hint = @token_hint,
    token_set_by = @token_set_by,
    token_set_at = @token_set_at,
    token_secret_version = @token_secret_version,
    auth_kind = @auth_kind,
    oauth = null,
    updated_by = @token_set_by,
    updated_at = @token_set_at
where name = @name;

-- name: SetMcpServerOAuth :execrows
-- The other way in: a sign-in. token_hint stays empty — an access token is not a thing to
-- show four characters of — and `oauth` carries what a refresh needs.
update mcp_servers
set token_hint = '',
    token_set_by = @token_set_by,
    token_set_at = @token_set_at,
    token_secret_version = @token_secret_version,
    auth_kind = @auth_kind,
    oauth = @oauth,
    updated_by = @token_set_by,
    updated_at = @token_set_at
where name = @name;

-- name: RefreshMcpServerOAuth :execrows
-- The background pass, which is not a human: it moves the token and the expiry and touches
-- neither `updated_by` nor the provenance of the sign-in.
update mcp_servers
set token_secret_version = @token_secret_version,
    oauth = @oauth
where name = @name;

-- name: ClearMcpServerTokenMeta :execrows
-- The refresh token goes too, and that is what actually signs out: leaving it would let the
-- background pass mint a new access token for a server the operator has just disconnected.
update mcp_servers
set token_hint = '',
    token_set_by = '',
    token_set_at = null,
    token_secret_version = 0,
    auth_kind = '',
    oauth = null,
    updated_by = @updated_by,
    updated_at = @updated_at
where name = @name;

-- name: DeleteMcpServer :execrows
delete from mcp_servers where name = @name;
