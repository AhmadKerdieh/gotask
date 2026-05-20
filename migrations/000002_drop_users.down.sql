-- 000002_drop_users.down.sql
--
-- Reverses 000002 at the SCHEMA level: recreates the users table and the
-- four foreign keys exactly as 000001 defined them.
--
-- HONEST LIMITATION: this restores the schema, NOT the data. The rows in
-- users were destroyed by the up migration's DROP TABLE; a down migration
-- cannot resurrect them. If you roll back, the users table exists again
-- but is empty, and re-adding the foreign keys will FAIL if tasks/projects
-- already contain owner_id/reporter_id values that no longer have a
-- matching users row (which, post-Option-A, they all do).
--
-- This is called out loudly rather than hidden: a down migration that
-- silently cannot fully reverse is a trap. In practice, rolling 000002
-- back is only safe on an empty dataset (e.g. local dev reset), which is
-- the only realistic scenario for reversing a fundamental architecture
-- decision anyway.

BEGIN;

CREATE TABLE users (
    id         UUID PRIMARY KEY,
    email      TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    role       TEXT NOT NULL CHECK (role <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Re-add the foreign keys with the same semantics 000001 used. These
-- will error if existing data violates them; see the limitation note
-- above. That failure is correct behaviour, not a bug in this file.
ALTER TABLE projects
    ADD CONSTRAINT projects_owner_id_fkey
    FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE RESTRICT;

ALTER TABLE tasks
    ADD CONSTRAINT tasks_reporter_id_fkey
    FOREIGN KEY (reporter_id) REFERENCES users(id) ON DELETE RESTRICT;

ALTER TABLE tasks
    ADD CONSTRAINT tasks_assignee_id_fkey
    FOREIGN KEY (assignee_id) REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE comments
    ADD CONSTRAINT comments_author_id_fkey
    FOREIGN KEY (author_id) REFERENCES users(id) ON DELETE RESTRICT;

COMMIT;
