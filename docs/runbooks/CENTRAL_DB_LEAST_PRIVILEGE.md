# Central database least privilege (migration 0063)

Audit P3 M1 and P3 L2. Until 0063, ctrlapi connected to Postgres as `stayconnect`, the superuser that owns every table:
a SQL-injection or code-execution bug in ctrlapi was full control of the database (rewrite the audit trail, alter
triggers, `COPY ... PROGRAM`). Now ctrlapi connects as a data-only role.

## Roles

| Role | Used by | May |
|---|---|---|
| `stayconnect` (owner, image superuser; secret `db_password`) | migrations (`central-migrate.sh`), `pg_dump`/`pg_restore`, the backup and export scripts, first-admin seeding (`ctrlapi seed-admin` in `central-install.sh`), the scripts' `pg_q` | everything |
| `ctrlapi_rt` (secret `ctrlapi_db_password`) | `ctrlapi` (`CTRLAPI_DB_URL`), and the `ctrlapi reset-mfa` / `unlock-signin` operator commands that read the same file | see below |

`ctrlapi_rt`: `LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`, owns nothing.

* `CONNECT` on the database; `USAGE` on schema `public`; **no `CREATE`** on `public` (also revoked from `PUBLIC`).
* `SELECT, INSERT, UPDATE, DELETE` on the application tables; `USAGE, SELECT, UPDATE` on sequences.
* **`SELECT, INSERT` only** (no update, no delete) on: `audit_log`, `portal_tls_events`, `appliance_lifecycle_events`,
  `appliance_certificate_events`, `appliance_certificate_revocations`, `appliance_assignments`,
  `appliance_assignment_history`, `appliance_history_archive`, `retired_appliance_identities`, `appliance_ca_versions`.
* `SELECT` only on `schema_migrations` and views; **nothing** on `central_forget_pending`.
* No `TRUNCATE`, `TRIGGER`, `REFERENCES`; cannot alter or drop anything, create roles, read `pg_authid`, use `COPY ... PROGRAM`
  or read server files.
* `EXECUTE` on the four SECURITY DEFINER functions, owned by `stayconnect`, which are the only way it changes append-only
  history: `central_retention_apply()`, `central_forget_actor()`, `central_forget_audit_apply()`,
  `central_correct_actor_email()`. They set `central.retention = 'on'` themselves; the guards on `audit_log` and
  `portal_tls_events` accept a change only when that flag is on **and** the current user is the table owner, so
  `SET central.retention = 'on'` from `ctrlapi_rt` achieves nothing.
* `central_apply_runtime_grants()` (owner only) re-asserts all of the above; it is idempotent.

Future tables: `ALTER DEFAULT PRIVILEGES IN SCHEMA public` (no `FOR ROLE`, so it applies to objects created by the role
that runs the migrations, `stayconnect`) gives `ctrlapi_rt` full DML on every table and `USAGE, SELECT, UPDATE` on every sequence a
later migration creates. A migration that creates an **append-only** table must add it to the `ro` list in
`central_apply_runtime_grants()` (`CREATE OR REPLACE`) and `REVOKE UPDATE, DELETE` on it. Run
`scripts/ci/central-restricted-role-tests.sh` after adding a table.

## The password

Never in a migration, never committed, never printed. `central_gen_secret ctrlapi_db_password` creates
`/opt/stayconnect/central/secrets/ctrlapi_db_password` (64 hex characters, 0600) **only if absent**, so an upgrade is
idempotent. `ALTER ROLE ctrlapi_rt PASSWORD ...` receives it on psql's stdin (not on a command line), and the login check
passes it through `PGPASSWORD` handed to docker by name.

## Fresh install

`central-install.sh` generates both secrets, migrates (creating the role without a password), runs
`central_runtime_role_apply` (grants, password, proven login), renders `ctrlapi.env` from `deploy/env/ctrlapi.env.example`
with the **runtime** password, seeds the first admin as the owner, and starts ctrlapi. `--mode restore` restores the dump with
`--no-acl` and re-asserts the grants.

## Upgrade of an existing Central

`central-deploy.sh` does this itself after `central-migrate.sh up`, while ctrlapi is stopped:

1. generate `secrets/ctrlapi_db_password` if absent;
2. `SELECT central_apply_runtime_grants()` (0063 already did it; this repairs drift);
3. `ALTER ROLE ctrlapi_rt PASSWORD` from the file;
4. log in as `ctrlapi_rt` with that password: must be non-superuser, able to read, unable to create;
5. only then rewrite `CTRLAPI_DB_URL` in `/etc/stayconnect/ctrlapi.env` (host, port, database and parameters kept),
   after copying the old file to `/etc/stayconnect/ctrlapi.env.bak-dbrole-<UTC stamp>` (0600).

If any step fails, the env file is not touched, the deploy continues on the previous DSN and prints
`DATABASE LEAST PRIVILEGE IS NOT ACTIVE`. Re-run the deploy after fixing the cause.

## Rollback

```sh
systemctl stop stayconnect-ctrlapi
cp -p /etc/stayconnect/ctrlapi.env.bak-dbrole-<stamp> /etc/stayconnect/ctrlapi.env     # owner DSN again
systemctl start stayconnect-ctrlapi
```

If the `.bak-dbrole-<stamp>` copy is gone (the retention sweep used to remove the newest copy of an unchanged file; fixed by PR #270, 2026-10-29), build the owner DSN
again instead: set `CTRLAPI_DB_URL` in `/etc/stayconnect/ctrlapi.env` to
`postgres://stayconnect:<the contents of /opt/stayconnect/central/secrets/db_password>@127.0.0.1:5432/stayconnect?sslmode=disable` (keep the host, port and database
that the current URL has), then start ctrlapi. `central_owner_dsn` in `deploy/scripts/central-lib.sh` builds exactly this.

The role and the grants may stay; they do nothing while nothing connects as `ctrlapi_rt`. To remove the migration as well:
stop ctrlapi, restore the env file, then run `0063_central_runtime_role_least_privilege.down.sql` by hand as `stayconnect`
(`central-migrate.sh` has no automatic down; the file restores the 0054/0055/0056 function bodies and drops the role) and
delete the `0063_...` row from `schema_migrations`. Rolling the **binary** back (`central-deploy.sh rollback`) keeps the role DSN: the
previous binary works with the role, but it does not drain `central_forget_pending`, so a removed user's email stays in old
audit payloads until the next new binary runs `central_forget_audit_apply()`.

## Verify in psql

```sh
docker exec sc-central-pg psql -U stayconnect -d stayconnect -c "\du ctrlapi_rt"
docker exec sc-central-pg psql -U stayconnect -d stayconnect -c "\dp audit_log" -c "\dp operators" -c "\ddp"
docker exec sc-central-pg psql -U stayconnect -d stayconnect -tAc "SELECT has_table_privilege('ctrlapi_rt','audit_log','UPDATE'), has_schema_privilege('ctrlapi_rt','public','CREATE')"   # f|f
grep -c '^CTRLAPI_DB_URL=postgres://ctrlapi_rt:' /etc/stayconnect/ctrlapi.env                                                                   # 1
docker exec sc-central-pg psql -U stayconnect -d stayconnect -tAc "SELECT usename, count(*) FROM pg_stat_activity WHERE datname='stayconnect' GROUP BY 1"
```

## Tests

`scripts/ci/central-restricted-role-tests.sh [--owner-baseline] [--pkgs ...] [--run REGEX]` starts a throwaway TimescaleDB,
migrates it, sets the role password from a generated secret and runs `go test` for `control-plane` with the pool
connected as `ctrlapi_rt` (`CTRLAPI_TEST_DB_APP_ROLE` / `CTRLAPI_TEST_DB_APP_PASSWORD`, `internal/testdb`). The
assertions of what the role cannot do are in `internal/api/runtime_role_integration_test.go`.

## P3 L2: forgetting a removed user

`central_forget_actor` now rewrites only the small history columns inside the operator `DELETE` and queues the email in
`central_forget_pending`. `central_forget_audit_apply()` scrubs `audit_log` payloads afterwards, case-insensitively,
one week of the hypertable per statement, outside the delete transaction (ctrlapi calls it right after a removal commits
and on every housekeeping pass). The queue row exists only until the scrub has run.
