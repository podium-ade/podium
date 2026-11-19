-- Usage reads the usage_rows view (0056): every turn that ran a model, every delegated task
-- and every piece of the conductor's own housekeeping, one row each. Every query here takes
-- the same filter, so a figure on the screen and the rows behind it are always one set.
--
-- A filter parameter that is '' does not filter. started_by and auth_kind use '-' for "none
-- recorded", because '' already means "any". credential is the key UsageByCredential returns.

-- UsageByDay is the range's spend per day, bucketed by the caller's offset. The day leaves as
-- text rather than as a date, because a date would arrive as pgtype.Date and the point of the
-- overrides in sqlc.yaml is that no store signature speaks pgx.
-- name: UsageByDay :many
select to_char((u.started_at + make_interval(mins => @tz_offset_minutes::int))::date,
               'YYYY-MM-DD')::text as day,
       coalesce(sum(u.cost_usd), 0)::float8                                              as cost_usd,
       coalesce(sum(u.cost_usd) filter (where u.auth_kind = 'subscription'), 0)::float8 as subscription_cost_usd,
       count(*)::int                                                                    as turns,
       coalesce(sum(u.num_turns), 0)::int                                               as model_turns,
       count(*) filter (where u.cost_usd is null)::int                                  as unpriced,
       coalesce(sum(u.input_tokens), 0)::bigint                                         as input_tokens,
       coalesce(sum(u.output_tokens), 0)::bigint                                        as output_tokens,
       coalesce(sum(u.reasoning_tokens), 0)::bigint                                     as reasoning_tokens,
       coalesce(sum(u.cache_read_tokens), 0)::bigint                                    as cache_read_tokens,
       coalesce(sum(u.cache_write_tokens), 0)::bigint                                   as cache_write_tokens
from usage_rows u
where u.started_at >= @from_time and u.started_at < @to_time
  and (@f_origin::text = '' or u.origin = @f_origin)
  and (@f_started_by::text = '' or coalesce(nullif(u.started_by, ''), '-') = @f_started_by)
  and (@f_provider::text = '' or coalesce(u.provider, '') = @f_provider)
  and (@f_model::text = '' or coalesce(u.model, '') = @f_model)
  and (@f_auth_kind::text = '' or coalesce(u.auth_kind, '-') = @f_auth_kind)
  and (@f_credential::text = '' or concat_ws('|', coalesce(u.auth_kind, ''), coalesce(u.provider, ''), coalesce(u.credential_owner, '')) = @f_credential)
  and (@f_routine_id::text = '' or u.routine_id = @f_routine_id)
  and (@f_chat_id::text = '' or u.chat_id = @f_chat_id)
  and (@f_kind::text = '' or u.kind = @f_kind)
group by day
order by day;

-- UsageRows is one page of the range's rows, newest first. The cursor is the last row's
-- (started_at, id), so a page never repeats or skips a row that shares a start with another.
-- name: UsageRows :many
select u.id, u.kind, u.task_id, u.session_id, u.source_kind, u.source_key, u.playbook,
       u.profile, u.status, u.started_at, u.finished_at, u.num_turns, u.cost_usd,
       coalesce(u.agent, '')::text    as agent,
       coalesce(u.model, '')::text    as model,
       coalesce(u.effort, '')::text   as effort,
       coalesce(u.provider, '')::text as provider,
       u.started_by,
       coalesce(u.auth_kind, '')::text          as auth_kind,
       coalesce(u.credential_owner, '')::text   as credential_owner,
       coalesce(u.credential_account, '')::text as credential_account,
       u.input_tokens, u.output_tokens, u.reasoning_tokens, u.cache_read_tokens, u.cache_write_tokens,
       u.chat_id, u.chat_title, u.routine_id, u.routine_name, u.origin
from usage_rows u
where u.started_at >= @from_time and u.started_at < @to_time
  and (@f_origin::text = '' or u.origin = @f_origin)
  and (@f_started_by::text = '' or coalesce(nullif(u.started_by, ''), '-') = @f_started_by)
  and (@f_provider::text = '' or coalesce(u.provider, '') = @f_provider)
  and (@f_model::text = '' or coalesce(u.model, '') = @f_model)
  and (@f_auth_kind::text = '' or coalesce(u.auth_kind, '-') = @f_auth_kind)
  and (@f_credential::text = '' or concat_ws('|', coalesce(u.auth_kind, ''), coalesce(u.provider, ''), coalesce(u.credential_owner, '')) = @f_credential)
  and (@f_routine_id::text = '' or u.routine_id = @f_routine_id)
  and (@f_chat_id::text = '' or u.chat_id = @f_chat_id)
  and (@f_kind::text = '' or u.kind = @f_kind)
  and (sqlc.narg('cursor_at')::timestamptz is null
       or (u.started_at, u.id) < (sqlc.narg('cursor_at')::timestamptz, @cursor_id::text))
order by u.started_at desc, u.id desc
limit @page_limit::int;

-- UsageByBackend is spend grouped by what actually ran it. Rows from before the columns
-- existed group under empty strings, which the API reports as unrecorded.
-- name: UsageByBackend :many
select coalesce(u.provider, '')::text as provider,
       coalesce(u.agent, '')::text    as agent,
       coalesce(u.model, '')::text    as model,
       coalesce(u.effort, '')::text   as effort,
       coalesce(sum(u.cost_usd), 0)::float8                                              as cost_usd,
       coalesce(sum(u.cost_usd) filter (where u.auth_kind = 'subscription'), 0)::float8 as subscription_cost_usd,
       count(*)::int                                                                    as turns,
       coalesce(sum(u.num_turns), 0)::int                                               as model_turns,
       count(*) filter (where u.cost_usd is null)::int                                  as unpriced,
       coalesce(sum(u.input_tokens), 0)::bigint                                         as input_tokens,
       coalesce(sum(u.output_tokens), 0)::bigint                                        as output_tokens,
       coalesce(sum(u.reasoning_tokens), 0)::bigint                                     as reasoning_tokens,
       coalesce(sum(u.cache_read_tokens), 0)::bigint                                    as cache_read_tokens,
       coalesce(sum(u.cache_write_tokens), 0)::bigint                                   as cache_write_tokens
from usage_rows u
where u.started_at >= @from_time and u.started_at < @to_time
  and (@f_origin::text = '' or u.origin = @f_origin)
  and (@f_started_by::text = '' or coalesce(nullif(u.started_by, ''), '-') = @f_started_by)
  and (@f_provider::text = '' or coalesce(u.provider, '') = @f_provider)
  and (@f_model::text = '' or coalesce(u.model, '') = @f_model)
  and (@f_auth_kind::text = '' or coalesce(u.auth_kind, '-') = @f_auth_kind)
  and (@f_credential::text = '' or concat_ws('|', coalesce(u.auth_kind, ''), coalesce(u.provider, ''), coalesce(u.credential_owner, '')) = @f_credential)
  and (@f_routine_id::text = '' or u.routine_id = @f_routine_id)
  and (@f_chat_id::text = '' or u.chat_id = @f_chat_id)
  and (@f_kind::text = '' or u.kind = @f_kind)
group by 1, 2, 3, 4
order by cost_usd desc, turns desc;

-- UsageByPerson is spend grouped by whose turn it was. A delegated task is the person's whose
-- turn delegated it, because the person who asked is the person the work was for.
-- name: UsageByPerson :many
select u.started_by::text as started_by,
       coalesce(sum(u.cost_usd), 0)::float8                                              as cost_usd,
       coalesce(sum(u.cost_usd) filter (where u.auth_kind = 'subscription'), 0)::float8 as subscription_cost_usd,
       count(*)::int                                                                    as turns,
       coalesce(sum(u.num_turns), 0)::int                                               as model_turns,
       count(*) filter (where u.cost_usd is null)::int                                  as unpriced,
       coalesce(sum(u.input_tokens), 0)::bigint                                         as input_tokens,
       coalesce(sum(u.output_tokens), 0)::bigint                                        as output_tokens,
       coalesce(sum(u.reasoning_tokens), 0)::bigint                                     as reasoning_tokens,
       coalesce(sum(u.cache_read_tokens), 0)::bigint                                    as cache_read_tokens,
       coalesce(sum(u.cache_write_tokens), 0)::bigint                                   as cache_write_tokens
from usage_rows u
where u.started_at >= @from_time and u.started_at < @to_time
  and (@f_origin::text = '' or u.origin = @f_origin)
  and (@f_started_by::text = '' or coalesce(nullif(u.started_by, ''), '-') = @f_started_by)
  and (@f_provider::text = '' or coalesce(u.provider, '') = @f_provider)
  and (@f_model::text = '' or coalesce(u.model, '') = @f_model)
  and (@f_auth_kind::text = '' or coalesce(u.auth_kind, '-') = @f_auth_kind)
  and (@f_credential::text = '' or concat_ws('|', coalesce(u.auth_kind, ''), coalesce(u.provider, ''), coalesce(u.credential_owner, '')) = @f_credential)
  and (@f_routine_id::text = '' or u.routine_id = @f_routine_id)
  and (@f_chat_id::text = '' or u.chat_id = @f_chat_id)
  and (@f_kind::text = '' or u.kind = @f_kind)
group by 1
order by cost_usd desc, turns desc;

-- UsageBySource is spend grouped by origin: the surface, or 'routine' for a routine's run and
-- 'housekeeping' for the conductor's own.
-- name: UsageBySource :many
select u.origin::text as origin,
       coalesce(sum(u.cost_usd), 0)::float8                                              as cost_usd,
       coalesce(sum(u.cost_usd) filter (where u.auth_kind = 'subscription'), 0)::float8 as subscription_cost_usd,
       count(*)::int                                                                    as turns,
       coalesce(sum(u.num_turns), 0)::int                                               as model_turns,
       count(*) filter (where u.cost_usd is null)::int                                  as unpriced,
       coalesce(sum(u.input_tokens), 0)::bigint                                         as input_tokens,
       coalesce(sum(u.output_tokens), 0)::bigint                                        as output_tokens,
       coalesce(sum(u.reasoning_tokens), 0)::bigint                                     as reasoning_tokens,
       coalesce(sum(u.cache_read_tokens), 0)::bigint                                    as cache_read_tokens,
       coalesce(sum(u.cache_write_tokens), 0)::bigint                                   as cache_write_tokens
from usage_rows u
where u.started_at >= @from_time and u.started_at < @to_time
  and (@f_origin::text = '' or u.origin = @f_origin)
  and (@f_started_by::text = '' or coalesce(nullif(u.started_by, ''), '-') = @f_started_by)
  and (@f_provider::text = '' or coalesce(u.provider, '') = @f_provider)
  and (@f_model::text = '' or coalesce(u.model, '') = @f_model)
  and (@f_auth_kind::text = '' or coalesce(u.auth_kind, '-') = @f_auth_kind)
  and (@f_credential::text = '' or concat_ws('|', coalesce(u.auth_kind, ''), coalesce(u.provider, ''), coalesce(u.credential_owner, '')) = @f_credential)
  and (@f_routine_id::text = '' or u.routine_id = @f_routine_id)
  and (@f_chat_id::text = '' or u.chat_id = @f_chat_id)
  and (@f_kind::text = '' or u.kind = @f_kind)
group by 1
order by cost_usd desc, turns desc;

-- UsageByCredential is spend grouped by what paid for it: a subscription or an API key, which
-- provider's, and whose (empty for the company's). Rows from before 0056 group under an
-- empty kind.
-- name: UsageByCredential :many
select coalesce(u.auth_kind, '')::text        as auth_kind,
       coalesce(u.provider, '')::text         as provider,
       coalesce(u.credential_owner, '')::text as credential_owner,
       coalesce(max(u.credential_account), '')::text as credential_account,
       coalesce(sum(u.cost_usd), 0)::float8                                              as cost_usd,
       coalesce(sum(u.cost_usd) filter (where u.auth_kind = 'subscription'), 0)::float8 as subscription_cost_usd,
       count(*)::int                                                                    as turns,
       coalesce(sum(u.num_turns), 0)::int                                               as model_turns,
       count(*) filter (where u.cost_usd is null)::int                                  as unpriced,
       coalesce(sum(u.input_tokens), 0)::bigint                                         as input_tokens,
       coalesce(sum(u.output_tokens), 0)::bigint                                        as output_tokens,
       coalesce(sum(u.reasoning_tokens), 0)::bigint                                     as reasoning_tokens,
       coalesce(sum(u.cache_read_tokens), 0)::bigint                                    as cache_read_tokens,
       coalesce(sum(u.cache_write_tokens), 0)::bigint                                   as cache_write_tokens
from usage_rows u
where u.started_at >= @from_time and u.started_at < @to_time
  and (@f_origin::text = '' or u.origin = @f_origin)
  and (@f_started_by::text = '' or coalesce(nullif(u.started_by, ''), '-') = @f_started_by)
  and (@f_provider::text = '' or coalesce(u.provider, '') = @f_provider)
  and (@f_model::text = '' or coalesce(u.model, '') = @f_model)
  and (@f_auth_kind::text = '' or coalesce(u.auth_kind, '-') = @f_auth_kind)
  and (@f_credential::text = '' or concat_ws('|', coalesce(u.auth_kind, ''), coalesce(u.provider, ''), coalesce(u.credential_owner, '')) = @f_credential)
  and (@f_routine_id::text = '' or u.routine_id = @f_routine_id)
  and (@f_chat_id::text = '' or u.chat_id = @f_chat_id)
  and (@f_kind::text = '' or u.kind = @f_kind)
group by 1, 2, 3
order by cost_usd desc, turns desc;

-- UsageByConversation is the range's most expensive conversations.
-- name: UsageByConversation :many
select u.session_id::text  as session_id,
       max(u.chat_id)::text    as chat_id,
       max(u.chat_title)::text as chat_title,
       max(u.origin)::text     as origin,
       max(u.source_key)::text as source_key,
       coalesce(sum(u.cost_usd), 0)::float8                                              as cost_usd,
       coalesce(sum(u.cost_usd) filter (where u.auth_kind = 'subscription'), 0)::float8 as subscription_cost_usd,
       count(*)::int                                                                    as turns,
       coalesce(sum(u.num_turns), 0)::int                                               as model_turns,
       count(*) filter (where u.cost_usd is null)::int                                  as unpriced,
       coalesce(sum(u.input_tokens), 0)::bigint                                         as input_tokens,
       coalesce(sum(u.output_tokens), 0)::bigint                                        as output_tokens,
       coalesce(sum(u.reasoning_tokens), 0)::bigint                                     as reasoning_tokens,
       coalesce(sum(u.cache_read_tokens), 0)::bigint                                    as cache_read_tokens,
       coalesce(sum(u.cache_write_tokens), 0)::bigint                                   as cache_write_tokens
from usage_rows u
where u.started_at >= @from_time and u.started_at < @to_time
  and (@f_origin::text = '' or u.origin = @f_origin)
  and (@f_started_by::text = '' or coalesce(nullif(u.started_by, ''), '-') = @f_started_by)
  and (@f_provider::text = '' or coalesce(u.provider, '') = @f_provider)
  and (@f_model::text = '' or coalesce(u.model, '') = @f_model)
  and (@f_auth_kind::text = '' or coalesce(u.auth_kind, '-') = @f_auth_kind)
  and (@f_credential::text = '' or concat_ws('|', coalesce(u.auth_kind, ''), coalesce(u.provider, ''), coalesce(u.credential_owner, '')) = @f_credential)
  and (@f_routine_id::text = '' or u.routine_id = @f_routine_id)
  and (@f_chat_id::text = '' or u.chat_id = @f_chat_id)
  and (@f_kind::text = '' or u.kind = @f_kind)
group by 1
order by cost_usd desc, turns desc
limit @page_limit::int;

-- UsageByRoutine is spend grouped by routine, for the rows that were a routine's run.
-- name: UsageByRoutine :many
select u.routine_id::text        as routine_id,
       max(u.routine_name)::text as routine_name,
       coalesce(sum(u.cost_usd), 0)::float8                                              as cost_usd,
       coalesce(sum(u.cost_usd) filter (where u.auth_kind = 'subscription'), 0)::float8 as subscription_cost_usd,
       count(*)::int                                                                    as turns,
       coalesce(sum(u.num_turns), 0)::int                                               as model_turns,
       count(*) filter (where u.cost_usd is null)::int                                  as unpriced,
       coalesce(sum(u.input_tokens), 0)::bigint                                         as input_tokens,
       coalesce(sum(u.output_tokens), 0)::bigint                                        as output_tokens,
       coalesce(sum(u.reasoning_tokens), 0)::bigint                                     as reasoning_tokens,
       coalesce(sum(u.cache_read_tokens), 0)::bigint                                    as cache_read_tokens,
       coalesce(sum(u.cache_write_tokens), 0)::bigint                                   as cache_write_tokens
from usage_rows u
where u.started_at >= @from_time and u.started_at < @to_time
  and u.routine_id <> ''
  and (@f_origin::text = '' or u.origin = @f_origin)
  and (@f_started_by::text = '' or coalesce(nullif(u.started_by, ''), '-') = @f_started_by)
  and (@f_provider::text = '' or coalesce(u.provider, '') = @f_provider)
  and (@f_model::text = '' or coalesce(u.model, '') = @f_model)
  and (@f_auth_kind::text = '' or coalesce(u.auth_kind, '-') = @f_auth_kind)
  and (@f_credential::text = '' or concat_ws('|', coalesce(u.auth_kind, ''), coalesce(u.provider, ''), coalesce(u.credential_owner, '')) = @f_credential)
  and (@f_routine_id::text = '' or u.routine_id = @f_routine_id)
  and (@f_chat_id::text = '' or u.chat_id = @f_chat_id)
  and (@f_kind::text = '' or u.kind = @f_kind)
group by 1
order by cost_usd desc, turns desc;
