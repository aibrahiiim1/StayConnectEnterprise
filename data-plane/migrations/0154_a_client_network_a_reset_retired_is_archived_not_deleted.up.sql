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

COMMIT;
