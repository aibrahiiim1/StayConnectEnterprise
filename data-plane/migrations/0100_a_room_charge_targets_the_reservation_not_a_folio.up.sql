-- A ROOM CHARGE TARGETS THE RESERVATION (RN + G#), NOT A FOLIO. Phase-0 Amendment A1, Product-Owner decision D46.
--
-- docs/architecture/StayConnect-IAM-Phase0-Amendment-A1.md; the FINAL contract carries the normative wording.
--
--   1. POSTING TARGET. pms_interface_revisions.folio_identity_strategy becomes posting_target_model
--      ('UNSET' | 'RESERVATION'), still fail-closed on UNSET. Financial onboarding records RESERVATION.
--   2. NO FOLIO MODEL. pms_postings pins g_number (the stay's reservation number) instead of folio_id; the
--      folios and stay_folios tables are dropped. Nothing in the posting path reads or writes a folio.
--   3. POSTING PERMISSION. stays.posting_allowed = IN_HOUSE ∧ reservation present ∧ no active posting block,
--      recomputed by the database whenever the stay or its blocks change. Blocks live in
--      stay_posting_blocks; PMS_NO_POST and PMS_DATA_SUSPECT are cleared only by fresh PMS data, never by an
--      operator; POSTING_UNRESOLVED only when its charge is terminal; ADMIN_BLOCK administratively.
--   4. ONE UNRESOLVED ROOM CHARGE PER STAY. A CHARGE is refused while the same stay has a charge whose
--      settlement is REQUIRED, IN_PROGRESS or MANUAL_REVIEW. Terminal charges do not count.
--   5. THE PMS ANSWER DECIDES, CONSERVATIVELY. A non-OK PA is definite "not posted" only for a code the vendor
--      has confirmed for this interface (pms_answer_confirmations); otherwise it is UNKNOWN. Confirmed NP sets
--      PMS_NO_POST; NG/NR set PMS_DATA_SUSPECT and request a resync; NA/RY place no stay block.
--   6. ROOM MOVES. The attempt's RN is the reservation's current room; pmsd's authorisation now refuses a
--      command whose RN is no longer the stay's room (ROOM_CHANGED) or whose stay is no longer postable. An
--      attempt refused before its first byte is NOT_SENT (renamed from FAILED).
--
-- Every structure changed here is empty on every installation (guarded below). Real posting still requires
-- STAYCONNECT_PHASE4_PMS_TRANSMIT on scd and pmsd, which is not authorised.

BEGIN;

DO $guard$
BEGIN
  IF EXISTS (SELECT 1 FROM iam_v2.pms_interface_revisions WHERE folio_identity_strategy <> 'UNSET')
     OR EXISTS (SELECT 1 FROM iam_v2.pms_financial_onboardings)
     OR EXISTS (SELECT 1 FROM iam_v2.folios)
     OR EXISTS (SELECT 1 FROM iam_v2.stay_folios)
     OR EXISTS (SELECT 1 FROM iam_v2.pms_postings)
     OR EXISTS (SELECT 1 FROM iam_v2.posting_attempts) THEN
    RAISE EXCEPTION 'A1_MIGRATION: a folio strategy, onboarding, folio or posting already exists; this migration '
                    'only converts an empty posting ledger' USING ERRCODE = 'check_violation';
  END IF;
END $guard$;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The posting target.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.pms_interface_revisions DROP CONSTRAINT IF EXISTS pms_interface_revisions_folio_identity_strategy_check;
ALTER TABLE iam_v2.pms_interface_revisions RENAME COLUMN folio_identity_strategy TO posting_target_model;
ALTER TABLE iam_v2.pms_interface_revisions ADD CONSTRAINT pms_interface_revisions_posting_target_model_check
  CHECK (posting_target_model IN ('UNSET','RESERVATION'));
COMMENT ON COLUMN iam_v2.pms_interface_revisions.posting_target_model IS
  'Fail-closed posting target (Amendment A1, D46). UNSET blocks every financial CHARGE; RESERVATION = a PS targets '
  'RN + G#, G# being the vendor-confirmed non-reused reservation number. Recorded by financial onboarding only.';

ALTER TABLE iam_v2.pms_financial_onboardings DROP CONSTRAINT IF EXISTS pms_financial_onboardings_folio_identity_strategy_check;
ALTER TABLE iam_v2.pms_financial_onboardings RENAME COLUMN folio_identity_strategy TO posting_target_model;
ALTER TABLE iam_v2.pms_financial_onboardings ADD CONSTRAINT pms_financial_onboardings_posting_target_model_check
  CHECK (posting_target_model = 'RESERVATION');

-- ---------------------------------------------------------------------------------------------------------
-- 2. No folio model: postings pin the reservation.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.pms_postings DROP CONSTRAINT IF EXISTS pms_postings_tenant_id_site_id_pms_interface_id_folio_id_fkey;
ALTER TABLE iam_v2.pms_postings DROP COLUMN IF EXISTS folio_id;
ALTER TABLE iam_v2.pms_postings ADD COLUMN g_number text NOT NULL;
ALTER TABLE iam_v2.pms_postings ADD CONSTRAINT posting_gnumber_wire_safe CHECK (
  btrim(g_number) <> '' AND length(g_number) <= 32 AND g_number !~ '[\x00-\x1f\x7f|]');
COMMENT ON COLUMN iam_v2.pms_postings.g_number IS
  'The reservation number (G#) of the pinned stay, snapshotted at creation and immutable. Posting identity is '
  '(pms_interface_id, g_number) (Amendment A1).';
DROP TABLE iam_v2.stay_folios;
DROP TABLE iam_v2.folios;

-- ---------------------------------------------------------------------------------------------------------
-- 3. Attempt outcomes: NOT_SENT is "pmsd proved no byte was written".
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.posting_attempts DROP CONSTRAINT IF EXISTS posting_attempts_outcome_check;
ALTER TABLE iam_v2.posting_attempts ADD CONSTRAINT posting_attempts_outcome_check
  CHECK (outcome IN ('SENDING','ACKED','UNKNOWN','NOT_SENT'));

-- The attempt targets the posting's reservation, always.
CREATE OR REPLACE FUNCTION iam_v2.p4_attempt_targets_posting_reservation() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_g text;
BEGIN
  SELECT g_number INTO v_g FROM iam_v2.pms_postings WHERE id = NEW.internal_posting_id;
  IF v_g IS NULL OR NEW.g_number IS DISTINCT FROM v_g THEN
    RAISE EXCEPTION 'ATTEMPT_RESERVATION_MISMATCH: an attempt carries its posting''s reservation number and no other'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS p4_attempt_targets_posting_reservation ON iam_v2.posting_attempts;
CREATE TRIGGER p4_attempt_targets_posting_reservation BEFORE INSERT ON iam_v2.posting_attempts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_attempt_targets_posting_reservation();

CREATE OR REPLACE VIEW iam_v2.posting_execution_state AS
 SELECT p.id AS posting_id,
    p.tenant_id,
    p.site_id,
    p.pms_interface_id,
    p.posting_type,
    p.amount_minor,
    p.currency,
    p.currency_exponent,
    p.idempotency_key,
    p.created_at,
        CASE
            WHEN (la.attempt_no IS NULL) THEN 'NOT_ATTEMPTED'::text
            WHEN (la.outcome = 'SENDING'::text) THEN 'IN_FLIGHT'::text
            WHEN (la.outcome = 'UNKNOWN'::text) THEN 'UNKNOWN'::text
            WHEN (la.outcome = 'NOT_SENT'::text) THEN 'NOT_SENT'::text
            WHEN ((la.outcome = 'ACKED'::text) AND (la.pa_as_status = 'OK'::text)) THEN 'POSTED'::text
            WHEN (la.outcome = 'ACKED'::text) THEN 'REJECTED'::text
            ELSE NULL::text
        END AS execution_state,
    la.attempt_no AS latest_attempt_no,
    la.p_number AS latest_p_number,
    la.outcome AS latest_attempt_outcome,
    la.pa_as_status AS latest_pa_as_status,
    ac.attempt_count,
    ac.unknown_attempt_count,
    (ac.unknown_attempt_count > 0) AS has_unknown_history,
    ob.state AS outbox_state,
    rs.terminal_action AS terminal_review_action,
    rs.review_version,
    rs.escalation_count,
    rs.retry_authorized_attempt_no,
    (rs.retry_authorization_consumed_at IS NOT NULL) AS retry_authorization_consumed,
    ((la.outcome = 'UNKNOWN'::text) AND (rs.terminal_action IS NULL)) AS awaiting_manual_review,
    iam_v2.p4_interface_freshness_block(p.tenant_id, p.site_id, p.pms_interface_id, p.posting_interface_revision_id, now()) AS freshness_block
   FROM ((((iam_v2.pms_postings p
     LEFT JOIN LATERAL ( SELECT a.attempt_no,
            a.outcome,
            a.p_number,
            a.pa_as_status
           FROM iam_v2.posting_attempts a
          WHERE (a.internal_posting_id = p.id)
          ORDER BY a.attempt_no DESC
         LIMIT 1) la ON (true))
     LEFT JOIN LATERAL ( SELECT count(*) AS attempt_count,
            count(*) FILTER (WHERE (a.outcome = 'UNKNOWN'::text)) AS unknown_attempt_count
           FROM iam_v2.posting_attempts a
          WHERE (a.internal_posting_id = p.id)) ac ON (true))
     LEFT JOIN LATERAL ( SELECT o.state
           FROM iam_v2.posting_outbox o
          WHERE ((o.posting_id = p.id) AND (o.state = ANY (ARRAY['QUEUED'::text, 'IN_FLIGHT'::text, 'HELD_RECOVERY'::text])))
         LIMIT 1) ob ON (true))
     LEFT JOIN iam_v2.posting_review_state rs ON ((rs.posting_id = p.id)));

-- ---------------------------------------------------------------------------------------------------------
-- 4. Posting blocks and posting permission.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.stay_posting_blocks (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  pms_interface_id   uuid NOT NULL,
  stay_id            uuid NOT NULL,
  reason             text NOT NULL CHECK (reason IN ('PMS_NO_POST','PMS_DATA_SUSPECT','POSTING_UNRESOLVED','ADMIN_BLOCK')),
  source             text NOT NULL CHECK (source IN ('PMS_ANSWER','POSTING_LEDGER','OPERATOR')),
  posting_id         uuid,
  pa_as_status       text CHECK (pa_as_status IS NULL OR pa_as_status IN ('NG','NA','NP','NR','RY','UR')),
  note               text CHECK (note IS NULL OR length(note) BETWEEN 4 AND 500),
  created_by         uuid,
  created_at         timestamptz NOT NULL DEFAULT now(),
  cleared_at         timestamptz,
  cleared_by_source  text CHECK (cleared_by_source IS NULL OR cleared_by_source IN ('PMS_FEED','POSTING_LEDGER','OPERATOR')),
  cleared_by         uuid,
  cleared_reason     text CHECK (cleared_reason IS NULL OR length(cleared_reason) BETWEEN 4 AND 500),
  CONSTRAINT stay_posting_block_source_fits CHECK (
       (reason IN ('PMS_NO_POST','PMS_DATA_SUSPECT') AND source = 'PMS_ANSWER')
    OR (reason = 'POSTING_UNRESOLVED' AND source = 'POSTING_LEDGER' AND posting_id IS NOT NULL)
    OR (reason = 'ADMIN_BLOCK' AND source = 'OPERATOR' AND created_by IS NOT NULL AND note IS NOT NULL)),
  CONSTRAINT stay_posting_block_clearance_fits CHECK (
       cleared_at IS NULL AND cleared_by_source IS NULL
    OR (cleared_at IS NOT NULL AND (
          (reason IN ('PMS_NO_POST','PMS_DATA_SUSPECT') AND cleared_by_source = 'PMS_FEED')
       OR (reason = 'POSTING_UNRESOLVED' AND cleared_by_source = 'POSTING_LEDGER')
       OR (reason = 'ADMIN_BLOCK' AND cleared_by_source = 'OPERATOR' AND cleared_by IS NOT NULL AND cleared_reason IS NOT NULL)))),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id)
    REFERENCES iam_v2.stays (tenant_id, site_id, pms_interface_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS stay_posting_block_one_active
  ON iam_v2.stay_posting_blocks (stay_id, reason, COALESCE(posting_id, '00000000-0000-0000-0000-000000000000'::uuid))
  WHERE cleared_at IS NULL;
CREATE INDEX IF NOT EXISTS stay_posting_blocks_active ON iam_v2.stay_posting_blocks (stay_id) WHERE cleared_at IS NULL;
COMMENT ON TABLE iam_v2.stay_posting_blocks IS
  'Reasoned stops on room charging for one stay (Amendment A1, contract section 9a rule 9). Written only by the '
  'posting functions; a block is never deleted and is cleared exactly once, by the source its reason allows.';

-- A block is never deleted, never re-opened and its identity never changes; only the clearance is written, once.
CREATE OR REPLACE FUNCTION iam_v2.stay_posting_blocks_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'stay_posting_blocks is not deletable' USING ERRCODE = 'restrict_violation';
  END IF;
  IF ROW(NEW.id, NEW.tenant_id, NEW.site_id, NEW.pms_interface_id, NEW.stay_id, NEW.reason, NEW.source, NEW.posting_id,
         NEW.pa_as_status, NEW.note, NEW.created_by, NEW.created_at)
     IS DISTINCT FROM ROW(OLD.id, OLD.tenant_id, OLD.site_id, OLD.pms_interface_id, OLD.stay_id, OLD.reason, OLD.source,
         OLD.posting_id, OLD.pa_as_status, OLD.note, OLD.created_by, OLD.created_at) THEN
    RAISE EXCEPTION 'stay_posting_blocks identity is immutable' USING ERRCODE = 'restrict_violation';
  END IF;
  IF OLD.cleared_at IS NOT NULL THEN
    RAISE EXCEPTION 'a cleared posting block stays cleared' USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END $fn$;
DROP TRIGGER IF EXISTS stay_posting_blocks_guard ON iam_v2.stay_posting_blocks;
CREATE TRIGGER stay_posting_blocks_guard BEFORE UPDATE OR DELETE ON iam_v2.stay_posting_blocks
  FOR EACH ROW EXECUTE FUNCTION iam_v2.stay_posting_blocks_guard();

-- The ONE evaluation of posting permission (contract section 2, invariant 9).
CREATE OR REPLACE FUNCTION iam_v2.p4_refresh_stay_posting_permission(p_stay uuid)
  RETURNS boolean
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE st record; b record; v_allowed boolean; v_reason text; v_source text;
BEGIN
  SELECT id, status, external_reservation_id, posting_allowed, posting_block_reason, posting_permission_source
    INTO st FROM iam_v2.stays WHERE id = p_stay;
  IF st.id IS NULL THEN RETURN false; END IF;
  SELECT reason, source INTO b FROM iam_v2.stay_posting_blocks
   WHERE stay_id = p_stay AND cleared_at IS NULL
   ORDER BY CASE reason WHEN 'POSTING_UNRESOLVED' THEN 1 WHEN 'PMS_NO_POST' THEN 2
                        WHEN 'PMS_DATA_SUSPECT' THEN 3 ELSE 4 END, created_at
   LIMIT 1;
  IF st.status <> 'IN_HOUSE' THEN
    v_allowed := false; v_reason := 'NOT_IN_HOUSE'; v_source := 'PMS_FEED';
  ELSIF st.external_reservation_id IS NULL OR btrim(st.external_reservation_id) = '' THEN
    v_allowed := false; v_reason := 'NO_RESERVATION'; v_source := 'PMS_FEED';
  ELSIF b.reason IS NOT NULL THEN
    v_allowed := false; v_reason := b.reason; v_source := b.source;
  ELSE
    v_allowed := true; v_reason := NULL; v_source := 'PMS_FEED';
  END IF;
  IF st.posting_allowed IS DISTINCT FROM v_allowed OR st.posting_block_reason IS DISTINCT FROM v_reason
     OR st.posting_permission_source IS DISTINCT FROM v_source THEN
    UPDATE iam_v2.stays
       SET posting_allowed = v_allowed, posting_block_reason = v_reason,
           posting_permission_source = v_source, posting_checked_at = now()
     WHERE id = p_stay;
  END IF;
  RETURN v_allowed;
END $fn$;

-- Posting permission FOR ONE CHARGE: the stay's permission, except that the charge's OWN unresolved block
-- does not stop it. Without this, a reviewed CONFIRM_NOT_POSTED_RETRY would be refused by the very
-- POSTING_UNRESOLVED block its UNKNOWN placed; every other block (including another charge's) still applies.
CREATE OR REPLACE FUNCTION iam_v2.p4_stay_postable_for(p_stay uuid, p_posting uuid)
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT s.status = 'IN_HOUSE' AND s.external_reservation_id IS NOT NULL AND btrim(s.external_reservation_id) <> ''
     AND NOT EXISTS (SELECT 1 FROM iam_v2.stay_posting_blocks b
                      WHERE b.stay_id = s.id AND b.cleared_at IS NULL
                        AND NOT (b.reason = 'POSTING_UNRESOLVED' AND p_posting IS NOT NULL AND b.posting_id = p_posting))
    FROM iam_v2.stays s WHERE s.id = p_stay;
$fn$;

-- Fresh, valid PMS data clears a data-suspect block: the same reservation, IN_HOUSE, a resolvable room, and
-- occupancy evidence (an applied guest record or resync record) that arrived AFTER the block was placed and
-- whose clock is not suspect. PMS_NO_POST is not cleared here: the guest feed carries no explicit "posting
-- allowed" indicator, and only such authoritative data may lift it.
CREATE OR REPLACE FUNCTION iam_v2.p4_stay_feed_confirms(p_stay uuid)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE st record; n integer := 0;
BEGIN
  SELECT id, status, normalized_room_number, occupancy_evidence_at, occupancy_clock_suspect
    INTO st FROM iam_v2.stays WHERE id = p_stay;
  IF st.id IS NULL THEN RETURN 0; END IF;
  IF st.status = 'IN_HOUSE' AND st.normalized_room_number IS NOT NULL AND btrim(st.normalized_room_number) <> ''
     AND st.occupancy_evidence_at IS NOT NULL AND st.occupancy_clock_suspect IS NOT TRUE THEN
    UPDATE iam_v2.stay_posting_blocks
       SET cleared_at = now(), cleared_by_source = 'PMS_FEED'
     WHERE stay_id = p_stay AND reason = 'PMS_DATA_SUSPECT' AND cleared_at IS NULL
       AND created_at < st.occupancy_evidence_at;
    GET DIAGNOSTICS n = ROW_COUNT;
  END IF;
  RETURN n;
END $fn$;

-- Every change to a stay that bears on posting re-evaluates it in the same transaction.
CREATE OR REPLACE FUNCTION iam_v2.trg_stay_posting_permission() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  PERFORM iam_v2.p4_stay_feed_confirms(NEW.id);
  PERFORM iam_v2.p4_refresh_stay_posting_permission(NEW.id);
  RETURN NULL;
END $fn$;
DROP TRIGGER IF EXISTS stay_posting_permission ON iam_v2.stays;
CREATE TRIGGER stay_posting_permission
  AFTER INSERT OR UPDATE OF status, external_reservation_id, normalized_room_number, occupancy_evidence_at,
                            occupancy_clock_suspect ON iam_v2.stays
  FOR EACH ROW EXECUTE FUNCTION iam_v2.trg_stay_posting_permission();

-- Placing a block is idempotent (one active block per stay, reason and posting).
CREATE OR REPLACE FUNCTION iam_v2.p4_place_stay_posting_block(p_stay uuid, p_reason text, p_source text,
    p_posting uuid, p_as text, p_actor uuid, p_note text)
  RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE st record;
BEGIN
  SELECT tenant_id, site_id, pms_interface_id INTO st FROM iam_v2.stays WHERE id = p_stay;
  IF st.tenant_id IS NULL THEN RETURN; END IF;
  INSERT INTO iam_v2.stay_posting_blocks
    (tenant_id, site_id, pms_interface_id, stay_id, reason, source, posting_id, pa_as_status, note, created_by)
  VALUES (st.tenant_id, st.site_id, st.pms_interface_id, p_stay, p_reason, p_source, p_posting, p_as, p_note, p_actor)
  ON CONFLICT DO NOTHING;
  PERFORM iam_v2.p4_refresh_stay_posting_permission(p_stay);
END $fn$;

CREATE OR REPLACE FUNCTION iam_v2.p4_clear_posting_unresolved(p_posting uuid)
  RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_stay uuid;
BEGIN
  SELECT stay_id INTO v_stay FROM iam_v2.pms_postings WHERE id = p_posting;
  UPDATE iam_v2.stay_posting_blocks SET cleared_at = now(), cleared_by_source = 'POSTING_LEDGER'
   WHERE posting_id = p_posting AND reason = 'POSTING_UNRESOLVED' AND cleared_at IS NULL;
  IF v_stay IS NOT NULL THEN PERFORM iam_v2.p4_refresh_stay_posting_permission(v_stay); END IF;
END $fn$;

-- ADMIN_BLOCK is the only block an operator sets or clears.
CREATE OR REPLACE FUNCTION iam_v2.p4_admin_posting_block(p_tenant uuid, p_site uuid, p_stay uuid, p_action text,
    p_reason text, p_operator uuid)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $fn$
DECLARE st record; v_status text; n integer;
BEGIN
  IF p_action NOT IN ('SET','CLEAR') THEN
    RAISE EXCEPTION 'ADMIN_BLOCK_ACTION: SET or CLEAR' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason,''))) < 4 THEN
    RAISE EXCEPTION 'ADMIN_BLOCK_REASON: a reason is required' USING ERRCODE = 'check_violation';
  END IF;
  SELECT status INTO v_status FROM public.operators WHERE id = p_operator AND tenant_id = p_tenant;
  IF v_status IS DISTINCT FROM 'active' THEN
    RAISE EXCEPTION 'ADMIN_BLOCK_ACTOR: an active operator of this tenant is required' USING ERRCODE = 'check_violation';
  END IF;
  SELECT id INTO st FROM iam_v2.stays WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_stay FOR UPDATE;
  IF st.id IS NULL THEN RAISE EXCEPTION 'ADMIN_BLOCK_STAY: no such stay' USING ERRCODE = 'no_data_found'; END IF;
  IF p_action = 'SET' THEN
    PERFORM iam_v2.p4_place_stay_posting_block(p_stay, 'ADMIN_BLOCK', 'OPERATOR', NULL, NULL, p_operator, btrim(p_reason));
    RETURN 'SET';
  END IF;
  UPDATE iam_v2.stay_posting_blocks
     SET cleared_at = now(), cleared_by_source = 'OPERATOR', cleared_by = p_operator, cleared_reason = btrim(p_reason)
   WHERE stay_id = p_stay AND reason = 'ADMIN_BLOCK' AND cleared_at IS NULL;
  GET DIAGNOSTICS n = ROW_COUNT;
  PERFORM iam_v2.p4_refresh_stay_posting_permission(p_stay);
  RETURN CASE WHEN n > 0 THEN 'CLEARED' ELSE 'NOT_BLOCKED' END;
END $fn$;

-- Whether a stay has an unresolved / in-flight / UNKNOWN room charge (for offering; scd holds no ledger read).
CREATE OR REPLACE FUNCTION iam_v2.p4_stay_room_charge_open(p_stay uuid)
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT EXISTS (SELECT 1 FROM iam_v2.pms_postings p JOIN iam_v2.settlements se ON se.id = p.settlement_id
                  WHERE p.stay_id = p_stay AND p.posting_type = 'CHARGE'
                    AND se.status IN ('REQUIRED','IN_PROGRESS','MANUAL_REVIEW'));
$fn$;

-- Why an attempt was NOT_SENT (pmsd's bounded reason), for the review screen, without a read of the event ledger.
CREATE OR REPLACE FUNCTION iam_v2.p4_attempt_not_sent_reason(p_attempt uuid)
  RETURNS text
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT e.detail->>'reason' FROM iam_v2.posting_attempt_events e
   WHERE e.posting_attempt_id = p_attempt AND e.event_type = 'NOT_SENT' ORDER BY e.created_at LIMIT 1;
$fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 5. Vendor-confirmed answer meanings, per interface. Until a code is confirmed it is UNKNOWN.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.pms_answer_confirmations (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL,
  site_id           uuid NOT NULL,
  pms_interface_id  uuid NOT NULL,
  as_status         text NOT NULL CHECK (as_status IN ('NP','NG','NR','NA','RY')),
  action            text NOT NULL CHECK (action IN ('CONFIRM','WITHDRAW')),
  evidence          text NOT NULL CHECK (length(btrim(evidence)) BETWEEN 20 AND 2000),
  reason            text NOT NULL CHECK (length(btrim(reason)) BETWEEN 4 AND 500),
  recorded_by       uuid NOT NULL,
  recorded_at       timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id)
);
CREATE INDEX IF NOT EXISTS pms_answer_confirmations_iface
  ON iam_v2.pms_answer_confirmations (pms_interface_id, as_status, recorded_at DESC);
COMMENT ON TABLE iam_v2.pms_answer_confirmations IS
  'Append-only record that the PMS vendor confirmed a PA status means "definitely not posted" on this interface '
  '(Amendment A1, contract section 9a rule 8). The latest row per code decides. UR can never be confirmed here.';
DROP TRIGGER IF EXISTS pms_answer_confirmations_append_only ON iam_v2.pms_answer_confirmations;
CREATE TRIGGER pms_answer_confirmations_append_only BEFORE UPDATE OR DELETE ON iam_v2.pms_answer_confirmations
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_append_only_refuse();

CREATE OR REPLACE FUNCTION iam_v2.pms_answer_confirmation_record(p_tenant uuid, p_site uuid, p_iface uuid,
    p_as text, p_action text, p_evidence text, p_reason text, p_operator uuid)
  RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $fn$
DECLARE v_status text; v_id uuid;
BEGIN
  IF p_as NOT IN ('NP','NG','NR','NA','RY') THEN
    RAISE EXCEPTION 'ANSWER_CODE: only NP, NG, NR, NA and RY can be confirmed as definitely not posted' USING ERRCODE = 'check_violation';
  END IF;
  IF p_action NOT IN ('CONFIRM','WITHDRAW') THEN
    RAISE EXCEPTION 'ANSWER_ACTION: CONFIRM or WITHDRAW' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_evidence,''))) < 20 THEN
    RAISE EXCEPTION 'ANSWER_EVIDENCE: state the vendor confirmation (who, when, document reference)' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason,''))) < 4 THEN
    RAISE EXCEPTION 'ANSWER_REASON: a reason is required' USING ERRCODE = 'check_violation';
  END IF;
  SELECT status INTO v_status FROM public.operators WHERE id = p_operator AND tenant_id = p_tenant;
  IF v_status IS DISTINCT FROM 'active' THEN
    RAISE EXCEPTION 'ANSWER_ACTOR: an active operator of this tenant is required' USING ERRCODE = 'check_violation';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM iam_v2.pms_interfaces WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface
                  AND connector_kind = 'protel-fias') THEN
    RAISE EXCEPTION 'ANSWER_INTERFACE: a FIAS interface of this site is required' USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.pms_answer_confirmations
    (tenant_id, site_id, pms_interface_id, as_status, action, evidence, reason, recorded_by)
  VALUES (p_tenant, p_site, p_iface, p_as, p_action, btrim(p_evidence), btrim(p_reason), p_operator)
  RETURNING id INTO v_id;
  RETURN v_id;
END $fn$;

-- The effect of one PA status on this interface: POSTED, a NOT_POSTED_* effect, or UNPROVEN (= UNKNOWN).
CREATE OR REPLACE FUNCTION iam_v2.p4_answer_effect(p_iface uuid, p_as text)
  RETURNS text
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_action text;
BEGIN
  IF p_as = 'OK' THEN RETURN 'POSTED'; END IF;
  IF p_as IS NULL OR p_as NOT IN ('NP','NG','NR','NA','RY') THEN RETURN 'UNPROVEN'; END IF;
  SELECT action INTO v_action FROM iam_v2.pms_answer_confirmations
   WHERE pms_interface_id = p_iface AND as_status = p_as ORDER BY recorded_at DESC, id DESC LIMIT 1;
  IF v_action IS DISTINCT FROM 'CONFIRM' THEN RETURN 'UNPROVEN'; END IF;
  RETURN CASE p_as WHEN 'NP' THEN 'NOT_POSTED_NO_POST'
                   WHEN 'NG' THEN 'NOT_POSTED_TARGET_INVALID'
                   WHEN 'NR' THEN 'NOT_POSTED_TARGET_INVALID'
                   ELSE 'NOT_POSTED_NO_STAY_EFFECT' END;
END $fn$;

-- The data-suspect resync has its own bounded reason.
CREATE OR REPLACE FUNCTION iam_v2.request_full_resync(p_tenant uuid, p_site uuid, p_interface uuid, p_reason text) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_id uuid;
BEGIN
  -- The reason vocabulary is closed HERE as well as in edged. A bounded set enforced only in the process that
  -- happens to call today is not a bound; it is a convention that survives until the next caller.
  IF p_reason IS NULL OR p_reason NOT IN
     ('SUSPECTED_STALE_GUEST_LIST','AFTER_PMS_MAINTENANCE','OPERATOR_VERIFICATION','SUPPORT_REQUEST',
      'PMS_ANSWER_TARGET_INVALID') THEN
    RAISE EXCEPTION 'unbounded resync reason' USING ERRCODE = 'check_violation';
  END IF;

  UPDATE iam_v2.pms_interface_runtime rt
     SET resync_command_id            = gen_random_uuid(),
         resync_command_requested_at  = now(),
         resync_command_reason        = p_reason,
         resync_command_generation    = rt.runtime_generation,
         resync_command_claimed_at    = NULL,
         sync_stage                   = 'REQUESTING_FULL_SYNC',
         sync_stage_at                = now(),
         sync_failure_code            = NULL,
         updated_at                   = now()
    FROM iam_v2.pms_interfaces pi
   WHERE pi.tenant_id = rt.tenant_id AND pi.site_id = rt.site_id AND pi.id = rt.pms_interface_id
     AND rt.tenant_id = p_tenant AND rt.site_id = p_site AND rt.pms_interface_id = p_interface
     -- there is a worker with a live socket to receive this
     AND pi.lifecycle_state = 'ACTIVE'
     AND rt.transport_status = 'CONNECTED'
     -- a resync is not already running, and no earlier request is still waiting to be claimed
     AND rt.sync_status <> 'RESYNC_IN_PROGRESS'
     AND rt.resync_command_id IS NULL
  RETURNING rt.resync_command_id INTO v_id;

  RETURN v_id; -- NULL when no row qualified; the caller explains which precondition failed
END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- 6. Onboarding records the reservation target.
-- ---------------------------------------------------------------------------------------------------------
DROP FUNCTION IF EXISTS iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid);
CREATE FUNCTION iam_v2.pms_interface_financial_onboard(
    p_tenant uuid, p_site uuid, p_iface uuid, p_expected_revision uuid, p_target_model text, p_currency text,
    p_exponent smallint, p_attestation text, p_reason text, p_operator uuid)
  RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $fn$
DECLARE v_current uuid; v_kind text; v_new uuid; v_status text;
BEGIN
  IF p_target_model IS DISTINCT FROM 'RESERVATION' THEN
    RAISE EXCEPTION 'ONBOARDING_TARGET: the posting target must be RESERVATION (RN + G#)' USING ERRCODE = 'check_violation';
  END IF;
  IF p_currency IS NULL OR p_currency !~ '^[A-Z]{3}$' OR p_exponent IS NULL OR p_exponent NOT BETWEEN 0 AND 4 THEN
    RAISE EXCEPTION 'ONBOARDING_CURRENCY: a three-letter currency and an exponent 0..4 are required' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_attestation,''))) < 20 THEN
    RAISE EXCEPTION 'ONBOARDING_ATTESTATION: state the vendor confirmation that reservation numbers are unique and never reused' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason,''))) < 4 THEN
    RAISE EXCEPTION 'ONBOARDING_REASON: a reason is required' USING ERRCODE = 'check_violation';
  END IF;
  SELECT status INTO v_status FROM public.operators WHERE id = p_operator AND tenant_id = p_tenant;
  IF v_status IS DISTINCT FROM 'active' THEN
    RAISE EXCEPTION 'ONBOARDING_ACTOR: an active operator of this tenant must approve onboarding' USING ERRCODE = 'check_violation';
  END IF;
  SELECT current_revision_id, connector_kind INTO v_current, v_kind FROM iam_v2.pms_interfaces
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface FOR UPDATE;
  IF v_current IS NULL THEN
    RAISE EXCEPTION 'ONBOARDING_INTERFACE: no published revision for this interface' USING ERRCODE = 'no_data_found';
  END IF;
  IF v_current IS DISTINCT FROM p_expected_revision THEN
    RAISE EXCEPTION 'ONBOARDING_REVISION_CONFLICT: the interface changed while this form was open' USING ERRCODE = 'serialization_failure';
  END IF;
  IF v_kind <> 'protel-fias' THEN
    RAISE EXCEPTION 'ONBOARDING_CONNECTOR: room charge is supported for FIAS interfaces only' USING ERRCODE = 'check_violation';
  END IF;
  IF p_exponent <> 2 THEN
    RAISE EXCEPTION 'ONBOARDING_EXPONENT: FIAS transmits amounts with exponent 2' USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.pms_interface_revisions
    (tenant_id, site_id, pms_interface_id, revision_no, source_timezone, posting_target_model, config,
     normalization_version, source_fingerprint, financial_base_currency, financial_base_currency_exponent)
  SELECT r.tenant_id, r.site_id, r.pms_interface_id,
         (SELECT COALESCE(MAX(x.revision_no),0)+1 FROM iam_v2.pms_interface_revisions x
           WHERE x.tenant_id = r.tenant_id AND x.site_id = r.site_id AND x.pms_interface_id = r.pms_interface_id),
         r.source_timezone, 'RESERVATION', r.config, r.normalization_version, r.source_fingerprint, p_currency, p_exponent
    FROM iam_v2.pms_interface_revisions r WHERE r.id = v_current
  RETURNING id INTO v_new;
  UPDATE iam_v2.pms_interfaces SET current_revision_id = v_new
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface;
  INSERT INTO iam_v2.pms_financial_onboardings
    (tenant_id, site_id, pms_interface_id, revision_id, previous_revision_id, posting_target_model, currency,
     currency_exponent, attestation, reason, approved_by)
  VALUES (p_tenant, p_site, p_iface, v_new, v_current, 'RESERVATION', p_currency, p_exponent, btrim(p_attestation),
          btrim(p_reason), p_operator);
  RETURN v_new;
END $fn$;

CREATE OR REPLACE FUNCTION iam_v2.pms_interface_financially_ready(p_tenant uuid, p_site uuid, p_iface uuid)
  RETURNS TABLE (ready boolean, reason text, currency char(3), currency_exponent smallint)
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE i record; r record;
BEGIN
  SELECT * INTO i FROM iam_v2.pms_interfaces WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_iface;
  IF i.id IS NULL THEN ready := false; reason := 'INTERFACE_UNKNOWN'; RETURN NEXT; RETURN; END IF;
  IF i.connector_kind <> 'protel-fias' THEN ready := false; reason := 'CONNECTOR_NOT_FINANCIAL'; RETURN NEXT; RETURN; END IF;
  IF i.lifecycle_state NOT IN ('ACTIVE','AUTH_DISABLED') THEN ready := false; reason := 'INTERFACE_NOT_ACTIVE'; RETURN NEXT; RETURN; END IF;
  SELECT * INTO r FROM iam_v2.pms_interface_revisions WHERE id = i.current_revision_id;
  currency := r.financial_base_currency; currency_exponent := r.financial_base_currency_exponent;
  IF r.posting_target_model <> 'RESERVATION' OR r.financial_base_currency IS NULL THEN
    ready := false; reason := 'NOT_ONBOARDED'; RETURN NEXT; RETURN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM iam_v2.pms_financial_onboardings o WHERE o.revision_id = r.id) THEN
    ready := false; reason := 'ONBOARDING_NOT_APPROVED'; RETURN NEXT; RETURN;
  END IF;
  ready := true; reason := NULL; RETURN NEXT;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 7. The charge gate: posting target, postable stay, the reservation, one unresolved charge per stay.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.trg_posting_charge_gate() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE v_model text; st record;
BEGIN
  IF NEW.posting_type = 'CHARGE' THEN
    SELECT posting_target_model INTO v_model FROM iam_v2.pms_interface_revisions
      WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND pms_interface_id=NEW.pms_interface_id AND id=NEW.posting_interface_revision_id;
    IF v_model IS NULL OR v_model = 'UNSET' THEN
      RAISE EXCEPTION 'POSTING_TARGET_UNSET: financial CHARGE blocked fail-closed (interface %, revision %)', NEW.pms_interface_id, NEW.posting_interface_revision_id
        USING ERRCODE='check_violation';
    END IF;
    IF NEW.stay_id IS NULL THEN
      RAISE EXCEPTION 'POSTING_NO_STAY: a CHARGE targets a stay''s reservation' USING ERRCODE='check_violation';
    END IF;
    -- Locking the stay serialises concurrent charges for it, so the one-unresolved check below cannot race.
    SELECT status, posting_allowed, external_reservation_id INTO st FROM iam_v2.stays
      WHERE tenant_id=NEW.tenant_id AND site_id=NEW.site_id AND pms_interface_id=NEW.pms_interface_id AND id=NEW.stay_id
      FOR UPDATE;
    IF st.status IS DISTINCT FROM 'IN_HOUSE' OR st.posting_allowed IS NOT TRUE THEN
      RAISE EXCEPTION 'POSTING_NOT_ALLOWED: stay % not IN_HOUSE/posting_allowed', NEW.stay_id USING ERRCODE='check_violation';
    END IF;
    IF NEW.g_number IS DISTINCT FROM st.external_reservation_id THEN
      RAISE EXCEPTION 'POSTING_RESERVATION_MISMATCH: a CHARGE carries its stay''s reservation number' USING ERRCODE='check_violation';
    END IF;
    IF EXISTS (SELECT 1 FROM iam_v2.pms_postings p JOIN iam_v2.settlements se ON se.id = p.settlement_id
                WHERE p.stay_id = NEW.stay_id AND p.posting_type = 'CHARGE'
                  AND se.status IN ('REQUIRED','IN_PROGRESS','MANUAL_REVIEW')) THEN
      RAISE EXCEPTION 'ROOM_CHARGE_UNRESOLVED: stay % already has an unresolved room charge', NEW.stay_id
        USING ERRCODE='check_violation';
    END IF;
  END IF;
  RETURN NEW;
END; $$;

-- ---------------------------------------------------------------------------------------------------------
-- 8. Room-charge creation pins the reservation.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_create_room_charge_posting(p_tenant uuid, p_site uuid, p_settlement uuid)
  RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE se record; pu record; ac record; i record; rdy record; st record; v_posting uuid;
BEGIN
  SELECT * INTO se FROM iam_v2.settlements WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_settlement FOR UPDATE;
  IF se.id IS NULL OR se.method <> 'PMS_POSTING' OR se.status <> 'REQUIRED' THEN
    RAISE EXCEPTION 'ROOM_CHARGE_SETTLEMENT: not a REQUIRED room-charge settlement' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO pu FROM iam_v2.purchases WHERE id = se.purchase_id FOR UPDATE;
  IF pu.state <> 'AWAITING_SETTLEMENT' OR pu.stay_id IS NULL OR pu.pms_interface_id IS NULL OR pu.settlement_mapping_id IS NULL THEN
    RAISE EXCEPTION 'ROOM_CHARGE_PURCHASE: the purchase does not pin a stay, interface and mapping' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO ac FROM iam_v2.auth_contexts WHERE id = pu.auth_context_id;
  IF ac.method <> 'PMS' OR ac.stay_id IS DISTINCT FROM pu.stay_id THEN
    RAISE EXCEPTION 'ROOM_CHARGE_AUTHENTICATION: a room charge needs a verified Room sign-in for the same stay' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO rdy FROM iam_v2.pms_interface_financially_ready(p_tenant, p_site, pu.pms_interface_id);
  IF NOT rdy.ready THEN
    RAISE EXCEPTION 'ROOM_CHARGE_NOT_READY: %', rdy.reason USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO i FROM iam_v2.pms_interfaces WHERE id = pu.pms_interface_id;
  SELECT external_reservation_id INTO st FROM iam_v2.stays
   WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = pu.pms_interface_id AND id = pu.stay_id;
  IF st.external_reservation_id IS NULL OR btrim(st.external_reservation_id) = '' THEN
    RAISE EXCEPTION 'ROOM_CHARGE_NO_RESERVATION: the stay has no reservation number' USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.pms_postings
    (tenant_id, site_id, pms_interface_id, settlement_id, purchase_id, stay_id, g_number,
     posting_interface_revision_id, posting_type, amount_minor, currency, currency_exponent, idempotency_key)
  VALUES (p_tenant, p_site, pu.pms_interface_id, se.id, pu.id, pu.stay_id, st.external_reservation_id,
          i.current_revision_id, 'CHARGE', pu.amount_minor, pu.currency, pu.currency_exponent,
          'room-charge:' || se.id::text)
  RETURNING id INTO v_posting;
  INSERT INTO iam_v2.posting_outbox (tenant_id, site_id, pms_interface_id, posting_id, state)
  VALUES (p_tenant, p_site, pu.pms_interface_id, v_posting, 'QUEUED');
  UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = se.id;
  RETURN v_posting;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 9. pmsd's authorisation: the command must still target the reservation's current room (room moves).
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_posting_command_authorised(p_iface uuid, p_pnum text, p_sha256 text)
  RETURNS text
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE a record; n int; v_outbox text; p record; st record; rdy record; v_block text;
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
  SELECT * INTO p FROM iam_v2.pms_postings WHERE id = a.internal_posting_id;
  IF p.posting_type IS DISTINCT FROM 'CHARGE' THEN RETURN 'NOT_A_CHARGE'; END IF;
  SELECT o.state INTO v_outbox FROM iam_v2.posting_outbox o WHERE o.posting_id = a.internal_posting_id;
  IF v_outbox IS DISTINCT FROM 'IN_FLIGHT' THEN RETURN 'POSTING_NOT_IN_FLIGHT'; END IF;
  SELECT * INTO rdy FROM iam_v2.pms_interface_financially_ready(a.tenant_id, a.site_id, p_iface);
  IF NOT COALESCE(rdy.ready, false) THEN RETURN 'INTERFACE_NOT_FINANCIALLY_READY'; END IF;
  -- The reservation the charge targets, as the database knows it NOW (Amendment A1, rule 7).
  SELECT status, posting_allowed, external_reservation_id, normalized_room_number INTO st
    FROM iam_v2.stays WHERE id = p.stay_id;
  IF st.external_reservation_id IS NULL OR st.external_reservation_id IS DISTINCT FROM a.g_number
     OR p.g_number IS DISTINCT FROM a.g_number THEN
    RETURN 'RESERVATION_MISMATCH';
  END IF;
  IF NOT COALESCE(iam_v2.p4_stay_postable_for(p.stay_id, p.id), false) THEN RETURN 'STAY_NOT_POSTABLE'; END IF;
  IF st.normalized_room_number IS NULL OR st.normalized_room_number IS DISTINCT FROM a.rn THEN RETURN 'ROOM_CHANGED'; END IF;
  v_block := iam_v2.p4_interface_freshness_block(a.tenant_id, a.site_id, p_iface, p.posting_interface_revision_id, now());
  IF v_block IS NOT NULL THEN RETURN 'INTERFACE_NOT_FRESH'; END IF;
  RETURN 'AUTHORISED';
END $fn$;

-- The posting worker locks the pinned stay while it builds an attempt, so a room move cannot land between
-- reading the reservation's current room and committing the attempt that carries it (A1 rule 7). The worker
-- holds no UPDATE privilege on stays, so the lock is taken by this definer.
CREATE OR REPLACE FUNCTION iam_v2.p4_lock_posting_stay(p_posting uuid)
  RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  PERFORM 1 FROM iam_v2.stays s JOIN iam_v2.pms_postings p ON p.stay_id = s.id WHERE p.id = p_posting FOR SHARE OF s;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 10. The settlement follows the PMS answer, conservatively, and the stay effect follows the answer.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_posting_settlement_outcome(p_posting uuid)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE p record; se record; a record; v_target text; v_effect text; v_ent uuid;
BEGIN
  SELECT * INTO p FROM iam_v2.pms_postings WHERE id = p_posting AND posting_type = 'CHARGE';
  IF p.id IS NULL THEN RAISE EXCEPTION 'POSTING_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = p.settlement_id FOR UPDATE;
  IF se.method <> 'PMS_POSTING' THEN RETURN 'NOT_A_ROOM_CHARGE'; END IF;
  SELECT * INTO a FROM iam_v2.posting_attempts WHERE internal_posting_id = p.id ORDER BY attempt_no DESC LIMIT 1;
  IF a.id IS NULL THEN RETURN 'NO_ATTEMPT'; END IF;
  IF a.outcome = 'ACKED' THEN
    v_effect := iam_v2.p4_answer_effect(p.pms_interface_id, a.pa_as_status);
  END IF;
  IF a.outcome = 'ACKED' AND v_effect = 'POSTED' THEN
    v_target := 'SETTLED';
  ELSIF a.outcome = 'ACKED' AND v_effect LIKE 'NOT_POSTED_%' THEN
    v_target := 'FAILED';
  ELSIF a.outcome = 'UNKNOWN' OR (a.outcome = 'ACKED' AND v_effect = 'UNPROVEN') THEN
    v_target := 'MANUAL_REVIEW';
  ELSE
    RETURN 'NOT_CONCLUSIVE';
  END IF;
  IF se.status = v_target OR se.status IN ('FAILED','PARTIALLY_REVERSED','REVERSED') THEN
    NULL;
  ELSIF se.status IN ('IN_PROGRESS','MANUAL_REVIEW') THEN
    IF NOT (se.status = 'MANUAL_REVIEW' AND v_target = 'MANUAL_REVIEW') THEN
      UPDATE iam_v2.settlements SET status = v_target WHERE id = se.id;
    END IF;
  ELSE
    RAISE EXCEPTION 'POSTING_SETTLEMENT_STATE: settlement is %', se.status USING ERRCODE = 'check_violation';
  END IF;
  -- The stay effect of the answer (contract section 9a rule 9).
  IF v_target = 'MANUAL_REVIEW' THEN
    PERFORM iam_v2.p4_place_stay_posting_block(p.stay_id, 'POSTING_UNRESOLVED', 'POSTING_LEDGER', p.id, NULL, NULL, NULL);
    RETURN v_target;
  END IF;
  PERFORM iam_v2.p4_clear_posting_unresolved(p.id);
  IF v_effect = 'NOT_POSTED_NO_POST' THEN
    PERFORM iam_v2.p4_place_stay_posting_block(p.stay_id, 'PMS_NO_POST', 'PMS_ANSWER', NULL, a.pa_as_status, NULL, NULL);
  ELSIF v_effect = 'NOT_POSTED_TARGET_INVALID' THEN
    PERFORM iam_v2.p4_place_stay_posting_block(p.stay_id, 'PMS_DATA_SUSPECT', 'PMS_ANSWER', NULL, a.pa_as_status, NULL, NULL);
    PERFORM iam_v2.request_full_resync(p.tenant_id, p.site_id, p.pms_interface_id, 'PMS_ANSWER_TARGET_INVALID');
  END IF;
  IF v_target = 'SETTLED' THEN
    SELECT entitlement_id INTO v_ent FROM iam_v2.p4_grant_paid_entitlement(p.tenant_id, p.site_id, se.id);
    RETURN 'GRANTED';
  END IF;
  RETURN v_target;
END $fn$;

CREATE OR REPLACE FUNCTION iam_v2.p4_posting_review_apply(p_tenant uuid, p_site uuid, p_posting uuid)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE p record; se record; rs record; v_ent uuid;
BEGIN
  SELECT * INTO p FROM iam_v2.pms_postings WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_posting AND posting_type = 'CHARGE';
  IF p.id IS NULL THEN RAISE EXCEPTION 'POSTING_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  SELECT * INTO rs FROM iam_v2.posting_review_state WHERE posting_id = p.id;
  IF rs.terminal_action IS NULL THEN RETURN 'NO_DECISION'; END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = p.settlement_id FOR UPDATE;
  IF rs.terminal_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
    -- Still unresolved: the POSTING_UNRESOLVED block stays until the authorised attempt concludes.
    UPDATE iam_v2.posting_outbox SET state = 'QUEUED'
     WHERE posting_id = p.id AND state = 'HELD_RECOVERY' AND rs.retry_authorized_attempt_no IS NOT NULL
       AND rs.retry_authorization_consumed_at IS NULL;
    RETURN 'REQUEUED';
  END IF;
  IF se.method <> 'PMS_POSTING' THEN RETURN 'NOT_A_ROOM_CHARGE'; END IF;
  IF rs.terminal_action = 'CONFIRM_POSTED' THEN
    IF se.status IN ('IN_PROGRESS','MANUAL_REVIEW') THEN
      UPDATE iam_v2.settlements SET status = 'SETTLED' WHERE id = se.id;
    END IF;
    IF (SELECT status FROM iam_v2.settlements WHERE id = se.id) = 'SETTLED' THEN
      PERFORM iam_v2.p4_clear_posting_unresolved(p.id);
      SELECT entitlement_id INTO v_ent FROM iam_v2.p4_grant_paid_entitlement(p.tenant_id, p.site_id, se.id);
      RETURN 'GRANTED';
    END IF;
    RETURN 'NOT_SETTLED';
  END IF;
  IF rs.terminal_action = 'CONFIRM_NOT_POSTED_ABANDON' THEN
    IF se.status IN ('IN_PROGRESS','MANUAL_REVIEW') THEN
      UPDATE iam_v2.settlements SET status = 'FAILED' WHERE id = se.id;
    END IF;
    PERFORM iam_v2.p4_clear_posting_unresolved(p.id);
    RETURN 'FAILED';
  END IF;
  RETURN 'NO_EFFECT';
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 11. Abort before sending: the reservation is no longer postable. Definitely not posted; the purchase fails.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.posting_presend_aborts (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL,
  site_id      uuid NOT NULL,
  posting_id   uuid NOT NULL UNIQUE,
  reason       text NOT NULL CHECK (reason IN ('STAY_NOT_IN_HOUSE','RESERVATION_MISMATCH','STAY_BLOCKED',
                                               'ROOM_UNRESOLVED','DATA_STALE')),
  created_at   timestamptz NOT NULL DEFAULT now()
);
DROP TRIGGER IF EXISTS posting_presend_aborts_append_only ON iam_v2.posting_presend_aborts;
CREATE TRIGGER posting_presend_aborts_append_only BEFORE UPDATE OR DELETE ON iam_v2.posting_presend_aborts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_append_only_refuse();

CREATE OR REPLACE FUNCTION iam_v2.p4_posting_abort_before_send(p_posting uuid, p_reason text)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE p record; se record;
BEGIN
  SELECT * INTO p FROM iam_v2.pms_postings WHERE id = p_posting AND posting_type = 'CHARGE';
  IF p.id IS NULL THEN RAISE EXCEPTION 'POSTING_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  -- Only a charge that has provably never been transmitted: no attempt, or every attempt NOT_SENT.
  IF EXISTS (SELECT 1 FROM iam_v2.posting_attempts WHERE internal_posting_id = p.id AND outcome <> 'NOT_SENT') THEN
    RAISE EXCEPTION 'ABORT_AFTER_TRANSMISSION: a charge that may have reached the PMS is never aborted'
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = p.settlement_id FOR UPDATE;
  INSERT INTO iam_v2.posting_presend_aborts (tenant_id, site_id, posting_id, reason)
  VALUES (p.tenant_id, p.site_id, p.id, p_reason) ON CONFLICT (posting_id) DO NOTHING;
  UPDATE iam_v2.posting_outbox SET state = 'DONE' WHERE posting_id = p.id AND state IN ('QUEUED','IN_FLIGHT');
  IF se.status = 'IN_PROGRESS' THEN
    UPDATE iam_v2.settlements SET status = 'FAILED' WHERE id = se.id;
  END IF;
  RETURN 'ABORTED';
END $fn$;


-- ---------------------------------------------------------------------------------------------------------
-- 12. A review reversal pins the same reservation as the charge it corrects (no folio).
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.record_posting_review_action(p_posting uuid, p_action text, p_actor uuid, p_reason text, p_evidence jsonb DEFAULT '{}'::jsonb, p_expected_version integer DEFAULT NULL::integer, p_reversal_amount bigint DEFAULT NULL::bigint) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE
  v_t uuid; v_s uuid; st record; v_action_id uuid; v_next_attempt int; v_attempts int; la record;
  o record; v_amount bigint; v_rev uuid;
BEGIN
  IF p_action NOT IN ('CONFIRM_POSTED','CONFIRM_NOT_POSTED_RETRY','CONFIRM_NOT_POSTED_ABANDON',
                      'CREATE_REVERSAL','ESCALATE') THEN
    RAISE EXCEPTION 'REVIEW_ACTION_UNKNOWN: % is not in the approved review catalog', p_action
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_actor IS NULL OR p_reason IS NULL OR btrim(p_reason) = '' THEN
    RAISE EXCEPTION 'REVIEW_ACTOR_REASON_REQUIRED: every financial review decision is attributable'
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_action <> 'ESCALATE'
     AND (p_evidence IS NULL OR jsonb_typeof(p_evidence) <> 'object' OR p_evidence = '{}'::jsonb) THEN
    RAISE EXCEPTION 'REVIEW_EVIDENCE_REQUIRED: a terminal financial decision must record its evidence'
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO o FROM iam_v2.pms_postings WHERE id = p_posting;
  IF o.id IS NULL THEN
    RAISE EXCEPTION 'REVIEW_POSTING_UNKNOWN: posting % does not exist', p_posting
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  v_t := o.tenant_id; v_s := o.site_id;

  PERFORM pg_advisory_xact_lock(iam_v2.ns_financial_review(p_posting::text));
  INSERT INTO iam_v2.posting_review_state (posting_id, tenant_id, site_id)
  VALUES (p_posting, v_t, v_s) ON CONFLICT (posting_id) DO NOTHING;
  SELECT * INTO st FROM iam_v2.posting_review_state WHERE posting_id = p_posting FOR UPDATE;

  IF p_expected_version IS NOT NULL AND p_expected_version <> st.review_version THEN
    RAISE EXCEPTION 'REVIEW_VERSION_STALE: expected version %, current is %',
      p_expected_version, st.review_version USING ERRCODE = 'serialization_failure';
  END IF;

  SELECT count(*) INTO v_attempts FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting;
  IF v_attempts = 0 AND p_action <> 'ESCALATE' THEN
    RAISE EXCEPTION 'REVIEW_NOT_APPLICABLE: posting % has no transmission attempt to decide about', p_posting
      USING ERRCODE = 'check_violation';
  END IF;

  IF p_action <> 'ESCALATE' AND st.terminal_action IS NOT NULL THEN
    IF st.terminal_action = p_action THEN
      RAISE EXCEPTION 'REVIEW_ALREADY_DECIDED: posting % is already decided as %', p_posting, st.terminal_action
        USING ERRCODE = 'unique_violation';
    END IF;
    RAISE EXCEPTION 'REVIEW_CONFLICT: posting % is already decided as %; % is incompatible',
      p_posting, st.terminal_action, p_action USING ERRCODE = 'unique_violation';
  END IF;

  SELECT attempt_no, outcome, pa_as_status INTO la
    FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting
    ORDER BY attempt_no DESC LIMIT 1;

  -- THE ACTION/STATE MATRIX.
  IF p_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
    IF la.outcome = 'ACKED' AND la.pa_as_status = 'OK' THEN
      RAISE EXCEPTION 'REVIEW_RETRY_REFUSED: attempt % was ACKed OK by the PMS; retrying it would post the '
                      'charge twice. Use CREATE_REVERSAL or CONFIRM_POSTED.', la.attempt_no
        USING ERRCODE = 'check_violation';
    END IF;
    IF la.outcome = 'SENDING' THEN
      RAISE EXCEPTION 'REVIEW_RETRY_REFUSED: attempt % is still SENDING; its outcome is not yet known',
        la.attempt_no USING ERRCODE = 'check_violation';
    END IF;
  END IF;

  -- CREATE_REVERSAL corrects money the PMS is believed to hold. Reversing a charge nobody thinks was
  -- posted would put a correction in the ledger for a debit that never happened.
  IF p_action = 'CREATE_REVERSAL' THEN
    IF o.posting_type <> 'CHARGE' THEN
      RAISE EXCEPTION 'REVIEW_REVERSAL_REFUSED: only a CHARGE can be reversed' USING ERRCODE = 'check_violation';
    END IF;
    IF la.outcome = 'SENDING' THEN
      RAISE EXCEPTION 'REVIEW_REVERSAL_REFUSED: attempt % is still SENDING; its outcome is not yet known',
        la.attempt_no USING ERRCODE = 'check_violation';
    END IF;
    IF NOT (la.outcome = 'UNKNOWN' OR (la.outcome = 'ACKED' AND la.pa_as_status = 'OK')) THEN
      RAISE EXCEPTION 'REVIEW_REVERSAL_REFUSED: the latest attempt is %/%; nothing is believed posted, so '
                      'there is nothing to reverse. Use CONFIRM_NOT_POSTED_ABANDON.',
        la.outcome, coalesce(la.pa_as_status,'-') USING ERRCODE = 'check_violation';
    END IF;
    v_amount := coalesce(p_reversal_amount, o.amount_minor);
  ELSIF p_reversal_amount IS NOT NULL THEN
    RAISE EXCEPTION 'REVIEW_AMOUNT_NOT_APPLICABLE: only CREATE_REVERSAL carries an amount'
      USING ERRCODE = 'check_violation';
  END IF;

  PERFORM set_config('iam_v2.p4_review_writer', txid_current()::text, true);

  INSERT INTO iam_v2.posting_review_actions (tenant_id, site_id, posting_id, action, actor, reason, evidence)
  VALUES (v_t, v_s, p_posting, p_action, p_actor, p_reason, coalesce(p_evidence, '{}'::jsonb))
  RETURNING id INTO v_action_id;

  IF p_action = 'CREATE_REVERSAL' THEN
    -- §15: "a new ledger row referencing the original". It pins the SAME evidence the original pinned, so
    -- the correction is attached to the same authorization rather than to a fresh resolution.
    INSERT INTO iam_v2.pms_postings
      (tenant_id, site_id, pms_interface_id, settlement_id, purchase_id, stay_id, g_number,
       posting_interface_revision_id, secret_generation_id, posting_type, reverses_posting_id,
       amount_minor, currency, currency_exponent, idempotency_key)
    VALUES (o.tenant_id, o.site_id, o.pms_interface_id, o.settlement_id, o.purchase_id, o.stay_id, o.g_number,
            o.posting_interface_revision_id, o.secret_generation_id, 'REVERSAL', o.id,
            v_amount, o.currency, o.currency_exponent, o.idempotency_key || ':rev:' || v_action_id::text)
    RETURNING id INTO v_rev;
  END IF;

  PERFORM set_config('iam_v2.p4_review_writer', '', true);

  IF p_action = 'ESCALATE' THEN
    UPDATE iam_v2.posting_review_state
       SET escalation_count = escalation_count + 1, review_version = review_version + 1, updated_at = now()
     WHERE posting_id = p_posting;
  ELSE
    IF p_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
      SELECT coalesce(max(attempt_no), 0) + 1 INTO v_next_attempt
        FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting;
    ELSE
      v_next_attempt := NULL;
    END IF;
    UPDATE iam_v2.posting_review_state
       SET terminal_action = p_action, terminal_action_id = v_action_id, decided_at = now(),
           retry_authorized_attempt_no = v_next_attempt, reversal_posting_id = v_rev,
           review_version = review_version + 1, updated_at = now()
     WHERE posting_id = p_posting;
  END IF;

  RETURN v_action_id;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 13. Owners, grants, and the first evaluation of posting permission for every existing stay.
-- ---------------------------------------------------------------------------------------------------------
ALTER FUNCTION iam_v2.trg_posting_charge_gate() SECURITY DEFINER;
ALTER FUNCTION iam_v2.trg_posting_charge_gate() SET search_path = iam_v2, pg_temp;
-- Now a definer: nobody calls a trigger function directly.
REVOKE ALL ON FUNCTION iam_v2.trg_posting_charge_gate() FROM PUBLIC;

REVOKE ALL ON FUNCTION iam_v2.p4_refresh_stay_posting_permission(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_stay_feed_confirms(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.trg_stay_posting_permission() FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_place_stay_posting_block(uuid,text,text,uuid,text,uuid,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_clear_posting_unresolved(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_admin_posting_block(uuid,uuid,uuid,text,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_stay_room_charge_open(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_attempt_not_sent_reason(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.pms_answer_confirmation_record(uuid,uuid,uuid,text,text,text,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_answer_effect(uuid,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_posting_abort_before_send(uuid,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_attempt_targets_posting_reservation() FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.stay_posting_blocks_guard() FROM PUBLIC;
REVOKE ALL ON iam_v2.stay_posting_blocks, iam_v2.pms_answer_confirmations, iam_v2.posting_presend_aborts FROM PUBLIC;

REVOKE ALL ON FUNCTION iam_v2.p4_lock_posting_stay(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_stay_postable_for(uuid,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_stay_postable_for(uuid,uuid) TO sc_posting_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_lock_posting_stay(uuid) TO sc_posting_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_answer_effect(uuid,text) TO sc_posting_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_posting_abort_before_send(uuid,text) TO sc_posting_runtime;
GRANT SELECT ON iam_v2.stay_posting_blocks TO sc_posting_runtime;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_stay_room_charge_open(uuid) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.pms_answer_confirmation_record(uuid,uuid,uuid,text,text,text,text,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_admin_posting_block(uuid,uuid,uuid,text,text,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_stay_room_charge_open(uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_attempt_not_sent_reason(uuid) TO svc_edged;
    GRANT SELECT ON iam_v2.stay_posting_blocks, iam_v2.pms_answer_confirmations, iam_v2.posting_presend_aborts TO svc_edged;
  END IF;
END
$grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.stay_posting_blocks OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.pms_answer_confirmations OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.posting_presend_aborts OWNER TO iam_v2_owner';
    EXECUTE 'ALTER VIEW iam_v2.posting_execution_state OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_attempt_targets_posting_reservation() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.stay_posting_blocks_guard() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_refresh_stay_posting_permission(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_stay_feed_confirms(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.trg_stay_posting_permission() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_place_stay_posting_block(uuid,text,text,uuid,text,uuid,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_clear_posting_unresolved(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_admin_posting_block(uuid,uuid,uuid,text,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_stay_room_charge_open(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_attempt_not_sent_reason(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_answer_confirmation_record(uuid,uuid,uuid,text,text,text,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_answer_effect(uuid,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_interface_financial_onboard(uuid,uuid,uuid,uuid,text,text,smallint,text,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.pms_interface_financially_ready(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.trg_posting_charge_gate() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_create_room_charge_posting(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_posting_command_authorised(uuid,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_posting_settlement_outcome(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_posting_review_apply(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_posting_abort_before_send(uuid,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_lock_posting_stay(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_stay_postable_for(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.request_full_resync(uuid,uuid,uuid,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,integer,bigint) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- Every existing stay gets its first evaluation now, so posting_allowed is never a stale default.
DO $backfill$
DECLARE r record;
BEGIN
  FOR r IN SELECT id FROM iam_v2.stays LOOP
    PERFORM iam_v2.p4_refresh_stay_posting_permission(r.id);
  END LOOP;
END $backfill$;

COMMIT;
