# Legacy PMS provider secret columns (`public.pms_providers.auth_key`, `api_key`): remediation plan

Status: **AUTHORIZED 2026-11-06 and delivered as migration 0212** (`0212_the_retired_pms_provider_secrets_are_dropped_once_emptied`):
0170's own emptiness check again, applied by every upgrade, so any appliance in this state converges without a manual step. Run
on the test appliance after a verified pre-change backup; the receipts are in the delivery's governance record. The manual
procedure below is kept as the record of the plan and for an appliance that cannot take the upgrade.

## What is different, and why

Migration 0170 (`0170_a_the_retired_pms_provider_secrets_are_dropped_when_empty`) drops the two plaintext secret columns of the
retired PMS provider layer, but only when no row holds a value; otherwise it leaves them and says so. A fresh installation is built from
the production baseline, which no longer has them.

The test appliance (172.21.96.97) applied 0170 on 2026-10-29 at 04:55 UTC while legacy rows still held secrets, so 0170 correctly kept
the columns. The rows were removed afterwards. The migration does not run again, so the columns remain. This is the only schema
difference between that appliance and a fresh installation (catalog comparison of 2026-11-06, 14,936 facts; the other two
differences are the comparison's own superuser name and the erasure ledger's rows).

## Evidence (read-only, 2026-11-06, test appliance)

| Question | Answer |
|---|---|
| Rows in `public.pms_providers` | 0 |
| Column types | `auth_key text NULL`, `api_key text NULL`, no default |
| Objects that depend on the columns (views, constraints, indexes, `pg_depend`) | none |
| Column grants | only the table-level grants of `svc_edged` and `svc_scd`; nothing column-specific |
| Product code that reads or writes the columns | none: the provider layer is retired (AD-39). The two code paths that still name the table are column-agnostic: the cross-tenant purge (`data-plane/cmd/scd/tenant_transition.go`) deletes rows by tenant, and site migration (`data-plane/cmd/sitemigrate`) copies only the columns both sides have (`copy.go`, `intersectColumns`) |

## Proposed change

Run 0170's own body again on the appliance. It is idempotent and re-checks emptiness itself: with any row holding a value it changes
nothing.

```
docker exec -i stayconnect-pg psql -X -v ON_ERROR_STOP=1 -U stayconnect -d stayconnect_site \
  < /opt/stayconnect/data-plane/migrations/0170_a_the_retired_pms_provider_secrets_are_dropped_when_empty.up.sql
```

Before it, as for every schema change, take a verified pre-change backup: Admin Console -> Backups -> *Take a backup now*, or
`/opt/stayconnect/bin/scd backup-now --reason "pre-change: drop the retired PMS provider columns"`, and confirm it is listed as verified.

Verification after it:

- the catalog comparison with the fresh baseline shows no `public.pms_providers` difference;
- `parity:migrations` and the services stay PASS in the monitor;
- a site migration dry run between this appliance and a fresh one copies `pms_providers` without a drift note.

## Rollback

The columns hold no data, so re-creating them restores the schema exactly:

```
ALTER TABLE public.pms_providers ADD COLUMN auth_key text, ADD COLUMN api_key text;
```

The pre-change backup is the fallback for anything else.

## Other appliances

Any appliance upgraded through 0170 while it still had legacy provider rows is in the same state. The monitor's install parity check does
not compare the catalog, so this needs the catalog comparison above, or this check, run read-only on the appliance:

```
SELECT count(*) FROM information_schema.columns
 WHERE table_schema = 'public' AND table_name = 'pms_providers' AND column_name IN ('auth_key', 'api_key');
```

A result of 2 with an empty table means the next upgrade (0212) removes them; a non-empty table means the stored secrets must first be reviewed and the
rows removed by their owner.
