# StayConnect IAM — Phase 1B Live-Dark Acceptance Record

**Status:** PENDING PRODUCT-OWNER ACCEPTANCE (transition **T0010**). Phase 1B is NOT accepted/closed; no
cutover, no Phase 2, no iam_v2 production access. Legacy public-schema IAM remains the sole production
authority.

**Appliance:** `radius` / `172.21.60.23`, site DB `stayconnect_site` (docker `stayconnect-pg`,
timescaledb 2.16.1-pg16). **Executed:** 2026-07-17 → 2026-07-18. **Branch/PR:** `phase/1b-dark-auth` / PR #2.

## 1. Scope delivered

Phase 1B builds the runtime-security foundation and the DARK IAM-v2 application layer, and cuts the
appliance's four site-DB daemons over to least-privilege database roles — all with **every new auth
feature OFF** (dark). No guest is authenticated through iam_v2; no iam_v2 row is written.

| Area | Delivered |
|---|---|
| Durable throttle (D4) | `public.auth_throttle_buckets` (migration 0007); `internal/throttle` — atomic upsert, cross-window hard block, deterministic sort+dedup (deadlock-free), fail-closed |
| Keyed-HMAC OTP (D7) | `otp_hmac_key_generations` + `auth_otps.otp_key_generation` (migration 0008); `internal/otpkey` ring; `otp.Issue/Verify` ring-aware with legacy salt:sha256 compat |
| Local key lifecycle | `internal/localkeys` — bootstrap (`CreateKeyIfAbsent`) vs runtime (`LoadExistingKey`, load-only, fail-closed) split; `internal/keybootstrap` + `cmd/keybootstrap` controlled OTP gen-1 bootstrap |
| Dark IAM-v2 | `internal/iamv2` — authenticator that never touches its repository while flags OFF; voucher/account/OTP/social adapters; auth-context pin validation before SQL |
| scd wiring | throttle guard on every auth handler, ring-aware OTP, dark iamv2 construction — all gated OFF by default (byte-for-byte legacy) |
| Gate P | least-privilege `svc_scd/svc_edged/svc_acctd/svc_netd` roles replacing superuser `stayconnect`; SCRAM passwords; reconciler grants |

## 2. Implementation commits (on `4ecbe57`)

```
52042b1  voucher DB integration test, key bootstrap/runtime split, Gate-P grants for new tables
c6dd7f7  controlled OTP generation-1 bootstrap (deployment-time, fail-closed)
337e9c6  Gate-P dry run applies 0007/0008, bootstraps OTP, tests new-table grants
ee35bbb  throttle core: deterministic rule ordering + dedup, deadlock-safe
7618dc4  wire durable throttle, keyed-HMAC OTP, dark IAM-v2, auth-context pins into scd
ea3354b  grant svc_scd the cross-tenant reconciliation + mirror-seed privileges it actually uses
b87a59b  Gate-P docs + dry run: reflect live-derived svc_scd cross-tenant grants
```

## 3. Software gate (all green)

- `go build ./...`, `go vet ./...` clean; `go test ./...` = **16/16 packages pass, 0 fail** (DB-backed
  tests run against disposable databases: iamv2, throttle, otp keyed, keybootstrap, localkeys).
- Migrations 0007+0008: apply / apply-again (idempotent) / rollback / reapply — all rc=0 on a disposable DB.
- Gate-P dry run on a genuinely disposable timescaledb cluster: **GATEP_DRYRUN = PASS**, 68 role-table
  grant rows, idempotent reconciliation, **zero effective iam_v2 privileges**, self-test still fails on
  invalid SQL; cluster destroyed.
- Dark proofs: `TestDarkIAMv2WiringIssuesZeroSQL` (repository panics if entered → never entered),
  social Stub refusal, secret redaction.
- Disposable test infra (containers, DBs, tunnel) torn down; Governance CI **success** on 7618dc4.

## 4. Live deployment (T0010) — evidence

| Step | Result |
|---|---|
| Pre-change backup | `/root/phase1b-deploy/pre_phase1b_20260718.dump` sha256 `2671b008216e377aaea32dc02fb35806772cc4371f725480a109db11fddb4d0b`, size 885342 B; public column-DDL SHA-256 `8659c08d…` (pre-migration; see §6); iam_v2 **49/0** |
| Migrations 0007+0008 applied live | both tables present, `otp_key_generation` column added, both registered in `schema_migrations`; iam_v2 unchanged 49/0 |
| keybootstrap | `throttle.key` + `otp_hmac_1.key` (0600, dir 0700); OTP generation 1 active; key↔DB metadata agree |
| Gate-P roles | `svc_scd/edged/acctd/netd` created NOSUPERUSER/LOGIN; SCRAM passwords (log-safety verified); grants applied |
| DSN rotation (one service at a time) | acctd→svc_acctd, netd→svc_netd, edged→svc_edged, scd→svc_scd — each restarted and verified with rollback readiness |
| Grant gap found & fixed live | scd's every-boot cross-tenant reconciliation needed SELECT/DELETE across tenant-owned tables + UPDATE on `sites`; initially held the appliance fail-closed under svc_scd → grants corrected (ea3354b), re-applied, scd healthy |
| Pinned scd build | linux/amd64 static, sha256 `dc2033a9…`; flags OFF; `iamv2 dark authenticator constructed` (master=false, all methods false); health 200 |
| Final reboot + re-validate | all 5 services active; all four daemons connected as their `svc_*` roles; scd healthy + dark + not fail-closed; key files intact; OTP gen 1 active; throttle 0 rows; **iam_v2 49/0**; installed scd sha matches |

## 5. Dark invariants (post-reboot)

- Every new auth feature OFF: `SCD_DURABLE_THROTTLE`, `SCD_OTP_HMAC`, `STAYCONNECT_IAMV2_*` all unset →
  throttle ring/OTP ring not loaded, iamv2 constructed dark (zero SQL). `auth_throttle_buckets` = 0 rows.
- Legacy public-schema IAM remains the sole production authority; no service routed to iam_v2.
- iam_v2 schema unchanged at **49 tables / 0 rows**; the only production data change is the additive
  0007/0008 schema objects (empty) + the single active OTP-generation metadata row (no secret material).

## 6. Public-schema structural fingerprints (naming)

Two distinct fingerprints appear across the records; they measure the same object (the public schema
structure) with **different algorithms** and are **not comparable to each other**:

| Name | Value | Algorithm | Meaning |
|---|---|---|---|
| Phase-1A structural MD5 | `d86ca4c6aed4bb868c173a0d328e0dde` | MD5 of sorted public `{columns + constraints + indexes}` excluding the Phase-1A MG-0 anchor index `guest_networks_tsi_anchor` (`iam_v2_scratch/prod_live_dark.sh`) | Pre-Phase-1B baseline |
| Gate-P/Phase-1B column SHA-256 (pre) | `8659c08db41624d5cab946027c0d6c2d37251fc1550ec35929241827acf852f9` | SHA-256 of `information_schema.columns` `table.column:data_type` | Public columns **before** 0007/0008 |
| Gate-P/Phase-1B column SHA-256 (current) | `7241b1cd76201cd48df0eaa0055c336d0951aa218830545d7dbbe92bd61a0fcc` | same as above | Public columns **after** 0007/0008 |

The Phase-1A MD5 baseline (`d86ca4c6`) was intentionally **superseded** by the AUTHORIZED additive
migrations 0007/0008. The pre→post SHA-256 delta (`8659c08d` → `7241b1cd`) is exactly those additive
objects — authorized schema evolution, **not drift**. iam_v2's own fingerprint (`bd75026f`, 49 tables)
and row count (0) are unchanged.

## 7. Live Gate-P incident record (honest)

During the FIRST `svc_scd` DSN rotation, scd's **every-boot** cross-tenant reconciliation
(`hasForeignTenantData()` in `tenant_transition.go`) hit `permission denied` on `sites` (UPDATE, from
the tenant/site mirror upsert) and `accounting_records` (SELECT) — permissions the fixed-allowlist
Gate-P dry run had not exercised. Because the cross-tenant detection **errored**, scd correctly held the
appliance **fail-closed** (no guest authorization). Timeline (appliance UTC, Etc/UTC):

- ~2026-07-17T21:11–21:12Z: first `svc_scd` rotation → fail-closed detected in scd logs.
- Immediate action: scd DSN **rolled back** to the superuser DSN and restarted → guest authorization
  restored; confirmed no remaining `FAIL-CLOSED`/`permission denied` lines.
- Correction: `gatep-grants.sql` given scd the exact cross-tenant reconciliation grants (commit
  **`ea3354b`**); re-applied live; scd re-rotated onto `svc_scd` → healthy, not fail-closed.
- Regression coverage: dry-run harness + privilege docs updated to assert these grants (commit
  **`b87a59b`**).
- Post-reboot: **0** `FAIL-CLOSED`/`permission denied` lines this boot (see
  `evidence/phase1b/T0010-LIVE-EVIDENCE.txt` §10).

Session impact: the appliance is the authorized dev/test box; sessions are disposable. During the brief
fail-closed window **no** guest authorization was permitted (fail-closed is the intended safe state).
This is a controlled, intentional mutation of the dev/test appliance — it is **not** accurate to
describe the appliance as "production untouched"; it was deliberately reconfigured under authorization.

## 8. Residual security limitation — `svc_scd` breadth (known, accepted for now)

`svc_scd` legitimately holds broad SELECT/DELETE across the tenant-owned public tables (and UPDATE on
`sites`/`tenants`) because scd performs the **secure cross-tenant reassignment purge** during
boot/assignment reconciliation. Assessment:

- **Why required now:** `reconcileTenantOwnership` must detect (SELECT) and, on a confirmed cross-tenant
  transition, purge (DELETE) every foreign-tenant-owned row in one transaction, and reseed the
  tenant/site mirror (INSERT/UPDATE).
- **Still bounded:** zero `iam_v2` privilege (0 table grants, 0 schema USAGE — verified live); no
  superuser/CREATEDB/CREATEROLE/REPLICATION/BYPASSRLS; scoped to `public` tables scd's code touches.
  Materially narrower than the previous superuser DSN.
- **Blast radius:** a compromise of `svc_scd` could read/delete tenant-owned `public` rows (including
  `payments`/`stripe_events` metadata) — broader than a pure session role, though it cannot escalate,
  create objects, touch `iam_v2`, or bypass RLS.
- **Future hardening (recommendation only — NOT built or deployed in this pass):** move the cross-tenant
  purge into a separately-controlled one-shot maintenance role/helper invoked only during
  assignment reconciliation, or behind an audited `SECURITY DEFINER` boundary, so the always-on
  `svc_scd` runtime role can drop the broad DELETE grants.

## 9. Evidence artifacts

- Live re-verification: `docs/acceptance/evidence/phase1b/T0010-LIVE-EVIDENCE.txt` (sanitized).
- Software gate: `docs/acceptance/evidence/phase1b/T0010-SOFTWARE-GATE.txt`.

## 10. Not done (out of scope / not authorized)

No PR #2 merge; Phase 1B not marked accepted/closed; no Phase 2 / cutover; no iam_v2 production access;
no guest activation through iam_v2; no bulk migration; no PMS posting. Dark features remain OFF pending a
separate, explicitly-authorized enablement step.
