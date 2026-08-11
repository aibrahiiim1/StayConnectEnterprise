# Phase 4 — Pre-Implementation Readiness Matrix (A8)

**Produced:** 2026-08-11, against master `a4e951972d8087f00a40d8b39eb1b87ea03144b6` and the live appliance /
Central baseline. Every row below was **measured**, not recalled from an earlier report.

**Verdict: the baseline is READY. No safety prerequisite is BLOCKED.** One external item is a Product-Owner
business decision (per-property financial onboarding for Hotel ID 2) and is **not** a blocker for building
Phase 4 DARK, because the contract requires exactly that interface to stay fail-closed until it passes.

---

## 1. Baseline readiness

| # | Prerequisite | Authoritative requirement | Measured evidence | Verdict |
|---|---|---|---|---|
| R1 | Repository authoritative and clean | GH-SOURCE-OF-TRUTH | master `a4e95197`, 0 uncommitted, 0 open PRs, only `master` + the historical `phase/1b-dark-auth` branch | **PASS** |
| R2 | Governance validators green | GH-MANDATORY-CI | project-state PASS, parity 16/16, negative 26/26, artifact-staleness 10/10, zero-stale PASS, transition-times PASS | **PASS** |
| R3 | Deployment == source of truth | A3 | 4/4 artefacts md5-identical after correcting two comment-only drifts (Edge unit, Central Caddyfile) | **PASS** |
| R4 | Both hosts healthy | A4 | Edge 7 services running / 0 failed; Central 4 services + 4 containers / 0 failed | **PASS** |
| R5 | Browser surfaces reachable | A4 | `https://172.21.60.23/login` 200, `https://150.0.0.252/login` 200, static assets 200, backend 401 boundaries | **PASS** |
| R6 | Licence valid | A4 | `Active`, `c03b5aa5…` v2, valid to **2027-08-08T23:59:59Z**, `cloud_stale=false`, `clock_rollback=false` | **PASS** |
| R7 | Migration baseline known | A5 | ledger = 7 applied, latest `0010_phase3_stay_resolution`; repo has 10 `.up.sql` (0001–0010) | **PASS** |
| R8 | iam_v2 DARK | A5 | **63 tables / 0 rows**; `svc_*` grants on `iam_v2` = **0** | **PASS** |
| R9 | Financial tables empty | A5 | postings 0, attempts 0, outbox 0, review actions 0, payments 0, settlements 0, purchases 0, `financial_epoch` 0 | **PASS** |
| R10 | No hidden Phase-4 runtime privilege | A5/A6 | zero `svc_*` grants; `pms_interfaces` = 0, `pms_interface_revisions` = 0, `pnumber_seq` = 0 — nothing pre-seeded | **PASS** |
| R11 | Legacy IAM still authoritative | baseline | `public.sessions` is the production authority; `iam_v2.sessions` = 0 | **PASS** |
| R12 | Phase-3 flags OFF | baseline | 0 flag names in any env file/unit; 0 `STAYCONNECT_PHASE3_*` vars in every running process | **PASS** |
| R13 | Accepted binaries unchanged | D16 | all six appliance binaries hash-match the build from `7c8b8cf0…` | **PASS** |
| R14 | Host identities correct | T0027/T0028 | Central `sc-central` / `476149613e92…`; appliance `radius` / `9b1e4e35…`, serial `SC-BEN1-JS4A-0D9C`, appliance_id `ef78219b-…` unchanged | **PASS** |

## 2. Phase-4 specific prerequisites

| # | Prerequisite | Authoritative requirement | Measured evidence | Verdict |
|---|---|---|---|---|
| P1 | Financial schema exists | §4.5, §9a rule 2 | `pms_postings`, `posting_attempts`, `posting_attempt_events`, `posting_outbox`, `posting_review_actions`, `pms_interface_pnumber_seq`, `payment_transactions`, `settlements`, `purchases`, `financial_epoch` all present in `iam_v2` | **PASS** |
| P2 | FIAS wire layer exists | §9b finding 1 | `data-plane/internal/pms/fias_wire.go` (framing, `LS`/`LD`/`LR`/`LA`/`DR`), `protel_fias.go` | **PASS** |
| P3 | Commerce layer exists | Phase 2 | quotes, purchases, settlement mappings, entitlement grant path (`internal/iamv2/commerce_*.go`) | **PASS** |
| P4 | Stay/Folio domain exists | Phase 3 | `stays`, `folios`, `stay_folios`, resolution engine, checkout grace | **PASS** |
| P5 | DARK service host exists | T0026 | `stayconnect-pmsd` installed, enabled, least-privilege, flags-OFF exit 0, reboot-verified | **PASS** |
| P6 | Key hierarchy / secret generations | §11 | `pms_interface_secret_generations` present; appliance KEK → tenant DEK model implemented in Phase 1A/1B | **PASS** |
| P7 | RBAC + step-up available | §15 | operator roles + `RequireReauth` step-up proven working (used for the licence renewal this round) | **PASS** |
| P8 | Backup/rollback tooling | A7 | automated backup/rollback artifact retention; `scripts/binary-rollback.sh` with pre-`nftconverge` boundary; per-host config rollback copies | **PASS** |
| P9 | Gate 3A financial evidence | §9b | one live controlled USD 1.00 `PS` debit, Hotel ID 3, correct folio + revenue mapping + manual cleanup verified | **PASS (bounded to Hotel ID 3)** |
| P10 | **Posting execution runtime** | §9a, Tier-3 3C | **ABSENT** — all seven financial tables have **zero** Go references; `pmsd` is a 139-line DARK stub | **GAP — this is the Phase-4 build** |

## 3. External / business items (not baseline blockers)

| # | Item | Status | Why it does not block building Phase 4 DARK |
|---|---|---|---|
| B1 | Hotel ID 2 (Aqua Club) financial onboarding | **Not authorized** | §9c Tier 2 requires the interface to remain `folio_identity_strategy = UNSET` and financially fail-closed until its own onboarding passes. Phase 4 must *implement* that fail-closed behaviour, so the absence of onboarding is a test case, not a blocker. |
| B2 | `SO=WIFI` revenue mapping per property | Confirmed for Hotel ID 3 only (§9b) | Same reasoning — per-property gating is a Phase-4 feature. |
| B3 | Programmatic reversal | `capability = false` (§9a rule 5, Gate 3B) | Phase 4 must NOT implement it. Explicitly out of scope. |
| B4 | Real financial acceptance (Tier-3 3C live) | Requires explicit PO live-financial authorization | Prohibited in this round. Phase 4 can reach DARK / no-financial-traffic maturity without it. |
| B5 | Central web certificate `DNS:radius` SAN | Accepted non-operational item (D-decision, T0028) | Cosmetic PKI; untouched by design. |

## 4. Conclusion

Rows R1–R14 and P1–P9 are **PASS**. P10 is the Phase-4 implementation itself. B1–B5 are recorded and none
of them prevents building and DARK-deploying Phase 4.

**The baseline is clean, internally consistent and reproducible.** Phase-4 implementation is authorized to
begin against master `a4e951972d8087f00a40d8b39eb1b87ea03144b6`.
