-- The databases beside podium's own. Postgres runs this ONLY on an empty data directory;
-- to add them to an existing install, see docs/operations.md.
create database podium_agent owner podium;   -- the conductor's, migrated by podium-agent
create database podium_memory owner podium;  -- Hindsight's, migrated by Hindsight

\connect podium_memory

-- Hindsight needs pgvector in `public` and would otherwise DROP EXTENSION ... CASCADE and
-- recreate it there itself. podium and podium_agent stay extension-free.
create extension if not exists vector;
