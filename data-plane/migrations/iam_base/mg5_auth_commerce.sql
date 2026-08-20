-- MG-5  Auth contexts, quotes, purchases, settlements (iam_v2). Single transaction.
-- (auth_contexts.device_id FK is added in MG-6, after devices exists.)
BEGIN;
CREATE TABLE iam_v2.auth_contexts (            -- one-time, TTL 10 min
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  method text NOT NULL CHECK (method IN ('PMS','VOUCHER','ACCOUNT','OTP','SOCIAL','POST_STAY_PIN')),
  stay_id uuid, guest_account_id uuid, voucher_id uuid, guest_principal_id uuid, post_stay_profile_id uuid,
  pms_interface_id uuid, authentication_interface_revision_id uuid,
  device_id uuid NOT NULL, guest_network_id uuid NOT NULL,
  expires_at timestamptz NOT NULL, consumed_at timestamptz,
  CONSTRAINT ac_one_subject CHECK (num_nonnulls(stay_id, guest_account_id, voucher_id, guest_principal_id, post_stay_profile_id) = 1),
  CONSTRAINT ac_method_subject CHECK (
      (method = 'PMS'             AND stay_id IS NOT NULL)
   OR (method = 'VOUCHER'         AND voucher_id IS NOT NULL)
   OR (method = 'ACCOUNT'         AND guest_account_id IS NOT NULL)
   OR (method IN ('OTP','SOCIAL') AND guest_principal_id IS NOT NULL)
   OR (method = 'POST_STAY_PIN'   AND post_stay_profile_id IS NOT NULL)),
  CONSTRAINT ac_pms_pins CHECK (method <> 'PMS'
      OR (pms_interface_id IS NOT NULL AND authentication_interface_revision_id IS NOT NULL)),
  UNIQUE (tenant_id, site_id, id),
  UNIQUE (id, pms_interface_id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, guest_account_id) REFERENCES iam_v2.guest_access_accounts (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, voucher_id) REFERENCES iam_v2.vouchers (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, guest_principal_id) REFERENCES iam_v2.guest_principals (tenant_id, id),
  FOREIGN KEY (tenant_id, site_id, post_stay_profile_id) REFERENCES iam_v2.post_stay_profiles (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, guest_network_id) REFERENCES public.guest_networks (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, authentication_interface_revision_id)
    REFERENCES iam_v2.pms_interface_revisions (tenant_id, site_id, pms_interface_id, id));

CREATE TABLE iam_v2.offer_quotes (             -- one-time, TTL 5 min
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  auth_context_id uuid NOT NULL, package_revision_id uuid NOT NULL,
  pms_interface_id uuid, settlement_mapping_id uuid,
  price_minor bigint NOT NULL, currency char(3), currency_exponent smallint,
  tax_code text, tax_rate_bp int, tax_amount_minor bigint,
  grant_snapshot jsonb NOT NULL,
  expires_at timestamptz NOT NULL, consumed_at timestamptz,
  UNIQUE (tenant_id, site_id, id),
  UNIQUE (id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id),
  FOREIGN KEY (tenant_id, site_id, auth_context_id) REFERENCES iam_v2.auth_contexts (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id, pms_interface_id, settlement_mapping_id)
    REFERENCES iam_v2.package_settlement_mappings (tenant_id, site_id, package_revision_id, pms_interface_id, id));

CREATE TABLE iam_v2.purchases (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  package_revision_id uuid NOT NULL,
  offer_quote_id uuid UNIQUE, auth_context_id uuid,
  pms_interface_id uuid, stay_id uuid, settlement_mapping_id uuid,
  authentication_interface_revision_id uuid,
  trigger text NOT NULL CHECK (trigger IN ('GUEST_SELECTION','VOUCHER_REDEMPTION','ACCOUNT_AUTO_GRANT',
    'OTP_SOCIAL_DEFAULT','CHECKOUT_GRACE','EMERGENCY_GRACE','POST_STAY_CONVERSION',
    'CROSS_PMS_TRANSFER','ADMIN_GRANT','RENEWAL')),
  amount_minor bigint NOT NULL DEFAULT 0 CHECK (amount_minor >= 0),
  currency char(3), currency_exponent smallint,
  tax_code text, tax_rate_bp int, tax_amount_minor bigint,
  state text NOT NULL DEFAULT 'PENDING' CHECK (state IN
    ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW','GRANTED','FAILED','CANCELLED')),
  purchase_seq int NOT NULL DEFAULT 1, checkout_episode int,
  UNIQUE (tenant_id, site_id, id), UNIQUE (id, pms_interface_id),
  CONSTRAINT purchase_guest_needs_quote CHECK (trigger <> 'GUEST_SELECTION' OR offer_quote_id IS NOT NULL),
  FOREIGN KEY (offer_quote_id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id)
    REFERENCES iam_v2.offer_quotes (id, auth_context_id, package_revision_id, pms_interface_id, settlement_mapping_id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, stay_id) REFERENCES iam_v2.stays (tenant_id, site_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id, pms_interface_id, settlement_mapping_id)
    REFERENCES iam_v2.package_settlement_mappings (tenant_id, site_id, package_revision_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id, authentication_interface_revision_id)
    REFERENCES iam_v2.pms_interface_revisions (tenant_id, site_id, pms_interface_id, id));
CREATE UNIQUE INDEX purchase_once_per_stay ON iam_v2.purchases (stay_id, package_revision_id)
  WHERE state IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW','GRANTED') AND trigger='GUEST_SELECTION';
CREATE UNIQUE INDEX one_conversion_per_episode ON iam_v2.purchases (stay_id, checkout_episode)
  WHERE trigger IN ('CHECKOUT_GRACE','EMERGENCY_GRACE','POST_STAY_CONVERSION');

CREATE TABLE iam_v2.settlements (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, purchase_id uuid NOT NULL UNIQUE,
  method text NOT NULL CHECK (method IN ('NOT_REQUIRED','PREPAID','PMS_POSTING','ONLINE_PAYMENT','MANUAL_APPROVAL')),
  status text NOT NULL CHECK (status IN ('NOT_REQUIRED','REQUIRED','IN_PROGRESS','SETTLED','FAILED',
    'MANUAL_REVIEW','PARTIALLY_REVERSED','REVERSED')),
  UNIQUE (id, purchase_id), UNIQUE (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, purchase_id) REFERENCES iam_v2.purchases (tenant_id, site_id, id));
COMMIT;
