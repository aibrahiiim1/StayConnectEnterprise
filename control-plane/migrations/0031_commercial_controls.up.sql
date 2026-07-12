-- Commercial controls: operator-chosen subscription activation, override effective
-- windows, and versioned/audited plan-limit changes.

-- 1. Subscriptions -----------------------------------------------------------
-- 'scheduled' = agreed but not yet in force (starts in the future). It is
-- deliberately NOT an entitlement-granting state: it is excluded from the
-- effective-limits view and from license issuance, so a scheduled customer gets
-- no license until it activates.
ALTER TABLE tenant_subscriptions DROP CONSTRAINT IF EXISTS tenant_subscriptions_status_check;
ALTER TABLE tenant_subscriptions ADD CONSTRAINT tenant_subscriptions_status_check
    CHECK (status IN ('trialing','active','past_due','canceled','paused','scheduled'));
ALTER TABLE tenant_subscriptions ADD COLUMN IF NOT EXISTS auto_renew boolean NOT NULL DEFAULT true;

-- Only one non-terminal subscription per tenant (now including 'scheduled').
DROP INDEX IF EXISTS tenant_subscriptions_one_active;
CREATE UNIQUE INDEX IF NOT EXISTS tenant_subscriptions_one_active
    ON tenant_subscriptions(tenant_id)
    WHERE status IN ('trialing','active','past_due','paused','scheduled');

-- 2. Tenant overrides: effective window --------------------------------------
ALTER TABLE tenant_limit_overrides ADD COLUMN IF NOT EXISTS starts_at timestamptz NOT NULL DEFAULT now();

-- 3. Plan-limit versioning + audit -------------------------------------------
-- Plan limits are the vendor product definition. Every change is recorded here so
-- a limit can be traced. Changing a plan NEVER rewrites an already-issued signed
-- license (licenses are signed snapshots); a license must be re-issued to pick up
-- new terms.
CREATE TABLE IF NOT EXISTS plan_limit_history (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id     uuid NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    version     bigint NOT NULL,
    key         text NOT NULL,
    old_value   jsonb,
    new_value   jsonb,
    change_type text NOT NULL CHECK (change_type IN ('set','removed')),
    reason      text,
    actor_id    uuid,
    changed_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_plan_limit_history_plan ON plan_limit_history (plan_id, version DESC);

-- Monotonic version per plan, bumped on every limit change.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS limits_version bigint NOT NULL DEFAULT 1;

-- 4. Effective limits: plan -> subscription -> tenant override ----------------
-- Deterministic resolution. An override applies ONLY inside its effective window
-- [starts_at, expires_at); outside it the plan value stands.
CREATE OR REPLACE VIEW tenant_effective_limits AS
WITH sub AS (
    SELECT ts.tenant_id, ts.plan_id
      FROM tenant_subscriptions ts
     WHERE ts.status IN ('trialing','active','past_due','paused')   -- 'scheduled' grants nothing
),
plan_vals AS (
    SELECT s.tenant_id, pl.key, pl.value_type, pl.int_value, pl.bool_value, pl.str_value, pl.unit,
           'plan'::text AS source
      FROM sub s
      JOIN plan_limits pl ON pl.plan_id = s.plan_id
),
ovr_vals AS (
    SELECT o.tenant_id, o.key, o.value_type, o.int_value, o.bool_value, o.str_value, NULL::text AS unit,
           'override'::text AS source
      FROM tenant_limit_overrides o
     WHERE o.starts_at <= now()
       AND (o.expires_at IS NULL OR o.expires_at > now())
),
merged AS (
    SELECT * FROM ovr_vals
    UNION ALL
    SELECT pv.*
      FROM plan_vals pv
      LEFT JOIN ovr_vals ov
        ON ov.tenant_id = pv.tenant_id AND ov.key = pv.key
     WHERE ov.tenant_id IS NULL
)
SELECT * FROM merged;
