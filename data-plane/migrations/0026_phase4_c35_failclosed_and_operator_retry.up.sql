-- 0026 — PHASE 4: C35 fails closed on a VERIFIED receipt, and the zero-attempt retry becomes reachable by
-- the real operator surface. D18 / T0029. Receipt: T0041. Additive, reversible, DARK.
--
-- TWO MEASURED FINDINGS FROM 0025.
--
-- (a) C35 DID NOT FAIL CLOSED. p4_assert_compliance_archived passed as soon as ANY archive row existed for
--     the tenant, regardless of receipt_verified. So the local export plus its digest -- written by the same
--     appliance that is about to delete the data -- was sufficient to authorize a cross-customer purge.
--     That is self-certification: the party doing the deleting attests that it kept a copy, and nothing
--     outside it agrees. The contract asks for a compliance archive before purge; an archive nobody has
--     acknowledged custody of is not one.
--
--     The correction makes the gate require receipt_verified = true. Since no external archival receipt
--     authority exists in this product -- no service, no endpoint, no issued key -- the honest consequence
--     is that CROSS-CUSTOMER PURGE IS NOW IMPOSSIBLE. That is the point. A capability that cannot be
--     performed safely should be unavailable, not quietly available on weaker evidence, and the appliance
--     already fails closed (no guest authorization) when a transition cannot complete -- so the safe
--     behaviour is a held appliance and a visible reason, not a deletion nobody can vouch for.
--
--     Nothing here fabricates a receipt, an authority, a key or managed state. The verification path exists
--     and is unusable until something real can satisfy it.
--
-- (b) THE ZERO-ATTEMPT RETRY WAS UNREACHABLE. p4_authorize_zero_attempt_retry exists and is proven, but
--     nothing calls it: the operator surface has no route to it, so a posting restored with no surviving
--     attempts is still stuck in practice. A safe path nobody can walk is not a path. The API and UI land
--     in this milestone; what this migration adds is the read model they need to find such postings, which
--     the existing review queue cannot show because it keys on attempts.
BEGIN;

-- ============================================================================
-- (a) C35 — the gate now requires a VERIFIED receipt
-- ============================================================================
-- receipt_verified may only become true through a recorded external verification, so it needs somewhere to
-- record WHAT was verified and BY WHOM. These columns exist so that the day an authority does exist, the
-- evidence has a home; until then they stay NULL and the gate stays shut.
ALTER TABLE iam_v2.compliance_archives
  ADD COLUMN IF NOT EXISTS receipt_authority text,
  ADD COLUMN IF NOT EXISTS receipt_reference text,
  ADD COLUMN IF NOT EXISTS receipt_verified_at timestamptz;

-- A verified receipt is a claim about an EXTERNAL party. If the flag is true, the row must say who and
-- what; if it is false, it must not pretend to. Structural, so no code path can set the flag alone.
ALTER TABLE iam_v2.compliance_archives
  ADD CONSTRAINT ca_receipt_evidence_matches_flag CHECK (
    (receipt_verified = false)
    OR (receipt_verified = true
        AND receipt_authority IS NOT NULL AND btrim(receipt_authority) <> ''
        AND receipt_reference IS NOT NULL AND btrim(receipt_reference) <> ''
        AND receipt_verified_at IS NOT NULL));

COMMENT ON COLUMN iam_v2.compliance_archives.receipt_verified IS
  'TRUE only when an EXTERNAL archival authority has acknowledged custody of the artefact, with the '
  'authority and its reference recorded alongside. No such authority exists in this product, so this is '
  'false everywhere and cross-customer purge is consequently impossible. That is the intended failure '
  'mode: the alternative is letting the appliance that is about to delete the data certify its own copy.';

-- The gate. It is the ONLY thing that authorizes a cross-customer purge, and it now asks the question the
-- contract actually asks: has someone outside this appliance acknowledged holding the archive?
CREATE OR REPLACE FUNCTION iam_v2.p4_assert_compliance_archived(p_tenant uuid)
RETURNS void
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_any int; v_verified int;
BEGIN
  SELECT count(*) INTO v_any FROM iam_v2.compliance_archives
   WHERE tenant_id = p_tenant AND purpose = 'CROSS_CUSTOMER_PURGE';
  IF v_any = 0 THEN
    RAISE EXCEPTION 'COMPLIANCE_ARCHIVE_MISSING: tenant % has no compliance archive; its data may not be '
                    'purged until one has been produced and its custody acknowledged', p_tenant
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT count(*) INTO v_verified FROM iam_v2.compliance_archives
   WHERE tenant_id = p_tenant AND purpose = 'CROSS_CUSTOMER_PURGE' AND receipt_verified;
  IF v_verified = 0 THEN
    RAISE EXCEPTION 'COMPLIANCE_RECEIPT_UNVERIFIED: tenant % has an archive but no EXTERNAL authority has '
                    'acknowledged custody of it. A local copy attested only by the appliance that is about '
                    'to delete the data is self-certification, not a compliance archive. No archival '
                    'receipt authority exists in this product, so this purge cannot proceed', p_tenant
      USING ERRCODE = 'check_violation';
  END IF;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_assert_compliance_archived(uuid) FROM PUBLIC;

-- Recording a receipt requires the external evidence. There is deliberately no parameter that sets the flag
-- without it, and no default that could make it true.
CREATE OR REPLACE FUNCTION iam_v2.p4_record_compliance_receipt(
  p_archive uuid, p_authority text, p_reference text)
RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE a record;
BEGIN
  IF p_authority IS NULL OR btrim(p_authority) = '' OR p_reference IS NULL OR btrim(p_reference) = '' THEN
    RAISE EXCEPTION 'COMPLIANCE_RECEIPT_EVIDENCE_REQUIRED: a verified receipt names the authority that '
                    'acknowledged custody and its reference' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO a FROM iam_v2.compliance_archives WHERE id = p_archive FOR UPDATE;
  IF a.id IS NULL THEN
    RAISE EXCEPTION 'COMPLIANCE_ARCHIVE_UNKNOWN: %', p_archive USING ERRCODE = 'no_data_found';
  END IF;
  IF a.receipt_verified THEN
    RAISE EXCEPTION 'COMPLIANCE_RECEIPT_ALREADY_RECORDED: custody was already acknowledged by % at %',
      a.receipt_authority, a.receipt_verified_at USING ERRCODE = 'check_violation';
  END IF;
  UPDATE iam_v2.compliance_archives
     SET receipt_verified = true, receipt_authority = p_authority, receipt_reference = p_reference,
         receipt_verified_at = now(), receipt_blocked_reason = NULL
   WHERE id = p_archive;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_record_compliance_receipt(uuid,text,text) FROM PUBLIC;
-- Granted to NOBODY. There is no authority to receive a receipt from, so there is no runtime that could
-- legitimately call this. It exists so the shape is right the day one exists; granting it now would be
-- creating the capability by the back door.

-- ============================================================================
-- (b) The read model for postings the review queue cannot see
-- ============================================================================
-- posting_execution_state keys its review queue on attempts. A posting restored with ZERO attempts has
-- none, so it never appears -- which is precisely why the operator surface could not reach the safe path.
-- This view is the one place that lists them, with everything the audited authorization checks so the
-- screen can show WHY an item is or is not eligible rather than letting an operator discover it by being
-- refused.
CREATE OR REPLACE VIEW iam_v2.v_zero_attempt_recovery_queue AS
SELECT
  o.tenant_id,
  o.site_id,
  o.posting_id,
  o.id                            AS outbox_id,
  p.pms_interface_id,
  p.amount_minor,
  p.currency,
  p.currency_exponent,
  h.id                            AS hold_id,
  h.resolution                    AS hold_resolution,
  h.resolved_at                   AS hold_resolved_at,
  rs.retry_authorized_attempt_no,
  -- Eligibility, computed here rather than in the UI: the screen must not be able to disagree with the
  -- function about what is allowed, and a second copy of these rules is how that starts.
  (h.resolution = 'CONFIRMED_NOT_COMPLETED'
     AND rs.retry_authorized_attempt_no IS NULL)  AS eligible_for_retry_authorization
  FROM iam_v2.posting_outbox o
  JOIN iam_v2.pms_postings p ON p.id = o.posting_id
  LEFT JOIN LATERAL (
        SELECT * FROM iam_v2.financial_recovery_holds fh
         WHERE fh.work_kind = 'POSTING_OUTBOX' AND fh.work_id = o.id
         ORDER BY fh.held_at DESC LIMIT 1) h ON true
  LEFT JOIN iam_v2.posting_review_state rs ON rs.posting_id = o.posting_id
 WHERE o.state = 'HELD_RECOVERY'
   AND NOT EXISTS (SELECT 1 FROM iam_v2.posting_attempts a WHERE a.internal_posting_id = o.posting_id);

COMMENT ON VIEW iam_v2.v_zero_attempt_recovery_queue IS
  'Postings held by recovery with NO surviving attempts. They cannot appear in the ordinary review queue, '
  'which keys on attempts, and before this view the only safe way out of that state was unreachable from '
  'any operator surface.';

GRANT SELECT ON iam_v2.v_zero_attempt_recovery_queue TO sc_financial_operator;

INSERT INTO public.schema_migrations (version)
  VALUES ('0026_phase4_c35_failclosed_and_operator_retry') ON CONFLICT DO NOTHING;
COMMIT;
