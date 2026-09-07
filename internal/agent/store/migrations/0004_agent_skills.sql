-- Agent Skills, stored.
--
-- Phase 2 read skills off the conductor's own disk and nowhere else, which meant that loading
-- one needed a shell on the host running podium-agent. This is the other source: a bundle
-- somebody uploaded through AgentService, so a human can add a skill from a browser.
--
-- The table is `agent_skills` and not `skills` on purpose. 0003 renamed `skills` to
-- `playbooks` precisely because the word had been taken by something else; putting a table
-- called `skills` back would undo the only part of that rename anybody has to remember.
--
-- The bundle DOCUMENT lives in this column rather than in the object store. The conductor
-- holds no object-store credential — PODIUM_S3_* is podium-server's, and
-- internal/server/artifacts/config.go is explicit that nothing else talks to S3 — so putting
-- bundles there would mean handing the conductor read and delete on every artifact any task
-- has ever produced, in order to store a document that is capped at 128 KiB. A row is the
-- cheaper answer and it is deleted with the skill.
--
-- What is stored is the document, not the delivery encoding: sha256 is over these exact
-- bytes, and the gzip and base64 a turn travels with are re-derived per turn. That keeps the
-- digest meaning one thing regardless of how a bundle is later carried.
create table agent_skills (
  name        text        primary key,
  description text        not null,
  sha256      text        not null,
  size_bytes  bigint      not null,
  file_count  integer     not null,
  -- enabled is how a skill is taken out of service without being deleted. A playbook that
  -- names a disabled skill fails its turns saying so; it is not silently dropped, because a
  -- turn running with fewer skills than its playbook describes is the one outcome nobody can
  -- diagnose afterwards.
  enabled     boolean     not null default true,
  document    bytea       not null,
  uploaded_by text        not null,
  uploaded_at timestamptz not null
);
