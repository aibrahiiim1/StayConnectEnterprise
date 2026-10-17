-- Backup key holders: backups are encrypted to the hotel's own keys (migration 0129; GDPR remediation plan C5,
-- Product-Owner decision D-C5). Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P reconcile
-- reproduces exactly what the migration granted.
--
--   svc_edged  reads the register (every column but the check-code hash) and its event log, and changes it only
--              through the definer functions: add a holder, confirm its check code, remove it (never the last),
--              record a restore test.
--   svc_scd    learns the public keys to encrypt to, and nothing else.
--
-- GUARDED on the migration being present, like every file granting on 0116+ objects (audit B-20).
DO $g$
BEGIN
  IF to_regprocedure('public.backup_key_recipients(uuid,uuid)') IS NULL THEN
    RAISE NOTICE 'migration 0129 is not applied; backup-key grants skipped';
    RETURN;
  END IF;
  GRANT SELECT (id, tenant_id, site_id, label, public_key, status, created_at, created_by, confirmed_at,
                removed_at, removed_by, remove_reason, last_tested_at) ON public.backup_key_holders TO svc_edged;
  GRANT SELECT ON public.backup_key_holder_events TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.backup_key_holder_add(uuid,uuid,text,text,text,boolean,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.backup_key_holder_confirm(uuid,uuid,uuid,text,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.backup_key_holder_remove(uuid,uuid,uuid,text,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.backup_key_holder_tested(uuid,uuid,text,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.backup_key_recipients(uuid,uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.backup_key_recipients(uuid,uuid) TO svc_scd;
END $g$;
