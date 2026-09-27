-- REVERSES THE STRUCTURE OF 0047_central_cleanup. IT DOES NOT RESTORE DATA.
--
-- The legacy_archive schema comes back with the four tables 0046 moved into it, EMPTY: what 0047 deleted
-- (commercial history, and on the live Central the older guest/session/voucher/accounting archive) is gone
-- and only a backup taken before 0047 has it. The dropped columns come back with their original types and
-- defaults, not their old values. The retired-identity register is dropped, and with it the refusal of
-- retired identity keys. The truncated fetch log stays empty.

BEGIN;

DROP TABLE IF EXISTS retired_appliance_identities;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = 'public' AND table_name = 'appliances' AND column_name = 'registered_at') THEN
        ALTER TABLE appliances RENAME COLUMN registered_at TO enrolled_at;
    END IF;
END $$;

ALTER TABLE licenses ADD COLUMN IF NOT EXISTS commercial_plan_code text NOT NULL DEFAULT 'direct';
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS features jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS limits   jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE operator_roles ADD COLUMN IF NOT EXISTS site_id uuid;
CREATE INDEX IF NOT EXISTS operator_roles_site_idx ON operator_roles (site_id);

ALTER TABLE appliances ADD COLUMN IF NOT EXISTS identity_verified_at timestamptz;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS cert_fingerprint text;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS name text;
ALTER TABLE appliances ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
UPDATE appliances SET name = serial WHERE name IS NULL;
UPDATE appliances SET cert_fingerprint = current_cert_fingerprint WHERE cert_fingerprint IS NULL;

ALTER TABLE sites   ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS contact_email text;
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS auth_methods jsonb NOT NULL
    DEFAULT '{"voucher": {"enabled": true, "template_id": null}}'::jsonb;

ALTER TABLE operators ADD COLUMN IF NOT EXISTS auth_method text NOT NULL DEFAULT 'local';
ALTER TABLE operators ADD COLUMN IF NOT EXISTS oidc_sub text;
ALTER TABLE operators ADD COLUMN IF NOT EXISTS last_sso_login_at timestamptz;
ALTER TABLE operators DROP CONSTRAINT IF EXISTS operators_auth_method_check;
ALTER TABLE operators ADD CONSTRAINT operators_auth_method_check CHECK (auth_method IN ('local', 'sso'));
CREATE UNIQUE INDEX IF NOT EXISTS operators_oidc_sub_uniq ON operators (tenant_id, oidc_sub) WHERE oidc_sub IS NOT NULL;

-- legacy_archive, as 0046 left it, empty.
CREATE SCHEMA IF NOT EXISTS legacy_archive;
COMMENT ON SCHEMA legacy_archive IS
    'Commercial history from the retired plans/subscriptions model (migration 0046). Kept for the record; nothing reads it.';

CREATE TABLE IF NOT EXISTS legacy_archive.plans (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    code text NOT NULL,
    name text NOT NULL,
    description text,
    billing_cycle text NOT NULL,
    price_cents integer DEFAULT 0 NOT NULL,
    currency text DEFAULT 'USD'::text NOT NULL,
    trial_days integer DEFAULT 0 NOT NULL,
    is_public boolean DEFAULT true NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    limits_version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT plans_pkey PRIMARY KEY (id),
    CONSTRAINT plans_code_key UNIQUE (code),
    CONSTRAINT plans_billing_cycle_check CHECK ((billing_cycle = ANY (ARRAY['monthly'::text, 'yearly'::text])))
);
COMMENT ON TABLE legacy_archive.plans IS 'CommercialPlan: subscription plans sold by StayConnect to tenants. Not guest internet packages (those are ticket_templates / GuestAccessPlan, owned by the Site/Edge domain).';

CREATE TABLE IF NOT EXISTS legacy_archive.plan_limits (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    plan_id uuid NOT NULL,
    key text NOT NULL,
    value_type text NOT NULL,
    int_value bigint,
    bool_value boolean,
    str_value text,
    unit text,
    CONSTRAINT plan_limits_pkey PRIMARY KEY (id),
    CONSTRAINT plan_limits_plan_id_key_key UNIQUE (plan_id, key),
    CONSTRAINT plan_limits_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES legacy_archive.plans(id) ON DELETE CASCADE,
    CONSTRAINT plan_limits_check CHECK ((((value_type = 'int'::text) AND (int_value IS NOT NULL) AND (bool_value IS NULL) AND (str_value IS NULL)) OR ((value_type = 'bool'::text) AND (bool_value IS NOT NULL) AND (int_value IS NULL) AND (str_value IS NULL)) OR ((value_type = 'string'::text) AND (str_value IS NOT NULL) AND (int_value IS NULL) AND (bool_value IS NULL)))),
    CONSTRAINT plan_limits_value_type_check CHECK ((value_type = ANY (ARRAY['int'::text, 'bool'::text, 'string'::text])))
);

CREATE TABLE IF NOT EXISTS legacy_archive.plan_limit_history (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    plan_id uuid NOT NULL,
    version bigint NOT NULL,
    key text NOT NULL,
    old_value jsonb,
    new_value jsonb,
    change_type text NOT NULL,
    reason text,
    actor_id uuid,
    changed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT plan_limit_history_pkey PRIMARY KEY (id),
    CONSTRAINT plan_limit_history_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES legacy_archive.plans(id) ON DELETE CASCADE,
    CONSTRAINT plan_limit_history_change_type_check CHECK ((change_type = ANY (ARRAY['set'::text, 'removed'::text])))
);
CREATE INDEX IF NOT EXISTS idx_plan_limit_history_plan ON legacy_archive.plan_limit_history (plan_id, version DESC);

CREATE TABLE IF NOT EXISTS legacy_archive.subscription_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    subscription_id uuid,
    type text NOT NULL,
    from_plan_id uuid,
    to_plan_id uuid,
    at timestamp with time zone DEFAULT now() NOT NULL,
    actor_type text,
    actor_id text,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    change_type text,
    CONSTRAINT subscription_events_pkey PRIMARY KEY (id),
    CONSTRAINT subscription_events_from_plan_id_fkey FOREIGN KEY (from_plan_id) REFERENCES legacy_archive.plans(id),
    CONSTRAINT subscription_events_to_plan_id_fkey FOREIGN KEY (to_plan_id) REFERENCES legacy_archive.plans(id),
    CONSTRAINT subscription_events_actor_type_check CHECK ((actor_type = ANY (ARRAY['operator'::text, 'system'::text, 'billing'::text, 'api'::text]))),
    CONSTRAINT subscription_events_change_type_check CHECK (((change_type IS NULL) OR (change_type = ANY (ARRAY['upgrade'::text, 'downgrade'::text, 'lateral'::text]))))
);
CREATE INDEX IF NOT EXISTS subscription_events_change_type_idx ON legacy_archive.subscription_events (tenant_id, change_type) WHERE (change_type IS NOT NULL);
CREATE INDEX IF NOT EXISTS subscription_events_tenant_idx ON legacy_archive.subscription_events (tenant_id, at DESC);

COMMIT;
