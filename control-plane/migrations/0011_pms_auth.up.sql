-- Phase 4.5 — PMS-backed guest auth (Protel/Opera/Fidelio via FIAS, etc.)
--
-- Validation data is room_number + ONE OF (first_name | last_name |
-- reservation_number). It is NOT a password — case-insensitive matching for
-- names; lockout protects against brute-force enumeration.
--
-- pms_attempts records every verify call (success or failure) for two
-- purposes: per-room lockout and per-IP rate-limit.

CREATE TABLE IF NOT EXISTS pms_attempts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    appliance_id    uuid REFERENCES appliances(id) ON DELETE SET NULL,
    room_number     text NOT NULL,
    secondary_kind  text NOT NULL CHECK (secondary_kind IN ('first_name','last_name','reservation','either')),
    ip              inet,
    success         boolean NOT NULL,
    error_code      text,                       -- 'not_found','mismatch','upstream_fail','locked'
    attempted_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS pms_attempts_room_idx
    ON pms_attempts(tenant_id, lower(room_number), attempted_at DESC);
CREATE INDEX IF NOT EXISTS pms_attempts_ip_idx
    ON pms_attempts(tenant_id, ip, attempted_at DESC);

-- Plan-tier feature flag for PMS auth. Premium-only by default; explicitly
-- enable for any tier you sell PMS integration to.
WITH targets AS (
    SELECT id, code, regexp_replace(code, '-(monthly|yearly)$', '') AS tier FROM plans
)
INSERT INTO plan_limits (plan_id, key, value_type, bool_value)
SELECT t.id, 'feature.auth.pms', 'bool', x.allowed
FROM targets t
CROSS JOIN LATERAL (
    VALUES ('starter', false), ('pro', true), ('enterprise', true)
) AS x(tier, allowed)
WHERE t.tier = x.tier
ON CONFLICT (plan_id, key) DO NOTHING;

-- Note: we do NOT add a column to tenants for pms — it lives inside the
-- existing tenants.auth_methods jsonb under the "pms" key, shape:
--   {
--     "pms": {
--       "enabled":     true,
--       "template_id": "<uuid>",
--       "provider":    "stub" | "protel-fias" | "opera-fias" | ...,
--       "mode":        "room_lastname" | "room_firstname" | "room_reservation" | "either",
--       "max_failures_per_room": 5,        -- optional override (default 5)
--       "lockout_window_minutes": 15       -- optional override (default 15)
--     }
--   }

INSERT INTO schema_migrations(version) VALUES ('0011_pms_auth') ON CONFLICT DO NOTHING;
