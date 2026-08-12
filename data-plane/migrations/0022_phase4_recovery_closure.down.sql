-- Reverse 0022. The 0019 bodies are restored verbatim, the gate trigger is dropped, and the provenance
-- column goes with its constraints. The 'legacy:<uuid>' strings are NOT recreated: re-fabricating an
-- external identity would be the defect, not the reversal of it, so those rows keep a NULL reference and
-- their DISABLED/non-default status, which every prior migration already permits.
BEGIN;
DROP TRIGGER IF EXISTS p4_outbox_recovery_gate ON iam_v2.posting_outbox;
DROP FUNCTION IF EXISTS iam_v2.p4_outbox_recovery_gate();

CREATE OR REPLACE FUNCTION iam_v2.p4_reconcile_financial_epoch(
  p_tenant uuid, p_site uuid, p_system_identity text)
RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE cur record; v_epoch bigint; v_held int := 0;
BEGIN
  IF p_system_identity IS NULL OR btrim(p_system_identity) = '' THEN
    RAISE EXCEPTION 'RECOVERY_IDENTITY_REQUIRED: restore detection needs the running system identity'
      USING ERRCODE = 'check_violation';
  END IF;
  -- Serialize per site: two workers starting together must not both open an epoch.
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

  -- The identity moved. This data is not where it was written.
  IF cur.released_at IS NULL THEN
    -- Already in recovery and restored AGAIN. Record the new identity against the open epoch rather than
    -- opening a second one: there is still exactly one unresolved financial history to reconcile.
    UPDATE iam_v2.financial_epochs SET system_identity = p_system_identity
     WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch;
    RETURN 'RECOVERY_ACTIVE';
  END IF;

  v_epoch := cur.epoch + 1;
  INSERT INTO iam_v2.financial_epochs (tenant_id, site_id, epoch, system_identity, reason)
  VALUES (p_tenant, p_site, v_epoch, p_system_identity, 'RESTORE_DETECTED');

  -- Hold every piece of non-terminal financial work. This is a snapshot, taken once, in the same
  -- transaction that opens the epoch -- so there is no window in which recovery is active but the work has
  -- not been captured.
  INSERT INTO iam_v2.financial_recovery_holds
    (tenant_id, site_id, epoch, work_kind, work_id, held_status, amount_minor, currency)
  SELECT o.tenant_id, o.site_id, v_epoch, 'POSTING_OUTBOX', o.id, o.state, NULL, NULL
    FROM iam_v2.posting_outbox o
   WHERE o.tenant_id = p_tenant AND o.site_id = p_site
     AND o.state IN ('QUEUED','IN_FLIGHT','HELD_RECOVERY')
  ON CONFLICT DO NOTHING;
  GET DIAGNOSTICS v_held = ROW_COUNT;

  INSERT INTO iam_v2.financial_recovery_holds
    (tenant_id, site_id, epoch, work_kind, work_id, held_status, amount_minor, currency)
  SELECT t.tenant_id, t.site_id, v_epoch, 'PAYMENT_TRANSACTION', t.id, t.status, t.amount_minor, t.currency
    FROM iam_v2.payment_transactions t
   WHERE t.tenant_id = p_tenant AND t.site_id = p_site
     AND t.status IN ('CREATED','PENDING','UNKNOWN')
  ON CONFLICT DO NOTHING;

  INSERT INTO iam_v2.financial_recovery_holds
    (tenant_id, site_id, epoch, work_kind, work_id, held_status, amount_minor, currency)
  SELECT se.tenant_id, se.site_id, v_epoch, 'SETTLEMENT', se.id, se.status, NULL, NULL
    FROM iam_v2.settlements se
   WHERE se.tenant_id = p_tenant AND se.site_id = p_site
     AND se.status IN ('REQUIRED','IN_PROGRESS','MANUAL_REVIEW')
  ON CONFLICT DO NOTHING;

  RETURN 'RECOVERY_ENTERED';
END $fn$;
CREATE OR REPLACE FUNCTION iam_v2.p4_declare_financial_recovery(
  p_tenant uuid, p_site uuid, p_actor uuid, p_reason text)
RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE cur record; v_epoch bigint;
BEGIN
  IF p_actor IS NULL THEN
    RAISE EXCEPTION 'RECOVERY_ACTOR_REQUIRED' USING ERRCODE = 'check_violation';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 10 THEN
    RAISE EXCEPTION 'RECOVERY_REASON_REQUIRED: declaring recovery needs a reason of at least 10 characters'
      USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('p4.epoch:' || p_tenant::text || ':' || p_site::text));
  SELECT * INTO cur FROM iam_v2.financial_epochs
   WHERE tenant_id = p_tenant AND site_id = p_site ORDER BY epoch DESC LIMIT 1;
  IF cur.epoch IS NOT NULL AND cur.released_at IS NULL THEN
    RETURN cur.epoch;  -- already held; declaring again is a no-op, not an error
  END IF;
  v_epoch := coalesce(cur.epoch, 0) + 1;
  INSERT INTO iam_v2.financial_epochs (tenant_id, site_id, epoch, system_identity, reason)
  VALUES (p_tenant, p_site, v_epoch, coalesce(cur.system_identity, 'operator-declared'), 'OPERATOR_DECLARED');
  INSERT INTO iam_v2.financial_recovery_holds
    (tenant_id, site_id, epoch, work_kind, work_id, held_status, amount_minor, currency)
  SELECT t.tenant_id, t.site_id, v_epoch, 'PAYMENT_TRANSACTION', t.id, t.status, t.amount_minor, t.currency
    FROM iam_v2.payment_transactions t
   WHERE t.tenant_id = p_tenant AND t.site_id = p_site AND t.status IN ('CREATED','PENDING','UNKNOWN')
  ON CONFLICT DO NOTHING;
  RETURN v_epoch;
END $fn$;
CREATE OR REPLACE FUNCTION iam_v2.p4_resolve_recovery_hold(
  p_hold uuid, p_resolution text, p_actor uuid, p_note text)
RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE h record;
BEGIN
  IF p_actor IS NULL THEN
    RAISE EXCEPTION 'RECOVERY_ACTOR_REQUIRED: a reconciliation decision has an author'
      USING ERRCODE = 'check_violation';
  END IF;
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
  UPDATE iam_v2.financial_recovery_holds
     SET resolution = p_resolution, resolved_at = now(), resolved_by = p_actor, resolution_note = p_note
   WHERE id = p_hold;
END $fn$;
CREATE OR REPLACE FUNCTION iam_v2.p4_release_financial_recovery(
  p_tenant uuid, p_site uuid, p_actor uuid, p_note text)
RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE cur record; v_open int;
BEGIN
  IF p_actor IS NULL THEN
    RAISE EXCEPTION 'RECOVERY_ACTOR_REQUIRED' USING ERRCODE = 'check_violation';
  END IF;
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
    RAISE EXCEPTION 'RECOVERY_HOLDS_UNRESOLVED: % held item(s) have not been reconciled. Recovery is not '
                    'released with financial work whose outcome nobody has established', v_open
      USING ERRCODE = 'check_violation';
  END IF;
  UPDATE iam_v2.financial_epochs
     SET released_at = now(), released_by = p_actor, release_note = p_note
   WHERE tenant_id = p_tenant AND site_id = p_site AND epoch = cur.epoch;
  RETURN cur.epoch;
END $fn$;

REVOKE EXECUTE ON FUNCTION iam_v2.p4_reconcile_financial_epoch(uuid,uuid,text) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_declare_financial_recovery(uuid,uuid,uuid,text) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_resolve_recovery_hold(uuid,text,uuid,text) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_release_financial_recovery(uuid,uuid,uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_reconcile_financial_epoch(uuid,uuid,text) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_declare_financial_recovery(uuid,uuid,uuid,text) TO sc_financial_operator;
GRANT EXECUTE ON FUNCTION iam_v2.p4_resolve_recovery_hold(uuid,text,uuid,text) TO sc_financial_operator;
GRANT EXECUTE ON FUNCTION iam_v2.p4_release_financial_recovery(uuid,uuid,uuid,text) TO sc_financial_operator;

DROP FUNCTION IF EXISTS iam_v2.p4_hold_financial_rails(uuid,uuid,bigint);
ALTER TABLE iam_v2.payment_provider_accounts DROP CONSTRAINT IF EXISTS ppa_unverified_is_never_live;
ALTER TABLE iam_v2.payment_provider_accounts DROP CONSTRAINT IF EXISTS ppa_reference_matches_provenance;
ALTER TABLE iam_v2.payment_provider_accounts DROP COLUMN IF EXISTS provenance;
DELETE FROM public.schema_migrations WHERE version = '0022_phase4_recovery_closure';
COMMIT;
