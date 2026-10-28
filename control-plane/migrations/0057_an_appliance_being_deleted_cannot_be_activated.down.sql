-- 0057 down -- the deletion marker goes; a delete in progress is no longer visible to activation.

BEGIN;

ALTER TABLE appliances DROP COLUMN IF EXISTS deletion_started_at;

COMMIT;
