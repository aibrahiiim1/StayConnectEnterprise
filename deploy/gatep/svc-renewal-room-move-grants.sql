-- Package renewal and room-move continuity (migration 0213). Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P
-- reconcile reproduces what the migration granted.
--
-- scd reads the purchase rule for the guest offer list (read-only). pmsd's stay engine continues a converted package through the
-- definer function only; it holds no INSERT on purchases, entitlements or device authorizations. The Hotel Admin login reads the
-- restoration record.
--
-- GUARDED on the migration being present (audit B-20).
DO $g$
BEGIN
  IF to_regprocedure('iam_v2.restore_room_move_access(uuid,uuid,uuid,integer,uuid,text,boolean)') IS NULL THEN
    RAISE NOTICE 'migration 0213 is not applied; renewal and room-move grants skipped';
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.stay_package_purchase_refusal(uuid,uuid) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.restore_room_move_access(uuid,uuid,uuid,integer,uuid,text,boolean) TO svc_pmsd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.room_move_restorations TO svc_edged;
  END IF;
END $g$;
