-- 0112 -- A CARD PROVIDER MAY CONVERT, AND WHAT IT DID IS RECORDED.
--
-- Product-Owner decision of 2026-10-11 (PR #211): a Client sees and pays a package's price in the package's own
-- currency (PRESENTMENT); where the provider and the merchant account support it, the PROVIDER converts into
-- the account's SETTLEMENT currency. OneGate never invents a rate and performs no local FX: the transaction
-- keeps the presentment currency and amount exactly as quoted, and the provider's own report of what it
-- settled -- currency, gross, rate, fee, net, its balance-transaction reference -- is recorded beside it,
-- read from the authoritative status query.
--
-- WHAT CHANGES IN THE CONTRACT, AND ONLY THIS:
--   * An account may be marked PROVIDER-CONVERTS by a definer setter, audited in its own append-only log, and
--     only for a provider that converts (Stripe). Paymob settles in the merchant's currency and is refused.
--   * The payment identity gate (0018/0096) still refuses a charge in a currency the account does not settle
--     -- UNLESS the account is marked provider-converts. A reversal (refund, chargeback) of a charge is always
--     recorded in that charge's own currency, as before.
--   * A settlement report table, written once per captured charge by the outcome authority.
-- PMS posting is untouched: a room charge still requires the package currency to equal the pinned
-- interface's, with no conversion of any kind.
--
-- Additive: two tables, one log, two definer functions, one trigger function replaced. No row is touched.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.payment_account_conversion (
  account_id         uuid PRIMARY KEY REFERENCES iam_v2.payment_provider_accounts (id),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  provider_converts  boolean NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now(),
  changed_by         text NOT NULL CHECK (length(btrim(changed_by)) > 0)
);
CREATE TABLE IF NOT EXISTS iam_v2.payment_account_conversion_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  account_id         uuid NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now(),
  changed_by         text NOT NULL CHECK (length(btrim(changed_by)) > 0),
  change_reason      text,
  old_value          boolean,
  new_value          boolean NOT NULL
);
DROP TRIGGER IF EXISTS pacc_append_only ON iam_v2.payment_account_conversion_changes;
CREATE TRIGGER pacc_append_only BEFORE UPDATE OR DELETE ON iam_v2.payment_account_conversion_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_append_only_refuse();

CREATE OR REPLACE FUNCTION iam_v2.payment_account_set_conversion(
    p_tenant uuid, p_site uuid, p_account uuid, p_converts boolean, p_operator text, p_reason text)
  RETURNS boolean
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE acct record; v_old boolean;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO acct FROM iam_v2.payment_provider_accounts WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_account;
  IF acct.id IS NULL THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_UNKNOWN' USING ERRCODE = 'no_data_found';
  END IF;
  -- Only a provider that converts may be told to. Paymob settles in the merchant's currency.
  IF p_converts AND acct.provider <> 'stripe' THEN
    RAISE EXCEPTION 'PAYMENT_CONVERSION_UNSUPPORTED: % does not convert card payments into the account currency',
      acct.provider USING ERRCODE = 'check_violation';
  END IF;
  SELECT provider_converts INTO v_old FROM iam_v2.payment_account_conversion WHERE account_id = p_account FOR UPDATE;
  INSERT INTO iam_v2.payment_account_conversion (account_id, tenant_id, site_id, provider_converts, changed_by)
  VALUES (p_account, p_tenant, p_site, p_converts, btrim(p_operator))
  ON CONFLICT (account_id) DO UPDATE SET provider_converts = EXCLUDED.provider_converts, changed_at = now(),
                                         changed_by = EXCLUDED.changed_by;
  INSERT INTO iam_v2.payment_account_conversion_changes
    (tenant_id, site_id, account_id, changed_by, change_reason, old_value, new_value)
  VALUES (p_tenant, p_site, p_account, btrim(p_operator), NULLIF(btrim(coalesce(p_reason,'')),''), v_old, p_converts);
  RETURN p_converts;
END $fn$;

-- The identity gate, with the one exception above. Every other check is exactly as 0096 left it.
CREATE OR REPLACE FUNCTION iam_v2.p4_payment_identity_gate() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE acct record; v_converts boolean; v_parent_currency text;
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
    IF NEW.parent_transaction_id IS NOT NULL THEN
      -- A reversal is recorded in its charge's currency, whatever the account settles in.
      SELECT currency INTO v_parent_currency FROM iam_v2.payment_transactions WHERE id = NEW.parent_transaction_id;
      IF v_parent_currency IS NOT DISTINCT FROM NEW.currency THEN
        RETURN NEW;
      END IF;
    END IF;
    SELECT provider_converts INTO v_converts FROM iam_v2.payment_account_conversion WHERE account_id = acct.id;
    IF NEW.transaction_type = 'CHARGE' AND coalesce(v_converts, false) AND acct.provider = 'stripe' THEN
      RETURN NEW; -- presented in NEW.currency; the provider converts into acct.currency and reports it
    END IF;
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_CURRENCY: account % settles in %, this payment is in %. No conversion '
                    'is performed', acct.id, acct.currency, NEW.currency USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $fn$;

-- WHAT THE PROVIDER SETTLED, as the provider reported it. Written once per captured charge (a repeat is a no-op).
CREATE TABLE IF NOT EXISTS iam_v2.payment_settlement_reports (
  transaction_id            uuid PRIMARY KEY REFERENCES iam_v2.payment_transactions (id),
  tenant_id                 uuid NOT NULL,
  site_id                   uuid NOT NULL,
  presentment_currency      text NOT NULL CHECK (presentment_currency ~ '^[A-Z]{3}$'),
  presentment_amount_minor  bigint NOT NULL CHECK (presentment_amount_minor > 0),
  settlement_currency       text CHECK (settlement_currency ~ '^[A-Z]{3}$'),
  settlement_amount_minor   bigint,
  exchange_rate             text CHECK (exchange_rate IS NULL OR exchange_rate ~ '^[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$'),
  fee_minor                 bigint,
  net_minor                 bigint,
  provider_balance_ref      text CHECK (provider_balance_ref IS NULL OR length(provider_balance_ref) <= 200),
  converted                 boolean NOT NULL,
  report_status             text NOT NULL CHECK (report_status IN ('REPORTED','NOT_REPORTED')),
  recorded_at               timestamptz NOT NULL DEFAULT now()
);
DROP TRIGGER IF EXISTS psr_append_only ON iam_v2.payment_settlement_reports;
CREATE TRIGGER psr_append_only BEFORE UPDATE OR DELETE ON iam_v2.payment_settlement_reports
  FOR EACH ROW EXECUTE FUNCTION iam_v2.p4_append_only_refuse();
COMMENT ON TABLE iam_v2.payment_settlement_reports IS
  'The provider''s own report of a captured card charge''s settlement (contract §9): presentment as quoted, '
  'settlement currency/amount, exchange rate as the provider stated it (text, never recomputed), fee and net, '
  'and the provider balance-transaction reference. NOT_REPORTED when the provider has not reported it yet.';

CREATE OR REPLACE FUNCTION iam_v2.p4_record_settlement_report(
    p_tenant uuid, p_site uuid, p_transaction uuid, p_settlement_currency text, p_settlement_amount bigint,
    p_exchange_rate text, p_fee bigint, p_net bigint, p_balance_ref text)
  RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE t record; v_status text; v_converted boolean;
BEGIN
  SELECT * INTO t FROM iam_v2.payment_transactions
   WHERE tenant_id = p_tenant AND site_id = p_site AND id = p_transaction AND transaction_type = 'CHARGE';
  IF t.id IS NULL OR t.status <> 'CAPTURED' THEN
    RAISE EXCEPTION 'SETTLEMENT_REPORT_PARENT: a settlement report belongs to a captured charge' USING ERRCODE = 'check_violation';
  END IF;
  v_status := CASE WHEN p_settlement_currency IS NULL OR p_settlement_amount IS NULL THEN 'NOT_REPORTED' ELSE 'REPORTED' END;
  v_converted := v_status = 'REPORTED' AND upper(p_settlement_currency) <> t.currency;
  INSERT INTO iam_v2.payment_settlement_reports
    (transaction_id, tenant_id, site_id, presentment_currency, presentment_amount_minor, settlement_currency,
     settlement_amount_minor, exchange_rate, fee_minor, net_minor, provider_balance_ref, converted, report_status)
  VALUES (t.id, p_tenant, p_site, t.currency, t.amount_minor, upper(p_settlement_currency), p_settlement_amount,
          NULLIF(btrim(coalesce(p_exchange_rate,'')),''), p_fee, p_net, NULLIF(btrim(coalesce(p_balance_ref,'')),''),
          v_converted, v_status)
  ON CONFLICT (transaction_id) DO NOTHING;
  RETURN v_status;
END $fn$;

REVOKE ALL ON iam_v2.payment_account_conversion, iam_v2.payment_account_conversion_changes,
              iam_v2.payment_settlement_reports FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.payment_account_set_conversion(uuid,uuid,uuid,boolean,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.p4_record_settlement_report(uuid,uuid,uuid,text,bigint,text,bigint,bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.p4_record_settlement_report(uuid,uuid,uuid,text,bigint,text,bigint,bigint,text) TO sc_payment_outcome;
GRANT SELECT ON iam_v2.payment_account_conversion TO sc_payment_runtime;

DO $grant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.payment_account_set_conversion(uuid,uuid,uuid,boolean,text,text) TO svc_scd;
    GRANT SELECT ON iam_v2.payment_account_conversion TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.payment_account_conversion, iam_v2.payment_account_conversion_changes,
                    iam_v2.payment_settlement_reports TO svc_edged;
  END IF;
END $grant$;

COMMIT;
