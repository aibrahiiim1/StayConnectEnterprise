-- Sign-in limits and capacity settings (migration 0162; audit decisions AD-30/31/34/51/52/58). Idempotent;
-- included by gatep-grants.sql after its revoke, so a Gate-P reconcile reproduces what the migration granted.
--   svc_scd    reads the effective values; prunes idle devices through the definer (no table privilege).
--   svc_edged  reads and changes them through the definers (operator and reason required) and reads the change log.
-- GUARDED on the migration being present (audit B-20).
DO $g0170$
BEGIN
  IF to_regprocedure('iam_v2.signin_capacity_settings_get(uuid,uuid)') IS NULL THEN
    RAISE NOTICE 'migration 0162 is not applied; sign-in capacity grants skipped';
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_get(uuid,uuid) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.prune_idle_devices(uuid,uuid,integer) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.signin_capacity_settings_set(uuid,uuid,jsonb,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.signin_capacity_settings_changes TO svc_edged;
  END IF;
END $g0170$;

-- Migration 0163 (AD-51): the durable throttle charge in one round trip. SECURITY INVOKER over the bucket table scd
-- already writes; the function itself must be granted because the Gate-P revoke covers every function in public.
DO $g0171$
BEGIN
  IF to_regprocedure('public.auth_throttle_charge(text[],text[],text[],integer[],integer[],boolean[],timestamptz,timestamptz,integer)') IS NULL THEN
    RAISE NOTICE 'migration 0163 is not applied; throttle charge grant skipped';
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION public.auth_throttle_charge(text[],text[],text[],integer[],integer[],boolean[],timestamptz,timestamptz,integer) TO svc_scd;
  END IF;
END $g0171$;
