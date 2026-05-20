-- 000002_drop_users.up.sql
--
-- Phase 6, Option A: Keycloak is the SOLE source of truth for users.
-- This project no longer maintains a local users table. The columns that
-- referenced users(id) -- projects.owner_id, tasks.reporter_id,
-- tasks.assignee_id, comments.author_id -- now hold the Keycloak subject
-- claim ("sub", a UUID) directly, as bare UUID columns with NO foreign
-- key. The database no longer knows or cares whether a given user
-- "exists"; only Keycloak knows that.
--
-- CONSEQUENCE (accepted deliberately, see README Phase 6): you can no
-- longer JOIN against users to resolve a name/email for an owner or
-- reporter. Any feature that needs to display user details must call the
-- Keycloak Admin API. This is the cost of maximum single-source-of-truth
-- purity and was chosen with that trade-off explicit.
--
-- Postgres names inline foreign keys deterministically as
-- <table>_<column>_fkey, so we can drop them by that name without
-- guessing. IF EXISTS keeps this safe if a name ever differs.

BEGIN;

-- Drop every FK that references users(id). The columns themselves stay;
-- only the referential constraint is removed.
ALTER TABLE projects DROP CONSTRAINT IF EXISTS projects_owner_id_fkey;
ALTER TABLE tasks    DROP CONSTRAINT IF EXISTS tasks_reporter_id_fkey;
ALTER TABLE tasks    DROP CONSTRAINT IF EXISTS tasks_assignee_id_fkey;
ALTER TABLE comments DROP CONSTRAINT IF EXISTS comments_author_id_fkey;

-- The user-id-bearing indexes are still useful (we filter tasks by
-- assignee, projects by owner), so they are intentionally KEPT. Only the
-- table and its constraints go.

DROP TABLE IF EXISTS users;

COMMIT;
