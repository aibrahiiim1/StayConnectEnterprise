-- Reverse of 0045.
--
-- The TABLES come back empty. The 251 166 telemetry rows do not, and cannot: they were deleted on a
-- Product-Owner decision, not moved aside. A rollback of this migration restores the shape of a collection
-- that no longer has any code to fill it, which is the honest thing for a down migration to do -- it must
-- not be read as a way to recover the data.

BEGIN;

CREATE TABLE IF NOT EXISTS fleet_telemetry (
    ts          timestamptz NOT NULL DEFAULT now(),
    tenant_id   uuid,
    site_id     uuid,
    appliance_id uuid NOT NULL,
    kind        text NOT NULL,
    seq         bigint NOT NULL,
    payload     jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS fleet_telemetry_dedupe (
    appliance_id uuid NOT NULL,
    seq          bigint NOT NULL,
    seen_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (appliance_id, seq)
);

COMMIT;
