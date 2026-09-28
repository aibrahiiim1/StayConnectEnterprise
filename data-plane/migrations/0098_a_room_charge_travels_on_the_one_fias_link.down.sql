-- Roll back 0098: pmsd can no longer confirm a posting command, so no room charge can be carried.
BEGIN;
DROP FUNCTION IF EXISTS iam_v2.p4_posting_command_authorised(uuid,text,text);
-- The attempt identity without the command hash (as before 0098).
CREATE OR REPLACE FUNCTION iam_v2.trg_posting_attempt_oneway() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'posting_attempts is not deletable'; END IF;
  IF ROW(NEW.p_number,NEW.rn,NEW.g_number,NEW.sent_at,NEW.internal_posting_id,NEW.attempt_no,NEW.pms_interface_id)
     IS DISTINCT FROM ROW(OLD.p_number,OLD.rn,OLD.g_number,OLD.sent_at,OLD.internal_posting_id,OLD.attempt_no,OLD.pms_interface_id)
  THEN RAISE EXCEPTION 'posting_attempts identity is immutable'; END IF;
  IF OLD.outcome <> 'SENDING' AND NEW.outcome <> OLD.outcome THEN
     RAISE EXCEPTION 'posting_attempts.outcome is terminal (% -> %)', OLD.outcome, NEW.outcome; END IF;
  IF NEW.outcome = 'SENDING' AND OLD.outcome <> 'SENDING' THEN
     RAISE EXCEPTION 'posting_attempts.outcome cannot return to SENDING'; END IF;
  RETURN NEW;
END; $$;
ALTER TABLE iam_v2.posting_attempts DROP CONSTRAINT IF EXISTS attempt_ps_sha256_shape;
ALTER TABLE iam_v2.posting_attempts DROP COLUMN IF EXISTS ps_sha256;
COMMIT;
