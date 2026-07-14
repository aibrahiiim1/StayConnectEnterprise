-- 0040_simple_license_model
--
-- FINAL SIMPLE LICENSE MODEL: a license is bound to exactly one appliance and
-- carries only (a) max concurrent online guests, (b) validity window
-- valid_from..valid_until, (c) an explicit grace period. license_version is a
-- per-appliance/site MONOTONIC sequence used by the appliance's anti-rollback
-- store: any document with a lower version than the highest it has accepted is
-- rejected (LICENSE_ROLLBACK_REJECTED), so a superseded/replayed license can
-- never regain authority.
--
-- Plans / subscriptions / plan limits / tenant overrides leave the NORMAL
-- licensing workflow. Historical rows are preserved read-only (plans are
-- retired: not selectable for anything new, still visible in history/audit).

BEGIN;

ALTER TABLE licenses ADD COLUMN IF NOT EXISTS license_version bigint NOT NULL DEFAULT 0;
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS max_concurrent_online_guests int NOT NULL DEFAULT 0;
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS grace_period_days int NOT NULL DEFAULT 0;
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS supersedes_license_id uuid;
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS valid_from timestamptz;

-- Backfill: versions follow issuance order per site (matches the per-site
-- advisory-lock scope used at issue time); terms map from the legacy columns.
WITH ranked AS (
  SELECT id, row_number() OVER (PARTITION BY site_id ORDER BY issued_at, created_at) AS rn
    FROM licenses
)
UPDATE licenses l SET license_version = r.rn
  FROM ranked r WHERE l.id = r.id AND l.license_version = 0;

UPDATE licenses SET grace_period_days = offline_grace_days WHERE grace_period_days = 0;
UPDATE licenses
   SET max_concurrent_online_guests = COALESCE((limits->>'max_concurrent_guest_sessions')::int, 0)
 WHERE max_concurrent_online_guests = 0;
UPDATE licenses SET valid_from = issued_at WHERE valid_from IS NULL;

-- One CURRENT license per site, enforced by the database itself: concurrent
-- issuance can never leave two active/suspended rows.
CREATE UNIQUE INDEX IF NOT EXISTS licenses_one_current_per_site
    ON licenses (site_id) WHERE status IN ('active','suspended');

-- Monotonic versions are unique within a site's history.
CREATE UNIQUE INDEX IF NOT EXISTS licenses_site_version_unique
    ON licenses (site_id, license_version);

-- Retire the plan catalog from the normal workflow (read-only history).
UPDATE plans SET is_active = false, is_public = false, updated_at = now();

INSERT INTO schema_migrations(version) VALUES ('0040_simple_license_model') ON CONFLICT DO NOTHING;

COMMIT;
