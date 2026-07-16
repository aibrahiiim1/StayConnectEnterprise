# iam_v2 — Phase 1A SCRATCH/TEST implementation

**Status: IMPLEMENTED-IN-SCRATCH and VERIFIED-IN-SCRATCH. NOT created on any live database. NOT cut over.**

This directory is the **Product-Owner-authorized Phase 1A implementation, executed strictly in a
dedicated disposable scratch/test PostgreSQL database** (a throwaway Docker container). It is **not**
production code, **not** a live migration, and touches **no** live database, service, PMS, network, or
deployment. See the FINAL contract and `docs/architecture/StayConnect-IAM-Phase1A-Plan.md`.

## What runs where

- **Scratch DB:** disposable container `iamv2-scratch` on `127.0.0.1:55432`, database `iam_scratch`,
  PostgreSQL 16.14. Credentials are a throwaway passed only to the one-off `docker run` command — **not**
  committed here.
- **Hard safety guard** (`lib.sh`): every DB call is refused if the target matches a known live
  identifier (`172.21.60.23`, `150.0.0.*`, `120.0.0.*`, `stayconnect_site`, `stayconnect-pg`,
  `/opt/stayconnect`, `appliance`, …), if the container is published on a non-loopback address, or if
  `current_database()` is not the scratch DB.

## Files

| File | Purpose |
|---|---|
| `lib.sh` | guarded psql runners + the live-target safety guard |
| `00_platform_fixture.sql` | minimal `public` stand-ins (tenants/sites/guest_networks) for the *existing* platform, so cross-schema FKs resolve |
| `mg0.sh` | **MG-0** anchor: duplicate pre-check → `CREATE UNIQUE INDEX CONCURRENTLY` (non-transactional) → validity guard (no bare `IF NOT EXISTS`) → `indisvalid`/exact-def verify → invalid-index detect + `DROP INDEX CONCURRENTLY` recovery |
| `migrations/mg1..mg9_*.sql` | the `iam_v2` schema (MG-1…MG-9): interfaces, plans/packages, identities/credentials, stays/folios, auth/commerce, entitlements/devices/sessions/accounting, postings/payments, resolution, and engine triggers/functions |
| `run.sh` | orchestrator: `fixture | up | down | reup | fresh` |
| `seed.sql` | deterministic base data for the A-series |
| `tests.sh` | core A-series (schema integrity, immutability, folio-UNSET fail-closed, entitlement guards, accounting idempotency, device admission, advisory namespaces, one-way posting) |
| `tests_extra.sh` | migration up/down/re-up, MG-0 interruption recovery, concurrency races, restart persistence, secret/PII scan |
| `EVIDENCE.txt` | captured PASS/FAIL evidence (Core 38/0, Extra 13/0, Isolation 6/6) |

## Reproduce

```sh
# a disposable local Postgres must be running as container 'iamv2-scratch' on 127.0.0.1:55432 (db iam_scratch)
bash run.sh fresh        # MG-0 (CONCURRENTLY) + MG-1..MG-9
psql < seed.sql          # via run.sh helpers
bash tests.sh            # core A-series
bash tests_extra.sh      # migration lifecycle, MG-0 recovery, races, restart, scans
```

## Boundaries (unchanged, enforced)

`folio_identity_strategy DEFAULT 'UNSET'` fail-closed CHARGE; isolated `iam_v2`; MG-0 non-transactional;
`programmatic_reversal=false` (no executable reversal / `PT=C` / negative-`TA` / reversal API/UI);
Stripe merchant FK deferred (no invented anchor); `AGGREGATE_ONLINE_TIME` inert; advisory namespaces
`LN_DEVICE_SLOT=11` / `LN_CAPACITY=7` (read from `data-plane/internal/session/session.go`).

**Next authorized step:** Product-Owner review of this scratch acceptance evidence, then a **separate**
explicit authorization to create dark `iam_v2` in the live `stayconnect_site` database (plan §7a/§11 ladder).
No live creation or cutover is authorized by this scratch work.
