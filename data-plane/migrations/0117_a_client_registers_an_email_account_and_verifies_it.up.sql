-- 0117 -- A CLIENT REGISTERS AN EMAIL ACCOUNT, AND VERIFIES IT
-- (contract docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md; migration approved by the Product Owner
-- on 2026-10-15).
--
-- Email one-time-code sign-in is replaced by Email accounts: the client registers with an address and a password,
-- is signed in at once, and proves the address later through a single-use emailed link. This migration adds what
-- that needs and nothing else:
--
--   1. iam_v2.guest_access_accounts learns that an account was SELF-REGISTERED, when its address was verified,
--      its verification deadline, the reminder, the one-time grace hour, and the device that created it (the
--      per-device daily registration cap counts by it).
--   2. The username is unique per SITE, not per tenant. A self-service account is site-specific (contract
--      section 2): the same address may hold one account at each of a customer's properties. Widening the
--      uniqueness scope cannot invalidate a row that satisfied the narrower one.
--   3. iam_v2.account_email_tokens: the VERIFY and RESET links, stored as HMAC-SHA256 only, and their outbox
--      state (sent, attempts, next attempt, last error), so registration never waits for a mail server.
--   4. iam_v2.site_self_service_settings + append-only iam_v2.self_service_settings_changes, read and written
--      only through SECURITY DEFINER functions (the 0104 shape): an operational setting with defaults, bounds,
--      a version and a reason for every change (docs/OPERATIONAL_SETTINGS_POLICY.md).
--   5. tenants.auth_methods: the "email" (code) switch becomes "email_account" with the same enabled state; the
--      email-code-only settings (temporary internet, the mail-service allowlist) go with it.
--   6. Least privilege for scd, mirrored in deploy/gatep/svc-self-service-grants.sql (a grant only a migration
--      makes does not survive the Gate-P reconcile).
--
-- Additive and idempotent. Historical rows (verification_access_grants, auth_otps, escrow, reveals) are kept.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The self-service account.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.guest_access_accounts
  ADD COLUMN IF NOT EXISTS self_registered               boolean     NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS email_verified_at             timestamptz,
  ADD COLUMN IF NOT EXISTS verification_deadline         timestamptz,
  ADD COLUMN IF NOT EXISTS verification_reminder_sent_at timestamptz,
  ADD COLUMN IF NOT EXISTS grace_until                   timestamptz,
  ADD COLUMN IF NOT EXISTS grace_extensions_used         integer     NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS created_by_device_mac         macaddr;

DO $c$
BEGIN
  -- The hard maximum of the grace setting is 3 (contract section 6); a counter past it is a defect, not data.
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'gaa_grace_extensions_used_bounds'
                    AND conrelid = 'iam_v2.guest_access_accounts'::regclass) THEN
    ALTER TABLE iam_v2.guest_access_accounts ADD CONSTRAINT gaa_grace_extensions_used_bounds
      CHECK (grace_extensions_used BETWEEN 0 AND 3);
  END IF;
  -- A self-service account's username IS its address, lowercased and trimmed (contract section 2), so the
  -- per-site uniqueness below compares addresses and an emailed link always names the account it belongs to.
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'gaa_self_registered_username_is_email'
                    AND conrelid = 'iam_v2.guest_access_accounts'::regclass) THEN
    ALTER TABLE iam_v2.guest_access_accounts ADD CONSTRAINT gaa_self_registered_username_is_email
      CHECK (NOT self_registered OR (username = lower(btrim(username)) AND position('@' IN username) > 1));
  END IF;
END $c$;

COMMENT ON COLUMN iam_v2.guest_access_accounts.self_registered IS
  'true: the client created this account on the portal (Email accounts, 0117); its username is the address. '
  'false: a Personal account staff created (Client accounts).';
COMMENT ON COLUMN iam_v2.guest_access_accounts.email_verified_at IS
  'When the client proved control of the address (a VERIFY or RESET link). NULL = not verified.';
COMMENT ON COLUMN iam_v2.guest_access_accounts.verification_deadline IS
  'Until when an unverified self-registered account works normally. NULL = no deadline (verification not '
  'required when the account was created).';
COMMENT ON COLUMN iam_v2.guest_access_accounts.grace_until IS
  'End of the one-time grace hour a suspended client asked for with their password. Lifts only the '
  'verification gate; no entitlement is created, extended or restored.';
COMMENT ON COLUMN iam_v2.guest_access_accounts.created_by_device_mac IS
  'The device that registered this self-service account; counted by the per-device daily registration cap.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. Usernames are unique per site (the name gaa_username is kept; its scope widens).
-- ---------------------------------------------------------------------------------------------------------
DROP INDEX IF EXISTS iam_v2.gaa_username;
CREATE UNIQUE INDEX gaa_username ON iam_v2.guest_access_accounts (tenant_id, site_id, lower(username));

CREATE INDEX IF NOT EXISTS gaa_self_registered_device
  ON iam_v2.guest_access_accounts (tenant_id, site_id, created_by_device_mac, created_at)
  WHERE self_registered;
-- The verification sweep's question, every 30 seconds: which self-registered accounts are not verified yet.
CREATE INDEX IF NOT EXISTS gaa_unverified_self_registered
  ON iam_v2.guest_access_accounts (tenant_id, site_id, verification_deadline)
  WHERE self_registered AND email_verified_at IS NULL;

-- ---------------------------------------------------------------------------------------------------------
-- 3. Emailed links and their outbox.
-- ---------------------------------------------------------------------------------------------------------
-- token_hmac is NULL until the first send attempt: scd mints the 256-bit token at the moment it hands the
-- message to the mail server and stores only its HMAC, so the plaintext link exists in exactly one place -- the
-- email -- and never in this database, a backup or a queue. A retry mints a fresh token (the undelivered one is
-- worthless), which is why a row, not a token, is the unit of the outbox.
CREATE TABLE IF NOT EXISTS iam_v2.account_email_tokens (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        uuid NOT NULL,
  site_id          uuid NOT NULL,
  account_id       uuid NOT NULL,
  purpose          text NOT NULL CONSTRAINT aet_purpose CHECK (purpose IN ('VERIFY','RESET')),
  token_hmac       bytea UNIQUE CONSTRAINT aet_token_hmac_len CHECK (token_hmac IS NULL OR length(token_hmac) = 32),
  key_id           text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  used_at          timestamptz,
  superseded_at    timestamptz,
  sent_at          timestamptz,
  send_attempts    integer NOT NULL DEFAULT 0 CONSTRAINT aet_send_attempts CHECK (send_attempts >= 0),
  next_send_at     timestamptz NOT NULL DEFAULT now(),
  last_send_error  text CONSTRAINT aet_last_send_error_len CHECK (last_send_error IS NULL OR length(last_send_error) <= 500),
  CONSTRAINT aet_expiry CHECK (expires_at > created_at),
  CONSTRAINT aet_key_with_hmac CHECK ((token_hmac IS NULL) = (key_id IS NULL)),
  -- A link that was never minted cannot have been sent or used.
  CONSTRAINT aet_sent_has_hmac CHECK (sent_at IS NULL OR token_hmac IS NOT NULL),
  CONSTRAINT aet_used_has_hmac CHECK (used_at IS NULL OR token_hmac IS NOT NULL),
  CONSTRAINT aet_account_fkey FOREIGN KEY (tenant_id, site_id, account_id)
    REFERENCES iam_v2.guest_access_accounts (tenant_id, site_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS aet_account_purpose
  ON iam_v2.account_email_tokens (account_id, purpose, created_at DESC);
CREATE INDEX IF NOT EXISTS aet_outbox
  ON iam_v2.account_email_tokens (next_send_at)
  WHERE sent_at IS NULL AND used_at IS NULL AND superseded_at IS NULL;

COMMENT ON TABLE iam_v2.account_email_tokens IS
  'Single-use VERIFY (until the verification deadline, at least 24 h) and RESET (30 min) links of self-service '
  'accounts (0117). Only HMAC-SHA256 of a token is stored (key derived from the appliance-local resume key, '
  'domain account-email-token:v1). A new token of a purpose supersedes the older unused ones. The row is also '
  'the outbox entry: sent_at, send_attempts, next_send_at, last_send_error.';

-- ---------------------------------------------------------------------------------------------------------
-- 4. The site's self-service settings (operational setting: defaults, bounds, version, reasoned audit).
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.site_self_service_settings (
  tenant_id                        uuid NOT NULL,
  site_id                          uuid NOT NULL,
  verification_required            boolean NOT NULL DEFAULT true,
  verification_deadline_hours      integer NOT NULL DEFAULT 24
    CONSTRAINT ssss_deadline CHECK (verification_deadline_hours BETWEEN 1 AND 168),
  suspension_policy                text    NOT NULL DEFAULT 'SUSPEND'
    CONSTRAINT ssss_policy CHECK (suspension_policy IN ('SUSPEND','WARN_ONLY')),
  reminder_hours_before            integer NOT NULL DEFAULT 4
    CONSTRAINT ssss_reminder CHECK (reminder_hours_before BETWEEN 0 AND 72),
  resend_cooldown_seconds          integer NOT NULL DEFAULT 60
    CONSTRAINT ssss_resend_cooldown CHECK (resend_cooldown_seconds BETWEEN 30 AND 3600),
  resend_max_per_day               integer NOT NULL DEFAULT 5
    CONSTRAINT ssss_resend_max CHECK (resend_max_per_day BETWEEN 1 AND 20),
  grace_minutes                    integer NOT NULL DEFAULT 60
    CONSTRAINT ssss_grace_minutes CHECK (grace_minutes BETWEEN 15 AND 240),
  max_grace_extensions             integer NOT NULL DEFAULT 1
    CONSTRAINT ssss_max_grace CHECK (max_grace_extensions BETWEEN 0 AND 3),
  registrations_per_device_per_day integer NOT NULL DEFAULT 3
    CONSTRAINT ssss_registrations CHECK (registrations_per_device_per_day BETWEEN 1 AND 10),
  config_version                   bigint  NOT NULL DEFAULT 1 CONSTRAINT ssss_version CHECK (config_version >= 1),
  updated_at                       timestamptz NOT NULL DEFAULT now(),
  updated_by                       text,
  PRIMARY KEY (tenant_id, site_id),
  -- A reminder sent at or before the moment the account was created would be no reminder at all.
  CONSTRAINT ssss_reminder_before_deadline CHECK (reminder_hours_before < verification_deadline_hours)
);
COMMENT ON TABLE iam_v2.site_self_service_settings IS
  'Per-site Email accounts settings (0117): verification required, deadline, suspension policy, reminder, '
  'resend limits, the grace hour and the per-device registration cap. A site with no row reads the defaults. '
  'Written only through iam_v2.self_service_settings_set.';

CREATE TABLE IF NOT EXISTS iam_v2.self_service_settings_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  changed_by         text NOT NULL
    CONSTRAINT self_service_settings_changes_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text NOT NULL
    CONSTRAINT self_service_settings_changes_reason CHECK (length(btrim(change_reason)) > 0),
  -- One old/new pair per setting (old_* NULL on the first change, when the site still read the defaults), so
  -- the history reads as columns in the Admin Console exactly as 0104's does.
  old_verification_required            boolean,
  new_verification_required            boolean NOT NULL,
  old_verification_deadline_hours      integer,
  new_verification_deadline_hours      integer NOT NULL,
  old_suspension_policy                text,
  new_suspension_policy                text    NOT NULL,
  old_reminder_hours_before            integer,
  new_reminder_hours_before            integer NOT NULL,
  old_resend_cooldown_seconds          integer,
  new_resend_cooldown_seconds          integer NOT NULL,
  old_resend_max_per_day               integer,
  new_resend_max_per_day               integer NOT NULL,
  old_grace_minutes                    integer,
  new_grace_minutes                    integer NOT NULL,
  old_max_grace_extensions             integer,
  new_max_grace_extensions             integer NOT NULL,
  old_registrations_per_device_per_day integer,
  new_registrations_per_device_per_day integer NOT NULL,
  new_config_version bigint NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS self_service_settings_changes_lookup
  ON iam_v2.self_service_settings_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.self_service_settings_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.self_service_settings_changes is append-only: % refused', TG_OP
    USING ERRCODE = 'restrict_violation';
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.self_service_settings_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS self_service_settings_changes_append_only ON iam_v2.self_service_settings_changes;
CREATE TRIGGER self_service_settings_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.self_service_settings_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.self_service_settings_changes_append_only();

-- Reading it. A site with no row reads the defaults (config_version 0, updated_at NULL).
CREATE OR REPLACE FUNCTION iam_v2.self_service_settings_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (verification_required boolean, verification_deadline_hours integer, suspension_policy text,
                 reminder_hours_before integer, resend_cooldown_seconds integer, resend_max_per_day integer,
                 grace_minutes integer, max_grace_extensions integer, registrations_per_device_per_day integer,
                 config_version bigint, updated_at timestamptz, updated_by text)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.verification_required, true),
         COALESCE(s.verification_deadline_hours, 24),
         COALESCE(s.suspension_policy, 'SUSPEND'),
         COALESCE(s.reminder_hours_before, 4),
         COALESCE(s.resend_cooldown_seconds, 60),
         COALESCE(s.resend_max_per_day, 5),
         COALESCE(s.grace_minutes, 60),
         COALESCE(s.max_grace_extensions, 1),
         COALESCE(s.registrations_per_device_per_day, 3),
         COALESCE(s.config_version, 0),
         s.updated_at,
         s.updated_by
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_self_service_settings s
      ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;
COMMENT ON FUNCTION iam_v2.self_service_settings_get(uuid,uuid) IS
  'The Email accounts settings in force for a site. config_version 0 with a null updated_at means no row '
  'exists and the answer is the contract defaults.';

-- Changing it. Bounds re-checked here AND by the table CHECKs; an operator and a reason are required.
CREATE OR REPLACE FUNCTION iam_v2.self_service_settings_set(
    p_tenant uuid, p_site uuid,
    p_verification_required boolean, p_verification_deadline_hours integer, p_suspension_policy text,
    p_reminder_hours_before integer, p_resend_cooldown_seconds integer, p_resend_max_per_day integer,
    p_grace_minutes integer, p_max_grace_extensions integer, p_registrations_per_device_per_day integer,
    p_operator text, p_reason text)
  RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_version bigint; v_old iam_v2.site_self_service_settings%ROWTYPE;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required: "somebody changed it" is not an audit record'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change the Email accounts settings'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_verification_required IS NULL THEN
    RAISE EXCEPTION 'verification_required must be true or false' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_verification_deadline_hours IS NULL OR p_verification_deadline_hours NOT BETWEEN 1 AND 168 THEN
    RAISE EXCEPTION 'verification_deadline_hours must be 1-168 (got %)', p_verification_deadline_hours
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_suspension_policy IS NULL OR p_suspension_policy NOT IN ('SUSPEND','WARN_ONLY') THEN
    RAISE EXCEPTION 'suspension_policy must be SUSPEND or WARN_ONLY (got %)', coalesce(p_suspension_policy,'null')
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reminder_hours_before IS NULL OR p_reminder_hours_before NOT BETWEEN 0 AND 72
     OR p_reminder_hours_before >= p_verification_deadline_hours THEN
    RAISE EXCEPTION 'reminder_hours_before must be 0-72 and less than the deadline (got %)', p_reminder_hours_before
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_resend_cooldown_seconds IS NULL OR p_resend_cooldown_seconds NOT BETWEEN 30 AND 3600 THEN
    RAISE EXCEPTION 'resend_cooldown_seconds must be 30-3600 (got %)', p_resend_cooldown_seconds
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_resend_max_per_day IS NULL OR p_resend_max_per_day NOT BETWEEN 1 AND 20 THEN
    RAISE EXCEPTION 'resend_max_per_day must be 1-20 (got %)', p_resend_max_per_day
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_grace_minutes IS NULL OR p_grace_minutes NOT BETWEEN 15 AND 240 THEN
    RAISE EXCEPTION 'grace_minutes must be 15-240 (got %)', p_grace_minutes USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_max_grace_extensions IS NULL OR p_max_grace_extensions NOT BETWEEN 0 AND 3 THEN
    RAISE EXCEPTION 'max_grace_extensions must be 0-3 (got %)', p_max_grace_extensions
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_registrations_per_device_per_day IS NULL OR p_registrations_per_device_per_day NOT BETWEEN 1 AND 10 THEN
    RAISE EXCEPTION 'registrations_per_device_per_day must be 1-10 (got %)', p_registrations_per_device_per_day
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  PERFORM pg_advisory_xact_lock(hashtext('self_service_settings'), hashtext(p_site::text));

  SELECT s.* INTO v_old
    FROM iam_v2.site_self_service_settings s
   WHERE s.tenant_id = p_tenant AND s.site_id = p_site
     FOR UPDATE;

  INSERT INTO iam_v2.site_self_service_settings AS s
    (tenant_id, site_id, verification_required, verification_deadline_hours, suspension_policy,
     reminder_hours_before, resend_cooldown_seconds, resend_max_per_day, grace_minutes, max_grace_extensions,
     registrations_per_device_per_day, updated_by)
  VALUES (p_tenant, p_site, p_verification_required, p_verification_deadline_hours, p_suspension_policy,
          p_reminder_hours_before, p_resend_cooldown_seconds, p_resend_max_per_day, p_grace_minutes,
          p_max_grace_extensions, p_registrations_per_device_per_day, btrim(p_operator))
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET verification_required            = EXCLUDED.verification_required,
         verification_deadline_hours      = EXCLUDED.verification_deadline_hours,
         suspension_policy                = EXCLUDED.suspension_policy,
         reminder_hours_before            = EXCLUDED.reminder_hours_before,
         resend_cooldown_seconds          = EXCLUDED.resend_cooldown_seconds,
         resend_max_per_day               = EXCLUDED.resend_max_per_day,
         grace_minutes                    = EXCLUDED.grace_minutes,
         max_grace_extensions             = EXCLUDED.max_grace_extensions,
         registrations_per_device_per_day = EXCLUDED.registrations_per_device_per_day,
         updated_by                       = EXCLUDED.updated_by,
         config_version                   = s.config_version + 1,
         updated_at                       = now()
  RETURNING s.config_version INTO v_version;

  -- v_old is NULL-filled when no row existed: the first change records no "old" values, as in 0104.
  INSERT INTO iam_v2.self_service_settings_changes
    (tenant_id, site_id, changed_by, change_reason,
     old_verification_required, new_verification_required,
     old_verification_deadline_hours, new_verification_deadline_hours,
     old_suspension_policy, new_suspension_policy,
     old_reminder_hours_before, new_reminder_hours_before,
     old_resend_cooldown_seconds, new_resend_cooldown_seconds,
     old_resend_max_per_day, new_resend_max_per_day,
     old_grace_minutes, new_grace_minutes,
     old_max_grace_extensions, new_max_grace_extensions,
     old_registrations_per_device_per_day, new_registrations_per_device_per_day,
     new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason),
          v_old.verification_required, p_verification_required,
          v_old.verification_deadline_hours, p_verification_deadline_hours,
          v_old.suspension_policy, p_suspension_policy,
          v_old.reminder_hours_before, p_reminder_hours_before,
          v_old.resend_cooldown_seconds, p_resend_cooldown_seconds,
          v_old.resend_max_per_day, p_resend_max_per_day,
          v_old.grace_minutes, p_grace_minutes,
          v_old.max_grace_extensions, p_max_grace_extensions,
          v_old.registrations_per_device_per_day, p_registrations_per_device_per_day,
          v_version);

  RETURN v_version;
END $fn$;
COMMENT ON FUNCTION iam_v2.self_service_settings_set(uuid,uuid,boolean,integer,text,integer,integer,integer,integer,integer,integer,text,text) IS
  'Stores validated Email accounts settings and returns the new config_version, recording the old and new '
  'values with the operator and the reason. Existing deadlines are not moved: a change applies to accounts '
  'registered after it (and to the sweep''s suspension and reminder decisions at once).';

-- ---------------------------------------------------------------------------------------------------------
-- 5. tenants.auth_methods: "email" (code) becomes "email_account".
-- ---------------------------------------------------------------------------------------------------------
-- The switch keeps its enabled state: a site that offered email sign-in keeps offering email sign-in, now as an
-- account. The temporary-internet block (email.temporary_access) and the mail-service allowlist
-- (portal.mail_providers / portal.mail_domains) only ever served the code and are dropped with it, and a primary
-- method of "email" is renamed with the switch.
--
-- public.tenants belongs to the platform role (stayconnect), and the live-site migration runner applies as
-- iam_v2_owner, which may not write it. So, as in 0107, the change is made here only when the applying role can,
-- and is otherwise one explicit step for the owner: deploy/scripts/convert-email-method-to-email-account.sql
-- (the same statement). Until it runs, scd reads a leftover "email" switch as "email_account" (tenantcfg.Load),
-- so the site keeps the same answer either way.
DO $am$
BEGIN
  IF NOT (has_table_privilege(current_user, 'public.tenants', 'SELECT')
          AND has_table_privilege(current_user, 'public.tenants', 'UPDATE')) THEN
    RAISE NOTICE 'public.tenants is not writable by %; convert the email switch with deploy/scripts/convert-email-method-to-email-account.sql', current_user;
    RETURN;
  END IF;
  UPDATE public.tenants t
     SET auth_methods = (CASE WHEN t.auth_methods ? 'email_account' THEN t.auth_methods - 'email'
                              ELSE (t.auth_methods - 'email') || jsonb_build_object('email_account',
                                     jsonb_build_object('enabled',
                                       COALESCE(CASE WHEN jsonb_typeof(t.auth_methods->'email'->'enabled') = 'boolean'
                                                     THEN (t.auth_methods->'email'->>'enabled')::boolean END, false)))
                         END)
   WHERE t.auth_methods ? 'email';
  UPDATE public.tenants t
     SET auth_methods = jsonb_set(t.auth_methods, '{portal}',
                          ((t.auth_methods->'portal') - 'mail_providers' - 'mail_domains')
                          || CASE WHEN t.auth_methods->'portal'->>'primary_method' = 'email'
                                  THEN '{"primary_method":"email_account"}'::jsonb ELSE '{}'::jsonb END)
   WHERE jsonb_typeof(t.auth_methods->'portal') = 'object'
     AND (t.auth_methods->'portal' ? 'mail_providers' OR t.auth_methods->'portal' ? 'mail_domains'
          OR t.auth_methods->'portal'->>'primary_method' = 'email');
END $am$;

-- ---------------------------------------------------------------------------------------------------------
-- 6. Ownership, declared rather than inherited from whoever applied this file (see 0089).
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.account_email_tokens OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.site_self_service_settings OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.self_service_settings_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.self_service_settings_changes_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.self_service_settings_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.self_service_settings_set(uuid,uuid,boolean,integer,text,integer,integer,integer,integer,integer,integer,text,text) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- ---------------------------------------------------------------------------------------------------------
-- 7. Least privilege. Mirrored in deploy/gatep/svc-self-service-grants.sql so it survives Gate-P.
-- ---------------------------------------------------------------------------------------------------------
-- scd creates the self-service account (INSERT) and, of an account, writes only the password (reset), the
-- verification and grace state; it owns the token outbox. It reads the settings through the getter and never
-- the settings table. edged reads and writes the settings only through the definer functions and reads the
-- history; it already holds full DML on guest_access_accounts (staff-created Personal accounts).
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT INSERT ON iam_v2.guest_access_accounts TO svc_scd;
    GRANT UPDATE (password_hash, email_verified_at, verification_deadline, verification_reminder_sent_at,
                  grace_until, grace_extensions_used) ON iam_v2.guest_access_accounts TO svc_scd;
    GRANT SELECT, INSERT, UPDATE ON iam_v2.account_email_tokens TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.self_service_settings_get(uuid,uuid) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.self_service_settings_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.self_service_settings_set(uuid,uuid,boolean,integer,text,integer,integer,integer,integer,integer,integer,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.self_service_settings_changes TO svc_edged;
  END IF;
END $g$;

REVOKE EXECUTE ON FUNCTION iam_v2.self_service_settings_get(uuid,uuid) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION iam_v2.self_service_settings_set(uuid,uuid,boolean,integer,text,integer,integer,integer,integer,integer,integer,text,text) FROM PUBLIC;

COMMIT;
