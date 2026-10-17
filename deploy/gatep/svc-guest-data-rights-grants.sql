-- Guest data rights: look up, export and erase one guest (migration 0128; GDPR remediation plan C3 and C4,
-- Product-Owner decisions D-C3 and D-C4). Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P
-- reconcile reproduces exactly what the migration granted.
--
--   svc_edged  runs the lookup, the export and the erasure through their definer functions (who may is edged's
--              guest-data-rights key: site_admin and the IT manager, password and reason, audited), and reads
--              the erasure record. No table privilege on guest data is added: the scope, the export records and
--              the iam_v2 half of the erasure are granted to nobody.
--
-- GUARDED on the migration being present, like every file granting on 0116+ objects (audit B-20).
DO $g$
BEGIN
  IF to_regprocedure('public.guest_data_erase(uuid,uuid,text,uuid,text,text)') IS NULL THEN
    RAISE NOTICE 'migration 0128 is not applied; guest-data-rights grants skipped';
    RETURN;
  END IF;
  GRANT EXECUTE ON FUNCTION iam_v2.guest_data_find(uuid,uuid,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.guest_data_export(uuid,uuid,text,uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.guest_data_erase(uuid,uuid,text,uuid,text,text) TO svc_edged;
  GRANT SELECT ON iam_v2.guest_data_erasures TO svc_edged;
END $g$;
