# StayConnect Enterprise — START HERE (ChatGPT Project entry point)

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0007 -->
**Current phase:** 1B — Credential/identity/auth-context (DARK)
**Current activity:** `PHASE_1B_PLAN_CORRECTION_PENDING_APPROVAL`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B PLANNING (NOT implemented) · 2 NOT_STARTED · 3 NOT_STARTED · 4 NOT_STARTED · 5 NOT_STARTED · 6 NOT_STARTED · 7 NOT_STARTED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**iam_v2:** 49 tables, 0 rows, dark; no service routed; no data migration; legacy public schema is the sole production authority.
**Single next authorized action:** Product-Owner approval or rejection of the corrected Phase 1B plan
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D9`.
<!-- END GENERATED PROJECT STATE -->


**Read this file first.** It is the orientation for an AI consultant continuing work on StayConnect Enterprise. It summarizes the current, authoritative state; the individual documents in this pack are the detailed sources. Where this summary and a copied source document disagree, follow the **source-of-truth precedence** in §12.

**Source documentation baseline commit:** `a28f6f6` (binding Phase-1B decisions D1-D9; Phase 1A accepted/closed; contract §18 clarification; supersedes `afade95`/`d4fa9be`/`22a2e15`).
**Project-pack export commit:** recorded exactly as `PROJECT_PACK_EXPORT_COMMIT` in `MANIFEST.md`.
**Export date:** 2026-07-16.

**Permanent project rule:** every milestone must satisfy the **Zero-Stale-Leftovers** rule (repo `docs/ZERO_STALE_LEFTOVERS_RULE.md`) — no stale/contradictory/superseded artifact may survive a completed task, enforced by `tools/validate-project-state.sh`. See §14.

---

## 1. What StayConnect Enterprise is

A Linux-based inline **captive-portal Wi-Fi gateway appliance for hotels**, plus a cloud **Central Control Plane** — an enterprise alternative to IACBOX. Guests get internet access via the hotel network; the appliance authenticates them (PMS room lookup, vouchers, username/password guest accounts, OTP/social), enforces plans (speed/time/data/devices), meters usage, and can post Wi-Fi charges to the guest folio in the hotel's PMS over the **Protel/Opera FIAS** protocol.

## 2. Current architecture (two tiers)

- **Appliance (on-site):** Go daemons — `scd` (session/auth control), `edged` (admin API), `portald` (guest captive portal), `acctd` (accounting) — plus a `hotel-admin` Next.js UI. Local Postgres (site DB `stayconnect_site`; an isolated second-site **test** DB `stayconnect_site_b` exists for isolation tests — **not** a replication standby). Enforces guest access, shaping, accounting, and PMS integration at the edge; operates offline.
- **Central Control Plane (cloud, `150.0.0.252`):** `ctrlapi` Go API + `cloud-admin` Next.js. Fleet/customer/site/license management, telemetry, backup health. Outbound-only from appliances; internal-CA mTLS.
- **Ownership hierarchy (frozen):** Platform → Customer → Site (one physical property) → Appliance → guest VLANs/networks.
- **Appliance NIC topology (approved, permanent): exactly two physical NICs — WAN and LAN.** **WAN is also the management interface** (Hotel Admin/SSH/outbound sync); **LAN** carries guest connectivity and guest VLAN/trunk. There is **no** separate management NIC and **no** approved third HA-sync NIC. (Older docs describing a separate `mgmt` IP or an optional `hasync` NIC are superseded.)
- **PMS integration:** FIAS connector is **lookup-only today**; the financial **posting engine is a future component** (see phase status). Existing FIAS parse/framing lives in `data-plane/internal/pms/`.

## 3. Current project phase & status

**Current operational state is the GENERATED PROJECT STATE block at the top of this file** (rendered from `governance/project-state.json` — the single machine-readable source). Do not maintain a second current-state description here. Non-dynamic context: the isolated `iam_v2` schema is an additive, reversible, dark schema; the currently deployed voucher/guest-account system is a separate prior delivery, live and untouched.

## 4. Completed & live-verified milestones

- **Protel FIAS Gate 3A — PASS (2026-07-16):** one supervised, controlled **USD 1.00** folio debit against **Coral Sea Holiday Village / Hotel ID 3** was executed and **verified end-to-end** by Front Office: protocol accepted (`PA ASOK`, matched by PMS Interface + `P#`), correct **guest folio**, correct **`SO=WIFI` revenue mapping**, then **manually corrected** back to the **exact original balance**. (Guest identifiers redacted in this pack.)
- Verified FIAS behavior: `LS→LD→LR→LA` startup sequence; live `GI/GC/GO` feed + read-only `DR` resync; mandatory `RN`+`G#` folio targeting; production-grounded `PS`/`PA` field order and `AS` statuses; **single active-client slot** per interface; `P#` is a **protocol-attempt reference, not business idempotency**.
- Phase-0 IAM architecture contract fully specified and FINAL: domain model, canonical DDL, invariants, state machines, RBAC, financial safety, offline/restore.
- **Phase 1A `iam_v2` — scratch-verified (99/99), offline-real-schema-verified, and PRODUCTION LIVE-DARK created + verified (18/18, 2026-07-16):** 49 tables (catalog fingerprint `bd75026f`, identical across scratch/offline/production), dark in `stayconnect_site`, public schema unchanged, services active. Not cut over.

## 5. Permanent architecture decisions (do not relitigate)

- **No guest-facing PMS selector** — automatic STRICT multi-PMS resolution; unmapped guest networks **fail closed**.
- **Room number is evidence, never identity or financial ownership;** every stay/folio/event/purchase/posting is pinned to exactly one PMS-interface namespace; sharers (two stays, one room) are legal.
- **Mandatory Seamless Checkout Grace;** one live entitlement per subject; atomic same-subject supersession.
- **Tenant-wide Guest Principals** keyed by verified factors; **MAC identifies a device, never a person.**
- **Immutable revisions** for plans/packages/mappings/interface configs/PMS secrets; purchases/postings pin exact revisions.
- **Financial safety:** purchase → settlement → posting/payment separation; **`UNKNOWN` postings never auto-retry;** ISO-4217 minor-unit money; five-action audited manual-review governance.
- **Idempotent accounting** via per-session watermarks + append-only ledger + monotonic counters.

## 6. Known limitations (current)

- **Hotel ID 2 (Coral Sea Aqua Club, `120.0.0.15:5001`)** is **read-only FIAS capable but financially UNAPPROVED** — it must pass its own per-property financial-onboarding checklist before posting is enabled there.
- **`programmatic_reversal = false` for v1** — financial corrections are an **audited manual Front Office** operation; no `PT=C`/negative-`TA`/automatic reversal exists.
- **Physical traffic accounting** (real-device usage → non-zero accounting) still requires **live implementation acceptance**; it cannot be proven at Phase 0.
- The single Hotel ID 3 debit does **not** generalize to other properties/interfaces, sharers, multi-folio, no-post, or error-status cases.

## 7. Deferred capabilities

- **Programmatic reversal** — only after a separate, explicitly approved **capability spike**.
- **`AGGREGATE_ONLINE_TIME`** accounting mode — enum reserved, capability-disabled and inert in v1.
- **Gate 3C (UNKNOWN / Manual-Review posting safety)** — **post-implementation** acceptance, testable only after the Posting Engine exists.
- **Gate 3D (Checkout & Checkout-Grace)** — **post-implementation** acceptance, testable only after the PMS/Entitlement components exist.

## 8. Current approved plan (Phase 1A)

Build the **entire clean-slate IAM schema into an isolated `iam_v2` PostgreSQL schema inside the existing site database**, plus the core entitlement engine (validity-window, supersession, counters, watermarks), device registry, and lock strategy — **dark** (no service reads/writes it; no `search_path` cutover). Rollback before cutover = leave `iam_v2` dark / drop the schema; **no whole-database swap**. See `StayConnect-IAM-Phase1A-Plan.md` for migration groups MG-0…MG-9, per-object specs, the row-lock-first strategy, the replace/retain/migrate/remove matrix, disposable-data handling, and acceptance tests. **Cutover to `iam_v2` is a separate, later, explicitly gated event and an ATOMIC complete-domain switch of all IAM services together** (never per-flow or per-service; plan §7a); a single credential vertical slice does **not** authorize cutover, and build completion does **not** auto-promote.

**Rollback boundaries (cutover):** a routing flip-back is safe **only before the first production write** to `iam_v2` (Boundary A). **After** the first production write (Boundary B) a direct flip-back is forbidden without a tested reverse-migration/replay; otherwise **forward-fix only**, and all durable writes must be reconciled before any return. The first production write is the explicit no-return boundary.

**Resolved / open items:**
- **`folio_identity_strategy` fail-closed — APPROVED & in force (2026-07-16).** FINAL contract §4.1 amended to `NOT NULL DEFAULT 'UNSET'` (4-value CHECK). `UNSET` permits read-only ingestion/lookup/auth but **blocks every financial CHARGE** (before outbox/`P#`/transmission) until property onboarding records a concrete strategy in a **new** revision. `UNSET` is the only unset sentinel (`UNKNOWN` = a Posting state). No open folio item remains.
- **HA synchronization transport under the two-NIC rule — OPEN.** Single-appliance local-first/offline is current and supported; **HA failover under the two-NIC architecture is NOT designed/implemented/accepted**; the old third-NIC `hasync` design is superseded.
- **Live `iam_v2` creation** is a separately authorized action **after** A-series acceptance in a scratch/test DB; **cutover** is a still-later separate approval.

## 9. Next authorized action

The single **next authorized action** is Product-Owner **approval or rejection of the complete Phase 1B plan** (`StayConnect-IAM-Phase1B-Plan.md`, planning-only). The Phase 1A LIVE-DARK acceptance record is unchanged (review of the Phase 1A LIVE-DARK acceptance remains available). Phase 1B is **not implemented**; approving the plan does not authorize implementation (the mandatory least-privilege / superuser-elimination prerequisite is inside the plan, §2/§14).

**Product-Owner review of the Phase 1A LIVE-DARK acceptance** (`StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md`, 18/18; authoritative production evidence is `PROD_LIVE_DARK_EVIDENCE_V2.txt`, captured read-only — the earlier `PROD_LIVE_DARK_EVIDENCE.txt` is **superseded/erroneous**) — **before any Phase 1B authorization**. The dark `iam_v2` schema is created + verified in production but **NOT cut over**; no service reads/writes it, no DSN/`search_path` change. Phase 1B (credential/portal, dark/flagged), cutover, IAM data migration, and legacy cleanup each need their **own** separate PO approval (plan §7a/§11 ladder). Nothing downstream is authorized yet.

**Mandatory Phase-1B prerequisite (superuser deviation).** Production services currently connect to `stayconnect_site` as the PostgreSQL superuser `stayconnect` (`rolsuper=true`). The least-privilege `iam_v2` service roles therefore do **not** yet bind them; the schema's darkness rests on *zero code references + no `search_path` change*, not on grant isolation. No service may be routed to `iam_v2` until a separately reviewed least-privilege service-role + credential-rotation plan is approved and applied. This blocks Phase-1B runtime integration; it is **not** a defect in the dark schema.

## 10. Forbidden until explicitly approved

Schema migrations; feature code; production connector/posting-engine development; portal/admin-UI cutover; PMS production configuration; `pms_providers` creation; **any further live PMS/FIAS financial test** without separate authorization; guest-networking changes; deployment; network scanning; enabling Hotel ID 2 posting; building any reversal sender.

## 11. Documents in this pack

| File | Role |
|---|---|
| `StayConnect-IAM-Phase0-Contract.md` | **Authoritative** — FINAL Phase-0 architecture contract (DDL, invariants, state machines, FIAS findings §9). |
| `StayConnect-IAM-Handoff.md` | **Authoritative** — current synchronized operational handoff. |
| `StayConnect-IAM-Phase1A-Plan.md` | **Authoritative** — current approved phase plan (implemented through production live-dark; cutover/1B still gated). |
| `StayConnect-IAM-Phase1B-Plan.md` | **Authoritative (planning-only)** — complete Phase 1B credential/portal implementation plan; awaiting PO approval/rejection; not implemented. |
| `Protel-FIAS-Phase0-Spike.md` | **Authoritative** — live FIAS spike + Gate 3A PASS evidence (guest identifiers redacted). |
| `SYSTEM_OVERVIEW.md` | Supporting — canonical current-system reference. |
| `TARGET_ARCHITECTURE.md` | Supporting — target architecture. |
| `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | Supporting — operations manual. |
| `DEPLOYMENT_APPLIANCE.md` | Supporting — appliance deployment. |
| `OFFLINE_OPERATION.md` | Supporting — offline behavior. |
| `MIGRATION_RUNBOOK.md` | Supporting — migration/rollback runbook. |
| `PROJECT-INSTRUCTIONS.md` | Paste into the ChatGPT Project's custom instructions. |
| `ZERO_STALE_LEFTOVERS_RULE.md` | **Permanent rule** — the authoritative Zero-Stale-Leftovers rule (full text, bundled). |
| `MANIFEST.md` | Provenance + SHA-256 for every exported file. |

## 12. Source-of-truth precedence

1. Latest Product-Owner-approved **FINAL architecture contract** (`StayConnect-IAM-Phase0-Contract.md`).
2. Current synchronized **Context Handoff** (`StayConnect-IAM-Handoff.md`).
3. Current approved **phase plan** (`StayConnect-IAM-Phase1A-Plan.md`).
4. **Verified live spike / acceptance evidence** (`Protel-FIAS-Phase0-Spike.md`).
5. Current **system & operations documentation** (SYSTEM_OVERVIEW, TARGET_ARCHITECTURE, operations manual, deployment, offline, migration).
6. Historical project chats.
7. Superseded drafts / old Agent reports.

**Historical chats never override a newer approved contract or verified execution result.**

## 13. How a new AI chat should continue safely

- Treat this as **production hospitality software handling real guest folios and money.** Correctness and safety outrank speed.
- **Do not invent PMS/FIAS protocol behavior, financial semantics, credentials, or test data.** If a fact is not in these documents or verified evidence, say so and ask.
- **Recommend and review; do not authorize implementation.** No migrations, code, deployment, or live PMS traffic proceed without explicit Product-Owner approval.
- When reviewing an engineering Agent's report, check it against the precedence order above and the permanent decisions/limitations; flag anything that contradicts a FINAL decision, generalizes the single Hotel ID 3 result, or would build a deferred/forbidden capability.
- After any approved milestone, **all related documents must be re-synchronized** to one consistent status and next step.

## 14. Permanent Zero-Stale-Leftovers rule

A permanent, project-wide Product-Owner rule governs every future milestone: **no completed task may leave behind any stale, superseded, contradictory, misleading, or partially-updated artifact** — in docs, handoffs, plans, acceptance records, runbooks, comments, config, migrations, exports, manifests, checksums, or scripts. A newer statement elsewhere does **not** excuse a stale one; a lower section does not correct an earlier one in the same file; a banner does not excuse contradictory current-state content. Old content may remain only if it is required as audit/history, explicitly labeled `HISTORICAL`/`SUPERSEDED`/`CLOSED`/`DEPRECATED`, cannot be mistaken for current behavior, and names its current replacement.

Before any milestone is declared complete: run a repo-wide stale scan, build a current-state assertion set and prove zero contradictions, regenerate + verify both export packs from the synchronized commit, and run `tools/validate-project-state.sh` (must print `ZERO_STALE_LEFTOVERS = PASS`). The authoritative rule text is bundled in this pack — see [ZERO_STALE_LEFTOVERS_RULE.md](ZERO_STALE_LEFTOVERS_RULE.md) (repository path `docs/ZERO_STALE_LEFTOVERS_RULE.md`); the enforcing validator `tools/validate-project-state.sh` is bundled in the Evidence Pack. Every future milestone report must include a `ZERO-STALE-LEFTOVERS VERIFICATION` section and confirm `ZERO_STALE_LEFTOVERS = PASS`.
