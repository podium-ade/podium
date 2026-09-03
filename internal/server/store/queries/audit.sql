-- name: AppendAudit :one
insert into audit_log (actor, action, subject, details)
values (@actor, @action, @subject, @details)
returning *;

-- name: ListAudit :many
select * from audit_log
where (@action::text = '' or action = @action::text)
  and (@subject::text = '' or subject = @subject::text)
order by id desc
limit @row_limit::int;
