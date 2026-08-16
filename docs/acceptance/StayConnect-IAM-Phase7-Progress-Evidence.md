# Phase 7 — full-system re-acceptance: progress evidence

**Phase 7 is `IN_PROGRESS`.** Not accepted, not closed, not merged. Authorized by **D26** (2026-08-16).
Branch `phase/7-full-system-reacceptance`, from post-Phase-6 master `9cb25b8`.

Scope reconciled from the FINAL contract §18 (Phase 7 = *cleanup, final docs/ops manual, full-system
re-acceptance*, gate = *complete matrix*) and its §19 A–G Acceptance & Failure-Drill Matrix. **Not** from
`MIGRATION_RUNBOOK.md` §"Phase 7 — Start edged" (a deployment step) or
`COMMERCIAL_ONBOARDING_EXECUTION_STATUS.md` (a separate commercial numbering).

---

## What is proven

### M1 — identity and acquisition, composed · **20/20**

`iam_v2_scratch/phase7_m1_identity_and_acquisition.sh`, run repeatedly and re-runnably against the Phase-6
scratch database. Discharges **D1** (two PMS namespaces, colliding room 101, no selector), **C3** (once per
stay), **C4** (a pinned revision is immutable under a live entitlement), **C** (one entitlement per purchase),
**A1/A2/A3** (shared window, third device refused against a limit of two, re-authorization burns no slot),
**A7** (no exit from TERMINATED), plus the two seams no phase gate can assert alone: the entitlement's stay,
interface and purchase agree, and the plan it is accounted against is the one the package revision sold.

**Mutation-checked.** Dropping `ent_live_stay` fails exactly C3 and nothing else; restoring it returns 20/20.

### M2 — the stay, end to end · **22/22**

`iam_v2_scratch/phase7_m2_the_stay_end_to_end.sh`. **F1** room move, **F2** stale events refused, **F3**
checkout ending the pre-checkout entitlement at the boundary with the reason recorded, **F5** grace granted as
a new entitlement with once-per-stay still holding, and Phase 6 composed: an existing entitlement keeps the
`VALIDITY_WINDOW` revision it pinned while the same plan publishes an `AGGREGATE_ONLINE_TIME` revision;
budgets come from the pinned revision; exhaustion terminates for `TIME`. The cross-namespace seam is checked
last: after a full checkout/grace cycle on stay A, stay B — same room number, other namespace — is untouched.

### M3 — the boundaries hold · **31/31**

`iam_v2_scratch/phase7_m3_boundaries.sh`, every privilege measured **as the real service role**. Financial
DARK expressed as privilege rather than as a flag; `E4b`'s `UNSET` confirmed as both a real value and the
default; no runtime role able to write an entitlement or session, delete an accounting record or device,
execute the boundary termination or apply an arbitrary transition; append-only checked by *attempting* the
write; the mixed-version catalog probe's premise verified; all 47 migrations confirmed to have a down
migration, counted from the filesystem.

**Mutation-checked.** Granting `svc_acctd` UPDATE on entitlements fails exactly that assertion; revoking it
returns 31/31.

### The reboot drill · **5/5**, from a real reboot with no operator action

`deploy/scripts/phase7-reboot-drill.sh`, on the development appliance. All six services converged to serving
in **10 seconds**, no unit latched failed, every unit enabled at boot, flag coherence green, and the guest
device route **ABSENT** in the `scd` that came back.

### The appliance, dark

Verified through the authoritative gate after every reboot in this phase: flag coherence 6/6, guest route
absent, exact dark Hotel Admin release by path and content hash, no synthetic state, no appliance with the
capability enabled.

---

## What is NOT proven, and why

**The complete cross-phase matrix has NOT been run to completion in this environment.** It is reported here
rather than omitted, because a matrix that silently skips is worse than one that fails.

`iam_v2_scratch/phase7_full_matrix.sh` exists and runs, and the Phase-7 gates pass through it (73/0 at the
time they were run). The cross-phase run did not complete for two environment reasons, neither of which is a
product finding:

1. **Two matrix runs overlapped on one scratch database.** The matrix includes `phase6_backup_restore`, which
   DROPs and restores that database. Both runs then reported failures that were artifacts of the collision.
   The runner now holds an exclusive lock, so this cannot recur.
2. **The Phase-6 scratch database was rebuilt without the phase migrations.** `run.sh fresh` applies the
   `iam_v2_scratch/migrations/mg*` base set, not `data-plane/migrations/0009..0047`, and re-applying those on
   top conflicts with the base set. Restoring a full Phase-2→6 scratch database is an environment task that
   remains open.

Consequently: **the M1/M2/M3 results above stand as recorded at the time they were run and were each
reproduced across repeated runs; the combined Phase-3/4/5/6 regression matrix is NOT currently green in this
environment and is not claimed to be.**

### A privilege observation, resolved

While investigating the corrupted database, `iam_v2.p6_data_crossing` — a `SECURITY DEFINER` function —
appeared executable by `PUBLIC`. Both authoritative sources were checked:

* **the migration set is correct**: `0041` does `REVOKE ALL ... FROM PUBLIC` and grants `svc_acctd`;
* **the appliance is correct**: its ACL is `{stayconnect=X/stayconnect, svc_acctd=X/stayconnect}`, and PUBLIC
  holds no EXECUTE.

The exposure existed only in the damaged scratch database, whose function had been re-created without its
grants. Recorded because it was checked rather than assumed, and because the Phase-6 foundation gate detecting
it is the gate working.

---

## Boundaries observed

No Production contact or mutation · no Phase-4/5/6 capability enabled on any environment · no IAM-v2 cutover ·
no production data migration · no dual read/write · no legacy IAM removal · no real guest, PMS, provider or
financial traffic · no paid access · no per-property financial enablement · no programmatic reversal.
