-- 000003_audit_log.up.sql
--
-- Phase 7: a single audit_log table records every authorization-relevant
-- action. "Relevant" means: anything where a user did (or attempted to do)
-- something the service guards. Read-only from the API.
--
-- Notes on the schema:
--
--   * actor_sub is a bare UUID with NO foreign key. Under Option A there
--     is no users table to reference. The sub is the Keycloak subject,
--     which is authoritative; we never need referential integrity on it
--     locally because Keycloak alone decides whether a user "exists".
--
--   * outcome includes 'denied' deliberately. Audit is not just "what
--     happened" — knowing that someone TRIED to delete a task and was
--     refused is exactly what a security review wants. The handler/
--     service records both paths.
--
--   * detail is JSONB so different action types can carry their own
--     shape (a status change carries {"from":..,"to":..}; a delete
--     carries the snapshot of fields that mattered). JSONB indexing is
--     not required yet; the indexes below cover the queries we have.
--
--   * request_id ties an audit row back to the access-log line for the
--     same HTTP request, so an incident response can pivot between the
--     two without guessing.

BEGIN;

CREATE TABLE audit_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_sub   UUID NOT NULL,
    action      TEXT NOT NULL CHECK (action <> ''),
    target_kind TEXT NOT NULL CHECK (target_kind <> ''),
    target_id   UUID NOT NULL,
    outcome     TEXT NOT NULL CHECK (outcome IN ('success', 'denied')),
    detail      JSONB,
    request_id  TEXT
);

-- The most common queries:
--   * "this user's recent actions"          → actor_sub, occurred_at desc
--   * "everything that happened to task X"  → target_kind, target_id
--   * "everything in the last hour"         → occurred_at
-- Three indexes serve all three. They are cheap now on an empty table;
-- adding them later, on a large table, is an operational event.

CREATE INDEX audit_actor_time_idx  ON audit_log(actor_sub, occurred_at DESC);
CREATE INDEX audit_target_idx      ON audit_log(target_kind, target_id);
CREATE INDEX audit_time_idx        ON audit_log(occurred_at DESC);

COMMIT;
