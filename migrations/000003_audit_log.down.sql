-- 000003_audit_log.down.sql
--
-- Exact inverse of 000003_audit_log.up.sql.
--
-- Dropping audit data is a real loss; an operator running this is doing
-- so deliberately. The migration does what it says; warnings about lost
-- audit history are an operational concern, not a SQL concern.

BEGIN;

DROP INDEX IF EXISTS audit_time_idx;
DROP INDEX IF EXISTS audit_target_idx;
DROP INDEX IF EXISTS audit_actor_time_idx;
DROP TABLE IF EXISTS audit_log;

COMMIT;
