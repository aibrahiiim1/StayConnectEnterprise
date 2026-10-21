-- 0127 -- GUEST DATA IS ANONYMISED WHEN THE HOTEL'S RETENTION ENDS
-- (GDPR remediation plan C1, with F6; Product-Owner decisions D-C1, D-C1a and D-C3, 2026-10-17;
--  docs/privacy/GDPR_REMEDIATION_PLAN.md section 4, docs/OPERATIONAL_SETTINGS_POLICY.md).
--
-- Until now the appliance kept guests' personal data for ever: the PMS mirror after checkout, MAC and IP
-- addresses on devices and sessions, email/phone/social identities, the destinations of verification codes,
-- and the audit log. This migration adds:
--
--   1. iam_v2.site_data_retention -- six per-site retention periods with the approved defaults, bounds as
--      CHECKs, written only through iam_v2.data_retention_set (operator + reason, append-only change log).
--   2. ONE anonymisation routine -- per-subject primitives (a stay, a device, a guest principal, a guest
--      account) that the retention run uses now and the C3 "erase this guest" action will reuse. Anonymising
--      means: every identifier is replaced by a RANDOM token (never derived from the value, nothing keeps a
--      mapping back) or cleared; free text is blanked; no row is deleted (D-C1a), so totals, financial records
--      and the shape of history survive.
--   3. iam_v2.run_data_retention and public.run_data_retention_public -- the hourly run (scd), batched,
--      touching only what is no longer in use, and recording every run that changed anything.
--   4. Two narrow exceptions to append-only guards, usable only by the table owner inside a run: stay_events
--      may have its payload and event fingerprint replaced, and a removed client account may be renamed to an
--      anonymised (still removed) username. Every other change those guards refuse is still refused.
--
-- What is NOT touched: financial rows (purchases, payments, postings, settlements -- kept 10 years, never
-- purged automatically), entitlement and session totals, accounting detail (its own setting, migration 0122),
-- operator identities in the audit log, and anything still live: an open session, an authorised device, an
-- unexpired remembered device, a stay with an active entitlement or valid post-stay access.
--
-- Sign-in attempts keep their existing DELETE (scd, internal/signinattempt) and now read sign_in_attempt_days
-- from this setting instead of a constant (plan F6).

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The setting, its defaults, its change log, the run record.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.data_retention_defaults(
    OUT pms_stay_days integer, OUT client_activity_days integer, OUT inactive_identity_days integer,
    OUT expired_record_days integer, OUT audit_log_days integer, OUT sign_in_attempt_days integer)
    LANGUAGE sql IMMUTABLE
    AS $$ SELECT 30, 365, 365, 7, 730, 30 $$;
COMMENT ON FUNCTION iam_v2.data_retention_defaults() IS
  'The Product Owner''s approved retention defaults (D-C1, 2026-10-17), in days: PMS stay data 30 after checkout, '
  'sessions/devices 365, inactive guest identities 365, expired single-use records 7, audit log 730, sign-in '
  'attempts 30. The one source of the defaults: a site with no row reads these.';

CREATE TABLE IF NOT EXISTS iam_v2.site_data_retention (
  tenant_id              uuid NOT NULL,
  site_id                uuid NOT NULL,
  pms_stay_days          integer NOT NULL CONSTRAINT sdr_pms_stay CHECK (pms_stay_days BETWEEN 7 AND 3650),
  client_activity_days   integer NOT NULL CONSTRAINT sdr_client_activity CHECK (client_activity_days BETWEEN 30 AND 3650),
  inactive_identity_days integer NOT NULL CONSTRAINT sdr_inactive_identity CHECK (inactive_identity_days BETWEEN 30 AND 3650),
  expired_record_days    integer NOT NULL CONSTRAINT sdr_expired_record CHECK (expired_record_days BETWEEN 1 AND 365),
  audit_log_days         integer NOT NULL CONSTRAINT sdr_audit_log CHECK (audit_log_days BETWEEN 90 AND 3650),
  sign_in_attempt_days   integer NOT NULL CONSTRAINT sdr_sign_in_attempt CHECK (sign_in_attempt_days BETWEEN 7 AND 365),
  config_version         bigint NOT NULL DEFAULT 1 CONSTRAINT sdr_version CHECK (config_version >= 1),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             text,
  PRIMARY KEY (tenant_id, site_id)
);
COMMENT ON TABLE iam_v2.site_data_retention IS
  'How long guest personal data is kept before it is anonymised, per site (0127, GDPR plan C1). A site with no '
  'row uses iam_v2.data_retention_defaults(). Written only through iam_v2.data_retention_set.';

CREATE TABLE IF NOT EXISTS iam_v2.data_retention_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  changed_by         text NOT NULL CONSTRAINT drc_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text NOT NULL CONSTRAINT drc_reason CHECK (length(btrim(change_reason)) BETWEEN 4 AND 500),
  old_settings       jsonb,
  new_settings       jsonb NOT NULL,
  new_config_version bigint NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS drc_site ON iam_v2.data_retention_changes (tenant_id, site_id, changed_at DESC);

CREATE TABLE IF NOT EXISTS iam_v2.data_retention_runs (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL,
  site_id         uuid NOT NULL,
  category        text NOT NULL CONSTRAINT drr_category CHECK (category IN
                    ('pms_stays','client_activity','inactive_identities','expired_records','audit_log')),
  retention_days  integer NOT NULL,
  cutoff          timestamptz NOT NULL,
  rows_anonymised integer NOT NULL CONSTRAINT drr_rows CHECK (rows_anonymised > 0),
  ran_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS drr_site ON iam_v2.data_retention_runs (tenant_id, site_id, ran_at DESC);
COMMENT ON TABLE iam_v2.data_retention_runs IS
  'Every retention run that anonymised something (0127): the category, the retention in force, the cutoff and '
  'how many records changed. Append-only. A run that changed nothing writes nothing.';

-- Where the audit-log pass has reached. The audit log has no key, so it is anonymised oldest-first in bounded
-- time windows and this remembers the end of the last window instead of rescanning the table every hour.
CREATE TABLE IF NOT EXISTS iam_v2.data_retention_checkpoints (
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  category   text NOT NULL CONSTRAINT drk_category CHECK (category IN ('audit_log')),
  done_until timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, site_id, category)
);

CREATE OR REPLACE FUNCTION iam_v2.data_retention_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.% is append-only: % refused', TG_TABLE_NAME, TG_OP USING ERRCODE = 'restrict_violation';
END $$;
DROP TRIGGER IF EXISTS data_retention_changes_append_only ON iam_v2.data_retention_changes;
CREATE TRIGGER data_retention_changes_append_only BEFORE UPDATE OR DELETE ON iam_v2.data_retention_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.data_retention_append_only();
DROP TRIGGER IF EXISTS data_retention_runs_append_only ON iam_v2.data_retention_runs;
CREATE TRIGGER data_retention_runs_append_only BEFORE UPDATE OR DELETE ON iam_v2.data_retention_runs
  FOR EACH ROW EXECUTE FUNCTION iam_v2.data_retention_append_only();

CREATE OR REPLACE FUNCTION iam_v2.data_retention_get(p_tenant uuid, p_site uuid)
    RETURNS TABLE(pms_stay_days integer, client_activity_days integer, inactive_identity_days integer,
                  expired_record_days integer, audit_log_days integer, sign_in_attempt_days integer,
                  config_version bigint, updated_at timestamptz, updated_by text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(s.pms_stay_days, d.pms_stay_days), COALESCE(s.client_activity_days, d.client_activity_days),
         COALESCE(s.inactive_identity_days, d.inactive_identity_days),
         COALESCE(s.expired_record_days, d.expired_record_days), COALESCE(s.audit_log_days, d.audit_log_days),
         COALESCE(s.sign_in_attempt_days, d.sign_in_attempt_days),
         COALESCE(s.config_version, 0), s.updated_at, s.updated_by
    FROM iam_v2.data_retention_defaults() d
    LEFT JOIN iam_v2.site_data_retention s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$$;

CREATE OR REPLACE FUNCTION iam_v2.data_retention_set(
    p_tenant uuid, p_site uuid, p_pms_stay_days integer, p_client_activity_days integer,
    p_inactive_identity_days integer, p_expired_record_days integer, p_audit_log_days integer,
    p_sign_in_attempt_days integer, p_operator text, p_reason text) RETURNS bigint
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_version bigint; v_old jsonb; v_new jsonb;
BEGIN
  IF p_operator IS NULL OR btrim(p_operator) = '' THEN
    RAISE EXCEPTION 'an operator label is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) < 4 OR length(btrim(p_reason)) > 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required to change data retention'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF num_nulls(p_pms_stay_days, p_client_activity_days, p_inactive_identity_days, p_expired_record_days,
               p_audit_log_days, p_sign_in_attempt_days) > 0 THEN
    RAISE EXCEPTION 'every retention period is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('data_retention'), hashtext(p_site::text));
  SELECT to_jsonb(s) - 'tenant_id' - 'site_id' - 'config_version' - 'updated_at' - 'updated_by' INTO v_old
    FROM iam_v2.site_data_retention s WHERE s.tenant_id = p_tenant AND s.site_id = p_site FOR UPDATE;
  INSERT INTO iam_v2.site_data_retention AS s
    (tenant_id, site_id, pms_stay_days, client_activity_days, inactive_identity_days, expired_record_days,
     audit_log_days, sign_in_attempt_days, updated_by)
  VALUES (p_tenant, p_site, p_pms_stay_days, p_client_activity_days, p_inactive_identity_days,
          p_expired_record_days, p_audit_log_days, p_sign_in_attempt_days, btrim(p_operator))
  ON CONFLICT (tenant_id, site_id) DO UPDATE
     SET pms_stay_days = EXCLUDED.pms_stay_days, client_activity_days = EXCLUDED.client_activity_days,
         inactive_identity_days = EXCLUDED.inactive_identity_days,
         expired_record_days = EXCLUDED.expired_record_days, audit_log_days = EXCLUDED.audit_log_days,
         sign_in_attempt_days = EXCLUDED.sign_in_attempt_days, updated_by = EXCLUDED.updated_by,
         config_version = s.config_version + 1, updated_at = now()
  RETURNING s.config_version, to_jsonb(s) - 'tenant_id' - 'site_id' - 'config_version' - 'updated_at' - 'updated_by'
    INTO v_version, v_new;
  INSERT INTO iam_v2.data_retention_changes
    (tenant_id, site_id, changed_by, change_reason, old_settings, new_settings, new_config_version)
  VALUES (p_tenant, p_site, btrim(p_operator), btrim(p_reason), v_old, v_new, v_version);
  RETURN v_version;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 2. The anonymisation routine.
-- ---------------------------------------------------------------------------------------------------------
-- A random token: 128 bits, unrelated to the value it replaces, so nothing can map it back.
CREATE OR REPLACE FUNCTION iam_v2.anonymised_token() RETURNS text
    LANGUAGE sql VOLATILE SET search_path TO 'iam_v2', 'pg_temp'
    AS $$ SELECT 'anon-' || encode(public.gen_random_bytes(16), 'hex') $$;

-- A random MAC with the multicast and locally-administered bits set (first octet 03): no network card sends
-- from such an address, so a token can never collide with a device that later appears for real.
CREATE OR REPLACE FUNCTION iam_v2.anonymised_mac() RETURNS macaddr
    LANGUAGE sql VOLATILE SET search_path TO 'iam_v2', 'pg_temp'
    AS $$ SELECT ('03' || encode(public.gen_random_bytes(5), 'hex'))::macaddr $$;

-- The guards below admit an anonymising write only from the table owner (the definer functions here) and
-- only while this transaction-local flag is set: neither alone is enough, and no runtime role is the owner.
CREATE OR REPLACE FUNCTION iam_v2.anonymise_stay(p_stay uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  PERFORM iam_v2.begin_controlled_operation('stay');
  UPDATE iam_v2.stays
     SET external_reservation_id = iam_v2.anonymised_token(), external_stay_identity = iam_v2.anonymised_token(),
         normalized_room_number = NULL, vip = NULL, travel_agent = NULL, rate_plan = NULL
   WHERE id = p_stay AND external_reservation_id NOT LIKE 'anon-%';
  UPDATE iam_v2.stay_guests
     SET external_guest_id = NULL, first_name_norm = NULL, last_name_norm = NULL, display_name = NULL,
         date_of_birth = NULL, pin_hash = NULL
   WHERE stay_id = p_stay
     AND num_nonnulls(external_guest_id, first_name_norm, last_name_norm, display_name, date_of_birth, pin_hash) > 0;
  UPDATE iam_v2.stay_events
     SET payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE stay_id = p_stay AND processing_status <> 'PENDING'
     AND payload IS DISTINCT FROM '{"anonymised": true}'::jsonb;
END $$;
COMMENT ON FUNCTION iam_v2.anonymise_stay(uuid) IS
  'Anonymises one stay (0127): reservation and stay identity become random tokens; room, VIP, travel agent and '
  'rate plan are cleared; every guest''s names, external id, date of birth and PIN hash are cleared; the raw PMS '
  'payload and fingerprint of its processed events are replaced. Status, dates and versions are untouched.';

CREATE OR REPLACE FUNCTION iam_v2.anonymise_device(p_device uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  UPDATE iam_v2.devices SET mac = iam_v2.anonymised_mac(), last_ip = NULL
   WHERE id = p_device AND mac::text NOT LIKE '03:%';
  UPDATE iam_v2.sessions SET ip = NULL, mac = NULL
   WHERE device_id = p_device AND ended IS NOT NULL AND (ip IS NOT NULL OR mac IS NOT NULL);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.anonymise_guest_principal(p_principal uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  UPDATE iam_v2.guest_principals SET display_name = NULL WHERE id = p_principal AND display_name IS NOT NULL;
  UPDATE iam_v2.guest_principal_identities SET factor_value_norm = iam_v2.anonymised_token(), attrs = '{}'::jsonb
   WHERE guest_principal_id = p_principal AND factor_value_norm NOT LIKE 'anon-%';
  UPDATE iam_v2.principal_device_credentials SET revoked_at = now()
   WHERE guest_principal_id = p_principal AND revoked_at IS NULL;
END $$;

-- The username keeps the removed-account shape ('<x>.removed-<id8>.invalid') and the account is disabled, so
-- every surface that already treats removed accounts as removed keeps doing so. The password hash is kept: it
-- identifies nobody once the username is gone, and an unparsable hash would turn a refused sign-in into an error.
CREATE OR REPLACE FUNCTION iam_v2.anonymise_guest_account(p_account uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  UPDATE iam_v2.account_email_tokens
     SET last_send_error = NULL,
         superseded_at = CASE WHEN used_at IS NULL AND superseded_at IS NULL THEN now() ELSE superseded_at END
   WHERE account_id = p_account;
  UPDATE iam_v2.guest_access_accounts
     SET username = iam_v2.anonymised_token() || '@anonymised.removed-' || left(replace(id::text, '-', ''), 8) || '.invalid',
         display_name = NULL, notes = NULL, created_by_device_mac = NULL, enabled = false
   WHERE id = p_account AND username NOT LIKE 'anon-%@anonymised.removed-%';
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 3. The run (iam_v2 tables). Each category takes at most p_batch subjects per call; scd calls it hourly.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.run_data_retention(p_tenant uuid, p_site uuid, p_batch integer)
    RETURNS TABLE(o_category text, o_cutoff timestamptz, o_anonymised integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE r record; v_cut timestamptz; v_n integer; v_k integer; v_id uuid;
BEGIN
  IF p_batch IS NULL OR p_batch NOT BETWEEN 1 AND 10000 THEN
    RAISE EXCEPTION 'a batch of 1-10000 is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO r FROM iam_v2.data_retention_get(p_tenant, p_site);
  PERFORM set_config('iam_v2.anonymise', 'retention', true);

  -- PMS STAYS: the stay is over (checked out, cancelled or no-show) for longer than the retention, nothing it
  -- granted is still live, and no post-stay access is valid inside the retention.
  v_cut := now() - make_interval(days => r.pms_stay_days); v_n := 0;
  FOR v_id IN
    SELECT s.id FROM iam_v2.stays s
      LEFT JOIN LATERAL (SELECT max(e.received_at) AS last_event FROM iam_v2.stay_events e WHERE e.stay_id = s.id) le ON true
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site
       AND s.status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
       AND s.external_reservation_id NOT LIKE 'anon-%'
       AND COALESCE(s.effective_checkout_at, (s.departure + 1)::timestamptz, (s.arrival + 1)::timestamptz, le.last_event) < v_cut
       AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e WHERE e.stay_id = s.id AND e.status <> 'TERMINATED')
       AND NOT EXISTS (SELECT 1 FROM iam_v2.post_stay_profiles p
                        WHERE p.origin_stay_id = s.id AND p.status = 'ACTIVE' AND p.valid_until >= v_cut)
     ORDER BY s.id LIMIT p_batch
  LOOP
    PERFORM iam_v2.anonymise_stay(v_id); v_n := v_n + 1;
  END LOOP;
  -- Processed PMS events that never resolved to a stay carry the same guest data.
  PERFORM iam_v2.begin_controlled_operation('stay');
  UPDATE iam_v2.stay_events e
     SET payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE e.id IN (SELECT x.id FROM iam_v2.stay_events x
                   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.stay_id IS NULL
                     AND x.processing_status <> 'PENDING' AND x.received_at < v_cut
                     AND x.payload IS DISTINCT FROM '{"anonymised": true}'::jsonb
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'pms_stays', r.pms_stay_days, v_cut, v_n);
    o_category := 'pms_stays'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;

  -- CLIENT ACTIVITY: a device unseen for longer than the retention with nothing live on it, and the address
  -- columns of every session that ended before the cutoff.
  v_cut := now() - make_interval(days => r.client_activity_days); v_n := 0;
  FOR v_id IN
    SELECT d.id FROM iam_v2.devices d
     WHERE d.tenant_id = p_tenant AND d.site_id = p_site
       AND d.mac::text NOT LIKE '03:%'
       AND COALESCE(d.last_seen, d.first_seen) < v_cut
       AND NOT EXISTS (SELECT 1 FROM iam_v2.sessions se WHERE se.device_id = d.id AND (se.ended IS NULL OR se.ended >= v_cut))
       AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlement_devices ed WHERE ed.device_id = d.id AND ed.status = 'AUTHORIZED')
       AND NOT EXISTS (SELECT 1 FROM iam_v2.principal_device_credentials c
                        WHERE c.device_id = d.id AND c.revoked_at IS NULL AND c.expires_at > now())
     ORDER BY d.id LIMIT p_batch
  LOOP
    PERFORM iam_v2.anonymise_device(v_id); v_n := v_n + 1;
  END LOOP;
  UPDATE iam_v2.sessions se SET ip = NULL, mac = NULL
   WHERE se.id IN (SELECT x.id FROM iam_v2.sessions x
                    WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.ended < v_cut
                      AND (x.ip IS NOT NULL OR x.mac IS NOT NULL)
                    LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'client_activity', r.client_activity_days, v_cut, v_n);
    o_category := 'client_activity'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;

  -- INACTIVE IDENTITIES: no sign-in, verification, access or remembered-device use inside the retention, and
  -- nothing granted to the identity still live.
  v_cut := now() - make_interval(days => r.inactive_identity_days); v_n := 0;
  FOR v_id IN
    SELECT gp.id FROM iam_v2.guest_principals gp
     WHERE gp.tenant_id = p_tenant
       AND (gp.display_name IS NOT NULL
            OR EXISTS (SELECT 1 FROM iam_v2.guest_principal_identities i
                        WHERE i.guest_principal_id = gp.id AND i.factor_value_norm NOT LIKE 'anon-%'))
       AND NOT EXISTS (SELECT 1 FROM iam_v2.guest_principal_identities i
                        WHERE i.guest_principal_id = gp.id AND i.verified_at >= v_cut)
       AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e
                        WHERE e.guest_principal_id = gp.id
                          AND (e.status <> 'TERMINATED' OR COALESCE(e.terminated_at, 'infinity') >= v_cut))
       AND NOT EXISTS (SELECT 1 FROM iam_v2.auth_contexts ac
                        WHERE ac.guest_principal_id = gp.id AND COALESCE(ac.consumed_at, ac.expires_at) >= v_cut)
       AND NOT EXISTS (SELECT 1 FROM iam_v2.principal_device_credentials c
                        WHERE c.guest_principal_id = gp.id
                          AND (GREATEST(c.created_at, c.last_used_at) >= v_cut
                               OR (c.revoked_at IS NULL AND c.expires_at > now())))
     ORDER BY gp.id LIMIT p_batch
  LOOP
    PERFORM iam_v2.anonymise_guest_principal(v_id); v_n := v_n + 1;
  END LOOP;
  FOR v_id IN
    SELECT a.id FROM iam_v2.guest_access_accounts a
     WHERE a.tenant_id = p_tenant AND a.site_id = p_site
       AND a.username NOT LIKE 'anon-%@anonymised.removed-%'
       AND GREATEST(COALESCE(a.created_at, '-infinity'), COALESCE(a.valid_until, '-infinity'),
                    COALESCE(a.grace_until, '-infinity'), COALESCE(a.email_verified_at, '-infinity'),
                    COALESCE(a.last_login_at, '-infinity'), COALESCE(a.locked_until, '-infinity')) < v_cut
       AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e
                        WHERE e.guest_account_id = a.id
                          AND (e.status <> 'TERMINATED' OR COALESCE(e.terminated_at, 'infinity') >= v_cut))
       AND NOT EXISTS (SELECT 1 FROM iam_v2.auth_contexts ac
                        WHERE ac.guest_account_id = a.id AND COALESCE(ac.consumed_at, ac.expires_at) >= v_cut)
     ORDER BY a.id LIMIT p_batch
  LOOP
    PERFORM iam_v2.anonymise_guest_account(v_id); v_n := v_n + 1;
  END LOOP;
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'inactive_identities', r.inactive_identity_days, v_cut, v_n);
    o_category := 'inactive_identities'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;

  -- EXPIRED SINGLE-USE RECORDS (iam_v2 part; public tables are in run_data_retention_public).
  v_cut := now() - make_interval(days => r.expired_record_days); v_n := 0;
  UPDATE iam_v2.verification_access_grants g
     SET device_mac = iam_v2.anonymised_mac(), ip = '0.0.0.0'::inet,
         destination_digest = encode(public.gen_random_bytes(32), 'hex')
   WHERE g.id IN (SELECT x.id FROM iam_v2.verification_access_grants x
                   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.expires_at < v_cut
                     AND x.device_mac::text NOT LIKE '03:%'
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  UPDATE iam_v2.guest_signin_restrictions g
     SET device_mac = iam_v2.anonymised_mac(), last_submitted_room = NULL
   WHERE g.id IN (SELECT x.id FROM iam_v2.guest_signin_restrictions x
                   WHERE x.tenant_id = p_tenant AND x.site_id = p_site
                     AND GREATEST(x.updated_at, COALESCE(x.expires_at, '-infinity'), COALESCE(x.released_at, '-infinity')) < v_cut
                     AND x.device_mac::text NOT LIKE '03:%'
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  UPDATE iam_v2.account_email_tokens t SET last_send_error = NULL
   WHERE t.id IN (SELECT x.id FROM iam_v2.account_email_tokens x
                   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.expires_at < v_cut
                     AND x.last_send_error IS NOT NULL
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'expired_records', r.expired_record_days, v_cut, v_n);
    o_category := 'expired_records'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;

  PERFORM set_config('iam_v2.anonymise', '', true);
END $$;
COMMENT ON FUNCTION iam_v2.run_data_retention(uuid, uuid, integer) IS
  'The hourly guest-data retention run for the iam_v2 tables (0127, GDPR plan C1): anonymises, in batches of at '
  'most p_batch per category, PMS stays over for longer than the site''s retention, devices and sessions, inactive '
  'guest identities and accounts, and expired single-use records. Deletes nothing; records every run that changed '
  'something in iam_v2.data_retention_runs.';

-- ---------------------------------------------------------------------------------------------------------
-- 4. The run (public tables): verification codes, OAuth states, legacy PMS attempts, the audit log.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.run_data_retention_public(p_tenant uuid, p_site uuid, p_batch integer)
    RETURNS TABLE(o_category text, o_cutoff timestamptz, o_anonymised integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE r record; v_cut timestamptz; v_n integer; v_k integer; v_from timestamptz; v_to timestamptz;
BEGIN
  IF p_batch IS NULL OR p_batch NOT BETWEEN 1 AND 10000 THEN
    RAISE EXCEPTION 'a batch of 1-10000 is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO r FROM iam_v2.data_retention_get(p_tenant, p_site);

  v_cut := now() - make_interval(days => r.expired_record_days); v_n := 0;
  UPDATE public.auth_otps o SET destination = 'anonymised', ip = NULL, user_agent = NULL
   WHERE o.id IN (SELECT x.id FROM public.auth_otps x
                   WHERE x.tenant_id = p_tenant AND x.expires_at < v_cut
                     AND (x.destination <> 'anonymised' OR x.ip IS NOT NULL OR x.user_agent IS NOT NULL)
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  UPDATE public.social_oauth_states s
     SET client_ip = NULL, client_mac = NULL, user_agent = NULL, code_verifier = NULL, nonce = NULL
   WHERE s.state IN (SELECT x.state FROM public.social_oauth_states x
                      WHERE x.tenant_id = p_tenant AND x.expires_at < v_cut
                        AND num_nonnulls(x.client_ip, x.client_mac, x.user_agent, x.code_verifier, x.nonce) > 0
                      LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  UPDATE public.pms_attempts a SET room_number = 'anonymised', ip = NULL
   WHERE a.id IN (SELECT x.id FROM public.pms_attempts x
                   WHERE x.tenant_id = p_tenant AND x.attempted_at < v_cut
                     AND (x.room_number <> 'anonymised' OR x.ip IS NOT NULL)
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'expired_records', r.expired_record_days, v_cut, v_n);
    o_category := 'expired_records'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;

  -- AUDIT LOG: oldest first, at most seven days of rows per call. What happened, when, and which operator did
  -- it stay; addresses, user agents, targets, payloads and guest actor ids go.
  v_cut := now() - make_interval(days => r.audit_log_days);
  SELECT k.done_until INTO v_from FROM iam_v2.data_retention_checkpoints k
   WHERE k.tenant_id = p_tenant AND k.site_id = p_site AND k.category = 'audit_log';
  IF v_from IS NULL THEN
    SELECT min(l.ts) INTO v_from FROM public.audit_log l;
  END IF;
  IF v_from IS NOT NULL AND v_from < v_cut THEN
    v_to := LEAST(v_from + interval '7 days', v_cut);
    UPDATE public.audit_log l
       SET ip = NULL, user_agent = NULL, target_id = NULL, payload = '{"anonymised": true}'::jsonb,
           actor_id = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.actor_id END
     WHERE l.ts >= v_from AND l.ts < v_to
       AND (l.tenant_id = p_tenant OR l.tenant_id IS NULL)
       AND l.payload IS DISTINCT FROM '{"anonymised": true}'::jsonb;
    GET DIAGNOSTICS v_n = ROW_COUNT;
    INSERT INTO iam_v2.data_retention_checkpoints (tenant_id, site_id, category, done_until)
    VALUES (p_tenant, p_site, 'audit_log', v_to)
    ON CONFLICT (tenant_id, site_id, category) DO UPDATE SET done_until = EXCLUDED.done_until;
    IF v_n > 0 THEN
      INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
      VALUES (p_tenant, p_site, 'audit_log', r.audit_log_days, v_cut, v_n);
      o_category := 'audit_log'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
    END IF;
  END IF;
END $$;
COMMENT ON FUNCTION public.run_data_retention_public(uuid, uuid, integer) IS
  'The hourly guest-data retention run for the public tables (0127, GDPR plan C1): anonymises expired '
  'verification codes, OAuth states and legacy PMS attempts, and audit-log rows older than the site''s retention. '
  'Deletes nothing; records runs in iam_v2.data_retention_runs.';

-- ---------------------------------------------------------------------------------------------------------
-- 5. The two guard exceptions.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p3_stay_event_appendonly()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE ok_stay int;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'stay_events is append-only (DELETE rejected)';
  END IF;

  -- RETENTION ANONYMISATION (0127). The table owner, inside a retention run, may replace the payload and the
  -- event fingerprint of a processed event -- and nothing else.
  IF TG_OP = 'UPDATE'
     AND current_setting('iam_v2.anonymise', true) = 'retention'
     AND current_user = (SELECT pg_get_userbyid(c.relowner) FROM pg_class c WHERE c.oid = TG_RELID) THEN
    IF OLD.processing_status = 'PENDING'
       OR (to_jsonb(NEW) - 'payload' - 'external_event_identity')
          IS DISTINCT FROM (to_jsonb(OLD) - 'payload' - 'external_event_identity') THEN
      RAISE EXCEPTION 'stay_events anonymisation may replace only payload and external_event_identity of a processed event';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP = 'INSERT' THEN
    IF NEW.processing_status <> 'PENDING' THEN
      RAISE EXCEPTION 'stay_events must be inserted as PENDING (no terminal event inserted directly)';
    END IF;
    IF NEW.stay_id IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events cannot be inserted with a pre-resolved stay_id';
    END IF;
    IF NEW.processed_at IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.processed_at must be NULL on insert';
    END IF;
    IF NEW.review_code IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.review_code must be NULL on insert';
    END IF;
    RETURN NEW;
  END IF;

  -- UPDATE: immutable identity / normalization / admission columns
  IF   NEW.id                    IS DISTINCT FROM OLD.id
    OR NEW.tenant_id             IS DISTINCT FROM OLD.tenant_id
    OR NEW.site_id               IS DISTINCT FROM OLD.site_id
    OR NEW.pms_interface_id      IS DISTINCT FROM OLD.pms_interface_id
    OR NEW.external_event_identity IS DISTINCT FROM OLD.external_event_identity
    OR NEW.event_type            IS DISTINCT FROM OLD.event_type
    OR NEW.pms_timestamp_raw     IS DISTINCT FROM OLD.pms_timestamp_raw
    OR NEW.pms_timestamp_utc     IS DISTINCT FROM OLD.pms_timestamp_utc
    OR NEW.source_timezone       IS DISTINCT FROM OLD.source_timezone
    OR NEW.received_at           IS DISTINCT FROM OLD.received_at
    OR NEW.sequence_version      IS DISTINCT FROM OLD.sequence_version
    OR NEW.normalization_version IS DISTINCT FROM OLD.normalization_version
    OR NEW.clock_suspect         IS DISTINCT FROM OLD.clock_suspect
    OR NEW.payload               IS DISTINCT FROM OLD.payload
    OR NEW.admission_kind              IS DISTINCT FROM OLD.admission_kind
    OR NEW.admission_runtime_generation IS DISTINCT FROM OLD.admission_runtime_generation
    OR NEW.resync_generation           IS DISTINCT FROM OLD.resync_generation
    OR NEW.fingerprint_key_version     IS DISTINCT FROM OLD.fingerprint_key_version
  THEN
    RAISE EXCEPTION 'stay_events identity/normalization/admission columns are immutable (append-only)';
  END IF;

  -- Once terminal, the only permitted update is a no-op (no result/lineage field may change).
  IF OLD.processing_status <> 'PENDING' THEN
    IF NEW.processing_status IS DISTINCT FROM OLD.processing_status
       OR NEW.stay_id      IS DISTINCT FROM OLD.stay_id
       OR NEW.processed_at IS DISTINCT FROM OLD.processed_at
       OR NEW.review_code  IS DISTINCT FROM OLD.review_code THEN
      RAISE EXCEPTION 'terminal stay_events row is immutable (status/stay_id/processed_at/review_code frozen)';
    END IF;
    RETURN NEW;
  END IF;

  -- OLD is PENDING and staying PENDING: no result/lineage field may be set yet.
  IF NEW.processing_status = 'PENDING' THEN
    IF NEW.stay_id IS NOT NULL OR NEW.processed_at IS NOT NULL OR NEW.review_code IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events result fields (stay_id/processed_at/review_code) may only be set on PENDING->terminal';
    END IF;
    RETURN NEW;
  END IF;

  -- PENDING -> terminal (one move). A RESYNC row's PUBLICATION is enforced by the consumer against the
  -- interface's published_resync_generation boundary; the row itself stays immutable append-first evidence.
  IF NEW.processing_status NOT IN ('APPLIED','SKIPPED_DUPLICATE','MANUAL_REVIEW','FAILED') THEN
    RAISE EXCEPTION 'invalid stay_events terminal processing_status %', NEW.processing_status;
  END IF;
  IF NEW.processed_at IS NULL THEN
    RAISE EXCEPTION 'stay_events.processed_at is required on PENDING->%', NEW.processing_status;
  END IF;
  -- stay_id lineage: NULL -> a same-interface Stay only
  IF NEW.stay_id IS NOT NULL THEN
    IF OLD.stay_id IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.stay_id may only go from NULL to a resolved Stay';
    END IF;
    SELECT count(*) INTO ok_stay FROM iam_v2.stays s
      WHERE s.id = NEW.stay_id AND s.tenant_id = NEW.tenant_id
        AND s.site_id = NEW.site_id AND s.pms_interface_id = NEW.pms_interface_id;
    IF ok_stay <> 1 THEN
      RAISE EXCEPTION 'stay_events.stay_id must reference a Stay in the same tenant/site/pms_interface';
    END IF;
  END IF;
  -- review_code vocabulary: bounded machine code only (no PII / payload / stack traces)
  IF NEW.review_code IS NOT NULL AND NEW.review_code !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'stay_events.review_code must match ^[A-Z][A-Z0-9_]{0,63}$ (bounded machine code)';
  END IF;
  -- result-specific invariants
  IF NEW.processing_status = 'APPLIED' THEN
    IF NEW.stay_id IS NULL THEN RAISE EXCEPTION 'APPLIED requires a resolved same-interface stay_id'; END IF;
    IF NEW.review_code IS NOT NULL THEN RAISE EXCEPTION 'APPLIED must not carry a review_code'; END IF;
  ELSIF NEW.processing_status = 'MANUAL_REVIEW' THEN
    IF NEW.review_code IS NULL THEN RAISE EXCEPTION 'MANUAL_REVIEW requires a bounded review_code'; END IF;
  ELSIF NEW.processing_status = 'FAILED' THEN
    IF NEW.review_code IS NULL THEN RAISE EXCEPTION 'FAILED requires a bounded review_code'; END IF;
  END IF;
  -- SKIPPED_DUPLICATE: processed_at required (checked); stay_id/review_code optional (validated above).
  RETURN NEW;
END $function$;

CREATE OR REPLACE FUNCTION iam_v2.guest_access_account_removed_is_final()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
  -- RETENTION ANONYMISATION (0127): the table owner, inside a retention run, may rename a removed account to
  -- an anonymised username that is still a removed one.
  IF current_setting('iam_v2.anonymise', true) = 'retention'
     AND current_user = (SELECT pg_get_userbyid(c.relowner) FROM pg_class c WHERE c.oid = TG_RELID)
     AND NOT NEW.enabled AND lower(NEW.username) LIKE '%.removed-%.invalid' THEN
    RETURN NEW;
  END IF;
  IF NOT OLD.enabled AND lower(OLD.username) LIKE '%.removed-%.invalid'
     AND (NEW.enabled OR NEW.username IS DISTINCT FROM OLD.username) THEN
    RAISE EXCEPTION 'client account % has been removed: it cannot be enabled or renamed', OLD.id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 6. Ownership (Gate-P re-asserts it for iam_v2; the public function stays with the platform role).
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.site_data_retention OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.data_retention_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.data_retention_runs OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.data_retention_checkpoints OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.data_retention_defaults() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.data_retention_append_only() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.data_retention_get(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.anonymised_token() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.anonymised_mac() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.anonymise_stay(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.anonymise_device(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.anonymise_guest_principal(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.anonymise_guest_account(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.run_data_retention(uuid,uuid,integer) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.p3_stay_event_appendonly() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_access_account_removed_is_final() OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- New functions are not for PUBLIC.
REVOKE ALL ON FUNCTION iam_v2.data_retention_get(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.anonymised_token() FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.anonymised_mac() FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.anonymise_stay(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.anonymise_device(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.anonymise_guest_principal(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.anonymise_guest_account(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.run_data_retention(uuid,uuid,integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.run_data_retention_public(uuid,uuid,integer) FROM PUBLIC;
REVOKE ALL ON iam_v2.site_data_retention, iam_v2.data_retention_changes, iam_v2.data_retention_runs,
              iam_v2.data_retention_checkpoints FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 7. Least privilege, mirrored in deploy/gatep/svc-data-retention-grants.sql. The anonymise_* primitives are
--    granted to nobody: they run only inside run_data_retention (and, later, the C3 erasure action).
-- ---------------------------------------------------------------------------------------------------------
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.data_retention_get(uuid,uuid) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.run_data_retention(uuid,uuid,integer) TO svc_scd;
    GRANT EXECUTE ON FUNCTION public.run_data_retention_public(uuid,uuid,integer) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.data_retention_get(uuid,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.data_retention_set(uuid,uuid,integer,integer,integer,integer,integer,integer,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.data_retention_changes, iam_v2.data_retention_runs TO svc_edged;
  END IF;
END $g$;

COMMIT;
