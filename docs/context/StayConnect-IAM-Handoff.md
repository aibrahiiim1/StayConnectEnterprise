# StayConnect IAM — Context Handoff

Operational handoff for a future agent or new session working on the Internet Access Management (IAM) redesign. The authoritative design is [StayConnect-IAM-Phase0-Contract.md](../architecture/StayConnect-IAM-Phase0-Contract.md); the live spike record is [Protel-FIAS-Phase0-Spike.md](../spikes/Protel-FIAS-Phase0-Spike.md).

**Authoritative documentation baseline:** commit `ffe2200`.

## Current Stage

**Phase 0 — Architecture Contract and live PMS validation.** Protocol/architecture validation is **complete**; the contract awaits explicit product-owner FINAL approval.

## Current Status

- **Contract status: `READY_FOR_FINAL_OWNER_APPROVAL`** *(was CONDITIONALLY FROZEN).* It is **not** FINAL yet — it awaits **only** the product owner's explicit FINAL approval. `READY_FOR_FINAL_OWNER_APPROVAL` does **not** unlock implementation.
- The live Protel FIAS Phase-0 spike is **done**; results are merged into contract §9b/§9c/§9d.
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

## Next Authorized Step

1. **Explicit product-owner approval to mark the Phase-0 contract FINAL** (the single remaining finalization condition).
2. **Then begin Phase 1A only.**

Per-property onboarding (Tier 2, incl. Aqua Club) and post-implementation acceptance (Tier 3 / Gates 3C, 3D) are **not** finalization blockers and do **not** precede implementation.

## Forbidden Until FINAL Approval

- schema migrations (any DDL for this domain);
- feature code;
- production connector development;
- portal/admin-UI work;
- PMS production configuration changes;
- `pms_providers` creation;
- live Posting (no further PMS financial test without separate explicit authorization);
- network scanning;
- deployment of IAM-redesign artifacts.

## Useful Environment Facts

- Appliance: `172.21.60.23` (SSH as root, key auth), code at `/opt/stayconnect`, Postgres in container `stayconnect-pg`, site DB `stayconnect_site` (+ standby `stayconnect_site_b`).
- Central Control Plane: `150.0.0.252` — do not touch for this work.
- PMS Interfaces (owner-attested): **Hotel ID 3** Coral Sea Holiday Village `150.0.0.18:5003` (financially validated — one debit, Gate 3A PASS); **Hotel ID 2** Coral Sea Aqua Club `120.0.0.15:5001` (read-only capable, financially unapproved). No `IfcAuthKey` on either interface. **Do not discover PMS systems by network scanning.**
- Repo: `d:\WebProjects\StayConnectEnterprise`. Existing FIAS parsing/framing lives in `data-plane/internal/pms/` (lookup-only; no posting code exists anywhere yet).
- The currently deployed production IAM (vouchers/guest accounts/plans, commits `8a1f882`/`0cca51b` era) stays operational until the Phase-1B cutover, which is far in the future and separately gated.
