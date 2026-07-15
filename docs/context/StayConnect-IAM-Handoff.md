# StayConnect IAM — Context Handoff

Operational handoff for a future agent or new session working on the Internet Access Management (IAM) redesign. The authoritative design is [StayConnect-IAM-Phase0-Contract.md](../architecture/StayConnect-IAM-Phase0-Contract.md); the spike record is [Protel-FIAS-Phase0-Spike.md](../spikes/Protel-FIAS-Phase0-Spike.md).

## Current Stage

**Phase 0 — Architecture Contract and live PMS validation.**

## Current Status

- The architecture contract is **CONDITIONALLY FROZEN** (it becomes FINAL only after the live Protel FIAS spike results are merged into the contract's FIAS section §9 and the product owner explicitly approves).
- **No feature implementation has started.**
- **No production schema, service, portal/UI, firewall, networking, or PMS configuration change has occurred** for this redesign. (The currently deployed voucher/guest-account/max-devices system is the previous, separate delivery and remains live and untouched.)
- The next authorized execution is the **read-only Protel FIAS preflight (Gate 1)**, followed by an **explicitly approved** live spike (Gate 3). Nothing else is authorized.

## Non-Negotiable Product Decisions (compact)

1. **No guest-facing PMS selector** — automatic STRICT backend Multi-PMS resolution on the complete outcome vector; unavailable/stale candidates block authentication; unmapped guest networks fail closed; no fallback PMS; uniform time-padded non-success responses.
2. **Room Number is evidence, never identity or financial ownership.** Every Stay, Folio, Event, Purchase, and Posting is pinned to exactly one PMS Interface namespace; identical room numbers across interfaces never collide. Sharers (two stays, one room) are legal.
3. **Mandatory Seamless Checkout Grace** (site-level, hidden system package): eligible checked-out guests — free, paid, or prepaid — are atomically superseded onto the grace entitlement; sessions rebind with zero nft churn and no re-authentication; over-limit devices grandfathered; no future room posting; emergency-grace fallback if config is corrupt.
4. **One live data-plane Entitlement per subject** (stay / account / voucher / guest principal-per-site). Changes are atomic same-subject supersessions; cross-PMS movement uses typed, cycle-safe `entitlement_transfers`.
5. **Stable tenant-wide Guest Principals** for OTP/social, keyed by verified factors (email, phone, issuer-scoped social subject). **MAC addresses identify Devices only, never people.**
6. **Immutable revisions everywhere:** service plans, packages, settlement mappings, PMS interface configs, PMS secrets (generations). Purchases/postings pin exact revisions; edits never mutate existing entitlements.
7. **Voucher codes stored HMAC-indexed + AEAD-encrypted** (recoverable value + last4); reveal/export requires re-auth + audit; single-redemption.
8. **One-time Auth Contexts and Offer Quotes**, consumed atomically with Purchase creation (null-safe exact quote binding). **Sessions are created only after Entitlement grant** — never at authentication.
9. **Idempotent accounting** via per-session watermarks (tenant/site-scoped, source epochs, sample sequence) + append-only ledger + monotonic entitlement counters; audited adjustments are the only way counters decrease.
10. **Financial safety:** purchase → settlement → posting/payment separation; postings pin interface + both revisions + secret generation + folio + exact settlement/purchase pair; per-interface outbox lanes; UNKNOWN never auto-retries on FIAS; FINANCIAL_RECOVERY_MODE after restore; five-action manual-review governance (no generic approve); ISO-4217 minor-unit money; merchant-account-scoped payment refs.
11. **Compliance archive with verified receipt before cross-customer purge**; tenant DEK crypto-shred; fail-closed transition.
12. **Supported-restore limitation:** exactly-once FIAS posting is guaranteed only under manifest-signed restore workflows — this is part of the support contract.
13. **No feature implementation until the Protel spike is done and the contract is approved FINAL.**

## Current Blocker

- The appliance (172.21.60.23, site DB `stayconnect_site`) currently has **zero configured `pms_providers` rows** — earlier stub-based PMS test config was cleaned up.
- The Protel FIAS endpoints are supplied **manually by the product owner** (recorded in the spike document). **Do not discover PMS systems by network scanning — scanning is forbidden.**

## Next Execution Gates

**Gate 1 — Read-only preflight (authorized next step):**
- TCP (and TLS if applicable) connectivity to the supplied endpoints;
- FIAS handshake and framing (LS/LA per FIAS 2.20 — spec at `docs/FIAS_2.20.24.pdf`);
- heartbeat/keepalive observation;
- connector/FIAS version identification;
- lookup of the approved test reservation and Folio;
- **no Posting; no link interruption.**

**Gate 2 — Present the exact live test plan** (scenarios, test room/folio, amounts, reversal method, timing, front-office coordination) **and wait for explicit approval.**

**Gate 3 — After approval only:**
- small test charge; Folio verification on the Protel side;
- reversal (negative posting);
- lost-ACK / UNKNOWN drill (link interrupted mid-post);
- Checkout while link down (staleness behavior);
- stale occupancy abort;
- heartbeat/resync cadence measurements;
- Folio-number reuse behavior.
Results are merged into the contract's §9 and the capability matrix; then the contract goes to the owner for FINAL approval.

## Forbidden Until FINAL Approval

- schema migrations (any database DDL for this domain);
- feature code;
- portal cutover;
- PMS production configuration changes;
- live Posting without explicit Gate-3 approval;
- network scanning;
- deployment of IAM-redesign artifacts.

## Useful Environment Facts

- Appliance: `172.21.60.23` (SSH as root, key auth), code at `/opt/stayconnect`, Postgres in container `stayconnect-pg`, site DB `stayconnect_site` (+ standby `stayconnect_site_b`).
- Central Control Plane: `150.0.0.252` — do not touch for this work.
- Repo: `d:\WebProjects\StayConnectEnterprise` (this repository). Existing FIAS parsing/framing lives in `data-plane/internal/pms/` (lookup-only; no posting code exists anywhere yet).
- The currently deployed production IAM (vouchers/guest accounts/plans, commits `8a1f882`/`0cca51b` era) stays operational until the Phase-1B cutover, which is far in the future and separately gated.
