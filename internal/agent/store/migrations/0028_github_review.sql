-- The pull-request review this session already submitted, so a later turn updates it
-- instead of opening a second one. Zero means this session has not submitted a review.
alter table sessions add column github_review_id bigint not null default 0;
