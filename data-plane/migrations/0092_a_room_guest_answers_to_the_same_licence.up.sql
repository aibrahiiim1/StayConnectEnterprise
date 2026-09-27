-- 0092 — A ROOM GUEST ANSWERS TO THE SAME LICENCE AS EVERY OTHER GUEST.
--
-- Product-Owner decision: PMS room sign-in follows the SAME licence and concurrent-capacity contract as
-- vouchers, accounts, OTP and social sign-in. The room grant used to insert its session with no licence gate
-- and no concurrent-guest reservation, so an unlicensed, expired or full appliance still admitted every PMS
-- guest. scd now asks the same licenseRefusal and makes the same reserveLicensedSlot reservation on that
-- path, and records the two refusals as sign-in attempts like every other refusal on it.
--
-- WHAT THIS MIGRATION CHANGES, AND ONLY THAT. Two new results become recordable:
--
--   LICENSE_REFUSED           the licence did not permit a new guest (no valid licence, expired, suspended,
--                             the PMS feature not entitled, or a cross-tenant transition pending). Written
--                             by the resolve as a new row, or by the grant onto the row the resolve wrote.
--   LICENSE_CAPACITY_REACHED  the guest was verified and the stay had access, and the appliance was at the
--                             concurrent-online-guest cap in its signed licence. Written by the grant.
--
-- So the result CHECK gains both, and complete_sign_in_attempt -- the one narrow operation that moves a
-- VERIFIED row to a terminal grant outcome -- accepts both as terminal outcomes. Its one-way, once-only rule
-- is unchanged: it still acts only on a row that is VERIFIED with no session.
--
-- No row is rewritten. No privilege changes: CREATE OR REPLACE keeps the function's owner and its grants.

BEGIN;

ALTER TABLE iam_v2.sign_in_attempts DROP CONSTRAINT IF EXISTS sign_in_attempts_result_check;
ALTER TABLE iam_v2.sign_in_attempts ADD CONSTRAINT sign_in_attempts_result_check
  CHECK (result IN ('VERIFIED','CREDENTIAL_MISMATCH','ROOM_NOT_IN_MIRROR','STAY_NOT_ELIGIBLE',
                    'AMBIGUOUS_ROOM_CANDIDATES','MIRROR_STALE_OR_MISSING_CHANGE','RATE_LIMITED',
                    'ROUTING_OR_INTERFACE_FAILURE','SERVICE_UNAVAILABLE','SPENT_REQUEST_ID',
                    'MALFORMED_SUBMISSION','VERIFIED_NO_ELIGIBLE_PACKAGE',
                    'LICENSE_REFUSED','LICENSE_CAPACITY_REACHED'));

CREATE OR REPLACE FUNCTION iam_v2.complete_sign_in_attempt(
    p_tenant uuid, p_site uuid, p_request uuid,
    p_result text, p_entitlement uuid, p_session uuid)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_rows integer;
BEGIN
  IF p_request IS NULL THEN
    RETURN 0;
  END IF;
  IF p_result NOT IN ('VERIFIED','SERVICE_UNAVAILABLE','STAY_NOT_ELIGIBLE','VERIFIED_NO_ELIGIBLE_PACKAGE',
                      'LICENSE_REFUSED','LICENSE_CAPACITY_REACHED') THEN
    RAISE EXCEPTION 'complete_sign_in_attempt: % is not a terminal grant outcome', p_result;
  END IF;
  UPDATE iam_v2.sign_in_attempts
     SET result         = p_result,
         entitlement_id = COALESCE(p_entitlement, entitlement_id),
         session_id     = COALESCE(p_session, session_id)
   WHERE tenant_id = p_tenant AND site_id = p_site AND request_id = p_request
     AND result = 'VERIFIED' AND session_id IS NULL;
  GET DIAGNOSTICS v_rows = ROW_COUNT;
  RETURN v_rows;
END $fn$;

COMMIT;
