-- 0092 down — the two licence results stop being recordable.
--
-- REFUSES rather than rewrites when a recorded attempt already carries one of them: a sign-in attempt is the
-- evidence of what happened, and relabelling it to fit an older constraint would be falsifying that record.
-- Such rows age out under the thirty-day retention; roll back after they have, or not at all.

BEGIN;

DO $guard$
BEGIN
  IF EXISTS (SELECT 1 FROM iam_v2.sign_in_attempts
              WHERE result IN ('LICENSE_REFUSED','LICENSE_CAPACITY_REACHED')) THEN
    RAISE EXCEPTION '0092 down: recorded sign-in attempts carry a licence result; refusing to rewrite them';
  END IF;
END $guard$;

ALTER TABLE iam_v2.sign_in_attempts DROP CONSTRAINT IF EXISTS sign_in_attempts_result_check;
ALTER TABLE iam_v2.sign_in_attempts ADD CONSTRAINT sign_in_attempts_result_check
  CHECK (result IN ('VERIFIED','CREDENTIAL_MISMATCH','ROOM_NOT_IN_MIRROR','STAY_NOT_ELIGIBLE',
                    'AMBIGUOUS_ROOM_CANDIDATES','MIRROR_STALE_OR_MISSING_CHANGE','RATE_LIMITED',
                    'ROUTING_OR_INTERFACE_FAILURE','SERVICE_UNAVAILABLE','SPENT_REQUEST_ID',
                    'MALFORMED_SUBMISSION','VERIFIED_NO_ELIGIBLE_PACKAGE'));

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
  IF p_result NOT IN ('VERIFIED','SERVICE_UNAVAILABLE','STAY_NOT_ELIGIBLE','VERIFIED_NO_ELIGIBLE_PACKAGE') THEN
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
