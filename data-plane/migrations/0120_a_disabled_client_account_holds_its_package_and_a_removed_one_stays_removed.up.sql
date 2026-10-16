-- 0120 -- A DISABLED CLIENT ACCOUNT HOLDS ITS PACKAGE, AND A REMOVED ONE STAYS REMOVED
-- (Product-Owner decision D-A, audit remediation 2026-10-16; contract
-- docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md "Staff actions").
--
-- 1. DISABLE / ENABLE. Disabling a client account used to flip guest_access_accounts.enabled and nothing else:
--    every device already online stayed online on the account's package, so "Disable" meant "nobody can sign in
--    AGAIN" while the help said it stops the account being used. The decision: Disable ends the account's
--    sessions (edged, through scd, as Remove does) and moves its live package ACTIVE -> SUSPENDED through the
--    ordinary audited transition writer, with the machine reason ACCOUNT_DISABLED. Enable moves back
--    SUSPENDED -> ACTIVE, reason ACCOUNT_ENABLED -- but only a package THIS rule suspended (its live head says
--    ACCOUNT_DISABLED; an over-budget suspension is not lifted by re-enabling an account) and only while it is
--    still inside its window. The validity clock keeps running while suspended: window_ends_at is not touched,
--    and the expiry sweep terminates a SUSPENDED package whose window ends exactly as it terminates an ACTIVE
--    one.
--
--    iam_v2.account_entitlements_follow_access aligns the account's packages with the account's CURRENT enabled
--    flag, read inside the caller's transaction, so edged calls it right after its own UPDATE and the two
--    commit together. It changes nothing else: no consumption, no window, no device place, no history row other
--    than the transition itself.
--
-- 2. REMOVED IS FINAL. Remove (contract "Remove", guest_accounts_manage.go) disables the account and moves its
--    username to "<name>.removed-<id8>.invalid", keeping its history. A plain PATCH could bring it back
--    ({"enabled":true}, or a username). The API now refuses that, and this trigger refuses it underneath the
--    API, for any writer: a removed account's enabled flag and username can never change again.
--
-- Additive. No existing row is changed.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Disable suspends, Enable resumes.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.account_entitlements_follow_access(p_tenant uuid, p_site uuid, p_account uuid)
  RETURNS TABLE (suspended integer, resumed integer)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_enabled boolean;
  e         record;
  v_s       integer := 0;
  v_r       integer := 0;
BEGIN
  SELECT a.enabled INTO v_enabled
    FROM iam_v2.guest_access_accounts a
   WHERE a.id = p_account AND a.tenant_id = p_tenant AND a.site_id = p_site
   FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'client account % does not exist on this site', p_account USING ERRCODE = 'no_data_found';
  END IF;

  IF NOT v_enabled THEN
    FOR e IN
      SELECT en.id FROM iam_v2.entitlements en
       WHERE en.tenant_id = p_tenant AND en.site_id = p_site AND en.guest_account_id = p_account
         AND en.status = 'ACTIVE'
       ORDER BY en.id
       FOR UPDATE
    LOOP
      PERFORM iam_v2.apply_entitlement_transition(e.id, 'SUSPENDED', now(), 'ACCOUNT_DISABLED');
      v_s := v_s + 1;
    END LOOP;
  ELSE
    FOR e IN
      SELECT en.id FROM iam_v2.entitlements en
       WHERE en.tenant_id = p_tenant AND en.site_id = p_site AND en.guest_account_id = p_account
         AND en.status = 'SUSPENDED'
         AND (en.window_ends_at IS NULL OR en.window_ends_at > now())
         AND (SELECT t.reason FROM iam_v2.entitlement_state_transitions t
               WHERE t.entitlement_id = en.id AND t.superseded_by IS NULL
               ORDER BY t.seq DESC LIMIT 1) = 'ACCOUNT_DISABLED'
       ORDER BY en.id
       FOR UPDATE
    LOOP
      PERFORM iam_v2.apply_entitlement_transition(e.id, 'ACTIVE', now(), 'ACCOUNT_ENABLED');
      v_r := v_r + 1;
    END LOOP;
  END IF;

  suspended := v_s; resumed := v_r;
  RETURN NEXT;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.account_entitlements_follow_access(uuid,uuid,uuid) FROM PUBLIC;
COMMENT ON FUNCTION iam_v2.account_entitlements_follow_access(uuid,uuid,uuid) IS
  'Aligns a client account''s live packages with its enabled flag (0120, decision D-A): disabled -> every ACTIVE '
  'package SUSPENDED (reason ACCOUNT_DISABLED); enabled -> every package that rule suspended, still inside its '
  'window, ACTIVE again (reason ACCOUNT_ENABLED). Through apply_entitlement_transition only; the window, '
  'consumption and device places are untouched. Sessions are ended by the caller through scd.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. A removed account can never be enabled or renamed again.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.guest_access_account_removed_is_final() RETURNS trigger
LANGUAGE plpgsql AS $fn$
BEGIN
  IF NOT OLD.enabled AND lower(OLD.username) LIKE '%.removed-%.invalid'
     AND (NEW.enabled OR NEW.username IS DISTINCT FROM OLD.username) THEN
    RAISE EXCEPTION 'client account % has been removed: it cannot be enabled or renamed', OLD.id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.guest_access_account_removed_is_final() FROM PUBLIC;
DROP TRIGGER IF EXISTS guest_access_account_removed_is_final ON iam_v2.guest_access_accounts;
CREATE TRIGGER guest_access_account_removed_is_final
  BEFORE UPDATE OF enabled, username ON iam_v2.guest_access_accounts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.guest_access_account_removed_is_final();

-- ---------------------------------------------------------------------------------------------------------
-- 3. Ownership and least privilege, mirrored in deploy/gatep/svc-client-accounts-grants.sql.
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.account_entitlements_follow_access(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_access_account_removed_is_final() OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.account_entitlements_follow_access(uuid,uuid,uuid) TO svc_edged;
  END IF;
END $g$;

COMMIT;
