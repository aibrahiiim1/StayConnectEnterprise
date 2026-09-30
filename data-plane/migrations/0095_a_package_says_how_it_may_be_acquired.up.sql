-- A PACKAGE SAYS HOW IT MAY BE ACQUIRED.
--
-- Internet Packages are the canonical offer (docs/architecture/ONEGATE_MODULES_AND_ACQUISITION.md). A package
-- revision already carried settlement_methods text[]; nothing constrained it, and every writer hard-coded
-- {NOT_REQUIRED}. This migration makes that column the acquisition contract and adds the pieces the four
-- acquisition methods need, on the EXISTING purchase -> settlement -> grant kernel -> entitlement spine:
--
--   1. settlement_methods is CHECK-constrained: NOT_REQUIRED (Free), PREPAID (Voucher), ONLINE_PAYMENT (Card
--      payment), PMS_POSTING (Room charge). Price 0 allows only Free and Voucher; a price above 0 forbids Free.
--      MANUAL_APPROVAL stays in the SETTLEMENT contract, dormant and never offered; it is not a package method.
--   2. A settlement is born coherent: NOT_REQUIRED/NOT_REQUIRED, <method>/REQUIRED, or PREPAID/SETTLED backed by
--      a voucher redemption. Before this, nothing constrained a settlement's first status at all.
--   3. Vouchers are package-backed: a NEW voucher may be issued only against a revision that lists PREPAID, of
--      an ACTIVE package, that is the package's CURRENT revision. Redemption does NOT consult the revision's
--      method list or the package's active flag: an issued voucher is honoured until its own state or window
--      ends it, or an operator revokes it (voucher_revoke, and the new voucher_batch_revoke).
--   4. The anonymous access subject for open package selection: a server-generated opaque subject, NOT a
--      guest_principals row (that stays a verified identity) and NOT a MAC (a MAC identifies a Device only).
--   5. The grant kernel learns two more subject kinds -- the anonymous subject and a stay (for Room charge) --
--      and the entry points that authorise each acquisition: p4_grant_voucher_entitlement (new) and
--      p4_grant_paid_entitlement (now also PMS_POSTING). Every grant still funnels through the one kernel.
--
-- DATA. Existing free revisions keep {NOT_REQUIRED}. PREPAID is added ONLY to revisions that already-issued,
-- still-valid vouchers pin -- UNUSED within their window, or REDEEMED with a live entitlement -- so those
-- packages keep issuing and their cards keep redeeming. Revisions are immutable by trigger; this one correction
-- runs as the table owner inside this migration with the trigger disabled for exactly that statement, and the
-- rows it touched are counted in the migration output. Every other package gets Voucher support only when the
-- Site Admin chooses it.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The acquisition contract on the package revision.
-- ---------------------------------------------------------------------------------------------------------
DO $data$
DECLARE v_n int;
BEGIN
  ALTER TABLE iam_v2.internet_package_revisions DISABLE TRIGGER imm_pkg_rev;
  UPDATE iam_v2.internet_package_revisions r
     SET settlement_methods = array_append(r.settlement_methods, 'PREPAID')
   WHERE NOT ('PREPAID' = ANY (r.settlement_methods))
     AND r.id IN (
       SELECT v.package_revision_id FROM iam_v2.vouchers v
        WHERE (v.state = 'UNUSED' AND (v.redemption_valid_until IS NULL OR v.redemption_valid_until > now()))
           OR (v.state = 'REDEEMED' AND EXISTS (
                 SELECT 1 FROM iam_v2.entitlements e
                  WHERE e.voucher_id = v.id AND e.status IN ('PENDING','ACTIVE','SUSPENDED'))));
  GET DIAGNOSTICS v_n = ROW_COUNT;
  ALTER TABLE iam_v2.internet_package_revisions ENABLE TRIGGER imm_pkg_rev;
  RAISE NOTICE '0095: PREPAID added to % package revision(s) that already-issued valid vouchers pin', v_n;
END $data$;

ALTER TABLE iam_v2.internet_package_revisions
  DROP CONSTRAINT IF EXISTS ipr_acquisition_methods;
ALTER TABLE iam_v2.internet_package_revisions
  ADD CONSTRAINT ipr_acquisition_methods CHECK (
    cardinality(settlement_methods) >= 1
    AND settlement_methods <@ ARRAY['NOT_REQUIRED','PREPAID','ONLINE_PAYMENT','PMS_POSTING']::text[]
    AND (price_minor <> 0 OR settlement_methods <@ ARRAY['NOT_REQUIRED','PREPAID']::text[])
    AND (price_minor = 0 OR NOT ('NOT_REQUIRED' = ANY (settlement_methods)))
  );
COMMENT ON CONSTRAINT ipr_acquisition_methods ON iam_v2.internet_package_revisions IS
  'How a package may be acquired: NOT_REQUIRED=Free, PREPAID=Voucher, ONLINE_PAYMENT=Card payment, '
  'PMS_POSTING=Room charge. Free requires price 0 and a priced package cannot be free. MANUAL_APPROVAL is a '
  'dormant settlement method and never a package acquisition method.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. A settlement is born coherent.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_settlement_birth() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_trigger text; v_voucher uuid;
BEGIN
  IF NEW.method = 'NOT_REQUIRED' AND NEW.status = 'NOT_REQUIRED' THEN
    RETURN NEW;
  END IF;
  IF NEW.method IN ('ONLINE_PAYMENT','PMS_POSTING','MANUAL_APPROVAL') AND NEW.status = 'REQUIRED' THEN
    RETURN NEW;
  END IF;
  IF NEW.method = 'PREPAID' AND NEW.status = 'SETTLED' THEN
    SELECT pu.trigger, ac.voucher_id INTO v_trigger, v_voucher
      FROM iam_v2.purchases pu
      JOIN iam_v2.auth_contexts ac ON ac.id = pu.auth_context_id
     WHERE pu.id = NEW.purchase_id;
    IF v_trigger = 'VOUCHER_REDEMPTION' AND v_voucher IS NOT NULL THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'SETTLEMENT_BIRTH: a PREPAID settlement is born SETTLED only for a voucher redemption'
      USING ERRCODE = 'check_violation';
  END IF;
  RAISE EXCEPTION 'SETTLEMENT_BIRTH: % / % is not a coherent first state', NEW.method, NEW.status
    USING ERRCODE = 'check_violation';
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.p4_settlement_birth() FROM PUBLIC;
DROP TRIGGER IF EXISTS p4_settlement_birth ON iam_v2.settlements;
CREATE TRIGGER p4_settlement_birth BEFORE INSERT ON iam_v2.settlements
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_settlement_birth();

-- ---------------------------------------------------------------------------------------------------------
-- 3. Vouchers are issued against a package that supports Voucher acquisition.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.voucher_issuance_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_methods text[]; v_active boolean; v_current uuid;
BEGIN
  SELECT r.settlement_methods, p.active, p.current_revision_id
    INTO v_methods, v_active, v_current
    FROM iam_v2.internet_package_revisions r
    JOIN iam_v2.internet_packages p
      ON p.tenant_id = r.tenant_id AND p.site_id = r.site_id AND p.id = r.package_id
   WHERE r.tenant_id = NEW.tenant_id AND r.site_id = NEW.site_id AND r.id = NEW.package_revision_id;
  IF v_methods IS NULL THEN
    RAISE EXCEPTION 'VOUCHER_PACKAGE_UNKNOWN' USING ERRCODE = 'check_violation';
  END IF;
  IF NOT ('PREPAID' = ANY (v_methods)) THEN
    RAISE EXCEPTION 'VOUCHER_NOT_ACCEPTED: this package does not accept Voucher acquisition'
      USING ERRCODE = 'check_violation';
  END IF;
  IF v_active IS NOT TRUE THEN
    RAISE EXCEPTION 'VOUCHER_PACKAGE_INACTIVE: vouchers are issued only for an active package'
      USING ERRCODE = 'check_violation';
  END IF;
  IF v_current IS DISTINCT FROM NEW.package_revision_id THEN
    RAISE EXCEPTION 'VOUCHER_REVISION_NOT_CURRENT: vouchers are issued against the package''s current revision'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.voucher_issuance_gate() FROM PUBLIC;
DROP TRIGGER IF EXISTS voucher_issuance_gate ON iam_v2.vouchers;
CREATE TRIGGER voucher_issuance_gate BEFORE INSERT ON iam_v2.vouchers
  FOR EACH ROW EXECUTE FUNCTION iam_v2.voucher_issuance_gate();

-- An explicit, audited revocation of a whole batch. Revokes every UNUSED voucher of the batch; REDEEMED ones
-- already granted access and are not touched (ending that access is a session/entitlement action).
CREATE TABLE IF NOT EXISTS iam_v2.voucher_revocations (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL,
  site_id     uuid NOT NULL,
  batch_id    uuid,
  voucher_id  uuid,
  revoked_count integer NOT NULL CHECK (revoked_count >= 0),
  operator_id uuid NOT NULL,
  reason      text NOT NULL CHECK (length(btrim(reason)) >= 4),
  revoked_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT voucher_revocations_target CHECK (num_nonnulls(batch_id, voucher_id) = 1)
);
CREATE OR REPLACE FUNCTION iam_v2.voucher_revocations_append_only() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION 'iam_v2.voucher_revocations is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.voucher_revocations_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS voucher_revocations_append_only ON iam_v2.voucher_revocations;
CREATE TRIGGER voucher_revocations_append_only BEFORE UPDATE OR DELETE ON iam_v2.voucher_revocations
  FOR EACH ROW EXECUTE FUNCTION iam_v2.voucher_revocations_append_only();

CREATE OR REPLACE FUNCTION iam_v2.voucher_batch_revoke(
    p_tenant uuid, p_site uuid, p_batch uuid, p_operator uuid, p_reason text)
  RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $fn$
DECLARE v_n integer;
BEGIN
  IF p_operator IS NULL THEN
    RAISE EXCEPTION 'VOUCHER_REVOKE_NEEDS_AN_OPERATOR' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason, ''))) < 4 THEN
    RAISE EXCEPTION 'VOUCHER_REVOKE_NEEDS_A_REASON' USING ERRCODE = 'check_violation';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('voucher_batch_revoke'), hashtext(p_batch::text));
  UPDATE iam_v2.vouchers SET state = 'REVOKED'
   WHERE tenant_id = p_tenant AND site_id = p_site AND batch_id = p_batch AND state = 'UNUSED';
  GET DIAGNOSTICS v_n = ROW_COUNT;
  INSERT INTO iam_v2.voucher_revocations (tenant_id, site_id, batch_id, revoked_count, operator_id, reason)
  VALUES (p_tenant, p_site, p_batch, v_n, p_operator, btrim(p_reason));
  RETURN v_n;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.voucher_batch_revoke(uuid,uuid,uuid,uuid,text) FROM PUBLIC;

-- Single-voucher revocation now records who and why in the same append-only table.
CREATE OR REPLACE FUNCTION iam_v2.voucher_revoke(
    p_tenant uuid, p_site uuid, p_voucher uuid, p_operator uuid, p_reason text)
  RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, public, pg_temp AS $$
DECLARE v_n integer;
BEGIN
  IF p_operator IS NULL THEN
    RAISE EXCEPTION 'VOUCHER_REVOKE_NEEDS_AN_OPERATOR' USING ERRCODE = 'check_violation';
  END IF;
  IF length(btrim(coalesce(p_reason, ''))) < 4 THEN
    RAISE EXCEPTION 'VOUCHER_REVOKE_NEEDS_A_REASON' USING ERRCODE = 'check_violation';
  END IF;
  UPDATE iam_v2.vouchers
     SET state = 'REVOKED'
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_voucher AND state = 'UNUSED';
  GET DIAGNOSTICS v_n = ROW_COUNT;
  IF v_n <> 1 THEN
    RAISE EXCEPTION 'VOUCHER_NOT_REVOCABLE: no UNUSED voucher % for tenant %/site %', p_voucher, p_tenant, p_site
      USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.voucher_revocations (tenant_id, site_id, voucher_id, revoked_count, operator_id, reason)
  VALUES (p_tenant, p_site, p_voucher, 1, p_operator, btrim(p_reason));
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. The anonymous access subject.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.anonymous_access_subjects (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL,
  site_id         uuid NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  last_resumed_at timestamptz,
  UNIQUE (tenant_id, site_id, id)
);
COMMENT ON TABLE iam_v2.anonymous_access_subjects IS
  'The entitlement subject of a client who chose a package without signing in. Server-generated and opaque: '
  'it carries no identity factor and no MAC (a MAC identifies a Device only). Not a guest principal.';

CREATE TABLE IF NOT EXISTS iam_v2.anonymous_subject_credentials (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id    uuid NOT NULL,
  site_id      uuid NOT NULL,
  subject_id   uuid NOT NULL,
  kind         text NOT NULL CHECK (kind IN ('RESUME','RECOVERY')),
  secret_hmac  bytea NOT NULL UNIQUE,
  key_id       text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  revoked_at   timestamptz,
  FOREIGN KEY (tenant_id, site_id, subject_id)
    REFERENCES iam_v2.anonymous_access_subjects (tenant_id, site_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS anonymous_subject_credentials_subject
  ON iam_v2.anonymous_subject_credentials (tenant_id, site_id, subject_id);
COMMENT ON TABLE iam_v2.anonymous_subject_credentials IS
  'Resume token (HttpOnly portal cookie) and recovery code (shown to the client) of an anonymous subject. '
  'Only keyed HMACs are stored; the plaintext exists only in the client''s browser or hand.';

ALTER TABLE iam_v2.auth_contexts ADD COLUMN IF NOT EXISTS anonymous_subject_id uuid;
ALTER TABLE iam_v2.entitlements  ADD COLUMN IF NOT EXISTS anonymous_subject_id uuid;
DO $fk$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ac_anonymous_subject_fk') THEN
    ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT ac_anonymous_subject_fk
      FOREIGN KEY (tenant_id, site_id, anonymous_subject_id)
      REFERENCES iam_v2.anonymous_access_subjects (tenant_id, site_id, id);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ent_anonymous_subject_fk') THEN
    ALTER TABLE iam_v2.entitlements ADD CONSTRAINT ent_anonymous_subject_fk
      FOREIGN KEY (tenant_id, site_id, anonymous_subject_id)
      REFERENCES iam_v2.anonymous_access_subjects (tenant_id, site_id, id) ON DELETE CASCADE;
  END IF;
END $fk$;

ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS auth_contexts_method_check;
ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT auth_contexts_method_check
  CHECK (method IN ('PMS','VOUCHER','ACCOUNT','OTP','SOCIAL','POST_STAY_PIN','OPEN'));
ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_one_subject;
ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT ac_one_subject
  CHECK (num_nonnulls(stay_id, guest_account_id, voucher_id, guest_principal_id, post_stay_profile_id,
                      anonymous_subject_id) = 1);
ALTER TABLE iam_v2.auth_contexts DROP CONSTRAINT IF EXISTS ac_method_subject;
ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT ac_method_subject CHECK (
      (method = 'PMS'             AND stay_id IS NOT NULL)
   OR (method = 'VOUCHER'         AND voucher_id IS NOT NULL)
   OR (method = 'ACCOUNT'         AND guest_account_id IS NOT NULL)
   OR (method IN ('OTP','SOCIAL') AND guest_principal_id IS NOT NULL)
   OR (method = 'POST_STAY_PIN'   AND post_stay_profile_id IS NOT NULL)
   OR (method = 'OPEN'            AND anonymous_subject_id IS NOT NULL));

ALTER TABLE iam_v2.entitlements DROP CONSTRAINT IF EXISTS ent_one_subject;
ALTER TABLE iam_v2.entitlements ADD CONSTRAINT ent_one_subject
  CHECK (num_nonnulls(stay_id, guest_account_id, voucher_id, guest_principal_id, anonymous_subject_id) = 1);
CREATE UNIQUE INDEX IF NOT EXISTS ent_live_anonymous ON iam_v2.entitlements (anonymous_subject_id)
  WHERE status IN ('PENDING','ACTIVE','SUSPENDED');

-- ---------------------------------------------------------------------------------------------------------
-- 5. The grant kernel, with every subject kind, and its entry points.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_entitlement_grant_kernel_v2(
  p_tenant uuid, p_site uuid, p_purchase uuid,
  p_voucher uuid, p_account uuid, p_principal uuid, p_anon uuid, p_stay uuid, p_iface uuid,
  p_snapshot jsonb, p_plan_rev uuid, p_pkg_rev uuid)
RETURNS TABLE (entitlement_id uuid, already_granted boolean, superseded uuid)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_subject_key text; v_existing uuid; v_superseded uuid; v_new uuid;
  v_time_mode text; v_end_mode text; v_window timestamptz; v_state text;
  v_burned int; v_voucher_pkg uuid; v_quota bigint;
BEGIN
  IF num_nonnulls(p_voucher, p_account, p_principal, p_anon, p_stay) <> 1 THEN
    RAISE EXCEPTION 'GRANT_SUBJECT_UNRESOLVED: an entitlement always belongs to exactly one subject'
      USING ERRCODE = 'check_violation';
  END IF;
  IF p_stay IS NOT NULL AND p_iface IS NULL THEN
    RAISE EXCEPTION 'GRANT_STAY_UNPINNED: a stay entitlement names its PMS interface' USING ERRCODE = 'check_violation';
  END IF;
  IF p_snapshot IS NULL OR p_snapshot->>'service_plan_revision_id' IS NULL THEN
    RAISE EXCEPTION 'GRANT_SNAPSHOT_UNREADABLE' USING ERRCODE = 'check_violation';
  END IF;

  -- A voucher grants what it was printed for, and nothing else (0088).
  IF p_voucher IS NOT NULL THEN
    IF p_pkg_rev IS NULL THEN
      RAISE EXCEPTION 'VOUCHER_PACKAGE_UNPINNED: a voucher grant must name the package revision it grants'
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT package_revision_id INTO v_voucher_pkg FROM iam_v2.vouchers
     WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_voucher;
    IF v_voucher_pkg IS NULL THEN
      RAISE EXCEPTION 'VOUCHER_NOT_FOUND: voucher % does not belong to this owner', p_voucher
        USING ERRCODE = 'check_violation';
    END IF;
    IF v_voucher_pkg <> p_pkg_rev THEN
      RAISE EXCEPTION 'VOUCHER_PACKAGE_MISMATCH: voucher % was printed against package revision %, and this '
                      'grant is for %; a card grants what it was printed for', p_voucher, v_voucher_pkg, p_pkg_rev
        USING ERRCODE = 'check_violation';
    END IF;
  END IF;
  v_time_mode := coalesce(p_snapshot->>'time_accounting_mode', 'VALIDITY_WINDOW');
  v_end_mode  := coalesce(nullif(p_snapshot->>'end_mode',''), 'MANUAL_END');
  v_window    := CASE WHEN p_snapshot->>'window_ends_at' IS NOT NULL
                      THEN (p_snapshot->>'window_ends_at')::timestamptz ELSE NULL END;
  -- A PER_STAY_NIGHT allowance resolved ONCE at quote time from the pinned stay and frozen here. Only this
  -- explicit key is read: the snapshot's plan data_quota_bytes is the plan's own number and the entitlement
  -- keeps reading its plan revision for it, exactly as before.
  v_quota     := CASE WHEN (p_snapshot->>'frozen_data_quota_bytes') IS NOT NULL
                       AND (p_snapshot->>'frozen_data_quota_bytes')::bigint > 0
                      THEN (p_snapshot->>'frozen_data_quota_bytes')::bigint ELSE NULL END;

  v_subject_key := 'phase2.subject|' || p_tenant::text || '|' || p_site::text || '|' ||
                   coalesce(p_voucher::text, p_account::text, p_principal::text, p_anon::text, p_stay::text);
  PERFORM pg_advisory_xact_lock(hashtext(v_subject_key));

  SELECT id INTO v_existing FROM iam_v2.entitlements WHERE purchase_id = p_purchase LIMIT 1;
  IF v_existing IS NOT NULL THEN
    entitlement_id := v_existing; already_granted := true; superseded := NULL; RETURN NEXT; RETURN;
  END IF;

  SELECT id INTO v_superseded FROM iam_v2.entitlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND status IN ('PENDING','ACTIVE','SUSPENDED')
     AND ( (p_voucher   IS NOT NULL AND voucher_id           = p_voucher)
        OR (p_account   IS NOT NULL AND guest_account_id     = p_account)
        OR (p_principal IS NOT NULL AND guest_principal_id   = p_principal)
        OR (p_anon      IS NOT NULL AND anonymous_subject_id = p_anon)
        OR (p_stay      IS NOT NULL AND stay_id              = p_stay) )
   ORDER BY activated_at DESC NULLS LAST, id LIMIT 1 FOR UPDATE;
  IF v_superseded IS NOT NULL THEN
    PERFORM iam_v2.apply_entitlement_transition(v_superseded, 'TERMINATED', now(), 'SUPERSEDED');
  END IF;

  INSERT INTO iam_v2.entitlements
    (tenant_id, site_id, voucher_id, guest_account_id, guest_principal_id, anonymous_subject_id,
     stay_id, pms_interface_id, purchase_id,
     policy_snapshot, service_plan_revision_id, package_revision_id, time_accounting_mode,
     end_mode, window_ends_at, status, supersedes_entitlement_id, activated_at, data_quota_bytes)
  VALUES (p_tenant, p_site, p_voucher, p_account, p_principal, p_anon,
          p_stay, CASE WHEN p_stay IS NOT NULL THEN p_iface END, p_purchase,
          p_snapshot, p_plan_rev, p_pkg_rev, v_time_mode, v_end_mode, v_window,
          'ACTIVE', v_superseded, now(), v_quota)
  RETURNING id INTO v_new;
  PERFORM iam_v2.apply_entitlement_transition(v_new, 'ACTIVE', now(), 'GRANTED');

  -- SINGLE-USE: the voucher is spent here, inside the grant, or the grant does not happen (0084).
  IF p_voucher IS NOT NULL THEN
    UPDATE iam_v2.vouchers
       SET state = 'REDEEMED'
     WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_voucher
       AND state = 'UNUSED'
       AND (redemption_valid_from  IS NULL OR redemption_valid_from  <= now())
       AND (redemption_valid_until IS NULL OR redemption_valid_until >  now());
    GET DIAGNOSTICS v_burned = ROW_COUNT;
    IF v_burned <> 1 THEN
      RAISE EXCEPTION 'VOUCHER_NOT_REDEEMABLE: voucher % is not UNUSED and inside its window for this owner',
        p_voucher USING ERRCODE = 'check_violation';
    END IF;
  END IF;

  SELECT state INTO v_state FROM iam_v2.purchases WHERE id = p_purchase FOR UPDATE;
  IF v_state NOT IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW') THEN
    RAISE EXCEPTION 'PURCHASE_STATE_TRANSITION: % -> GRANTED is not an approved transition', v_state
      USING ERRCODE = 'check_violation';
  END IF;
  UPDATE iam_v2.purchases SET state = 'GRANTED' WHERE id = p_purchase;

  entitlement_id := v_new; already_granted := false; superseded := v_superseded; RETURN NEXT;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.p4_entitlement_grant_kernel_v2(
  uuid,uuid,uuid,uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid) FROM PUBLIC;

-- The original kernel keeps its signature and delegates, so nothing that already calls it changes meaning.
CREATE OR REPLACE FUNCTION iam_v2.p4_entitlement_grant_kernel(
  p_tenant uuid, p_site uuid, p_purchase uuid,
  p_voucher uuid, p_account uuid, p_principal uuid,
  p_snapshot jsonb, p_plan_rev uuid, p_pkg_rev uuid)
RETURNS TABLE (entitlement_id uuid, already_granted boolean, superseded uuid)
  LANGUAGE sql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT * FROM iam_v2.p4_entitlement_grant_kernel_v2(p_tenant, p_site, p_purchase,
    p_voucher, p_account, p_principal, NULL, NULL, NULL, p_snapshot, p_plan_rev, p_pkg_rev);
$fn$;
REVOKE ALL ON FUNCTION iam_v2.p4_entitlement_grant_kernel(uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid) FROM PUBLIC;

-- FREE: unchanged authorisation (quote price 0, settlement NOT_REQUIRED), now also for the anonymous subject.
CREATE OR REPLACE FUNCTION iam_v2.p4_grant_quoted_entitlement(
  p_tenant uuid, p_site uuid, p_purchase uuid)
RETURNS TABLE (entitlement_id uuid, already_granted boolean, superseded uuid)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE pu record; se record; q record; ac record;
BEGIN
  SELECT * INTO pu FROM iam_v2.purchases
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_purchase FOR UPDATE;
  IF pu.id IS NULL THEN RAISE EXCEPTION 'GRANT_PURCHASE_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  SELECT * INTO se FROM iam_v2.settlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND purchase_id = pu.id;
  IF se.id IS NULL OR se.method <> 'NOT_REQUIRED' OR se.status <> 'NOT_REQUIRED' THEN
    RAISE EXCEPTION 'GRANT_NOT_FREE: this purchase requires settlement (% / %); a free grant is not the '
                    'right authorization for it', coalesce(se.method,'none'), coalesce(se.status,'none')
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO q  FROM iam_v2.offer_quotes
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.offer_quote_id;
  SELECT * INTO ac FROM iam_v2.auth_contexts
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.auth_context_id;
  IF q.id IS NULL OR ac.id IS NULL THEN
    RAISE EXCEPTION 'GRANT_EVIDENCE_MISSING: the purchase has no pinned quote or auth context'
      USING ERRCODE = 'no_data_found';
  END IF;
  IF coalesce(q.price_minor, 0) <> 0 THEN
    RAISE EXCEPTION 'GRANT_NOT_FREE: the pinned quote is priced at %; money has to arrive first',
      q.price_minor USING ERRCODE = 'check_violation';
  END IF;
  RETURN QUERY SELECT * FROM iam_v2.p4_entitlement_grant_kernel_v2(
    p_tenant, p_site, pu.id, ac.voucher_id, ac.guest_account_id, ac.guest_principal_id,
    ac.anonymous_subject_id, NULL, NULL,
    q.grant_snapshot, (q.grant_snapshot->>'service_plan_revision_id')::uuid, pu.package_revision_id);
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid) FROM PUBLIC;

-- VOUCHER: a PREPAID settlement born SETTLED for a VOUCHER_REDEMPTION purchase whose auth context carries the
-- voucher, for the voucher's own pinned revision. The kernel burns the voucher in the same transaction.
CREATE OR REPLACE FUNCTION iam_v2.p4_grant_voucher_entitlement(
  p_tenant uuid, p_site uuid, p_purchase uuid)
RETURNS TABLE (entitlement_id uuid, already_granted boolean, superseded uuid)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE pu record; se record; q record; ac record;
BEGIN
  SELECT * INTO pu FROM iam_v2.purchases
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_purchase FOR UPDATE;
  IF pu.id IS NULL THEN RAISE EXCEPTION 'GRANT_PURCHASE_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  IF pu.trigger <> 'VOUCHER_REDEMPTION' THEN
    RAISE EXCEPTION 'GRANT_WRONG_RAIL: a voucher grant needs a VOUCHER_REDEMPTION purchase, not %', pu.trigger
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO se FROM iam_v2.settlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND purchase_id = pu.id;
  IF se.id IS NULL OR se.method <> 'PREPAID' OR se.status <> 'SETTLED' THEN
    RAISE EXCEPTION 'GRANT_NOT_PREPAID: settlement is % / %', coalesce(se.method,'none'), coalesce(se.status,'none')
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO q  FROM iam_v2.offer_quotes
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.offer_quote_id;
  SELECT * INTO ac FROM iam_v2.auth_contexts
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.auth_context_id;
  IF q.id IS NULL OR ac.id IS NULL OR ac.voucher_id IS NULL THEN
    RAISE EXCEPTION 'GRANT_EVIDENCE_MISSING: a voucher grant needs a pinned quote and a voucher auth context'
      USING ERRCODE = 'no_data_found';
  END IF;
  RETURN QUERY SELECT * FROM iam_v2.p4_entitlement_grant_kernel_v2(
    p_tenant, p_site, pu.id, ac.voucher_id, NULL, NULL, NULL, NULL, NULL,
    q.grant_snapshot, (q.grant_snapshot->>'service_plan_revision_id')::uuid, pu.package_revision_id);
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.p4_grant_voucher_entitlement(uuid,uuid,uuid) FROM PUBLIC;

-- PAID: ONLINE_PAYMENT or PMS_POSTING, SETTLED. Nothing short of an authoritative settlement is money.
CREATE OR REPLACE FUNCTION iam_v2.p4_grant_paid_entitlement(
  p_tenant uuid, p_site uuid, p_settlement uuid)
RETURNS TABLE (entitlement_id uuid, already_granted boolean, superseded uuid)
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE se record; pu record; q record; ac record;
BEGIN
  SELECT * INTO se FROM iam_v2.settlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_settlement FOR UPDATE;
  IF se.id IS NULL THEN
    RAISE EXCEPTION 'GRANT_SETTLEMENT_UNKNOWN: no such settlement in this tenant and site'
      USING ERRCODE = 'no_data_found';
  END IF;
  IF se.method NOT IN ('ONLINE_PAYMENT','PMS_POSTING') THEN
    RAISE EXCEPTION 'GRANT_WRONG_RAIL: settlement method is %; this operation grants only against a card '
                    'payment or a room charge', se.method USING ERRCODE = 'check_violation';
  END IF;
  IF se.status <> 'SETTLED' THEN
    RAISE EXCEPTION 'GRANT_NOT_SETTLED: settlement is %; money is the authorization and nothing short of '
                    'SETTLED is money', se.status USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO pu FROM iam_v2.purchases
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = se.purchase_id FOR UPDATE;
  IF pu.id IS NULL THEN RAISE EXCEPTION 'GRANT_PURCHASE_UNKNOWN' USING ERRCODE = 'no_data_found'; END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.entitlements WHERE purchase_id = pu.id) THEN
    RETURN QUERY SELECT e.id, true, NULL::uuid FROM iam_v2.entitlements e WHERE e.purchase_id = pu.id LIMIT 1;
    RETURN;
  END IF;
  IF pu.state NOT IN ('AWAITING_SETTLEMENT','MANUAL_REVIEW') THEN
    RAISE EXCEPTION 'GRANT_PURCHASE_STATE: a paid grant requires the purchase to be AWAITING_SETTLEMENT, '
                    'not %', pu.state USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO q  FROM iam_v2.offer_quotes
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.offer_quote_id;
  SELECT * INTO ac FROM iam_v2.auth_contexts
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = pu.auth_context_id;
  IF q.id IS NULL OR ac.id IS NULL THEN
    RAISE EXCEPTION 'GRANT_EVIDENCE_MISSING: the purchase has no pinned quote or auth context'
      USING ERRCODE = 'no_data_found';
  END IF;
  IF se.method = 'PMS_POSTING' AND (pu.stay_id IS NULL OR pu.pms_interface_id IS NULL) THEN
    RAISE EXCEPTION 'GRANT_EVIDENCE_MISSING: a room charge grant needs the pinned stay and interface'
      USING ERRCODE = 'no_data_found';
  END IF;
  RETURN QUERY SELECT * FROM iam_v2.p4_entitlement_grant_kernel_v2(
    p_tenant, p_site, pu.id,
    ac.voucher_id, ac.guest_account_id, ac.guest_principal_id, ac.anonymous_subject_id,
    CASE WHEN ac.stay_id IS NOT NULL THEN pu.stay_id END,
    CASE WHEN ac.stay_id IS NOT NULL THEN pu.pms_interface_id END,
    q.grant_snapshot, (q.grant_snapshot->>'service_plan_revision_id')::uuid, pu.package_revision_id);
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 6. Privileges. The payment runtime keeps the paid entry point; the portal runtime gets the voucher entry
--    point beside the free one; scd reads and creates anonymous subjects. Durable copies live in Gate-P.
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON iam_v2.anonymous_access_subjects FROM PUBLIC;
REVOKE ALL ON iam_v2.anonymous_subject_credentials FROM PUBLIC;
REVOKE ALL ON iam_v2.voucher_revocations FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid) TO sc_commerce_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_grant_voucher_entitlement(uuid,uuid,uuid) TO sc_commerce_runtime;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.p4_grant_voucher_entitlement(uuid,uuid,uuid) TO svc_scd;
    GRANT SELECT, INSERT ON iam_v2.anonymous_access_subjects TO svc_scd;
    GRANT UPDATE (last_resumed_at) ON iam_v2.anonymous_access_subjects TO svc_scd;
    GRANT SELECT, INSERT ON iam_v2.anonymous_subject_credentials TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.voucher_batch_revoke(uuid,uuid,uuid,uuid,text) TO svc_scd;
    GRANT SELECT, INSERT ON iam_v2.voucher_batches TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.anonymous_access_subjects TO svc_edged;
    GRANT SELECT ON iam_v2.voucher_revocations TO svc_edged;
  END IF;
END
$grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.anonymous_access_subjects OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.anonymous_subject_credentials OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.voucher_revocations OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.voucher_revocations_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_settlement_birth() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.voucher_issuance_gate() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.voucher_batch_revoke(uuid,uuid,uuid,uuid,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.voucher_revoke(uuid,uuid,uuid,uuid,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_entitlement_grant_kernel_v2(uuid,uuid,uuid,uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_entitlement_grant_kernel(uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_grant_quoted_entitlement(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_grant_voucher_entitlement(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_grant_paid_entitlement(uuid,uuid,uuid) OWNER TO iam_v2_owner';
  END IF;
END $own$;

COMMIT;
