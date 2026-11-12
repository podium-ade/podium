-- name: GetOrganization :one
select * from organization where id = 1;

-- name: SetOrganization :one
insert into organization (id, organization_name, deployment_name, updated_by, updated_at)
values (1, @organization_name, @deployment_name, @updated_by, now())
on conflict (id) do update set
  organization_name = excluded.organization_name,
  deployment_name   = excluded.deployment_name,
  updated_by        = excluded.updated_by,
  updated_at        = now()
returning *;
