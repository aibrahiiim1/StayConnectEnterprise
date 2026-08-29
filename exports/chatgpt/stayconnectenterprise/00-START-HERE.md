# StayConnect Enterprise — START HERE (ChatGPT Project entry point)

<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
<!-- source: governance/project-state.json (schema 1.0.0) @ transition T0100 -->
**Current phase:** 7 — Cleanup, final docs, full-system re-acceptance
**Current activity:** `FRESH_PRODUCTION_APPLIANCE_ONBOARDED_PRE_LIVE`
**Phase status:** 0 FINAL_CLOSED · 1A **ACCEPTED_AND_CLOSED** (DARK, NOT CUT OVER) · 1B ACCEPTED_AND_CLOSED (DARK — accepted & closed; no cutover; no production iam_v2 use) · 2 ACCEPTED_AND_CLOSED · 3 ACCEPTED_AND_CLOSED · 4 ACCEPTED_AND_CLOSED · 5 ACCEPTED_AND_CLOSED · 6 ACCEPTED_AND_CLOSED · 7 ACCEPTED_AND_CLOSED
**Phase 1A maturity:** ACCEPTED_AND_CLOSED — SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER
**Fresh Production baseline:** 74 iam_v2 tables, 37 public tables, ZERO identity rows. Factory-clean and current-only: the superseded guest-IAM runtime and schema are ABSENT, so IAM-v2 is structurally the sole guest-IAM authority from first operation. Not a cutover.
**Fresh Production appliance (172.21.60.25, sce):** DEPLOYED factory-clean from `c72b49e`; first-bring-up fixes were then applied live and afterwards committed (`1c55d7f`), so no single commit describes what is running. PRE-LIVE. Enrollment: enrolled; claim: claimed; signed assignment: signed and pinned; licence: licensed. ens192 is configured. Guest traffic: granted and enforced, not yet proven end to end. two real room logins were performed by the product owner on 2026-08-26 (rooms 11209 and 11112, one device): purchases=2, entitlements=2, sessions=2. session 8b5758bf is active and kernel-enforced - authorized in nft and metered by its own tc class in both directions; session 9f6b62dc was ended superseded_on_address when the newer session took over the device's address. both entitlements remain active. what is still unproven is a guest-visible room login that succeeds in one attempt after the enforcement fix (t0100), and real guest traffic: no bytes have been carried and accounting_records=0.; PMS: read-only pms feed, live. protel interface ddff5d07 is active, connected, in_sync and continuous on revision 38207f74; the automatic fresh full sync completed 2026-08-26 (t0098). no pms posting, payment, reversal or fx traffic has ever occurred.; payment/financial: none. Go-Live: not performed and not authorized. Hotel Admin: https://172.21.60.25/
**Development reference appliance (172.21.60.23):** UNTOUCHED by this work and NOT cut over. Retains the historical live-dark runtime its accepted evidence records, including its superseded guest-IAM schema (68 iam_v2 tables live). Reference and evidence only, never an installation source.
**Lifecycle:** PRE-LIVE (D24): no real hotel guest or staff depends on StayConnect for live service yet.
**Single next authorized action:** NONE is authorized beyond ONE controlled real-device Room Login test, which requires its own Product-Owner decision. The enforcement plane that refused the first two real Room Logins is fixed, merged and deployed, and verified live on 172.21.60.25 (T0100): the producer is active and authenticated, netd is applying the plan, nft and tc carry the expected state, and durable Session state agrees with the kernel. What remains unproven is a Room Login that succeeds from the GUEST's point of view, end to end, in one attempt. A separate Product-Owner Go-Live decision remains required and no current authorization supplies it.
**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `D39`.
<!-- END GENERATED PROJECT STATE -->


**Read this file first.** It is the orientation for an AI consultant continuing work on StayConnect Enterprise. It summarizes the current, authoritative state; the individual documents in this pack are the detailed sources. Where this summary and a copied source document disagree, follow the **source-of-truth precedence** in §12.

**Source documentation baseline commit:** `a28f6f6` (binding Phase-1B decisions D1-D9; Phase 1A accepted/closed; contract §18 clarification; supersedes `afade95`/`d4fa9be`/`22a2e15`).
**Project-pack export commit:** recorded exactly as `PROJECT_PACK_EXPORT_COMMIT` in `MANIFEST.md`.
**Export date:** 2026-07-16.

**Permanent project rules:** every milestone must satisfy (1) the **Zero-Stale-Leftovers** rule (repo `docs/ZERO_STALE_LEFTOVERS_RULE.md`) — no stale/contradictory/superseded artifact may survive a completed task — and (2) the **GitHub Execution, Reporting and Delivery** rule (repo `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md`) — the GitHub repo `aibrahiiim1/StayConnectEnterprise` is the only authoritative source (ZIP packs are exports only), one Phase per branch + one PR, and every report embeds the deterministic changed-file manifest. Both are enforced by `tools/project-state.py` + `tools/validate-project-state.sh`. See §14–§15.

---

## 1. What StayConnect Enterprise is

A Linux-based inline **captive-portal Wi-Fi gateway appliance for hotels**, plus a cloud **Central Control Plane** — an enterprise alternative to IACBOX. Guests get internet access via the hotel network; the appliance authenticates them (PMS room lookup, vouchers, username/password guest accounts, OTP/social), enforces plans (speed/time/data/devices), meters usage, and can post Wi-Fi charges to the guest folio in the hotel's PMS over the **Protel/Opera FIAS** protocol.

## 2. Current architecture (two tiers)

- **Appliance (on-site):** Go daemons — `scd` (session/auth control), `edged` (admin API), `portald` (guest captive portal), `acctd` (accounting) — plus a `hotel-admin` Next.js UI. Local Postgres (site DB `stayconnect_site`; an isolated second-site **test** DB `stayconnect_site_b` exists for isolation tests — **not** a replication standby). Enforces guest access, shaping, accounting, and PMS integration at the edge; operates offline.
- **Central Control Plane (cloud, `150.0.0.252`):** `ctrlapi` Go API + `cloud-admin` Next.js. Fleet/customer/site/license management, telemetry, backup health. Outbound-only from appliances; internal-CA mTLS.
- **Ownership hierarchy (frozen):** Platform → Customer → Site (one physical property) → Appliance → guest VLANs/networks.
- **Appliance NIC topology (approved, permanent): exactly two physical NICs — WAN and LAN.** **WAN is also the management interface** (Hotel Admin/SSH/outbound sync); **LAN** carries guest connectivity and guest VLAN/trunk. There is **no** separate management NIC and **no** approved third HA-sync NIC. (Older docs describing a separate `mgmt` IP or an optional `hasync` NIC are superseded.)
- **PMS integration:** the FIAS connector performs **lookup only in production**, and the financial **posting engine EXISTS and is ACCEPTED at LIVE-DARK maturity** (D19/T0044) — deployed on the development appliance with every Phase-4 flag OFF, every Phase-4 route returning 404 and zero financial egress. It is **disabled, not absent**: no PS has ever been transmitted, no folio debited and no provider charged. See the generated PROJECT STATE block above for the authoritative status. Existing FIAS parse/framing lives in `data-plane/internal/pms/`; the financial execution core lives in `data-plane/internal/{payment,posting}/` and migrations `0011`–`0026`.

## 3. Current project phase & status

**Current operational state is the GENERATED PROJECT STATE block at the top of this file** (rendered from `governance/project-state.json` — the single machine-readable source). Do not maintain a second current-state description here. Non-dynamic context: the isolated `iam_v2` schema is an additive, reversible, dark schema; the currently deployed voucher/guest-account system is a separate prior delivery, DEPLOYED AND UNTOUCHED. PRE-LIVE (D24/T0056): it is not serving real hotel guests today -- it is the configured baseline in a system under active development and controlled testing.

## 4. Completed & live-verified milestones

- **Protel FIAS Gate 3A — PASS (2026-07-16):** one supervised, controlled **USD 1.00** folio debit against **Coral Sea Holiday Village / Hotel ID 3** was executed and **verified end-to-end** by Front Office: protocol accepted (`PA ASOK`, matched by PMS Interface + `P#`), correct **guest folio**, correct **`SO=WIFI` revenue mapping**, then **manually corrected** back to the **exact original balance**. (Guest identifiers redacted in this pack.)
- Verified FIAS behavior: `LS→LD→LR→LA` startup sequence; live `GI/GC/GO` feed + read-only `DR` resync; mandatory `RN`+`G#` folio targeting; production-grounded `PS`/`PA` field order and `AS` statuses; **single active-client slot** per interface; `P#` is a **protocol-attempt reference, not business idempotency**.
- Phase-0 IAM architecture contract fully specified and FINAL: domain model, canonical DDL, invariants, state machines, RBAC, financial safety, offline/restore.
- **Phase 1A `iam_v2` — scratch-verified (99/99), offline-real-schema-verified, and PRODUCTION LIVE-DARK created + verified (18/18, 2026-07-16):** *(figures AS AT 2026-07-16; the live schema has since grown through migrations 0010 and 0011-0026 — see the generated block)* 49 tables (catalog fingerprint `bd75026f`, identical across scratch/offline/production), dark in `stayconnect_site`, public schema unchanged, services active. Not cut over.

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

## 8. HISTORICAL — the Phase-1A approved plan, AS AT 2026-07-16

> **THIS SECTION IS HISTORICAL AND IS NOT THE CURRENT PLAN.** It is kept because the Phase-1A build
> shape, its rollback boundaries and its resolved/open items are still the reference for how `iam_v2`
> was constructed. Phase 1A was accepted and closed long ago, and Phases 1B, 2, 3 and 4 have since
> been accepted and closed too. **The current phase, the current approved plan and the current next
> action are the GENERATED PROJECT STATE block at the top of this file — the only surface in this
> document that carries them.**

*(As at 2026-07-16, the approved plan was to)* build the **entire clean-slate IAM schema into an isolated `iam_v2` PostgreSQL schema inside the existing site database**, plus the core entitlement engine (validity-window, supersession, counters, watermarks), device registry, and lock strategy — **dark** (no service reads/writes it; no `search_path` cutover). Rollback before cutover = leave `iam_v2` dark / drop the schema; **no whole-database swap**. See `StayConnect-IAM-Phase1A-Plan.md` for migration groups MG-0…MG-9, per-object specs, the row-lock-first strategy, the replace/retain/migrate/remove matrix, disposable-data handling, and acceptance tests. **Cutover to `iam_v2` is a separate, later, explicitly gated event and an ATOMIC complete-domain switch of all IAM services together** (never per-flow or per-service; plan §7a); a single credential vertical slice does **not** authorize cutover, and build completion does **not** auto-promote.

**Rollback boundaries (cutover):** a routing flip-back is safe **only before the first production write** to `iam_v2` (Boundary A). **After** the first production write (Boundary B) a direct flip-back is forbidden without a tested reverse-migration/replay; otherwise **forward-fix only**, and all durable writes must be reconciled before any return. The first production write is the explicit no-return boundary.

**Resolved / open items:**
- **`folio_identity_strategy` fail-closed — APPROVED & in force (2026-07-16).** FINAL contract §4.1 amended to `NOT NULL DEFAULT 'UNSET'` (4-value CHECK). `UNSET` permits read-only ingestion/lookup/auth but **blocks every financial CHARGE** (before outbox/`P#`/transmission) until property onboarding records a concrete strategy in a **new** revision. `UNSET` is the only unset sentinel (`UNKNOWN` = a Posting state). No open folio item remains.
- **HA synchronization transport under the two-NIC rule — OPEN.** Single-appliance local-first/offline is current and supported; **HA failover under the two-NIC architecture is NOT designed/implemented/accepted**; the old third-NIC `hasync` design is superseded.
- **Live `iam_v2` creation** is a separately authorized action **after** A-series acceptance in a scratch/test DB; **cutover** is a still-later separate approval.

## 9. HISTORICAL — the next authorized action AS AT 2026-07-17

> **THIS SECTION IS HISTORICAL AND IS NOT THE CURRENT NEXT ACTION.** **The current next action is the
> GENERATED PROJECT STATE block at the top of this file**, which is rendered from
> `governance/project-state.json` and is the only carrier of it. What follows records the Phase-1B
> position as it stood on 2026-07-17, retained for provenance.

*(HISTORICAL, as at 2026-07-17.)* The single next authorized action **was** to complete Phase 1B execution and live-dark verification; Phase 1B implementation **was** Product-Owner authorized and in progress (decision `D10`, 2026-07-17; W0 complete). **Phase 1B has since been ACCEPTED AND CLOSED (D11/T0011), as have Phases 2, 3 and 4.** Execution proceeds in verified stages on branch `phase/1b-dark-auth` (PR #2, **not merged**): Gate P (least-privilege roles + credential rotation) → durable throttle + keyed-HMAC OTP → dark IAM-v2 credential/identity/auth-context code (scratch-tested) → controlled live-dark verification. This does **not** authorize cutover, Phase 2, production `iam_v2` reads/writes, bulk IAM migration, PMS posting, or legacy removal; **legacy public-schema authentication remained the sole configured authentication path** *(HISTORICAL, as at 2026-07-17; see D24/T0056 — StayConnect is PRE-LIVE and no real hotel guest depends on it)*.

The **Phase 1A LIVE-DARK acceptance record** (`StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md`, 18/18; authoritative production evidence `PROD_LIVE_DARK_EVIDENCE_V2.txt`, read-only — the earlier `PROD_LIVE_DARK_EVIDENCE.txt` is **superseded/erroneous**) stands unchanged. The dark `iam_v2` schema is created + verified in production but **NOT cut over**; no service reads/writes it, no DSN/`search_path` change. Cutover, IAM data migration, and legacy cleanup remain **separately gated** future events (plan §7a/§11 ladder) and are **not** authorized by Phase 1B.

**Gate P — HISTORICAL, as at 2026-07-17; COMPLETE since Phase 1B closure (D11/T0011).** *At that time* production services connected to `stayconnect_site` as the PostgreSQL superuser `stayconnect` (`rolsuper=true`); the schema's darkness rests on *zero code references + no `search_path` change*, not on grant isolation. Gate P — the first authorized Phase-1B execution step — replaced superuser use with least-privilege `svc_*` roles (per `Phase1B-Privilege-Matrix.md`) holding **zero** `iam_v2` privileges. **That work is COMPLETE and reboot-verified; the four site-DB daemons run under `svc_*` roles today.** Routing any service **to** `iam_v2` remains a later cutover event, out of Phase 1B scope.

## 10. Forbidden until explicitly approved

Schema migrations; feature code; production connector/posting-engine development; portal/admin-UI cutover; PMS production configuration; `pms_providers` creation; **any further live PMS/FIAS financial test** without separate authorization; guest-networking changes; deployment; network scanning; enabling Hotel ID 2 posting; building any reversal sender.

## 11. Documents in this pack

| File | Role |
|---|---|
| `StayConnect-IAM-Phase0-Contract.md` | **Authoritative** — FINAL Phase-0 architecture contract (DDL, invariants, state machines, FIAS findings §9). |
| `StayConnect-IAM-Handoff.md` | **Authoritative** — synchronized operational handoff. Its own generated block carries current state. |
| `StayConnect-IAM-Phase1A-Plan.md` | **Authoritative for Phase 1A** — ACCEPTED_AND_CLOSED. Not the current plan; the generated PROJECT STATE block carries the current phase. |
| `StayConnect-IAM-Phase1B-Plan.md` | **Authoritative for Phase 1B** — ACCEPTED_AND_CLOSED (D11/T0011); implemented and live-dark verified. *(It was planning-only and awaiting approval when this table was first written.)* |
| `Protel-FIAS-Phase0-Spike.md` | **Authoritative** — live FIAS spike + Gate 3A PASS evidence (guest identifiers redacted). |
| `SYSTEM_OVERVIEW.md` | Supporting — canonical current-system reference. |
| `TARGET_ARCHITECTURE.md` | Supporting — target architecture. |
| `STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | Supporting — operations manual. |
| `DEPLOYMENT_APPLIANCE.md` | Supporting — appliance deployment. |
| `OFFLINE_OPERATION.md` | Supporting — offline behavior. |
| `MIGRATION_RUNBOOK.md` | Supporting — migration/rollback runbook. |
| `PROJECT-INSTRUCTIONS.md` | Paste into the ChatGPT Project's custom instructions. |
| `ZERO_STALE_LEFTOVERS_RULE.md` | **Permanent rule** — the authoritative Zero-Stale-Leftovers rule (full text, bundled). |
| `GITHUB_EXECUTION_AND_DELIVERY_RULE.md` | **Permanent rule** — the authoritative GitHub execution/reporting/delivery rule (full text, bundled). |
| `MANIFEST.md` | Provenance + SHA-256 for every exported file. |

## 12. Source-of-truth precedence

0. **The GENERATED PROJECT STATE block at the top of this file** for anything mutable — current phase, current activity, current maturity, next authorized action, and the live `iam_v2` figures. No prose in this pack overrides it, and no other section may restate it.
1. Latest Product-Owner-approved **FINAL architecture contract** (`StayConnect-IAM-Phase0-Contract.md`).
2. Current synchronized **Context Handoff** (`StayConnect-IAM-Handoff.md`).
3. The **phase plan** named as current by the generated block. *(This line previously named the Phase-1A plan; Phase 1A closed, and pinning a plan filename here is how that went stale.)*
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

## 15. Permanent GitHub execution, reporting and delivery rule

The GitHub repository **`aibrahiiim1/StayConnectEnterprise`** is the **only** authoritative working source; the uploaded ZIP packs (this Project Pack, the Evidence Pack, the Planning Pack) are **exports and review artifacts only** and never override the repository, its Git history or verified execution evidence — when a ZIP and the repository disagree, the repository wins. Each approved Phase is delivered on **one implementation branch and one PR** (`phase/<name>-<purpose>`), completed end-to-end without auto-continuing into the next Phase and without direct commits to the protected default branch unless explicitly authorized. Every final report follows a **mandatory 20-section structure** and embeds the **complete deterministic changed-file manifest** from `tools/generate-change-manifest.py` (`base..HEAD`); a report whose file list differs from Git **fails delivery**. The authoritative rule text is bundled in this pack — see [GITHUB_EXECUTION_AND_DELIVERY_RULE.md](GITHUB_EXECUTION_AND_DELIVERY_RULE.md) (repository path `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md`); it is enforced by `tools/project-state.py validate` (checks `authoritative_remote` + `delivery_governance` + the `GH-*` decisions) and the keyword layer `tools/validate-project-state.sh`.
