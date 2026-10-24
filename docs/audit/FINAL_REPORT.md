# Backend audit: final report

**What was audited:** the Go backend of OneGate / StayConnect.
- Central: `control-plane`.
- The appliance services: `data-plane` (scd, edged, portald, netd, acctd, pmsd).
- Their shared `license` module, the database migrations, and the deploy files that shape them.

**How:** in eleven phases (0–10), one area per phase, against the latest master before each phase. The last phase ran on master `0879e839`.

**Ground rules:**
- Code only. Nothing was run against PRE-LIVE or Central.
- Nothing in the product was changed: every result is a recommendation.
- Tests ran on throwaway databases, containers and network namespaces, all removed afterwards.

**How to use this report.** Start with the five High findings, then follow the order of work below. Every finding is in its phase file with where it is, the problem, its impact, the recommended fix, the test that proves it, and the batch it belongs to.

## Totals

| Phase | Area | Critical | High | Medium | Low | Clean code |
|---|---|---|---|---|---|---|
| [0](PHASE0_BASELINE.md) | Tooling baseline | 0 | 1 | 1 | 1 | 0 |
| [1](PHASE1_CENTRAL.md) | Central (ctrlapi) | 0 | 2 | 17 | 22 | 9 |
| [2](PHASE2_APPLIANCE_FOUNDATIONS.md) | Appliance security foundations | 0 | 1 | 15 | 24 | 6 |
| [3](PHASE3_DATABASE_AND_MIGRATIONS.md) | Database and migrations | 0 | 0 | 6 | 12 | 4 |
| [4](PHASE4_GUEST_SIGN_IN.md) | Guest sign-in (scd) | 0 | 0 | 3 | 15 | 4 |
| [5](PHASE5_CLIENT_PORTAL.md) | Client Portal (portald) | 0 | 0 | 2 | 12 | 4 |
| [6](PHASE6_PAYMENTS_AND_POSTING.md) | Payments, posting, accounting | 0 | 0 | 4 | 15 | 4 |
| [7](PHASE7_PMS.md) | PMS (pmsd) | 0 | 0 | 2 | 14 | 4 |
| [8](PHASE8_NETWORKING.md) | Networking (netd) | 0 | 1 | 10 | 14 | 4 |
| [9](PHASE9_ADMIN_CONSOLE_API.md) | Admin Console API (edged) | 0 | 0 | 5 | 16 | 4 |
| [10](PHASE10_CROSS_CUTTING.md) | Cross-cutting | 0 | 0 | 0 | 2 | 0 |
| **Total** | | **0** | **5** | **64** | **147** | **43** |

**How the counts were adjusted:**
- Phase 6 M4 and Phase 8 M4 are one defect, the shared-allowance speed update. It is counted once, in Phase 8.
- Phase 0's P0-3 is counted as Low, after Phase 10's correction.
- Phase 5's M2 was lowered to Low by Phase 6 and is counted there as Low.

**There is no Critical finding.** No path was found by which someone outside the hotel, without credentials, takes over Central, an appliance or a guest's money.

## The five High findings

| # | Finding | Why it matters | Batch |
|---|---|---|---|
| 1 | **Phase 0 P0-2: the Go toolchain and libraries carry known, reachable vulnerabilities.** Appliance 37, Central 20, re-checked in Phase 10. Several need Go 1.26.9 or later. | Every HTTP service, including Central's internet-facing API, is built on them. | Phase 0 B0b, with B0c. **DECISION.** |
| 2 | **Phase 1 H1: a missing CA certificate file makes Central create a new root CA.** | Every appliance certificate then stops verifying, and the trust chain is replaced silently. | Phase 1 B1. CONTROLLED (trust). |
| 3 | **Phase 1 H2: an appliance is activated on facts anyone can know.** | Someone who registers first with the same public facts can have their box activated in place of the real one. | Phase 1 (after Phase 2). **DECISION.** |
| 4 | **Phase 2 H1: the licence's anti-replay and revocation records can be lost silently.** | A revoked or replaced licence can be accepted again. | Phase 2 B1. CONTROLLED (licensing). |
| 5 | **Phase 8 H1: a guest network's name can add rules to the firewall.** Proven on a real kernel. | Any operator with the network permission can give every guest internet without signing in or paying, or open appliance services to guests. | Phase 8 B1 with Phase 9 B3. CONTROLLED (networking). |

## Recommended order of work

Each wave lists batches by phase file and batch number, for example "P8 B1". A batch is one branch. Batches in the same wave can run in parallel unless they share files; each phase file says when they do.

| Wave | Goal | Batches |
|---|---|---|
| **1** | **Close the High findings.** | P1 B1 (CA fails closed); P8 B1 + P9 B3 (firewall and config input); P2 B1 (licence store); P0 B0b + B0c (toolchain, on its own branch, when no other backend branch is open); P1 H2 after its decision |
| **2** | **Limits and timeouts everywhere** (pattern P1). One request can no longer take a service down. | P1 B2; P5 B1; P9 B1; P7 B1; the M1 part of P8 B6 |
| **3** | **Honest recovery and durable state** (patterns P2, P3). | P2 B3, B4, B5; P6 B2 + P8 B5 (plan counters, both sides); P8 B2, B3 (netd apply and system-network rollback); P6 B3 (reversals); P1 B4, B6; P9 B5 (restore checks the schema); P3 B2, B3 (migration runners) |
| **4** | **Guest enforcement correctness.** | P8 B4 + P6 B4 (shared allowance); P8 B6 (ruleset: private ranges after its decision, open connections, real-kernel tests); P8 B7 (device binding); P4 B3 (expiry sweep); P6 B1 (posting backoff); P7 B2 (resync) |
| **5** | **The remaining Medium findings.** | P1 B3, B5, B7, B8, B12; P2 B2, B7, B8, B9; P3 B4, B5, B7; P4 B1, B2; P5 B2, B3; P9 B2, B4 |
| **6** | **Low findings and clean code.** | The remaining batches of each phase. P0 B0a (the check gate) can go first at any time, because it changes no product code. |

**Before any batch:**
- Check that the lines it cites have not changed on master.
- Batches marked CONTROLLED need the authorization CLAUDE.md §0 requires: certificates and trust, licensing, database schema, guest authentication and enforcement, PMS, financial actions, networking, deployment.

## Decisions waiting for the Product Owner

| Phase, finding | Decision | Recommended |
|---|---|---|
| P0 P0-2 | Move to Go 1.26 and update the vulnerable libraries? | Yes, as the first backend branch. |
| P1 H2 | Require a short code, shown on the appliance's own console, to activate it? | Yes. |
| P1 M4, M5 | Hide Central staff emails, internal reasons, staff ids and IP addresses from customer users? | Yes, hide them. |
| P1 M15 | May administrators reset peer administrators? | No: only owners reset administrators. |
| P2 M3 | Remove the legacy allowance for unbound licences? | Yes, after confirming no deployed appliance still runs one. |
| P2 M8 | Pin the appliance's mutual-TLS trust anchor at provisioning, instead of trusting Central's response? | Yes. |
| P2 M14 | Keep the network-wide room sign-in pause? | Keep it, but exempt devices that signed in on that network recently, raise the default and alert staff on the first pause. |
| P3 L10 | Keep a Central appliance's certificate and lifecycle history after the appliance is deleted? | Yes: `RESTRICT` and soft delete. |
| P3 M2, fix 4 | Record migrations 0001–0149 as applied on baseline-built appliances with a one-time audited adopt step? | Yes, with P3 B2's deployment. |
| P4 M2 | Raise the minimum digits-only voucher length from 6 to 8? | Yes. |
| P4 L3 | Normalise guest names to NFC? | Yes, nothing looser. |
| P4 L11 | Block a tenant transition until a backup key holder exists? | Yes. |
| P8 M2 | Which internal networks may guests reach? | Deny all private, carrier-NAT and link-local ranges by default, with an allow-list setting. |
| P8 L1 | Relabel "client isolation" as depending on the access point, or enforce it on multi-port bridges? | Relabel it now; enforce it where a bridge has several ports. |
| P9 M4 | Record browsing a guest list in the read audit? | Yes: one entry per page view, without the search text. |
| P9 L2 | Does a desk reset of a guest account's password need a reason? | Yes, a reason; no password step-up. |
| P9 L4 | Keep operator account lockout as it is? | Keep it; document the trade-off. |

## What holds up well

These are the strengths to keep when fixing. Each phase file has its own list.

- **Signatures and trust.** Every signature covers exact bytes. Keys are chosen by key id, licences are re-checked on every evaluation, the PMS TLS pin is checked on every connection, and portal certificates are installed only after their chain is checked.
- **Authorization.**
  - Central checks permissions on the route and customer scope in the handler.
  - edged puts every route behind one permission helper, with deliberately split powers, and asks for the password again on almost every sensitive action.
  - netd identifies its callers by kernel peer credentials.
- **Money.**
  - The database checks a room charge's exact bytes, room, reservation and freshness, and never sends the same charge number twice.
  - Reversals are capped by a trigger.
  - Every financial decision needs a password, a reason and a version check.
- **Guests.**
  - Sign-in codes are counted and used up in one locked transaction.
  - Room sign-in keeps the last good roster.
  - Guest IPv6 is dropped in both directions.
- **Migrations.** A new appliance and an upgraded one have identical schemas, and the down migrations undo their up exactly.
- **Tests.** The suites are large and test real boundaries: the database as the real service roles, real kernel packets, a simulated PMS. All of them passed.

## Verification across the audit

| Check | Result |
|---|---|
| Central database tests, migrations up, down and up again | pass |
| Appliance install-path drift (baseline against upgrade) | none |
| scd, portald, payments, posting, PMS, netd and edged database tests, on throwaway full-schema databases with the real service roles | all pass. Phase 3 found 7 stale OTP and key-bootstrap tests (P3 M5). |
| Kernel netns suite (nftables and tc, real packets) | 33 pass |
| Race detector, unit tests of every package (Phase 10) | 79 packages, no data races |
| go vet, staticcheck, gosec, govulncheck | run in every phase; findings triaged into that phase |

## What this audit did not cover

- **Live behaviour on PRE-LIVE and Central.** By rule, nothing was run against them. Configuration on the boxes, such as env files, the Caddy vhosts as installed, and firewall state, was read from the repository only.
- **The web front ends** (`hotel-admin`, `cloud-admin`), except where a backend finding depends on them.
- **Database integration tests and the kernel suite under `-race`.**
- **The kernel suite on a production kernel.** It ran on the WSL2 6.6 kernel, not the appliance's.
- **Load and performance testing.**

## Where the work stands

- **Branch:** all audit files are on `delivery/backend-audit`. Nothing was pushed: pushing waits for the Product Owner's approval.
- **Product code:** no product code was changed.
- **Fixes:** each fix batch starts from the latest master, on its own branch, after the decisions it depends on.
