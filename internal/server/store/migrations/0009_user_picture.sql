-- Google Workspace avatars. Empty until a Google sign-in has seen a picture URL.
alter table users add column if not exists picture_url text;
