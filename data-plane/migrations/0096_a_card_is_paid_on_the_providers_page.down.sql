-- Roll back 0096. Refuses while any hosted checkout or sealed credential exists: dropping them would erase the
-- evidence of card payments that may already have moved money. public.stripe_accounts is recreated EMPTY with
-- its original shape (its plaintext rows are not restored; there were none in use).

BEGIN;

DO $guard$
BEGIN
  IF EXISTS (SELECT 1 FROM iam_v2.payment_checkouts) OR EXISTS (SELECT 1 FROM iam_v2.payment_provider_secret_generations) THEN
    RAISE EXCEPTION '0096 down refused: card payment records exist';
  END IF;
END $guard$;

DROP TRIGGER IF EXISTS p4_purchase_follows_settlement ON iam_v2.settlements;
DROP FUNCTION IF EXISTS iam_v2.p4_purchase_follows_settlement();
DROP FUNCTION IF EXISTS iam_v2.p4_record_provider_reversal(uuid,uuid,uuid,text,bigint,text,text);
DROP FUNCTION IF EXISTS iam_v2.card_payment_settings_set(uuid,uuid,integer,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.card_payment_settings_get(uuid,uuid);
DROP TABLE IF EXISTS iam_v2.site_card_payment_setting_changes;
DROP TABLE IF EXISTS iam_v2.site_card_payment_settings;
DROP FUNCTION IF EXISTS iam_v2.site_payment_domains_set(uuid,uuid,text[],text,text);
DROP FUNCTION IF EXISTS iam_v2.valid_payment_domain(text);
DROP TABLE IF EXISTS iam_v2.site_payment_domain_changes;
DROP TABLE IF EXISTS iam_v2.site_payment_domains;
DROP TABLE IF EXISTS iam_v2.payment_checkouts;
DROP FUNCTION IF EXISTS iam_v2.payment_checkouts_guard();
DROP FUNCTION IF EXISTS iam_v2.p4_resolve_payment_account_v2(uuid,uuid);
DROP FUNCTION IF EXISTS iam_v2.payment_account_set_secret(uuid,uuid,uuid,uuid,bytea,bytea,text,smallint,text);
DROP FUNCTION IF EXISTS iam_v2.payment_account_save(uuid,uuid,uuid,text,text,text,text,text,text,boolean,text,text);
DROP TABLE IF EXISTS iam_v2.payment_provider_account_changes;
DROP TABLE IF EXISTS iam_v2.payment_provider_secret_generations;
DROP FUNCTION IF EXISTS iam_v2.p4_append_only_refuse();
ALTER TABLE iam_v2.payment_provider_accounts DROP CONSTRAINT IF EXISTS ppa_mode_known;
ALTER TABLE iam_v2.payment_provider_accounts DROP COLUMN IF EXISTS mode;

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

CREATE TABLE IF NOT EXISTS public.stripe_accounts (
    id uuid DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    enabled boolean DEFAULT true NOT NULL,
    display_name text,
    publishable_key text NOT NULL,
    secret_key text NOT NULL,
    webhook_secret text NOT NULL,
    success_url text NOT NULL,
    cancel_url text NOT NULL,
    last_success_at timestamp with time zone,
    last_error text,
    last_error_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS stripe_accounts_tenant_enabled_idx ON public.stripe_accounts (tenant_id) WHERE enabled;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.p4_payment_identity_gate() OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- Restore the pre-0096 PUBLIC execute on the nine functions 0096 narrowed.
GRANT EXECUTE ON FUNCTION iam_v2.p5_controlled_operation_open(text) TO PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_get(uuid,uuid,uuid) TO PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer) TO PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.pms_dispose_snapshot_cases(uuid,uuid,uuid,text,text) TO PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_get(uuid,uuid) TO PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.pms_reconciliation_settings_set(uuid,uuid,integer,integer,text,text,integer,integer) TO PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.pms_record_resync_coverage(uuid,uuid,uuid,bigint,text[],integer,integer,integer) TO PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.pms_roster_of_generation(uuid,uuid,uuid,bigint) TO PUBLIC;
GRANT EXECUTE ON FUNCTION iam_v2.pms_site_blocked_after_refusals(uuid,uuid) TO PUBLIC;

COMMIT;
