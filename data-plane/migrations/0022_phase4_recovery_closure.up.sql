-- 0022 — PHASE 4: FINANCIAL_RECOVERY_MODE closure, and the legacy identity correction.
-- D18 / T0029. Receipt: T0039. Additive, reversible, DARK.
--
-- MEASURED, against 0011..0021:
--
-- (a) 0019 COPIES non-terminal posting work into financial_recovery_holds and blocks INSERTs into
--     posting_outbox. It does nothing to the rows already there. A posting_outbox row sitting in QUEUED when
--     recovery begins is still QUEUED afterwards, and the posting worker's claim -- an UPDATE to IN_FLIGHT --
--     was never gated. So the ledger said "held" while the underlying command remained fully sendable. That
--     is the exact failure recovery exists to prevent: the restored database re-sends a posting the folio
--     already accepted.
--
-- (b) Release checked only that every hold row had a non-null resolution. A resolution is an operator's
--     CONCLUSION; it says nothing about whether the underlying record was actually made safe. Recovery could
--     therefore be released with a QUEUED outbox row and a PENDING payment still live.
--
-- (c) A resolution had no rail-specific consequence at all. CONFIRMED_COMPLETED -- "the folio already has
--     this" -- left the original command exactly as sendable as before.
--
-- (d) 0018's backfill writes merchant_account_ref = 'legacy:<internal uuid>' into a column documented as the
--     PROVIDER'S OWN account identifier. That is a fabricated external identity: no provider ever issued it,
--     and a later reader cannot tell it apart from a real one.
--
-- This migration closes all four.
BEGIN;

-- ============================================================================
-- (1) Legacy identity provenance — stop fabricating an external identifier
-- ============================================================================
ALTER TABLE iam_v2.payment_provider_accounts
  ADD COLUMN IF NOT EXISTS provenance text NOT NULL DEFAULT 'CONFIGURED'
    CHECK (provenance IN ('CONFIGURED','BACKFILLED_UNVERIFIED'));

COMMENT ON COLUMN iam_v2.payment_provider_accounts.provenance IS
  'CONFIGURED: an operator supplied this account and its external reference is the provider''s own. '
  'BACKFILLED_UNVERIFIED: reconstructed by migration 0018 from a payment row that predates this table. Its '
  'external reference is UNKNOWN, not invented, and it can never become ACTIVE or default.';

-- The external reference becomes NULLable, because "we do not know it" is the truth for a backfilled row
-- and a placeholder string is not a better answer than a NULL.
ALTER TABLE iam_v2.payment_provider_accounts ALTER COLUMN merchant_account_ref DROP NOT NULL;

-- Replace 0018's fabricated identifiers with the honest absence. Matching on the exact shape 0018 wrote
-- means a real provider reference that happens to start with 'legacy:' -- vanishingly unlikely, but the
-- migration should not depend on that -- is left alone unless it is also the internal uuid.
UPDATE iam_v2.payment_provider_accounts
   SET merchant_account_ref = NULL,
       provenance = 'BACKFILLED_UNVERIFIED',
       display_name = coalesce(display_name, 'Reconstructed from an existing payment record (0018)')
 WHERE merchant_account_ref = 'legacy:' || id::text;

ALTER TABLE iam_v2.payment_provider_accounts
  ADD CONSTRAINT ppa_reference_matches_provenance CHECK (
    (provenance = 'CONFIGURED'
       AND merchant_account_ref IS NOT NULL
       AND btrim(merchant_account_ref) <> ''
       AND length(merchant_account_ref) <= 128)
    OR (provenance = 'BACKFILLED_UNVERIFIED' AND merchant_account_ref IS NULL));

-- An unverified account can never take money. Becoming usable requires an operator to supply the real
-- external reference and flip the provenance -- which is a deliberate act, not a default.
ALTER TABLE iam_v2.payment_provider_accounts
  ADD CONSTRAINT ppa_unverified_is_never_live CHECK (
    provenance = 'CONFIGURED' OR (status = 'DISABLED' AND NOT is_default));

-- ============================================================================
-- (2) The structural hold: existing posting work becomes NON-SENDABLE
-- ============================================================================
-- p4_hold_financial_rails is the single place that makes a site's financial work unsendable, and BOTH
-- recovery entry points call it. Operator-declared recovery previously held only payments; it now freezes
-- exactly what a detected restore freezes, because an operator who declares recovery is saying the same
-- thing a restore detector says: what this database believes may no longer be true.
CREATE OR REPLACE FUNCTION iam_v2.p4_hold_financial_rails(p_tenant uuid, p_site uuid, p_epoch bigint)
RETURNS int
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_held int := 0; v_n int;
BEGIN
  -- THE POSTING RAIL. This is the correction: the underlying outbox rows are moved to HELD_RECOVERY, so the
  -- worker's claim predicate (state = 'QUEUED') no longer matches them. Copying them into a ledger, as 0019
  -- did, left them exactly as claimable as before.
  --
  -- FOR UPDATE serializes against a worker claiming concurrently. Whichever transaction takes the row lock
  -- first wins: if the worker wins, its IN_FLIGHT row is then held here; if recovery wins, the worker's
  -- UPDATE finds HELD_RECOVERY and the gate below refuses it. There is no interleaving in which a claim
  -- escapes.
  PERFORM 1 FROM iam_v2.posting_outbox
   WHERE tenant_id = p_tenant AND site_id = p_site AND state IN ('QUEUED','IN_FLIGHT')
   FOR UPDATE;

  INSERT INTO iam_v2.financial_recovery_holds
    (tenant_id, site_id, epoch, work_kind, work_id, held_status, amount_minor, currency)
  SELECT o.tenant_id, o.site_id, p_epoch, 'POSTING_OUTBOX', o.id, o.state, NULL, NULL
    FROM iam_v2.posting_outbox o
   WHERE o.tenant_id = p_tenant AND o.site_id = p_site AND o.state IN ('QUEUED','IN_FLIGHT')
  ON CONFLICT DO NOTHING;
  GET DIAGNOSTICS v_n = ROW_COUNT; v_held := v_held + v_n;

  UPDATE iam_v2.posting_outbox SET state = 'HELD_RECOVERY'
   WHERE tenant_id = p_tenant AND site_id = p_site AND state IN ('QUEUED','IN_FLIGHT');

  -- THE PAYMENT RAIL. A payment cannot be "moved to held" -- its status machine is the financial record --
  -- so the hold is enforced by begin_payment_execution and the recovery gate refusing to start anything.
  -- What is recorded here is the ledger entry an operator reconciles.
  INSERT INTO iam_v2.financial_recovery_holds
    (tenant_id, site_id, epoch, work_kind, work_id, held_status, amount_minor, currency)
  SELECT t.tenant_id, t.site_id, p_epoch, 'PAYMENT_TRANSACTION', t.id, t.status, t.amount_minor, t.currency
    FROM iam_v2.payment_transactions t
   WHERE t.tenant_id = p_tenant AND t.site_id = p_site AND t.status IN ('CREATED','PENDING','UNKNOWN')
  ON CONFLICT DO NOTHING;
  GET DIAGNOSTICS v_n = ROW_COUNT; v_held := v_held + v_n;

  INSERT INTO iam_v2.financial_recovery_holds
    (tenant_id, site_id, epoch, work_kind, work_id, held_status, amount_minor, currency)
  SELECT se.tenant_id, se.site_id, p_epoch, 'SETTLEMENT', se.id, se.status, NULL, NULL
    FROM iam_v2.settlements se
   WHERE se.tenant_id = p_tenant AND se.site_id = p_site
     AND se.status IN ('REQUIRED','IN_PROGRESS','MANUAL_REVIEW')
  ON CONFLICT DO NOTHING;
  GET DIAGNOSTICS v_n = ROW_COUNT; v_held := v_held + v_n;

  RETURN v_held;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_hold_financial_rails(uuid,uuid,bigint) FROM PUBLIC;

-- The claim gate. A worker may not move a posting into IN_FLIGHT while the site is held, and may not move
-- it OUT of HELD_RECOVERY at all except through the reconciliation function below.
CREATE OR REPLACE FUNCTION iam_v2.p4_outbox_recovery_gate() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF NEW.state = 'IN_FLIGHT' AND OLD.state <> 'IN_FLIGHT'
     AND iam_v2.p4_financial_recovery_active(NEW.tenant_id, NEW.site_id) THEN
    RAISE EXCEPTION 'FINANCIAL_RECOVERY_MODE: this site is in financial recovery; a posting may not be '
                    'claimed or transmitted until an operator has reconciled what already happened'
      USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.state = 'HELD_RECOVERY' AND NEW.state <> 'HELD_RECOVERY'
     AND coalesce(current_setting('iam_v2.p4_recovery_reconciling', true), '') <> 'on' THEN
    RAISE EXCEPTION 'RECOVERY_HOLD_RELEASE_UNCONTROLLED: held posting work leaves HELD_RECOVERY only '
                    'through an audited reconciliation decision' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_outbox_recovery_gate() FROM PUBLIC;
CREATE TRIGGER p4_outbox_recovery_gate BEFORE UPDATE ON iam_v2.posting_outbox
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_outbox_recovery_gate();

-- ============================================================================
-- (3) Recovery entry, using the shared hold
-- ============================================================================
CREATE OR REPLACE FUNCTION iam_v2.p4_reconcile_financial_epoch(
  p_tenant uuid, p_site uuid, p_system_identity text)
RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE cur record; v_epoch bigint;
BEGIN
  IF p_system_identity IS NULL OR btrim(p_system_identity) = '' THEN
    RAISE EXCEPTION 'RECOVERY_IDENTITY_REQUIRED: restore detection needs the running system identity'
      USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site ORDER BY epoch DESC LIMIT 1;

  IF cur.epoch IS NULL THEN
    INSERT INTO iam_v2.financial_epochs (tenant_id, site_id, epoch, system_identity, reason, released_at)
    VALUES (p_tenant, p_site, 1, p_system_identity, 'INITIAL', now());
    RETURN 'INITIALIZED';
  END IF;
  IF cur.system_identity = p_system_identity THEN
    RETURN CASE WHEN cur.released_at IS NULL THEN 'RECOVERY_ACTIVE' ELSE 'UNCHANGED' END;
  END IF;
  IF cur.released_at IS NULL THEN
    UPDATE iam_v2.financial_epochs SET system_identity = p_system_identity
     WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch;
    PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, cur.epoch);
    RETURN 'RECOVERY_ACTIVE';
  END IF;

  v_epoch := cur.epoch + 1;
  INSERT INTO iam_v2.financial_epochs (tenant_id, site_id, epoch, system_identity, reason)
  VALUES (p_tenant, p_site, v_epoch, p_system_identity, 'RESTORE_DETECTED');
  PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, v_epoch);
  RETURN 'RECOVERY_ENTERED';
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_reconcile_financial_epoch(uuid,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_reconcile_financial_epoch(uuid,uuid,text) TO sc_payment_runtime;

CREATE OR REPLACE FUNCTION iam_v2.p4_declare_financial_recovery(
  p_tenant uuid, p_site uuid, p_actor uuid, p_reason text)
RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE cur record; v_epoch bigint;
BEGIN
  PERFORM iam_v2.p4_assert_financial_actor(p_tenant, p_actor);
  IF p_reason IS NULL OR length(btrim(p_reason)) < 10 THEN
    RAISE EXCEPTION 'RECOVERY_REASON_REQUIRED: declaring recovery needs a reason of at least 10 characters'
      USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site ORDER BY epoch DESC LIMIT 1;
  IF cur.epoch IS NOT NULL AND cur.released_at IS NULL THEN
    RETURN cur.epoch;
  END IF;
  v_epoch := coalesce(cur.epoch, 0) + 1;
  INSERT INTO iam_v2.financial_epochs (tenant_id, site_id, epoch, system_identity, reason)
  VALUES (p_tenant, p_site, v_epoch, coalesce(cur.system_identity, 'operator-declared'), 'OPERATOR_DECLARED');
  -- The SAME full-rail hold a detected restore performs. An operator declaring recovery is making the same
  -- statement a restore detector makes, so it must have the same consequence.
  PERFORM iam_v2.p4_hold_financial_rails(p_tenant, p_site, v_epoch);
  RETURN v_epoch;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_declare_financial_recovery(uuid,uuid,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_declare_financial_recovery(uuid,uuid,uuid,text) TO sc_financial_operator;

-- ============================================================================
-- (4) Reconciliation with rail-specific consequences
-- ============================================================================
-- A conclusion must CHANGE the underlying record, not merely be recorded next to it.
CREATE OR REPLACE FUNCTION iam_v2.p4_resolve_recovery_hold(
  p_hold uuid, p_resolution text, p_actor uuid, p_note text)
RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE h record; v_tx record;
BEGIN
  IF p_resolution NOT IN ('CONFIRMED_COMPLETED','CONFIRMED_NOT_COMPLETED','ABANDONED','ESCALATED') THEN
    RAISE EXCEPTION 'RECOVERY_RESOLUTION_INVALID: %', p_resolution USING ERRCODE = 'check_violation';
  END IF;
  IF p_note IS NULL OR length(btrim(p_note)) < 10 THEN
    RAISE EXCEPTION 'RECOVERY_NOTE_REQUIRED: a reconciliation decision records HOW it was established, in '
                    'at least 10 characters' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO h FROM iam_v2.financial_recovery_holds WHERE id = p_hold FOR UPDATE;
  IF h.id IS NULL THEN
    RAISE EXCEPTION 'RECOVERY_HOLD_UNKNOWN: %', p_hold USING ERRCODE = 'no_data_found';
  END IF;
  PERFORM iam_v2.p4_assert_financial_actor(h.tenant_id, p_actor);

  -- The reconciliation session flag. It is set ONLY here, so the outbox gate can tell an audited decision
  -- apart from an ordinary UPDATE, and it is scoped to this transaction.
  PERFORM set_config('iam_v2.p4_recovery_reconciling', 'on', true);

  IF h.work_kind = 'POSTING_OUTBOX' THEN
    -- CONFIRMED_COMPLETED: the folio already has it. The command must stop being sendable, permanently.
    -- ABANDONED: nothing further will be done about it, which is also terminal.
    IF p_resolution IN ('CONFIRMED_COMPLETED','ABANDONED') THEN
      UPDATE iam_v2.posting_outbox SET state = 'DONE'
       WHERE id = h.work_id AND state = 'HELD_RECOVERY';
    END IF;
    -- CONFIRMED_NOT_COMPLETED and ESCALATED deliberately leave the row HELD_RECOVERY. Re-queueing it here
    -- would be exactly the automatic replay recovery exists to prevent; a retry becomes possible only
    -- through record_posting_review_action, which authorizes ONE attempt and audits who authorized it.
  ELSIF h.work_kind = 'PAYMENT_TRANSACTION' THEN
    SELECT * INTO v_tx FROM iam_v2.payment_transactions WHERE id = h.work_id FOR UPDATE;
    IF v_tx.id IS NOT NULL AND v_tx.status IN ('CREATED','PENDING') THEN
      IF p_resolution = 'CONFIRMED_NOT_COMPLETED' THEN
        -- Nothing was charged. The intent is closed; a new attempt is a new purchase flow, never a replay
        -- of this one.
        IF v_tx.status = 'CREATED' THEN
          UPDATE iam_v2.payment_transactions SET status = 'PENDING' WHERE id = v_tx.id;
        END IF;
        UPDATE iam_v2.payment_transactions SET status = 'FAILED' WHERE id = v_tx.id;
      ELSE
        -- CONFIRMED_COMPLETED, ABANDONED and ESCALATED all leave the money AMBIGUOUS from the database's
        -- point of view. Recording a capture here would settle a settlement and grant access on an
        -- operator's say-so, bypassing the authenticated provider boundary entirely -- so the payment goes
        -- to UNKNOWN and the settlement to MANUAL_REVIEW, where the existing audited model decides.
        IF v_tx.status = 'CREATED' THEN
          UPDATE iam_v2.payment_transactions SET status = 'PENDING' WHERE id = v_tx.id;
        END IF;
        UPDATE iam_v2.payment_transactions SET status = 'UNKNOWN' WHERE id = v_tx.id;
        -- The ambiguity belongs to the SETTLEMENT as much as to the payment: an operator looking at the
        -- settlement must see that its outcome is unresolved, not that it is quietly still in progress.
        UPDATE iam_v2.settlements SET status = 'MANUAL_REVIEW'
         WHERE id = v_tx.settlement_id AND status = 'IN_PROGRESS';
      END IF;
    END IF;
  ELSIF h.work_kind = 'SETTLEMENT' THEN
    -- Only an IN_PROGRESS settlement is ambiguous: something was started against it and nobody knows how
    -- it ended. A REQUIRED settlement is simply still awaiting money, which a restore did not change, and
    -- section 16 has no REQUIRED -> MANUAL_REVIEW edge -- so it is left exactly where it is rather than
    -- widening the state machine to make the reconciliation look tidier.
    UPDATE iam_v2.settlements SET status = 'MANUAL_REVIEW'
     WHERE id = h.work_id AND status = 'IN_PROGRESS';
  END IF;

  UPDATE iam_v2.financial_recovery_holds
     SET resolution = p_resolution, resolved_at = now(), resolved_by = p_actor, resolution_note = p_note
   WHERE id = p_hold;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_resolve_recovery_hold(uuid,text,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_resolve_recovery_hold(uuid,text,uuid,text) TO sc_financial_operator;

-- ============================================================================
-- (5) Release, verified against the UNDERLYING records
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
  SELECT count(*) INTO v_sendable FROM iam_v2.posting_outbox
   WHERE tenant_id = p_tenant AND site_id = p_site AND state IN ('QUEUED','IN_FLIGHT');
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

INSERT INTO public.schema_migrations (version) VALUES ('0022_phase4_recovery_closure')
  ON CONFLICT DO NOTHING;
COMMIT;
