-- 000001_init.down.sql
--
-- The EXACT inverse of 000001_init.up.sql.
--
-- Drop order is the reverse of create order because of foreign keys:
-- comments references tasks references projects references users. Dropping
-- a parent before its children would error (or require CASCADE, which
-- hides mistakes). We drop children first so every DROP is unambiguous.
--
-- This file is not decoration. The contract for every migration is that
-- `migrate down` followed by `migrate up` returns the schema to a
-- byte-identical state. Test it that way — an untested down migration is
-- a story you tell yourself, not a rollback you can rely on.

BEGIN;

DROP TABLE IF EXISTS comments;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS users;

-- The trigger function is shared, so it is dropped only after every table
-- (and therefore every trigger that depends on it) is gone.
DROP FUNCTION IF EXISTS set_updated_at();

COMMIT;
