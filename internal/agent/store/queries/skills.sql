-- name: ListStoredSkills :many
select name, definition, updated_at, updated_by from skills order by name;

-- Insert and update are separate statements rather than one upsert so that "create" and
-- "replace" cannot be confused: the row count says whether the name was already taken, and
-- whether it was there to replace, without a read before the write.

-- name: InsertStoredSkill :execrows
insert into skills (name, definition, updated_at, updated_by)
values (@name, @definition, @updated_at, @updated_by)
on conflict (name) do nothing;

-- name: UpdateStoredSkill :execrows
update skills
set definition = @definition, updated_at = @updated_at, updated_by = @updated_by
where name = @name;

-- name: DeleteStoredSkill :execrows
delete from skills where name = @name;
