-- 0210 -- SIGN-IN RECORDS NAME AN ADDRESS CONFLICT, AND A RETRIED GRANT THAT SUCCEEDED IS RECORDED AS A SUCCESS (Product Owner, 2026-11-05).
--
-- Two record defects found on the test appliance:
--   * A device still using an address DHCP has since leased to another device signed in, the grant committed and netd ended the
--     session within the second; the guest was told "service unavailable" eleven times and the attempts said the same. scd now
--     refuses such a sign-in before anything is written and records ADDRESS_CONFLICT.
--   * A grant refused for a retryable reason (the device limit, a transient fault) and then completed on a retry of the same
--     sign-in stayed recorded as the refusal: complete_sign_in_attempt moved a row only while it still read VERIFIED. A VERIFIED
--     completion that carries the session now replaces such a refusal. A success is still never downgraded, and nothing else moves.
--
-- WHAT THIS MIGRATION CHANGES, AND ONLY THAT: the result CHECK gains ADDRESS_CONFLICT, and complete_sign_in_attempt accepts the
-- retried success. No row is rewritten. No privilege changes: CREATE OR REPLACE keeps the function's owner and its grants.

BEGIN;

ALTER TABLE iam_v2.sign_in_attempts DROP CONSTRAINT IF EXISTS sign_in_attempts_result_check;
ALTER TABLE iam_v2.sign_in_attempts ADD CONSTRAINT sign_in_attempts_result_check
  CHECK (result IN ('VERIFIED','CREDENTIAL_MISMATCH','ROOM_NOT_IN_MIRROR','STAY_NOT_ELIGIBLE',
                    'AMBIGUOUS_ROOM_CANDIDATES','MIRROR_STALE_OR_MISSING_CHANGE','RATE_LIMITED',
                    'ROUTING_OR_INTERFACE_FAILURE','SERVICE_UNAVAILABLE','SPENT_REQUEST_ID',
                    'MALFORMED_SUBMISSION','VERIFIED_NO_ELIGIBLE_PACKAGE',
                    'LICENSE_REFUSED','LICENSE_CAPACITY_REACHED','DEVICE_LIMIT_REACHED',
                    'AFTER_CHECKOUT_NEW_DEVICE_REJECTED','ADDRESS_CONFLICT'));

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
                      'LICENSE_REFUSED','LICENSE_CAPACITY_REACHED','DEVICE_LIMIT_REACHED',
                      'AFTER_CHECKOUT_NEW_DEVICE_REJECTED') THEN
    RAISE EXCEPTION 'complete_sign_in_attempt: % is not a terminal grant outcome', p_result;
  END IF;
  UPDATE iam_v2.sign_in_attempts
     SET result         = p_result,
         entitlement_id = COALESCE(p_entitlement, entitlement_id),
         session_id     = COALESCE(p_session, session_id)
   WHERE tenant_id = p_tenant AND site_id = p_site AND request_id = p_request
     AND ((result = 'VERIFIED' AND session_id IS NULL)
          -- A retry of the same sign-in that produced its session replaces a retryable refusal (0210).
          OR (p_result = 'VERIFIED' AND p_session IS NOT NULL
              AND result IN ('DEVICE_LIMIT_REACHED','SERVICE_UNAVAILABLE','LICENSE_CAPACITY_REACHED')
              AND (session_id IS NULL OR session_id = p_session)));
  GET DIAGNOSTICS v_rows = ROW_COUNT;
  RETURN v_rows;
END $fn$;

COMMIT;
