# StayConnect IAM — Phase 7 Final Report (IN_PROGRESS — NOT ACCEPTED, NOT CLOSED)

<!-- MACHINE ASSERTION - validated by tools/project-state.py -->
<!-- PHASE: 7 -->
<!-- STATUS: IN_PROGRESS -->
<!-- MERGE_STATE: PR_OPEN_UNMERGED -->

> **This report describes work in progress.** Phase 7 is not accepted and not closed, PR #15 is open and
> unmerged by instruction, and nothing in it authorizes a cutover, an enablement, a data migration or any
> real traffic. Production was never contacted.

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

## 2. Current Phase and authorized scope

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

- **A real least-privilege finding, reported and not silenced.** `iam_v2.p5_controlled_operation_open` is
  `SECURITY DEFINER` and granted `EXECUTE` to PUBLIC by accepted migration `0027:124` (Phase 3's
  `p3_controlled_operation_open` before it). PUBLIC has no `USAGE` on schema `iam_v2`, so it confers nothing
  today, and that compensating control is now asserted. Narrowing the grant would amend the schema this phase
  is re-accepting, so it is left for a separately authorized change.
- **The schema is not fully described by its migrations.** `public.edge_executed_commands`,
  `edge_installed_updates` and `edge_offline_packages` are created by `scd` at first use, not by any migration.
  Gate P grants on all three.
- **A database seeded under `session_replication_role = replica` cannot be restored from its own dump.**
  Several Phase-6 gates do exactly that, leaving 21 entitlements violating four validated constraints.
- **M4 is not complete.** The appliance-side items listed in §6 remain open and are not claimed.

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

### 6.2 Open items

The appliance-side M4 work — Hotel Admin and Guest Portal composed behaviour, DEVELOPMENT-appliance
full-system live-dark acceptance, local-first with Central unavailable, runtime-role boundaries exercised on
the appliance, appliance backup/restore evidence, purge and archive within authorization, guaranteed DARK/OFF
restoration and a final real reboot — is **not done and is not claimed**.

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

_(embedded at delivery time)_

---

## 11. All commits created

See the manifest's commit range in §10.

## 12. Branch and PR information

Branch `phase/7-full-system-reacceptance`; PR #15, **open and unmerged by instruction**.

## 13. Remote reachability of HEAD

Verified by pushing the branch and reading the PR head back from the GitHub API.

## 14. Full working-tree status

See §10.
