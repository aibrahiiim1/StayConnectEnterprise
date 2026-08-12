-- 0012 — Phase 4 financial-core HARDENING. Additive, reversible, DARK. No data.
-- Authorization: D18 / T0029 (unchanged). Corrective receipt: T0031.
--
-- WHY A SEPARATE MIGRATION. 0011 is recorded as delivered and verified in transition receipt T0030 with a
-- measured catalog fingerprint. It has never been applied outside disposable PostgreSQL, so amending it in
-- place would have been operationally harmless — and historically dishonest. Migrations are append-only in
-- this repository for the same reason the ledgers are: the record of what was built has to survive being
-- wrong. 0012 corrects 0011 in the open.
--
-- WHAT WAS WRONG, measured against the FINAL Phase-0 Contract:
--
--   (1) LANE SERIALIZATION WAS OVERCLAIMED. outbox_one_active stops two ACTIVE rows for the SAME posting.
--       It does NOT stop two DIFFERENT postings on ONE interface being in flight at once, which is exactly
--       what §10 "per-interface serialized financial lanes" forbids — a FIAS interface is a single ordered
--       socket, and two concurrent PS records on it is a protocol violation, not merely a race.
--
--   (2) INTERFACE LIFECYCLE WAS WRONG. §10 states AUTH_DISABLED blocks new guest AUTHENTICATION while
--       "posting/events continue"; DRAINING blocks new work while "outbox drains". The Phase-4 gate refused
--       everything that was not ACTIVE, which would have stranded legitimate money on an interface that was
--       merely closed to new logins, and would have refused to drain a DRAINING interface at all.
--
--   (3) FRESHNESS WAS NOT CHECKED AT ALL. §9/D10 require financial creation to fail closed on a stale,
--       disconnected, discontinuous or out-of-sync interface. C32 was reported as satisfied on the strength
--       of the stay-eligibility check alone; the four runtime axes were never consulted.
--
--   (4) THE FIAS EXPONENT WAS GENERALIZED. §9a fixes `TA` at integer minor units with EXPONENT 2 for this
--       posting path, and the Gate-3A live evidence is a USD 1.00 debit at exponent 2. 0011 allowed 0..4 on
--       the interface currency, which is right for the currency MODEL and wrong for what may be posted.
--
--   (5) RETRY AUTHORIZATION WAS NOT CONSUMED. record_posting_review_action recorded the authorized attempt
--       number, and nothing ever cleared it, so the authorization stayed live after the retry it authorized.
--
--   (6) CONFIRM_NOT_POSTED_RETRY COULD RETRY A POSTED CHARGE. Nothing checked the attempt ledger, so an
--       operator could authorize a second PS for a charge the PMS had already ACKed OK — the exact
--       duplicate-charge outcome the whole UNKNOWN design exists to prevent.
--
--   (7) REVIEW EVIDENCE WAS OPTIONAL IN PRACTICE. §15 requires reason AND evidence; the default '{}' meant
--       a terminal financial decision could be recorded with no evidence at all.
BEGIN;

-- ============================================================================
-- (1) Per-interface lane serialization — at most ONE in-flight PS per interface.
-- ============================================================================
-- The bound is ONE, not a tunable, because the contract's lane is a single ordered FIAS socket and the P#
-- correlation window is per-interface. Two PS records in flight on one interface cannot be correlated
-- safely no matter how the answers arrive.
--
-- A partial UNIQUE index is the right shape: it costs nothing for QUEUED/DONE/HELD_RECOVERY rows, it is
-- enforced against every writer including a future one, and a worker that tries to claim a second posting
-- on a busy interface fails at COMMIT rather than at a code path someone has to remember to write.
CREATE UNIQUE INDEX outbox_one_inflight_per_interface
  ON iam_v2.posting_outbox (pms_interface_id) WHERE state = 'IN_FLIGHT';

COMMENT ON INDEX iam_v2.outbox_one_inflight_per_interface IS
  'Contract 10: a PMS Interface is ONE serialized financial lane. At most one posting may be IN_FLIGHT on '
  'it at a time. Distinct interfaces are unaffected and remain fully independent.';

-- ============================================================================
-- (2) Interface lifecycle, as the contract actually defines it.
-- ============================================================================
-- ACTIVE          new financial work and draining both permitted
-- AUTH_DISABLED   no new guest AUTH (a Phase-3 concern); posting continues -- so both permitted here
-- DRAINING        no NEW financial work; the existing outbox drains
-- DECOMMISSIONED  terminal, fail-closed for both
--
-- The distinction is enforced where the two actions differ: creation looks at the interface state, and
-- execution does not refuse DRAINING. Execution's own refusal for DECOMMISSIONED is below.
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
  -- Creating NEW financial work. AUTH_DISABLED is deliberately permitted: it closes guest authentication,
  -- not the folio.
  IF v_state IN ('DRAINING','DECOMMISSIONED') THEN
    RAISE EXCEPTION 'INTERFACE_NOT_ACCEPTING_WORK: interface % is %; no new financial work may be created',
      NEW.pms_interface_id, v_state USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_posting_lifecycle_gate
  BEFORE INSERT ON iam_v2.pms_postings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_posting_lifecycle_gate();

-- A DECOMMISSIONED interface may not transmit either. This guards the attempt, which is the row that
-- exists only because bytes are about to go out.
CREATE OR REPLACE FUNCTION iam_v2.p4_attempt_lifecycle_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_state text;
BEGIN
  SELECT lifecycle_state INTO v_state FROM iam_v2.pms_interfaces
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.pms_interface_id;
  IF v_state = 'DECOMMISSIONED' THEN
    RAISE EXCEPTION 'INTERFACE_DECOMMISSIONED: interface % may not transmit', NEW.pms_interface_id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_attempt_lifecycle_gate
  BEFORE INSERT ON iam_v2.posting_attempts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_attempt_lifecycle_gate();

-- Decommissioning itself is the contract's own precondition, enforced rather than documented: zero
-- PENDING/SENDING/UNKNOWN postings. The audited override the contract allows is a Manual-Review routing
-- decision and is not implemented in v1, so this is unconditional here.
CREATE OR REPLACE FUNCTION iam_v2.p4_interface_decommission_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_open int;
BEGIN
  IF NEW.lifecycle_state = 'DECOMMISSIONED' AND OLD.lifecycle_state IS DISTINCT FROM 'DECOMMISSIONED' THEN
    SELECT count(*) INTO v_open
      FROM iam_v2.posting_outbox o
     WHERE o.pms_interface_id = NEW.id AND o.state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY');
    IF v_open > 0 THEN
      RAISE EXCEPTION 'DECOMMISSION_BLOCKED: interface % still has % non-terminal posting(s)', NEW.id, v_open
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT count(*) INTO v_open
      FROM iam_v2.posting_attempts a
     WHERE a.pms_interface_id = NEW.id AND a.outcome IN ('SENDING','UNKNOWN');
    IF v_open > 0 THEN
      RAISE EXCEPTION 'DECOMMISSION_BLOCKED: interface % still has % SENDING/UNKNOWN attempt(s)', NEW.id, v_open
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_interface_decommission_gate
  BEFORE UPDATE ON iam_v2.pms_interfaces
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_interface_decommission_gate();

-- ============================================================================
-- (4) The FIAS posting path is exponent 2. Full stop.
-- ============================================================================
-- Contract 9a: "TA integer minor units, exponent 2, no currency code on the wire", and the Gate-3A live
-- evidence is a USD 1.00 debit at exponent 2. The interface currency MODEL keeps its 0..4 range because
-- that models real ISO-4217 currencies; what changes here is what may be POSTED over this connector.
-- Generalizing the wire beyond the verified evidence would be inventing protocol behaviour.
CREATE OR REPLACE FUNCTION iam_v2.p4_fias_exponent_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_kind text;
BEGIN
  SELECT connector_kind INTO v_kind FROM iam_v2.pms_interfaces
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.pms_interface_id;
  -- A NULL exponent is a MISSING currency, not an unsupported one. Letting it fall through to the
  -- currency gate keeps the operator-facing reason the actionable one.
  IF v_kind = 'protel-fias' AND NEW.currency_exponent IS NOT NULL AND NEW.currency_exponent <> 2 THEN
    RAISE EXCEPTION 'FIAS_EXPONENT_UNSUPPORTED: the protel-fias posting path is exponent 2 by contract '
                    '(section 9a); posting exponent is %', NEW.currency_exponent
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_fias_exponent_gate
  BEFORE INSERT ON iam_v2.pms_postings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_fias_exponent_gate();

-- ============================================================================
-- (3) The four PMS runtime freshness axes, at the financial boundary.
-- ============================================================================
-- These are the SAME axes Phase 3 already maintains in iam_v2.pms_interface_runtime, read against the SAME
-- per-revision thresholds Phase 3 already stores in the revision config. No second freshness model is
-- introduced, and nothing here writes to the runtime row.
--
--   axis 1  transport    CONNECTED, and a heartbeat within heartbeat_timeout_ms
--   axis 2  continuity   CONTINUOUS, and a valid event within feed_freshness_ms
--   axis 3  complete sync IN_SYNC, and a complete sync within complete_sync_ms
--   axis 4  pin coherence the runtime's pinned revision IS the revision this posting pinned, and no resync
--                         generation is part-published (published = allocated)
--
-- p_at is passed in rather than read from now() so the function is testable and so a caller's transaction
-- time is used consistently. Returns NULL when every axis is green, or the code of the FIRST failed axis.
CREATE OR REPLACE FUNCTION iam_v2.p4_interface_freshness_block(
  p_tenant uuid, p_site uuid, p_interface uuid, p_revision uuid, p_at timestamptz)
RETURNS text
  LANGUAGE plpgsql STABLE SET search_path = iam_v2, pg_temp AS $fn$
DECLARE r record; hb_ms bigint; fresh_ms bigint; sync_ms bigint;
BEGIN
  SELECT * INTO r FROM iam_v2.pms_interface_runtime
   WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface;
  IF NOT FOUND THEN
    RETURN 'RUNTIME_UNKNOWN';           -- no runtime state at all: fail closed, never assume healthy
  END IF;

  SELECT (config->>'heartbeat_timeout_ms')::bigint, (config->>'feed_freshness_ms')::bigint,
         (config->>'complete_sync_ms')::bigint
    INTO hb_ms, fresh_ms, sync_ms
    FROM iam_v2.pms_interface_revisions
   WHERE tenant_id = p_tenant AND site_id = p_site AND pms_interface_id = p_interface AND id = p_revision;

  -- axis 1: transport
  IF r.transport_status <> 'CONNECTED' THEN RETURN 'TRANSPORT_' || r.transport_status; END IF;
  IF hb_ms IS NOT NULL AND (r.last_heartbeat_at IS NULL
       OR r.last_heartbeat_at < p_at - make_interval(secs => hb_ms / 1000.0)) THEN
    RETURN 'TRANSPORT_HEARTBEAT_STALE';
  END IF;

  -- axis 2: feed continuity
  IF r.continuity_status <> 'CONTINUOUS' THEN RETURN 'CONTINUITY_' || r.continuity_status; END IF;
  IF fresh_ms IS NOT NULL AND (r.last_valid_event_at IS NULL
       OR r.last_valid_event_at < p_at - make_interval(secs => fresh_ms / 1000.0)) THEN
    RETURN 'CONTINUITY_FEED_STALE';
  END IF;

  -- axis 3: complete sync
  IF r.sync_status <> 'IN_SYNC' THEN RETURN 'SYNC_' || r.sync_status; END IF;
  IF sync_ms IS NOT NULL AND (r.last_complete_sync_at IS NULL
       OR r.last_complete_sync_at < p_at - make_interval(secs => sync_ms / 1000.0)) THEN
    RETURN 'SYNC_STALE';
  END IF;

  -- axis 4: pin coherence
  IF r.pinned_revision_id IS DISTINCT FROM p_revision THEN RETURN 'PIN_REVISION_MISMATCH'; END IF;
  IF r.published_resync_generation <> r.resync_generation_seq THEN RETURN 'PIN_RESYNC_IN_FLIGHT'; END IF;

  RETURN NULL;
END $fn$;

CREATE OR REPLACE FUNCTION iam_v2.p4_posting_freshness_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_block text;
BEGIN
  v_block := iam_v2.p4_interface_freshness_block(
    NEW.tenant_id, NEW.site_id, NEW.pms_interface_id, NEW.posting_interface_revision_id, now());
  IF v_block IS NOT NULL THEN
    RAISE EXCEPTION 'INTERFACE_NOT_FRESH: %', v_block USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

-- Named to fire AFTER the currency and lifecycle gates, so the most actionable refusal still wins.
CREATE TRIGGER p4_zz_posting_freshness_gate
  BEFORE INSERT ON iam_v2.pms_postings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_posting_freshness_gate();

-- Revalidated again at transmission: an interface that went stale between authorization and the wire must
-- stop the bytes, not merely have been fresh once.
CREATE OR REPLACE FUNCTION iam_v2.p4_attempt_freshness_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_block text; v_rev uuid;
BEGIN
  SELECT posting_interface_revision_id INTO v_rev FROM iam_v2.pms_postings WHERE id = NEW.internal_posting_id;
  v_block := iam_v2.p4_interface_freshness_block(NEW.tenant_id, NEW.site_id, NEW.pms_interface_id, v_rev, now());
  IF v_block IS NOT NULL THEN
    RAISE EXCEPTION 'INTERFACE_NOT_FRESH: %', v_block USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_zz_attempt_freshness_gate
  BEFORE INSERT ON iam_v2.posting_attempts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_attempt_freshness_gate();

-- ============================================================================
-- (5)(6)(7) Manual Review: single-use authorization, no retry of a posted charge, real evidence.
-- ============================================================================
ALTER TABLE iam_v2.posting_review_state
  ADD COLUMN retry_authorization_consumed_at timestamptz,
  ADD CONSTRAINT prs_consumed_needs_authorization
    CHECK (retry_authorization_consumed_at IS NULL OR terminal_action = 'CONFIRM_NOT_POSTED_RETRY');

-- The authorization is consumed by the attempt it authorized, in the SAME transaction that creates it.
-- Consumption is what makes it single-use: the retry gate reads retry_authorized_attempt_no, and this
-- clears it the moment the attempt exists, so a second Requeue finds nothing to act on.
CREATE OR REPLACE FUNCTION iam_v2.p4_consume_retry_authorization() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  UPDATE iam_v2.posting_review_state
     SET retry_authorized_attempt_no = NULL,
         retry_authorization_consumed_at = now(),
         review_version = review_version + 1,
         updated_at = now()
   WHERE posting_id = NEW.internal_posting_id
     AND terminal_action = 'CONFIRM_NOT_POSTED_RETRY'
     AND retry_authorized_attempt_no = NEW.attempt_no;
  RETURN NULL;
END $fn$;

CREATE TRIGGER p4_consume_retry_authorization
  AFTER INSERT ON iam_v2.posting_attempts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_consume_retry_authorization();

-- The review writer, corrected. Everything 0011 established is kept: the advisory lock, the row lock, the
-- optimistic version, the incompatible-decision refusal and the append-only ledger with one writer.
CREATE OR REPLACE FUNCTION iam_v2.record_posting_review_action(
  p_posting uuid, p_action text, p_actor uuid, p_reason text,
  p_evidence jsonb DEFAULT '{}'::jsonb, p_expected_version int DEFAULT NULL)
RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_t uuid; v_s uuid; st record; v_action_id uuid; v_next_attempt int; v_attempts int; la record;
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
  -- Section 15 requires evidence, not a placeholder. A terminal financial decision recorded with '{}' is
  -- an unexplained decision, and an unexplained decision is what an audit cannot accept.
  IF p_action <> 'ESCALATE'
     AND (p_evidence IS NULL OR jsonb_typeof(p_evidence) <> 'object' OR p_evidence = '{}'::jsonb) THEN
    RAISE EXCEPTION 'REVIEW_EVIDENCE_REQUIRED: a terminal financial decision must record its evidence'
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT tenant_id, site_id INTO v_t, v_s FROM iam_v2.pms_postings WHERE id = p_posting;
  IF v_t IS NULL THEN
    RAISE EXCEPTION 'REVIEW_POSTING_UNKNOWN: posting % does not exist', p_posting
      USING ERRCODE = 'foreign_key_violation';
  END IF;

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

  -- THE ACTION/STATE MATRIX. A retry is only meaningful for an attempt whose outcome is genuinely
  -- unresolved or genuinely not sent. Authorizing a retry for a charge the PMS ACKed OK would create the
  -- duplicate debit the whole UNKNOWN design exists to prevent, and no amount of operator confidence makes
  -- that safe -- the PMS has already said it posted.
  IF p_action = 'CONFIRM_NOT_POSTED_RETRY' THEN
    SELECT attempt_no, outcome, pa_as_status INTO la
      FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting
      ORDER BY attempt_no DESC LIMIT 1;
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

  PERFORM set_config('iam_v2.p4_review_writer', txid_current()::text, true);
  INSERT INTO iam_v2.posting_review_actions (tenant_id, site_id, posting_id, action, actor, reason, evidence)
  VALUES (v_t, v_s, p_posting, p_action, p_actor, p_reason, coalesce(p_evidence, '{}'::jsonb))
  RETURNING id INTO v_action_id;
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
           retry_authorized_attempt_no = v_next_attempt,
           review_version = review_version + 1, updated_at = now()
     WHERE posting_id = p_posting;
  END IF;

  RETURN v_action_id;
END $fn$;
REVOKE EXECUTE ON FUNCTION
  iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int) FROM PUBLIC;

-- CREATE_REVERSAL stays NON-PROGRAMMATIC in v1. This is enforced, not merely documented: a REVERSAL
-- posting row cannot be created at all, so there is no automatic negative posting, no PT=C assumption and
-- no executable reversal sender anywhere in the system. The passive ledger SHAPE from mg7 is untouched, so
-- a future authorized version can lift this one trigger without a schema change.
CREATE OR REPLACE FUNCTION iam_v2.p4_no_programmatic_reversal() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF NEW.posting_type = 'REVERSAL' THEN
    RAISE EXCEPTION 'PROGRAMMATIC_REVERSAL_DISABLED: reversal is a manual, out-of-band correction in v1 '
                    '(capability = false); no reversal posting may be created programmatically'
      USING ERRCODE = 'feature_not_supported';
  END IF;
  RETURN NEW;
END $fn$;

CREATE TRIGGER p4_no_programmatic_reversal
  BEFORE INSERT ON iam_v2.pms_postings
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_no_programmatic_reversal();

-- ============================================================================
-- Read model: surface the corrections it can now derive.
-- ============================================================================
-- DROP + CREATE rather than CREATE OR REPLACE: the new columns are not appended at the end, and
-- PostgreSQL refuses to reshape a view in place. Nothing depends on the view, so dropping it is safe.
DROP VIEW IF EXISTS iam_v2.posting_execution_state;
CREATE VIEW iam_v2.posting_execution_state AS
SELECT
  p.id                       AS posting_id,
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
    WHEN la.attempt_no IS NULL THEN 'NOT_ATTEMPTED'
    WHEN la.outcome = 'SENDING' THEN 'IN_FLIGHT'
    WHEN la.outcome = 'UNKNOWN' THEN 'UNKNOWN'
    WHEN la.outcome = 'FAILED'  THEN 'NOT_SENT'
    WHEN la.outcome = 'ACKED' AND la.pa_as_status = 'OK' THEN 'POSTED'
    WHEN la.outcome = 'ACKED'   THEN 'REJECTED'
  END                        AS execution_state,
  la.attempt_no              AS latest_attempt_no,
  la.p_number                AS latest_p_number,
  la.outcome                 AS latest_attempt_outcome,
  la.pa_as_status            AS latest_pa_as_status,
  ac.attempt_count,
  ac.unknown_attempt_count,
  (ac.unknown_attempt_count > 0)               AS has_unknown_history,
  ob.state                   AS outbox_state,
  rs.terminal_action         AS terminal_review_action,
  rs.review_version,
  rs.escalation_count,
  rs.retry_authorized_attempt_no,
  (rs.retry_authorization_consumed_at IS NOT NULL) AS retry_authorization_consumed,
  (la.outcome = 'UNKNOWN' AND rs.terminal_action IS NULL) AS awaiting_manual_review,
  -- the live freshness verdict for this posting's own pinned revision: NULL means every axis is green
  iam_v2.p4_interface_freshness_block(p.tenant_id, p.site_id, p.pms_interface_id,
                                      p.posting_interface_revision_id, now()) AS freshness_block
FROM iam_v2.pms_postings p
LEFT JOIN LATERAL (
  SELECT a.attempt_no, a.outcome, a.p_number, a.pa_as_status
    FROM iam_v2.posting_attempts a
   WHERE a.internal_posting_id = p.id
   ORDER BY a.attempt_no DESC LIMIT 1) la ON true
LEFT JOIN LATERAL (
  SELECT count(*)::bigint AS attempt_count,
         count(*) FILTER (WHERE a.outcome = 'UNKNOWN')::bigint AS unknown_attempt_count
    FROM iam_v2.posting_attempts a
   WHERE a.internal_posting_id = p.id) ac ON true
LEFT JOIN LATERAL (
  SELECT o.state FROM iam_v2.posting_outbox o
   WHERE o.posting_id = p.id AND o.state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY') LIMIT 1) ob ON true
LEFT JOIN iam_v2.posting_review_state rs ON rs.posting_id = p.id;

INSERT INTO public.schema_migrations (version) VALUES ('0012_phase4_financial_hardening') ON CONFLICT DO NOTHING;

COMMIT;
