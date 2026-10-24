# Backend audit

An audit of the Go backend, one area per phase. Each phase file records:
- every finding, with where it is, the problem, its impact and status;
- the recommended fix and the test that proves it;
- the fixes grouped into small batches.

The audit changes no code. Fixes are made later, one batch at a time, on their own branches.

## Phases

| Phase | Area | File | Status |
|---|---|---|---|
| 0 | Tooling baseline, toolchain, phase map | [PHASE0_BASELINE.md](PHASE0_BASELINE.md) | done |
| 1 | Central (ctrlapi) | [PHASE1_CENTRAL.md](PHASE1_CENTRAL.md) | done |
| 2 | Appliance security foundations | [PHASE2_APPLIANCE_FOUNDATIONS.md](PHASE2_APPLIANCE_FOUNDATIONS.md) | done |
| 3 | Database and migrations | [PHASE3_DATABASE_AND_MIGRATIONS.md](PHASE3_DATABASE_AND_MIGRATIONS.md) | done |
| 4 | Guest sign-in (scd) | [PHASE4_GUEST_SIGN_IN.md](PHASE4_GUEST_SIGN_IN.md) | done |
| 5 | Client Portal (portald) | | not started |
| 6 | Payments, ledger, posting, accounting (acctd) | | not started |
| 7 | PMS (pmsd) | | not started |
| 8 | Networking (netd) | | not started |
| 9 | Admin Console API (edged), in four parts | | not started |
| 10 | Cross-cutting and final report | | not started |

The packages each phase covers are listed in the Phase 0 phase map.

## Rules for fixing

1. **One batch per branch.** Finish its tests before starting the next batch. Batches that share a file say so; do them one after another.
2. **DECISION findings** wait for the Product Owner.
3. **CONTROLLED findings** need the authorization CLAUDE.md §0 requires: database schema, certificates and trust, deployment.
4. **Start from the latest master.** Each phase records the commit it audited. Before fixing, check that the code it cites has not changed since.
5. **No guest data** in commits, PRs or these files.
