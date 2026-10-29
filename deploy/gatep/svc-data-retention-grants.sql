-- Guest-data retention (migration 0127; GDPR remediation plan C1 and F6, Product-Owner decisions D-C1, D-C1a and
-- D-C3). Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P reconcile reproduces exactly what
-- the migration granted.
--
--   svc_scd    runs the hourly retention: EXECUTE on the two run functions (iam_v2 tables and public tables),
--              and reads the setting to size the sign-in-attempt DELETE it already performs. No table privilege
--              is added: every anonymising write happens inside the runs' own definers, and the anonymise_*
--              primitives are granted to nobody.
--   svc_edged  reads and changes the setting through its definer functions (operator and reason required,
--              every change logged), and reads the change log and the run record.
--
-- GUARDED on the migration being present, like every file granting on 0116+ objects (audit B-20).
DO $g$
BEGIN
  IF to_regprocedure('iam_v2.run_data_retention(uuid,uuid,integer)') IS NULL THEN
    RAISE NOTICE 'migration 0127 is not applied; data-retention grants skipped';
    RETURN;
  END IF;
  GRANT EXECUTE ON FUNCTION iam_v2.data_retention_get(uuid,uuid) TO svc_scd;
  GRANT EXECUTE ON FUNCTION iam_v2.run_data_retention(uuid,uuid,integer) TO svc_scd;
  GRANT EXECUTE ON FUNCTION public.run_data_retention_public(uuid,uuid,integer) TO svc_scd;
  -- Migration 0243 (AD-49): removes one-time-code, room-attempt and redirect rows the run above has already scrubbed.
  IF to_regprocedure('public.run_event_table_trim(uuid,uuid,integer)') IS NOT NULL THEN
    GRANT EXECUTE ON FUNCTION public.run_event_table_trim(uuid,uuid,integer) TO svc_scd;
  END IF;

  GRANT EXECUTE ON FUNCTION iam_v2.data_retention_get(uuid,uuid) TO svc_edged;
  -- Migration 0134 (GDPR plan F5) added the system-log period, and 0140 (GDPR audit C4) the staff-note period,
  -- so the setter has gained an argument each time. The staff-note run is granted with 0140.
  IF to_regprocedure('iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,integer,integer,text,text)') IS NOT NULL THEN
    GRANT EXECUTE ON FUNCTION iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,integer,integer,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.run_staff_note_retention(uuid,uuid,integer) TO svc_scd;
  ELSIF to_regprocedure('iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,integer,text,text)') IS NOT NULL THEN
    GRANT EXECUTE ON FUNCTION iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,integer,text,text) TO svc_edged;
  ELSE
    GRANT EXECUTE ON FUNCTION iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,text,text) TO svc_edged;
  END IF;
  GRANT SELECT ON iam_v2.data_retention_changes, iam_v2.data_retention_runs TO svc_edged;
  -- Migration 0144: retention and erasure read a PMS date as a day in the site's own time zone. The iam_v2
  -- definers run as iam_v2_owner, which needs these three columns of public.sites and nothing else.
  IF to_regclass('iam_v2.retention_floor') IS NOT NULL AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    GRANT SELECT (id, tenant_id, timezone) ON public.sites TO iam_v2_owner;
  END IF;
END $g$;
