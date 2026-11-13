-- A Linear account can be proven to be a Podium login, as a Slack one can (docs/agent.md, "Who
-- is who"). The Linear agent app hands the conductor the speaker of every prompt; the conductor
-- keeps their email only when it is a login that has signed in, and records the account here.
--
--   linear  external_id is "<organization_id>/<user_id>". A Linear user id is unique to one
--           workspace, and an app can be installed in several, so the workspace is part of
--           the key for the same reason the Slack team is.
--
-- The poller, which signs in with a bot user's personal API key, still proves nobody: it never
-- writes a row of this provider.
alter table identities drop constraint if exists identities_provider_check;
alter table identities add constraint identities_provider_check
  check (provider in ('web', 'slack', 'github', 'linear'));
