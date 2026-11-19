-- RecordHousekeeping records one model call the conductor made for itself (0056).
-- name: RecordHousekeeping :exec
insert into housekeeping_spend (id, session_id, purpose, status, started_at, finished_at,
                                num_turns, cost_usd, agent, model, effort, provider,
                                auth_kind, credential_owner, credential_account,
                                input_tokens, output_tokens, reasoning_tokens,
                                cache_read_tokens, cache_write_tokens)
values (@id, @session_id, @purpose, @status, @started_at, @finished_at,
        @num_turns, @cost_usd, nullif(@agent::text, ''), nullif(@model::text, ''),
        nullif(@effort::text, ''), nullif(@provider::text, ''),
        nullif(@auth_kind::text, ''), @credential_owner::text, @credential_account::text,
        @input_tokens, @output_tokens, @reasoning_tokens, @cache_read_tokens, @cache_write_tokens);
