-- ZERO-LEGACY FRESH PRODUCTION BASELINE — remove the superseded guest-IAM domain.
--
-- WHY THIS MIGRATION EXISTS
-- -------------------------
-- The Production requirement is PHYSICAL zero-superseded guest IAM, not a superseded implementation that
-- configuration merely cannot select. An unselectable table is still a table: anything holding the privilege
-- can write it, it still has to be backed up, restored, migrated and reasoned about, and the only thing
-- between it and the guest authority is a build flag.
--
-- The runtime implementation went first. By the time this runs, nothing in the product reads or writes any
-- of these tables: guest authentication, sessions, entitlements, vouchers, packages, accounting and
-- enforcement are all the iam_v2 domain, and the operator surfaces were moved onto it.
--
-- WHAT IS REMOVED, AND WHAT REPLACED IT
-- -------------------------------------
--   public.sessions          -> iam_v2.sessions            (+ session_entitlement_bindings, watermarks)
--   public.guests            -> iam_v2.guest_principals    (+ guest_principal_identities)
--   public.guest_accounts    -> iam_v2.guest_access_accounts
--   public.vouchers          -> iam_v2.vouchers
--   public.voucher_batches   -> iam_v2.voucher_batches
--   public.ticket_templates  -> iam_v2.internet_packages   (+ service_plans, package_eligibility_rules)
--   public.payments          -> iam_v2.payment_transactions(+ payment_transaction_events, settlements)
--
-- WHAT IS DELIBERATELY KEPT
-- -------------------------
--   public.auth_otps            the OTP CHALLENGE store. Per the accepted decision (D2) the challenge is
--                               verified upstream and only the verified factor crosses into iam_v2, so this
--                               is delivery scaffolding, not a guest-IAM identity store. Its FK to
--                               ticket_templates is dropped below: pinning an access plan at challenge time
--                               was the superseded coupling, not a property of OTP.
--   public.social_oauth_states  the OAuth HANDSHAKE nonce store, for exactly the same reason.
--   public.social_oauth_providers, public.otp_hmac_key_generations, public.auth_throttle_buckets
--                               provider configuration, key generations and durable throttle state -- all
--                               current, none of them a guest-IAM authority.
--   public.accounting_records   a TimescaleDB hypertable and the historical accounting series. Phase-3
--                               accounting writes iam_v2.accounting_records; this one is retained because
--                               destroying an accounting series is not a schema cleanup.
--
-- HISTORY IS NOT REWRITTEN. Migrations 0001-0048 still create these tables; this one removes them at the end
-- of the chain. An existing installation upgrades through the same ordered path it would have taken anyway,
-- and a factory-clean installation ends with none of them.
\set ON_ERROR_STOP on

-- The OTP challenge and OAuth nonce stores lose their access-plan coupling but keep everything else.
ALTER TABLE public.auth_otps           DROP CONSTRAINT IF EXISTS auth_otps_template_id_fkey;
ALTER TABLE public.auth_otps           DROP COLUMN     IF EXISTS template_id;
ALTER TABLE public.social_oauth_states DROP CONSTRAINT IF EXISTS social_oauth_states_template_id_fkey;
ALTER TABLE public.social_oauth_states DROP COLUMN     IF EXISTS template_id;

-- Order matters only for readability: CASCADE would do it regardless, but naming the dependants first makes
-- the dependency direction explicit to the next reader, and means an unexpected extra dependant shows up as
-- an error here rather than being silently dropped by a CASCADE further down.
DROP TABLE IF EXISTS public.payments;
DROP TABLE IF EXISTS public.sessions;
DROP TABLE IF EXISTS public.guest_accounts;
DROP TABLE IF EXISTS public.vouchers;
DROP TABLE IF EXISTS public.voucher_batches;
DROP TABLE IF EXISTS public.ticket_templates;
DROP TABLE IF EXISTS public.guests;

-- FAIL CLOSED. A cleanup that silently leaves one of them behind is worse than none, because everything
-- downstream -- the verifier, the readiness claim, the operator's mental model -- assumes it worked.
DO $$
DECLARE leftover text;
BEGIN
  SELECT string_agg(tablename, ', ' ORDER BY tablename) INTO leftover
    FROM pg_tables
   WHERE schemaname = 'public'
     AND tablename IN ('sessions','guests','guest_accounts','vouchers','voucher_batches',
                       'ticket_templates','payments');
  IF leftover IS NOT NULL THEN
    RAISE EXCEPTION 'ZERO-LEGACY BLOCKER: superseded guest-IAM table(s) still present: %', leftover;
  END IF;
END $$;

-- And nothing may still POINT at them. A dangling reference would mean the drop above took something with it
-- that the product still needs.
DO $$
DECLARE n int;
BEGIN
  SELECT count(*) INTO n
    FROM information_schema.columns
   WHERE table_schema = 'public'
     AND table_name IN ('auth_otps','social_oauth_states')
     AND column_name = 'template_id';
  IF n > 0 THEN
    RAISE EXCEPTION 'ZERO-LEGACY BLOCKER: % retained table(s) still carry template_id', n;
  END IF;
END $$;
