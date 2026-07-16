# StayConnect IAM — Context Handoff

Operational handoff for a future agent or new session working on the Internet Access Management (IAM) redesign. The authoritative design is [StayConnect-IAM-Phase0-Contract.md](../architecture/StayConnect-IAM-Phase0-Contract.md); the live spike record is [Protel-FIAS-Phase0-Spike.md](../spikes/Protel-FIAS-Phase0-Spike.md).

**Current synchronized documentation baseline:** the Phase-1A live-dark reconciliation commit (see git log; supersedes `79bf3e8`/`22a2e15`). *(Historical Phase-0 finalization provenance only: contract `ffe2200`, synchronized handoff `6b4721d` — not current.)*

## Current Stage

**Phase 0 — FINAL and CLOSED (2026-07-16).** **Phase 1A — formally Product-Owner ACCEPTED and CLOSED (2026-07-16) at `SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED — DARK, NOT CUT OVER`.** Dark, not cut over, not a user-facing/authority switch. No cutover, no service routing, no IAM data migration, no Phase 1B implementation. **Phase 1B planning is the current activity: a complete Phase 1B implementation plan is drafted (planning-only) — [StayConnect-IAM-Phase1B-Plan.md](../architecture/StayConnect-IAM-Phase1B-Plan.md). Next authorized activity: Product-Owner approval or rejection of the complete Phase 1B plan** (Phase 1B remains NOT implemented; the mandatory least-privilege / superuser-elimination prerequisite is inside that plan).

## Current Status

- **Contract status: `FINAL` — Phase 0 CLOSED** *(2026-07-16, explicit Product-Owner approval; previously READY_FOR_FINAL_OWNER_APPROVAL, before that CONDITIONALLY FROZEN).* **Current status:** the Phase 1A plan has been approved and Phase 1A is implemented through **production live-dark (created + verified, dark, not cut over)**. *(Historical: at Phase-0 close, FINAL closed the architecture without by itself unlocking implementation, and Phase 1A **planning** was then the only authorized activity pending separate plan approval — that gate is now satisfied and does not describe current status.)*
- The live Protel FIAS Phase-0 spike is **done**; results are merged into contract §9b/§9c/§9d.
- The Phase 1A execution plan ([StayConnect-IAM-Phase1A-Plan.md](../architecture/StayConnect-IAM-Phase1A-Plan.md)) is **SCRATCH_IMPLEMENTED + SCRATCH_VERIFIED + OFFLINE_REAL_SCHEMA_COMPATIBILITY_VERIFIED + PRODUCTION_LIVE_DARK_CREATED_AND_VERIFIED (2026-07-16)** — **formally ACCEPTED/CLOSED at this DARK maturity (2026-07-16); NOT deployed, NOT cut over, NOT a user-facing/authority-switch system, NO IAM data migration, NO Phase 1B implementation**. The isolated `iam_v2` schema (49 tables, fingerprint `bd75026f`, 0 rows) now exists **dark** in production `stayconnect_site` (primary; PG 16.3): no service reads/writes it, no DSN/`search_path` change, public **structural** fingerprint unchanged (`d86ca4c6` before==after; public *rows* are live and grow with normal guest traffic — the structural fingerprint is the invariant, not the row count), services active. Additive + reversible (rollback proven). Deviation recorded: services connect as superuser `stayconnect`, so darkness rests on zero code refs + `search_path` (both verified), not privilege grants — this is a **mandatory Phase-1B prerequisite** (least-privilege service roles + credential rotation before any routing). See [Live-Dark Acceptance record](../acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md) (18/18) + authoritative read-only re-verification `iam_v2_scratch/review/prod/PROD_LIVE_DARK_EVIDENCE_V2.txt` (the earlier `PROD_LIVE_DARK_EVIDENCE.txt` is superseded/erroneous). Evidence + review bundle in [`iam_v2_scratch/`](../../iam_v2_scratch/) (`EVIDENCE.txt`, `review/`): MG-0…MG-9 (49 `iam_v2` tables) + engine; **99/99 PASS** — Core 42, Extra 11, allowlist-guard 12, role/least-privilege 20 (objects owned by `iam_v2_owner`, service roles+PUBLIC denied), migration idempotency 5 (apply-twice no-op + catalog equality), offline real-schema compatibility 9 (identical `iam_v2` catalog on the committed real platform chain `0001..0006`). Contract-to-implementation fidelity matrix: 0 missing constraint/FK/trigger/index. Disposable Docker Postgres, allowlist-guarded; N/A-SCRATCH/DEFERRED items never counted PASS. Phase 1A is formally accepted/CLOSED at this maturity; next = PO approval or rejection of the Phase 1B plan. Phase 1B **implementation** (including the mandatory least-privilege service-role prerequisite), cutover, IAM data migration, and legacy cleanup each under **separate** authorization. Isolation is an **isolated `iam_v2` schema** inside the existing site DB (dark; rollback = leave dark / drop schema; a separate gated `search_path` cutover — **not** a whole-DB standby swap). Row-level locks preferred over advisory (verified admission namespaces `LN_DEVICE_SLOT`=11 / `LN_CAPACITY`=7 from `session.go`); cutover is an **atomic complete-domain switch of all IAM services** (never per-flow/per-service) with **two rollback boundaries** (safe flip-back only *before* the first production write to `iam_v2`; after it, forward-fix / reconcile-before-return); reversal stays `capability=false` (no executable reversal built in 1A); Stripe FK deferred (no platform anchor exists). **Folio amendment — APPROVED & in force (2026-07-16):** FINAL contract §4.1 amended to `folio_identity_strategy NOT NULL DEFAULT 'UNSET'` (4-value CHECK). `UNSET` is fail-closed — read-only ingestion/lookup/auth allowed, **every financial CHARGE blocked before outbox/`P#`/transmission** until property onboarding records a concrete strategy in a **new** revision. `UNSET` is the only unset sentinel (`UNKNOWN` stays a Posting state). No open folio item remains.
- **No feature implementation has started.** No production schema, service, portal/UI, firewall, networking, or PMS configuration change has occurred for this redesign. (The deployed voucher/guest-account/max-devices system is a separate prior delivery and remains live and untouched.)

## Proven Phase-0 Scope (Tier 1 — the finalization basis)

Measured live and merged into the contract. Covers **only** what is listed; do **not** generalize the single Hotel ID 3 debit to other properties/interfaces, sharers, multi-folio, no-post, or error `AS` statuses.

- both Protel PMS endpoints are **reachable**;
- each PMS Interface has an **independent namespace** (identical room numbers across interfaces never collide);
- FIAS **framing and `LS`/`LD`/`LR`/`LA` startup** are verified (client sends `LS/LD/LR` immediately on connect, acks incoming `LS`/`LA` with a bare `LA|`; do not gate on a client-side "reach LA first" milestone);
- **`GI`/`GC`/`GO`** guest feed is verified (plus read-only `DR` resync);
- **`RN` + `G#` are mandatory** for Guest-Folio Posting (an `RN`-only `ASOK` is not proof);
- **`PS`/`PA` financial wire behavior is production-grounded** (`PS` field order `RN,G#,TA,PT,SO,CT,P#,WS`; `PA` fields `RN,AS,P#,CT`; `AS ∈ {OK,NG,NA,NP,NR,RY,UR}`);
- **Coral Sea Holiday Village / Hotel ID 3** completed **one live controlled Debit** (USD 1.00, `TA100`);
- **`PA ASOK` matched using PMS Interface + `P#`** (never by Room Number);
- Front Office **verified the correct Guest Folio**;
- **`SO=WIFI` revenue mapping** was verified;
- **manual correction** was completed;
- the **Folio returned to its original balance**;
- the Protel Interface has **one active-client slot** (single-client Socket Server);
- **`P#` is a protocol-attempt reference, not business idempotency**.

## Tier 2 — Per-Property Financial Onboarding (deployment gate, NOT a Phase-0 blocker)

Before financial Posting is enabled for **any** Property, that Property must independently validate:

- PMS Interface **currency and exponent**;
- **Package currency compatibility**;
- **`SO=WIFI` revenue mapping**;
- **`RN` + `G#` Folio targeting**;
- **one controlled Debit**;
- **actual Folio placement**;
- **approved correction and net-zero cleanup**.

**Coral Sea Aqua Club / Hotel ID 2 (`120.0.0.15:5001`)** is: **read-only FIAS capable**; **financially unapproved**; **pending its own property onboarding test**. It does **not** block architecture finalization.

## v1 Limitations (deferred; recorded in contract §9d)

- `programmatic_reversal = false`;
- corrections use an **audited manual Front Office process**;
- **no implicit FX conversion** for PMS-settled Packages;
- **Package currency must match the pinned PMS Interface currency**;
- **physical traffic accounting still requires live implementation acceptance** (non-zero real-device usage → accounting), unprovable at Phase 0.

## Tier 3 — Post-Implementation Acceptance Gates (binding; testable only after the components exist)

- **Gate 3C** — after the **Posting Engine** exists (`posting_attempts`/`posting_attempt_events`, `pms_interface_pnumber_seq`, Manual-Review): transmitted request with no matching `PA` becomes **UNKNOWN**; **no automatic retry**; **no auto-allocated second `P#`**; Manual-Review workflow; external Folio reconciliation; audited `CONFIRM_POSTED`/`RETRY_APPROVED`/`ABANDON`; **duplicate prevention**.
- **Gate 3D** — after the **PMS/Entitlement** components exist (Stay/Event persistence, Checkout handler, Post-Stay profile, Checkout-Grace Purchase+Entitlement, session reassignment, accounting cutoff, idempotent processing): **Checkout** (healthy-link / link-down / delayed), **stale occupancy** refusal, **Checkout Grace**, **session reassignment**, **accounting cutoff** at the effective checkout timestamp, and **idempotency** — with no intentional guest disconnect or re-authentication.

## Non-Negotiable Product Decisions (compact)

1. **No guest-facing PMS selector** — automatic STRICT backend Multi-PMS resolution on the complete outcome vector; unavailable/stale candidates block auth; unmapped guest networks fail closed; uniform time-padded non-success responses.
2. **Room Number is evidence, never identity or financial ownership.** Every Stay, Folio, Event, Purchase, and Posting is pinned to exactly one PMS Interface namespace. Sharers (two stays, one room) are legal.
3. **Mandatory Seamless Checkout Grace** (site-level hidden system package): eligible checked-out guests atomically superseded onto grace; sessions rebind with zero nft churn, no re-auth; over-limit devices grandfathered; no future room posting; emergency fallback if config corrupt.
4. **One live data-plane Entitlement per subject**; changes are atomic same-subject supersessions; cross-PMS movement via typed cycle-safe `entitlement_transfers`.
5. **Stable tenant-wide Guest Principals** for OTP/social keyed by verified factors; **MAC addresses identify Devices only, never people**.
6. **Immutable revisions everywhere** (plans, packages, mappings, interface configs, PMS secret generations); purchases/postings pin exact revisions.
7. **Voucher codes HMAC-indexed + AEAD-encrypted** (recoverable value + last4); reveal/export re-auth + audit; single-redemption.
8. **One-time Auth Contexts and Offer Quotes**, consumed atomically with Purchase creation. **Sessions created only after Entitlement grant.**
9. **Idempotent accounting** via per-session watermarks + append-only ledger + monotonic counters; audited adjustments are the only decrease.
10. **Financial safety:** purchase → settlement → posting/payment separation; postings pin interface + both revisions + secret generation + folio + exact settlement/purchase pair; per-interface outbox lanes; **UNKNOWN never auto-retries**; FINANCIAL_RECOVERY_MODE after restore; five-action manual-review governance; ISO-4217 minor-unit money.
11. **Compliance archive with verified receipt before cross-customer purge**; tenant DEK crypto-shred; fail-closed transition.
12. **Supported-restore limitation:** exactly-once FIAS posting is guaranteed only under manifest-signed restore workflows.
13. **No feature implementation until the contract is approved FINAL.**

## Roadmap (completed / current / future)

1. **Phase 1A — clean-slate IAM core + persistence.** Scratch verification: **done**. Offline real-schema compatibility: **done**. **Production live-dark creation + acceptance: DONE (2026-07-16, 18/18).** ← current maturity.
2. Phase 1B — credential/portal integration, initially dark/flagged. *(not started)*
3. Phase 2 — packages, quotes, non-financial purchases.
4. Phase 3 — stays, events, STRICT multi-PMS resolution.
5. Phase 4 — financial Posting + payment execution with UNKNOWN/manual-review safety.
6. Phase 5 — Checkout Grace, post-stay, cross-PMS transfer.
7. Phase 6 — guest self-service + remaining IAM capabilities.
8. Full-domain implementation + acceptance of every supported IAM path.
9. Separately-approved **atomic complete-domain cutover**.
10. Post-cutover soak, reconciliation, separately-approved legacy cleanup.

## Next Authorized Step

1. **Product-Owner approval or rejection of the complete Phase 1B plan** ([StayConnect-IAM-Phase1B-Plan.md](../architecture/StayConnect-IAM-Phase1B-Plan.md)) — the single next authorized action. The plan is **planning-only**; it authorizes no code/DDL/role/DSN/routing/deployment/migration/cutover. (Phase 1A LIVE-DARK acceptance record — [record](../acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md), 18/18 — is unchanged.)
2. Phase 1B **implementation** (credential/portal, dark/flagged, incl. the mandatory least-privilege / superuser-elimination prerequisite) requires its **own** explicit PO authorization (the blueprint in Phase-1B plan §14); approving the plan is not the same as authorizing implementation.
3. Guest-visible activation / cutover, DSN/`search_path` routing, IAM data migration, and legacy cleanup remain **separately gated** later ladder steps. Each transition needs its own PO approval.

Per-property onboarding (Tier 2, incl. Aqua Club) and post-implementation acceptance (Tier 3 / Gates 3C, 3D) are **not** Phase-0 finalization blockers; they carry forward as, respectively, a deployment prerequisite and binding acceptance requirements.

## Forbidden Until Separately Authorized (Phase 1B / cutover / deployment)

*(Phase 1A is implemented to production live-dark. The following remain forbidden until each is explicitly and separately authorized — approval of Phase 1A does **not** authorize any of them.)*

- further schema DDL for this domain **beyond the created dark `iam_v2`** (e.g. cutover/data-migration DDL);
- routing any service to `iam_v2`, DSN/`search_path` change, dual-write, or IAM data migration;
- feature code;
- production connector development;
- portal/admin-UI work;
- PMS production configuration changes;
- `pms_providers` creation;
- live Posting (no further PMS financial test without separate explicit authorization);
- network scanning;
- deployment of IAM-redesign artifacts.

## Useful Environment Facts

- Appliance: `172.21.60.23` (SSH as root, key auth), code at `/opt/stayconnect`, Postgres in container `stayconnect-pg`, site DB `stayconnect_site` (+ an isolated second-site **test** DB `stayconnect_site_b` used for isolation tests — **not** a replication standby).
- Central Control Plane: `150.0.0.252` — do not touch for this work.
- PMS Interfaces (owner-attested): **Hotel ID 3** Coral Sea Holiday Village `150.0.0.18:5003` (financially validated — one debit, Gate 3A PASS); **Hotel ID 2** Coral Sea Aqua Club `120.0.0.15:5001` (read-only capable, financially unapproved). No `IfcAuthKey` on either interface. **Do not discover PMS systems by network scanning.**
- Repo: `d:\WebProjects\StayConnectEnterprise`. Existing FIAS parsing/framing lives in `data-plane/internal/pms/` (lookup-only; no posting code exists anywhere yet).
- The currently deployed production IAM (vouchers/guest accounts/plans, commits `8a1f882`/`0cca51b` era) stays operational — it is the sole authority throughout Phase 1B (which is DARK, not a cutover) and until the later **atomic complete-domain cutover** (only after Phases 2–6 and full-domain acceptance; far in the future, separately gated).
- **Appliance topology (approved, permanent): exactly two physical NICs — WAN and LAN.** WAN is **also the management interface** (Hotel Admin/SSH/outbound sync); LAN carries guest connectivity and guest VLAN/trunk behavior. There is **no** separate physical management NIC and **no** approved third HA-sync NIC. `SYSTEM_OVERVIEW.md` and the operations manual already reflect this (WAN=`ens160`, LAN=`ens192`). The older `DEPLOYMENT_APPLIANCE.md`/`TARGET_ARCHITECTURE.md` three-interface (separate mgmt + optional `hasync`) wording is **superseded** by this two-NIC rule.

## Open Architecture Decisions (unresolved)

- **HA synchronization transport under the two-NIC rule (still OPEN).** The prior HA design (VRRP/conntrackd/nft-set replication/Postgres streaming replication) assumed a **dedicated third `hasync` NIC**, which is now superseded. The exact HA-sync transport over a two-NIC (WAN+LAN) appliance is **not designed, implemented, or accepted** — this is an **OPEN architecture decision**. **Single-appliance local-first/offline operation is current and supported; HA failover under the final two-NIC architecture is NOT.** Do **not** claim any WAN/LAN HA failover, conntrack/nft replication, or Postgres streaming replication is available.
- *(Resolved 2026-07-16: the `folio_identity_strategy` fail-closed amendment was Product-Owner-approved and applied to the FINAL contract §4.1 — no longer an open item.)*

## Permanent Rules (mandatory)

- **[Zero Stale Leftovers](../ZERO_STALE_LEFTOVERS_RULE.md)** — after every milestone/decision/implementation change, no stale/superseded/contradictory/partially-updated artifact may remain anywhere (docs, code, config, exports, manifests); run the repo-wide stale scan + `tools/validate-project-state.sh`, prove zero current-state contradictions, and confirm `ZERO_STALE_LEFTOVERS = PASS` before declaring a milestone complete. A lower section does not fix a stale statement earlier in the same file; a banner does not excuse contradictory current-state content.
- **Documentation synchronization** — the latest PO-approved contract + verified execution evidence override all older docs/chats; all directly-related sources must state one consistent current status, maturity, limitations, authoritative commits, remaining blockers, and the **single** next authorized action. Old content survives only when explicitly labeled HISTORICAL/SUPERSEDED and cannot be mistaken for current.
