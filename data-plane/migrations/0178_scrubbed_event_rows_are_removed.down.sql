-- 0178 down -- the removal function goes; rows already removed were content-free and are not restored.
BEGIN;
DROP FUNCTION IF EXISTS public.run_event_table_trim(uuid, uuid, integer);
COMMIT;
