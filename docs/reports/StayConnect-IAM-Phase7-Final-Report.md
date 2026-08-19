# StayConnect IAM — Phase 7 Final Report (ACCEPTED AND CLOSED)

<!-- MACHINE ASSERTION - validated by tools/project-state.py -->
<!-- PHASE: 7 -->
<!-- STATUS: ACCEPTED_AND_CLOSED -->
<!-- MERGE_STATE: PR_MERGED -->

> **Phase 7 is ACCEPTED AND CLOSED** at **VERIFIED FULL-SYSTEM LIVE-DARK** maturity by Product-Owner decision
> **D27** (2026-08-17, transition **T0064**) at delivery head `16819aa027633b84486999451e8b689a191a15d2`.
>
> **PR #15 was MERGED** to master on 2026-08-17 under Product-Owner merge decision **D28** (transition
> **T0065**), merge commit `9c57c2b5a29eb886cf317912a9eb6a6da8ccb603`. The merge introduced no content beyond
> the approved head and deployed nothing. Neither the acceptance nor the merge authorizes a cutover, no enablement, no data migration, no dual read/write, no legacy IAM removal,
> no real guest, PMS, provider or financial traffic, no paid access, no per-property financial enablement and no
> programmatic reversal. Production was never contacted.
>
> Phase 7 was the **last numbered development phase**. No Phase 8 exists or is authorized.

---

## 1. شرح مبسّط بالعامية المصرية

الفيز دي مش بتضيف feature جديدة. هي بتثبت إن النظام اللي اتبنى في الفيزات من ٢ لـ ٦ شغال كوحدة واحدة، وإن
اللي إحنا بنقيس بيه صح أصلاً.

أهم حاجة اتعملت: بقى ينفع نعيد بناء قاعدة البيانات من الريبو نفسه — من غير ما ننسخ نسخة الجهاز — ونطلع نفس
الحاجة بالظبط. البصمة الدلالية طلعت `e7216a98…` على الاتنين، وده بيغطي الأعمدة والقيود والفهارس والتريجرات
وجسم الدوال ومالك كل حاجة وصلاحيات كل الأدوار من غير أي استثناءات.

وفي حاجة تانية مهمة: أغلب الأخطاء اللي طلعت في المصفوفة مكانتش أخطاء في المنتج — كانت أخطاء في أدوات
الاختبار نفسها. اتصلحت من أصلها، مش اتلفّ حواليها.

---

## 2. Accepted scope and maturity

Phase 7 is *"Cleanup, final docs/ops manual, full-system re-acceptance"* with the gate *"complete matrix"*
(FINAL contract §18, §19 A–G). It consolidates; it does not reopen accepted phase internals without failing
evidence.

It is **not** `MIGRATION_RUNBOOK.md`'s "Phase 7 — Start edged", which is a deployment step in an unrelated
numbering. That runbook now says so in its own banner, because the coincidence is easy to act on and the
banner additionally described IAM Phase 1A as "not-yet-started", which stopped being true five phases ago.

---

## 3. What was implemented

**The schema can be rebuilt from the repository alone.** `iam_v2_scratch/phase7_reconstruct_from_sources.sh`
applies the accepted history — `0001-0008`, the Phase-1A role model, the MG-0 anchor, `mg1..mg9` as
`iam_v2_owner`, Gate P, the `sc_*` role bootstrap, `0009-0026` as the owner and `0027-0047` as the superuser —
into a fresh private cluster and reaches `e7216a988642c9d5e44ca22478d4972d parts=2686`, identical to the
appliance.

**A semantic fidelity proof** (`phase7_fidelity.sql`) covering columns, constraints with grouping preserved,
indexes, triggers, function bodies/attributes/`proconfig`, object ownership, the complete role-security surface
(`SUPERUSER`, `INHERIT`, `LOGIN`, `BYPASSRLS`, `CREATEROLE`, `CREATEDB`, `REPLICATION`), memberships, and every
grant and function privilege **with no allowlist**.

**A rebuildable gate environment** (`phase7_build_environment.sh`) — the matrix previously ran against a
database nobody could recreate.

**A ledger material-effect proof** (`phase7_ledger_material_effect.sh`) deriving every effect of migrations
`0003`, `0004` and `0006` from the migration files and checking each individually.

**Three composition gates** (M1 identity and acquisition, M2 the stay end to end, M3 the boundaries hold as the
real service roles) and a strict matrix runner with its own 20-case mutation suite.

---

## 4. Practical effect

The repository can now rebuild its own accepted schema and prove the result, rather than depending on a
snapshot of one appliance. The proof is mutation-checked, so a claim of equality is falsifiable.

---

## 5. Risks and limitations

- **A least-privilege finding, tested on the assembled system and preserved as a hardening item.**
  `iam_v2.p5_controlled_operation_open` is `SECURITY DEFINER` and granted `EXECUTE` to PUBLIC by accepted
  migration `0027:124` (Phase 3's `p3_controlled_operation_open` before it). It was **attempted for real on the
  appliance as `svc_scd`**, not reasoned about: the role cannot execute the opener, cannot `INSERT` into
  `controlled_operation_scope`, cannot forge a scope row even with the session token set, and cannot perform
  the guarded write; the predicate answers `false` to it; and PUBLIC holds no `USAGE` on `iam_v2` at all. The
  grant is therefore **inert**. It is preserved as a hardening / cutover-review item — narrowing it would
  amend an accepted migration this phase is re-accepting — and it is **not** a blocker.
- **The schema is not fully described by its migrations.** `public.edge_executed_commands`,
  `edge_installed_updates` and `edge_offline_packages` are created by `scd` at first use, not by any migration.
  Gate P grants on all three.
- **A database seeded under `session_replication_role = replica` cannot be restored from its own dump.**
  Several Phase-6 gates do exactly that, leaving 21 entitlements violating four validated constraints.
- **Three things are NOT PROVEN on the appliance and are not claimed as passes** (§6.2): a deliberate Central
  outage drill, a live rollback of the appliance schema, and a real purge or archive with external receipt
  authority. Each is stated with the reason it cannot lawfully be executed here.

---

## 6. Acceptance tests

Complete matrix, **strict** mode: `gates_run=20 skipped=0 unverdicted_or_crashed=0 pass=1262 fail=0` —
**`PHASE7_FULL_MATRIX = PASS (strict)`**. Strict counts skips, missing gates, crashes and unparsable verdicts
as failures, and the roster names every gate that must run, so "nothing failed" cannot mean "nothing ran".


| Gate | Result |
|---|---|
| phase3 lifecycle (0010) | 366/0 |
| phase4 financial (0011) | 269/0 |
| phase4 db invariants | 33/0 |
| phase4 least privilege (Phase-4 era) | 17/0 |
| phase4 least privilege (complete schema) | 19/0 |
| phase5 foundation (0027) · least privilege | 72/0 · 23/0 |
| phase7 rebuildable gate environment | 11/0 |
| phase6 backup and restore · rollback rehearsal | 19/0 · 65/0 |
| phase6 foundation · device self-service · aggregate online time · least privilege | 50/0 · 22/0 · 49/0 · 65/0 |
| phase7 M1 · M2 · M3 | 20/0 · 22/0 · 34/0 |
| phase7 reconstruction · fidelity mutation suite · ledger material effect | 26/0 · 23/0 · 57/0 |

Supporting suites: matrix-runner mutation suite 20/20; fidelity mutation suite 23/23; governance
`ZERO_STALE_LEFTOVERS = PASS`; `CURRENT_STATE_PARITY 23/0` and its negative suite 57/57;
`TRANSITION_TIMESTAMPS = PASS`.

### 6.1 What the matrix's own failures turned out to be

The first complete strict run reported 24 failures and 8 gates without a verdict. Almost none were product
defects, and each was fixed at its cause:

- the matrix ran against a database nobody could recreate, missing the fixture — six Phase-6 cases failed on
  foreign keys to an appliance and operator that did not exist;
- `pg_restore` failed on a foreign key nobody had touched, because gates seeding under
  `session_replication_role = replica` leave rows violating four validated constraints;
- nine `phase4 financial` failures were `TRANSPORT_HEARTBEAT_STALE` and its cascade — a twenty-minute gate
  outrunning its own fixture heartbeat;
- `phase4 db invariants` never ran at all (`rc=90`, the runner never passed `SCRATCH_PORT_ALLOW`) and was
  first-run-only: fixed UUIDs, idempotency keys and P numbers, every one unique by design;
- M1/M2/M3 collapsed because the matrix passed the database name but not the container;
- the `phase4` gate destroyed the container its two dependants needed, so both reported `SKIPPED`.

### 6.2 The assembled system, on the DEVELOPMENT appliance — **70/0, 3 NOT PROVEN**

`deploy/scripts/phase7-appliance-m4.sh`, against the real services, roles, listeners and schema:

- the DARK baseline captured **before** anything ran, and every restoration claim compared against that
  capture rather than a fresh reading;
- the PUBLIC definer finding attempted as the real role and shown inert (§5);
- runtime-role boundaries as the real roles, including an append-only edit refused;
- the financial core dark and fail-closed: zero postings, zero outbox rows, zero payment transactions, zero
  attempts, folio strategy still defaulting to `UNSET`;
- `scd` does **not mount** its Phase-6 endpoints (404) while the portal returns the uniform non-success that
  reveals nothing — darkness proved at the layer that decides it, not at the edge;
- Hotel Admin serving the exact expected release and sending an unauthenticated caller to login; the edge API
  healthy with its licence Active; and the appliance answering **404 for the Central-only names** rather than
  impersonating Central;
- guest and admin surfaces reading **one** database, agreeing on the same durable row set;
- accounting live (812 records), shaping and enforcement loaded;
- **a real `pg_dump` and `pg_restore`** into a fresh database reproducing the same `iam_v2` table count and
  entitlement count, then removing itself;
- restoration proved: identical counts, identical settings, identical column digest, same services, same
  release.

**NOT PROVEN, counted as neither pass nor failure:** a deliberate Central outage drill (Production Central must
not be disrupted; this appliance is unenrolled, so that outage state is already the running state); a live
rollback of the appliance schema (destructive on the system under acceptance — rehearsed at 65/0 in the
reproducible environment); a real purge or archive (needs an external receipt authority this environment does
not lawfully hold — the fail-closed gate that depends on it is proved instead).

### 6.3 The final real reboot — **24/0**

`deploy/scripts/phase7-final-reboot.sh`. The reboot is the evidence: the kernel boot id changed
(`291095eb…` → `05461c40…`, uptime 45s at first contact), so this is a boot and not a restart.

After it, **with no operator action of any kind**: all six services came back by themselves and the appliance
converged to **serving** 10s after ssh answered; schema, durable state and runtime roles survived exactly; the
Hotel Admin release is the expected DARK one; `scd` still does not mount its Phase-6 endpoints; the
per-appliance setting is still OFF; financial egress is still zero on all three counts; no synthetic session
was started or left open; and the Central-only names are still refused.

An earlier attempt reported five services down and was wrong to: it asserted at 44s of uptime, inside the
machine's normal startup window. It now waits, bounded, for convergence to *serving* rather than *active* —
waiting is measurement, and the script still starts, enables, reloads and fixes nothing.

### 6.4 Acceptance

Accepted at **VERIFIED FULL-SYSTEM LIVE-DARK** by **D27 / T0064** on the evidence above. The three NOT PROVEN
items in §6.2 were **not promoted** by the acceptance, and the `p5_controlled_operation_open` observation in §5
remains a hardening / cutover-review finding — neither a PASS nor a blocker.

The only outstanding item on this branch is the Product Owner's **separate merge decision** on PR #15.

---

## 7. Production and guest impact

None. Production was never contacted. No capability was enabled anywhere. No real guest, PMS, provider or
financial traffic. No paid access.

---

## 8. Rollback status

Every scratch cluster used is disposable and is destroyed by the script that created it; each such script
refuses a container it did not create, after that lifecycle once deleted a shared scratch container mid-run.
The appliance was read only.

---

## 9. Security and isolation results

The fidelity proof's role surface is the security surface: it fails on `BYPASSRLS`, `CREATEROLE`, `CREATEDB` or
`REPLICATION` appearing on any role, on a definer function changing owner or `search_path`, on an unexpected
grantee, and on PUBLIC gaining a function or table privilege — each proved by a separate mutation that must
move that specific digest component.

---

## 10. Complete generated changed-file manifest

> Embedded verbatim from `docs/manifests/Phase7-change-manifest.md` at delivery time. The evidence
> artifact's manifest-parity check confirms this equals the standalone generated manifest.

# Changed-file manifest (generated - do not hand-edit)

- **Base commit:** `9cb25b8afc6a4753d75148455c577228c0fbd67a`
- **HEAD commit:** `1048d68097f6024e47ef211500acfb2d7710278a`
- **Provenance (generation HEAD = inventory_head):** `1048d68097f6024e47ef211500acfb2d7710278a`  ·  path/status set covers the complete `base..delivery_head` diff (delivery_head = this staged content once committed).
- **Branch:** `post-roadmap/dev-iamv2-trial`
- **Remote branch:** `(no upstream)`
- **Changed files:** 73
- **Generated by:** `tools/generate-change-manifest.py 9cb25b8afc6a..STAGED`

## Files

| Path | Classification | Git status | Domain | Workstream | Rollback | Purpose (last commit subject in range) |
|---|---|---|---|---|---|---|
| `data-plane/cmd/edged/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Clean IAM-v2 replacement (D30/T0067): wire the repositories the accepted phases already built |
| `data-plane/cmd/scd/authsec.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Clean IAM-v2 replacement (D30/T0067): wire the repositories the accepted phases already built |
| `deploy/env/phase6-guest-dependency.md` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Correct false merge attribution via structured lifecycle, and make the Phase-6 guest dependency fail closed |
| `deploy/gatep/gatep-grants.sql` | MODIFIED | `M` | configuration | DEPLOY | rollback RESTORES prior content | D31/T0068: correct D30 scope, and make the edged writer grant reproducible |
| `deploy/scripts/check-phase6-guest-dependency.sh` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Correct false merge attribution via structured lifecycle, and make the Phase-6 guest dependency fail closed |
| `deploy/scripts/phase7-appliance-m4.sh` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Phase 7 M4: the assembled system on the DEVELOPMENT appliance (70/0, 3 NOT PROVEN) |
| `deploy/scripts/phase7-final-reboot.sh` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Phase 7 M4: the final real reboot, and what survived it (24/0) |
| `deploy/scripts/phase7-reboot-drill.sh` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Phase 7 M3/M4: the reboot drill the Phase-6 harness could not perform, a matrix runner, and two defects in my own tools |
| `docs/BACKUP_AND_RESTORE.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 7: document the scd bootstrap tables and the appliance verification procedure |
| `docs/MIGRATION_RUNBOOK.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 7 M4: a rebuildable gate environment, and the failures it explained |
| `docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 7: document the scd bootstrap tables and the appliance verification procedure |
| `docs/acceptance/StayConnect-IAM-Phase7-Progress-Evidence.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Post-merge cleanup, and D29/T0066 authorizing the DEVELOPMENT IAM-v2 trial |
| `docs/architecture/Phase4-Financial-Schema-Gap-Audit.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 7: the remaining action is the Product-Owner acceptance decision |
| `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | D31/T0068: correct D30 scope, and make the edged writer grant reproducible |
| `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | D31/T0068: correct D30 scope, and make the edged writer grant reproducible |
| `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | D31/T0068: correct D30 scope, and make the edged writer grant reproducible |
| `docs/architecture/StayConnect-IAM-Phase7-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Post-merge cleanup, and D29/T0066 authorizing the DEVELOPMENT IAM-v2 trial |
| `docs/context/StayConnect-IAM-Handoff.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | D31/T0068: correct D30 scope, and make the edged writer grant reproducible |
| `docs/manifests/Phase7-change-manifest.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Attribution correction (delivery_head): manifest, packs and checksums |
| `docs/reports/StayConnect-IAM-Phase7-Final-Report.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Attribution correction (delivery_head): manifest, packs and checksums |
| `exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Attribution correction (delivery_head): manifest, packs and checksums |
| `exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Attribution correction (delivery_head): manifest, packs and checksums |
| `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Attribution correction (delivery_head): manifest, packs and checksums |
| `exports/chatgpt/phase-evidence/GIT_STAT_1048d68.txt` | EXPORTED | `A` | export | EXPORT | rollback REMOVES it | (no commit subject in range) |
| `exports/chatgpt/phase-evidence/GIT_STAT_7b5b6b9.txt` | EXPORTED | `D` | export | EXPORT | rollback RESTORES it | Phase 7 IN_PROGRESS (T0063): M1-M3 complete and mutation-checked, M4 open, matrix not yet green |
| `exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Attribution correction (delivery_head): manifest, packs and checksums |
| `exports/chatgpt/phase-evidence/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Attribution correction (delivery_head): manifest, packs and checksums |
| `exports/chatgpt/phase-evidence/governance/decision-register.json` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Post-fef8 correction (delivery_head): regenerated manifest, packs and checksums |
| `exports/chatgpt/phase-evidence/tools/validate-project-state.sh` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Post-fef8 correction (delivery_head): regenerated manifest, packs and checksums |
| `exports/chatgpt/phase1b-planning/MANIFEST.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Attribution correction (delivery_head): manifest, packs and checksums |
| `exports/chatgpt/phase1b-planning/PACK_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Attribution correction (delivery_head): manifest, packs and checksums |
| `exports/chatgpt/phase1b-planning/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Attribution correction (delivery_head): manifest, packs and checksums |
| `exports/chatgpt/phase1b-planning/StayConnect-IAM-Phase1B-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Post-fef8 correction (delivery_head): regenerated manifest, packs and checksums |
| `exports/chatgpt/stayconnectenterprise/00-START-HERE.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | D31/T0068: correct D30 scope, and make the edged writer grant reproducible |
| `exports/chatgpt/stayconnectenterprise/MANIFEST.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Attribution correction (delivery_head): manifest, packs and checksums |
| `exports/chatgpt/stayconnectenterprise/MIGRATION_RUNBOOK.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 7 (delivery_head): complete staged manifest + rebuilt packs + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/PROJECT-INSTRUCTIONS.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | D31/T0068: correct D30 scope, and make the edged writer grant reproducible |
| `exports/chatgpt/stayconnectenterprise/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 7: state the next action in the project's canonical vocabulary |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Handoff.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Post-fef8 correction (delivery_head): regenerated manifest, packs and checksums |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase0-Contract.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Post-fef8 correction (delivery_head): regenerated manifest, packs and checksums |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase1A-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Post-fef8 correction (delivery_head): regenerated manifest, packs and checksums |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase1B-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Post-fef8 correction (delivery_head): regenerated manifest, packs and checksums |
| `governance/decision-register.json` | MODIFIED | `M` | governance | GOVERNANCE | rollback RESTORES prior content | D31/T0068: correct D30 scope, and make the edged writer grant reproducible |
| `governance/project-state.json` | MODIFIED | `M` | governance | GOVERNANCE | rollback RESTORES prior content | Correct false merge attribution via structured lifecycle, and make the Phase-6 guest dependency fail closed |
| `governance/transitions/T0063.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 7 IN_PROGRESS (T0063): M1-M3 complete and mutation-checked, M4 open, matrix not yet green |
| `governance/transitions/T0064.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 7 ACCEPTED AND CLOSED at VERIFIED FULL-SYSTEM LIVE-DARK (D27/T0064) |
| `governance/transitions/T0065.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Post-merge cleanup, and D29/T0066 authorizing the DEVELOPMENT IAM-v2 trial |
| `governance/transitions/T0066.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Transition receipts: stamp each before the commit that introduced it |
| `governance/transitions/T0067.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Transition receipts: stamp each before the commit that introduced it |
| `governance/transitions/T0068.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Transition receipts: stamp each before the commit that introduced it |
| `hotel-admin/e2e/phase4-financial-operator.spec.ts` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 7: make the Phase-4 operator e2e deterministic at its locator |
| `iam_v2_scratch/accepted/appliance-schema-20260816.sql` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M4: the fidelity proof was too weak, and it was hiding an approximate hybrid |
| `iam_v2_scratch/phase3_0010_lifecycle.sh` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 7 M4: a gate that destroys its container must refuse one it does not own |
| `iam_v2_scratch/phase4_0011_financial.sh` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 7 M4: a rebuildable gate environment, and the failures it explained |
| `iam_v2_scratch/phase4_db_invariants.sh` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 7: guard interface freshness on the ROW, not the table |
| `iam_v2_scratch/phase4_least_privilege.sh` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 7: make the Phase-4 gates era-aware instead of era-blind |
| `iam_v2_scratch/phase6_backup_restore.sh` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 7 M4: a rebuildable gate environment, and the failures it explained |
| `iam_v2_scratch/phase7_build_environment.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M4: a rebuildable gate environment, and the failures it explained |
| `iam_v2_scratch/phase7_build_full_scratch.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M4: role attributes are fidelity too, and the red matrix's Phase-6 failures were never product defects |
| `iam_v2_scratch/phase7_fidelity.sql` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M4: isolate the reconstruction cluster and stop swallowing its errors |
| `iam_v2_scratch/phase7_fidelity_selftest.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M4: complete the fidelity mutation proof (23/23, reason-checked) |
| `iam_v2_scratch/phase7_fixture.sql` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M4: the fidelity proof was too weak, and it was hiding an approximate hybrid |
| `iam_v2_scratch/phase7_full_matrix.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7: address the gates by container, not by database name alone |
| `iam_v2_scratch/phase7_ledger_material_effect.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M4: revalidate the ledger backfill by complete material effect |
| `iam_v2_scratch/phase7_m1_identity_and_acquisition.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M4: role attributes are fidelity too, and the red matrix's Phase-6 failures were never product defects |
| `iam_v2_scratch/phase7_m2_the_stay_end_to_end.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M4: role attributes are fidelity too, and the red matrix's Phase-6 failures were never product defects |
| `iam_v2_scratch/phase7_m3_boundaries.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M3: prove the product-setting audit instead of scoring it NOT PROVEN |
| `iam_v2_scratch/phase7_matrix_selftest.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7: close the Phase-4 container lifecycle defect and complete the matrix roster |
| `iam_v2_scratch/phase7_reconstruct_from_sources.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 7 M4: a rebuildable gate environment, and the failures it explained |
| `tools/tests/current_state_parity/run_negative.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Post-merge cleanup, and D29/T0066 authorizing the DEVELOPMENT IAM-v2 trial |
| `tools/tests/project_state_validator/run_mutations.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 7 closure: repair the mutation fixture anchor my reword drifted |
| `tools/validate-current-state-parity.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Correct false merge attribution via structured lifecycle, and make the Phase-6 guest dependency fail closed |
| `tools/validate-project-state.sh` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | D31/T0068: correct D30 scope, and make the edged writer grant reproducible |

## Total diff statistics (`git diff --stat`)
```text
 data-plane/cmd/edged/main.go                       |     9 +-
 data-plane/cmd/scd/authsec.go                      |    26 +-
 deploy/env/phase6-guest-dependency.md              |    31 +
 deploy/gatep/gatep-grants.sql                      |    17 +
 deploy/scripts/check-phase6-guest-dependency.sh    |    21 +
 deploy/scripts/phase7-appliance-m4.sh              |   303 +
 deploy/scripts/phase7-final-reboot.sh              |   142 +
 deploy/scripts/phase7-reboot-drill.sh              |   109 +
 docs/BACKUP_AND_RESTORE.md                         |    27 +
 docs/MIGRATION_RUNBOOK.md                          |    17 +-
 docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md     |    26 +
 .../StayConnect-IAM-Phase7-Progress-Evidence.md    |   145 +
 .../Phase4-Financial-Schema-Gap-Audit.md           |     6 +
 .../StayConnect-IAM-Phase0-Contract.md             |    10 +-
 docs/architecture/StayConnect-IAM-Phase1A-Plan.md  |    10 +-
 docs/architecture/StayConnect-IAM-Phase1B-Plan.md  |    10 +-
 docs/architecture/StayConnect-IAM-Phase7-Plan.md   |    10 +-
 docs/context/StayConnect-IAM-Handoff.md            |    12 +-
 docs/manifests/Phase7-change-manifest.md           |   251 +
 .../reports/StayConnect-IAM-Phase7-Final-Report.md |   488 +
 .../StayConnectEnterprise-ChatGPT-Project-Pack.zip |   Bin 312530 -> 313278 bytes
 .../StayConnectEnterprise-Phase-Evidence-Pack.zip  |   Bin 118948 -> 123191 bytes
 ...StayConnectEnterprise-Phase1B-Planning-Pack.zip |   Bin 42195 -> 42147 bytes
 .../chatgpt/phase-evidence/GIT_STAT_1048d68.txt    |     4 +
 .../chatgpt/phase-evidence/GIT_STAT_7b5b6b9.txt    |     4 -
 exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt |     8 +-
 .../REPOSITORY_ARTIFACT_SHA256SUMS.txt             |     6 +-
 .../governance/decision-register.json              |    35 +
 .../phase-evidence/tools/validate-project-state.sh |     2 +-
 exports/chatgpt/phase1b-planning/MANIFEST.md       |     2 +-
 .../chatgpt/phase1b-planning/PACK_SHA256SUMS.txt   |     6 +-
 .../REPOSITORY_ARTIFACT_SHA256SUMS.txt             |     6 +-
 .../StayConnect-IAM-Phase1B-Plan.md                |    10 +-
 .../chatgpt/stayconnectenterprise/00-START-HERE.md |    10 +-
 exports/chatgpt/stayconnectenterprise/MANIFEST.md  |    68 +-
 .../stayconnectenterprise/MIGRATION_RUNBOOK.md     |    17 +-
 .../stayconnectenterprise/PROJECT-INSTRUCTIONS.md  |    10 +-
 .../STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md      |    26 +
 .../StayConnect-IAM-Handoff.md                     |    12 +-
 .../StayConnect-IAM-Phase0-Contract.md             |    10 +-
 .../StayConnect-IAM-Phase1A-Plan.md                |    10 +-
 .../StayConnect-IAM-Phase1B-Plan.md                |    10 +-
 governance/decision-register.json                  |    35 +
 governance/project-state.json                      |   152 +-
 governance/transitions/T0063.json                  |    45 +
 governance/transitions/T0064.json                  |   120 +
 governance/transitions/T0065.json                  |    80 +
 governance/transitions/T0066.json                  |    88 +
 governance/transitions/T0067.json                  |    38 +
 governance/transitions/T0068.json                  |    62 +
 hotel-admin/e2e/phase4-financial-operator.spec.ts  |    20 +-
 .../accepted/appliance-schema-20260816.sql         | 15060 +++++++++++++++++++
 iam_v2_scratch/phase3_0010_lifecycle.sh            |    19 +-
 iam_v2_scratch/phase4_0011_financial.sh            |    57 +-
 iam_v2_scratch/phase4_db_invariants.sh             |   120 +-
 iam_v2_scratch/phase4_least_privilege.sh           |    78 +-
 iam_v2_scratch/phase6_backup_restore.sh            |    54 +
 iam_v2_scratch/phase7_build_environment.sh         |   108 +
 iam_v2_scratch/phase7_build_full_scratch.sh        |   123 +
 iam_v2_scratch/phase7_fidelity.sql                 |   191 +
 iam_v2_scratch/phase7_fidelity_selftest.sh         |   278 +
 iam_v2_scratch/phase7_fixture.sql                  |    61 +
 iam_v2_scratch/phase7_full_matrix.sh               |   270 +
 iam_v2_scratch/phase7_ledger_material_effect.sh    |   158 +
 .../phase7_m1_identity_and_acquisition.sh          |   275 +
 iam_v2_scratch/phase7_m2_the_stay_end_to_end.sh    |   220 +
 iam_v2_scratch/phase7_m3_boundaries.sh             |   192 +
 iam_v2_scratch/phase7_matrix_selftest.sh           |   137 +
 iam_v2_scratch/phase7_reconstruct_from_sources.sh  |   407 +
 tools/tests/current_state_parity/run_negative.py   |   172 +
 .../tests/project_state_validator/run_mutations.py |    20 +-
 tools/validate-current-state-parity.py             |   438 +
 tools/validate-project-state.sh                    |     2 +-
 73 files changed, 20813 insertions(+), 193 deletions(-)
```

## Working-tree status (`git status --short --untracked-files=all`)
```text
M  exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip
M  exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip
M  exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip
A  exports/chatgpt/phase-evidence/GIT_STAT_1048d68.txt
D  exports/chatgpt/phase-evidence/GIT_STAT_55369df.txt
M  exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt
M  exports/chatgpt/phase-evidence/REPOSITORY_ARTIFACT_SHA256SUMS.txt
M  exports/chatgpt/phase1b-planning/MANIFEST.md
M  exports/chatgpt/phase1b-planning/PACK_SHA256SUMS.txt
M  exports/chatgpt/phase1b-planning/REPOSITORY_ARTIFACT_SHA256SUMS.txt
M  exports/chatgpt/stayconnectenterprise/MANIFEST.md
M  governance/project-state.json
```

## Commits in range (`git log --oneline <base>..HEAD`)
```text
HISTORICAL: 1048d68 Correct false merge attribution via structured lifecycle, and make the Phase-6 guest dependency fail closed
HISTORICAL: a8d2052 Attribution correction (delivery_head): manifest, packs and checksums
HISTORICAL: 55369df Correct the D31/T0067 attribution, and make that class fail Governance
HISTORICAL: 79558c4 DEVELOPMENT trial round (delivery_head): manifest, packs and checksums
HISTORICAL: 38890f5 Environment-scope the IAM state, correct merge attribution, fix the PR metadata
HISTORICAL: e98edf1 Post-fef8 correction (delivery_head): manifest, packs and checksums
HISTORICAL: 40340a8 Transition receipts: stamp each before the commit that introduced it
HISTORICAL: ba79acd Post-fef8 correction (delivery_head): manifest, packs and checksums
HISTORICAL: 25ed4ee T0066: stamp the receipt after the trial attempt it records
HISTORICAL: 6a85eb6 Post-fef8 correction (delivery_head): manifest, packs and checksums
HISTORICAL: ddf1142 T0067: stamp the receipt after the work it records
HISTORICAL: 26fa705 Post-fef8 correction (delivery_head): regenerated manifest, packs and checksums
HISTORICAL: 39a322a D31/T0068: correct D30 scope, and make the edged writer grant reproducible
HISTORICAL: fef8af9 Clean IAM-v2 replacement (D30/T0067): wire the repositories the accepted phases already built
HISTORICAL: d0e3904 Trial-blocker round (delivery_head): manifest, packs and checksums
HISTORICAL: 2e6cb28 DEVELOPMENT IAM-v2 trial: blocked at the cutover boundary, appliance restored
HISTORICAL: 8d18424 Post-merge cleanup (delivery_head): manifest, packs and checksums
HISTORICAL: 9ecf961 Post-merge cleanup, and D29/T0066 authorizing the DEVELOPMENT IAM-v2 trial
HISTORICAL: 2418734 Phase 7 post-merge (delivery_head): manifest, packs and checksums
HISTORICAL: 4b68828 Phase 7 merge receipt: stamp T0065 at the time it was actually recorded
HISTORICAL: 17f805f Phase 7 post-merge: manifest, packs and checksums at the merge-receipt head
HISTORICAL: ad1ac8b Phase 7 merge receipt: PR #15 MERGED to master (D28/T0065)
HISTORICAL: 9c57c2b Phase 7 â€” full-system re-acceptance (#15)
HISTORICAL: 47e6502 Phase 7 (micro-correction delivery_head): manifest, packs and checksums
HISTORICAL: c644777 Phase 7 closure: a third copy of the stale sentence, and the rule that skipped it
HISTORICAL: b6cc983 Phase 7 (micro-correction delivery_head): re-rendered blocks, manifest, packs and checksums
HISTORICAL: cceec21 Phase 7 closure: two current-state statements frozen at the moment they were written
HISTORICAL: 73c17fd Phase 7 (closure-correction delivery_head): manifest, packs and checksums
HISTORICAL: 9a5c39f Phase 7 closure: repair the mutation fixture anchor my reword drifted
HISTORICAL: 19ae593 Phase 7 (closure-correction delivery_head): re-rendered blocks, manifest, packs and checksums
HISTORICAL: 7286e7e Phase 7 closure: reconcile current state with D27/T0064, and make the gate catch this class
HISTORICAL: aa8ecbb Phase 7 (closure delivery_head): acceptance manifest, packs and checksums
HISTORICAL: 129f60d Phase 7 closure: three current-state contradictions the gates caught
HISTORICAL: b6ee9b2 Phase 7 (closure delivery_head): acceptance manifest, packs and checksums
HISTORICAL: 29a899b Phase 7 ACCEPTED AND CLOSED at VERIFIED FULL-SYSTEM LIVE-DARK (D27/T0064)
HISTORICAL: 16819aa Phase 7 (delivery_head): appliance M4, real reboot, complete manifest and packs
HISTORICAL: a1b8b95 Phase 7: state the next action in the project's canonical vocabulary
HISTORICAL: f9ba606 Phase 7: the remaining action is the Product-Owner acceptance decision
HISTORICAL: 006aed0 Phase 7: record the appliance M4 and the real reboot in the report and evidence
HISTORICAL: 71bbe71 Phase 7: document the scd bootstrap tables and the appliance verification procedure
HISTORICAL: bd6c4ce Phase 7 M4: the final real reboot, and what survived it (24/0)
HISTORICAL: e88409c Phase 7 M4: the assembled system on the DEVELOPMENT appliance (70/0, 3 NOT PROVEN)
HISTORICAL: 603937b Phase 7: say the current truth on the current-state surfaces
HISTORICAL: 8d5c7b4 Phase 7: make the Phase-4 operator e2e deterministic at its locator
HISTORICAL: eead735 Phase 7 (delivery_head): complete staged manifest + rebuilt packs + report-embedded manifest
HISTORICAL: 8cd563f Phase 7: final report at the strict-green matrix
HISTORICAL: 287bd80 Phase 7: guard interface freshness on the ROW, not the table
HISTORICAL: 2afb51d Phase 7: make the Phase-4 gates era-aware instead of era-blind
HISTORICAL: 70d0e86 Phase 7: address the gates by container, not by database name alone
HISTORICAL: b5db519 Phase 7: make the Phase-4 invariants gate repeatable instead of first-run-only
HISTORICAL: a202b45 Phase 7 M4: a rebuildable gate environment, and the failures it explained
HISTORICAL: 78ecb6e Phase 7: close the Phase-4 container lifecycle defect and complete the matrix roster
HISTORICAL: e5946fb Phase 7 M4: revalidate the ledger backfill by complete material effect
HISTORICAL: f5069ca Phase 7 M3: prove the product-setting audit instead of scoring it NOT PROVEN
HISTORICAL: 214a31b Phase 7 M4: complete the fidelity mutation proof (23/23, reason-checked)
HISTORICAL: 5bbaaee Phase 7 M4: isolate the reconstruction cluster and stop swallowing its errors
HISTORICAL: 3460651 Phase 7 M4: the reconstruction reaches the appliance exactly, by reproducing history rather than patching symptoms
HISTORICAL: 4859cfd Phase 7 M4: reconstruct from repository sources, and state the fidelity claim at its true scope
HISTORICAL: eeaa667 Phase 7 M4: the provenance contradiction closed -- and two of my findings were wrong
HISTORICAL: 0c80af9 Phase 7 M4: role attributes are fidelity too, and the red matrix's Phase-6 failures were never product defects
HISTORICAL: daceb1f Phase 7 M4: the fidelity proof was too weak, and it was hiding an approximate hybrid
HISTORICAL: 8df5258 Phase 7 M4: the authoritative Phase-7 manifest, and M46/M48 restored as effective negative tests
HISTORICAL: c1fd3d5 Phase 7 M4: a gate that destroys its container must refuse one it does not own
HISTORICAL: 0f7288a Phase 7 M4: self-building gates must not be gated on a container they create themselves
HISTORICAL: 6c72865 Phase 7 M4: self-building gates must not be gated on a container they create themselves
HISTORICAL: b6e78a1 Phase 7 M4: a reproducible complete scratch database, proven identical to the appliance -- and a ledger that under-reported reality
HISTORICAL: c30ebf5 Phase 7 M4: the matrix runner gets a strict mode, and a mutation suite that proves each refusal
HISTORICAL: d87b243 Phase 7 IN_PROGRESS (T0063): M1-M3 complete and mutation-checked, M4 open, matrix not yet green
HISTORICAL: 779ac73 Phase 7 M3/M4: the reboot drill the Phase-6 harness could not perform, a matrix runner, and two defects in my own tools
HISTORICAL: 49f751a Phase 7 M3: the boundaries hold -- and a third vacuous assertion, this time from an empty table
HISTORICAL: d33145d Phase 7 M2: the stay end to end, and two more assertions that were passing without measuring anything
HISTORICAL: 62889ae Phase 7 M1: identity and acquisition, composed -- and a green assertion that proved nothing
```
## 11. All commits created

See the manifest's commit range in §10.

## 12. Branch and PR information

Branch `phase/7-full-system-reacceptance` (deleted after the merge, as merged phase branches are); PR #15
**merged and closed** under D28/T0065, merge commit `9c57c2b5a29eb886cf317912a9eb6a6da8ccb603`.

## 13. Remote reachability of HEAD

Verified by pushing the branch and reading the PR head back from the GitHub API.

## 14. Full working-tree status

See §10.
