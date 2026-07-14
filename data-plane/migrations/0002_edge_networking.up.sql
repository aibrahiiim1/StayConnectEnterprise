-- 0002_edge_networking: Site-local guest-network, VLAN and DHCP management.
--
-- Phase 19. The Site database becomes the source of truth for the appliance's
-- intended guest-network configuration; OS files (netplan/Kea/nftables/unbound)
-- are rendered artifacts produced by netd from these rows. No cloud dependency.
--
-- Design notes baked into this schema:
--   * A guest network is either untagged (a bridge straight over a parent
--     interface) or an 802.1Q VLAN (parent.<vlan> -> bridge). Each gets its
--     own L3 gateway, DHCP scope, portal policy and firewall zone.
--   * Overlapping enabled subnets on one appliance are rejected (no VRF yet),
--     so IP-with-interface uniqueness holds and sessions stay unambiguous.
--   * Every apply is a numbered revision with a full rendered bundle and a
--     safe transactional lifecycle (draft -> validated -> applying ->
--     pending_confirmation -> active | failed | rolled_back | superseded).

BEGIN;

-- ---------------------------------------------------------------------------
-- Physical / logical interface inventory (discovered by netd, role-assigned
-- by operators). Read-mostly; netd refreshes the observed columns.
-- ---------------------------------------------------------------------------
CREATE TABLE network_interfaces (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name          text NOT NULL UNIQUE,           -- e.g. ens192, ens160, bond0
  mac           macaddr,
  role          text NOT NULL DEFAULT 'unused'
                  CHECK (role IN ('management','wan','guest_access','guest_trunk','ha_sync','unused')),
  mode          text NOT NULL DEFAULT 'auto'
                  CHECK (mode IN ('auto','manual','trunk','bridge_slave')),
  parent        text,                            -- for bond members / vlan parents
  vlan_capable  boolean NOT NULL DEFAULT true,
  -- observed (refreshed by netd; never authoritative for intent)
  link_state    text,                            -- up|down|unknown
  speed_mbps    int,
  mtu           int,
  driver        text,
  ip_addresses  jsonb NOT NULL DEFAULT '[]'::jsonb,
  is_protected  boolean NOT NULL DEFAULT false,  -- management/wan guard
  last_seen_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Guest networks. One row per guest L2/L3 domain the appliance serves.
-- ---------------------------------------------------------------------------
CREATE TABLE guest_networks (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id              uuid NOT NULL,
  site_id                uuid NOT NULL,
  appliance_id           uuid,
  name                   text NOT NULL,
  description            text,
  ssid_label             text,                   -- descriptive only; WLAN ctrl owns the SSID
  enabled                boolean NOT NULL DEFAULT true,

  network_type           text NOT NULL DEFAULT 'untagged'
                           CHECK (network_type IN ('untagged','vlan')),
  parent_interface       text NOT NULL,          -- e.g. ens192
  vlan_id                int CHECK (vlan_id IS NULL OR vlan_id BETWEEN 1 AND 4094),
  bridge_name            text NOT NULL,          -- generated, <= 15 chars (IFNAMSIZ)

  gateway_cidr           inet NOT NULL,          -- e.g. 10.20.0.1/22 (host bits kept in gateway_ip)
  gateway_ip             inet NOT NULL,          -- e.g. 10.20.0.1
  subnet_cidr            cidr NOT NULL,          -- e.g. 10.20.0.0/22

  dhcp_mode              text NOT NULL DEFAULT 'local'
                           CHECK (dhcp_mode IN ('local','external','relay','disabled')),
  dns_mode               text NOT NULL DEFAULT 'appliance'
                           CHECK (dns_mode IN ('appliance','custom')),
  dns_servers            jsonb NOT NULL DEFAULT '[]'::jsonb,  -- used when dns_mode=custom
  domain_name            text NOT NULL DEFAULT 'guest.local',
  lease_default_seconds  int NOT NULL DEFAULT 3600,
  lease_min_seconds      int NOT NULL DEFAULT 900,
  lease_max_seconds      int NOT NULL DEFAULT 7200,
  relay_targets          jsonb NOT NULL DEFAULT '[]'::jsonb,  -- for dhcp_mode=relay

  captive_portal_enabled boolean NOT NULL DEFAULT true,
  portal_url             text,                   -- generated http://<gw>:8380/ when local+captive
  internet_access_enabled boolean NOT NULL DEFAULT true,
  nat_enabled            boolean NOT NULL DEFAULT true,
  client_isolation_enabled boolean NOT NULL DEFAULT false,
  walled_garden_profile  text,                   -- reserved for per-network profiles

  created_at             timestamptz NOT NULL DEFAULT now(),
  updated_at             timestamptz NOT NULL DEFAULT now(),

  -- VLAN networks must name a vlan_id; untagged must not.
  CONSTRAINT gn_vlan_consistency CHECK (
    (network_type = 'vlan' AND vlan_id IS NOT NULL) OR
    (network_type = 'untagged' AND vlan_id IS NULL)
  )
);

-- No duplicate active VLAN on the same parent interface.
CREATE UNIQUE INDEX guest_networks_vlan_parent_uniq
  ON guest_networks (parent_interface, vlan_id)
  WHERE enabled AND vlan_id IS NOT NULL;
-- Bridge names are unique.
CREATE UNIQUE INDEX guest_networks_bridge_uniq ON guest_networks (bridge_name);
-- One untagged network per parent interface (untagged frames aren't demuxable).
CREATE UNIQUE INDEX guest_networks_untagged_parent_uniq
  ON guest_networks (parent_interface)
  WHERE enabled AND network_type = 'untagged';
CREATE INDEX guest_networks_enabled_idx ON guest_networks (enabled);

-- ---------------------------------------------------------------------------
-- DHCP address pools (a subnet may have several).
-- ---------------------------------------------------------------------------
CREATE TABLE dhcp_pools (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  guest_network_id uuid NOT NULL REFERENCES guest_networks(id) ON DELETE CASCADE,
  start_ip         inet NOT NULL,
  end_ip           inet NOT NULL,
  sort_order       int NOT NULL DEFAULT 0,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dhcp_pool_order CHECK (start_ip <= end_ip)
);
CREATE INDEX dhcp_pools_network_idx ON dhcp_pools (guest_network_id, sort_order);

-- ---------------------------------------------------------------------------
-- Static DHCP reservations (host reservations rendered into the Kea subnet).
-- ---------------------------------------------------------------------------
CREATE TABLE dhcp_reservations (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  guest_network_id uuid NOT NULL REFERENCES guest_networks(id) ON DELETE CASCADE,
  mac              macaddr NOT NULL,
  reserved_ip      inet NOT NULL,
  hostname         text,
  description      text,
  enabled          boolean NOT NULL DEFAULT true,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (guest_network_id, mac),
  UNIQUE (guest_network_id, reserved_ip)
);

-- ---------------------------------------------------------------------------
-- Network configuration revisions — the transactional apply record. Each
-- revision references a rendered bundle on disk (/etc/stayconnect/generated/
-- network/revision-NNNNNN/) and carries its full lifecycle + audit trail.
-- ---------------------------------------------------------------------------
CREATE TABLE network_config_revisions (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  seq            bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
  state          text NOT NULL DEFAULT 'draft'
                   CHECK (state IN ('draft','validated','applying','pending_confirmation',
                                    'active','failed','rolled_back','superseded')),
  summary        text,                            -- human note (what changed)
  bundle_path    text,                            -- generated dir for this revision
  intent         jsonb NOT NULL DEFAULT '{}'::jsonb, -- snapshot of guest_networks+pools at gen time
  validation     jsonb NOT NULL DEFAULT '{}'::jsonb, -- structured validation results
  previous_seq   bigint,                          -- revision this supersedes/rolls back to
  created_by     uuid,
  validated_by   uuid,
  applied_by     uuid,
  confirmed_by   uuid,
  created_at     timestamptz NOT NULL DEFAULT now(),
  validated_at   timestamptz,
  applied_at     timestamptz,
  confirmed_at   timestamptz,
  confirm_deadline timestamptz,                   -- watchdog rollback point
  failure_reason text
);
CREATE INDEX ncr_state_idx ON network_config_revisions (state, seq DESC);
-- At most one revision may be mid-flight (applying/pending) at a time.
CREATE UNIQUE INDEX ncr_single_inflight
  ON network_config_revisions ((true))
  WHERE state IN ('applying','pending_confirmation');

-- ---------------------------------------------------------------------------
-- Apply events + health checks (observability/audit for each apply attempt).
-- ---------------------------------------------------------------------------
CREATE TABLE network_apply_events (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  revision_id  uuid REFERENCES network_config_revisions(id) ON DELETE CASCADE,
  phase        text NOT NULL,                     -- validate|snapshot|generate|apply|health|commit|rollback
  ok           boolean NOT NULL,
  detail       jsonb NOT NULL DEFAULT '{}'::jsonb,
  at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX nae_revision_idx ON network_apply_events (revision_id, at);

CREATE TABLE network_health_checks (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  revision_id  uuid REFERENCES network_config_revisions(id) ON DELETE CASCADE,
  check_name   text NOT NULL,                     -- mgmt_reachable|gateway_up|kea_running|portal_listen|dns
  ok           boolean NOT NULL,
  detail       text,
  at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX nhc_revision_idx ON network_health_checks (revision_id, at);

-- ---------------------------------------------------------------------------
-- Session network association (Phase 19). Existing sessions get columns so
-- each guest session is unambiguously tied to its network / VLAN / ingress.
-- Nullable + backfilled to the legacy network so nothing breaks mid-migration.
-- ---------------------------------------------------------------------------
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS guest_network_id  uuid;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS vlan_id           int;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS ingress_interface text;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS gateway_ip        inet;
CREATE INDEX IF NOT EXISTS sessions_guest_network_idx ON sessions (guest_network_id) WHERE state = 'active';

INSERT INTO schema_migrations(version) VALUES ('0002_edge_networking')
ON CONFLICT DO NOTHING;

COMMIT;
