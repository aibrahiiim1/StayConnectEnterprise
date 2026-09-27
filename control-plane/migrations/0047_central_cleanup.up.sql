-- CENTRAL CLEANUP: DELETE THE ARCHIVE, DROP WHAT NOTHING READS, REMEMBER RETIRED IDENTITIES.
--
-- Product-Owner decision (2026-09-27): Central's data is disposable PRE-LIVE/test data and legacy history need
-- not be preserved. So, unlike 0046, this migration DELETES DATA on purpose:
--
-- 1. SCHEMA legacy_archive IS DROPPED WITH EVERYTHING IN IT. On the live Central it holds the commercial
--    history 0046 moved there (plans, plan_limits, plan_limit_history, subscription_events) and the
--    guest/session/voucher/accounting history and classification tables of an older cleanup. Hypertables in
--    it are dropped first through TimescaleDB so their chunks go with them. A guard refuses to run if any
--    object OUTSIDE the schema (a view, a foreign key) depends on something inside it: CASCADE may take the
--    archive, never a live table.
--
-- 2. COLUMNS NO CODE READS OR WRITES ANY MORE are dropped (verified by grepping control-plane/ at this commit):
--      operators.auth_method, operators.oidc_sub, operators.last_sso_login_at   operator SSO (removed)
--      tenants.auth_methods                                                   guest sign-in methods (appliance-side)
--      tenants.contact_email, tenants.metadata, sites.metadata               never read
--      appliances.metadata, appliances.name                                  never read (name was a copy of serial)
--      appliances.cert_fingerprint                                           duplicate of current_cert_fingerprint
--      appliances.identity_verified_at                                       write-only; last_seen_at is the fact
--      operator_roles.site_id                                                site-scoped roles (removed)
--      licenses.commercial_plan_code, licenses.features, licenses.limits     plans (removed)
--    THE SIGNED LICENCE DOCUMENT IS UNCHANGED. Its commercial_plan_code/features/limits fields are still signed
--    into every envelope (license/doc.go) because deployed appliances verify that exact format; only the
--    database copies nobody read are gone. licenses.signed_envelope still holds the full signed document.
--
-- 3. appliances.enrolled_at is RENAMED registered_at: enrollment tokens no longer exist; the column records
--    when the appliance registered itself.
--
-- 4. NEW: retired_appliance_identities. A retired identity key must never come back as a WAITING appliance
--    that could be activated for someone else: the box still holds whatever its previous customer left on it.
--    The key is recorded when a retired appliance record is deleted and when a factory-reset box replaces a
--    retired row's key; registration and offline import refuse a recorded key. Changing an appliance's
--    customer is: Retire -> factory-reset the box (new identity key) -> it registers -> Activate.
--
-- 5. appliance_assignment_fetch_log is truncated (30-second poll diagnostics; retention keeps it bounded).
--
-- NOT TOUCHED: audit_log, appliance_lifecycle_events, appliance_security_alerts, certificate and assignment
-- history — they are live features.
--
-- The migration ledger row is written by deploy/scripts/central-migrate.sh, not here.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. legacy_archive
-- ---------------------------------------------------------------------------------------------------------
DO $$
DECLARE
    n   bigint;
    tbl text;
BEGIN
    IF to_regnamespace('legacy_archive') IS NULL THEN
        RETURN;
    END IF;
    -- A view or foreign key that lives outside the archive but depends on it would be dropped by CASCADE.
    SELECT count(*) INTO n
      FROM pg_depend d
      JOIN pg_class ref ON ref.oid = d.refobjid AND d.refclassid = 'pg_class'::regclass
      LEFT JOIN pg_rewrite rw ON d.classid = 'pg_rewrite'::regclass AND rw.oid = d.objid
      LEFT JOIN pg_class vc ON vc.oid = rw.ev_class
      LEFT JOIN pg_constraint co ON d.classid = 'pg_constraint'::regclass AND co.oid = d.objid
      LEFT JOIN pg_class cc ON cc.oid = co.conrelid
     WHERE ref.relnamespace = to_regnamespace('legacy_archive')
       AND COALESCE(vc.oid, cc.oid) IS NOT NULL
       AND COALESCE(vc.oid, cc.oid) <> ref.oid
       AND COALESCE(vc.relnamespace, cc.relnamespace) <> to_regnamespace('legacy_archive')
       AND COALESCE(vc.relnamespace, cc.relnamespace)::regnamespace::text NOT LIKE '\_timescaledb%';
    IF n > 0 THEN
        RAISE EXCEPTION '0047_central_cleanup refuses to run: % object(s) outside legacy_archive depend on it and would be dropped with it. Remove that dependency deliberately first.', n;
    END IF;
    IF to_regclass('_timescaledb_catalog.hypertable') IS NOT NULL THEN
        FOR tbl IN SELECT format('%I.%I', schema_name, table_name)
                     FROM _timescaledb_catalog.hypertable WHERE schema_name = 'legacy_archive' LOOP
            EXECUTE 'DROP TABLE ' || tbl || ' CASCADE';
        END LOOP;
    END IF;
END $$;

DROP SCHEMA IF EXISTS legacy_archive CASCADE;

-- ---------------------------------------------------------------------------------------------------------
-- 2. Columns nothing reads
-- ---------------------------------------------------------------------------------------------------------
DROP INDEX IF EXISTS operators_oidc_sub_uniq;
ALTER TABLE operators DROP CONSTRAINT IF EXISTS operators_auth_method_check;
ALTER TABLE operators DROP COLUMN IF EXISTS auth_method;
ALTER TABLE operators DROP COLUMN IF EXISTS oidc_sub;
ALTER TABLE operators DROP COLUMN IF EXISTS last_sso_login_at;

ALTER TABLE tenants DROP COLUMN IF EXISTS auth_methods;
ALTER TABLE tenants DROP COLUMN IF EXISTS contact_email;
ALTER TABLE tenants DROP COLUMN IF EXISTS metadata;
ALTER TABLE sites   DROP COLUMN IF EXISTS metadata;

ALTER TABLE appliances DROP COLUMN IF EXISTS metadata;
ALTER TABLE appliances DROP COLUMN IF EXISTS name;
ALTER TABLE appliances DROP COLUMN IF EXISTS cert_fingerprint;
ALTER TABLE appliances DROP COLUMN IF EXISTS identity_verified_at;

DROP INDEX IF EXISTS operator_roles_site_idx;
ALTER TABLE operator_roles DROP COLUMN IF EXISTS site_id;

ALTER TABLE licenses DROP COLUMN IF EXISTS commercial_plan_code;
ALTER TABLE licenses DROP COLUMN IF EXISTS features;
ALTER TABLE licenses DROP COLUMN IF EXISTS limits;

-- ---------------------------------------------------------------------------------------------------------
-- 3. enrolled_at -> registered_at
-- ---------------------------------------------------------------------------------------------------------
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'appliances' AND column_name = 'enrolled_at') THEN
        ALTER TABLE appliances RENAME COLUMN enrolled_at TO registered_at;
    END IF;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. Retired identity keys
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS retired_appliance_identities (
    public_key   text PRIMARY KEY,           -- base64 Ed25519 identity key, as stored in appliances.public_key
    serial       text NOT NULL,
    appliance_id uuid NOT NULL,              -- the record it belonged to (may since have been deleted)
    retired_at   timestamptz NOT NULL DEFAULT now(),
    reason       text NOT NULL
);
COMMENT ON TABLE retired_appliance_identities IS
    'Identity keys of retired appliances. Registration and offline import refuse them: a retired box must be factory-reset (new key) before it can be activated again.';

-- Keys of appliances that are ALREADY retired are recorded now, so deleting their record later cannot free
-- the key.
INSERT INTO retired_appliance_identities (public_key, serial, appliance_id, reason)
SELECT public_key, serial, id, 'retired before 0047'
  FROM appliances
 WHERE lifecycle_state IN ('revoked', 'decommissioned') AND COALESCE(public_key, '') <> ''
ON CONFLICT (public_key) DO NOTHING;

-- ---------------------------------------------------------------------------------------------------------
-- 5. Fetch-log diagnostics
-- ---------------------------------------------------------------------------------------------------------
TRUNCATE appliance_assignment_fetch_log;

COMMIT;
