-- Databases the compose stack needs beside podium's own.
--
-- MOUNTED AT /docker-entrypoint-initdb.d/10-databases.sql, which Postgres runs ONLY when
-- the data directory is empty. On an existing install nothing here happens: create the
-- databases by hand instead. See docs/operations.md → "Adding the agent and memory
-- databases to an existing deployment".
--
--   docker compose exec -T postgres createdb -U podium podium_agent
--   docker compose exec -T postgres createdb -U podium podium_memory
--   docker compose exec -T postgres psql -U podium -d podium_memory \
--       -c 'create extension if not exists vector'
--
-- podium-agent owns podium_agent and migrates it on start. It is a separate database, not a
-- schema in podium's: the conductor is an API client of the control plane and never opens
-- the control plane's schema.
create database podium_agent owner podium;

-- Hindsight owns podium_memory and runs its own Alembic migrations on start.
create database podium_memory owner podium;

\connect podium_memory

-- Hindsight requires the pgvector extension to live in `public`, and creates it itself if
-- it can. We create it up front anyway, for two reasons: it is the only statement here that
-- needs more than CREATEDB, so doing it now leaves the door open to giving Hindsight a
-- non-superuser role later; and Hindsight's migration, finding the extension in another
-- schema, would try to DROP EXTENSION vector CASCADE and recreate it in public.
--
-- podium and podium_agent stay extension-free. Podium's own migrations never reference it.
create extension if not exists vector;
