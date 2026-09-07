-- name: ListStoredPlaybooks :many
select name, definition, updated_at, updated_by from playbooks order by name;

-- Insert and update are separate statements rather than one upsert so that "create" and
-- "replace" cannot be confused: the row count says whether the name was already taken, and
-- whether it was there to replace, without a read before the write.

-- name: InsertStoredPlaybook :execrows
insert into playbooks (name, definition, updated_at, updated_by)
values (@name, @definition, @updated_at, @updated_by)
on conflict (name) do nothing;

-- name: UpdateStoredPlaybook :execrows
update playbooks
set definition = @definition, updated_at = @updated_at, updated_by = @updated_by
where name = @name;

-- name: DeleteStoredPlaybook :execrows
delete from playbooks where name = @name;
