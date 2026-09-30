-- A ROOM CHARGE TRAVELS ON THE ONE FIAS LINK, AND pmsd ONLY CARRIES IT.
--
-- Product-Owner decision D45 (docs/architecture/ONEGATE_MODULES_AND_ACQUISITION.md section 11): the property's
-- PMS accepts one FIAS client connection and pmsd owns it. There is no second connection and no second
-- interface for posting. The financial path (the posting engine, run by scd as svc_posting) validates, builds
-- the PS, records the attempt and hands the exact bytes to pmsd; pmsd writes them on its link and returns the
-- matched PA; the financial path records the outcome and settles only on PA=OK.
--
-- What this migration adds is the proof that pmsd carries ONLY what the financial path authorised:
--
--   1. posting_attempts.ps_sha256 -- the SHA-256 of the exact PS body the attempt authorises, written by the
--      engine in the same transaction as the attempt, before any byte exists.
--   2. p4_posting_command_authorised(interface, P#, sha256) -- a read-only definer check pmsd must pass before it
--      writes a PS: there is exactly one attempt for that interface and P#, it is still SENDING, it is recent,
--      its hash equals the bytes pmsd was handed, its posting is an in-flight CHARGE, and the interface is still
--      financially ready. pmsd holds EXECUTE on this and NO privilege on any posting table: it can ask, and it
--      can record nothing. It cannot create, edit, retry or settle a posting.
--
-- A command that has been answered, declined, recovered as UNKNOWN or is merely old is no longer SENDING or no
-- longer recent, so a replayed command is refused rather than transmitted twice.
--
-- Real posting still requires STAYCONNECT_PHASE4_PMS_TRANSMIT on BOTH scd and pmsd, which requires a separate
-- Product-Owner authorisation.

BEGIN;

ALTER TABLE iam_v2.posting_attempts ADD COLUMN IF NOT EXISTS ps_sha256 text;
ALTER TABLE iam_v2.posting_attempts DROP CONSTRAINT IF EXISTS attempt_ps_sha256_shape;
ALTER TABLE iam_v2.posting_attempts ADD CONSTRAINT attempt_ps_sha256_shape
  CHECK (ps_sha256 IS NULL OR ps_sha256 ~ '^[0-9a-f]{64}$');
COMMENT ON COLUMN iam_v2.posting_attempts.ps_sha256 IS
  'SHA-256 (lowercase hex) of the exact PS body this attempt authorises. pmsd transmits a PS only when '
  'p4_posting_command_authorised confirms a SENDING attempt carrying this hash (decision D45).';

-- The command is part of the attempt's immutable identity: once recorded, its hash can never be changed.
CREATE OR REPLACE FUNCTION iam_v2.trg_posting_attempt_oneway() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'posting_attempts is not deletable'; END IF;
  IF ROW(NEW.p_number,NEW.rn,NEW.g_number,NEW.sent_at,NEW.internal_posting_id,NEW.attempt_no,NEW.pms_interface_id,NEW.ps_sha256)
     IS DISTINCT FROM ROW(OLD.p_number,OLD.rn,OLD.g_number,OLD.sent_at,OLD.internal_posting_id,OLD.attempt_no,OLD.pms_interface_id,OLD.ps_sha256)
  THEN RAISE EXCEPTION 'posting_attempts identity is immutable'; END IF;
  IF OLD.outcome <> 'SENDING' AND NEW.outcome <> OLD.outcome THEN
     RAISE EXCEPTION 'posting_attempts.outcome is terminal (% -> %)', OLD.outcome, NEW.outcome; END IF;
  IF NEW.outcome = 'SENDING' AND OLD.outcome <> 'SENDING' THEN
     RAISE EXCEPTION 'posting_attempts.outcome cannot return to SENDING'; END IF;
  RETURN NEW;
END; $$;

CREATE OR REPLACE FUNCTION iam_v2.p4_posting_command_authorised(p_iface uuid, p_pnum text, p_sha256 text)
  RETURNS text
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE a record; n int; v_outbox text; v_type text; rdy record;
BEGIN
  IF p_iface IS NULL OR p_pnum IS NULL OR p_pnum !~ '^[0-9]{1,18}$' OR p_sha256 IS NULL OR p_sha256 !~ '^[0-9a-f]{64}$' THEN
    RETURN 'COMMAND_MALFORMED';
  END IF;
  SELECT count(*) INTO n FROM iam_v2.posting_attempts WHERE pms_interface_id = p_iface AND p_number = p_pnum;
  IF n = 0 THEN RETURN 'NO_SUCH_ATTEMPT'; END IF;
  IF n > 1 THEN RETURN 'P_NUMBER_NOT_UNIQUE'; END IF;
  SELECT * INTO a FROM iam_v2.posting_attempts x WHERE x.pms_interface_id = p_iface AND x.p_number = p_pnum;
  IF a.outcome <> 'SENDING' THEN RETURN 'ATTEMPT_NOT_SENDING'; END IF;
  IF a.ps_sha256 IS NULL OR a.ps_sha256 <> p_sha256 THEN RETURN 'COMMAND_BYTES_NOT_AUTHORISED'; END IF;
  IF a.sent_at < now() - interval '2 minutes' THEN RETURN 'ATTEMPT_TOO_OLD'; END IF;
  SELECT p.posting_type INTO v_type FROM iam_v2.pms_postings p WHERE p.id = a.internal_posting_id;
  IF v_type IS DISTINCT FROM 'CHARGE' THEN RETURN 'NOT_A_CHARGE'; END IF;
  SELECT o.state INTO v_outbox FROM iam_v2.posting_outbox o WHERE o.posting_id = a.internal_posting_id;
  IF v_outbox IS DISTINCT FROM 'IN_FLIGHT' THEN RETURN 'POSTING_NOT_IN_FLIGHT'; END IF;
  SELECT * INTO rdy FROM iam_v2.pms_interface_financially_ready(a.tenant_id, a.site_id, p_iface);
  IF NOT COALESCE(rdy.ready, false) THEN RETURN 'INTERFACE_NOT_FINANCIALLY_READY'; END IF;
  RETURN 'AUTHORISED';
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.p4_posting_command_authorised(uuid,text,text) FROM PUBLIC;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_posting_command_authorised(uuid,text,text) TO svc_pmsd;
  END IF;
END
$grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.p4_posting_command_authorised(uuid,text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

COMMIT;
