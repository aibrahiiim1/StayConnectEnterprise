-- 0019: cloud licensing + fleet telemetry (edge-first refactor, phase 2).
--
-- Terminology: `plans` are COMMERCIAL SUBSCRIPTION PLANS (what StayConnect
-- sells to a hotel group). Guest internet packages are `ticket_templates`
-- (GuestAccessPlan) and live in the Site/Edge domain, NOT here.

BEGIN;

COMMENT ON TABLE plans IS
  'CommercialPlan: subscription plans sold by StayConnect to tenants. Not guest internet packages (those are ticket_templates / GuestAccessPlan, owned by the Site/Edge domain).';

-- Read-only alias so new code and operators can use the unambiguous name.
CREATE VIEW commercial_plans AS SELECT * FROM plans;

-- ---------------------------------------------------------------------------
-- Vendor-signed licenses. The signed_envelope column stores the exact JSON
-- envelope delivered to the appliance (payload+signature+key_id); the other
-- columns are queryable projections of the same payload.
-- ---------------------------------------------------------------------------
CREATE TABLE licenses (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  site_id              uuid NOT NULL REFERENCES sites(id)   ON DELETE CASCADE,
  commercial_plan_code text NOT NULL,
  status               text NOT NULL DEFAULT 'active'
                         CHECK (status IN ('active','suspended','revoked','superseded')),
  issued_at            timestamptz NOT NULL DEFAULT now(),
  valid_until          timestamptz NOT NULL,
  offline_grace_days   int  NOT NULL DEFAULT 30 CHECK (offline_grace_days BETWEEN 0 AND 365),
  appliance_ids        uuid[] NOT NULL DEFAULT '{}',
  features             jsonb NOT NULL DEFAULT '{}'::jsonb,
  limits               jsonb NOT NULL DEFAULT '{}'::jsonb,
  signed_envelope      text NOT NULL,
  key_id               text NOT NULL,
  created_by           uuid REFERENCES operators(id) ON DELETE SET NULL,
  revoked_at           timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now()
);

-- Exactly one current (enforceable) license per site.
CREATE UNIQUE INDEX licenses_one_current_per_site
  ON licenses (site_id) WHERE status IN ('active','suspended');
CREATE INDEX licenses_tenant_idx ON licenses (tenant_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Fleet telemetry: aggregated, non-PII operational summaries pushed by
-- appliances through the sync outbox. Idempotent ingestion is enforced by
-- the (appliance_id, seq) dedupe gate — replays are dropped, not duplicated.
-- ---------------------------------------------------------------------------
CREATE TABLE fleet_telemetry (
  ts           timestamptz NOT NULL DEFAULT now(),
  tenant_id    uuid NOT NULL,
  site_id      uuid,
  appliance_id uuid NOT NULL,
  kind         text NOT NULL CHECK (kind IN
                 ('heartbeat','health','usage','auth_counts','pms_health',
                  'license_ack','backup','sync','update_progress')),
  seq          bigint NOT NULL,
  payload      jsonb NOT NULL DEFAULT '{}'::jsonb
);
SELECT create_hypertable('fleet_telemetry', 'ts', chunk_time_interval => INTERVAL '7 days');
CREATE INDEX fleet_telemetry_appliance_idx ON fleet_telemetry (appliance_id, ts DESC);
CREATE INDEX fleet_telemetry_tenant_idx    ON fleet_telemetry (tenant_id, kind, ts DESC);

CREATE TABLE fleet_telemetry_dedupe (
  appliance_id uuid   NOT NULL,
  seq          bigint NOT NULL,
  received_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (appliance_id, seq)
);

-- ---------------------------------------------------------------------------
-- New commercial entitlement keys used by the signed license document.
-- paid_wifi: pro+enterprise; guest access plan cap by tier.
-- ---------------------------------------------------------------------------
INSERT INTO plan_limits (plan_id, key, value_type, bool_value)
SELECT p.id, 'feature.paid_wifi', 'bool', (p.code NOT LIKE 'starter%')
  FROM plans p
ON CONFLICT (plan_id, key) DO NOTHING;

INSERT INTO plan_limits (plan_id, key, value_type, int_value, unit)
SELECT p.id, 'max_guest_access_plans', 'int',
       CASE WHEN p.code LIKE 'starter%' THEN 10
            WHEN p.code LIKE 'pro%'     THEN 100
            ELSE -1 END,
       'plans'
  FROM plans p
ON CONFLICT (plan_id, key) DO NOTHING;

INSERT INTO schema_migrations(version) VALUES ('0019_licensing_fleet')
ON CONFLICT DO NOTHING;

COMMIT;
