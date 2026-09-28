-- RETIRE public.stripe_accounts ON A LIVE APPLIANCE (docs/architecture/ONEGATE_MODULES_AND_ACQUISITION.md §6.3).
--
-- The table stored provider secrets in clear text and nothing has read it since the card payment rework
-- (migration 0096). Migration 0096 drops it only when the applying role owns it, because the live-site
-- migration runner applies as iam_v2_owner and may not change public-schema structure. On a live appliance the
-- table is owned by the database owner, so it is retired here, by that owner, as one explicit step.
--
-- Run as the table owner, after 0096 is applied:
--   docker exec -i stayconnect-pg psql -X -v ON_ERROR_STOP=1 -U stayconnect -d stayconnect_site \
--     < deploy/scripts/retire-legacy-stripe-accounts.sql
--
-- Idempotent. Refuses while a row exists: a stored credential is the operator's to delete, knowingly.
\set ON_ERROR_STOP 1
BEGIN;
DO $retire$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '0096_a_card_is_paid_on_the_providers_page') THEN
    RAISE EXCEPTION 'RETIRE_REFUSED: migration 0096 is not applied; the replacement card payment path is not in place';
  END IF;
  IF to_regclass('public.stripe_accounts') IS NULL THEN
    RAISE NOTICE 'public.stripe_accounts is already retired';
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM public.stripe_accounts) THEN
    RAISE EXCEPTION 'RETIRE_REFUSED: public.stripe_accounts still holds a stored credential; delete it deliberately first';
  END IF;
  EXECUTE 'DROP TABLE public.stripe_accounts CASCADE';
  RAISE NOTICE 'public.stripe_accounts retired';
END $retire$;
COMMIT;
