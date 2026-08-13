# Phase 4 — Final Live-Acceptance Record

**Decision D19 · closure transition T0044 · 2026-08-13 · ACCEPTED_AND_CLOSED — VERIFIED LIVE-DARK / NO-FINANCIAL-TRAFFIC**

This record exists to keep three things separable that are easy to blur: what CI proved, what the appliance
proved, and what the Product Owner decided. Everything below was measured; nothing was inferred from
something else being green.

---

## 1. Software-CI evidence

| Item | Value |
|---|---|
| Accepted software candidate | `b94112d8cb0ab63938b60f829ddd465c14491f97` |
| Workflow | Phase 4 Financial Core CI (`.github/workflows/phase4-financial-core.yml`) |
| Run | **31690016483 — SUCCESS**, 36 / 36 steps |
| Evidence artifact | **9177140558** |
| Digest | `sha256:aacb6e9f687f872632b36a1dfe5c4598340d2108d1e70f31ee6c48cae93ba52d` |
| Pre-acceptance delivery head | `105af49b9a0b44e6eda131b08f7d5fa6a37a2bbc` |
| Run | **31691250489 — SUCCESS**, 36 / 36 steps |
| Evidence artifact | **9177645571** |
| Digest | `sha256:1b03e9863d5a8b7ec79a01b89574ad82e8fc9822d06c5561bc29366f6d098200` |

What the gate covers: gofmt, build, vet, the unit matrix, the race detector, the pre-0011 financial
invariant suite on both chains, **migrations 0011–0026** with the full DB gate (269 assertions including the
whole-chain DOWN→UP structural equality), the PG16 integration matrix, the least-privilege role proof, the
payment-concurrency proof with real concurrent transactions, the supported restore drill, the Hotel-Admin
typecheck / unit / flags-OFF build / full browser suite, the dependency gate and its self-test, the
current-state parity check and its self-test, and the DARK static assertion — plus a self-test that
deliberately breaks a step and fails if the gate still reports success.

**What this evidence does not say:** nothing about any appliance. It runs against disposable PostgreSQL.

---

## 2. T0043 live evidence (WS-L)

Appliance `radius` / `172.21.60.23` · machine-id `9b1e4e3578164bd094b11d96fc08ed8a` · site
`7acf26a7-5ad2-4c65-aef7-651107484636` · serial `APP-DEV-0001` · **development** appliance.

### Deployed artefacts

Built from `f468dc099715bb8a9111cad5a1fc47134e6942ac`; `git diff f468dc0..b94112d` over `data-plane/cmd`,
`data-plane/internal`, `hotel-admin/` and `control-plane/` is **empty**, so these are the accepted
candidate's artefacts.

| Binary | sha256 |
|---|---|
| edged | `bdff757af2eedfddfcfee1ef15ebf806a9ebbe0a197d6f3b9470a71b26b3336a` |
| scd | `1ed125d813b3aa72c93e5de571453a6d6a5d716481b70b266dbcd36c1916030a` |
| portald | `6af17b82cb5299eb91fa15837478c1358c552c09beea3059eb58154459d57923` |
| netd | `cb6b5b84dd34dea62ba9fa3db2c372ec45a6283eac490628fc966c689b423af0` |
| acctd | `60930b3f9592a0018e6d0618a9d10ba6d43b020707bf4270682dcd71a28df675` |
| pmsd | `4cd34fba541913678534f67134f031aa8a88669160bd39440624858350211a88` |

Hotel-Admin release `20260813-094434`, bundle `f1dc5fafa697b076f924b81a4e96d3e65cb15aefbf65011ed6b7afafb1c10200`.

### Migration integrity

Ledger before: `0001, 0002, 0005, 0007, 0008, 0009, 0010`. Applied: **0011 → 0026**, each recorded
**exactly once** (23 entries total). Production database: **not migrated**.

| | before | after |
|---|---|---|
| `iam_v2` base tables | 62 | **68** |
| views | 1 | 7 |
| functions | 60 | 111 |
| indexes | 171 | 195 |
| triggers | 61 | 84 |
| **rows** | **0** | **0** |
| owner | `iam_v2_owner` | `iam_v2_owner` |

`public` legacy schema: 44 tables, column fingerprint
`a8fec747c4a177fd603ac8cdb4350c0407c3f5e0855b1ad471de102458a63506` — **identical** before and after. No data
migration, no dual read, no dual write, no fabricated financial row, and legacy IAM remains the sole
authentication authority.

### Least privilege, measured on the appliance

Five roles created, all **NOLOGIN**, none superuser, none with CREATEROLE / CREATEDB / BYPASSRLS. **No
Phase-4 login role and no Phase-4 DSN exists.** The four service logins (`svc_edged`, `svc_scd`, `svc_acctd`,
`svc_netd`) hold **zero** `iam_v2` INSERT / UPDATE / DELETE / TRUNCATE.

- PUBLIC holds EXECUTE on **0** Phase-4 SECURITY DEFINER functions.
- **0** definer functions lack a pinned `search_path`.
- The execution role **cannot** assert a provider outcome; the outcome role **can**; the outcome role cannot begin an execution or grant.
- **0** roles can reach `p4_entitlement_grant_kernel`.
- No runtime role can INSERT or UPDATE `entitlements`, `settlements`, `posting_review_actions` or `compliance_archives`.
- **No deployed runtime, service or PUBLIC role is authorized to call `p4_record_compliance_receipt`** — 0 of the ten roles measured hold EXECUTE, and it is unreachable from the current runtime. The controlled function exists deliberately for a future real external archival authority.

### C35 live

`p4_assert_compliance_archived` refused with `COMPLIANCE_ARCHIVE_MISSING`; an INSERT attempting to be born
`receipt_verified` was refused by `ca_receipt_evidence_matches_flag` **issued as the database superuser**;
**0** archives, **0** verified receipts, **0** rows naming an authority. Local archive and purge gate
implemented; **external archival receipt authority does not exist; external receipt verification is not
implemented and is not simulated or claimed**; cross-customer purge unavailable.

### C37 DARK / zero egress

**0** files under `/etc/stayconnect` and **0** StayConnect systemd units mention any Phase-4 flag — DARK by
**absence**, not by a variable set to `false`. Every Phase-4 route on the running `edged` returned **404**
(`/financial-ops/health`, `/financial-ops/recovery`, `/financial-ops/recovery/zero-attempt`,
`/financial-review/queue`, `/financial-ops/settlements`) while `/edge/v1/health` returned **200**, so the
surface genuinely does not exist rather than the probe being wrong. Only non-loopback outbound: `scd` to the
Central control plane (`150.0.0.252:443`, `:4223`, `:9443`) and the operator's SSH session. No financial
worker process, no PMS socket, no provider socket; `pmsd` inactive with all Phase-3 flags OFF.

**No PS transmitted, no PA financially accepted, no folio debited, no provider CHARGE or REFUND, no paid
guest access, no executable reversal, no blind UNKNOWN retry.** Every financial table holds 0 rows.

### Service and runtime health

All ten services active and self-reporting `healthy` before deployment, after deployment, after the reboot
and after the rollback rehearsal: `scd`, `edged`, `netd`, `portald`, `acctd`, `hotel-admin`, `caddy`,
`kea-dhcp4-server`, `unbound`, `postgres`. Legacy IAM 1 operator / 37 guests / 23 sessions; captive portal
200; Hotel Admin 200; unbound answering; Kea active; license `Active`; assignment, identity and the pinned
registry anchor unchanged. Topology unchanged: `ens160 172.21.60.23/24`, `ens192`, `ens192.90`,
`br-lan 10.10.0.1/24`, `br-g90 10.20.0.1/22`, same six nft tables.

### Reboot persistence

Authorized controlled reboot; the appliance returned in ~30 s. Persisted: all 23 ledger entries including
the 16 Phase-4 ones, the `iam_v2` fingerprint, 0 rows, the five roles, 34 table grants and 191 function
grants, the deployed binaries and the Hotel-Admin release symlink, site and appliance identity, the pinned
trust anchor, every Phase-4 flag absent, every Phase-4 route 404, all services active, `pmsd` inactive, 0
open financial epochs. **No financial worker or egress became active.**

*A first topology fingerprint appeared to change across the reboot; diagnosed as Docker veth churn plus nft
counters, not topology. Scoped to StayConnect-managed surfaces it is identical.*

### Backup, restore and recovery

Backup taken **before any mutation**: `/var/backups/stayconnect/site-20260813-093625.dump`,
`sha256:bdcec4eb011dbf434230e5c8b3c523a9e17423f85ea8beeac30fd23b305684bf`, with
`site-20260813-093625-etc.tgz` whose exclusion of the financial restore marker was **verified, not assumed**.
It was **really restored** into a scratch database on the appliance — 62 `iam_v2` tables, 44 `public` tables,
37 legacy guests — and the scratch database dropped; `stayconnect_site` was never touched by the
verification.

`stayconnect-financial-restore.sh` has **no `--pubkey` option** and verifies against the appliance's pinned
registry root anchor (32 raw bytes, Ed25519, `84655767…`). An unsigned manifest and a manifest carrying a
forged signature were both **refused** in `--dry-run`, and neither attempt created an epoch, a hold or the
marker.

**The financial restore management marker is NOT installed.** The MISSING-marker reconciliation path was
exercised once against a synthetic tenant: it returned `INITIALIZED`, opened and immediately released an
epoch, and manufactured **no** holds, **no** outbox work and **no** payments. The single probe row was
deleted and `iam_v2` returned to 0 rows.

### Rollback rehearsal

Binaries and the Hotel-Admin release rolled back to the pre-WS-L build: all services returned active, legacy
IAM unchanged, topology identical, license `Active`, portal 200, Hotel Admin 200, Phase-4 routes still 404,
no financial egress. *The previous build also runs correctly against the new schema*, which is what makes the
additive migration safe to leave in place during a binary rollback.

Schema rolled `0026 → 0011` and re-applied `0011 → 0026`: after the DOWN, 62 base tables, `public` unchanged,
legacy rows intact, ledger back to `0010`; after the re-apply, 68 tables, 16 versions each recorded exactly
once, 5 roles, 34 grants, 0 rows, uniform `iam_v2_owner` ownership. The appliance was **returned to the
intended candidate and re-verified in full**.

---

## 3. Product-Owner decision

**D19 — Phase 4 is ACCEPTED and CLOSED at VERIFIED LIVE-DARK / NO-FINANCIAL-TRAFFIC maturity.**

Recorded in `governance/decision-register.json` and `governance/transitions/T0044.json`
(`transition_accepted: true`). Acceptance is at LIVE-DARK maturity only and authorizes no enablement, no
cutover, no Production change, no real financial traffic and **no merge**.

---

## 4. Accepted limitations — preserved, not promoted

| # | Limitation | Status |
|---|---|---|
| 1 | **C35** external archival receipt authority does not exist; the archive and gate are accepted **because they fail closed**; cross-customer purge unavailable | NOT PASS |
| 2 | **C38** real-financial acceptance outside Phase 4; separate future authorization required | NOT PASS |
| 3 | **Financial restore management marker not installed** on the development appliance | **PRE-FINANCIAL-ENABLE PREREQUISITE** |
| 4 | **Legacy live-session continuity NOT PROVEN** (from Phase 3); must not be inferred from WS-L row counts | NOT PASS |
| 5 | **No real payment-provider adapter or real-provider behaviour** accepted by this DARK closure | NOT PASS |
| 6 | **Per-property Tier-2 financial onboarding mandatory** before any property can be financially enabled | NOT PASS |

---

## 5. Three findings the live environment exposed, fixed forward

1. **The documented site backup could not run at all** — host `pg_dump` 14.23 against a 16.3 server aborts on
   version mismatch; the default `PGUSER` was the *database* name; a least-privilege service role cannot dump
   the whole database. The supported script now uses the server's own client when containerised, compares
   major versions before writing anything, derives the role from the administrative DSN, and refuses an empty
   dump.
2. **The DOWN migrations cannot be run by the schema owner** — each deletes its own ledger row and
   `iam_v2_owner` holds only SELECT and INSERT on `public.schema_migrations`, deliberately. The rollback
   procedure runs them with the administrative credential and normalises ownership afterwards.
3. **The catalog fingerprint cannot prove a rollback returned the same schema** — dropped columns keep their
   attribute slots, so `ordinal_position` shifts while the structure is identical (proved: 838 columns, 411
   constraints, 195 indexes, 84 triggers, 111 functions, ordinal-stripped md5
   `b4830633d99f12d71caa0f33c590007f`, **0** structural differences).
   `iam_v2_scratch/schema_structure_fingerprint.sql` and a whole-chain DOWN→UP gate assertion now cover it.
