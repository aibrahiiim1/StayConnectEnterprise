-- 0018 — PHASE 4: authoritative financial identity, a controlled grant path, and operational least privilege.
-- Authorization: D18 / T0029 (unchanged). Receipt: T0038. Additive, reversible, DARK.
--
-- MEASURED FIRST, against the 0011..0017 chain in disposable PostgreSQL:
--
--   payment_transactions.merchant_account_id   uuid NOT NULL, referencing NOTHING. Any uuid was accepted.
--   payment_transactions.provider              free text. 'none' was accepted and was in fact what the
--                                              runtime persisted, because the DARK build has no adapter.
--   entitlements                               written by direct INSERT/UPDATE, so any role that could run
--                                              the grant path could also write an entitlement by hand.
--   sc_financial_readonly                      SELECT on ALL TABLES IN SCHEMA iam_v2 — every voucher hash,
--                                              every guest principal, every stay. Not a reporting role.
--   sc_financial_operator                      EXECUTE on nothing. It could not perform a review at all.
--
-- Three things follow from that, and this migration is those three things.
--
-- (1) FINANCIAL IDENTITY IS CONFIGURATION, NOT A REQUEST PARAMETER. Which provider and which merchant
--     account a site charges through is an operator decision recorded server-side. A payment intent
--     RESOLVES it; nothing may supply it. DARK means that configured identity exists with egress disabled
--     -- it does not mean inventing a placeholder identity and writing it to the financial record.
--
-- (2) THE ENTITLEMENT GRANT BECOMES REACHABLE WITHOUT ENTITLEMENT DML. The Phase-2 writer keeps its single
--     authoritative implementation; its three statements move behind SECURITY DEFINER functions so a
--     restricted runtime role can complete a paid grant while holding no INSERT or UPDATE on entitlements.
--
-- (3) THE ROLES BECOME OPERATIONAL. The operator can perform the sanctioned review and nothing else; the
--     reporting role sees explicit redacted views and nothing else.
BEGIN;

-- ============================================================================
-- (1) Authoritative provider / merchant-account configuration
-- ============================================================================
-- NO CREDENTIALS LIVE HERE. This table records WHICH provider and WHICH merchant account a site transacts
-- through -- identifiers the provider itself prints on statements, not secrets. API keys, signing secrets
-- and webhook secrets stay in the existing secret store and are never columns in the financial record,
-- because a financial table is read by reporting roles and copied into backups.
CREATE TABLE iam_v2.payment_provider_accounts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  -- A short stable adapter identifier. The placeholder guard is the point of the CHECK: a build with no
  -- adapter must fail closed at intent creation, never persist an invented identity and carry on.
  provider text NOT NULL CHECK (
    provider ~ '^[a-z][a-z0-9_-]{1,31}$'
    AND provider NOT IN ('none','unknown','placeholder','todo','default','null','na','n_a')),
  -- The provider's own account identifier for this site (a merchant id / account id / entity id).
  merchant_account_ref text NOT NULL CHECK (btrim(merchant_account_ref) <> '' AND length(merchant_account_ref) <= 128),
  display_name text,
  -- An account may be restricted to one settlement currency. NULL means "whatever the purchase pinned",
  -- which is the normal case; it is never a conversion instruction. Phase 4 performs no FX.
  currency char(3),
  status text NOT NULL DEFAULT 'DISABLED' CHECK (status IN ('ACTIVE','DISABLED')),
  is_default boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, site_id, id),
  UNIQUE (tenant_id, site_id, provider, merchant_account_ref),
  -- A DISABLED account is not a default. Otherwise "the default" could resolve to something switched off.
  CONSTRAINT ppa_default_is_active CHECK (NOT is_default OR status = 'ACTIVE')
);
-- Exactly one default per site, enforced rather than assumed: two defaults means the runtime picks one
-- arbitrarily, and an arbitrary choice of merchant account is an arbitrary choice of whose money moves.
CREATE UNIQUE INDEX ppa_one_default_per_site
  ON iam_v2.payment_provider_accounts (tenant_id, site_id) WHERE is_default;

COMMENT ON TABLE iam_v2.payment_provider_accounts IS
  'Authoritative per-site payment provider and merchant-account configuration. Identifiers only, never '
  'credentials. A payment intent RESOLVES its provider and merchant account from here; no request may '
  'supply either.';

-- BACKFILL BEFORE CONSTRAINING.
--
-- Adding the foreign key below to a database that already holds payment rows would fail: those rows name
-- merchant accounts that predate the configuration table. Measured on the gate's own fixture, that is
-- exactly what happens, and the same would be true of any environment where a payment had ever been
-- created. So every merchant account the financial record ALREADY references is recorded as what it
-- demonstrably is -- an account this site has transacted through.
--
-- They land DISABLED and non-default. Nothing is invented: the identifiers come from the rows themselves,
-- and no historical account is silently promoted into something new money can be taken through. An operator
-- decides which of them, if any, becomes the site's active default.
--
-- The one thing that is NOT backfilled is a placeholder. A historical row naming a provider this schema
-- forbids is the precise defect 0018 exists to eliminate, and quietly legitimising it in the same migration
-- would defeat the purpose; the migration stops instead and says so.
DO $backfill$
DECLARE bad int;
BEGIN
  SELECT count(*) INTO bad FROM iam_v2.payment_transactions
   WHERE provider !~ '^[a-z][a-z0-9_-]{1,31}$'
      OR provider IN ('none','unknown','placeholder','todo','default','null','na','n_a');
  IF bad > 0 THEN
    RAISE EXCEPTION 'PAYMENT_PLACEHOLDER_IDENTITY_PRESENT: % existing payment transaction(s) name a '
                    'placeholder provider. These must be reconciled by an operator before financial '
                    'identity can be constrained; this migration will not legitimise an invented '
                    'provider identity', bad USING ERRCODE = 'check_violation';
  END IF;
  INSERT INTO iam_v2.payment_provider_accounts
    (id, tenant_id, site_id, provider, merchant_account_ref, display_name, status, is_default)
  SELECT DISTINCT t.merchant_account_id, t.tenant_id, t.site_id, t.provider,
         'legacy:' || t.merchant_account_id::text,
         'Backfilled from existing payment records (0018)', 'DISABLED', false
    FROM iam_v2.payment_transactions t
  ON CONFLICT DO NOTHING;
END $backfill$;

-- The financial record now points at configured identity. This is the constraint that makes a forged or
-- invented merchant account impossible rather than merely unlikely.
ALTER TABLE iam_v2.payment_transactions
  ADD CONSTRAINT ptx_merchant_account_configured
  FOREIGN KEY (tenant_id, site_id, merchant_account_id)
  REFERENCES iam_v2.payment_provider_accounts (tenant_id, site_id, id);

-- The FK proves the account EXISTS and belongs to this site. It cannot prove the transaction's provider
-- string agrees with the account's, nor that the account was usable when the money moved.
CREATE OR REPLACE FUNCTION iam_v2.p4_payment_identity_gate() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $fn$
DECLARE acct record;
BEGIN
  SELECT * INTO acct FROM iam_v2.payment_provider_accounts
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.merchant_account_id;
  IF acct.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_UNKNOWN: merchant account % is not configured for this site',
      NEW.merchant_account_id USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.provider IS DISTINCT FROM acct.provider THEN
    RAISE EXCEPTION 'PAYMENT_PROVIDER_MISMATCH: the transaction says provider %, the configured account '
                    'says %. A payment must name the provider it is actually going to',
      NEW.provider, acct.provider USING ERRCODE = 'check_violation';
  END IF;
  IF acct.status <> 'ACTIVE' THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_NOT_ACTIVE: merchant account % is %; a disabled account cannot take '
                    'money', acct.id, acct.status USING ERRCODE = 'check_violation';
  END IF;
  IF acct.currency IS NOT NULL AND acct.currency <> NEW.currency THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_CURRENCY: account % settles in %, this payment is in %. Phase 4 '
                    'performs no conversion', acct.id, acct.currency, NEW.currency
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;
CREATE TRIGGER p4_payment_identity_gate BEFORE INSERT ON iam_v2.payment_transactions
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_payment_identity_gate();

-- The single resolution point. A runtime calls this and takes what it is given; there is no variant that
-- accepts a preference. It raises rather than returning NULL, so "no configuration" can never be mistaken
-- for "configuration that happens to be empty".
CREATE OR REPLACE FUNCTION iam_v2.p4_resolve_payment_account(p_tenant uuid, p_site uuid)
RETURNS TABLE (account_id uuid, provider text, merchant_account_ref text)
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE acct record;
BEGIN
  SELECT * INTO acct FROM iam_v2.payment_provider_accounts
   WHERE tenant_id = p_tenant AND site_id = p_site AND status = 'ACTIVE' AND is_default
   LIMIT 1;
  IF acct.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_NO_CONFIGURED_ACCOUNT: this site has no ACTIVE default payment account; '
                    'online payment cannot be attempted' USING ERRCODE = 'no_data_found';
  END IF;
  account_id := acct.id; provider := acct.provider; merchant_account_ref := acct.merchant_account_ref;
  RETURN NEXT;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_resolve_payment_account(uuid,uuid) FROM PUBLIC;

-- ============================================================================
-- (2) The controlled grant path
-- ============================================================================
-- These three functions are NOT a second entitlement writer. They are the three statements the ONE Phase-2
-- writer already executed, moved behind a definer boundary so a restricted role can run that writer without
-- holding entitlement DML. The Go writer keeps its single implementation and its ordering; what changes is
-- who needs privileges for it, not who decides.

-- Supersede whatever the subject currently holds. The SELECT ... FOR UPDATE is why this must be a function
-- at all: row locking requires UPDATE privilege, which is exactly what the restricted runtime must not have.
CREATE OR REPLACE FUNCTION iam_v2.p4_terminate_live_entitlement_for_subject(
  p_tenant uuid, p_site uuid, p_voucher uuid, p_account uuid, p_principal uuid)
RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_id uuid;
BEGIN
  IF p_voucher IS NULL AND p_account IS NULL AND p_principal IS NULL THEN
    RAISE EXCEPTION 'GRANT_SUBJECT_REQUIRED: an entitlement always belongs to exactly one subject'
      USING ERRCODE = 'check_violation';
  END IF;
  SELECT id INTO v_id FROM iam_v2.entitlements
   WHERE tenant_id = p_tenant AND site_id = p_site AND status IN ('PENDING','ACTIVE','SUSPENDED')
     AND ( (p_voucher   IS NOT NULL AND voucher_id         = p_voucher)
        OR (p_account   IS NOT NULL AND guest_account_id   = p_account)
        OR (p_principal IS NOT NULL AND guest_principal_id = p_principal) )
   ORDER BY activated_at DESC NULLS LAST, id
   LIMIT 1 FOR UPDATE;
  IF v_id IS NULL THEN RETURN NULL; END IF;
  PERFORM iam_v2.apply_entitlement_transition(v_id, 'TERMINATED', now(), 'SUPERSEDED');
  RETURN v_id;
END $fn$;
REVOKE EXECUTE ON FUNCTION
  iam_v2.p4_terminate_live_entitlement_for_subject(uuid,uuid,uuid,uuid,uuid) FROM PUBLIC;

-- Create the entitlement AND its opening transition, together. Splitting them is what produced the defect
-- corrected in T0037: an ACTIVE entitlement whose status no transition backs cannot commit.
CREATE OR REPLACE FUNCTION iam_v2.p4_insert_entitlement(
  p_tenant uuid, p_site uuid, p_voucher uuid, p_account uuid, p_principal uuid,
  p_purchase uuid, p_policy jsonb, p_plan_rev uuid, p_pkg_rev uuid,
  p_time_mode text, p_end_mode text, p_window_ends timestamptz, p_supersedes uuid)
RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_id uuid;
BEGIN
  INSERT INTO iam_v2.entitlements
    (tenant_id, site_id, voucher_id, guest_account_id, guest_principal_id, purchase_id,
     policy_snapshot, service_plan_revision_id, package_revision_id, time_accounting_mode,
     end_mode, window_ends_at, status, supersedes_entitlement_id, activated_at)
  VALUES (p_tenant, p_site, p_voucher, p_account, p_principal, p_purchase,
          p_policy, p_plan_rev, p_pkg_rev, p_time_mode,
          coalesce(nullif(p_end_mode,''), 'MANUAL_END'), p_window_ends, 'ACTIVE', p_supersedes, now())
  RETURNING id INTO v_id;
  PERFORM iam_v2.apply_entitlement_transition(v_id, 'ACTIVE', now(), 'GRANTED');
  RETURN v_id;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_insert_entitlement(
  uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid,text,text,timestamptz,uuid) FROM PUBLIC;

-- GRANTED is a state a purchase may only reach from a state that was actually awaiting one. The guard is
-- here rather than in the caller because a state machine enforced by its caller is a convention.
CREATE OR REPLACE FUNCTION iam_v2.p4_mark_purchase_granted(p_purchase uuid)
RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_state text;
BEGIN
  SELECT state INTO v_state FROM iam_v2.purchases WHERE id = p_purchase FOR UPDATE;
  IF v_state IS NULL THEN
    RAISE EXCEPTION 'PURCHASE_UNKNOWN: %', p_purchase USING ERRCODE = 'no_data_found';
  END IF;
  IF v_state NOT IN ('PENDING','AWAITING_SETTLEMENT') THEN
    RAISE EXCEPTION 'PURCHASE_STATE_TRANSITION: % -> GRANTED is not an approved transition', v_state
      USING ERRCODE = 'check_violation';
  END IF;
  UPDATE iam_v2.purchases SET state = 'GRANTED' WHERE id = p_purchase;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_mark_purchase_granted(uuid) FROM PUBLIC;

-- ============================================================================
-- (3) Reporting views — explicit and redacted
-- ============================================================================
-- A reporting role needs shapes and amounts, not identities. These views carry no voucher hash, no guest
-- principal, no device, no provider reference and no free-text reason: everything a finance or operations
-- reader legitimately needs to see, and nothing that would turn a reporting credential into a way to
-- enumerate guests.
CREATE VIEW iam_v2.v_financial_payments AS
SELECT t.tenant_id, t.site_id, t.id AS payment_id, t.settlement_id, t.transaction_type, t.status,
       t.provider, t.merchant_account_id, t.amount_minor, t.currency, t.currency_exponent,
       t.parent_transaction_id
  FROM iam_v2.payment_transactions t;

CREATE VIEW iam_v2.v_financial_settlements AS
SELECT se.tenant_id, se.site_id, se.id AS settlement_id, se.purchase_id, se.method, se.status,
       p.amount_minor, p.currency, p.currency_exponent, p.state AS purchase_state
  FROM iam_v2.settlements se
  JOIN iam_v2.purchases p ON p.tenant_id = se.tenant_id AND p.site_id = se.site_id AND p.id = se.purchase_id;

CREATE VIEW iam_v2.v_financial_review_queue AS
SELECT rs.tenant_id, rs.site_id, rs.posting_id, rs.review_version, rs.terminal_action,
       rs.escalation_count, rs.decided_at
  FROM iam_v2.posting_review_state rs;

COMMENT ON VIEW iam_v2.v_financial_payments IS
  'Redacted reporting projection. Deliberately omits provider_ref and idempotency_key: both are '
  'correlation handles, and a reporting role has nothing to correlate.';

-- ============================================================================
-- (4) The roles become operational
-- ============================================================================
-- sc_financial_readonly stops being "SELECT on everything". 0017 granted it the whole schema, which meant a
-- reporting credential could read every voucher hash and every guest principal in the estate.
REVOKE ALL ON ALL TABLES IN SCHEMA iam_v2 FROM sc_financial_readonly;
GRANT SELECT ON iam_v2.v_financial_payments, iam_v2.v_financial_settlements,
                iam_v2.v_financial_review_queue TO sc_financial_readonly;

-- sc_financial_operator can now perform the ONE sanctioned review operation. It still holds no write
-- privilege on any financial table, so the function is not merely the recommended path -- it is the only
-- path that exists for this role.
GRANT EXECUTE ON FUNCTION
  iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int,bigint) TO sc_financial_operator;
GRANT SELECT ON iam_v2.v_financial_payments, iam_v2.v_financial_settlements TO sc_financial_operator;

-- sc_payment_runtime can now complete Payment -> Settlement -> Entitlement end to end while holding NO
-- entitlement DML. Everything it needs is either a SELECT or a controlled function.
GRANT EXECUTE ON FUNCTION iam_v2.p4_resolve_payment_account(uuid,uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION
  iam_v2.p4_terminate_live_entitlement_for_subject(uuid,uuid,uuid,uuid,uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_insert_entitlement(
  uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid,text,text,timestamptz,uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_mark_purchase_granted(uuid) TO sc_payment_runtime;
GRANT SELECT ON iam_v2.payment_provider_accounts, iam_v2.entitlements, iam_v2.internet_package_revisions,
                iam_v2.service_plan_revisions, iam_v2.entitlement_state_transitions TO sc_payment_runtime;
-- The Phase-3 writer boundary. The grant path opens a controlled operation before it touches an auth
-- context, which is a guard rather than a privilege: it declares WHAT is being done so the append-only
-- machinery can check it. A role that may perform the operation must be able to declare it, otherwise the
-- guard becomes an accidental privilege wall around its own sanctioned caller.
GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) TO sc_payment_runtime;

-- Re-assert the definer posture over everything this migration added. A CREATE OR REPLACE restores the
-- default PUBLIC EXECUTE grant, so this sweep is not decoration.
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT p.oid::regprocedure AS sig FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
            WHERE n.nspname='iam_v2' AND p.prosecdef
  LOOP
    EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', r.sig);
  END LOOP;
END $$;
GRANT EXECUTE ON FUNCTION iam_v2.begin_payment_execution(uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION
  iam_v2.apply_payment_callback_v2(uuid,text,uuid,text,text,text,text,text,jsonb) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_resolve_payment_account(uuid,uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION
  iam_v2.p4_terminate_live_entitlement_for_subject(uuid,uuid,uuid,uuid,uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_insert_entitlement(
  uuid,uuid,uuid,uuid,uuid,uuid,jsonb,uuid,uuid,text,text,timestamptz,uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_mark_purchase_granted(uuid) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION
  iam_v2.record_posting_review_action(uuid,text,uuid,text,jsonb,int,bigint) TO sc_financial_operator;

-- ============================================================================
-- (5) Enforcement triggers must not charge their cost to the caller
-- ============================================================================
-- MEASURED: the restricted runtime could not create a payment intent at all. Not because it lacked any
-- privilege it should have had, but because 0016's admission gate is SECURITY INVOKER and does
-- SELECT ... FROM settlements ... FOR UPDATE. Row locking requires UPDATE privilege, so a trigger whose
-- entire purpose is to STOP the caller writing a settlement was demanding the right to write settlements.
--
-- That is backwards, and it is a trap worth naming: a SECURITY INVOKER enforcement trigger silently turns
-- its own internal reads into privileges the caller must hold, which pushes every least-privilege design
-- toward granting more than the operation needs. Enforcement runs as the owner. The identity gate below is
-- recreated for the same reason -- it reads configuration the caller has no business holding rights over.
CREATE OR REPLACE FUNCTION iam_v2.p4_payment_admission_gate() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_status text;
BEGIN
  SELECT status INTO v_status FROM iam_v2.settlements WHERE id = NEW.settlement_id FOR UPDATE;
  IF v_status IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_SETTLEMENT_UNKNOWN: settlement % does not exist', NEW.settlement_id
      USING ERRCODE = 'no_data_found';
  END IF;
  IF v_status IN ('SETTLED','FAILED','PARTIALLY_REVERSED','REVERSED') AND NEW.transaction_type = 'CHARGE' THEN
    RAISE EXCEPTION 'PAYMENT_SETTLEMENT_CLOSED: settlement is %; it cannot admit another charge. A charge '
                    'admitted here could later be CAPTURED while the settlement stays terminal, leaving '
                    'captured money whose settlement says otherwise', v_status
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

CREATE OR REPLACE FUNCTION iam_v2.p4_payment_identity_gate() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE acct record;
BEGIN
  SELECT * INTO acct FROM iam_v2.payment_provider_accounts
   WHERE tenant_id = NEW.tenant_id AND site_id = NEW.site_id AND id = NEW.merchant_account_id;
  IF acct.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_UNKNOWN: merchant account % is not configured for this site',
      NEW.merchant_account_id USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.provider IS DISTINCT FROM acct.provider THEN
    RAISE EXCEPTION 'PAYMENT_PROVIDER_MISMATCH: the transaction says provider %, the configured account '
                    'says %. A payment must name the provider it is actually going to',
      NEW.provider, acct.provider USING ERRCODE = 'check_violation';
  END IF;
  IF acct.status <> 'ACTIVE' THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_NOT_ACTIVE: merchant account % is %; a disabled account cannot take '
                    'money', acct.id, acct.status USING ERRCODE = 'check_violation';
  END IF;
  IF acct.currency IS NOT NULL AND acct.currency <> NEW.currency THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_CURRENCY: account % settles in %, this payment is in %. Phase 4 '
                    'performs no conversion', acct.id, acct.currency, NEW.currency
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

-- These two were recreated AFTER the sweep above, and CREATE OR REPLACE restores the default PUBLIC
-- EXECUTE grant, so without this they would be the only definer functions in the schema that PUBLIC could
-- call. Trigger functions are not callable usefully by hand, but "not useful" is not a security boundary.
REVOKE EXECUTE ON FUNCTION iam_v2.p4_payment_admission_gate() FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.p4_payment_identity_gate() FROM PUBLIC;

INSERT INTO public.schema_migrations (version) VALUES ('0018_phase4_financial_identity_and_privilege')
  ON CONFLICT DO NOTHING;

COMMIT;
