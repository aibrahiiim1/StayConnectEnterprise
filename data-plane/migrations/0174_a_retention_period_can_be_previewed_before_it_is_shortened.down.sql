-- 0174 down: the preview functions are dropped; nothing else changed.
BEGIN;
DROP FUNCTION IF EXISTS public.data_retention_preview(uuid, uuid, integer, integer, integer, integer, integer, integer, integer, integer);
DROP FUNCTION IF EXISTS public.data_retention_dry_run(uuid, uuid, boolean, integer, integer, integer, integer, integer, integer, integer, integer);
COMMIT;
