-- name: ListStoredSkills :many
-- Deliberately without `document`: a listing of twenty skills has no business carrying
-- twenty bundles, and nothing the UI shows comes out of the bundle itself.
select name, description, sha256, size_bytes, file_count, enabled, uploaded_by, uploaded_at
from agent_skills
order by name;

-- name: GetStoredSkill :one
select name, description, sha256, size_bytes, file_count, enabled, document, uploaded_by, uploaded_at
from agent_skills
where name = @name;

-- Insert and replace are separate statements rather than one upsert, exactly as the playbook
-- queries are: the row count says whether the name was already taken without a read before
-- the write, and "upload" and "replace this skill" stay two different requests.

-- name: InsertStoredSkill :execrows
insert into agent_skills (name, description, sha256, size_bytes, file_count, document, uploaded_by, uploaded_at)
values (@name, @description, @sha256, @size_bytes, @file_count, @document, @uploaded_by, @uploaded_at)
on conflict (name) do nothing;

-- name: ReplaceStoredSkill :execrows
-- `enabled` is not in the set list: replacing the bundle of a skill somebody turned off must
-- not turn it back on.
update agent_skills
set description = @description,
    sha256 = @sha256,
    size_bytes = @size_bytes,
    file_count = @file_count,
    document = @document,
    uploaded_by = @uploaded_by,
    uploaded_at = @uploaded_at
where name = @name;

-- name: SetStoredSkillEnabled :execrows
update agent_skills set enabled = @enabled where name = @name;

-- name: DeleteStoredSkill :execrows
delete from agent_skills where name = @name;
