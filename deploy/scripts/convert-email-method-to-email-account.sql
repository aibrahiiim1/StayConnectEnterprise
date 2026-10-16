-- CONVERT THE EMAIL-CODE SWITCH TO EMAIL ACCOUNTS ON A LIVE APPLIANCE (migration 0117).
--
-- Migration 0117 rewrites tenants.auth_methods only when the applying role may write public.tenants. The
-- live-site migration runner applies as iam_v2_owner, and public.tenants belongs to the database owner, so on a
-- live appliance the same change is applied here, by that owner, as one explicit step (the 0107 SMTP pattern).
--
--   "email": {"enabled": X, ...}            ->  "email_account": {"enabled": X}
--   portal.mail_providers / mail_domains    ->  removed (they only served reading an email code)
--   portal.primary_method "email"           ->  "email_account"
--
-- Until it runs, scd reads a leftover "email" switch as "email_account" (internal/tenantcfg), so guests see the
-- same answer before and after; running it makes the stored record say what is in force.
--
-- Run as the table owner, after 0117 is applied:
--   docker exec -i stayconnect-pg psql -X -v ON_ERROR_STOP=1 -U stayconnect -d stayconnect_site \
--     < deploy/scripts/convert-email-method-to-email-account.sql
--
-- Idempotent: running it again changes nothing. It touches no privilege and no other column.
\set ON_ERROR_STOP 1
BEGIN;
DO $convert$
DECLARE n int; p int;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM public.schema_migrations
                  WHERE version = '0117_a_client_registers_an_email_account_and_verifies_it') THEN
    RAISE EXCEPTION 'CONVERT_REFUSED: migration 0117 is not applied; the Email accounts software is not in place';
  END IF;
  UPDATE public.tenants t
     SET auth_methods = (CASE WHEN t.auth_methods ? 'email_account' THEN t.auth_methods - 'email'
                              ELSE (t.auth_methods - 'email') || jsonb_build_object('email_account',
                                     jsonb_build_object('enabled',
                                       COALESCE(CASE WHEN jsonb_typeof(t.auth_methods->'email'->'enabled') = 'boolean'
                                                     THEN (t.auth_methods->'email'->>'enabled')::boolean END, false)))
                         END)
   WHERE t.auth_methods ? 'email';
  GET DIAGNOSTICS n = ROW_COUNT;
  UPDATE public.tenants t
     SET auth_methods = jsonb_set(t.auth_methods, '{portal}',
                          ((t.auth_methods->'portal') - 'mail_providers' - 'mail_domains')
                          || CASE WHEN t.auth_methods->'portal'->>'primary_method' = 'email'
                                  THEN '{"primary_method":"email_account"}'::jsonb ELSE '{}'::jsonb END)
   WHERE jsonb_typeof(t.auth_methods->'portal') = 'object'
     AND (t.auth_methods->'portal' ? 'mail_providers' OR t.auth_methods->'portal' ? 'mail_domains'
          OR t.auth_methods->'portal'->>'primary_method' = 'email');
  GET DIAGNOSTICS p = ROW_COUNT;
  RAISE NOTICE 'email switch converted on % tenant row(s); portal settings cleaned on % row(s)', n, p;
END $convert$;
COMMIT;
