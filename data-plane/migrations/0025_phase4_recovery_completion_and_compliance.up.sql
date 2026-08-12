-- 0025 — PHASE 4: the zero-attempt recovery path, the marker-BEHIND case, cross-tenant merchant identity
-- (C27), and the compliance archive before a cross-customer purge (C35).
-- D18 / T0029. Receipt: T0040. Additive, reversible, DARK.
--
-- FOUR MEASURED FINDINGS.
--
-- (a) THE ZERO-ATTEMPT RESTORE. A posting can come back from a restore as QUEUED or HELD_RECOVERY with NO
--     surviving attempts: the attempts were made after the backup was taken, so the restored database has
--     the posting but no record of ever having sent it. 0022 holds it correctly and refuses to re-queue it.
--     But record_posting_review_action raises REVIEW_NOT_APPLICABLE when no attempt exists -- "a terminal
--     decision still needs something to decide about" -- so the ONLY audited route out is closed. Measured:
--     such a posting is stuck in HELD_RECOVERY permanently, and the guest's charge never reaches the folio.
--
-- (b) THE MARKER BEHIND THE DATABASE. 0023 handles a marker that is AHEAD (a restore) and one that is
--     MISSING. A marker BEHIND the database fell through to "no signal". That case is not hypothetical: the
--     documented runbook backs up /etc/stayconnect as a tar and restores it, so restoring an OLD /etc
--     rolls the marker backwards while the database stays current.
--
-- (c) C27, cross-tenant merchant reuse. 0018's uniqueness is per (tenant, site), so the SAME external
--     merchant account can be configured under two different customers. Measured: nothing prevents it.
--
-- (d) C35, compliance archive before cross-customer purge. iam_v2.compliance_archives has existed since mg7
--     and NOTHING writes it. scd's reconcileTenantOwnership DELETEs the previous tenant's rows outright.
BEGIN;

-- ============================================================================
-- (a) The zero-attempt retry authorization
-- ============================================================================
-- This is the ONLY way a posting with no attempts leaves HELD_RECOVERY, and every guard exists because of
-- something that would otherwise go wrong:
--
--   * recovery must have HELD it            -- otherwise this becomes a general re-queue button
--   * its hold must be CONFIRMED_NOT_COMPLETED -- an operator has established the folio was NOT charged
--   * there must be ZERO attempts           -- with attempts, the existing review path already applies and
--                                              two routes to one outcome is how they diverge
--   * exactly ONE authorization             -- consumed by the attempt it authorizes, like the existing one
--
-- It authorizes attempt number 1. It does NOT manufacture a historical attempt or a P#: nothing is written
-- to posting_attempts, no p_number is allocated, and the posting's history stays honestly empty until a
-- real transmission happens. Inventing an attempt would put a fiction into the audit trail of money.
CREATE OR REPLACE FUNCTION iam_v2.p4_authorize_zero_attempt_retry(
  p_posting uuid, p_actor uuid, p_reason text, p_evidence jsonb)
RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE po record; ob record; h record; v_action uuid; v_attempts int; v_bad text;
BEGIN
  SELECT * INTO po FROM iam_v2.pms_postings WHERE id = p_posting;
  IF po.id IS NULL THEN
    RAISE EXCEPTION 'REVIEW_POSTING_UNKNOWN: %', p_posting USING ERRCODE = 'no_data_found';
  END IF;
  PERFORM iam_v2.p4_assert_financial_actor(po.tenant_id, p_actor);
  IF p_reason IS NULL OR length(btrim(p_reason)) < 10 THEN
    RAISE EXCEPTION 'REVIEW_REASON_REQUIRED: a retry authorization records WHY, in at least 10 characters'
      USING ERRCODE = 'check_violation';
  END IF;
  -- Evidence screening. p4_callback_evidence_safe is deliberately NOT reused here: it enforces the
  -- PROVIDER-CALLBACK key allowlist (provider_status, provider_reason_code, ...), which is the wrong
  -- contract for a review decision and would reject the operator evidence the review surface actually
  -- collects. What matters in both places is the same, though -- the ledger is immutable, so nothing that
  -- looks like a secret may enter it -- so the screening is done here against the same shapes.
  IF p_evidence IS NOT NULL THEN
    IF jsonb_typeof(p_evidence) <> 'object' THEN
      RAISE EXCEPTION 'REVIEW_EVIDENCE_UNSAFE: evidence must be a bounded object'
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT string_agg(k, ',') INTO v_bad FROM jsonb_object_keys(p_evidence) k
     WHERE k IN ('raw_body','raw','payload','body','token','secret','password','api_key','card',
                 'pan','cvv','authorization');
    IF v_bad IS NOT NULL THEN
      RAISE EXCEPTION 'REVIEW_EVIDENCE_UNSAFE: key(s) % may not enter an immutable audit record; record a '
                      'REFERENCE to the artefact, never its contents', v_bad
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT string_agg(k, ',') INTO v_bad FROM jsonb_each_text(p_evidence) e(k, v)
     WHERE length(v) > 256 OR v ~ '[[:cntrl:]]'
        OR v ~ '(?i)(sk_live|pk_live|-----BEGIN|bearer [A-Za-z0-9._-]{20,}|[0-9]{13,19})';
    IF v_bad IS NOT NULL THEN
      RAISE EXCEPTION 'REVIEW_EVIDENCE_UNSAFE: value(s) under % look like a secret, a card number or an '
                      'unbounded blob', v_bad USING ERRCODE = 'check_violation';
    END IF;
  END IF;

  SELECT count(*) INTO v_attempts FROM iam_v2.posting_attempts WHERE internal_posting_id = p_posting;
  IF v_attempts > 0 THEN
    RAISE EXCEPTION 'RETRY_HAS_ATTEMPTS: this posting has % attempt(s); use the ordinary audited review '
                    'path, which is the one that knows how to read them', v_attempts
      USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO ob FROM iam_v2.posting_outbox WHERE posting_id = p_posting FOR UPDATE;
  IF ob.id IS NULL OR ob.state <> 'HELD_RECOVERY' THEN
    RAISE EXCEPTION 'RETRY_NOT_HELD: this posting is not held by recovery (%); there is nothing to release',
      coalesce(ob.state,'no outbox row') USING ERRCODE = 'check_violation';
  END IF;

  SELECT * INTO h FROM iam_v2.financial_recovery_holds
   WHERE work_kind = 'POSTING_OUTBOX' AND work_id = ob.id
   ORDER BY held_at DESC LIMIT 1;
  IF h.id IS NULL OR h.resolution IS DISTINCT FROM 'CONFIRMED_NOT_COMPLETED' THEN
    RAISE EXCEPTION 'RETRY_NOT_ESTABLISHED: this posting has no reconciliation establishing that it was '
                    'NOT completed (%). A retry is only safe once someone has checked the folio',
      coalesce(h.resolution, 'unreconciled') USING ERRCODE = 'check_violation';
  END IF;

  -- The audited record. It goes into the SAME append-only review ledger every other financial decision
  -- goes into, so a reader auditing this posting sees one history rather than two.
  --
  -- 0011 guards that ledger with a writer token so it has exactly one writer. Setting the token here makes
  -- this function a SECOND sanctioned writer, which is a deliberate and narrow addition: it is the only way
  -- to record the zero-attempt case, it is a definer function no runtime role can call except the operator
  -- role, and every other path into the table remains closed. The alternative -- writing the decision
  -- somewhere else -- would give this posting two histories.
  PERFORM set_config('iam_v2.p4_review_writer', txid_current()::text, true);
  INSERT INTO iam_v2.posting_review_actions
    (tenant_id, site_id, posting_id, action, actor, reason, evidence)
  VALUES (po.tenant_id, po.site_id, p_posting, 'CONFIRM_NOT_POSTED_RETRY', p_actor, p_reason,
          coalesce(p_evidence, '{}'::jsonb))
  RETURNING id INTO v_action;

  INSERT INTO iam_v2.posting_review_state
    (posting_id, tenant_id, site_id, review_version, retry_authorized_attempt_no, updated_at)
  VALUES (p_posting, po.tenant_id, po.site_id, 1, 1, now())
  ON CONFLICT (posting_id) DO UPDATE
     SET review_version = iam_v2.posting_review_state.review_version + 1,
         retry_authorized_attempt_no = 1,
         updated_at = now();

  -- Only NOW does it become sendable, and only because an operator established it never was.
  UPDATE iam_v2.posting_outbox SET state = 'QUEUED' WHERE id = ob.id;
  RETURN v_action;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_authorize_zero_attempt_retry(uuid,uuid,text,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  iam_v2.p4_authorize_zero_attempt_retry(uuid,uuid,text,jsonb) TO sc_financial_operator;

-- ============================================================================
-- (b) The marker BEHIND the database
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.p4_reconcile_financial_epoch_v2(
  p_tenant uuid, p_site uuid, p_system_identity text, p_marker_generation bigint,
  p_marker_present boolean)
RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE cur record; v_epoch bigint; v_reason text := NULL; v_detect text; v_gen bigint;
BEGIN
  IF p_system_identity IS NULL OR btrim(p_system_identity) = '' THEN
    RAISE EXCEPTION 'RECOVERY_IDENTITY_REQUIRED' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site ORDER BY epoch DESC LIMIT 1;
  v_gen := CASE WHEN p_marker_present THEN p_marker_generation ELSE 0 END;

  IF cur.epoch IS NULL THEN
    INSERT INTO iam_v2.financial_epochs
      (tenant_id, site_id, epoch, system_identity, reason, released_at, restore_generation)
    VALUES (p_tenant, p_site, 1, p_system_identity, 'INITIAL', now(), v_gen);
    RETURN 'INITIALIZED';
  END IF;

  IF p_marker_present AND p_marker_generation > cur.restore_generation THEN
    -- The database is OLDER than the appliance knows it should be: a restore happened.
    v_reason := 'MARKER_AHEAD'; v_detect := 'MANAGEMENT_MARKER';
  ELSIF p_marker_present AND p_marker_generation < cur.restore_generation THEN
    -- The MARKER is older than the database. The management partition was rolled back -- which the
    -- documented runbook can do all by itself, because it backs up /etc/stayconnect as a tar and restoring
    -- an old one carries an old marker with it.
    --
    -- Held, not ignored. The two records disagree about how many times this site has been restored, and
    -- until someone establishes which is right, nobody can say whether the financial data is current. A
    -- disagreement is exactly as much reason to stop as a detected rollback.
    v_reason := 'MARKER_BEHIND'; v_detect := 'MANAGEMENT_MARKER';
  ELSIF cur.system_identity <> p_system_identity THEN
    v_reason := 'IDENTITY_CHANGED'; v_detect := 'SYSTEM_IDENTITY';
  ELSIF cur.restore_generation > 0 AND NOT p_marker_present THEN
    v_reason := 'MARKER_MISSING'; v_detect := 'MANAGEMENT_MARKER';
  END IF;

  IF v_reason IS NULL THEN
    RETURN CASE WHEN cur.released_at IS NULL THEN 'RECOVERY_ACTIVE' ELSE 'UNCHANGED' END;
  END IF;

  IF cur.released_at IS NULL THEN
    UPDATE iam_v2.financial_epochs
       SET system_identity = p_system_identity,
           restore_generation = greatest(cur.restore_generation, v_gen)
     WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch;
    PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, cur.epoch);
    RETURN 'RECOVERY_ACTIVE';
  END IF;

  v_epoch := cur.epoch + 1;
  INSERT INTO iam_v2.financial_epochs
    (tenant_id, site_id, epoch, system_identity, reason, restore_generation)
  VALUES (p_tenant, p_site, v_epoch, p_system_identity, 'RESTORE_DETECTED',
          greatest(cur.restore_generation, v_gen));
  INSERT INTO iam_v2.financial_restore_events
    (tenant_id, site_id, restore_generation, manifest_sha256, restore_kind, detected_by, restored_by)
  VALUES (p_tenant, p_site, greatest(cur.restore_generation, v_gen), repeat('0', 64),
          'UNSUPPORTED_RAW_SNAPSHOT', v_detect, v_reason)
  ON CONFLICT DO NOTHING;
  PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, v_epoch);
  RETURN 'RECOVERY_ENTERED';
END $fn$;
REVOKE EXECUTE ON FUNCTION
  iam_v2.p4_reconcile_financial_epoch_v2(uuid,uuid,text,bigint,boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION
  iam_v2.p4_reconcile_financial_epoch_v2(uuid,uuid,text,bigint,boolean) TO sc_payment_runtime;

-- ============================================================================
-- (c) C27 — no cross-tenant merchant reuse
-- ============================================================================
-- An external merchant account belongs to ONE customer. Two tenants configuring the same provider account
-- would mean one customer's guests paying into another customer's account, and the money would be
-- indistinguishable afterwards. 0018's uniqueness was per (tenant, site), which permits exactly that.
--
-- Scoped to CONFIGURED rows: a BACKFILLED_UNVERIFIED row has a NULL reference by construction, so it names
-- no external account and cannot collide with one.
CREATE UNIQUE INDEX IF NOT EXISTS ppa_merchant_ref_globally_unique
  ON iam_v2.payment_provider_accounts (provider, merchant_account_ref)
  WHERE provenance = 'CONFIGURED' AND merchant_account_ref IS NOT NULL;

COMMENT ON INDEX iam_v2.ppa_merchant_ref_globally_unique IS
  'C27: one external merchant account belongs to one customer. Global rather than tenant-scoped on '
  'purpose -- the hazard is precisely two TENANTS naming the same account.';

-- ============================================================================
-- (d) C35 — the compliance archive, and the purge that must wait for it
-- ============================================================================
-- WHAT IS DELIVERED. Before a cross-customer purge deletes a departing tenant's data, the appliance must
-- produce a compliance archive and record its digest. p4_record_compliance_archive writes that record, and
-- p4_assert_compliance_archived REFUSES to let the purge proceed without one.
--
-- WHAT IS NOT, and why. compliance_archives.receipt_verified describes an EXTERNAL archival authority
-- countersigning that it holds the artefact. No such service exists in this product, no key for one has
-- been issued, and inventing a receipt would be fabricating managed state -- so receipt_verified stays
-- false and the column is documented as unfulfilled rather than quietly defaulted to true. The archive
-- itself, its digest and the gate ARE real and enforced.
ALTER TABLE iam_v2.compliance_archives
  ADD COLUMN IF NOT EXISTS purpose text NOT NULL DEFAULT 'CROSS_CUSTOMER_PURGE'
    CHECK (purpose IN ('CROSS_CUSTOMER_PURGE','RETENTION_EXPIRY','OPERATOR_EXPORT')),
  ADD COLUMN IF NOT EXISTS artifact_path text,
  ADD COLUMN IF NOT EXISTS row_counts jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN IF NOT EXISTS receipt_blocked_reason text;

COMMENT ON COLUMN iam_v2.compliance_archives.receipt_verified IS
  'FALSE and expected to stay false in Phase 4: it records that an EXTERNAL archival authority has '
  'countersigned custody of the artefact, and no such service or key exists in this product. The archive '
  'and its digest are real; the counter-signature is the missing external capability, recorded in '
  'receipt_blocked_reason rather than defaulted to true.';

CREATE OR REPLACE FUNCTION iam_v2.p4_record_compliance_archive(
  p_tenant uuid, p_site uuid, p_manifest_sha text, p_artifact_path text, p_row_counts jsonb)
RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_id uuid;
BEGIN
  IF p_manifest_sha IS NULL OR p_manifest_sha !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'ARCHIVE_DIGEST_REQUIRED: a compliance archive is identified by the digest of the '
                    'artefact actually written' USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.compliance_archives
    (tenant_id, site_id, manifest_sha256, receipt_verified, purpose, artifact_path, row_counts,
     receipt_blocked_reason)
  VALUES (p_tenant, p_site, p_manifest_sha, false, 'CROSS_CUSTOMER_PURGE', p_artifact_path,
          coalesce(p_row_counts, '{}'::jsonb),
          'No external archival receipt authority exists in this product; the artefact and its digest are '
          'recorded, the counter-signature is not available')
  RETURNING id INTO v_id;
  RETURN v_id;
END $fn$;
REVOKE EXECUTE ON FUNCTION
  iam_v2.p4_record_compliance_archive(uuid,uuid,text,text,jsonb) FROM PUBLIC;

-- The gate. The purge calls this and dies if it raises, which is the point: a purge that cannot prove an
-- archive exists must not run at all.
CREATE OR REPLACE FUNCTION iam_v2.p4_assert_compliance_archived(p_tenant uuid)
RETURNS void
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM iam_v2.compliance_archives
                  WHERE tenant_id = p_tenant AND purpose = 'CROSS_CUSTOMER_PURGE') THEN
    RAISE EXCEPTION 'COMPLIANCE_ARCHIVE_MISSING: tenant % has no compliance archive; its data may not be '
                    'purged until one has been produced and its digest recorded', p_tenant
      USING ERRCODE = 'check_violation';
  END IF;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_assert_compliance_archived(uuid) FROM PUBLIC;

-- ============================================================================
-- (e) Release, reconciled with the audited retry
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.p4_release_financial_recovery(
  p_tenant uuid, p_site uuid, p_actor uuid, p_note text)
RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE cur record; v_open int; v_sendable int; v_live int; v_inprog int;
BEGIN
  PERFORM iam_v2.p4_assert_financial_actor(p_tenant, p_actor);
  IF p_note IS NULL OR length(btrim(p_note)) < 10 THEN
    RAISE EXCEPTION 'RECOVERY_NOTE_REQUIRED: releasing recovery records why it is safe to resume'
      USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site AND released_at IS NULL;
  IF cur.epoch IS NULL THEN
    RAISE EXCEPTION 'RECOVERY_NOT_ACTIVE: this site is not in financial recovery'
      USING ERRCODE = 'no_data_found';
  END IF;

  SELECT count(*) INTO v_open FROM iam_v2.financial_recovery_holds
   WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch AND resolution IS NULL;
  IF v_open > 0 THEN
    RAISE EXCEPTION 'RECOVERY_HOLDS_UNRESOLVED: % held item(s) have not been reconciled', v_open
      USING ERRCODE = 'check_violation';
  END IF;

  -- The check that a resolution count cannot give. A conclusion is a claim ABOUT a record; this asks the
  -- records themselves whether anything is still sendable or still in flight.
  -- Sendable work is only unsafe when nobody has accounted for it. A posting made sendable by the audited
  -- zero-attempt authorization (0025) is the opposite of unaccounted for: an operator established that the
  -- folio was never charged and authorized exactly one attempt, which is the whole point of that path.
  -- Refusing release for it would mean the one safe route out of a zero-attempt restore could never be
  -- completed -- two correct rules cancelling each other out.
  SELECT count(*) INTO v_sendable FROM iam_v2.posting_outbox o
   WHERE o.tenant_id = p_tenant AND o.site_id = p_site AND o.state IN ('QUEUED','IN_FLIGHT')
     AND NOT EXISTS (SELECT 1 FROM iam_v2.posting_review_state rs
                      WHERE rs.posting_id = o.posting_id
                        AND rs.retry_authorized_attempt_no IS NOT NULL);
  IF v_sendable > 0 THEN
    RAISE EXCEPTION 'RECOVERY_STATE_UNSAFE: % posting(s) are still sendable. Every hold may be resolved '
                    'and the underlying command still be waiting to go out', v_sendable
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT count(*) INTO v_live FROM iam_v2.payment_transactions
   WHERE tenant_id = p_tenant AND site_id = p_site AND status IN ('CREATED','PENDING');
  IF v_live > 0 THEN
    RAISE EXCEPTION 'RECOVERY_STATE_UNSAFE: % payment(s) are still live', v_live
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT count(*) INTO v_inprog FROM iam_v2.settlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND status = 'IN_PROGRESS';
  IF v_inprog > 0 THEN
    RAISE EXCEPTION 'RECOVERY_STATE_UNSAFE: % settlement(s) are still IN_PROGRESS', v_inprog
      USING ERRCODE = 'check_violation';
  END IF;

  UPDATE iam_v2.financial_epochs
     SET released_at = now(), released_by = p_actor, release_note = p_note
   WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch;
  RETURN cur.epoch;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_release_financial_recovery(uuid,uuid,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_release_financial_recovery(uuid,uuid,uuid,text) TO sc_financial_operator;

INSERT INTO public.schema_migrations (version) VALUES ('0025_phase4_recovery_completion_and_compliance')
  ON CONFLICT DO NOTHING;
COMMIT;
