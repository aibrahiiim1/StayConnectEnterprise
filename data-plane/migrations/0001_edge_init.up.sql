-- 0001_edge_init: site-local (Edge) database schema.
--
-- One hotel site == one isolated database (default name: stayconnect_site).
-- This schema is intentionally shape-compatible with the guest-domain subset
-- of the central cloud schema (generated from the live pilot DB via pg_dump
-- on 2026-07-11) so that scd/portald/acctd cut over by changing only their
-- DSN, and so the sitemigrate tool can copy rows 1:1.
--
-- Edge-owned domains: local operators & roles, guest access plans
-- (ticket_templates), voucher batches & vouchers, guests, sessions,
-- accounting, OTP, social login state, PMS providers & attempts, walled
-- garden, notification/social/stripe provider config, payments, local audit,
-- sync outbox/checkpoints, backups.
--
-- The `tenants` and `sites` tables here hold EXACTLY ONE row each (this
-- site's identity + settings such as auth_methods and portal branding);
-- `tenant_effective_limits` is a plain table maintained from the verified
-- signed license by scd/edged — NOT from any cloud database.

BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE IF NOT EXISTS schema_migrations (
  version    text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);


CREATE TABLE accounting_records (
    ts timestamp with time zone NOT NULL,
    session_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    appliance_id uuid NOT NULL,
    bytes_up bigint NOT NULL,
    bytes_down bigint NOT NULL
);

CREATE TABLE audit_log (
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id uuid,
    actor_type text NOT NULL,
    actor_id text,
    action text NOT NULL,
    target_type text,
    target_id text,
    ip inet,
    user_agent text,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT audit_log_actor_type_check CHECK ((actor_type = ANY (ARRAY['operator'::text, 'system'::text, 'appliance'::text, 'guest'::text, 'api'::text])))
);

CREATE TABLE auth_otps (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    appliance_id uuid,
    template_id uuid,
    channel text NOT NULL,
    destination text NOT NULL,
    code_hash text NOT NULL,
    issued_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 5 NOT NULL,
    consumed_at timestamp with time zone,
    ip inet,
    user_agent text,
    CONSTRAINT auth_otps_channel_check CHECK ((channel = ANY (ARRAY['email'::text, 'sms'::text])))
);

CREATE TABLE guests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    mac macaddr NOT NULL,
    device_fingerprint text,
    display_name text,
    email text,
    phone text,
    consent_accepted_at timestamp with time zone,
    consent_version text,
    last_seen_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    email_verified_at timestamp with time zone,
    phone_verified_at timestamp with time zone
);

CREATE TABLE notification_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    channel text NOT NULL,
    kind text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    display_name text,
    api_key text,
    api_user text,
    from_address text,
    from_name text,
    region text,
    extra jsonb DEFAULT '{}'::jsonb NOT NULL,
    last_success_at timestamp with time zone,
    last_error text,
    last_error_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT notification_providers_channel_check CHECK ((channel = ANY (ARRAY['email'::text, 'sms'::text]))),
    CONSTRAINT notification_providers_kind_check CHECK ((kind = ANY (ARRAY['stub'::text, 'sendgrid'::text, 'ses'::text, 'twilio'::text])))
);

CREATE TABLE operator_roles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    operator_id uuid NOT NULL,
    tenant_id uuid,
    role text NOT NULL,
    CONSTRAINT operator_roles_role_check CHECK ((role = ANY (ARRAY['site_admin'::text, 'hotel_it_manager'::text, 'front_office_operator'::text, 'guest_relations_operator'::text, 'voucher_operator'::text, 'payments_operator'::text, 'site_viewer'::text, 'tenant_admin'::text, 'tenant_operator'::text, 'viewer'::text, 'billing'::text])))
);

CREATE TABLE operators (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid,
    email text NOT NULL,
    display_name text,
    password_hash text,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    auth_method text DEFAULT 'local'::text NOT NULL,
    oidc_sub text,
    last_sso_login_at timestamp with time zone,
    CONSTRAINT operators_auth_method_check CHECK ((auth_method = ANY (ARRAY['local'::text, 'sso'::text]))),
    CONSTRAINT operators_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'invited'::text])))
);

CREATE TABLE payments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid,
    template_id uuid NOT NULL,
    stripe_session_id text NOT NULL,
    stripe_payment_intent text,
    status text DEFAULT 'pending'::text NOT NULL,
    amount_cents bigint NOT NULL,
    currency text NOT NULL,
    voucher_id uuid,
    client_ip inet,
    client_mac macaddr,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payments_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'paid'::text, 'failed'::text, 'expired'::text, 'cancelled'::text])))
);

CREATE TABLE pms_attempts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    appliance_id uuid,
    room_number text NOT NULL,
    secondary_kind text NOT NULL,
    ip inet,
    success boolean NOT NULL,
    error_code text,
    attempted_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pms_attempts_secondary_kind_check CHECK ((secondary_kind = ANY (ARRAY['first_name'::text, 'last_name'::text, 'reservation'::text, 'either'::text])))
);

CREATE TABLE pms_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    display_name text,
    host text,
    port integer,
    use_tls boolean DEFAULT false NOT NULL,
    auth_key text,
    base_url text,
    api_key text,
    property_id text,
    extra jsonb DEFAULT '{}'::jsonb NOT NULL,
    field_map jsonb DEFAULT '{}'::jsonb NOT NULL,
    normalization jsonb DEFAULT '{}'::jsonb NOT NULL,
    stay_window jsonb DEFAULT '{}'::jsonb NOT NULL,
    status text DEFAULT 'idle'::text NOT NULL,
    last_record_at timestamp with time zone,
    last_error text,
    last_error_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    site_id uuid,
    CONSTRAINT pms_providers_kind_check CHECK ((kind = ANY (ARRAY['stub'::text, 'protel-fias'::text, 'opera-fias'::text, 'fidelio-fias'::text, 'mews'::text, 'apaleo'::text]))),
    CONSTRAINT pms_providers_status_check CHECK ((status = ANY (ARRAY['idle'::text, 'connecting'::text, 'connected'::text, 'degraded'::text, 'down'::text])))
);

CREATE TABLE sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    appliance_id uuid NOT NULL,
    guest_id uuid NOT NULL,
    voucher_id uuid,
    ip inet NOT NULL,
    mac macaddr NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    last_activity_at timestamp with time zone DEFAULT now() NOT NULL,
    ended_at timestamp with time zone,
    end_reason text,
    bytes_up bigint DEFAULT 0 NOT NULL,
    bytes_down bigint DEFAULT 0 NOT NULL,
    state text DEFAULT 'active'::text NOT NULL,
    expires_at timestamp with time zone,
    CONSTRAINT sessions_end_reason_check CHECK ((end_reason = ANY (ARRAY['quota_bytes'::text, 'quota_time'::text, 'admin'::text, 'idle'::text, 'dhcp_expired'::text, 'policy'::text]))),
    CONSTRAINT sessions_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'active'::text, 'suspended'::text, 'closed'::text])))
);

CREATE TABLE social_oauth_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    provider text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    display_name text,
    client_id text NOT NULL,
    client_secret text NOT NULL,
    redirect_uri text NOT NULL,
    scopes text,
    extra jsonb DEFAULT '{}'::jsonb NOT NULL,
    last_success_at timestamp with time zone,
    last_error text,
    last_error_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT social_oauth_providers_provider_check CHECK ((provider = ANY (ARRAY['google'::text, 'apple'::text, 'facebook'::text, 'microsoft'::text])))
);

CREATE TABLE social_oauth_states (
    state text NOT NULL,
    tenant_id uuid NOT NULL,
    appliance_id uuid,
    template_id uuid,
    provider text NOT NULL,
    client_ip inet,
    client_mac macaddr,
    redirect_uri text NOT NULL,
    user_agent text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone
);

CREATE TABLE stripe_accounts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    display_name text,
    publishable_key text NOT NULL,
    secret_key text NOT NULL,
    webhook_secret text NOT NULL,
    success_url text NOT NULL,
    cancel_url text NOT NULL,
    last_success_at timestamp with time zone,
    last_error text,
    last_error_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE stripe_events (
    event_id text NOT NULL,
    tenant_id uuid NOT NULL,
    event_type text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE tenants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    contact_email text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    auth_methods jsonb DEFAULT '{"voucher": {"enabled": true, "template_id": null}}'::jsonb NOT NULL,
    CONSTRAINT tenants_status_check CHECK ((status = ANY (ARRAY['active'::text, 'suspended'::text, 'archived'::text])))
);

CREATE TABLE ticket_templates (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    code text NOT NULL,
    name text NOT NULL,
    description text,
    duration_seconds integer,
    data_cap_bytes bigint,
    down_kbps integer,
    up_kbps integer,
    max_concurrent_devices integer DEFAULT 1 NOT NULL,
    validity_seconds integer,
    schedule jsonb,
    price_cents integer,
    currency text,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE voucher_batches (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    template_id uuid NOT NULL,
    name text,
    note text,
    count integer NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT voucher_batches_count_check CHECK ((count > 0))
);

CREATE TABLE vouchers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    template_id uuid NOT NULL,
    code text NOT NULL,
    batch_id uuid,
    state text DEFAULT 'unused'::text NOT NULL,
    issued_at timestamp with time zone DEFAULT now() NOT NULL,
    activated_at timestamp with time zone,
    expires_at timestamp with time zone,
    bytes_used bigint DEFAULT 0 NOT NULL,
    seconds_used integer DEFAULT 0 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT vouchers_state_check CHECK ((state = ANY (ARRAY['unused'::text, 'active'::text, 'exhausted'::text, 'expired'::text, 'revoked'::text])))
);

CREATE TABLE walled_garden_rules (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid,
    kind text NOT NULL,
    value text NOT NULL,
    ports integer[],
    description text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT walled_garden_rules_kind_check CHECK ((kind = ANY (ARRAY['domain'::text, 'cidr'::text, 'ip'::text])))
);

CREATE TABLE appliances (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    serial text NOT NULL,
    name text NOT NULL,
    model text,
    version text,
    enrolled_at timestamp with time zone,
    last_seen_at timestamp with time zone,
    status text DEFAULT 'pending'::text NOT NULL,
    public_key text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    identity_verified_at timestamp with time zone,
    CONSTRAINT appliances_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'enrolled'::text, 'online'::text, 'offline'::text, 'retired'::text])))
);

CREATE TABLE sites (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    code text NOT NULL,
    name text NOT NULL,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    country text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY auth_otps
    ADD CONSTRAINT auth_otps_pkey PRIMARY KEY (id);

ALTER TABLE ONLY guests
    ADD CONSTRAINT guests_pkey PRIMARY KEY (id);

ALTER TABLE ONLY guests
    ADD CONSTRAINT guests_tenant_id_mac_key UNIQUE (tenant_id, mac);

ALTER TABLE ONLY notification_providers
    ADD CONSTRAINT notification_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY operator_roles
    ADD CONSTRAINT operator_roles_pkey PRIMARY KEY (id);

ALTER TABLE ONLY operators
    ADD CONSTRAINT operators_email_key UNIQUE (email);

ALTER TABLE ONLY operators
    ADD CONSTRAINT operators_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments
    ADD CONSTRAINT payments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY payments
    ADD CONSTRAINT payments_stripe_session_id_key UNIQUE (stripe_session_id);

ALTER TABLE ONLY pms_attempts
    ADD CONSTRAINT pms_attempts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY pms_providers
    ADD CONSTRAINT pms_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY social_oauth_providers
    ADD CONSTRAINT social_oauth_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY social_oauth_states
    ADD CONSTRAINT social_oauth_states_pkey PRIMARY KEY (state);

ALTER TABLE ONLY stripe_accounts
    ADD CONSTRAINT stripe_accounts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY stripe_events
    ADD CONSTRAINT stripe_events_pkey PRIMARY KEY (event_id);

ALTER TABLE ONLY tenants
    ADD CONSTRAINT tenants_pkey PRIMARY KEY (id);

ALTER TABLE ONLY tenants
    ADD CONSTRAINT tenants_slug_key UNIQUE (slug);

ALTER TABLE ONLY ticket_templates
    ADD CONSTRAINT ticket_templates_pkey PRIMARY KEY (id);

ALTER TABLE ONLY ticket_templates
    ADD CONSTRAINT ticket_templates_tenant_id_code_key UNIQUE (tenant_id, code);

ALTER TABLE ONLY voucher_batches
    ADD CONSTRAINT voucher_batches_pkey PRIMARY KEY (id);

ALTER TABLE ONLY vouchers
    ADD CONSTRAINT vouchers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY vouchers
    ADD CONSTRAINT vouchers_tenant_id_code_key UNIQUE (tenant_id, code);

ALTER TABLE ONLY walled_garden_rules
    ADD CONSTRAINT walled_garden_rules_pkey PRIMARY KEY (id);

ALTER TABLE ONLY appliances
    ADD CONSTRAINT appliances_pkey PRIMARY KEY (id);

ALTER TABLE ONLY appliances
    ADD CONSTRAINT appliances_serial_key UNIQUE (serial);

ALTER TABLE ONLY sites
    ADD CONSTRAINT sites_pkey PRIMARY KEY (id);

ALTER TABLE ONLY sites
    ADD CONSTRAINT sites_tenant_id_code_key UNIQUE (tenant_id, code);

CREATE INDEX accounting_records_session_idx ON accounting_records USING btree (session_id, ts DESC);

CREATE INDEX accounting_records_tenant_idx ON accounting_records USING btree (tenant_id, ts DESC);

CREATE INDEX accounting_records_ts_idx ON accounting_records USING btree (ts DESC);

CREATE INDEX audit_log_tenant_idx ON audit_log USING btree (tenant_id, ts DESC);

CREATE INDEX audit_log_ts_idx ON audit_log USING btree (ts DESC);

CREATE INDEX auth_otps_dest_idx ON auth_otps USING btree (tenant_id, channel, lower(destination), issued_at DESC);

CREATE INDEX auth_otps_recent_idx ON auth_otps USING btree (tenant_id, channel, lower(destination), issued_at) WHERE (consumed_at IS NULL);

CREATE INDEX guests_email_idx ON guests USING btree (tenant_id, lower(email)) WHERE (email IS NOT NULL);

CREATE INDEX guests_phone_idx ON guests USING btree (tenant_id, phone) WHERE (phone IS NOT NULL);

CREATE UNIQUE INDEX notification_providers_tenant_channel_enabled_idx ON notification_providers USING btree (tenant_id, channel) WHERE (enabled = true);

CREATE UNIQUE INDEX operator_roles_uniq ON operator_roles USING btree (operator_id, COALESCE(tenant_id, '00000000-0000-0000-0000-000000000000'::uuid), role);

CREATE UNIQUE INDEX operators_oidc_sub_uniq ON operators USING btree (tenant_id, oidc_sub) WHERE (oidc_sub IS NOT NULL);

CREATE INDEX payments_tenant_status_idx ON payments USING btree (tenant_id, status, created_at DESC);

CREATE INDEX pms_attempts_ip_idx ON pms_attempts USING btree (tenant_id, ip, attempted_at DESC);

CREATE INDEX pms_attempts_room_idx ON pms_attempts USING btree (tenant_id, lower(room_number), attempted_at DESC);

CREATE INDEX pms_providers_tenant_enabled_idx ON pms_providers USING btree (tenant_id) WHERE (enabled = true);

CREATE UNIQUE INDEX pms_providers_tenant_name_global_idx ON pms_providers USING btree (tenant_id, name) WHERE (site_id IS NULL);

CREATE INDEX pms_providers_tenant_site_enabled_idx ON pms_providers USING btree (tenant_id, site_id) WHERE (enabled = true);

CREATE UNIQUE INDEX pms_providers_tenant_site_name_idx ON pms_providers USING btree (tenant_id, site_id, name) WHERE (site_id IS NOT NULL);

CREATE INDEX sessions_active_expires_idx ON sessions USING btree (expires_at) WHERE (state = 'active'::text);

CREATE INDEX sessions_active_idle_idx ON sessions USING btree (last_activity_at) WHERE (state = 'active'::text);

CREATE INDEX sessions_appliance_active_idx ON sessions USING btree (appliance_id, state) WHERE (state = 'active'::text);

CREATE INDEX sessions_tenant_active_idx ON sessions USING btree (tenant_id, state) WHERE (state = 'active'::text);

CREATE UNIQUE INDEX social_oauth_providers_tenant_provider_enabled_idx ON social_oauth_providers USING btree (tenant_id, provider) WHERE (enabled = true);

CREATE INDEX social_oauth_states_expiry_idx ON social_oauth_states USING btree (expires_at) WHERE (consumed_at IS NULL);

CREATE UNIQUE INDEX stripe_accounts_tenant_enabled_idx ON stripe_accounts USING btree (tenant_id) WHERE (enabled = true);

CREATE INDEX stripe_events_received_at_idx ON stripe_events USING btree (received_at);

CREATE INDEX voucher_batches_tenant_idx ON voucher_batches USING btree (tenant_id, created_at DESC);

CREATE INDEX vouchers_batch_idx ON vouchers USING btree (batch_id);

CREATE UNIQUE INDEX vouchers_code_global_uniq ON vouchers USING btree (code);

CREATE INDEX vouchers_state_idx ON vouchers USING btree (tenant_id, state);

CREATE INDEX appliances_tenant_site_idx ON appliances USING btree (tenant_id, site_id);

ALTER TABLE ONLY auth_otps
    ADD CONSTRAINT auth_otps_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES appliances(id) ON DELETE SET NULL;

ALTER TABLE ONLY auth_otps
    ADD CONSTRAINT auth_otps_template_id_fkey FOREIGN KEY (template_id) REFERENCES ticket_templates(id) ON DELETE SET NULL;

ALTER TABLE ONLY auth_otps
    ADD CONSTRAINT auth_otps_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY guests
    ADD CONSTRAINT guests_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY notification_providers
    ADD CONSTRAINT notification_providers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY operator_roles
    ADD CONSTRAINT operator_roles_operator_id_fkey FOREIGN KEY (operator_id) REFERENCES operators(id) ON DELETE CASCADE;

ALTER TABLE ONLY operator_roles
    ADD CONSTRAINT operator_roles_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY operators
    ADD CONSTRAINT operators_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY payments
    ADD CONSTRAINT payments_site_id_fkey FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE SET NULL;

ALTER TABLE ONLY payments
    ADD CONSTRAINT payments_template_id_fkey FOREIGN KEY (template_id) REFERENCES ticket_templates(id);

ALTER TABLE ONLY payments
    ADD CONSTRAINT payments_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY payments
    ADD CONSTRAINT payments_voucher_id_fkey FOREIGN KEY (voucher_id) REFERENCES vouchers(id) ON DELETE SET NULL;

ALTER TABLE ONLY pms_attempts
    ADD CONSTRAINT pms_attempts_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES appliances(id) ON DELETE SET NULL;

ALTER TABLE ONLY pms_attempts
    ADD CONSTRAINT pms_attempts_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY pms_providers
    ADD CONSTRAINT pms_providers_site_id_fkey FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE CASCADE;

ALTER TABLE ONLY pms_providers
    ADD CONSTRAINT pms_providers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY sessions
    ADD CONSTRAINT sessions_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES appliances(id) ON DELETE CASCADE;

ALTER TABLE ONLY sessions
    ADD CONSTRAINT sessions_guest_id_fkey FOREIGN KEY (guest_id) REFERENCES guests(id) ON DELETE CASCADE;

ALTER TABLE ONLY sessions
    ADD CONSTRAINT sessions_site_id_fkey FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE CASCADE;

ALTER TABLE ONLY sessions
    ADD CONSTRAINT sessions_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY sessions
    ADD CONSTRAINT sessions_voucher_id_fkey FOREIGN KEY (voucher_id) REFERENCES vouchers(id) ON DELETE SET NULL;

ALTER TABLE ONLY social_oauth_providers
    ADD CONSTRAINT social_oauth_providers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY social_oauth_states
    ADD CONSTRAINT social_oauth_states_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES appliances(id) ON DELETE SET NULL;

ALTER TABLE ONLY social_oauth_states
    ADD CONSTRAINT social_oauth_states_template_id_fkey FOREIGN KEY (template_id) REFERENCES ticket_templates(id) ON DELETE SET NULL;

ALTER TABLE ONLY social_oauth_states
    ADD CONSTRAINT social_oauth_states_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY stripe_accounts
    ADD CONSTRAINT stripe_accounts_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY stripe_events
    ADD CONSTRAINT stripe_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY ticket_templates
    ADD CONSTRAINT ticket_templates_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY voucher_batches
    ADD CONSTRAINT voucher_batches_created_by_fkey FOREIGN KEY (created_by) REFERENCES operators(id) ON DELETE SET NULL;

ALTER TABLE ONLY voucher_batches
    ADD CONSTRAINT voucher_batches_template_id_fkey FOREIGN KEY (template_id) REFERENCES ticket_templates(id) ON DELETE RESTRICT;

ALTER TABLE ONLY voucher_batches
    ADD CONSTRAINT voucher_batches_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY vouchers
    ADD CONSTRAINT vouchers_batch_id_fkey FOREIGN KEY (batch_id) REFERENCES voucher_batches(id) ON DELETE SET NULL;

ALTER TABLE ONLY vouchers
    ADD CONSTRAINT vouchers_template_id_fkey FOREIGN KEY (template_id) REFERENCES ticket_templates(id) ON DELETE RESTRICT;

ALTER TABLE ONLY vouchers
    ADD CONSTRAINT vouchers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY walled_garden_rules
    ADD CONSTRAINT walled_garden_rules_site_id_fkey FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE CASCADE;

ALTER TABLE ONLY walled_garden_rules
    ADD CONSTRAINT walled_garden_rules_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY appliances
    ADD CONSTRAINT appliances_site_id_fkey FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE CASCADE;

ALTER TABLE ONLY appliances
    ADD CONSTRAINT appliances_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY sites
    ADD CONSTRAINT sites_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;



-- ---------------------------------------------------------------------------
-- Edge-specific additions
-- ---------------------------------------------------------------------------

-- Portal branding / T&C / languages for THIS site (single tenants row).
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS branding jsonb NOT NULL DEFAULT '{}'::jsonb;

-- License-derived effective limits. scd/edged rewrite these rows whenever a
-- verified signed license is (re)loaded; session.CheckConcurrency and the
-- provisioning caps read them exactly like the old cloud view, so the data
-- plane needed no query changes. Source of truth = the signed license file.
CREATE TABLE tenant_effective_limits (
  tenant_id  uuid NOT NULL,
  key        text NOT NULL,
  value_type text NOT NULL CHECK (value_type IN ('int','bool','string')),
  int_value  bigint,
  bool_value boolean,
  str_value  text,
  source     text NOT NULL DEFAULT 'license',
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);

-- Durable sync outbox: everything the appliance tells the cloud goes through
-- here. seq is the idempotency key the cloud dedupes on.
CREATE TABLE sync_outbox (
  seq             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  kind            text NOT NULL,
  payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),
  sent_at         timestamptz,
  attempts        int NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  dead            boolean NOT NULL DEFAULT false,
  last_error      text
);
CREATE INDEX sync_outbox_pending_idx ON sync_outbox (next_attempt_at)
  WHERE sent_at IS NULL AND dead = false;

-- Named sync checkpoints (last license fetch, last successful drain, ...).
CREATE TABLE sync_checkpoints (
  name       text PRIMARY KEY,
  value      jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Local backup history (backup agent writes; Hotel Admin reads).
CREATE TABLE backup_records (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  started_at  timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  status      text NOT NULL DEFAULT 'running' CHECK (status IN ('running','ok','failed')),
  kind        text NOT NULL DEFAULT 'scheduled' CHECK (kind IN ('scheduled','manual','pre_migration')),
  path        text,
  size_bytes  bigint,
  error       text
);

-- Hypertables when TimescaleDB is present (pilot); plain tables otherwise.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
    PERFORM create_hypertable('accounting_records','ts', chunk_time_interval => INTERVAL '1 day');
    PERFORM create_hypertable('audit_log','ts',          chunk_time_interval => INTERVAL '7 days');
  ELSE
    CREATE INDEX IF NOT EXISTS accounting_records_ts_idx ON accounting_records (ts DESC);
    CREATE INDEX IF NOT EXISTS audit_log_ts_idx          ON audit_log (ts DESC);
  END IF;
END $$;

INSERT INTO schema_migrations(version) VALUES ('0001_edge_init')
ON CONFLICT DO NOTHING;

COMMIT;
