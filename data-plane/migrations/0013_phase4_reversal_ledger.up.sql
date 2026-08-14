-- 0013 — Phase 4: the PASSIVE reversal ledger, as the FINAL contract actually defines it.
-- Additive, reversible, DARK. No data. Authorization: D18 / T0029 (unchanged). Receipt: T0032.
--
-- WHAT 0012 GOT WRONG. 0012 read "programmatic reversal is capability=false" and concluded that a REVERSAL
-- row must not exist at all. That is not what the contract says, and the two halves of it have to be held
-- apart:
--
--   §9a rule 5 / Gate 3B  the EXECUTABLE reversal is unsupported in v1. PT=C and negative TA are
--                         UNVERIFIED and must never be transmitted. Corrections are manual Front Office
--                         operations.
--   §15 / §16             CREATE_REVERSAL is a Manual-Review action producing "a new ledger row
--                         referencing the original", and "reversal is a new REVERSAL row".
--
-- So the ledger row is REQUIRED and the sender is FORBIDDEN. 0012 forbade both, which would have made the
-- §15 action unimplementable and left an operator with no audited way to record that a charge was
-- corrected out of band — exactly the "visible, audited, operationally documented" limitation Gate 3B
-- requires in exchange for deferring the capability.
--
-- 0012 and its receipt T0031 are NOT rewritten. This migration corrects them additively and says so.
--
-- WHAT MAKES IT SAFE. The reversal row is structurally inert. It cannot acquire an outbox row, it cannot
-- acquire an attempt, and therefore it can never allocate a P# or produce a byte. There is no PT=C
-- anywhere, no negative TA, and no reversal sender. It is an audit record with financial arithmetic
-- attached, and the arithmetic is enforced.
BEGIN;

-- The blanket refusal goes. What replaces it is narrower and stronger: the row may exist, and it may never
-- become executable.
DROP TRIGGER IF EXISTS p4_no_programmatic_reversal ON iam_v2.pms_postings;
DROP FUNCTION IF EXISTS iam_v2.p4_no_programmatic_reversal();

-- ============================================================================
-- (1) The passive reversal ledger row: what one is allowed to look like.
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.p4_reversal_ledger_guard() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE orig record; v_already bigint;
BEGIN
  IF NEW.posting_type <> 'REVERSAL' THEN
    RETURN NEW;
  END IF;

  -- (a) Only the audited Manual-Review operation may write one. The same transaction-scoped token that
  -- guards the review ledger guards this, so a reversal cannot appear from anywhere else -- not from a
  -- worker, not from a repair script, not from an ad-hoc session.
  IF current_setting('iam_v2.p4_review_writer', true) IS DISTINCT FROM txid_current()::text THEN
    RAISE EXCEPTION 'REVERSAL_WRITER_ONLY: a reversal ledger row is created only by the audited '
                    'CREATE_REVERSAL review action'
      USING ERRCODE = 'insufficient_privilege';
  END IF;

  -- (b) It must reference a real, in-scope CHARGE. A reversal of nothing is not evidence of anything.
  SELECT id, posting_type, amount_minor, currency, currency_exponent, tenant_id, site_id, pms_interface_id
    INTO orig FROM iam_v2.pms_postings WHERE id = NEW.reverses_posting_id;
  IF orig.id IS NULL THEN
    RAISE EXCEPTION 'REVERSAL_ORIGINAL_UNKNOWN: posting % does not exist', NEW.reverses_posting_id
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF orig.posting_type <> 'CHARGE' THEN
    RAISE EXCEPTION 'REVERSAL_ORIGINAL_NOT_A_CHARGE: % is a %', orig.id, orig.posting_type
      USING ERRCODE = 'check_violation';
  END IF;
  IF orig.tenant_id <> NEW.tenant_id OR orig.site_id <> NEW.site_id
     OR orig.pms_interface_id <> NEW.pms_interface_id THEN
    RAISE EXCEPTION 'REVERSAL_OUT_OF_SCOPE: the original belongs to a different tenant/site/interface'
      USING ERRCODE = 'check_violation';
  END IF;

  -- (c) Same money, same units. A reversal in another currency would be an implicit conversion, and the
  -- exponent has to match for the sum below to mean anything at all.
  IF NEW.currency IS DISTINCT FROM orig.currency OR NEW.currency_exponent IS DISTINCT FROM orig.currency_exponent THEN
    RAISE EXCEPTION 'REVERSAL_CURRENCY_MISMATCH: reversal %/% <> original %/% (no implicit FX)',
      NEW.currency, NEW.currency_exponent, orig.currency, orig.currency_exponent
      USING ERRCODE = 'check_violation';
  END IF;

  -- (d) TA is a POSITIVE amount here, exactly as on a charge. The direction is carried by posting_type, not
  -- by a negative number -- §9a rule 5 says a negative TA is unverified, so this schema never stores one.
  IF NEW.amount_minor <= 0 THEN
    RAISE EXCEPTION 'REVERSAL_AMOUNT_INVALID: a reversal records a POSITIVE amount; direction is carried '
                    'by posting_type, never by a negative TA (section 9a rule 5)'
      USING ERRCODE = 'check_violation';
  END IF;

  -- (e) Cumulative bound: the sum of reversals may never exceed the charge they reverse.
  SELECT coalesce(sum(amount_minor), 0) INTO v_already
    FROM iam_v2.pms_postings
   WHERE posting_type = 'REVERSAL' AND reverses_posting_id = NEW.reverses_posting_id;
  IF v_already + NEW.amount_minor > orig.amount_minor THEN
    RAISE EXCEPTION 'REVERSAL_EXCEEDS_CHARGE: % already reversed + % requested > % charged',
      v_already, NEW.amount_minor, orig.amount_minor USING ERRCODE = 'check_violation';
  END IF;

  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_reversal_ledger_guard
  BEFORE INSERT ON iam_v2.pms_postings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_reversal_ledger_guard();

-- ============================================================================
-- (2) A reversal is structurally NON-EXECUTABLE.
-- ============================================================================
-- These two triggers are the whole of "capability = false". A reversal cannot be queued and cannot be
-- attempted, so no worker can pick it up, no P# can be allocated for it, and no byte can be built from it.
-- There is no code path to disable and no flag to get wrong.
CREATE OR REPLACE FUNCTION iam_v2.p4_reversal_never_executes() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_type text; v_posting uuid;
BEGIN
  -- Branch with IF, not CASE: plpgsql resolves record fields at RUNTIME, and a CASE evaluates the field
  -- reference in both arms, so NEW.posting_id would be looked up on posting_attempts and fail there.
  IF TG_TABLE_NAME = 'posting_outbox' THEN
    v_posting := NEW.posting_id;
  ELSE
    v_posting := NEW.internal_posting_id;
  END IF;
  SELECT posting_type INTO v_type FROM iam_v2.pms_postings WHERE id = v_posting;
  IF v_type = 'REVERSAL' THEN
    RAISE EXCEPTION 'REVERSAL_NOT_EXECUTABLE: programmatic PMS reversal is capability=false in v1. The '
                    'reversal ledger row is a passive audit record; correction is a manual Front Office '
                    'operation (section 9a rule 5, Gate 3B)'
      USING ERRCODE = 'feature_not_supported';
  END IF;
  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_reversal_never_queued
  BEFORE INSERT ON iam_v2.posting_outbox
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_reversal_never_executes();

CREATE TRIGGER p4_reversal_never_attempted
  BEFORE INSERT ON iam_v2.posting_attempts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_reversal_never_executes();

-- ============================================================================
-- (3) The gates that exist to protect TRANSMISSION do not apply to a record that never transmits.
-- ============================================================================
-- Recording that a charge was corrected out of band must be possible when the interface is down -- indeed
-- especially then, because that is when corrections happen. Requiring a fresh, ACTIVE, in-house interface
-- to write an audit row would make the audit trail unavailable exactly when it is needed.
CREATE OR REPLACE FUNCTION iam_v2.p4_posting_freshness_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_block text;
BEGIN
  IF NEW.posting_type = 'REVERSAL' THEN
    RETURN NEW;                       -- passive ledger row; it will never reach a wire
  END IF;
  v_block := iam_v2.p4_interface_freshness_block(
    NEW.tenant_id, NEW.site_id, NEW.pms_interface_id, NEW.posting_interface_revision_id, now());
  IF v_block IS NOT NULL THEN
    RAISE EXCEPTION 'INTERFACE_NOT_FRESH: %', v_block USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

CREATE OR REPLACE FUNCTION iam_v2.p4_posting_lifecycle_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_state text;
BEGIN
  SELECT lifecycle_state INTO v_state FROM iam_v2.pms_interfaces
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.pms_interface_id;
  IF v_state IS NULL THEN
    RAISE EXCEPTION 'INTERFACE_UNKNOWN: no interface % in tenant/site', NEW.pms_interface_id
      USING ERRCODE = 'foreign_key_violation';
  END IF;
  IF NEW.posting_type = 'REVERSAL' THEN
    RETURN NEW;                       -- an audit record, not new financial work
  END IF;
  IF v_state IN ('DRAINING','DECOMMISSIONED') THEN
    RAISE EXCEPTION 'INTERFACE_NOT_ACCEPTING_WORK: interface % is %; no new financial work may be created',
      NEW.pms_interface_id, v_state USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

-- The FIAS exponent bound also exists to protect the wire. A reversal inherits the original's exponent by
-- (c) above, so it is already consistent, and applying the wire rule to it would be applying a transmission
-- constraint to something that never transmits.
CREATE OR REPLACE FUNCTION iam_v2.p4_fias_exponent_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_kind text;
BEGIN
  IF NEW.posting_type = 'REVERSAL' THEN
    RETURN NEW;
  END IF;
  SELECT connector_kind INTO v_kind FROM iam_v2.pms_interfaces
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.pms_interface_id;
  IF v_kind = 'protel-fias' AND NEW.currency_exponent IS NOT NULL AND NEW.currency_exponent <> 2 THEN
    RAISE EXCEPTION 'FIAS_EXPONENT_UNSUPPORTED: the protel-fias posting path is exponent 2 by contract '
                    '(section 9a); posting exponent is %', NEW.currency_exponent
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

ALTER TABLE iam_v2.posting_review_state
  ADD COLUMN reversal_posting_id uuid,
  ADD CONSTRAINT prs_reversal_needs_action
    CHECK (reversal_posting_id IS NULL OR terminal_action = 'CREATE_REVERSAL');

-- ============================================================================
-- (4) CREATE_REVERSAL now does what §15 says it does.
-- ============================================================================
-- The review writer creates the ledger row itself, in the same transaction as the immutable action record,
-- so the decision and its financial consequence cannot come apart. p_reversal_amount defaults to the full
-- original amount, which is the ordinary case; a partial correction states its own amount and is bounded
-- by (e) above.
CREATE OR REPLACE FUNCTION iam_v2.record_posting_review_action(
  p_posting uuid, p_action text, p_actor uuid, p_reason text,
  p_evidence jsonb DEFAULT '{}'::jsonb, p_expected_version int DEFAULT NULL,
  p_reversal_amount bigint DEFAULT NULL)
RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
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
      (tenant_id, site_id, pms_interface_id, settlement_id, purchase_id, stay_id, folio_id,
       posting_interface_revision_id, secret_generation_id, posting_type, reverses_posting_id,
       amount_minor, currency, currency_exponent, idempotency_key)
    VALUES (o.tenant_id, o.site_id, o.pms_interface_id, o.settlement_id, o.purchase_id, o.stay_id, o.folio_id,
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
END $fn$;
REVOKE EXECUTE ON FUNCTION
  iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int,bigint) FROM PUBLIC;
-- the 0012 six-argument signature is superseded; drop it so there is exactly ONE review writer
DROP FUNCTION IF EXISTS iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int);

INSERT INTO public.schema_migrations (version) VALUES ('0013_phase4_reversal_ledger') ON CONFLICT DO NOTHING;

COMMIT;
