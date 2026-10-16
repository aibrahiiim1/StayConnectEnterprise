-- 0121 -- a card purchase never strands, and a payment account keeps its identity while checkouts are open
-- (OneGate audit remediation, stream 4: O-2', O-13).
--
--   * A purchase AWAITING_SETTLEMENT whose REQUIRED ONLINE_PAYMENT settlement never got a payment transaction
--     -- the intent was refused (a currency the account cannot settle, no account, a provider that refused
--     before anything existed) -- used to wait for ever. It held the stay's one purchase of that revision
--     (purchase_once_per_stay), so the package vanished from the guest's offers for the rest of the stay.
--     p4_fail_unstarted_card_purchases moves such a settlement through the approved state machine
--     (REQUIRED -> IN_PROGRESS -> FAILED; the purchase follows by p4_purchase_follows_settlement) once it is
--     older than the checkout expiry plus the reconcile grace, and records each one, append-only, in
--     card_purchase_sweeps. A settlement with ANY payment transaction is never touched: from the moment a
--     transaction exists, the provider is the only authority on what happened, and the reconciler owns it.
--   * Changing an account's provider, merchant reference, mode or currency, or rotating its credentials, while
--     one of its card checkouts is still open (CREATED or PENDING) is refused: the reconciler asks the
--     provider about an open checkout with the account's CURRENT identity, so an in-place change made a paid
--     checkout look "not found" and it was marked FAILED. Wait for the checkouts to finish, or add a new
--     account.
--
-- Nothing here creates, retries or reverses money, and no historical row is changed. Additive; the .down drops
-- what this adds.
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The stranded-purchase sweep.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.card_purchase_sweeps (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL,
  site_id       uuid NOT NULL,
  purchase_id   uuid NOT NULL,
  settlement_id uuid NOT NULL UNIQUE,
  swept_at      timestamptz NOT NULL DEFAULT now(),
  reason        text NOT NULL CHECK (reason IN ('NO_PAYMENT_TRANSACTION')),
  waited_seconds integer NOT NULL CHECK (waited_seconds > 0)
);
COMMENT ON TABLE iam_v2.card_purchase_sweeps IS
  'Card purchases failed because no payment transaction was ever created for them (0121). Append-only.';

CREATE OR REPLACE FUNCTION iam_v2.card_purchase_sweeps_append_only() RETURNS trigger
  LANGUAGE plpgsql SET search_path = iam_v2, pg_temp AS $f$
BEGIN
  RAISE EXCEPTION 'card_purchase_sweeps is append-only' USING ERRCODE = 'insufficient_privilege';
END $f$;
DROP TRIGGER IF EXISTS card_purchase_sweeps_append_only ON iam_v2.card_purchase_sweeps;
CREATE TRIGGER card_purchase_sweeps_append_only BEFORE UPDATE OR DELETE ON iam_v2.card_purchase_sweeps
  FOR EACH ROW EXECUTE FUNCTION iam_v2.card_purchase_sweeps_append_only();

-- p_older_than_seconds is the checkout expiry plus the reconcile grace (scd passes the site's card payment
-- settings); it is bounded so a caller cannot sweep a purchase the client may still be paying for.
CREATE OR REPLACE FUNCTION iam_v2.p4_fail_unstarted_card_purchases(
    p_tenant uuid, p_site uuid, p_older_than_seconds integer)
  RETURNS integer
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
DECLARE r record; v_n integer := 0;
BEGIN
  IF p_tenant IS NULL OR p_site IS NULL THEN
    RAISE EXCEPTION 'CARD_SWEEP_SCOPE: tenant and site are required' USING ERRCODE = 'check_violation';
  END IF;
  IF p_older_than_seconds IS NULL OR p_older_than_seconds < 600 OR p_older_than_seconds > 7 * 86400 THEN
    RAISE EXCEPTION 'CARD_SWEEP_AGE: % s is outside 600 s .. 7 days', p_older_than_seconds
      USING ERRCODE = 'check_violation';
  END IF;
  FOR r IN
    SELECT se.id AS settlement_id, pu.id AS purchase_id,
           GREATEST(1, extract(epoch FROM now() - q.consumed_at)::integer) AS waited
      FROM iam_v2.settlements se
      JOIN iam_v2.purchases pu
        ON pu.tenant_id = se.tenant_id AND pu.site_id = se.site_id AND pu.id = se.purchase_id
      JOIN iam_v2.offer_quotes q ON q.id = pu.offer_quote_id
     WHERE se.tenant_id = p_tenant AND se.site_id = p_site
       AND se.method = 'ONLINE_PAYMENT' AND se.status = 'REQUIRED'
       AND pu.state = 'AWAITING_SETTLEMENT'
       AND q.consumed_at IS NOT NULL
       AND q.consumed_at < now() - make_interval(secs => p_older_than_seconds)
       AND NOT EXISTS (SELECT 1 FROM iam_v2.payment_transactions pt WHERE pt.settlement_id = se.id)
     ORDER BY q.consumed_at
     LIMIT 200
     FOR UPDATE OF se SKIP LOCKED
  LOOP
    -- Re-checked under the row lock: a transaction created between the scan and the lock wins.
    IF EXISTS (SELECT 1 FROM iam_v2.payment_transactions pt WHERE pt.settlement_id = r.settlement_id) THEN
      CONTINUE;
    END IF;
    UPDATE iam_v2.settlements SET status = 'IN_PROGRESS' WHERE id = r.settlement_id AND status = 'REQUIRED';
    UPDATE iam_v2.settlements SET status = 'FAILED' WHERE id = r.settlement_id AND status = 'IN_PROGRESS';
    INSERT INTO iam_v2.card_purchase_sweeps (tenant_id, site_id, purchase_id, settlement_id, reason, waited_seconds)
    VALUES (p_tenant, p_site, r.purchase_id, r.settlement_id, 'NO_PAYMENT_TRANSACTION', r.waited);
    v_n := v_n + 1;
  END LOOP;
  RETURN v_n;
END $f$;
REVOKE ALL ON FUNCTION iam_v2.p4_fail_unstarted_card_purchases(uuid, uuid, integer) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 2. A payment account keeps its identity while its checkouts are open.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.payment_account_has_open_checkouts(p_account uuid) RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
  SELECT EXISTS (SELECT 1 FROM iam_v2.payment_transactions pt
                  WHERE pt.merchant_account_id = p_account AND pt.transaction_type = 'CHARGE'
                    AND pt.status IN ('CREATED','PENDING'))
$f$;
REVOKE ALL ON FUNCTION iam_v2.payment_account_has_open_checkouts(uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION iam_v2.payment_account_identity_guard() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
BEGIN
  IF (NEW.provider, NEW.merchant_account_ref, NEW.mode, NEW.currency)
     IS DISTINCT FROM (OLD.provider, OLD.merchant_account_ref, OLD.mode, OLD.currency)
     AND iam_v2.payment_account_has_open_checkouts(OLD.id) THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_CHECKOUTS_OPEN: this account has card checkouts in progress, so its provider, merchant, mode and currency cannot change until they finish. Try again later, or add a new account.'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $f$;
REVOKE ALL ON FUNCTION iam_v2.payment_account_identity_guard() FROM PUBLIC;
DROP TRIGGER IF EXISTS payment_account_identity_guard ON iam_v2.payment_provider_accounts;
CREATE TRIGGER payment_account_identity_guard BEFORE UPDATE ON iam_v2.payment_provider_accounts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.payment_account_identity_guard();

CREATE OR REPLACE FUNCTION iam_v2.payment_account_secret_guard() RETURNS trigger
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $f$
BEGIN
  IF iam_v2.payment_account_has_open_checkouts(NEW.account_id) THEN
    RAISE EXCEPTION 'PAYMENT_ACCOUNT_CHECKOUTS_OPEN: this account has card checkouts in progress, so its credentials cannot change until they finish. Try again later, or add a new account.'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $f$;
REVOKE ALL ON FUNCTION iam_v2.payment_account_secret_guard() FROM PUBLIC;
DROP TRIGGER IF EXISTS payment_account_secret_guard ON iam_v2.payment_provider_secret_generations;
CREATE TRIGGER payment_account_secret_guard BEFORE INSERT ON iam_v2.payment_provider_secret_generations
  FOR EACH ROW EXECUTE FUNCTION iam_v2.payment_account_secret_guard();

-- ---------------------------------------------------------------------------------------------------------
-- Grants. The sweep is a provider-independent OUTCOME (nothing exists at the provider), applied through the
-- outcome credential that records every other card outcome. edged reads the sweep record for the console.
-- ---------------------------------------------------------------------------------------------------------
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_payment_outcome') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p4_fail_unstarted_card_purchases(uuid, uuid, integer) TO sc_payment_outcome;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT ON iam_v2.card_purchase_sweeps TO svc_edged;
  END IF;
END $g$;

COMMIT;
