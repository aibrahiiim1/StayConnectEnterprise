-- 0207 -- A SIGN-IN REFUSED AT THE PACKAGE'S DEVICE LIMIT IS RECORDED AS SUCH (Product Owner, 2026-11-04).
--
-- A verified room guest whose stay's package already held its device limit was told so (DEVICE_LIMIT, "disconnect another device"),
-- but the grant completed the attempt with its default, SERVICE_UNAVAILABLE: Guest sign-in attempts showed an internal failure, and the
-- appliance monitor counted it as one. The grant now records DEVICE_LIMIT_REACHED.
--
-- WHAT THIS MIGRATION CHANGES, AND ONLY THAT: the result CHECK gains DEVICE_LIMIT_REACHED, and complete_sign_in_attempt -- the one narrow
-- operation that moves a VERIFIED row to a terminal grant outcome -- accepts it. Its one-way, once-only rule is unchanged. No row is
-- rewritten (rows recorded before stay as they are). No privilege changes: CREATE OR REPLACE keeps the function's owner and its grants.

BEGIN;

ALTER TABLE iam_v2.sign_in_attempts DROP CONSTRAINT IF EXISTS sign_in_attempts_result_check;
ALTER TABLE iam_v2.sign_in_attempts ADD CONSTRAINT sign_in_attempts_result_check
  CHECK (result IN ('VERIFIED','CREDENTIAL_MISMATCH','ROOM_NOT_IN_MIRROR','STAY_NOT_ELIGIBLE',
                    'AMBIGUOUS_ROOM_CANDIDATES','MIRROR_STALE_OR_MISSING_CHANGE','RATE_LIMITED',
                    'ROUTING_OR_INTERFACE_FAILURE','SERVICE_UNAVAILABLE','SPENT_REQUEST_ID',
                    'MALFORMED_SUBMISSION','VERIFIED_NO_ELIGIBLE_PACKAGE',
                    'LICENSE_REFUSED','LICENSE_CAPACITY_REACHED','DEVICE_LIMIT_REACHED'));

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
                      'LICENSE_REFUSED','LICENSE_CAPACITY_REACHED','DEVICE_LIMIT_REACHED') THEN
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
