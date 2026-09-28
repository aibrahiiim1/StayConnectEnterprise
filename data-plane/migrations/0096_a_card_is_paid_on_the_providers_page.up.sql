-- A CARD IS PAID ON THE PROVIDER'S PAGE, AND ONLY THE PROVIDER SAYS IT WAS PAID.
--
-- Card payment (docs/architecture/ONEGATE_MODULES_AND_ACQUISITION.md, section 6.3) reuses the accepted Phase-4
-- payment ledger: payment_transactions, begin_payment_execution, the outcome authority p4_apply_provider_outcome
-- (outcome role only), the settlement state machine and p4_grant_paid_entitlement. What was missing, and is added
-- here:
--
--   1. The site-local provider account is configurable: a mode (TEST or LIVE), a definer writer with an
--      append-only change log, and SEALED credentials (AES-256-GCM, owner-bound AAD; the key never enters the
--      database). No runtime role holds a table write privilege on either table.
--   2. A hosted checkout is recorded beside its transaction (payment_checkouts): the provider's session
--      reference is write-once; the reconciler's bookkeeping (last status check) is the only thing that moves.
--   3. Provider-originated refunds and chargebacks reported by a status query are recorded in the existing
--      ledger through the outcome role (p4_record_provider_reversal). OneGate never initiates one.
--   4. The purchase follows its settlement when the settlement fails or goes to manual review, so a purchase
--      whose money never arrived does not stay "awaiting" forever.
--   5. Extra payment walled-garden domains a Site Admin may add (bounded, FQDN-only, audited).
--   6. THE LEGACY PLAINTEXT PATH IS REMOVED: public.stripe_accounts (secret_key / webhook_secret in clear text)
--      is dropped. It was reachable through an operator CRUD surface and read by nothing.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Provider accounts: mode, writer, change log, sealed credentials.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.payment_provider_accounts
  ADD COLUMN IF NOT EXISTS mode text NOT NULL DEFAULT 'TEST';
ALTER TABLE iam_v2.payment_provider_accounts DROP CONSTRAINT IF EXISTS ppa_mode_known;
ALTER TABLE iam_v2.payment_provider_accounts ADD CONSTRAINT ppa_mode_known CHECK (mode IN ('TEST','LIVE'));
COMMENT ON COLUMN iam_v2.payment_provider_accounts.mode IS
  'TEST uses the provider sandbox. LIVE moves real money and is refused by the deployment ceiling unless '
  'STAYCONNECT_PAYMENT_LIVE_ALLOWED is set, which requires a separate Product-Owner authorisation.';

CREATE TABLE IF NOT EXISTS iam_v2.payment_provider_secret_generations (
  id             uuid PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  site_id        uuid NOT NULL,
  account_id     uuid NOT NULL,
  ciphertext     bytea NOT NULL,
  nonce          bytea NOT NULL,
  key_id         text  NOT NULL,
  cipher_version smallint NOT NULL CHECK (cipher_version = 1),
  created_at     timestamptz NOT NULL DEFAULT now(),
  created_by     text NOT NULL CHECK (length(btrim(created_by)) > 0),
  superseded_at  timestamptz,
  FOREIGN KEY (tenant_id, site_id, account_id)
    REFERENCES iam_v2.payment_provider_accounts (tenant_id, site_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ppsg_one_current
  ON iam_v2.payment_provider_secret_generations (account_id) WHERE superseded_at IS NULL;
COMMENT ON TABLE iam_v2.payment_provider_secret_generations IS
  'Sealed provider credentials. Only ciphertext is stored; the AEAD key lives in /etc/stayconnect/secrets and '
  'the AAD binds each ciphertext to its tenant, site, account and generation. Never returned by any API.';

CREATE TABLE IF NOT EXISTS iam_v2.payment_provider_account_changes (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL,
  site_id       uuid NOT NULL,
  account_id    uuid NOT NULL,
  changed_at    timestamptz NOT NULL DEFAULT now(),
  changed_by    text NOT NULL CHECK (length(btrim(changed_by)) > 0),
  change_reason text,
  change_kind   text NOT NULL CHECK (change_kind IN ('CREATED','UPDATED','CREDENTIALS_ROTATED')),
  old_values    jsonb,
  new_values    jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS ppac_lookup ON iam_v2.payment_provider_account_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.p4_append_only_refuse() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
BEGIN
  RAISE EXCEPTION '% is append-only: % refused', TG_TABLE_NAME, TG_OP USING ERRCODE = 'restrict_violation';
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.p4_append_only_refuse() FROM PUBLIC;
DROP TRIGGER IF EXISTS ppac_append_only ON iam_v2.payment_provider_account_changes;
CREATE TRIGGER ppac_append_only BEFORE UPDATE OR DELETE ON iam_v2.payment_provider_account_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_append_only_refuse();

-- Save (create or update) one account. Secrets are NOT an argument: they are sealed by scd and stored by
-- payment_account_set_secret. Making an account the default demotes any other default in the same statement.
CREATE OR REPLACE FUNCTION iam_v2.payment_account_save(
    p_tenant uuid, p_site uuid, p_id uuid, p_provider text, p_merchant_ref text, p_display_name text,
    p_currency text, p_mode text, p_status text, p_is_default boolean, p_operator text, p_reason text)
  RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_id uuid; v_old jsonb; v_new jsonb;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_provider NOT IN ('stripe','paymob') THEN
    RAISE EXCEPTION 'PAYMENT_PROVIDER_UNSUPPORTED: %', p_provider USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_currency IS NULL OR p_currency !~ '^[A-Z]{3}$' THEN
    RAISE EXCEPTION 'a three-letter currency is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('payment_account_save'), hashtext(p_site::text));
  IF p_is_default THEN
    UPDATE iam_v2.payment_provider_accounts SET is_default = false, updated_at = now()
     WHERE tenant_id = p_tenant AND site_id = p_site AND is_default AND id IS DISTINCT FROM p_id;
  END IF;
  IF p_id IS NULL THEN
    INSERT INTO iam_v2.payment_provider_accounts
      (tenant_id, site_id, provider, merchant_account_ref, display_name, currency, mode, status, is_default, provenance)
    VALUES (p_tenant, p_site, p_provider, p_merchant_ref, p_display_name, p_currency, p_mode, p_status, p_is_default, 'CONFIGURED')
    RETURNING id INTO v_id;
  ELSE
    SELECT to_jsonb(a) - 'created_at' - 'updated_at' INTO v_old FROM iam_v2.payment_provider_accounts a
     WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_id FOR UPDATE;
    IF v_old IS NULL THEN
      RAISE EXCEPTION 'PAYMENT_ACCOUNT_UNKNOWN' USING ERRCODE = 'no_data_found';
    END IF;
    UPDATE iam_v2.payment_provider_accounts
       SET provider = p_provider, merchant_account_ref = p_merchant_ref, display_name = p_display_name,
           currency = p_currency, mode = p_mode, status = p_status, is_default = p_is_default,
           provenance = 'CONFIGURED', updated_at = now()
     WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_id
    RETURNING id INTO v_id;
  END IF;
  SELECT to_jsonb(a) - 'created_at' - 'updated_at' INTO v_new FROM iam_v2.payment_provider_accounts a WHERE id = v_id;
  INSERT INTO iam_v2.payment_provider_account_changes
    (tenant_id, site_id, account_id, changed_by, change_reason, change_kind, old_values, new_values)
  VALUES (p_tenant, p_site, v_id, btrim(p_operator), NULLIF(btrim(coalesce(p_reason,'')),''),
          CASE WHEN v_old IS NULL THEN 'CREATED' ELSE 'UPDATED' END, v_old, v_new);
  RETURN v_id;
END $fn$;

CREATE OR REPLACE FUNCTION iam_v2.payment_account_set_secret(
    p_tenant uuid, p_site uuid, p_account uuid, p_generation uuid, p_ciphertext bytea, p_nonce bytea,
    p_key_id text, p_cipher_version smallint, p_operator text)
  RETURNS void
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM 1 FROM iam_v2.payment_provider_accounts
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_account FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_UNKNOWN' USING ERRCODE = 'no_data_found';
  END IF;
  UPDATE iam_v2.payment_provider_secret_generations SET superseded_at = now()
   WHERE account_id = p_account AND superseded_at IS NULL;
  INSERT INTO iam_v2.payment_provider_secret_generations
    (id, tenant_id, site_id, account_id, ciphertext, nonce, key_id, cipher_version, created_by)
  VALUES (p_generation, p_tenant, p_site, p_account, p_ciphertext, p_nonce, p_key_id, p_cipher_version, btrim(p_operator));
  INSERT INTO iam_v2.payment_provider_account_changes
    (tenant_id, site_id, account_id, changed_by, change_kind, new_values)
  VALUES (p_tenant, p_site, p_account, btrim(p_operator), 'CREDENTIALS_ROTATED',
          jsonb_build_object('generation', p_generation, 'key_id', p_key_id));
END $fn$;

-- The account the runtime resolves, now with its mode.
DROP FUNCTION IF EXISTS iam_v2.p4_resolve_payment_account_v2(uuid,uuid);
CREATE OR REPLACE FUNCTION iam_v2.p4_resolve_payment_account_v2(p_tenant uuid, p_site uuid)
  RETURNS TABLE (account_id uuid, provider text, merchant_account_ref text, currency text, mode text)
  LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE acct record;
BEGIN
  SELECT * INTO acct FROM iam_v2.payment_provider_accounts
   WHERE tenant_id = p_tenant AND site_id = p_site AND status = 'ACTIVE' AND is_default LIMIT 1;
  IF acct.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_NO_CONFIGURED_ACCOUNT' USING ERRCODE = 'no_data_found';
  END IF;
  account_id := acct.id; provider := acct.provider; merchant_account_ref := acct.merchant_account_ref;
  currency := acct.currency; mode := acct.mode;
  RETURN NEXT;
END $fn$;

-- The identity gate enforces an ACTIVE account for a new CHARGE only. Provider-originated evidence about a
-- charge that already happened (a refund or chargeback) must be recordable even after the account was
-- disabled; provider and currency are still checked for every row.
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
  IF NEW.transaction_type = 'CHARGE' AND acct.status <> 'ACTIVE' THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_NOT_ACTIVE: merchant account % is %; a disabled account cannot take '
                    'money', acct.id, acct.status USING ERRCODE = 'check_violation';
  END IF;
  IF acct.currency IS NOT NULL AND acct.currency <> NEW.currency THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_CURRENCY: account % settles in %, this payment is in %. No conversion '
                    'is performed', acct.id, acct.currency, NEW.currency USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 2. The hosted checkout beside its transaction.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.payment_checkouts (
  transaction_id       uuid PRIMARY KEY REFERENCES iam_v2.payment_transactions (id),
  tenant_id            uuid NOT NULL,
  site_id              uuid NOT NULL,
  provider_session_ref text CHECK (provider_session_ref IS NULL OR (length(provider_session_ref) BETWEEN 1 AND 300)),
  redirect_url         text CHECK (redirect_url IS NULL OR (length(redirect_url) <= 2048 AND redirect_url ~ '^https://')),
  expires_at           timestamptz NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  creation_outcome     text NOT NULL DEFAULT 'PENDING' CHECK (creation_outcome IN ('PENDING','CREATED','NOT_CREATED','AMBIGUOUS')),
  last_status_at       timestamptz,
  last_state           text,
  status_checks        integer NOT NULL DEFAULT 0 CHECK (status_checks >= 0),
  consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0)
);
CREATE INDEX IF NOT EXISTS payment_checkouts_open
  ON iam_v2.payment_checkouts (tenant_id, site_id, last_status_at NULLS FIRST);

CREATE OR REPLACE FUNCTION iam_v2.payment_checkouts_guard() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'payment_checkouts rows are never deleted' USING ERRCODE = 'restrict_violation';
  END IF;
  IF OLD.provider_session_ref IS NOT NULL AND NEW.provider_session_ref IS DISTINCT FROM OLD.provider_session_ref THEN
    RAISE EXCEPTION 'PAYMENT_SESSION_REF_IMMUTABLE: a checkout keeps the provider reference it was created with'
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.transaction_id <> OLD.transaction_id OR NEW.tenant_id <> OLD.tenant_id OR NEW.site_id <> OLD.site_id
     OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'payment_checkouts identity is immutable' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.payment_checkouts_guard() FROM PUBLIC;
DROP TRIGGER IF EXISTS payment_checkouts_guard ON iam_v2.payment_checkouts;
CREATE TRIGGER payment_checkouts_guard BEFORE UPDATE OR DELETE ON iam_v2.payment_checkouts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.payment_checkouts_guard();

-- ---------------------------------------------------------------------------------------------------------
-- 3. Provider-originated refunds and chargebacks: recorded, never initiated.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_record_provider_reversal(
    p_tenant uuid, p_site uuid, p_parent uuid, p_kind text, p_amount_minor bigint,
    p_provider_event_id text, p_provider_txn_ref text)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE par record; v_key text; v_ref text; v_id uuid; v_result text;
BEGIN
  IF p_kind NOT IN ('REFUND','CHARGEBACK') THEN
    RAISE EXCEPTION 'REVERSAL_KIND: %', p_kind USING ERRCODE = 'check_violation';
  END IF;
  IF p_amount_minor IS NULL OR p_amount_minor <= 0 THEN
    RAISE EXCEPTION 'REVERSAL_AMOUNT: must be positive' USING ERRCODE = 'check_violation';
  END IF;
  IF p_provider_event_id IS NULL OR btrim(p_provider_event_id) = '' THEN
    RAISE EXCEPTION 'REVERSAL_EVENT_ID: the provider event id is the deduplication key' USING ERRCODE = 'check_violation';
  END IF;
  SELECT * INTO par FROM iam_v2.payment_transactions
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_parent AND transaction_type = 'CHARGE' FOR UPDATE;
  IF par.id IS NULL OR par.status <> 'CAPTURED' THEN
    RAISE EXCEPTION 'REVERSAL_PARENT: a reversal belongs to a captured charge' USING ERRCODE = 'check_violation';
  END IF;
  v_key := left('provider-reversal:' || p_provider_event_id, 200);
  IF EXISTS (SELECT 1 FROM iam_v2.payment_transactions WHERE tenant_id = p_tenant AND idempotency_key = v_key) THEN
    RETURN 'DUPLICATE';
  END IF;
  v_ref := left('rev_' || md5(v_key), 200);
  INSERT INTO iam_v2.payment_transactions
    (tenant_id, site_id, settlement_id, merchant_account_id, transaction_type, parent_transaction_id,
     provider, provider_ref, idempotency_key, amount_minor, currency, currency_exponent, status)
  VALUES (p_tenant, p_site, par.settlement_id, par.merchant_account_id, p_kind, par.id,
          par.provider, v_ref, v_key, p_amount_minor, par.currency, par.currency_exponent, 'CREATED')
  RETURNING id INTO v_id;
  UPDATE iam_v2.payment_transactions SET status = 'PENDING' WHERE id = v_id;
  SELECT iam_v2.apply_payment_callback_v2(p_tenant, par.provider, par.merchant_account_id, v_ref,
           left(p_provider_event_id, 200), p_kind, 'CAPTURED', p_provider_txn_ref,
           jsonb_build_object('source','provider_status_query','kind',p_kind))
    INTO v_result;
  RETURN v_result;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. The purchase follows a failed or reviewed settlement.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p4_purchase_follows_settlement() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
BEGIN
  IF NEW.status = OLD.status THEN
    RETURN NEW;
  END IF;
  IF NEW.status = 'FAILED' THEN
    UPDATE iam_v2.purchases SET state = 'FAILED'
     WHERE id = NEW.purchase_id AND state IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW');
  ELSIF NEW.status = 'MANUAL_REVIEW' THEN
    UPDATE iam_v2.purchases SET state = 'MANUAL_REVIEW'
     WHERE id = NEW.purchase_id AND state IN ('PENDING','AWAITING_SETTLEMENT');
  END IF;
  RETURN NEW;
END $fn$;
REVOKE ALL ON FUNCTION iam_v2.p4_purchase_follows_settlement() FROM PUBLIC;
DROP TRIGGER IF EXISTS p4_purchase_follows_settlement ON iam_v2.settlements;
CREATE TRIGGER p4_purchase_follows_settlement AFTER UPDATE OF status ON iam_v2.settlements
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_purchase_follows_settlement();

-- ---------------------------------------------------------------------------------------------------------
-- 5. Extra payment domains a Site Admin may add to the pre-sign-in walled garden (bounded and audited).
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.site_payment_domains (
  tenant_id      uuid NOT NULL,
  site_id        uuid NOT NULL,
  domains        text[] NOT NULL DEFAULT '{}',
  config_version bigint NOT NULL DEFAULT 1,
  updated_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, site_id),
  CONSTRAINT site_payment_domains_bounded CHECK (cardinality(domains) <= 20)
);
CREATE TABLE IF NOT EXISTS iam_v2.site_payment_domain_changes (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL,
  site_id     uuid NOT NULL,
  changed_at  timestamptz NOT NULL DEFAULT now(),
  changed_by  text NOT NULL CHECK (length(btrim(changed_by)) > 0),
  change_reason text,
  old_domains text[],
  new_domains text[] NOT NULL
);
DROP TRIGGER IF EXISTS spdc_append_only ON iam_v2.site_payment_domain_changes;
CREATE TRIGGER spdc_append_only BEFORE UPDATE OR DELETE ON iam_v2.site_payment_domain_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_append_only_refuse();

-- A payment domain is a lower-case FQDN with at least two labels, optionally ONE leading "*." wildcard,
-- never an IP address, a CIDR, a bare TLD or a public-suffix-only name. Validated here and in Go.
CREATE OR REPLACE FUNCTION iam_v2.valid_payment_domain(p text) RETURNS boolean
  LANGUAGE sql IMMUTABLE AS $fn$
  SELECT p IS NOT NULL
     AND length(p) BETWEEN 4 AND 253
     AND p ~ '^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$'
     AND p !~ '^(\*\.)?[a-z]{2,63}$'
     AND p !~ '^(\*\.)?(com|net|org|co\.[a-z]{2}|com\.[a-z]{2}|gov\.[a-z]{2}|org\.[a-z]{2}|ac\.[a-z]{2})$'
     AND p !~ '^[0-9.]+$';
$fn$;

CREATE OR REPLACE FUNCTION iam_v2.site_payment_domains_set(
    p_tenant uuid, p_site uuid, p_domains text[], p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_old text[]; v_version bigint; d text;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_domains IS NULL OR cardinality(p_domains) > 20 THEN
    RAISE EXCEPTION 'at most 20 payment domains' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  FOREACH d IN ARRAY p_domains LOOP
    IF NOT iam_v2.valid_payment_domain(d) THEN
      RAISE EXCEPTION 'PAYMENT_DOMAIN_INVALID: %', d USING ERRCODE = 'invalid_parameter_value';
    END IF;
  END LOOP;
  PERFORM pg_advisory_xact_lock(hashtext('site_payment_domains'), hashtext(p_site::text));
  SELECT domains INTO v_old FROM iam_v2.site_payment_domains
   WHERE tenant_id = p_tenant AND site_id = p_site FOR UPDATE;
  INSERT INTO iam_v2.site_payment_domains AS s (tenant_id, site_id, domains)
  VALUES (p_tenant, p_site, p_domains)
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET domains = EXCLUDED.domains, config_version = s.config_version + 1, updated_at = now()
  RETURNING s.config_version INTO v_version;
  INSERT INTO iam_v2.site_payment_domain_changes (tenant_id, site_id, changed_by, change_reason, old_domains, new_domains)
  VALUES (p_tenant, p_site, btrim(p_operator), NULLIF(btrim(coalesce(p_reason,'')),''), v_old, p_domains);
  RETURN v_version;
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 6. The legacy plaintext Stripe credential path is removed.
-- ---------------------------------------------------------------------------------------------------------
DROP TABLE IF EXISTS public.stripe_accounts CASCADE;

-- ---------------------------------------------------------------------------------------------------------
-- 7. Privileges. scd owns the payment key: it seals credentials and runs checkouts and reconciliation. The
--    money-moving operations stay with the payment roles; edged only reads.
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON iam_v2.payment_provider_secret_generations FROM PUBLIC;
REVOKE ALL ON iam_v2.payment_provider_account_changes FROM PUBLIC;
REVOKE ALL ON iam_v2.payment_checkouts FROM PUBLIC;
REVOKE ALL ON iam_v2.site_payment_domains FROM PUBLIC;
REVOKE ALL ON iam_v2.site_payment_domain_changes FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.payment_account_save(uuid,uuid,uuid,text,text,text,text,text,text,boolean,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.payment_account_set_secret(uuid,uuid,uuid,uuid,bytea,bytea,text,smallint,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_resolve_payment_account_v2(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_record_provider_reversal(uuid,uuid,uuid,text,bigint,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.site_payment_domains_set(uuid,uuid,text[],text,text) FROM PUBLIC;

GRANT EXECUTE ON FUNCTION iam_v2.p4_resolve_payment_account_v2(uuid,uuid) TO sc_payment_runtime;
GRANT SELECT, INSERT ON iam_v2.payment_checkouts TO sc_payment_runtime;
GRANT UPDATE (provider_session_ref, redirect_url, creation_outcome, last_status_at, last_state, status_checks,
              consecutive_failures) ON iam_v2.payment_checkouts TO sc_payment_runtime;
GRANT SELECT ON iam_v2.payment_provider_accounts, iam_v2.payment_provider_secret_generations TO sc_payment_runtime;
GRANT EXECUTE ON FUNCTION iam_v2.p4_record_provider_reversal(uuid,uuid,uuid,text,bigint,text,text) TO sc_payment_outcome;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.payment_account_save(uuid,uuid,uuid,text,text,text,text,text,text,boolean,text,text) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.payment_account_set_secret(uuid,uuid,uuid,uuid,bytea,bytea,text,smallint,text) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.site_payment_domains_set(uuid,uuid,text[],text,text) TO svc_scd;
    GRANT SELECT ON iam_v2.payment_provider_accounts, iam_v2.payment_provider_secret_generations,
                    iam_v2.site_payment_domains, iam_v2.payment_checkouts TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.payment_provider_accounts, iam_v2.payment_provider_account_changes,
                    iam_v2.site_payment_domains, iam_v2.site_payment_domain_changes, iam_v2.payment_checkouts TO svc_edged;
  END IF;
END
$grant$;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.payment_provider_secret_generations OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.payment_provider_account_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.payment_checkouts OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.site_payment_domains OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.site_payment_domain_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_append_only_refuse() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.payment_account_save(uuid,uuid,uuid,text,text,text,text,text,text,boolean,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.payment_account_set_secret(uuid,uuid,uuid,uuid,bytea,bytea,text,smallint,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_resolve_payment_account_v2(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_payment_identity_gate() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.payment_checkouts_guard() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_record_provider_reversal(uuid,uuid,uuid,text,bigint,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p4_purchase_follows_settlement() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.valid_payment_domain(text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.site_payment_domains_set(uuid,uuid,text[],text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

COMMIT;
