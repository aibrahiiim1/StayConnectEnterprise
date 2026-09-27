-- DOWN for 0046_central_redesign: restores the STRUCTURE the up migration removed.
--
-- What comes back: every dropped table (empty, in its final pre-0046 shape: columns, keys, indexes, foreign
-- keys; accounting_records again as a Timescale hypertable), the two commercial views, appliances.status and
-- appliances.environment, the pre-0046 lifecycle CHECK, and the archived commercial tables back in public.
--
-- What does NOT come back: the fine-grained lifecycle values the up migration collapsed (see its header).
-- appliances.status is backfilled from the collapsed lifecycle: assigned -> enrolled,
-- revoked/decommissioned -> retired, anything else -> pending. The out-of-band legacy_ro guard triggers that
-- existed on the live database (never part of a migration) are not recreated.
--
-- The table DDL below is the pg_dump of the schema produced by migrations 0001..0045, not hand-copied.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Commercial history back into public.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE IF EXISTS legacy_archive.plans               SET SCHEMA public;
ALTER TABLE IF EXISTS legacy_archive.plan_limits         SET SCHEMA public;
ALTER TABLE IF EXISTS legacy_archive.plan_limit_history  SET SCHEMA public;
ALTER TABLE IF EXISTS legacy_archive.subscription_events SET SCHEMA public;
-- NOT VALID: a customer deleted while the history was archived leaves rows this constraint would reject.
ALTER TABLE public.subscription_events
    ADD CONSTRAINT subscription_events_tenant_id_fkey FOREIGN KEY (tenant_id)
    REFERENCES public.tenants(id) ON DELETE CASCADE NOT VALID;
DROP SCHEMA IF EXISTS legacy_archive;

-- ---------------------------------------------------------------------------------------------------------
-- 2. Appliance columns and the pre-0046 lifecycle vocabulary.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS status text DEFAULT 'pending' NOT NULL;
UPDATE appliances SET status = CASE lifecycle_state
        WHEN 'assigned' THEN 'enrolled'
        WHEN 'revoked' THEN 'retired'
        WHEN 'decommissioned' THEN 'retired'
        ELSE 'pending'
    END;
ALTER TABLE appliances ADD CONSTRAINT appliances_status_check
    CHECK (status = ANY (ARRAY['pending'::text, 'enrolled'::text, 'online'::text, 'offline'::text, 'retired'::text]));
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS environment text;
ALTER TABLE appliances DROP COLUMN IF EXISTS activated_at;
ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_lifecycle_state_check;
ALTER TABLE appliances ALTER COLUMN lifecycle_state SET DEFAULT 'installed_unenrolled';
ALTER TABLE appliances ADD CONSTRAINT appliances_lifecycle_state_check
    CHECK (lifecycle_state = ANY (ARRAY['manufactured'::text, 'installed_unenrolled'::text, 'pending_enrollment'::text,
        'pending_approval'::text, 'claimed'::text, 'assigned'::text, 'licensed'::text, 'grace'::text, 'online'::text,
        'offline'::text, 'suspended'::text, 'license_expired'::text, 'revoked'::text, 'decommissioned'::text]));

-- ---------------------------------------------------------------------------------------------------------
-- 3. The dropped tables, their keys, indexes and foreign keys, and the two views.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE public.accounting_records (
    ts timestamp with time zone NOT NULL,
    session_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    appliance_id uuid NOT NULL,
    bytes_up bigint NOT NULL,
    bytes_down bigint NOT NULL
);

CREATE TABLE public.appliance_bootstrap_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    site_id uuid NOT NULL,
    expected_serial text,
    token_hash bytea NOT NULL,
    token_hint text NOT NULL,
    created_by uuid,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    consumed_by_appliance uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.auth_oidc_states (
    state text NOT NULL,
    nonce text NOT NULL,
    tenant_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    redirect_uri text NOT NULL,
    return_to text,
    client_ip inet,
    user_agent text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone
);

CREATE TABLE public.auth_otps (
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

CREATE TABLE public.guests (
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

CREATE TABLE public.idp_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    name text NOT NULL,
    display_name text NOT NULL,
    kind text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    auto_provision boolean DEFAULT true NOT NULL,
    discovery_url text,
    authorize_url text,
    token_url text,
    userinfo_url text,
    issuer text,
    jwks_url text,
    client_id text,
    client_secret text,
    scopes text[] DEFAULT ARRAY['openid'::text, 'email'::text, 'profile'::text] NOT NULL,
    sub_claim text DEFAULT 'sub'::text NOT NULL,
    email_claim text DEFAULT 'email'::text NOT NULL,
    name_claim text DEFAULT 'name'::text NOT NULL,
    groups_claim text,
    claims_map jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT idp_providers_kind_check CHECK ((kind = ANY (ARRAY['oidc'::text, 'saml'::text, 'stub'::text])))
);

CREATE TABLE public.invoice_lines (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    invoice_id uuid NOT NULL,
    description text NOT NULL,
    quantity integer DEFAULT 1 NOT NULL,
    unit_cents integer DEFAULT 0 NOT NULL,
    amount_cents integer DEFAULT 0 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL
);

CREATE TABLE public.invoices (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    subscription_id uuid,
    external_ref text,
    number text,
    status text NOT NULL,
    currency text NOT NULL,
    subtotal_cents integer DEFAULT 0 NOT NULL,
    tax_cents integer DEFAULT 0 NOT NULL,
    total_cents integer DEFAULT 0 NOT NULL,
    period_start timestamp with time zone,
    period_end timestamp with time zone,
    issued_at timestamp with time zone DEFAULT now() NOT NULL,
    due_at timestamp with time zone,
    paid_at timestamp with time zone,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT invoices_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'open'::text, 'paid'::text, 'void'::text, 'uncollectible'::text])))
);

CREATE TABLE public.networks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    appliance_id uuid NOT NULL,
    ssid text,
    vlan_id integer,
    cidr cidr NOT NULL,
    gateway inet NOT NULL,
    dhcp_enabled boolean DEFAULT true NOT NULL,
    purpose text DEFAULT 'guest'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT networks_purpose_check CHECK ((purpose = ANY (ARRAY['guest'::text, 'staff'::text, 'mgmt'::text]))),
    CONSTRAINT networks_vlan_id_check CHECK (((vlan_id IS NULL) OR ((vlan_id >= 0) AND (vlan_id <= 4094))))
);

CREATE TABLE public.notification_providers (
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

CREATE TABLE public.payments (
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

CREATE TABLE public.pms_attempts (
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

CREATE TABLE public.pms_providers (
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

CREATE TABLE public.sessions (
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

CREATE TABLE public.social_oauth_providers (
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

CREATE TABLE public.social_oauth_states (
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

CREATE TABLE public.stripe_accounts (
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

CREATE TABLE public.stripe_events (
    event_id text NOT NULL,
    tenant_id uuid NOT NULL,
    event_type text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.tenant_limit_overrides (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    key text NOT NULL,
    value_type text NOT NULL,
    int_value bigint,
    bool_value boolean,
    str_value text,
    reason text,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by uuid,
    starts_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tenant_limit_overrides_check CHECK ((((value_type = 'int'::text) AND (int_value IS NOT NULL) AND (bool_value IS NULL) AND (str_value IS NULL)) OR ((value_type = 'bool'::text) AND (bool_value IS NOT NULL) AND (int_value IS NULL) AND (str_value IS NULL)) OR ((value_type = 'string'::text) AND (str_value IS NOT NULL) AND (int_value IS NULL) AND (bool_value IS NULL)))),
    CONSTRAINT tenant_limit_overrides_value_type_check CHECK ((value_type = ANY (ARRAY['int'::text, 'bool'::text, 'string'::text])))
);

CREATE TABLE public.tenant_subscriptions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    status text NOT NULL,
    billing_cycle text NOT NULL,
    current_period_start timestamp with time zone NOT NULL,
    current_period_end timestamp with time zone NOT NULL,
    trial_end timestamp with time zone,
    cancel_at_period_end boolean DEFAULT false NOT NULL,
    canceled_at timestamp with time zone,
    ended_at timestamp with time zone,
    external_ref text,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    auto_renew boolean DEFAULT true NOT NULL,
    CONSTRAINT tenant_subscriptions_billing_cycle_check CHECK ((billing_cycle = ANY (ARRAY['monthly'::text, 'yearly'::text]))),
    CONSTRAINT tenant_subscriptions_check CHECK ((current_period_end > current_period_start)),
    CONSTRAINT tenant_subscriptions_status_check CHECK ((status = ANY (ARRAY['trialing'::text, 'active'::text, 'past_due'::text, 'canceled'::text, 'paused'::text, 'scheduled'::text])))
);

CREATE TABLE public.ticket_templates (
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

CREATE TABLE public.voucher_batches (
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

CREATE TABLE public.vouchers (
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

CREATE TABLE public.walled_garden_rules (
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

ALTER TABLE ONLY public.appliance_bootstrap_tokens
    ADD CONSTRAINT appliance_bootstrap_tokens_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.appliance_bootstrap_tokens
    ADD CONSTRAINT appliance_bootstrap_tokens_token_hash_key UNIQUE (token_hash);

ALTER TABLE ONLY public.auth_oidc_states
    ADD CONSTRAINT auth_oidc_states_pkey PRIMARY KEY (state);

ALTER TABLE ONLY public.auth_otps
    ADD CONSTRAINT auth_otps_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.guests
    ADD CONSTRAINT guests_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.guests
    ADD CONSTRAINT guests_tenant_id_mac_key UNIQUE (tenant_id, mac);

ALTER TABLE ONLY public.idp_providers
    ADD CONSTRAINT idp_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.idp_providers
    ADD CONSTRAINT idp_providers_tenant_id_name_key UNIQUE (tenant_id, name);

ALTER TABLE ONLY public.invoice_lines
    ADD CONSTRAINT invoice_lines_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.invoices
    ADD CONSTRAINT invoices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.networks
    ADD CONSTRAINT networks_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.notification_providers
    ADD CONSTRAINT notification_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_stripe_session_id_key UNIQUE (stripe_session_id);

ALTER TABLE ONLY public.pms_attempts
    ADD CONSTRAINT pms_attempts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.pms_providers
    ADD CONSTRAINT pms_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.social_oauth_providers
    ADD CONSTRAINT social_oauth_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.social_oauth_states
    ADD CONSTRAINT social_oauth_states_pkey PRIMARY KEY (state);

ALTER TABLE ONLY public.stripe_accounts
    ADD CONSTRAINT stripe_accounts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.stripe_events
    ADD CONSTRAINT stripe_events_pkey PRIMARY KEY (event_id);

ALTER TABLE ONLY public.tenant_limit_overrides
    ADD CONSTRAINT tenant_limit_overrides_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.tenant_limit_overrides
    ADD CONSTRAINT tenant_limit_overrides_tenant_id_key_key UNIQUE (tenant_id, key);

ALTER TABLE ONLY public.tenant_subscriptions
    ADD CONSTRAINT tenant_subscriptions_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.ticket_templates
    ADD CONSTRAINT ticket_templates_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.ticket_templates
    ADD CONSTRAINT ticket_templates_tenant_id_code_key UNIQUE (tenant_id, code);

ALTER TABLE ONLY public.voucher_batches
    ADD CONSTRAINT voucher_batches_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.vouchers
    ADD CONSTRAINT vouchers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.vouchers
    ADD CONSTRAINT vouchers_tenant_id_code_key UNIQUE (tenant_id, code);

ALTER TABLE ONLY public.walled_garden_rules
    ADD CONSTRAINT walled_garden_rules_pkey PRIMARY KEY (id);

CREATE INDEX accounting_records_session_idx ON public.accounting_records USING btree (session_id, ts DESC);

CREATE INDEX accounting_records_tenant_idx ON public.accounting_records USING btree (tenant_id, ts DESC);

CREATE INDEX accounting_records_ts_idx ON public.accounting_records USING btree (ts DESC);

CREATE INDEX appliance_bootstrap_tokens_tenant_idx ON public.appliance_bootstrap_tokens USING btree (tenant_id, consumed_at);

CREATE INDEX auth_oidc_states_expiry_idx ON public.auth_oidc_states USING btree (expires_at) WHERE (consumed_at IS NULL);

CREATE INDEX auth_otps_dest_idx ON public.auth_otps USING btree (tenant_id, channel, lower(destination), issued_at DESC);

CREATE INDEX auth_otps_recent_idx ON public.auth_otps USING btree (tenant_id, channel, lower(destination), issued_at) WHERE (consumed_at IS NULL);

CREATE INDEX guests_email_idx ON public.guests USING btree (tenant_id, lower(email)) WHERE (email IS NOT NULL);

CREATE INDEX guests_phone_idx ON public.guests USING btree (tenant_id, phone) WHERE (phone IS NOT NULL);

CREATE INDEX idp_providers_tenant_enabled_idx ON public.idp_providers USING btree (tenant_id) WHERE (enabled = true);

CREATE INDEX invoices_tenant_idx ON public.invoices USING btree (tenant_id, issued_at DESC);

CREATE UNIQUE INDEX notification_providers_tenant_channel_enabled_idx ON public.notification_providers USING btree (tenant_id, channel) WHERE (enabled = true);

CREATE INDEX payments_tenant_status_idx ON public.payments USING btree (tenant_id, status, created_at DESC);

CREATE INDEX pms_attempts_ip_idx ON public.pms_attempts USING btree (tenant_id, ip, attempted_at DESC);

CREATE INDEX pms_attempts_room_idx ON public.pms_attempts USING btree (tenant_id, lower(room_number), attempted_at DESC);

CREATE INDEX pms_providers_tenant_enabled_idx ON public.pms_providers USING btree (tenant_id) WHERE (enabled = true);

CREATE UNIQUE INDEX pms_providers_tenant_name_global_idx ON public.pms_providers USING btree (tenant_id, name) WHERE (site_id IS NULL);

CREATE INDEX pms_providers_tenant_site_enabled_idx ON public.pms_providers USING btree (tenant_id, site_id) WHERE (enabled = true);

CREATE UNIQUE INDEX pms_providers_tenant_site_name_idx ON public.pms_providers USING btree (tenant_id, site_id, name) WHERE (site_id IS NOT NULL);

CREATE INDEX sessions_active_expires_idx ON public.sessions USING btree (expires_at) WHERE (state = 'active'::text);

CREATE INDEX sessions_active_idle_idx ON public.sessions USING btree (last_activity_at) WHERE (state = 'active'::text);

CREATE INDEX sessions_appliance_active_idx ON public.sessions USING btree (appliance_id, state) WHERE (state = 'active'::text);

CREATE INDEX sessions_tenant_active_idx ON public.sessions USING btree (tenant_id, state) WHERE (state = 'active'::text);

CREATE UNIQUE INDEX social_oauth_providers_tenant_provider_enabled_idx ON public.social_oauth_providers USING btree (tenant_id, provider) WHERE (enabled = true);

CREATE INDEX social_oauth_states_expiry_idx ON public.social_oauth_states USING btree (expires_at) WHERE (consumed_at IS NULL);

CREATE UNIQUE INDEX stripe_accounts_tenant_enabled_idx ON public.stripe_accounts USING btree (tenant_id) WHERE (enabled = true);

CREATE INDEX stripe_events_received_at_idx ON public.stripe_events USING btree (received_at);

CREATE UNIQUE INDEX tenant_subscriptions_one_active ON public.tenant_subscriptions USING btree (tenant_id) WHERE (status = ANY (ARRAY['trialing'::text, 'active'::text, 'past_due'::text, 'paused'::text, 'scheduled'::text]));

CREATE INDEX tenant_subscriptions_period_end_idx ON public.tenant_subscriptions USING btree (current_period_end) WHERE (status = ANY (ARRAY['trialing'::text, 'active'::text, 'past_due'::text]));

CREATE INDEX tenant_subscriptions_tenant_idx ON public.tenant_subscriptions USING btree (tenant_id, status);

CREATE INDEX voucher_batches_tenant_idx ON public.voucher_batches USING btree (tenant_id, created_at DESC);

CREATE INDEX vouchers_batch_idx ON public.vouchers USING btree (batch_id);

CREATE UNIQUE INDEX vouchers_code_global_uniq ON public.vouchers USING btree (code);

CREATE INDEX vouchers_state_idx ON public.vouchers USING btree (tenant_id, state);

ALTER TABLE ONLY public.appliance_bootstrap_tokens
    ADD CONSTRAINT appliance_bootstrap_tokens_consumed_by_appliance_fkey FOREIGN KEY (consumed_by_appliance) REFERENCES public.appliances(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.appliance_bootstrap_tokens
    ADD CONSTRAINT appliance_bootstrap_tokens_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.operators(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.appliance_bootstrap_tokens
    ADD CONSTRAINT appliance_bootstrap_tokens_site_id_fkey FOREIGN KEY (site_id) REFERENCES public.sites(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.appliance_bootstrap_tokens
    ADD CONSTRAINT appliance_bootstrap_tokens_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.auth_oidc_states
    ADD CONSTRAINT auth_oidc_states_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.idp_providers(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.auth_oidc_states
    ADD CONSTRAINT auth_oidc_states_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.auth_otps
    ADD CONSTRAINT auth_otps_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES public.appliances(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.auth_otps
    ADD CONSTRAINT auth_otps_template_id_fkey FOREIGN KEY (template_id) REFERENCES public.ticket_templates(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.auth_otps
    ADD CONSTRAINT auth_otps_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.guests
    ADD CONSTRAINT guests_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.idp_providers
    ADD CONSTRAINT idp_providers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.invoice_lines
    ADD CONSTRAINT invoice_lines_invoice_id_fkey FOREIGN KEY (invoice_id) REFERENCES public.invoices(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.invoices
    ADD CONSTRAINT invoices_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES public.tenant_subscriptions(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.invoices
    ADD CONSTRAINT invoices_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.networks
    ADD CONSTRAINT networks_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES public.appliances(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.notification_providers
    ADD CONSTRAINT notification_providers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_site_id_fkey FOREIGN KEY (site_id) REFERENCES public.sites(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_template_id_fkey FOREIGN KEY (template_id) REFERENCES public.ticket_templates(id);

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_voucher_id_fkey FOREIGN KEY (voucher_id) REFERENCES public.vouchers(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.pms_attempts
    ADD CONSTRAINT pms_attempts_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES public.appliances(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.pms_attempts
    ADD CONSTRAINT pms_attempts_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.pms_providers
    ADD CONSTRAINT pms_providers_site_id_fkey FOREIGN KEY (site_id) REFERENCES public.sites(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.pms_providers
    ADD CONSTRAINT pms_providers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_guest_id_fkey FOREIGN KEY (guest_id) REFERENCES public.guests(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_site_id_fkey FOREIGN KEY (site_id) REFERENCES public.sites(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_voucher_id_fkey FOREIGN KEY (voucher_id) REFERENCES public.vouchers(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.social_oauth_providers
    ADD CONSTRAINT social_oauth_providers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.social_oauth_states
    ADD CONSTRAINT social_oauth_states_appliance_id_fkey FOREIGN KEY (appliance_id) REFERENCES public.appliances(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.social_oauth_states
    ADD CONSTRAINT social_oauth_states_template_id_fkey FOREIGN KEY (template_id) REFERENCES public.ticket_templates(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.social_oauth_states
    ADD CONSTRAINT social_oauth_states_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.stripe_accounts
    ADD CONSTRAINT stripe_accounts_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.stripe_events
    ADD CONSTRAINT stripe_events_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.tenant_limit_overrides
    ADD CONSTRAINT tenant_limit_overrides_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.operators(id);

ALTER TABLE ONLY public.tenant_limit_overrides
    ADD CONSTRAINT tenant_limit_overrides_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.tenant_subscriptions
    ADD CONSTRAINT tenant_subscriptions_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES public.plans(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.tenant_subscriptions
    ADD CONSTRAINT tenant_subscriptions_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.ticket_templates
    ADD CONSTRAINT ticket_templates_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.voucher_batches
    ADD CONSTRAINT voucher_batches_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.operators(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.voucher_batches
    ADD CONSTRAINT voucher_batches_template_id_fkey FOREIGN KEY (template_id) REFERENCES public.ticket_templates(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.voucher_batches
    ADD CONSTRAINT voucher_batches_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.vouchers
    ADD CONSTRAINT vouchers_batch_id_fkey FOREIGN KEY (batch_id) REFERENCES public.voucher_batches(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.vouchers
    ADD CONSTRAINT vouchers_template_id_fkey FOREIGN KEY (template_id) REFERENCES public.ticket_templates(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.vouchers
    ADD CONSTRAINT vouchers_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.walled_garden_rules
    ADD CONSTRAINT walled_garden_rules_site_id_fkey FOREIGN KEY (site_id) REFERENCES public.sites(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.walled_garden_rules
    ADD CONSTRAINT walled_garden_rules_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.subscription_events
    ADD CONSTRAINT subscription_events_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES public.tenant_subscriptions(id) ON DELETE SET NULL;

CREATE VIEW public.commercial_plans AS
 SELECT id,
    code,
    name,
    description,
    billing_cycle,
    price_cents,
    currency,
    trial_days,
    is_public,
    is_active,
    sort_order,
    metadata,
    created_at,
    updated_at
   FROM public.plans;

CREATE VIEW public.tenant_effective_limits AS
 WITH sub AS (
         SELECT ts.tenant_id,
            ts.plan_id
           FROM public.tenant_subscriptions ts
          WHERE (ts.status = ANY (ARRAY['trialing'::text, 'active'::text, 'past_due'::text, 'paused'::text]))
        ), plan_vals AS (
         SELECT s.tenant_id,
            pl.key,
            pl.value_type,
            pl.int_value,
            pl.bool_value,
            pl.str_value,
            pl.unit,
            'plan'::text AS source
           FROM (sub s
             JOIN public.plan_limits pl ON ((pl.plan_id = s.plan_id)))
        ), ovr_vals AS (
         SELECT o.tenant_id,
            o.key,
            o.value_type,
            o.int_value,
            o.bool_value,
            o.str_value,
            NULL::text AS unit,
            'override'::text AS source
           FROM public.tenant_limit_overrides o
          WHERE ((o.starts_at <= now()) AND ((o.expires_at IS NULL) OR (o.expires_at > now())))
        ), merged AS (
         SELECT ovr_vals.tenant_id,
            ovr_vals.key,
            ovr_vals.value_type,
            ovr_vals.int_value,
            ovr_vals.bool_value,
            ovr_vals.str_value,
            ovr_vals.unit,
            ovr_vals.source
           FROM ovr_vals
        UNION ALL
         SELECT pv.tenant_id,
            pv.key,
            pv.value_type,
            pv.int_value,
            pv.bool_value,
            pv.str_value,
            pv.unit,
            pv.source
           FROM (plan_vals pv
             LEFT JOIN ovr_vals ov ON (((ov.tenant_id = pv.tenant_id) AND (ov.key = pv.key))))
          WHERE (ov.tenant_id IS NULL)
        )
 SELECT tenant_id,
    key,
    value_type,
    int_value,
    bool_value,
    str_value,
    unit,
    source
   FROM merged;

-- accounting_records was a hypertable (0003). It is empty here, so this is a pure re-registration.
SELECT create_hypertable('public.accounting_records', 'ts', chunk_time_interval => INTERVAL '1 day',
                         create_default_indexes => false, if_not_exists => true);

COMMIT;
