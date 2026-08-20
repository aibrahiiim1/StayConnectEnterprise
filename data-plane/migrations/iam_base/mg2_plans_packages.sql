-- MG-2  Plans & packages (iam_v2). Single transaction.
BEGIN;
CREATE TABLE iam_v2.service_plans (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  code text NOT NULL, enabled boolean NOT NULL DEFAULT true,
  current_revision_id uuid,
  UNIQUE (tenant_id, site_id, code), UNIQUE (tenant_id, site_id, id));

CREATE TABLE iam_v2.service_plan_revisions (            -- append-only
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, service_plan_id uuid NOT NULL,
  revision_no int NOT NULL, name text,
  down_kbps int, up_kbps int,
  max_concurrent_devices int NOT NULL DEFAULT 1 CHECK (max_concurrent_devices >= 1),
  device_limit_policy text NOT NULL DEFAULT 'REJECT_NEW_DEVICE'
    CHECK (device_limit_policy IN ('REJECT_NEW_DEVICE','DISCONNECT_OLDEST','ADMIN_APPROVAL')),
  idle_timeout_seconds int, max_continuous_session_seconds int,
  time_accounting_mode text NOT NULL DEFAULT 'VALIDITY_WINDOW'
    CHECK (time_accounting_mode IN ('VALIDITY_WINDOW','AGGREGATE_ONLINE_TIME')),   -- AGGREGATE inert in v1
  time_quota_seconds bigint, data_quota_bytes bigint,
  UNIQUE (service_plan_id, revision_no), UNIQUE (tenant_id, site_id, id),
  UNIQUE (tenant_id, site_id, service_plan_id, id),
  FOREIGN KEY (tenant_id, site_id, service_plan_id) REFERENCES iam_v2.service_plans (tenant_id, site_id, id));
ALTER TABLE iam_v2.service_plans ADD FOREIGN KEY (tenant_id, site_id, id, current_revision_id)
  REFERENCES iam_v2.service_plan_revisions (tenant_id, site_id, service_plan_id, id);

CREATE TABLE iam_v2.internet_packages (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  code text NOT NULL, active boolean NOT NULL DEFAULT true, is_system boolean NOT NULL DEFAULT false,
  current_revision_id uuid, central_template_id uuid,   -- Central template OUT OF SCOPE in 1A: inert/NULL
  UNIQUE (tenant_id, site_id, code), UNIQUE (tenant_id, site_id, id));

CREATE TABLE iam_v2.internet_package_revisions (        -- append-only
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, package_id uuid NOT NULL,
  revision_no int NOT NULL,
  service_plan_revision_id uuid NOT NULL,
  package_type text NOT NULL CHECK (package_type IN
    ('FREE_STAY','ONE_DAY','REST_OF_STAY','POST_STAY','GENERAL','CHECKOUT_GRACE')),
  price_minor bigint NOT NULL DEFAULT 0 CHECK (price_minor >= 0),
  currency char(3), currency_exponent smallint,
  settlement_methods text[] NOT NULL DEFAULT '{NOT_REQUIRED}',
  duration_policy jsonb NOT NULL DEFAULT '{}', plan_overrides jsonb,
  renewable boolean NOT NULL DEFAULT false, max_purchases_per_stay int,
  display jsonb, visible_from timestamptz, visible_until timestamptz,
  UNIQUE (package_id, revision_no), UNIQUE (tenant_id, site_id, id),
  UNIQUE (tenant_id, site_id, package_id, id),
  FOREIGN KEY (tenant_id, site_id, package_id) REFERENCES iam_v2.internet_packages (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, service_plan_revision_id) REFERENCES iam_v2.service_plan_revisions (tenant_id, site_id, id));
ALTER TABLE iam_v2.internet_packages ADD FOREIGN KEY (tenant_id, site_id, id, current_revision_id)
  REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, package_id, id);

CREATE TABLE iam_v2.package_eligibility_rules (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, package_revision_id uuid NOT NULL,
  rule_type text NOT NULL, rule_value jsonb NOT NULL DEFAULT '{}',
  FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, id) ON DELETE CASCADE);

CREATE TABLE iam_v2.package_grant_tiers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL, package_revision_id uuid NOT NULL,
  tier_order int NOT NULL, grant_value jsonb NOT NULL DEFAULT '{}',
  UNIQUE (package_revision_id, tier_order),
  FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, id) ON DELETE CASCADE);

CREATE TABLE iam_v2.package_settlement_mappings (       -- append-only linear chains
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  package_revision_id uuid NOT NULL, pms_interface_id uuid NOT NULL,
  mapping_revision int NOT NULL,
  posting_code text NOT NULL, tax_code text, tax_rate_bp int,
  retired_at timestamptz, replaces_mapping_id uuid,
  UNIQUE (package_revision_id, pms_interface_id, mapping_revision),
  UNIQUE (tenant_id, site_id, package_revision_id, pms_interface_id, id),
  FOREIGN KEY (tenant_id, site_id, package_revision_id) REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, id),
  FOREIGN KEY (tenant_id, site_id, pms_interface_id) REFERENCES iam_v2.pms_interfaces (tenant_id, site_id, id));

CREATE TABLE iam_v2.site_checkout_grace_config (
  tenant_id uuid NOT NULL, site_id uuid NOT NULL,
  grace_package_revision_id uuid,
  config jsonb NOT NULL DEFAULT '{}',
  PRIMARY KEY (tenant_id, site_id),
  FOREIGN KEY (tenant_id, site_id, grace_package_revision_id) REFERENCES iam_v2.internet_package_revisions (tenant_id, site_id, id));
COMMIT;
