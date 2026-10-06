-- A secret is global or owned by one signed-in login.
--
-- Existing rows become global: scope and owner are added with defaults, and ciphertext,
-- nonce, version and key_id are not rewritten. A name may not exist in both scopes, and a
-- podium.agent.* name may only be global. Two people may share a personal name; they may
-- not share it with the global row.

alter table secrets add column scope text not null default 'global';
alter table secrets add column owner text not null default '';

alter table secrets drop constraint secrets_pkey;
alter table secrets add primary key (scope, owner, name);

alter table secrets add constraint secrets_scope_ok
  check (scope in ('global', 'personal'));

alter table secrets add constraint secrets_owner_ok
  check (
    (scope = 'global' and owner = '') or
    (scope = 'personal' and owner <> '')
  );

alter table secrets add constraint secrets_reserved_global
  check (
    scope = 'global' or (name not like 'podium.agent.%' and name <> 'podium.agent')
  );

create or replace function secrets_reject_shared_name() returns trigger
language plpgsql as $$
begin
  if exists (
    select 1 from secrets
    where name = new.name and scope <> new.scope
  ) then
    raise exception 'secret name % exists in both global and personal scope', new.name
      using errcode = '23514';
  end if;
  return new;
end;
$$;

create trigger secrets_reject_shared_name
before insert or update of name, scope on secrets
for each row execute function secrets_reject_shared_name();
