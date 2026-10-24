# Performance audit

An audit of how the solution performs at the size it is sold for, one area per phase. It covers the appliance services, its database, the Admin Console and Central. Each phase file records:
- every finding, with where it is, the problem, its impact at the target load, and its status;
- the recommended fix and the measurement that proves it;
- the fixes grouped into small batches.

The audit changes no code. Fixes are made later, one batch at a time, on their own branches.

It follows the backend audit in [`docs/audit/`](../audit/README.md). Where a performance finding is also a robustness finding there, for example missing request limits, this audit points to it rather than counting it again.

## Phases

| Phase | Area | File | Status |
|---|---|---|---|
| 0 | Baseline, targets, method, phase map | [PHASE0_BASELINE.md](PHASE0_BASELINE.md) | done |
| 1 | Database foundations: Postgres settings, connection pools, growth, retention, indexes | [PHASE1_DATABASE.md](PHASE1_DATABASE.md) | done |
| 2 | Guest sign-in (scd) under a sign-in storm | [PHASE2_GUEST_SIGN_IN.md](PHASE2_GUEST_SIGN_IN.md) | done |
| 3 | Client Portal (portald) and captive-portal probe traffic | [PHASE3_CLIENT_PORTAL.md](PHASE3_CLIENT_PORTAL.md) | done |
| 4 | Enforcement path (netd): nftables sets, tc shaping, conntrack, apply time | [PHASE4_ENFORCEMENT.md](PHASE4_ENFORCEMENT.md) | done |
| 5 | Background jobs and accounting (acctd, scd loops, posting, retention) | PHASE5_BACKGROUND_JOBS.md | planned |
| 6 | PMS (pmsd): roster load, resync, FIAS throughput | PHASE6_PMS.md | planned |
| 7 | Admin Console API (edged): lists, reports, exports, dashboards | PHASE7_ADMIN_API.md | planned |
| 8 | Admin front ends (hotel-admin, cloud-admin): bundles, polling, rendering | PHASE8_FRONT_ENDS.md | planned |
| 9 | Central (ctrlapi): fleet scale, appliance calls, Redis | PHASE9_CENTRAL.md | planned |
| 10 | Appliance resource budget: memory, CPU, disk, startup, backups | PHASE10_RESOURCE_BUDGET.md | planned |
| 11 | Load-test plan and final report | PHASE11_LOAD_TEST_PLAN.md, FINAL_REPORT.md | planned |

The packages, tables and loops each phase covers are listed in the Phase 0 phase map.

## Severity

| Level | Meaning |
|---|---|
| **High** | At the target load the service fails: an outage, timeouts guests or staff notice, memory exhaustion, or a queue that never drains. |
| **Medium** | At the target load the service slows noticeably, or a cost grows without limit as data or time accumulates. |
| **Low** | Wasted work or resources with no visible effect at the target load. |
| **Clean code** | A structural change that makes the next performance fix easier or measurable. |

Each finding also says how it is known: **Measured** (a number from a run), **Confirmed** (read in the code and certain), or **Suspected** (needs the measurement named in its fix).

## Rules for fixing

1. **One batch per branch.** Each batch states the measurement before and after. A performance fix without a number is not done.
2. **Measure on the reference size** (Phase 0 §2), not on a workstation alone.
3. **DECISION findings** wait for the Product Owner.
4. **CONTROLLED findings** need the authorization CLAUDE.md §0 requires: database schema, enforcement, PMS, deployment.
5. **Operational limits become settings** (CLAUDE.md §0C) when a fix introduces one.
6. **Start from the latest master** and check that the cited code has not changed.
7. **No guest data** in commits, PRs or these files. Load tests use generated data only.
8. **A performance fix never weakens a compliance control** (below). Where the faster option and the compliant option differ, the finding says so and the compliant one is recommended.

## Compliance

Every finding that touches personal data, security controls or records the law requires is checked against the rules below. Each phase has a **Compliance** column or note saying which apply. The appliance's operator (the hotel) is the controller; OneGate is its processor (`docs/privacy/DPA_TEMPLATE.md`). The repository's own privacy programme is in `docs/privacy/` and is binding here.

| Source | What it requires of a performance fix |
|---|---|
| **GDPR Art. 5(1)(c), (e): data minimisation and storage limitation** | Keep no more detail, and no longer, than the purpose needs. Coarser detail and shorter retention are preferred when a fix can choose. |
| **GDPR Art. 25: data protection by design and by default** | New caches, logs, metrics, profiles and load-test data hold no guest data by default. Metric labels never carry user-supplied values (`internal/metrics/metrics.go:2`). |
| **GDPR Art. 32: security of processing** | A fix keeps the security strength it replaces: password hashing, sign-in throttles, encryption, audit. |
| **GDPR Art. 15, 17, 20: access, erasure, portability** | Moved, rolled-up or cached personal data must still be found by the guest export and erasure (`docs/privacy/ERASURE_LEDGER.md`, GDPR plan C3, C4). |
| **ePrivacy Directive 2002/58/EC Art. 6: traffic data** | Traffic data (who used how many bytes, when) is erased or anonymised once it is no longer needed to deliver the service or for billing. Per-second accounting detail is traffic data. |
| **National connection-log laws** | Some countries require Wi-Fi operators to keep connection logs for a set time. The log period is the hotel's setting (1–730 days, `docs/privacy/DPA_ANNEX_II_SECURITY_MEASURES.md` §9); a fix never shortens below what the hotel set. |
| **Financial record retention** | Payment and posting records stay for the statutory period even after a guest is anonymised (`docs/privacy/HOTEL_RECORD_OF_PROCESSING_TEMPLATE.md` row 4). No trimming batch deletes them. |
| **PCI DSS** | Card data never reaches the appliance (Stripe and Paymob take it directly). A fix never logs, caches or profiles payment payloads. |
| **OWASP ASVS 4.0 and NIST SP 800-63B: authentication** | Password hashing stays at or above OWASP's Argon2id minimum (for example 19 MiB, 2 passes, or 46 MiB, 1 pass). Sign-in throttles and lockouts are not relaxed to gain speed. |
| **ISO/IEC 27001:2022 A.8.15, A.8.16: logging and monitoring** | Performance logging is kept, protected and time-limited like other logs; slow-query logs never print bound parameters. |
