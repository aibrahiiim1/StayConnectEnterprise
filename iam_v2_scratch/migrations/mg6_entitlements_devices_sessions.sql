-- MG-6  Entitlements, transfers, devices, sessions, accounting (iam_v2). Single transaction.
BEGIN;
CREATE TABLE iam_v2.devices (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, appliance_id uuid NOT NULL, mac macaddr NOT NULL,
  first_seen timestamptz, last_seen timestamptz, last_ip inet,
  UNIQUE (tenant_id, site_id, appliance_id, mac), UNIQUE (tenant_id, site_id, id));

-- deferred FK from MG-5 (auth_contexts.device_id -> devices)
ALTER TABLE iam_v2.auth_contexts
  ADD FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices (tenant_id, site_id, id);

CREATE TABLE iam_v2.device_network_appearances (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  device_id uuid NOT NULL, guest_network_id uuid NOT NULL,
  first_seen timestamptz, last_seen timestamptz,
  PRIMARY KEY (device_id, guest_network_id),
  FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, guest_network_id) REFERENCES public.guest_networks (tenant_id, site_id, id));

CREATE TABLE iam_v2.entitlements (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  stay_id uuid, guest_account_id uuid, voucher_id uuid, guest_principal_id uuid,
  pms_interface_id uuid,
  purchase_id uuid NOT NULL UNIQUE,
  policy_snapshot jsonb NOT NULL, snapshot_version int NOT NULL DEFAULT 1,
  service_plan_revision_id uuid NOT NULL, package_revision_id uuid NOT NULL,
  time_accounting_mode text NOT NULL,
  end_mode text NOT NULL CHECK (end_mode IN ('FIXED_AT','VALIDITY_WINDOW','AT_CHECKOUT',
    'EARLIEST_OF_FIXED_AND_CHECKOUT','GRACE_AFTER_CHECKOUT','MANUAL_END')),
  window_ends_at timestamptz,
  status text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','ACTIVE','SUSPENDED','TERMINATED')),
  terminal_reason text CHECK (terminal_reason IN ('TIME','DATA','HARD_EXPIRY','CHECKOUT','ADMIN',
    'REVOKED','SUPERSEDED','CONVERTED','TRANSFERRED','CANCELLED','OTHER')),
  consumed_data_bytes bigint NOT NULL DEFAULT 0 CHECK (consumed_data_bytes >= 0),
  consumed_online_seconds bigint NOT NULL DEFAULT 0 CHECK (consumed_online_seconds >= 0),
  usage_version bigint NOT NULL DEFAULT 0, renewal_number int NOT NULL DEFAULT 1,
  supersedes_entitlement_id uuid UNIQUE,
  is_emergency_grace boolean NOT NULL DEFAULT false,
  activated_at timestamptz, terminated_at timestamptz,
  CONSTRAINT ent_one_subject CHECK (num_nonnulls(stay_id, guest_account_id, voucher_id, guest_principal_id) = 1),
  CONSTRAINT ent_terminal CHECK ((status='TERMINATED') = (terminal_reason IS NOT NULL)),
  UNIQUE (tenant_id, id), UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, purchase_id) REFERENCES iam_v2.purchases (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, supersedes_entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays (tenant_id, site_id, pms_interface_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, guest_account_id) REFERENCES iam_v2.guest_access_accounts (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, voucher_id) REFERENCES iam_v2.vouchers (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, guest_principal_id) REFERENCES iam_v2.guest_principals (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, service_plan_revision_id) REFERENCES iam_v2.service_plan_revisions (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, id));
-- ONE LIVE ENTITLEMENT PER SUBJECT (per-site for principals)
CREATE UNIQUE INDEX ent_live_stay      ON iam_v2.entitlements (stay_id)                     WHERE status IN ('PENDING','ACTIVE','SUSPENDED');
CREATE UNIQUE INDEX ent_live_account   ON iam_v2.entitlements (guest_account_id)            WHERE status IN ('PENDING','ACTIVE','SUSPENDED');
CREATE UNIQUE INDEX ent_live_voucher   ON iam_v2.entitlements (voucher_id)                  WHERE status IN ('PENDING','ACTIVE','SUSPENDED');
CREATE UNIQUE INDEX ent_live_principal ON iam_v2.entitlements (guest_principal_id, site_id) WHERE status IN ('PENDING','ACTIVE','SUSPENDED');

CREATE TABLE iam_v2.entitlement_adjustments (   -- SOLE way counters decrease / windows move
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, entitlement_id uuid NOT NULL,
  field text NOT NULL, old_value text, new_value text, actor uuid, reason text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id));

CREATE TABLE iam_v2.entitlement_transfers (     -- typed, cycle-safe cross-PMS lineage
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  from_entitlement_id uuid NOT NULL UNIQUE, to_entitlement_id uuid NOT NULL UNIQUE,
  from_stay_id uuid NOT NULL, to_stay_id uuid NOT NULL,
  reason text NOT NULL DEFAULT 'CROSS_PMS_TRANSFER', actor uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT et_no_self CHECK (from_entitlement_id <> to_entitlement_id),
  CONSTRAINT et_two_stays CHECK (from_stay_id <> to_stay_id),
  FOREIGN KEY (tenant_id, site_id, from_entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, to_entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, from_stay_id) REFERENCES iam_v2.stays (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, to_stay_id) REFERENCES iam_v2.stays (tenant_id, site_id, id));

CREATE TABLE iam_v2.entitlement_devices (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  entitlement_id uuid NOT NULL, device_id uuid NOT NULL,
  status text NOT NULL DEFAULT 'AUTHORIZED' CHECK (status IN ('AUTHORIZED','DISCONNECTED')),
  grandfathered boolean NOT NULL DEFAULT false,
  disconnected_reason text, first_authorized timestamptz, last_authorized timestamptz,
  PRIMARY KEY (entitlement_id, device_id),
  FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices (tenant_id, site_id, id));

CREATE TABLE iam_v2.sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  entitlement_id uuid NOT NULL, device_id uuid NOT NULL,
  credential_method text, ip inet, mac macaddr,
  state text NOT NULL DEFAULT 'active', started timestamptz NOT NULL DEFAULT now(), ended timestamptz,
  end_reason text, expires_at timestamptz, bytes_up bigint NOT NULL DEFAULT 0, bytes_down bigint NOT NULL DEFAULT 0,
  ingress_interface text,
  UNIQUE (tenant_id, id), UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, entitlement_id) REFERENCES iam_v2.entitlements (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, device_id) REFERENCES iam_v2.devices (tenant_id, site_id, id));

CREATE TABLE iam_v2.accounting_records (        -- append-only usage ledger
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, session_id uuid NOT NULL,
  sample_seq bigint NOT NULL, bytes_up bigint NOT NULL DEFAULT 0, bytes_down bigint NOT NULL DEFAULT 0,
  sampled_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (session_id, sample_seq),
  FOREIGN KEY (tenant_id, site_id, session_id) REFERENCES iam_v2.sessions (tenant_id, site_id, id));

CREATE TABLE iam_v2.session_counter_watermarks (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  session_id uuid PRIMARY KEY,
  source_epoch int NOT NULL DEFAULT 1,
  last_up bigint NOT NULL DEFAULT 0, last_down bigint NOT NULL DEFAULT 0,
  sample_seq bigint NOT NULL DEFAULT 0, updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, session_id) REFERENCES iam_v2.sessions (tenant_id, site_id, id) ON DELETE CASCADE);
COMMIT;
