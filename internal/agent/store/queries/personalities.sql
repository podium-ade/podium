-- A personality is one person's voice on top of Podium. Every write is filtered by
-- login, so knowing an id is not access to somebody else's row.

-- name: ListPersonalities :many
select id, login, name, display_name, instructions, updated_at, agent, model, effort
from personalities
where login = @login
order by name;

-- name: GetPersonality :one
select id, login, name, display_name, instructions, updated_at, agent, model, effort
from personalities
where id = @id;

-- InsertPersonality reports a taken name as zero rows. The unique key is (login, name),
-- so two people may each have a voice called "helper" and one person may not have two.
-- name: InsertPersonality :execrows
insert into personalities (id, login, name, display_name, instructions, agent, model, effort, updated_at)
values (@id, @login, @name, @display_name, @instructions, @agent, @model, @effort, @updated_at)
on conflict (login, name) do nothing;

-- UpdatePersonality replaces one of login's rows. Zero rows means it is missing or it
-- belongs to somebody else, which are the same answer. A rename onto a name this login
-- already has is a unique violation, which the store maps to a conflict.
-- name: UpdatePersonality :execrows
update personalities
set name = @name,
    display_name = @display_name,
    instructions = @instructions,
    agent = @agent,
    model = @model,
    effort = @effort,
    updated_at = @updated_at
where id = @id and login = @login;

-- name: DeletePersonality :execrows
delete from personalities
where id = @id and login = @login;
