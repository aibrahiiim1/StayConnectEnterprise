# Phase 7 — full-system re-acceptance: progress evidence

**Phase 7 is `ACCEPTED_AND_CLOSED`** at **VERIFIED FULL-SYSTEM LIVE-DARK** maturity — Product-Owner decision
**D27** (2026-08-17), transition **T0064**, delivery head `16819aa027633b84486999451e8b689a191a15d2`. PR #15 remains **open and
unmerged** pending a separate merge decision. Originally authorized by **D26** (2026-08-16).
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

### M3 — the boundaries hold · **34/0**

`iam_v2_scratch/phase7_m3_boundaries.sh`, every privilege measured **as the real service role**. Financial
DARK expressed as privilege rather than as a flag; `E4b`'s `UNSET` confirmed as both a real value and the
default; no runtime role able to write an entitlement or session, delete an accounting record or device,
execute the boundary termination or apply an arbitrary transition; append-only checked by *attempting* the
write; the mixed-version catalog probe's premise verified; all 47 migrations confirmed to have a down
migration, counted from the filesystem.

**Mutation-checked.** Granting `svc_acctd` UPDATE on entitlements fails exactly that assertion; revoking it
returns the gate to green.

The gate was 31 cases when first recorded. It is **34/0** now, and the three added cases are the reason the
earlier number should not be quoted: one line used to read `ok "NOT PROVEN here: ..."`, incrementing the pass
count while its own text said nothing had been proved. The gate now seeds its own tenant, site, appliance and
operator — it promises to be fixture-free, and the scratch database has no such rows — seeds a real
product-setting change row, proves the row exists, and only then attempts the edit that must be refused.

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

**The complete cross-phase matrix is now GREEN, in strict mode.** This section previously recorded that it had
never run to completion; that is no longer true and the older text is not preserved as if it were.

```
PHASE7_FULL_MATRIX = PASS (strict)
gates_run=20  skipped=0  unverdicted_or_crashed=0  pass=1262  fail=0
```

Strict mode counts skips, missing gates, crashes and unparsable verdicts as failures, and the roster names
every gate that must run — so "nothing failed" cannot quietly mean "nothing ran".

It took five complete runs to get there. The first reported 24 failures and 8 gates with no verdict, and
**almost none of them were product defects**; each was fixed at its cause rather than tolerated:

- the matrix ran against a scratch database nobody could recreate, missing the deterministic fixture entirely,
  so six Phase-6 cases failed on foreign keys to an appliance and an operator that did not exist. The gate
  environment is now built from repository sources by `phase7_build_environment.sh`, proved equal to the
  appliance before anything runs against it, and is itself a roster gate;
- `pg_restore` failed on a foreign key nobody had touched: several Phase-6 gates seed under
  `session_replication_role = replica`, leaving 21 entitlements that violate four validated constraints, so
  that database cannot be restored from its own dump. That is a property of the DATA, and the gate now says so
  before dumping;
- nine Phase-4 failures were `TRANSPORT_HEARTBEAT_STALE` and its cascade — a twenty-minute gate outrunning its
  own fixture heartbeat;
- `phase4 db invariants` never ran at all (`rc=90`: the runner never passed `SCRATCH_PORT_ALLOW`) and was
  first-run-only, because its fixed UUIDs, idempotency keys and P numbers are each unique by design;
- M1/M2/M3 collapsed because the runner passed a database name but not a container.

**And the schema can be rebuilt from the repository alone.** `phase7_reconstruct_from_sources.sh` applies the
accepted history into a fresh private cluster and reaches semantic digest
`e7216a988642c9d5e44ca22478d4972d parts=2686` — identical to the DEVELOPMENT appliance across columns,
constraints with grouping preserved, indexes, triggers, function bodies and configuration, object ownership,
the complete role-security surface, memberships, and every grant and function privilege with no allowlist.

**Appliance-side M4 has now been run.** `deploy/scripts/phase7-appliance-m4.sh` is **70/0 with 3 NOT PROVEN**
against the real services, roles, listeners and schema: the DARK baseline captured before anything ran and
proved unchanged afterwards; the PUBLIC-executable definer finding attempted as the real `svc_scd` role and
shown inert; runtime-role boundaries; the financial core dark with zero postings, outbox rows, payments and
attempts; `scd` not mounting its Phase-6 endpoints at all while the portal returns the uniform non-success;
Hotel Admin on the expected release and closed to the unauthenticated; the appliance refusing the Central-only
names; guest and admin over one database; accounting, shaping and enforcement live; and a real
`pg_dump`/`pg_restore` into a fresh database that reproduces the same table and row counts and then removes
itself.

The three NOT PROVEN lines are counted as neither pass nor failure: a deliberate Central outage drill, a live
rollback of the appliance schema, and a real purge/archive with external receipt authority.

**The final reboot was real.** `deploy/scripts/phase7-final-reboot.sh` is **24/0**, and the kernel boot id
changed (`291095eb…` → `05461c40…`) because a service restart is not reboot evidence. Everything came back
with no operator action, and everything that was dark is still dark.

Phase 7 was subsequently **ACCEPTED AND CLOSED** on this evidence (D27 / T0064). The three NOT PROVEN items
were **not promoted** by that acceptance.

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
