-- 0048 — bring the edge command-channel tables into the migration ledger.
--
-- edge_executed_commands, edge_installed_updates and edge_offline_packages were created by scd at RUNTIME
-- with CREATE TABLE IF NOT EXISTS, and deploy/scripts/phase7-appliance-m4.sh says so in as many words:
-- "No migration creates edge_executed_commands, edge_installed_updates or edge_offline_packages; scd
-- creates [them]".
--
-- That is fine on a machine that has already run scd, and fatal on a factory-clean install: Gate-P grants
-- SELECT/INSERT on these tables, and gatep-grants.sql is applied BEFORE any service starts. A clean build
-- therefore stopped with `relation "public.edge_executed_commands" does not exist` -- the schema was
-- complete, and the privilege bootstrap could not run against it.
--
-- Declaring them here removes the ordering dependency on a running service, and puts three tables that were
-- invisible to the ledger under the same auditable path as everything else. Shapes match the accepted
-- appliance exactly (verified column by column against 172.21.60.23, read-only).
--
-- IF NOT EXISTS throughout: on an existing appliance scd has already created them, and this migration must
-- be a no-op there rather than a conflict.
CREATE TABLE IF NOT EXISTS public.edge_executed_commands (
    command_id   uuid PRIMARY KEY,
    command_type text,
    status       text,
    result       jsonb,
    completed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.edge_installed_updates (
    update_id    uuid PRIMARY KEY,
    component    text,
    version      text,
    status       text,
    installed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.edge_offline_packages (
    package_id    uuid PRIMARY KEY,
    nonce         text UNIQUE,
    consumed_at   timestamptz NOT NULL DEFAULT now(),
    reconciled_at timestamptz
);
