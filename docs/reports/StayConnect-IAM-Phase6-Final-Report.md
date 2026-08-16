# StayConnect IAM — Phase 6 Final Report (acceptance candidate, UNMERGED)

> Structure required by [../GITHUB_EXECUTION_AND_DELIVERY_RULE.md](../GITHUB_EXECUTION_AND_DELIVERY_RULE.md) §5.
> All Git/GitHub operations in this delivery were performed by the authorized AI Agent
> (`GIT_OPERATIONS_OWNER: AGENT`).
>
> **Phase 6 is NOT accepted, NOT closed and NOT merged.** This report exists so a separate Product-Owner
> decision can be taken on it.

---

## 1. شرح مبسّط بالعامية المصرية

عملنا حاجتين جديدين للنزيل، والاتنين **مقفولين** لسه ومحدش شغّلهم في أي مكان حقيقي.

**الأولى:** النزيل يقدر يشوف الأجهزة اللي هو موصّلها بنفسه، ويشيل الجهاز القديم بتاعه من غير ما يكلم الاستقبال.
مش هيشوف غير أجهزته هو، ومش هيقدر يشيل جهاز حد تاني، ومش هيشوف أي MAC ولا أي رقم داخلي — وكل حاجة بيعملها،
حتى الرفض، بتتسجل. والميزة دي **اختيارية لكل جهاز** والفندق هو اللي بيقرر يفتحها أو لأ، والافتراضي إنها مقفولة.

**التانية:** باقة بالوقت الفعلي على النت — يعني «ساعتين استخدام» مش «ساعتين من ساعة ما اشتريت». الوقت بيتحسب
لما النزيل يكون فعلاً متصل، وبيتقسم على كل أجهزته، وليه كمان تاريخ انتهاء نهائي. لما الرصيد يخلص، الاتصال
بيقف — والنظام بيسجل إمتى بالظبط خلص، من واقع البيانات، مش بالتخمين.

أهم حاجة اتأكدنا منها: **لو قفلنا الميزة، اللي اشتغل قبل كده بيفضل بيتحاسب**. يعني مستحيل حد يكون معاه باقة
محدودة وتتحول لمفتوحة لمجرد إن حد قفل الميزة أو رجّع نسخة قديمة.

جرّبنا كل ده **على جهاز التطوير بس** في جلسة واحدة متحكم فيها، وبعدها رجّعنا كل حاجة مقفولة، وعملنا restart
للجهاز كله واتأكدنا إنه لسه مقفول. **الإنتاج لمسناهوش خالص**، ومفيش أي نزيل حقيقي ولا أي فلوس ولا أي PMS
حقيقي دخل في الموضوع.

## 2. Current Phase and authorized scope

- **Phase:** 6 — Guest Device Self-Service + `AGGREGATE_ONLINE_TIME`
- **Authorized scope:** implementation and a controlled DEVELOPMENT-appliance LIVE-DARK validation, as scoped
  in [`docs/architecture/StayConnect-IAM-Phase6-Plan.md`](../architecture/StayConnect-IAM-Phase6-Plan.md).
- **PO authorization reference:** decision **D25**, transition **T0057** (2026-08-15), with the Product-Owner
  findings of 2026-08-15 on the aggregate window ceiling, the piecewise crossing, the edged capability wiring,
  acquisition gating on every free path, the `svc_acctd` runtime boundary, the provable exhaustion instant,
  fail-closed enforcement for an undatable crossing, suspension evidence, the rollback rehearsal as execution
  evidence, the controlled-validation harness, and current-state documentation.
- **Milestones:** M1, M2 (T0058), M3 (T0059) and M4 (T0060) are complete. Phase 6 remains `IN_PROGRESS`.

## 3. What was implemented

**Guest Device Self-Service** — a guest lists and releases *their own* devices, with the subject derived by
the server from the requesting device's address and never from anything the caller sends. The listing exposes
no MAC and no internal identity: a guest recognises their phone by when it was last seen and whether it is
online. Every outcome, including every refusal, is written to an append-only audit, and releases are throttled
from durable rows rather than from process memory. It is **optional per appliance**, default OFF, and the
setting is read from the local site database on every request — no Central Control Plane call is on the read
path.

**`AGGREGATE_ONLINE_TIME`** — a budget of online seconds shared across an entitlement's devices, inside an
outer hard-validity window. The mode is a property of the **immutable plan revision**, so no existing revision
is ever reinterpreted. Accrual charges only sessions in state `active`, bounded by observation, so unobserved
time is recorded as a skipped interval rather than billed. Exhaustion is the exact piecewise crossing over the
union of billable intervals, and the sweep terminates on the **earliest** terminal condition — window, DATA or
TIME — with the matching reason.

**Two controls that are never collapsed:** the phase deployment gate decides whether the routes exist at all;
the per-appliance product setting decides whether the hotel offers the capability. Neither alone is
sufficient, and the appliance proved it.

**Migrations 0030–0047**, all additive and dark, each with a faithful down migration that states the weakness
it reinstates.

## 4. Practical effect

Nothing behaves differently anywhere today. Every Phase-6 capability is OFF on every environment; the guest
routes are absent from the running process rather than merely refusing; and the Phase-6 operator screens are
compiled out of the deployed Hotel Admin bundle, because `NEXT_PUBLIC_PHASE6_ADMIN` is build-time state.

What is now *possible*, on a decision that has not been taken: a hotel can offer guests self-service device
management per appliance, and can sell time packages measured in actual online time rather than wall-clock.

## 5. Risks and limitations

- **The capability has been exercised on one appliance, once, under supervision.** It has never carried a real
  guest.
- **A full in-place restore of the appliance database is not proven.** The backup was taken with the sanctioned
  script and proven restorable into a scratch database; an in-place restore requires a manifest signed
  off-appliance with the registry root key, which is key custody and was not worked around.
- **The Phase-3 auth arm is a hard prerequisite** for the guest surface, and enabling it requires a
  provisioning grant (`EXECUTE` on `iam_v2.begin_controlled_operation`) that `svc_scd` and `svc_acctd` do not
  hold today. `scd` and `acctd` refuse to start without it, which is correct, and it means enabling Phase 6 is
  not a flag flip alone.
- **`NEXT_PUBLIC_PHASE6_ADMIN` is build-time**, so validating the operator screens needs its own bundle.
- The controlled validation leaves **terminated** business state and its audit trail under a reserved stay on
  the development appliance. Nothing is live and nothing carries access; removing even that residue would mean
  a database restore, which was not performed.

## 6. Acceptance tests

| Gate | Result |
|---|---|
| `phase6_0030_foundation.sh` | **50/50** |
| `phase6_0031_device_self_service.sh` | **22/22** |
| `phase6_0036_aggregate_online_time.sh` | **49/49** |
| `phase6_least_privilege.sh` (as the real roles) | **65/65** |
| `phase6_backup_restore.sh` | **18/18** |
| `phase6_rollback_rehearsal.sh` (0030→0047 down and up) | **65/65**, mutation-proven to fail hard |
| Go tagged integration suite | green on one database |
| Go unit tests, `go vet`, `go build` | green |
| **DEVELOPMENT-appliance controlled validation** | **42 proofs, 0 failures** |
| Harness fault injection (failing body / signal / partial) | restored and dark in all three |
| Post-reboot verification | all six services active, coherence 6/6, routes absent |
| `CURRENT_STATE_PARITY` | **23/23 PASS** |
| `TRANSITION_TIMESTAMPS` | **PASS** |

The rollback rehearsal deserves a note: the earlier runner decided each migration by grepping psql's output
for the word `ERROR`, so a missing file and a database it could not reach both produced no `ERROR` line and
were **counted as passes**. It now decides on the exit status, and is mutation-proven — pointed at a container
that does not exist it reports `pass=2 fail=5` and aborts at the first down migration.

## 7. Production and guest impact

**None.** Production was not contacted and not mutated. No real guest, PMS, provider or financial traffic was
involved at any point. No paid access was created: the only entitlement the validation created is a
zero-price `ADMIN_GRANT` on a reserved stay. No IAM-v2 cutover was performed.

## 8. Rollback status

Every migration `0030`–`0047` has a down migration, rehearsed **65/65** in both directions including the
`0032` boundary the plan records as load-bearing. Each down migration states, in its header, the weakness it
reinstates — including `0047`, whose rollback restores the state in which the guest surface cannot resolve the
device asking it a question.

On the appliance, the previous binaries are one `install` away at `*.bak-*`, and the previous Hotel Admin
release is one symlink away at `hotel-admin.previous`.

**The safe-disable invariant is what makes rollback safe, and it was proven live:** with the aggregate
capability disabled, the already-durable budget is *still* accounted. Accrual is data-driven precisely so that
disabling the capability can never turn finite access into unlimited access.

## 9. Security and isolation results

- The guest subject is **server-derived**. `middleware.RealIP` was removed from this path: with it, a header
  could redirect the subject, and the two-subject regressions fail by *releasing the victim's device* if it is
  reintroduced.
- A device id that is not the caller's own is **indistinguishable** from one that never existed.
- `svc_acctd` holds `EXECUTE` on three controlled writers, `SELECT` on the tables the sweep reads, and **no
  write authority anywhere in `iam_v2`**. It cannot execute `apply_entitlement_transition`,
  `terminate_entitlement_at_boundary` or the device-auth primitives.
- `svc_scd` may resolve a device (`INSERT`, and `UPDATE` on `mac`, `last_seen`, `last_ip` only) and read
  entitlements and plan revisions. It may not delete a device, may not move one between tenants or appliances,
  may not write an entitlement, and may not rewrite an immutable plan revision — all asserted, not assumed.
- The controlled-validation harness refuses any host outside a **compiled-in** allow-list. It is not
  redirectable by an environment variable, because a runner that enables features and can be pointed elsewhere
  by one export is not protected.

## 10. Complete generated changed-file manifest

<!-- embedded at delivery time by tools/embed-report-manifest.py -->

## 11. All commits created

See §10's range. Every commit on `phase/6-device-selfservice-and-time-modes` from the Phase-6 base commit to
the delivery HEAD is part of this delivery; the manifest is generated from that exact range.

## 12. Branch and PR information

- **Branch:** `phase/6-device-selfservice-and-time-modes`
- **Base:** `master`
- **PR:** opened by the Agent, **UNMERGED**, for a separate Product-Owner acceptance decision.
- **Merge gate:** the GitHub Actions **Project Governance** check (job `governance`) must be green; local
  validator output alone is insufficient.

## 13. Remote reachability of HEAD

The delivery HEAD is verified to resolve on the authoritative remote
(`https://github.com/aibrahiiim1/StayConnectEnterprise.git`) as the exact 40-character SHA, read from
`git rev-parse HEAD` and confirmed with `git ls-remote`.

## 14. Full working-tree status

Clean at the delivery commit.

## 15. Documentation and governance synchronization

The Phase-6 plan and the DEVELOPMENT-appliance evidence document were re-synchronized before this report was
written: they had been describing a rollback rehearsal of 54 through 0044, a Hotel Admin bundle "not yet
rebuilt and deployed", and a controlled validation that "has not run" — all three overtaken. The plan also
gained the `0045`/`0046` design it had never described. `ZERO_STALE_LEFTOVERS` and `CURRENT_STATE_PARITY` are
green **after** the synchronization, which is the only order in which either result means anything.

## 16. Project / Evidence Pack paths and checksums

Regenerated deterministically by `tools/project-state.py build-packs` at delivery time; the pack `MANIFEST`
and `PACK_SHA256SUMS` checksums are verified by `tools/validate-project-state.sh` §8/§8b.

## 17. `PROJECT_STATE_GOVERNANCE` result

**PASS** at the delivery commit.

## 18. `ZERO_STALE_LEFTOVERS` result

**PASS** at the delivery commit.

## 19. Remaining blockers

None technical. What remains is a **decision**: Phase 6 is complete as an acceptance candidate and is not
accepted, not closed and not merged.

## 20. Single next proposed action

**Review this pull request and decide.** Acceptance, closure, merge, any Phase-6 enablement on any
environment, IAM-v2 cutover, paid access and Phase 7 all remain unauthorized until that decision is taken.
