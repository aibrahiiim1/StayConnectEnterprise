# Continuation checkpoint

Master is at merge commit `0582eb78` (PR #120), all four required gates ALL_GREEN before merge.
Appliance `172.21.60.25` and Central `150.0.0.252` both run artefacts rebuilt from that exact commit.

## Verified complete

* PMS departure fix: live-proven. Generation 248 admitted 424 roster records and ZERO snapshot departures,
  against 143 (gen 246) and 149 (gen 247). Backlog frozen, no longer growing.
* Snapshot-closure audit: 547 events -> 328 stays; 311 correct, 10 self-healed, 7 stale IN_HOUSE, ZERO in a
  proven-incorrect state. The 13 checked-out-but-rostered stays were live departures AFTER the roster
  boundary and are correct. No guest lost or regained access.
* Central non-licensing removal: code, APIs, UI, jobs and dependency gone; 251 166 telemetry rows dropped;
  nats-authz inactive AND disabled; 0 NATS containers; 0 telemetry tables. Removed endpoints answer 404,
  licensing endpoints answer 401 (present and enforcing).
* Provenance reconciled: all three deployed binaries now hash-match a rebuild from `0582eb78`.

## OPEN DEFECT, found by live verification after merge

**The completeness test no longer has its evidence.** Reconciliation proves a roster is complete by counting
the DISTINCT rooms a generation names -- occupied rooms arrive as GI/GC, vacant rooms as GO. The ingestion
fix discards vacant-room GO records, which are exactly the ones that made the sweep countable:

    gen 247 (before fix)   439 roster + 149 snapshot GO = 587 rooms
    gen 248 (after fix)    424 roster +   0 snapshot GO = 423 rooms
    gen 249 (after fix)    413 roster +   0 snapshot GO = 412 rooms

So `pms_roster_reconcile` now refuses with REFUSED_ROSTER_INCOMPLETE (423 of 587) and will keep refusing.
That is FAIL-SAFE -- it closes nothing and no guest is affected -- but the feature cannot be enabled.

**The fix is to record the evidence rather than infer it from admitted rows.** pmsd already counts the
records it skips (`RecordSkipped`). It should persist, per resync generation, the count of DISTINCT ROOMS the
sweep named including the skipped vacant ones, and `pms_roster_reconcile` should read that instead of
counting `stay_events`. Needs: a column or small table for per-generation room coverage, the pmsd counter,
a migration, tests, gates, merge, redeploy.

Do NOT work around it by re-admitting snapshot GO records; that reopens the 14 000-case defect.

## Also outstanding

* The 13 717 historical MANUAL_REVIEW cases are frozen and harmless but not yet dispositioned. The
  `pms_dispose_snapshot_cases` function and its UI button are deployed and will answer them; running it is
  safe independently of the completeness defect above, as it closes no stay and deletes nothing.
* Stage 6 of preflight OOMs on this workstation under default parallelism (system commit charge, not a
  product fault). tsc, vitest 280/280 and next build each pass when run individually.

## Build environment

Designated workstation, Docker 29.5.2, data disk on C: (F: is NOT achievable -- Docker Desktop 4.76 on the
WSL2 backend ignores `DataFolder` and reverts a supported WSL distro move). Go builds need `-p 2` /
`GOMAXPROCS=2`; Node needs `--no-file-parallelism`. Do NOT set `MSYS_NO_PATHCONV=1` for
`clean-install-reconstruction.sh` or `generate-production-baseline.sh` -- they manage path conversion
themselves and the override breaks them.
