-- 0112 down -- removes provider-managed conversion and restores the 0096 identity gate exactly: a charge in a
-- currency the account does not settle is refused again. Settlement reports already written are dropped with
-- their table (the transactions they describe are untouched).
BEGIN;
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
DROP FUNCTION IF EXISTS iam_v2.p4_record_settlement_report(uuid,uuid,uuid,text,bigint,text,bigint,bigint,text);
DROP FUNCTION IF EXISTS iam_v2.payment_account_set_conversion(uuid,uuid,uuid,boolean,text,text);
DROP TABLE IF EXISTS iam_v2.payment_settlement_reports;
DROP TABLE IF EXISTS iam_v2.payment_account_conversion_changes;
DROP TABLE IF EXISTS iam_v2.payment_account_conversion;
COMMIT;
