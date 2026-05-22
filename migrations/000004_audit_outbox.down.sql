-- 000004_audit_outbox.down.sql
--
-- Reverses 000004. Dropping audit_outbox while the drainer is running is
-- an operational coordination problem the SQL doesn't try to solve: stop
-- the app first. A running drainer against a dropped table will produce
-- noisy errors until the next deploy.

BEGIN;

DROP INDEX IF EXISTS audit_outbox_pending_idx;
DROP TABLE IF EXISTS audit_outbox;

COMMIT;
