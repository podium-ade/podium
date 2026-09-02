-- name: CreateEnrollmentToken :one
insert into enrollment_tokens (id, token_hash, labels, expires_at, created_by)
values (@id, @token_hash, @labels, @expires_at, @created_by)
returning *;

-- ConsumeEnrollmentToken is the whole single-use check: one statement, so two racing
-- enrollments cannot both win.
-- name: ConsumeEnrollmentToken :one
update enrollment_tokens
set used_at = now(), used_by_node_id = @used_by_node_id
where token_hash = @token_hash and used_at is null and expires_at > now()
returning *;

-- name: GetEnrollmentTokenByHash :one
select * from enrollment_tokens where token_hash = @token_hash;
