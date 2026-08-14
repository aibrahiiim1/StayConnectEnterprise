-- 0012 DOWN — reverse of 0012_phase4_financial_hardening.up.sql. Reverses ONLY 0012.
--
-- The two objects 0012 REPLACED (record_posting_review_action and the posting_execution_state view) are
-- restored to their 0011 definitions verbatim, so a rollback leaves 0011 exactly as 0011 built it rather
-- than leaving a half-hardened hybrid behind.
BEGIN;

DELETE FROM public.schema_migrations WHERE version = '0012_phase4_financial_hardening';

DROP TRIGGER IF EXISTS p4_no_programmatic_reversal ON iam_v2.pms_postings;
DROP FUNCTION IF EXISTS iam_v2.p4_no_programmatic_reversal();

DROP TRIGGER IF EXISTS p4_consume_retry_authorization ON iam_v2.posting_attempts;
DROP FUNCTION IF EXISTS iam_v2.p4_consume_retry_authorization();

DROP TRIGGER IF EXISTS p4_zz_attempt_freshness_gate ON iam_v2.posting_attempts;
DROP FUNCTION IF EXISTS iam_v2.p4_attempt_freshness_gate();
DROP TRIGGER IF EXISTS p4_zz_posting_freshness_gate ON iam_v2.pms_postings;
DROP FUNCTION IF EXISTS iam_v2.p4_posting_freshness_gate();

DROP TRIGGER IF EXISTS p4_fias_exponent_gate ON iam_v2.pms_postings;
DROP FUNCTION IF EXISTS iam_v2.p4_fias_exponent_gate();

DROP TRIGGER IF EXISTS p4_interface_decommission_gate ON iam_v2.pms_interfaces;
DROP FUNCTION IF EXISTS iam_v2.p4_interface_decommission_gate();
DROP TRIGGER IF EXISTS p4_attempt_lifecycle_gate ON iam_v2.posting_attempts;
DROP FUNCTION IF EXISTS iam_v2.p4_attempt_lifecycle_gate();
DROP TRIGGER IF EXISTS p4_posting_lifecycle_gate ON iam_v2.pms_postings;
DROP FUNCTION IF EXISTS iam_v2.p4_posting_lifecycle_gate();

DROP INDEX IF EXISTS iam_v2.outbox_one_inflight_per_interface;

-- restore the 0011 read model (the freshness column and the consumed flag go away with it)
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
  (la.outcome = 'UNKNOWN' AND rs.terminal_action IS NULL) AS awaiting_manual_review
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

DROP FUNCTION IF EXISTS iam_v2.p4_interface_freshness_block(uuid,uuid,uuid,uuid,timestamptz);

ALTER TABLE iam_v2.posting_review_state
  DROP CONSTRAINT IF EXISTS prs_consumed_needs_authorization,
  DROP COLUMN IF EXISTS retry_authorization_consumed_at;

-- restore the 0011 review writer verbatim (no evidence requirement, no action/state matrix)
CREATE OR REPLACE FUNCTION iam_v2.record_posting_review_action(
  p_posting uuid, p_action text, p_actor uuid, p_reason text,
  p_evidence jsonb DEFAULT '{}'::jsonb, p_expected_version int DEFAULT NULL)
RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_t uuid; v_s uuid; st record; v_action_id uuid; v_next_attempt int; v_attempts int;
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

COMMIT;
