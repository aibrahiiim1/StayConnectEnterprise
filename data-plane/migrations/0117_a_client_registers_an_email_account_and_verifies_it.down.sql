-- 0117 down. Removing Email accounts.
--
-- WHAT THIS COSTS. Self-registered accounts have no meaning to the previous software: they are DISABLED (not
-- deleted -- their entitlements, purchases and sessions still reference them) before the columns that mark them
-- go, so they cannot quietly become staff-style Personal accounts. Outstanding verification and reset links are
-- dropped with their table, as are the settings and their history; take a copy first if either is wanted.
--
-- THE UNIQUENESS SCOPE NARROWS BACK to (tenant, lower(username)). If the same username now exists at two sites of
-- one tenant that cannot be represented under the old rule, and the rollback stops with the offending count
-- rather than deleting anybody's account.
--
-- tenants.auth_methods: "email_account" becomes "email" with the same enabled state, when the applying role may
-- write public.tenants (otherwise a NOTICE; the reverse of deploy/scripts/convert-email-method-to-email-account.sql
-- is the same statement below, run as the owner).

BEGIN;

DO $dup$
DECLARE n int;
BEGIN
  SELECT count(*) INTO n FROM (
    SELECT 1 FROM iam_v2.guest_access_accounts GROUP BY tenant_id, lower(username) HAVING count(*) > 1) d;
  IF n > 0 THEN
    RAISE EXCEPTION '0117 down refused: % username(s) exist at more than one site of a tenant; the tenant-wide '
                    'uniqueness of the previous software cannot hold them', n
      USING ERRCODE = 'unique_violation';
  END IF;
END $dup$;

DO $am$
BEGIN
  IF NOT (has_table_privilege(current_user, 'public.tenants', 'SELECT')
          AND has_table_privilege(current_user, 'public.tenants', 'UPDATE')) THEN
    RAISE NOTICE 'public.tenants is not writable by %; convert email_account back to email as the table owner', current_user;
    RETURN;
  END IF;
  UPDATE public.tenants t
     SET auth_methods = (CASE WHEN t.auth_methods ? 'email' THEN t.auth_methods - 'email_account'
                              ELSE (t.auth_methods - 'email_account') || jsonb_build_object('email',
                                     jsonb_build_object('enabled',
                                       COALESCE(CASE WHEN jsonb_typeof(t.auth_methods->'email_account'->'enabled') = 'boolean'
                                                     THEN (t.auth_methods->'email_account'->>'enabled')::boolean END, false)))
                         END)
   WHERE t.auth_methods ? 'email_account';
  UPDATE public.tenants t
     SET auth_methods = jsonb_set(t.auth_methods, '{portal,primary_method}', '"email"'::jsonb)
   WHERE t.auth_methods->'portal'->>'primary_method' = 'email_account';
END $am$;

DROP FUNCTION IF EXISTS iam_v2.self_service_settings_set(uuid,uuid,boolean,integer,text,integer,integer,integer,integer,integer,integer,text,text);
DROP FUNCTION IF EXISTS iam_v2.self_service_settings_get(uuid,uuid);
DROP TRIGGER IF EXISTS self_service_settings_changes_append_only ON iam_v2.self_service_settings_changes;
DROP TABLE IF EXISTS iam_v2.self_service_settings_changes;
DROP FUNCTION IF EXISTS iam_v2.self_service_settings_changes_append_only();
DROP TABLE IF EXISTS iam_v2.site_self_service_settings;

DROP TABLE IF EXISTS iam_v2.account_email_tokens;

DO $dis$
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'iam_v2'
                AND table_name = 'guest_access_accounts' AND column_name = 'self_registered') THEN
    UPDATE iam_v2.guest_access_accounts SET enabled = false WHERE self_registered AND enabled;
  END IF;
END $dis$;

-- The column-level UPDATE grants go with their columns; the table-level INSERT is revoked explicitly.
DO $rv$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    REVOKE INSERT ON iam_v2.guest_access_accounts FROM svc_scd;
  END IF;
END $rv$;

DROP INDEX IF EXISTS iam_v2.gaa_unverified_self_registered;
DROP INDEX IF EXISTS iam_v2.gaa_self_registered_device;
DROP INDEX IF EXISTS iam_v2.gaa_username;
CREATE UNIQUE INDEX gaa_username ON iam_v2.guest_access_accounts (tenant_id, lower(username));

ALTER TABLE iam_v2.guest_access_accounts
  DROP CONSTRAINT IF EXISTS gaa_self_registered_username_is_email,
  DROP CONSTRAINT IF EXISTS gaa_grace_extensions_used_bounds,
  DROP COLUMN IF EXISTS created_by_device_mac,
  DROP COLUMN IF EXISTS grace_extensions_used,
  DROP COLUMN IF EXISTS grace_until,
  DROP COLUMN IF EXISTS verification_reminder_sent_at,
  DROP COLUMN IF EXISTS verification_deadline,
  DROP COLUMN IF EXISTS email_verified_at,
  DROP COLUMN IF EXISTS self_registered;

COMMIT;
