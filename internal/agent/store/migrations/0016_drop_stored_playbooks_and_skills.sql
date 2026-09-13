-- Playbooks and Agent Skills are files on the conductor's host. The UI overlay that stored
-- definitions in these tables is gone: playbooks live in PODIUM_AGENT_PROFILE_DIR and
-- skills in PODIUM_AGENT_SKILLS_DIR.
--
-- sessions.playbook is a name, not a foreign key to `playbooks`, and is left alone.
-- Profile overrides stay in settings; those are not playbook documents.

drop table if exists agent_skills;
drop table if exists playbooks;
