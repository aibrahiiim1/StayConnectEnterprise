-- 0107 -- A CLIENT IS ONE IDENTITY, MAY BELONG TO A CLIENT GROUP, AND MAY BE REMEMBERED ON A DEVICE.
--
-- Contract: docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md. Everything here is ADDITIVE: no
-- row is rewritten, no column is dropped, no constraint is tightened on existing data.
--
-- 1. VERIFIED CLAIMS ON AN IDENTITY FACTOR
-- ----------------------------------------
-- iam_v2.guest_principal_identities already keys a Client by verified factors (EMAIL, PHONE, SOCIAL_SUBJECT).
-- What it could not hold is what the issuer ASSERTED about that factor: Google's hosted domain (hd), the
-- Microsoft tenant (tid), and which verification produced an EMAIL row (a one-time code, or a trusted issuer's
-- email_verified claim). Client Group rules are a pure function of those claims, so they live on the factor.
-- The browser never supplies any of them.
--
-- 2. CLIENT GROUPS
-- ----------------
-- A site-level policy object: name, priority (lower wins), enabled, and membership rules. A Client who matches
-- any rule of a group is a member; the lowest-priority group is the EFFECTIVE one and is pinned on the auth
-- context, so eligibility reads a recorded decision instead of re-deriving one.
--
-- 3. THE REMEMBERED DEVICE
-- ------------------------
-- After a verified sign-in scd issues a device-bound resume credential (HMAC stored, plaintext only in the
-- client's cookie), so the next reconnection from THAT device can mint an auth context without another code.
-- It mirrors anonymous_subject_credentials for principals. A credential is bound to one device and refused from
-- any other.
--
-- 4. SEALED NOTIFICATION SECRETS
-- ------------------------------
-- public.notification_providers.api_key is a plaintext column. New secrets (SMTP passwords, API keys, tokens)
-- are sealed by scd under notify_dek.key, AAD-bound to (tenant, provider, generation), in the same shape as
-- iam_v2.payment_provider_secret_generations. Existing plaintext rows keep working until re-saved.
--
-- 5. THE smtp KIND
-- ----------------
-- public.notification_providers.kind gains 'smtp' (channel email). As in 0099 the public table is changed
-- here only when the applying role owns it (factory-clean build, baseline generator); a live appliance applies
-- deploy/scripts/extend-notification-kinds-smtp.sql as the owner. Both end with the same constraint.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Verified claims on an identity factor.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE iam_v2.guest_principal_identities ADD COLUMN IF NOT EXISTS attrs jsonb NOT NULL DEFAULT '{}'::jsonb;
COMMENT ON COLUMN iam_v2.guest_principal_identities.attrs IS
  'Claims the ISSUER verified about this factor and nothing else: hd (Google Workspace domain), tid (Microsoft '
  'tenant), email (the verified address an IdP asserted beside a SOCIAL_SUBJECT), source (otp | issuer name). '
  'Never set from anything the browser sent. Read by Client Group rules.';

-- ---------------------------------------------------------------------------------------------------------
-- 2. Client Groups.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.client_groups (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL,
  site_id     uuid NOT NULL,
  name        text NOT NULL CONSTRAINT client_groups_name CHECK (length(btrim(name)) BETWEEN 1 AND 80),
  description text NOT NULL DEFAULT '' CONSTRAINT client_groups_description CHECK (length(description) <= 500),
  priority    integer NOT NULL DEFAULT 100 CONSTRAINT client_groups_priority CHECK (priority BETWEEN 1 AND 1000),
  enabled     boolean NOT NULL DEFAULT true,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, site_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS client_groups_name_per_site
  ON iam_v2.client_groups (tenant_id, site_id, lower(btrim(name)));
COMMENT ON TABLE iam_v2.client_groups IS
  'A site''s business groups (Employees, Partners, VIP ...). Membership is decided by the group''s rules '
  'against a Client''s VERIFIED identity factors; the lowest priority among the matches is the effective '
  'group. The Public group is implicit: a Client who matches no enabled group.';

CREATE TABLE IF NOT EXISTS iam_v2.client_group_rules (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  group_id   uuid NOT NULL,
  rule_type  text NOT NULL
    CONSTRAINT client_group_rules_type CHECK (rule_type IN ('EMAIL_DOMAIN','IDP_TENANT','IDP_HOSTED_DOMAIN')),
  rule_value jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, site_id, group_id)
    REFERENCES iam_v2.client_groups (tenant_id, site_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS client_group_rules_group ON iam_v2.client_group_rules (tenant_id, site_id, group_id);
COMMENT ON TABLE iam_v2.client_group_rules IS
  'OR-ed membership rules of a Client Group. EMAIL_DOMAIN {domains, include_subdomains} needs a verified '
  'mailbox; IDP_TENANT {provider, tenant_ids} and IDP_HOSTED_DOMAIN {provider, domains} need the claim from a '
  'verified id_token. IDP_GROUP is reserved and refused until a provider emits group claims.';

CREATE TABLE IF NOT EXISTS iam_v2.client_group_changes (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL,
  site_id       uuid NOT NULL,
  group_id      uuid NOT NULL,
  action        text NOT NULL CONSTRAINT client_group_changes_action CHECK (action IN ('CREATED','UPDATED','DELETED')),
  changed_by    text NOT NULL CONSTRAINT client_group_changes_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason text NOT NULL DEFAULT '',
  before        jsonb,
  after         jsonb,
  changed_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS client_group_changes_lookup
  ON iam_v2.client_group_changes (tenant_id, site_id, changed_at DESC);

CREATE OR REPLACE FUNCTION iam_v2.client_group_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'iam_v2.client_group_changes is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $$;
REVOKE EXECUTE ON FUNCTION iam_v2.client_group_changes_append_only() FROM PUBLIC;
DROP TRIGGER IF EXISTS client_group_changes_append_only ON iam_v2.client_group_changes;
CREATE TRIGGER client_group_changes_append_only
  BEFORE UPDATE OR DELETE ON iam_v2.client_group_changes
  FOR EACH ROW EXECUTE FUNCTION iam_v2.client_group_changes_append_only();

-- The effective group, pinned on the auth context when scd creates it.
ALTER TABLE iam_v2.auth_contexts ADD COLUMN IF NOT EXISTS client_group_id uuid;
ALTER TABLE iam_v2.auth_contexts ADD COLUMN IF NOT EXISTS client_group_evidence jsonb;
DO $fk$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ac_client_group_fk') THEN
    -- ON DELETE SET NULL: deleting a group must not make an auth context un-deletable or un-readable; the
    -- evidence column keeps the group name for the record.
    ALTER TABLE iam_v2.auth_contexts ADD CONSTRAINT ac_client_group_fk
      FOREIGN KEY (tenant_id, site_id, client_group_id)
      REFERENCES iam_v2.client_groups (tenant_id, site_id, id) ON DELETE SET NULL;
  END IF;
END $fk$;
COMMENT ON COLUMN iam_v2.auth_contexts.client_group_id IS
  'The Client Group in force for this sign-in (lowest priority among the matches), decided by scd from the '
  'Client''s verified factors and pinned here. NULL = Public. Eligibility reads this pin and never re-derives it.';
COMMENT ON COLUMN iam_v2.auth_contexts.client_group_evidence IS
  'Why: {group, rule, factor_type, matched[], via} -- rule ids and factor kinds, never an address.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. The remembered device.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.principal_device_credentials (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL,
  site_id            uuid NOT NULL,
  guest_principal_id uuid NOT NULL,
  device_id          uuid NOT NULL,
  method             text NOT NULL CONSTRAINT principal_device_credentials_method CHECK (method IN ('OTP','SOCIAL')),
  secret_hmac        bytea NOT NULL UNIQUE,
  key_id             text NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  expires_at         timestamptz NOT NULL,
  last_used_at       timestamptz,
  revoked_at         timestamptz,
  FOREIGN KEY (tenant_id, guest_principal_id)
    REFERENCES iam_v2.guest_principals (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, site_id, device_id)
    REFERENCES iam_v2.devices (tenant_id, site_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS principal_device_credentials_principal
  ON iam_v2.principal_device_credentials (tenant_id, site_id, guest_principal_id);
CREATE INDEX IF NOT EXISTS principal_device_credentials_device
  ON iam_v2.principal_device_credentials (tenant_id, site_id, device_id);
COMMENT ON TABLE iam_v2.principal_device_credentials IS
  'A verified Client''s "remember this device" credential: the keyed HMAC of a 256-bit token held only in '
  'the portal cookie, bound to ONE device. Usable only from that device (MAC resolved by the kernel on every '
  'use), for the site''s Remember-devices period; revoked by sign-out, expiry, or an operator disconnect. It '
  'replaces the code on a reconnection, never the eligibility, entitlement or session logic.';

-- ---------------------------------------------------------------------------------------------------------
-- 4. Sealed notification secrets.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.notification_provider_secret_generations (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL,
  provider_id    uuid NOT NULL,
  generation_no  integer NOT NULL CONSTRAINT npsg_generation CHECK (generation_no >= 1),
  ciphertext     bytea NOT NULL,
  nonce          bytea NOT NULL,
  key_id         text NOT NULL,
  cipher_version integer NOT NULL DEFAULT 1,
  created_at     timestamptz NOT NULL DEFAULT now(),
  superseded_at  timestamptz,
  UNIQUE (tenant_id, provider_id, generation_no)
);
CREATE UNIQUE INDEX IF NOT EXISTS npsg_one_current
  ON iam_v2.notification_provider_secret_generations (tenant_id, provider_id) WHERE superseded_at IS NULL;
COMMENT ON TABLE iam_v2.notification_provider_secret_generations IS
  'The secret of a public.notification_providers row (SMTP password, API key, access token), sealed by scd '
  'with AES-256-GCM under notify_dek.key and AAD bound to (tenant, provider, generation). Exactly one current '
  'generation per provider. public.notification_providers.api_key is left NULL for rows written after 0107.';

-- ---------------------------------------------------------------------------------------------------------
-- 5. The smtp kind (owner-conditional, as 0099).
-- ---------------------------------------------------------------------------------------------------------
DO $smtp_kind$
DECLARE v_owner oid;
BEGIN
  SELECT relowner INTO v_owner FROM pg_class WHERE oid = 'public.notification_providers'::regclass;
  IF NOT pg_has_role(current_user, v_owner, 'USAGE') THEN
    RAISE NOTICE 'public.notification_providers is not owned by %; extend it with deploy/scripts/extend-notification-kinds-smtp.sql', current_user;
    RETURN;
  END IF;
  EXECUTE 'ALTER TABLE public.notification_providers DROP CONSTRAINT IF EXISTS notification_providers_kind_check';
  EXECUTE $c$ALTER TABLE public.notification_providers ADD CONSTRAINT notification_providers_kind_check
    CHECK (kind = ANY (ARRAY['stub'::text, 'sendgrid'::text, 'ses'::text, 'smtp'::text, 'twilio'::text,
                             'meta_whatsapp'::text, 'twilio_whatsapp'::text]))$c$;
END $smtp_kind$;

-- ---------------------------------------------------------------------------------------------------------
-- 6. Ownership, declared rather than inherited from whoever applied this file.
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.client_groups OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.client_group_rules OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.client_group_changes OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.principal_device_credentials OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.notification_provider_secret_generations OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.client_group_changes_append_only() OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- ---------------------------------------------------------------------------------------------------------
-- 7. Least privilege. Mirrored in deploy/gatep/svc-scd-iamv2-guest-auth-grants.sql and
--    deploy/gatep/svc-edged-phase345-admin-grants.sql so it survives a Gate-P reconcile.
-- ---------------------------------------------------------------------------------------------------------
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    -- identity: a factor's verified claims may be refreshed (tid/hd), nothing else on the row changes.
    GRANT UPDATE (attrs) ON iam_v2.guest_principal_identities TO svc_scd;
    -- groups: scd evaluates membership at sign-in (read only) and pins it on the context it creates.
    GRANT SELECT ON iam_v2.client_groups, iam_v2.client_group_rules TO svc_scd;
    -- the remembered device: issued, used (last_used_at) and revoked by scd.
    GRANT SELECT, INSERT, UPDATE ON iam_v2.principal_device_credentials TO svc_scd;
    -- sealed secrets: scd seals and opens them; edged never touches the ciphertext.
    GRANT SELECT, INSERT, UPDATE ON iam_v2.notification_provider_secret_generations TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    -- the Admin Console owns groups and their history; it reads NO identity table (unchanged).
    GRANT SELECT, INSERT, UPDATE, DELETE ON iam_v2.client_groups, iam_v2.client_group_rules TO svc_edged;
    GRANT SELECT, INSERT ON iam_v2.client_group_changes TO svc_edged;
  END IF;
END $g$;

COMMIT;
