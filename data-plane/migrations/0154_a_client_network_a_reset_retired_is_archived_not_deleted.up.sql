-- 0154 — A CLIENT NETWORK THAT A CONFIRMED RESET RETIRED IS ARCHIVED, NOT DELETED.
--
-- Restore Factory Defaults and Full Client Networking Reset switch every other client network off. Those networks keep their rows because their identity is
-- referenced by sign-ins, device appearances, resolutions, verification grants, the PMS routing, package rules, sign-in restrictions and pauses, the audit trail
-- and rollback (see docs/CLIENT_NETWORKING.md section 15). What the Product Owner approved on 2026-10-25 is that, once the reset has been APPLIED and CONFIRMED,
-- they are marked ARCHIVED and hidden from Client networks behind a collapsed control with their count, so the main screen shows the Default Network.
--
-- Archiving is a PRESENTATION state only. It moves no data, deletes nothing, and changes nothing a guest, an entitlement, a package rule or the PMS sees. It is
-- written ONLY when the operator confirms the reset (inside the same transaction that settles the change), so a failed apply, a rollback, a lapsed confirmation
-- or a cancelled staging can never leave a network archived. Restoring a network from the archive removes the marker and leaves the network switched off; enabling
-- a network also removes it.
--
-- One row per archived network, kept as a row rather than a flag on public.guest_networks because live-site migrations apply as iam_v2_owner, which owns nothing in
-- public (see 0105, 0151). No free text and no operator identity: the audit log says who and why. group_id names the reset that retired the network.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.network_archive (
  network_id  uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  site_id     uuid NOT NULL,
  group_id    uuid,
  group_kind  text NOT NULL,
  CONSTRAINT na_group_kind CHECK (group_kind IN ('FACTORY_RESET','FULL_RESET')),
  archived_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS na_site ON iam_v2.network_archive (tenant_id, site_id);

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.network_archive OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON iam_v2.network_archive TO svc_edged;
  END IF;
END;
$g$;

-- A reset that was CONFIRMED before this migration retired its networks without archiving them. Archive them now, from the recorded facts alone: the networks a
-- confirmed Restore Factory Defaults or Full Reset switched off, and that nothing has touched since. Idempotent. Whether a network is still switched off is
-- checked when the archive is read (iam_v2_owner cannot read public.guest_networks), and the marker is dropped the moment the network serves again.
INSERT INTO iam_v2.network_archive (network_id, tenant_id, site_id, group_id, group_kind, archived_at)
SELECT DISTINCT ON (c.network_id) c.network_id, c.tenant_id, c.site_id, c.group_id, c.group_kind, COALESCE(c.settled_at, now())
  FROM iam_v2.guest_network_changes c
 WHERE c.state = 'CONFIRMED' AND c.kind = 'DISABLE' AND c.group_kind IN ('FACTORY_RESET','FULL_RESET')
   AND NOT EXISTS (SELECT 1 FROM iam_v2.guest_network_changes later
                    WHERE later.network_id = c.network_id AND later.created_at > c.created_at AND later.state <> 'CANCELLED')
 ORDER BY c.network_id, c.settled_at DESC NULLS LAST
ON CONFLICT (network_id) DO NOTHING;

COMMIT;
