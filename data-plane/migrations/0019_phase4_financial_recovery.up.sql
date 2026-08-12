-- 0019 — PHASE 4: FINANCIAL_RECOVERY_MODE. Authorization: D18 / T0029 (unchanged). Receipt: T0038.
-- Additive, reversible, DARK.
--
-- THE PROBLEM. A database restore moves financial state BACKWARDS in time. Everything the restored copy
-- believes about money is true as of the backup, and everything that happened afterwards is invisible to
-- it. A PMS posting that was in flight may or may not have reached the folio. A provider charge that was
-- PENDING may since have captured. A settlement that reads REQUIRED may already have been paid.
--
-- The dangerous instinct is to carry on. The outbox looks like it has work to do, the retry logic is
-- correct, the worker is healthy -- and every one of those in-flight items gets sent AGAIN, against a folio
-- or a provider that already accepted it once. A restore is precisely the situation in which a
-- correctly-implemented retry becomes a double charge.
--
-- SO: after a restore, financial execution STOPS. Not slows, not retries more carefully -- stops. Every
-- piece of non-terminal financial work is held, nothing is replayed automatically, and an operator decides
-- item by item what actually happened. That decision is a Manual Review decision with all of Phase 4's
-- existing evidence and audit requirements behind it; recovery invents no shortcut.
--
-- WHAT IS DELIBERATELY NOT AFFECTED. Guest access. An entitlement that was granted before the backup is
-- still a promise this hotel made to a guest sitting in their room, and a restore is our problem, not
-- theirs. Recovery mode holds MONEY MOVEMENT, and nothing in this migration touches entitlements, sessions
-- or authentication.
BEGIN;

-- ============================================================================
-- (1) The financial epoch
-- ============================================================================
-- An epoch is "which run of this database's financial history are we in". It increments on every detected
-- restore, and it is what makes restore detection possible at all: work created in an earlier epoch than
-- the current one is, by definition, work whose fate this database cannot vouch for.
CREATE TABLE iam_v2.financial_epochs (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  epoch bigint NOT NULL,
  -- A value generated ONCE per database lifetime. If the stored value stops matching what the running
  -- database reports, this data has been moved -- restored, cloned, or promoted from a replica.
  system_identity text NOT NULL,
  entered_at timestamptz NOT NULL DEFAULT now(),
  reason text NOT NULL CHECK (reason IN ('INITIAL','RESTORE_DETECTED','OPERATOR_DECLARED')),
  -- Recovery is EXITED by an operator, never by a timer and never by the system deciding it looks fine.
  released_at timestamptz,
  released_by uuid,
  release_note text CHECK (release_note IS NULL OR length(release_note) <= 2000),
  PRIMARY KEY (tenant_id, site_id, epoch)
);
CREATE UNIQUE INDEX fin_epoch_one_open_per_site
  ON iam_v2.financial_epochs (tenant_id, site_id) WHERE released_at IS NULL;

COMMENT ON TABLE iam_v2.financial_epochs IS
  'One row per run of a site''s financial history. An open row whose reason is RESTORE_DETECTED or '
  'OPERATOR_DECLARED means the site is in FINANCIAL_RECOVERY_MODE: money movement is held pending operator '
  'reconciliation. Guest access is unaffected.';

-- Held work. One row per piece of non-terminal financial work that was in flight when the epoch changed.
-- It is a HOLD, not a queue: nothing drains it automatically and there is no worker that reads it.
CREATE TABLE iam_v2.financial_recovery_holds (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  epoch bigint NOT NULL,
  work_kind text NOT NULL CHECK (work_kind IN ('POSTING_OUTBOX','PAYMENT_TRANSACTION','SETTLEMENT')),
  work_id uuid NOT NULL,
  -- What the work looked like at the moment it was held. Bounded and non-sensitive by construction: a
  -- status and an amount, never a payload, a folio, a guest name or a provider body.
  held_status text NOT NULL CHECK (length(held_status) <= 64),
  amount_minor bigint, currency char(3),
  held_at timestamptz NOT NULL DEFAULT now(),
  -- The operator's conclusion about what ACTUALLY happened, out in the world.
  resolution text CHECK (resolution IN ('CONFIRMED_COMPLETED','CONFIRMED_NOT_COMPLETED','ABANDONED','ESCALATED')),
  resolved_at timestamptz, resolved_by uuid,
  resolution_note text CHECK (resolution_note IS NULL OR length(resolution_note) <= 2000),
  UNIQUE (tenant_id, site_id, epoch, work_kind, work_id)
);
CREATE INDEX fin_holds_open ON iam_v2.financial_recovery_holds (tenant_id, site_id, epoch)
  WHERE resolution IS NULL;

-- Holds are append-only in the ways that matter: a hold is never deleted, and a resolution is written once.
CREATE OR REPLACE FUNCTION iam_v2.p4_recovery_hold_immutable() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'RECOVERY_HOLD_IMMUTABLE: a recovery hold is never deleted; it is resolved'
      USING ERRCODE = 'feature_not_supported';
  END IF;
  IF OLD.resolution IS NOT NULL THEN
    RAISE EXCEPTION 'RECOVERY_HOLD_ALREADY_RESOLVED: this hold was resolved as % at %; a conclusion about '
                    'money is not revised in place', OLD.resolution, OLD.resolved_at
      USING ERRCODE = 'check_violation';
  END IF;
  IF (NEW.work_kind, NEW.work_id, NEW.epoch, NEW.held_status) IS DISTINCT FROM
     (OLD.work_kind, OLD.work_id, OLD.epoch, OLD.held_status) THEN
    RAISE EXCEPTION 'RECOVERY_HOLD_IMMUTABLE: what was held cannot be rewritten'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER ao_recovery_holds BEFORE UPDATE OR DELETE ON iam_v2.financial_recovery_holds
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_recovery_hold_immutable();

-- ============================================================================
-- (2) Detection
-- ============================================================================
-- p4_financial_recovery_active is the single predicate every financial write consults. It is a function
-- rather than a flag file or an environment variable for one reason: a restored database brings its own
-- answer with it. A flag set in configuration is a flag the restore did not restore.
CREATE OR REPLACE FUNCTION iam_v2.p4_financial_recovery_active(p_tenant uuid, p_site uuid)
RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT EXISTS (SELECT 1 FROM iam_v2.financial_epochs
                  WHERE tenant_id = p_tenant AND site_id = p_site AND released_at IS NULL
                    AND reason IN ('RESTORE_DETECTED','OPERATOR_DECLARED'));
$fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_financial_recovery_active(uuid,uuid) FROM PUBLIC;

-- p4_reconcile_financial_epoch is called at every startup, and it is IDEMPOTENT: a normal restart finds the
-- stored identity matching and does nothing at all. Only a genuine change -- the marker the running
-- database reports no longer being the one recorded -- opens a new epoch.
--
-- The identity comes from the database's own control data. It survives a dump/restore as a NEW value,
-- because the restored cluster generated its own, which is exactly the signal wanted here.
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
REVOKE EXECUTE ON FUNCTION iam_v2.p4_reconcile_financial_epoch(uuid,uuid,text) FROM PUBLIC;

-- An operator may also declare recovery without a restore -- after any event that makes them doubt what the
-- financial record says. Holding money movement is always available; releasing it is what is controlled.
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
REVOKE EXECUTE ON FUNCTION iam_v2.p4_declare_financial_recovery(uuid,uuid,uuid,text) FROM PUBLIC;

-- ============================================================================
-- (3) Holding money movement
-- ============================================================================
-- The gate every financial write passes. It is a trigger rather than an application check because the whole
-- premise of recovery is that the application's beliefs may be wrong.
CREATE OR REPLACE FUNCTION iam_v2.p4_recovery_gate() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF iam_v2.p4_financial_recovery_active(NEW.tenant_id, NEW.site_id) THEN
    RAISE EXCEPTION 'FINANCIAL_RECOVERY_MODE: this site is in financial recovery after a restore or an '
                    'operator declaration. New financial work is held until an operator has reconciled '
                    'what already happened. Guest access is unaffected'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_recovery_gate() FROM PUBLIC;

-- New money movement is blocked at creation. Note what is NOT gated: no UPDATE trigger, so an outcome that
-- arrives for work that was ALREADY in flight can still be recorded. Refusing that would be the opposite of
-- safe -- it would leave a capture that really happened permanently unrecorded.
CREATE TRIGGER p4_recovery_gate_payments BEFORE INSERT ON iam_v2.payment_transactions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_recovery_gate();
CREATE TRIGGER p4_recovery_gate_outbox BEFORE INSERT ON iam_v2.posting_outbox
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_recovery_gate();

-- The execution boundary refuses to start anything new while held. This is the one that matters most: it is
-- the last point before a provider is contacted.
CREATE OR REPLACE FUNCTION iam_v2.begin_payment_execution(p_txn uuid) RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE tx record; se record;
BEGIN
  SELECT * INTO tx FROM iam_v2.payment_transactions WHERE id = p_txn FOR UPDATE;
  IF tx.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_NOT_EXECUTABLE: no such payment transaction %', p_txn
      USING ERRCODE = 'no_data_found';
  END IF;
  IF iam_v2.p4_financial_recovery_active(tx.tenant_id, tx.site_id) THEN
    RAISE EXCEPTION 'FINANCIAL_RECOVERY_MODE: execution is held pending operator reconciliation. Nothing '
                    'is replayed automatically after a restore' USING ERRCODE = 'check_violation';
  END IF;
  IF tx.status = 'PENDING' THEN RETURN 'ALREADY_EXECUTING'; END IF;
  IF tx.status <> 'CREATED' THEN
    RAISE EXCEPTION 'PAYMENT_NOT_EXECUTABLE: transaction is %; only a CREATED intent may begin executing',
      tx.status USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO se FROM iam_v2.settlements WHERE id = tx.settlement_id FOR UPDATE;
  IF tx.transaction_type = 'CHARGE' AND se.status = 'REQUIRED' THEN
    UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = se.id;
  ELSIF tx.transaction_type = 'CHARGE' AND se.status <> 'IN_PROGRESS' THEN
    RAISE EXCEPTION 'SETTLEMENT_NOT_EXECUTABLE: settlement is %; a charge cannot begin against it',
      se.status USING ERRCODE = 'check_violation';
  END IF;
  UPDATE iam_v2.payment_transactions SET status = 'PENDING' WHERE id = p_txn;
  RETURN 'EXECUTING';
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.begin_payment_execution(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.begin_payment_execution(uuid) TO sc_payment_runtime;

-- ============================================================================
-- (4) Operator reconciliation and release
-- ============================================================================
-- Resolving a hold records what an operator established actually happened. It moves no money and replays
-- nothing: CONFIRMED_COMPLETED means "the world already did this", which is the whole point -- the correct
-- response to work that already completed is to stop treating it as outstanding, not to do it again.
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
REVOKE EXECUTE ON FUNCTION iam_v2.p4_resolve_recovery_hold(uuid,text,uuid,text) FROM PUBLIC;

-- Release requires EVERY hold to have been resolved. There is no force flag and no partial release: leaving
-- recovery with unreconciled work is precisely the thing recovery exists to prevent, and an escape hatch
-- would become the normal path the first time an operator was in a hurry.
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
REVOKE EXECUTE ON FUNCTION iam_v2.p4_release_financial_recovery(uuid,uuid,uuid,text) FROM PUBLIC;

-- ============================================================================
-- (5) Reading recovery state
-- ============================================================================
CREATE VIEW iam_v2.v_financial_recovery AS
SELECT e.tenant_id, e.site_id, e.epoch, e.reason, e.entered_at, e.released_at,
       (e.released_at IS NULL AND e.reason <> 'INITIAL') AS recovery_active,
       (SELECT count(*) FROM iam_v2.financial_recovery_holds h
         WHERE h.tenant_id = e.tenant_id AND h.site_id = e.site_id AND h.epoch = e.epoch) AS held_total,
       (SELECT count(*) FROM iam_v2.financial_recovery_holds h
         WHERE h.tenant_id = e.tenant_id AND h.site_id = e.site_id AND h.epoch = e.epoch
           AND h.resolution IS NULL) AS held_open
  FROM iam_v2.financial_epochs e;

GRANT SELECT ON iam_v2.v_financial_recovery TO sc_financial_readonly, sc_financial_operator, sc_payment_runtime;
GRANT SELECT ON iam_v2.financial_recovery_holds, iam_v2.financial_epochs TO sc_financial_operator;
GRANT EXECUTE ON FUNCTION iam_v2.p4_financial_recovery_active(uuid,uuid)
  TO sc_payment_runtime, sc_financial_operator;
GRANT EXECUTE ON FUNCTION iam_v2.p4_reconcile_financial_epoch(uuid,uuid,text) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_resolve_recovery_hold(uuid,text,uuid,text) TO sc_financial_operator;
GRANT EXECUTE ON FUNCTION iam_v2.p4_release_financial_recovery(uuid,uuid,uuid,text) TO sc_financial_operator;
GRANT EXECUTE ON FUNCTION iam_v2.p4_declare_financial_recovery(uuid,uuid,uuid,text) TO sc_financial_operator;

INSERT INTO public.schema_migrations (version) VALUES ('0019_phase4_financial_recovery')
  ON CONFLICT DO NOTHING;

COMMIT;
