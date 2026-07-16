-- MG-3  Guest identities & credentials (iam_v2). Single transaction.
BEGIN;
CREATE TABLE iam_v2.guest_principals (        -- TENANT-WIDE (no site_id)
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, display_name text,
  UNIQUE (tenant_id, id));

CREATE TABLE iam_v2.guest_principal_identities (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, guest_principal_id uuid NOT NULL,
  factor_type text NOT NULL CHECK (factor_type IN ('EMAIL','PHONE','SOCIAL_SUBJECT')),
  factor_issuer text NOT NULL DEFAULT '',
  factor_value_norm text NOT NULL, verified_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT gpi_social_needs_issuer CHECK (factor_type <> 'SOCIAL_SUBJECT' OR factor_issuer <> ''),
  UNIQUE (tenant_id, factor_type, factor_issuer, factor_value_norm),
  FOREIGN KEY (tenant_id, guest_principal_id) REFERENCES iam_v2.guest_principals (tenant_id, id) ON DELETE CASCADE);

CREATE TABLE iam_v2.guest_access_accounts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  username text NOT NULL, password_hash text NOT NULL, display_name text, notes text,
  enabled boolean NOT NULL DEFAULT true, valid_from timestamptz, valid_until timestamptz,
  assigned_package_id uuid, stay_id uuid,
  failed_attempts int NOT NULL DEFAULT 0, locked_until timestamptz,
  last_login_at timestamptz, login_count int NOT NULL DEFAULT 0,
  UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, assigned_package_id) REFERENCES iam_v2.internet_packages (tenant_id, site_id, id));
CREATE UNIQUE INDEX gaa_username ON iam_v2.guest_access_accounts (tenant_id, lower(username));

CREATE TABLE iam_v2.voucher_code_key_generations (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, generation_no int NOT NULL,
  hmac_key_ciphertext bytea NOT NULL, aead_params jsonb NOT NULL,
  encryption_key_id uuid NOT NULL, superseded_at timestamptz,
  UNIQUE (tenant_id, generation_no), UNIQUE (tenant_id, site_id, id));

CREATE TABLE iam_v2.voucher_batches (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  package_revision_id uuid NOT NULL, label text,
  UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, id));

CREATE TABLE iam_v2.vouchers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  batch_id uuid, package_revision_id uuid NOT NULL,
  code_hmac bytea NOT NULL,
  code_ciphertext bytea NOT NULL, code_nonce bytea NOT NULL,
  code_key_generation_id uuid NOT NULL, code_last4 text NOT NULL,
  state text NOT NULL DEFAULT 'UNUSED' CHECK (state IN ('UNUSED','REDEEMED','REVOKED','REDEMPTION_EXPIRED')),
  redemption_valid_from timestamptz, redemption_valid_until timestamptz, notes text,
  UNIQUE (code_hmac), UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, code_key_generation_id) REFERENCES iam_v2.voucher_code_key_generations (tenant_id, site_id, id));
COMMIT;
