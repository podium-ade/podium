-- name: GetInstance :one
select * from instance where id = 1;

-- name: InsertInstance :one
insert into instance (hosted_domain, claimed_by)
values (@hosted_domain, @claimed_by)
returning *;