-- PHASE 6 — the runtime privilege shape for Guest Device Self-Service.
--
-- THE TRAP THIS AVOIDS.
--
-- p6_guest_release_device calls iam_v2.deauthorize_entitlement_device, so the naive way to make the guest
-- path work is to grant the guest-facing role EXECUTE on that primitive. That would be a disaster dressed as
-- a dependency fix: deauthorize_entitlement_device releases ANY device on ANY entitlement, with no throttle,
-- no own-subject scoping, no offline check and no audit row. Granting it would hand the guest-facing role a
-- complete bypass around every policy the Self-Service surface exists to enforce, and nothing in the code
-- would look wrong.
--
-- THE ANSWER IS THE ONE PHASE 3 ALREADY USES. authorize_entitlement_device is SECURITY DEFINER precisely so
-- callers can perform a controlled operation without holding the privileges the operation needs. The release
-- becomes SECURITY DEFINER for the same reason: the runtime role is granted EXECUTE on the POLICY function
-- and on nothing beneath it. The only device release it can perform is one that passed the throttle, the
-- ownership scope, the offline check and the audit -- because that is the only door it has a key to.
--
-- WHAT EACH ROLE GETS, and why it is the minimum:
--
--   svc_scd  — EXECUTE on p6_guest_release_device (the policy surface, nothing under it), SELECT on the four
--              tables the listing reads, and SELECT/INSERT on the guest action audit. It gets NO grant on
--              deauthorize_entitlement_device, NO write on entitlement_devices, and NO write on
--              entitlement_device_authorizations. It cannot release a device except through the policy.
--
--   svc_edged — SELECT/INSERT/UPDATE on the per-appliance setting and INSERT on its audit. The operator
--              surface changes a setting; it does not touch guest device state at all, so it is granted
--              nothing that would let it.
--
-- The session-binding guard added by 0032 runs as a BEFORE trigger in the INVOKER's context, so the
-- session-admission role must be able to READ entitlement_devices for the trigger to work. That is one
-- SELECT on one table -- not the broad write access that "make the trigger work" could easily have become.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The policy function becomes the privilege boundary
-- ---------------------------------------------------------------------------------------------------------
-- SECURITY DEFINER with a pinned search_path, exactly as the Phase-3 primitives are written. Without the
-- pinned path a caller could shadow iam_v2 and have the definer execute their own code.
ALTER FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int)
  SECURITY DEFINER SET search_path = iam_v2, pg_temp;

COMMENT ON FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int) IS
  'The guest device release, and the PRIVILEGE BOUNDARY for it. SECURITY DEFINER so the guest-facing runtime '
  'role can perform a controlled release without ever holding EXECUTE on deauthorize_entitlement_device -- '
  'which would release any device on any entitlement with no throttle, no ownership scope, no offline check '
  'and no audit. The only release the runtime can perform is one that passed all four.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. Roles. Created only if the platform has not already made them (Gate P owns their lifecycle).
-- ---------------------------------------------------------------------------------------------------------
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    CREATE ROLE svc_scd NOLOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    CREATE ROLE svc_edged NOLOGIN;
  END IF;
END $$;

GRANT USAGE ON SCHEMA iam_v2 TO svc_scd, svc_edged;

-- ---------------------------------------------------------------------------------------------------------
-- 3. svc_scd: the guest surface
-- ---------------------------------------------------------------------------------------------------------
-- The policy function, and nothing beneath it.
GRANT EXECUTE ON FUNCTION iam_v2.p6_guest_release_device(uuid, uuid, int) TO svc_scd;

-- The listing. Read-only, and entitlement_devices is also what the 0032 session guard reads on the
-- admission path, which is the same grant rather than a second one.
GRANT SELECT ON iam_v2.entitlement_devices, iam_v2.devices,
                iam_v2.entitlement_device_authorizations TO svc_scd;

-- Sessions: SELECT to read online state, INSERT because scd's admission path opens the session itself
-- (openSessionTx). NOT UPDATE -- promoting PENDING_ENFORCEMENT to active is the enforcement owner's write,
-- not the guest surface's, and granting it here would let scd declare a guest online without the kernel
-- having authorized a packet.
GRANT SELECT, INSERT ON iam_v2.sessions TO svc_scd;

-- The audit. INSERT because a LIST is itself an auditable action; SELECT because the throttle counts it.
GRANT SELECT, INSERT ON iam_v2.guest_device_actions TO svc_scd;

-- The per-appliance setting is READ by the guest surface to decide whether the feature exists at all, and
-- never written by it.
GRANT SELECT ON iam_v2.appliance_product_settings TO svc_scd;

-- ---------------------------------------------------------------------------------------------------------
-- 4. svc_edged: the operator surface
-- ---------------------------------------------------------------------------------------------------------
GRANT SELECT, INSERT, UPDATE ON iam_v2.appliance_product_settings TO svc_edged;
GRANT SELECT, INSERT ON iam_v2.appliance_product_setting_changes TO svc_edged;

-- ---------------------------------------------------------------------------------------------------------
-- 5. What is deliberately NOT granted
-- ---------------------------------------------------------------------------------------------------------
-- Stated as executable assertions rather than as a comment, so the claim is checked every time the migration
-- runs rather than believed because it is written down.
DO $$
DECLARE bad text;
BEGIN
  -- No runtime role may execute the internal device-authorization primitives directly.
  SELECT string_agg(format('%s on %s', r.rolname, p.proname), ', ') INTO bad
    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace,
         (VALUES ('svc_scd'), ('svc_edged')) AS r(rolname)
   WHERE n.nspname = 'iam_v2'
     AND p.proname IN ('deauthorize_entitlement_device', 'authorize_entitlement_device',
                       'p6_record_time_termination')
     AND has_function_privilege(r.rolname, p.oid, 'EXECUTE');
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION '0033 would leave a bypass around the Self-Service policy: %', bad;
  END IF;

  -- scd must not be able to promote a session to active: that is the enforcement owner's write, and holding
  -- it would let the guest surface claim a guest is online before any packet was authorized.
  IF has_table_privilege('svc_scd', 'iam_v2.sessions', 'UPDATE') THEN
    RAISE EXCEPTION '0033 would let svc_scd UPDATE sessions, which includes promoting one to active';
  END IF;

  -- No runtime role may write device authorization state directly.
  SELECT string_agg(format('%s can %s %s', r.rolname, pr.priv, t.tbl), ', ') INTO bad
    FROM (VALUES ('svc_scd'), ('svc_edged')) AS r(rolname),
         (VALUES ('entitlement_devices'), ('entitlement_device_authorizations')) AS t(tbl),
         (VALUES ('INSERT'), ('UPDATE'), ('DELETE')) AS pr(priv)
   WHERE has_table_privilege(r.rolname, 'iam_v2.' || t.tbl, pr.priv);
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION '0033 would leave direct device-authorization writes: %', bad;
  END IF;

  -- PUBLIC gets nothing, on any Phase-6 function.
  SELECT string_agg(p.proname, ', ') INTO bad
    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
   WHERE n.nspname = 'iam_v2' AND p.proname LIKE 'p6!_%' ESCAPE '!'
     AND has_function_privilege('public', p.oid, 'EXECUTE');
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION '0033 would leave PUBLIC executing: %', bad;
  END IF;
END $$;

INSERT INTO public.schema_migrations (version) VALUES ('0033_phase6_runtime_least_privilege')
  ON CONFLICT (version) DO NOTHING;

COMMIT;
