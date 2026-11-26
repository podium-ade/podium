-- A routine may start a playbook right away (docs/agent.md, "Routines"): its run delegates
-- that playbook with the instruction and the events as the task's brief, instead of an
-- assistant's turn deciding what to delegate. '' is the assistant, as every routine before.
-- '@review', on a routine on GitHub events, is the pull request's repository's reviewer: the
-- playbook naming the repository in github_repos, else the github: true one. That is how the
-- review on a pull request being opened, once the GitHub App's own, is now a routine.
alter table routines add column playbook text not null default '';

-- A routine on GitHub events decides whose events it is given and about which pull requests,
-- as the GitHub App's triggers decided for its review: {"allowed_people": [...],
-- "ignored_people": [...], "ignore_bots": bool, "excluded_branches": [...], "skip_drafts": bool}.
-- '{}' turns nothing away.
alter table routines add column github_filters jsonb not null default '{}'::jsonb;
