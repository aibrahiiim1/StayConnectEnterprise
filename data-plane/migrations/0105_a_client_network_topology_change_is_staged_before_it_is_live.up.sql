-- 0105 — A CLIENT NETWORK'S TOPOLOGY CHANGE IS STAGED BEFORE IT IS LIVE.
--
-- WHAT THIS IS FOR. A hotel moves a LAN port from untagged to a tagged trunk, changes a VLAN id, moves a
-- client network to another port, or re-addresses it. None of that may be edited in place: the applied
-- configuration is keyed on the network's BRIDGE, and a row whose VLAN changed underneath its bridge would be
-- rendered as one thing while the live port carried another. edged refuses such a PUT (immutable_topology) and
-- that refusal stays.
--
-- The supported change is a REPLACEMENT, and a replacement has to be STAGED: writing the new topology must not,
-- by itself, change guest authentication, PMS routing, which client network a device is attributed to, or
-- anything on the wire. Those change only when the operator APPLIES the configuration and the appliance keeps
-- it (validate -> apply -> confirm, with the watchdog rolling back an unconfirmed apply).
--
-- So the request is recorded HERE, as an intent, and nothing in public.guest_networks moves until the apply:
--
--   PENDING    the operator asked for it. Nothing has changed anywhere. It can be cancelled.
--   APPLIED    the apply materialised it: the successor network row exists and is enabled, the original is
--              disabled, and netd has built the new bridge. The appliance is inside its confirmation window.
--   CONFIRMED  the operator kept it. Internet Packages that named the original network were republished
--              forward onto the successor, so eligibility continues to mean what the hotel intended.
--   REVERTED   the apply failed, the operator rolled back, or the confirmation window expired. The successor
--              row is gone, the original is enabled again, and the request is back to PENDING (a new row) or
--              closed. The appliance and this database agree.
--   CANCELLED  the operator dropped the request before it was ever applied.
--
-- WHY HERE AND NOT A COLUMN ON public.guest_networks. Live-site migrations apply as iam_v2_owner, which owns
-- nothing in public (see 0099, which has to defer its public changes to a separate owner-run script). netd
-- holds SELECT on public.guest_networks and nothing in iam_v2, and it must not need to: netd renders what the
-- rows say, and edged is the only writer of those rows. Staging therefore belongs to edged, in iam_v2.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.guest_network_replacements (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL,
  site_id              uuid NOT NULL,

  -- The client network being replaced, and (once materialised) the row that replaces it. No foreign key to
  -- public.guest_networks: iam_v2_owner cannot create one against a table it does not own, and the successor
  -- is deliberately deleted again when an apply is reverted.
  original_network_id  uuid NOT NULL,
  successor_network_id uuid,

  state                text NOT NULL DEFAULT 'PENDING',
  CONSTRAINT gnr_state CHECK (state IN ('PENDING','APPLIED','CONFIRMED','CANCELLED','REVERTED')),

  -- The requested topology. Addressing is optional: NULL means "keep the addressing this network already has",
  -- which is the common case for an untagged -> tagged move.
  network_type         text NOT NULL,
  CONSTRAINT gnr_network_type CHECK (network_type IN ('untagged','vlan')),
  parent_interface     text NOT NULL,
  CONSTRAINT gnr_parent CHECK (length(btrim(parent_interface)) > 0),
  vlan_id              integer,
  CONSTRAINT gnr_vlan_range CHECK (vlan_id IS NULL OR (vlan_id BETWEEN 1 AND 4094)),
  CONSTRAINT gnr_vlan_consistency CHECK (
    (network_type = 'vlan'     AND vlan_id IS NOT NULL) OR
    (network_type = 'untagged' AND vlan_id IS NULL)),
  subnet_cidr          cidr,
  gateway_ip           inet,
  CONSTRAINT gnr_addressing CHECK (
    (subnet_cidr IS NULL AND gateway_ip IS NULL) OR (subnet_cidr IS NOT NULL AND gateway_ip IS NOT NULL)),
  -- The DHCP pools for a new subnet. Only meaningful when the addressing changes; the existing pools are
  -- carried over otherwise.
  pools                jsonb NOT NULL DEFAULT '[]'::jsonb,
  new_name             text,

  reason               text NOT NULL,
  CONSTRAINT gnr_reason CHECK (length(btrim(reason)) >= 4),
  created_by           text NOT NULL,
  CONSTRAINT gnr_actor CHECK (length(btrim(created_by)) > 0),

  -- The netd revision the apply created, so a reconcile can ask what became of it.
  revision_id          uuid,
  created_at           timestamptz NOT NULL DEFAULT now(),
  applied_at           timestamptz,
  settled_at           timestamptz
);

-- ONE LIVE REQUEST PER NETWORK. Two staged replacements of the same network would race to materialise onto the
-- same VLAN/port slot; the second is refused at the source.
CREATE UNIQUE INDEX IF NOT EXISTS gnr_one_live_per_network
  ON iam_v2.guest_network_replacements (original_network_id)
  WHERE state IN ('PENDING','APPLIED');

CREATE INDEX IF NOT EXISTS gnr_site_state
  ON iam_v2.guest_network_replacements (tenant_id, site_id, state, created_at DESC);

-- Ownership, declared rather than inherited from whoever applied this file (see 0089).
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.guest_network_replacements OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- Least privilege. Mirrored in deploy/gatep/svc-edged-phase345-admin-grants.sql so it survives a Gate-P
-- reconcile. edged is the only service that stages, materialises and settles a replacement; scd, netd, acctd
-- and pmsd have no business with it and are granted nothing.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON iam_v2.guest_network_replacements TO svc_edged;
  END IF;
END;
$g$;

COMMIT;
