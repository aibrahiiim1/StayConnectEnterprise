-- Configurable voucher generation + Guest Username/Password Accounts.
-- Backward compatible: existing voucher_batches/vouchers are untouched; new
-- columns are nullable / defaulted.

-- 1) Voucher batch generation metadata (for display + reproducibility). NULL on
--    pre-existing batches (they used the legacy 12-char Crockford format).
ALTER TABLE voucher_batches ADD COLUMN IF NOT EXISTS code_length       integer;
ALTER TABLE voucher_batches ADD COLUMN IF NOT EXISTS char_mode         text;
ALTER TABLE voucher_batches ADD COLUMN IF NOT EXISTS code_prefix       text;
ALTER TABLE voucher_batches ADD COLUMN IF NOT EXISTS exclude_ambiguous boolean;

-- 2) Guest Username/Password Accounts — a first-class auth method, separate from
--    vouchers. One account = a username + argon2id password hash, bound to a
--    Guest Access Plan (ticket_templates) which supplies duration/data-cap/speed/
--    max-devices/price. tenant_id/site_id are the appliance-local mirror owners
--    (same as vouchers), so accounts are tenant-isolated and purged on a
--    cross-customer transition.
CREATE TABLE IF NOT EXISTS guest_accounts (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    site_id       uuid REFERENCES sites(id) ON DELETE SET NULL,
    template_id   uuid NOT NULL REFERENCES ticket_templates(id) ON DELETE RESTRICT,
    username      text NOT NULL,
    password_hash text NOT NULL,           -- argon2id; never returned by any API
    display_name  text,
    notes         text,
    enabled       boolean NOT NULL DEFAULT true,
    valid_from    timestamptz,
    valid_until   timestamptz,
    last_login_at timestamptz,
    login_count   bigint NOT NULL DEFAULT 0,
    failed_attempts integer NOT NULL DEFAULT 0,   -- for temporary lockout
    locked_until  timestamptz,
    created_by    uuid,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
-- Case-insensitive unique username per tenant.
CREATE UNIQUE INDEX IF NOT EXISTS guest_accounts_tenant_username_uniq
    ON guest_accounts (tenant_id, lower(username));
CREATE INDEX IF NOT EXISTS guest_accounts_tenant_idx ON guest_accounts (tenant_id);

-- 3) Link a session to the guest account that created it (nullable, like
--    voucher_id). Enables per-account usage/accounting and last-login tracking.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS guest_account_id uuid
    REFERENCES guest_accounts(id) ON DELETE SET NULL;
