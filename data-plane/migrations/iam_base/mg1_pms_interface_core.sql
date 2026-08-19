-- MG-1  PMS interface core (iam_v2). Single transaction.
BEGIN;
CREATE SCHEMA IF NOT EXISTS iam_v2;

CREATE TABLE iam_v2.pms_interfaces (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  connector_kind text NOT NULL, display_label text,
  lifecycle_state text NOT NULL DEFAULT 'ACTIVE'
    CHECK (lifecycle_state IN ('ACTIVE','AUTH_DISABLED','DRAINING','DECOMMISSIONED')),
  current_revision_id uuid,
  UNIQUE (tenant_id, site_id, id));

CREATE TABLE iam_v2.pms_interface_revisions (            -- append-only (trigger MG-9)
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  revision_no int NOT NULL,
  source_timezone text NOT NULL,
  -- FOLIO fail-closed amendment (PO-approved): default 'UNSET' blocks CHARGE until onboarding.
  folio_identity_strategy text NOT NULL DEFAULT 'UNSET'
    CHECK (folio_identity_strategy IN ('UNSET','GLOBALLY_UNIQUE','UNIQUE_PER_STAY','REUSED_SEQUENTIAL')),
  config jsonb NOT NULL,
  normalization_version int NOT NULL DEFAULT 1, source_fingerprint text,
  UNIQUE (pms_interface_id, revision_no),
  UNIQUE (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id));
ALTER TABLE iam_v2.pms_interfaces ADD FOREIGN KEY (tenant_id, site_id, id, current_revision_id)
  REFERENCES iam_v2.pms_interface_revisions (tenant_id, site_id, pms_interface_id, id);

CREATE TABLE iam_v2.pms_interface_secret_generations (   -- append-only AEAD (trigger MG-9)
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  generation_no int NOT NULL,
  ciphertext bytea NOT NULL, nonce bytea NOT NULL,
  encryption_key_id uuid NOT NULL, cipher_version int NOT NULL, superseded_at timestamptz,
  UNIQUE (pms_interface_id, generation_no),
  UNIQUE (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id));

CREATE TABLE iam_v2.guest_network_pms_map (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  guest_network_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  is_default boolean NOT NULL DEFAULT false,
  routing_mode text NOT NULL DEFAULT 'MAPPED' CHECK (routing_mode IN ('MAPPED','ALL_ACTIVE_INTERFACES')),
  PRIMARY KEY (guest_network_id, pms_interface_id),
  FOREIGN KEY (tenant_id, site_id, guest_network_id)
    REFERENCES public.guest_networks (tenant_id, site_id, id) ON DELETE CASCADE,   -- cross-schema anchor (MG-0)
  FOREIGN KEY (tenant_id, site_id, pms_interface_id)
    REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id));
CREATE UNIQUE INDEX gnpm_one_default ON iam_v2.guest_network_pms_map (guest_network_id) WHERE is_default;

CREATE TABLE iam_v2.pms_interface_pnumber_seq (          -- durable atomic per-interface P#
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  next_p_number bigint NOT NULL DEFAULT 1,
  PRIMARY KEY (pms_interface_id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id));

CREATE TABLE iam_v2.pms_source_conflicts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  interface_a uuid NOT NULL, interface_b uuid NOT NULL,
  severity text, resolution text,
  CONSTRAINT psc_order CHECK (interface_a < interface_b),
  UNIQUE (tenant_id, site_id, interface_a, interface_b),
  FOREIGN KEY (tenant_id, site_id, interface_a) REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, interface_b) REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id));
COMMIT;
