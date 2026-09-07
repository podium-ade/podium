-- Podium's "skill" is now a "playbook".
--
-- The word was taken. An Agent Skill — a SKILL.md with YAML frontmatter, shipped by a third
-- party and discovered by the harness — is what the industry means by "skill", and Podium is
-- about to support those as a concept of their own. What this schema holds is something else:
-- a configured kind of turn, an image and a prompt and a tool allow-list and the routing
-- rules that pick it. That is a playbook.
--
-- 0001 and 0002 are left exactly as they were written. A migration that has already run
-- somewhere is a record of what ran, not a description of the current schema, and editing one
-- would leave a fresh database and an existing one disagreeing about what this file does. The
-- cost is that a fresh database creates `skills` and `sessions.skill` and then renames both a
-- moment later; the alternative is two schemas to reason about instead of one.

alter table skills rename to playbooks;
alter table sessions rename column skill to playbook;

-- The profile overrides a browser writes are one JSON document in `settings`, and two of its
-- keys were named after the old word. profiles.Overrides decodes with the ordinary rules, so a
-- `default_skill` left in that document would not fail — it would be ignored, and an operator
-- who had chosen a default on the Profile screen would silently get profile.yaml's answer
-- instead. Rename the keys rather than drop them.
update settings
   set value = (value - 'default_skill' - 'chat_default_skill')
             || jsonb_strip_nulls(jsonb_build_object(
                  'default_playbook', value -> 'default_skill',
                  'chat_default_playbook', value -> 'chat_default_skill'))
 where key = 'profile.overrides'
   and (value ? 'default_skill' or value ? 'chat_default_skill');
