-- 000001_init.up.sql
--
-- The initial schema: projects, users, tasks, comments.
--
-- Conventions used throughout:
--
--   * Primary keys are UUID with DEFAULT gen_random_uuid(). The database
--     owns identity — application code never supplies an id on insert and
--     reads the generated value back via INSERT ... RETURNING.
--     gen_random_uuid() is built into Postgres 13+ (pgcrypto no longer
--     required), which the docker-compose image satisfies.
--
--   * created_at / updated_at are timestamptz, never plain timestamp.
--     Storing wall-clock time without a zone is a classic production bug;
--     timestamptz stores an absolute instant.
--
--   * updated_at is maintained by a trigger (defined below) rather than
--     trusted to application code. Any path that mutates a row — including
--     a manual psql UPDATE during an incident — gets a correct
--     updated_at. Application code that also sets it is harmless but
--     redundant.
--
--   * Status and priority are stored as plain text, not a Postgres ENUM.
--     The legal set lives in workflow.yaml and is enforced by the
--     application validator (Phase 2). A database ENUM would be a second,
--     competing source of truth that requires a migration to change —
--     exactly the rigidity workflow.yaml exists to avoid. We DO add a
--     CHECK that the column is non-empty, which is a cheap structural
--     guard without duplicating the policy.

BEGIN;

-- ── updated_at trigger function ────────────────────────────────────────
-- One shared function, attached to every table that has updated_at.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ── users ──────────────────────────────────────────────────────────────
-- The id is the Keycloak subject ("sub" claim) once Phase 6 lands, so we
-- do NOT default it here — it is supplied by the application. Until then
-- the table simply exists; no repository writes to it yet.
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

-- ── projects ───────────────────────────────────────────────────────────
CREATE TABLE projects (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key         TEXT NOT NULL UNIQUE CHECK (key <> ''),
    name        TEXT NOT NULL CHECK (name <> ''),
    description TEXT NOT NULL DEFAULT '',
    owner_id    UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX projects_owner_id_idx ON projects(owner_id);

CREATE TRIGGER projects_set_updated_at
    BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── tasks ──────────────────────────────────────────────────────────────
-- assignee_id is nullable: a task can be unassigned. reporter_id is not:
-- every task has a creator. due_date is nullable: not every task has a
-- deadline. The domain model represents these absences with uuid.Nil /
-- zero time.Time; the repository maps NULL <-> zero value.
CREATE TABLE tasks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    title       TEXT NOT NULL CHECK (title <> ''),
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL CHECK (status <> ''),
    priority    TEXT NOT NULL CHECK (priority <> ''),
    assignee_id UUID REFERENCES users(id) ON DELETE SET NULL,
    reporter_id UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    due_date    TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- These indexes back the most common query patterns the task list API
-- will use (filter by project, by assignee, by status). Adding them now
-- is cheap on an empty table; adding them later on a large table is an
-- operational event.
CREATE INDEX tasks_project_id_idx  ON tasks(project_id);
CREATE INDEX tasks_assignee_id_idx ON tasks(assignee_id);
CREATE INDEX tasks_status_idx      ON tasks(status);

CREATE TRIGGER tasks_set_updated_at
    BEFORE UPDATE ON tasks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ── comments ───────────────────────────────────────────────────────────
-- edited_at is nullable: NULL means "never edited". The domain maps NULL
-- <-> zero time.Time.
CREATE TABLE comments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id    UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    author_id  UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    body       TEXT NOT NULL CHECK (body <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    edited_at  TIMESTAMPTZ
);

CREATE INDEX comments_task_id_idx ON comments(task_id);

COMMIT;
