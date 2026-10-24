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
| 1 | Database foundations: Postgres settings, connection pools, growth, retention, indexes | PHASE1_DATABASE.md | planned |
| 2 | Guest sign-in (scd) under a sign-in storm | PHASE2_GUEST_SIGN_IN.md | planned |
| 3 | Client Portal (portald) and captive-portal probe traffic | PHASE3_CLIENT_PORTAL.md | planned |
| 4 | Enforcement path (netd): nftables sets, tc shaping, conntrack, apply time | PHASE4_ENFORCEMENT.md | planned |
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
