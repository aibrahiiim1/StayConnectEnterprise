-- 0111 -- A CODE REQUEST EARNS A SHORT, METERED INTERNET WINDOW.
--
-- Product-Owner decision of 2026-10-11 (PR #211): when a Client asks for an email code and the code is issued,
-- the requesting device gets temporary general internet access -- so the code can be read in any mail app --
-- bounded by a site setting (default 10 minutes, 2 Mbps down, 1 Mbps up, 25 MB combined). The kernel enforces
-- it (internal/nft/verify.go); this table is the authority it is restored from after a restart, and the
-- ledger the abuse limits are counted against (per device and per destination, rolling 24 hours).
--
-- It is NOT an entitlement and touches none: no package, purchase, quota or session row is written for it.
-- The destination is kept only as a keyed digest (HMAC under the appliance's anonymous-access key), never
-- the address. MAC identifies the device only -- never a person.
--
-- Additive: one table and its indexes; no existing row is touched.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.verification_access_grants (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  guest_network_id   uuid,
  device_mac         macaddr NOT NULL,
  ip                 inet NOT NULL,
  bridge             text NOT NULL CHECK (bridge ~ '^[A-Za-z0-9_.-]{1,15}$'),
  challenge_id       uuid,
  destination_digest text NOT NULL CHECK (destination_digest ~ '^[0-9a-f]{64}$'),
  started_at         timestamptz NOT NULL DEFAULT now(),
  expires_at         timestamptz NOT NULL,
  data_cap_bytes     bigint NOT NULL CHECK (data_cap_bytes > 0),
  down_kbps          integer NOT NULL CHECK (down_kbps > 0),
  up_kbps            integer NOT NULL CHECK (up_kbps > 0),
  used_bytes         bigint NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
  ended_at           timestamptz,
  end_reason         text CHECK (end_reason IN ('VERIFIED','SIGNED_IN','EXPIRED','DATA_EXHAUSTED','CANCELLED','ADMIN','RESTORE_FAILED')),
  CHECK (expires_at > started_at),
  CHECK ((ended_at IS NULL) = (end_reason IS NULL))
);
-- One live window per device: a second request while one is open reuses it, never resets it.
CREATE UNIQUE INDEX IF NOT EXISTS verification_access_one_live_per_device
  ON iam_v2.verification_access_grants (tenant_id, site_id, device_mac) WHERE ended_at IS NULL;
CREATE INDEX IF NOT EXISTS verification_access_device_window
  ON iam_v2.verification_access_grants (tenant_id, site_id, device_mac, started_at);
CREATE INDEX IF NOT EXISTS verification_access_destination_window
  ON iam_v2.verification_access_grants (tenant_id, site_id, destination_digest, started_at);
COMMENT ON TABLE iam_v2.verification_access_grants IS
  'Temporary internet windows earned by an issued email code (contract §6.6). scd writes and ends them and '
  'restores the kernel from the live rows; abuse limits count rows per device and per destination digest over '
  'a rolling 24 hours. Not an entitlement.';

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT, INSERT, UPDATE ON iam_v2.verification_access_grants TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    -- the Admin Console reports who is on a temporary window; it never writes one
    GRANT SELECT ON iam_v2.verification_access_grants TO svc_edged;
  END IF;
END $g$;

COMMIT;
