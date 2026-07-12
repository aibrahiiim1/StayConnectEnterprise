-- Voucher batches: a group of vouchers created together from a single template.

CREATE TABLE IF NOT EXISTS voucher_batches (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    template_id  uuid NOT NULL REFERENCES ticket_templates(id) ON DELETE RESTRICT,
    name         text,
    note         text,
    count        int  NOT NULL CHECK (count > 0),
    created_by   uuid REFERENCES operators(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS voucher_batches_tenant_idx ON voucher_batches(tenant_id, created_at DESC);

-- Link vouchers to their batch. ON DELETE SET NULL keeps voucher rows after
-- batch deletion (for accounting integrity).
ALTER TABLE vouchers
    DROP CONSTRAINT IF EXISTS vouchers_batch_id_fkey,
    ADD  CONSTRAINT vouchers_batch_id_fkey
        FOREIGN KEY (batch_id) REFERENCES voucher_batches(id) ON DELETE SET NULL;

-- Global uniqueness for voucher codes — mitigates cross-tenant collision on
-- the 60-bit Crockford space and lets portals look up by code alone.
CREATE UNIQUE INDEX IF NOT EXISTS vouchers_code_global_uniq ON vouchers(code);

INSERT INTO schema_migrations(version) VALUES ('0006_voucher_batches') ON CONFLICT DO NOTHING;
