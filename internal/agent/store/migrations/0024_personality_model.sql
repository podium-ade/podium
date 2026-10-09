-- A personal assistant may name the model it answers on. Empty agent, model, and
-- effort mean the voice follows Podium. It still has no skills and no computer.
alter table personalities add column agent text not null default '';
alter table personalities add column model text not null default '';
alter table personalities add column effort text not null default '';
