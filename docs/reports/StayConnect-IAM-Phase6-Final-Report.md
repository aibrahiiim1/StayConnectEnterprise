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

> Embedded verbatim from `docs/manifests/Phase6-change-manifest.md` at delivery time. The evidence
> artifact's manifest-parity check confirms this equals the standalone generated manifest.

# Changed-file manifest (generated - do not hand-edit)

- **Base commit:** `09e67156fb6cb286fe47fe632a368a3c4e4c6d23`
- **HEAD commit:** `22106eac47e37d14e5892115a295f4cd7b16e295`
- **Provenance (generation HEAD = inventory_head):** `22106eac47e37d14e5892115a295f4cd7b16e295`  ·  path/status set covers the complete `base..delivery_head` diff (delivery_head = this staged content once committed).
- **Branch:** `phase/6-device-selfservice-and-time-modes`
- **Remote branch:** `origin/phase/6-device-selfservice-and-time-modes`
- **Changed files:** 160
- **Generated by:** `tools/generate-change-manifest.py 09e67156fb6c..STAGED`

## Files

| Path | Classification | Git status | Domain | Workstream | Rollback | Purpose (last commit subject in range) |
|---|---|---|---|---|---|---|
| `data-plane/cmd/acctd/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/cmd/acctd/phase3.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3/M4: accrual is data-driven, guest sees both clocks, and the disable path cannot create unlimited access |
| `data-plane/cmd/acctd/phase3_boundary_integration_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | Phase 6 M3: wire the accrual tick into the one expiry sweep, and fix the fixtures 0032 caught |
| `data-plane/cmd/acctd/phase3_ingest_integration_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | Phase 6 M3: wire the accrual tick into the one expiry sweep, and fix the fixtures 0032 caught |
| `data-plane/cmd/acctd/phase6_accounting_owner.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/cmd/acctd/phase6_accounting_owner_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/cmd/edged/auth.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `data-plane/cmd/edged/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3: load the Phase-6 config before anything consumes it, and fail closed on NEW aggregate acquisition |
| `data-plane/cmd/edged/phase6_capability_wiring.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M3: load the Phase-6 config before anything consumes it, and fail closed on NEW aggregate acquisition |
| `data-plane/cmd/edged/phase6_capability_wiring_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M3: load the Phase-6 config before anything consumes it, and fail closed on NEW aggregate acquisition |
| `data-plane/cmd/edged/phase6_role_matrix_doc_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `data-plane/cmd/edged/phase6_setting_api_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M2: the HTTP surfaces, with the identity boundaries as the load-bearing part |
| `data-plane/cmd/edged/phase6_setting_rbac_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6: the portal source-identity boundary and real Hotel-Admin RBAC (two load-bearing HTTP findings) |
| `data-plane/cmd/edged/resources_phase6_aggregate.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M3: operator view of online-time budgets, and the M4 flag-coherence check |
| `data-plane/cmd/edged/resources_phase6_setting.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `data-plane/cmd/edged/resources_sessions.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3: operator view of online-time budgets, and the M4 flag-coherence check |
| `data-plane/cmd/portald/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6: the portal source-identity boundary and real Hotel-Admin RBAC (two load-bearing HTTP findings) |
| `data-plane/cmd/portald/phase6_device_handlers.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M2: the HTTP surfaces, with the identity boundaries as the load-bearing part |
| `data-plane/cmd/portald/phase6_guest_presentation_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M3/M4: accrual is data-driven, guest sees both clocks, and the disable path cannot create unlimited access |
| `data-plane/cmd/portald/phase6_public_authority_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `data-plane/cmd/portald/phase6_source_identity_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6: the portal source-identity boundary and real Hotel-Admin RBAC (two load-bearing HTTP findings) |
| `data-plane/cmd/portald/templates.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3/M4: accrual is data-driven, guest sees both clocks, and the disable path cannot create unlimited access |
| `data-plane/cmd/scd-enroll-test/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | gofmt: a missing blank line of mine, and an import block that predates this branch |
| `data-plane/cmd/scd/authsec.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `data-plane/cmd/scd/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3/M4: accrual is data-driven, guest sees both clocks, and the disable path cannot create unlimited access |
| `data-plane/cmd/scd/phase3_activation_integration_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/cmd/scd/phase3_auth.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3: load the Phase-6 config before anything consumes it, and fail closed on NEW aggregate acquisition |
| `data-plane/cmd/scd/phase3_auth_integration_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/cmd/scd/phase6_device.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M2: the HTTP surfaces, with the identity boundaries as the load-bearing part |
| `data-plane/cmd/scd/phase6_device_api_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M2: the HTTP surfaces, with the identity boundaries as the load-bearing part |
| `data-plane/cmd/scd/phase6_e2e_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/cmd/scd/phase6_localfirst_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `data-plane/cmd/scd/phase6_remaining.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M3/M4: accrual is data-driven, guest sees both clocks, and the disable path cannot create unlimited access |
| `data-plane/internal/deviceselfservice/deviceselfservice.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `data-plane/internal/deviceselfservice/deviceselfservice_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `data-plane/internal/enforce/enforce.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M4: undatable is not a licence to keep browsing -- over-budget access fails closed |
| `data-plane/internal/enforce/phase6_aggregate_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M4: suspension evidence tells the truth one level down, and the rehearsal fails where it used to pass |
| `data-plane/internal/iamv2/commerce_admin.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3: load the Phase-6 config before anything consumes it, and fail closed on NEW aggregate acquisition |
| `data-plane/internal/iamv2/commerce_engine.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `data-plane/internal/iamv2/commerce_flow.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/internal/iamv2/commerce_grant.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3: the accounting mode is a property of the immutable plan revision |
| `data-plane/internal/iamv2/commerce_hardening_db_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | gofmt: a missing blank line of mine, and an import block that predates this branch |
| `data-plane/internal/iamv2/commerce_integration_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `data-plane/internal/iamv2/phase6_acquisition.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/internal/iamv2/phase6_acquisition_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `data-plane/internal/iamv2/phase6_acquisition_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `data-plane/internal/iamv2/phase6_config.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M1: the pre-live clarification, the as-built reconciliation, and the additive foundation (D24/T0056, D25/T0057) |
| `data-plane/internal/iamv2/phase6_config_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M1: the pre-live clarification, the as-built reconciliation, and the additive foundation (D24/T0056, D25/T0057) |
| `data-plane/internal/iamv2/phase6_time_mode_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 6 M3: the accounting mode is a property of the immutable plan revision |
| `data-plane/internal/pmsd/pg_integration_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | Phase 6 M4: the dark-grant allow-list learns about 0047, and the rehearsal checks the grants come back |
| `data-plane/internal/staygrant/staygrant.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/migrations/0030_phase6_foundation.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6: evidence bound to real state, both aggregate outcomes proven, current facts reconciled from data |
| `data-plane/migrations/0030_phase6_foundation.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6: least privilege measured and fixed, and the M2 device-release core with its concurrency proof |
| `data-plane/migrations/0031_phase6_guest_device_self_service.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6: least privilege measured and fixed, and the M2 device-release core with its concurrency proof |
| `data-plane/migrations/0031_phase6_guest_device_self_service.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6: least privilege measured and fixed, and the M2 device-release core with its concurrency proof |
| `data-plane/migrations/0032_phase6_release_admission_serialization.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M2: reconcile the guest release with the REAL admission path, and make the forbidden state unrepresentable |
| `data-plane/migrations/0032_phase6_release_admission_serialization.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M2: reconcile the guest release with the REAL admission path, and make the forbidden state unrepresentable |
| `data-plane/migrations/0033_phase6_runtime_least_privilege.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6: the runtime privilege shape, proven as the real roles, and the plan synchronized with the as-built 0032 design |
| `data-plane/migrations/0033_phase6_runtime_least_privilege.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6: the runtime privilege shape, proven as the real roles, and the plan synchronized with the as-built 0032 design |
| `data-plane/migrations/0034_phase6_policy_boundaries.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6: close the remaining privilege bypasses and remove the speculative grants (0034) |
| `data-plane/migrations/0034_phase6_policy_boundaries.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6: close the remaining privilege bypasses and remove the speculative grants (0034) |
| `data-plane/migrations/0035_phase6_setting_serialization_and_list_audit.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6: order the first setting write, and stop claiming LIST is audited (0035) |
| `data-plane/migrations/0035_phase6_setting_serialization_and_list_audit.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6: order the first setting write, and stop claiming LIST is audited (0035) |
| `data-plane/migrations/0036_phase6_aggregate_online_time.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: the AGGREGATE_ONLINE_TIME accrual core (migration 0036), DARK |
| `data-plane/migrations/0036_phase6_aggregate_online_time.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: the AGGREGATE_ONLINE_TIME accrual core (migration 0036), DARK |
| `data-plane/migrations/0037_phase6_aggregate_window_and_exact_crossing.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: the outer window is a hard accrual ceiling, and the crossing is exact (migration 0037) |
| `data-plane/migrations/0037_phase6_aggregate_window_and_exact_crossing.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: the outer window is a hard accrual ceiling, and the crossing is exact (migration 0037) |
| `data-plane/migrations/0038_phase6_aggregate_respects_data_crossing.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `data-plane/migrations/0038_phase6_aggregate_respects_data_crossing.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `data-plane/migrations/0039_phase6_acctd_aggregate_privilege.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `data-plane/migrations/0039_phase6_acctd_aggregate_privilege.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `data-plane/migrations/0040_phase6_acctd_expiry_writer.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: the earliest terminal condition wins, and acctd runs the whole sweep as itself |
| `data-plane/migrations/0040_phase6_acctd_expiry_writer.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: the earliest terminal condition wins, and acctd runs the whole sweep as itself |
| `data-plane/migrations/0041_phase6_expiry_writer_derives_the_condition.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: the expiry writer establishes the terminal condition itself |
| `data-plane/migrations/0041_phase6_expiry_writer_derives_the_condition.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M3: the expiry writer establishes the terminal condition itself |
| `data-plane/migrations/0042_phase6_exhaustion_instant_must_be_provable.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/migrations/0042_phase6_exhaustion_instant_must_be_provable.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `data-plane/migrations/0043_phase6_exhaustion_instant_from_the_real_crossing.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: date exhaustion at the real crossing, and rehearse the whole rollback |
| `data-plane/migrations/0043_phase6_exhaustion_instant_from_the_real_crossing.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: date exhaustion at the real crossing, and rehearse the whole rollback |
| `data-plane/migrations/0044_phase6_exhaustion_instant_lower_bound.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: the adjustment history bounds consumption from below; it does not reconstruct it |
| `data-plane/migrations/0044_phase6_exhaustion_instant_lower_bound.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: the adjustment history bounds consumption from below; it does not reconstruct it |
| `data-plane/migrations/0045_phase6_over_budget_fail_closed.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: undatable is not a licence to keep browsing -- over-budget access fails closed |
| `data-plane/migrations/0045_phase6_over_budget_fail_closed.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: undatable is not a licence to keep browsing -- over-budget access fails closed |
| `data-plane/migrations/0046_phase6_suspension_reason_is_not_terminal.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: suspension evidence tells the truth one level down, and the rehearsal fails where it used to pass |
| `data-plane/migrations/0046_phase6_suspension_reason_is_not_terminal.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: suspension evidence tells the truth one level down, and the rehearsal fails where it used to pass |
| `data-plane/migrations/0047_phase6_guest_surface_can_resolve_a_device.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: the guest capability proven on the real appliance, and three privilege gaps only that could find |
| `data-plane/migrations/0047_phase6_guest_surface_can_resolve_a_device.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 6 M4: the guest capability proven on the real appliance, and three privilege gaps only that could find |
| `deploy/scripts/phase6-controlled-validation-body.sh` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Phase 6 M4: the guest capability proven on the real appliance, and three privilege gaps only that could find |
| `deploy/scripts/phase6-controlled-validation.sh` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Phase 6 M4: a restoration that drifted, and the appliance proven dark across a second reboot |
| `deploy/scripts/phase6-validation-scope.sql` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Phase 6 M4: the guest capability proven on the real appliance, and three privilege gaps only that could find |
| `deploy/scripts/phase6-validation-teardown.sql` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | Phase 6 M4: the guest capability proven on the real appliance, and three privilege gaps only that could find |
| `docs/ROLE_AND_SCOPE_MATRIX.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `docs/acceptance/StayConnect-IAM-Phase6-Development-Appliance-Evidence.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 6 M4: the documentation says what the appliance actually is |
| `docs/architecture/Phase3-Privilege-Matrix.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 6 M1 fix-forward: finish the pre-live reconciliation, stop inventing contract vocabulary, anchor scope server-side, and ground the online-time model |
| `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 6 M4: T0060 records milestone M4 complete at verified controlled LIVE-DARK |
| `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 6 M4: T0060 records milestone M4 complete at verified controlled LIVE-DARK |
| `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 6 M4: T0060 records milestone M4 complete at verified controlled LIVE-DARK |
| `docs/architecture/StayConnect-IAM-Phase3-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 6 M1 fix-forward: finish the pre-live reconciliation, stop inventing contract vocabulary, anchor scope server-side, and ground the online-time model |
| `docs/architecture/StayConnect-IAM-Phase5-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 6 M2 fix-forward: both aggregate terminal outcomes are representable; the pre-live guard catches the claim, not one spelling |
| `docs/architecture/StayConnect-IAM-Phase6-Plan.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 6 M4: the documentation says what the appliance actually is |
| `docs/context/StayConnect-IAM-Handoff.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 6 M4: T0060 records milestone M4 complete at verified controlled LIVE-DARK |
| `docs/manifests/Phase3-change-manifest.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 6 (delivery_head): M4 evidence pointer, complete staged manifest, rebuilt packs and report-embedded manifest |
| `docs/manifests/Phase6-change-manifest.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `docs/reports/StayConnect-IAM-Phase3-Final-Report.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 6 (delivery_head): M4 evidence pointer, complete staged manifest, rebuilt packs and report-embedded manifest |
| `docs/reports/StayConnect-IAM-Phase6-Final-Report.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase-evidence/GIT_STAT_22106ea.txt` | EXPORTED | `A` | export | EXPORT | rollback REMOVES it | (no commit subject in range) |
| `exports/chatgpt/phase-evidence/GIT_STAT_3da826c.txt` | EXPORTED | `D` | export | EXPORT | rollback RESTORES it | Phase 6 M1 fix-forward: finish the pre-live reconciliation, stop inventing contract vocabulary, anchor scope server-side, and ground the online-time model |
| `exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase-evidence/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase-evidence/governance/decision-register.json` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 M1 fix-forward: finish the pre-live reconciliation, stop inventing contract vocabulary, anchor scope server-side, and ground the online-time model |
| `exports/chatgpt/phase-evidence/tools/project-state.py` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 M1 fix-forward: finish the pre-live reconciliation, stop inventing contract vocabulary, anchor scope server-side, and ground the online-time model |
| `exports/chatgpt/phase-evidence/tools/validate-project-state.sh` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase1b-planning/MANIFEST.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase1b-planning/PACK_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase1b-planning/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase1b-planning/StayConnect-IAM-Phase1B-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/00-START-HERE.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 M4: T0060 records milestone M4 complete at verified controlled LIVE-DARK |
| `exports/chatgpt/stayconnectenterprise/MANIFEST.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/PROJECT-INSTRUCTIONS.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 M4: T0060 records milestone M4 complete at verified controlled LIVE-DARK |
| `exports/chatgpt/stayconnectenterprise/Phase3-Privilege-Matrix.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 M1 fix-forward: finish the pre-live reconciliation, stop inventing contract vocabulary, anchor scope server-side, and ground the online-time model |
| `exports/chatgpt/stayconnectenterprise/Phase3-change-manifest.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Handoff.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase0-Contract.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase1A-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase1B-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase3-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 6 M1 fix-forward: finish the pre-live reconciliation, stop inventing contract vocabulary, anchor scope server-side, and ground the online-time model |
| `governance/artifact-registry.json` | MODIFIED | `M` | governance | GOVERNANCE | rollback RESTORES prior content | Phase 6 M1: the pre-live clarification, the as-built reconciliation, and the additive foundation (D24/T0056, D25/T0057) |
| `governance/decision-register.json` | MODIFIED | `M` | governance | GOVERNANCE | rollback RESTORES prior content | Phase 6 M1: the pre-live clarification, the as-built reconciliation, and the additive foundation (D24/T0056, D25/T0057) |
| `governance/project-state.json` | MODIFIED | `M` | governance | GOVERNANCE | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `governance/transitions/T0056.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 6 M1: the pre-live clarification, the as-built reconciliation, and the additive foundation (D24/T0056, D25/T0057) |
| `governance/transitions/T0057.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 6 M1: the pre-live clarification, the as-built reconciliation, and the additive foundation (D24/T0056, D25/T0057) |
| `governance/transitions/T0058.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `governance/transitions/T0059.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 6: correct the T0059 receipt timestamp to UTC |
| `governance/transitions/T0060.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 6: correct T0060's timestamp, which described its own future by seventy seconds |
| `hotel-admin/app/(app)/guest-device-self-service/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `hotel-admin/app/(app)/online-time/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M3: operator view of online-time budgets, and the M4 flag-coherence check |
| `hotel-admin/components/nav.tsx` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M3: operator view of online-time budgets, and the M4 flag-coherence check |
| `hotel-admin/components/phase6/aggregate-time-view.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M3: operator view of online-time budgets, and the M4 flag-coherence check |
| `hotel-admin/components/phase6/guest-device-self-service-view.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `hotel-admin/lib/roles.ts` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `hotel-admin/test/phase6-aggregate-time.test.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M3: operator view of online-time budgets, and the M4 flag-coherence check |
| `hotel-admin/test/phase6-guest-device-self-service.test.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first |
| `iam_v2_scratch/00_platform_fixture.sql` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 6 M3: the expiry writer establishes the terminal condition itself |
| `iam_v2_scratch/phase6_0030_foundation.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `iam_v2_scratch/phase6_0031_device_self_service.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 6 M2: reconcile the guest release with the REAL admission path, and make the forbidden state unrepresentable |
| `iam_v2_scratch/phase6_0036_aggregate_online_time.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege |
| `iam_v2_scratch/phase6_backup_restore.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 6 M4: the adjustment history bounds consumption from below; it does not reconstruct it |
| `iam_v2_scratch/phase6_least_privilege.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 6 M4: the guest capability proven on the real appliance, and three privilege gaps only that could find |
| `iam_v2_scratch/phase6_rollback_rehearsal.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 6 M4: the dark-grant allow-list learns about 0047, and the rehearsal checks the grants come back |
| `iam_v2_scratch/run.sh` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 6 M1 fix-forward: finish the pre-live reconciliation, stop inventing contract vocabulary, anchor scope server-side, and ground the online-time model |
| `tools/embed-report-manifest.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 6: the final report, and a delivery tool that no longer hard-codes Phase 3 |
| `tools/phase6-flag-coherence.sh` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable |
| `tools/project-state.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 6 M1 fix-forward: finish the pre-live reconciliation, stop inventing contract vocabulary, anchor scope server-side, and ground the online-time model |
| `tools/tests/current_state_parity/run_negative.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 6: evidence bound to real state, both aggregate outcomes proven, current facts reconciled from data |
| `tools/tests/project_state_validator/run_mutations.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 6 M3: wire the accrual tick into the one expiry sweep, and fix the fixtures 0032 caught |
| `tools/validate-current-state-parity.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 6: evidence bound to real state, both aggregate outcomes proven, current facts reconciled from data |
| `tools/validate-project-state.sh` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |

## Total diff statistics (`git diff --stat`)
```text
 data-plane/cmd/acctd/main.go                       |    8 +
 data-plane/cmd/acctd/phase3.go                     |   51 +-
 .../cmd/acctd/phase3_boundary_integration_test.go  |   14 +
 .../cmd/acctd/phase3_ingest_integration_test.go    |   12 +
 data-plane/cmd/acctd/phase6_accounting_owner.go    |   77 +
 .../cmd/acctd/phase6_accounting_owner_test.go      |   45 +
 data-plane/cmd/edged/auth.go                       |   23 +-
 data-plane/cmd/edged/main.go                       |   36 +
 data-plane/cmd/edged/phase6_capability_wiring.go   |   18 +
 .../cmd/edged/phase6_capability_wiring_test.go     |   74 +
 .../cmd/edged/phase6_role_matrix_doc_test.go       |  123 ++
 data-plane/cmd/edged/phase6_setting_api_test.go    |  132 ++
 data-plane/cmd/edged/phase6_setting_rbac_test.go   |  145 ++
 data-plane/cmd/edged/resources_phase6_aggregate.go |   84 +
 data-plane/cmd/edged/resources_phase6_setting.go   |  158 ++
 data-plane/cmd/edged/resources_sessions.go         |    6 +
 data-plane/cmd/portald/main.go                     |   32 +-
 data-plane/cmd/portald/phase6_device_handlers.go   |  120 ++
 .../cmd/portald/phase6_guest_presentation_test.go  |  224 +++
 .../cmd/portald/phase6_public_authority_test.go    |  368 ++++
 .../cmd/portald/phase6_source_identity_test.go     |  164 ++
 data-plane/cmd/portald/templates.go                |  175 ++
 data-plane/cmd/scd-enroll-test/main.go             |   19 +-
 data-plane/cmd/scd/authsec.go                      |    7 +-
 data-plane/cmd/scd/main.go                         |   44 +-
 .../cmd/scd/phase3_activation_integration_test.go  |   61 +-
 data-plane/cmd/scd/phase3_auth.go                  |   23 +-
 data-plane/cmd/scd/phase3_auth_integration_test.go |   53 +-
 data-plane/cmd/scd/phase6_device.go                |  204 +++
 data-plane/cmd/scd/phase6_device_api_test.go       |  162 ++
 data-plane/cmd/scd/phase6_e2e_integration_test.go  |  443 +++++
 data-plane/cmd/scd/phase6_localfirst_test.go       |  113 ++
 data-plane/cmd/scd/phase6_remaining.go             |   71 +
 .../deviceselfservice/deviceselfservice.go         |  244 +++
 .../deviceselfservice_integration_test.go          | 1008 ++++++++++
 data-plane/internal/enforce/enforce.go             |  212 ++-
 .../enforce/phase6_aggregate_integration_test.go   | 1920 ++++++++++++++++++++
 data-plane/internal/iamv2/commerce_admin.go        |   41 +-
 data-plane/internal/iamv2/commerce_engine.go       |   12 +
 data-plane/internal/iamv2/commerce_flow.go         |   18 +
 data-plane/internal/iamv2/commerce_grant.go        |   22 +-
 .../internal/iamv2/commerce_hardening_db_test.go   |   20 +-
 .../internal/iamv2/commerce_integration_test.go    |   23 +-
 data-plane/internal/iamv2/phase6_acquisition.go    |   60 +
 .../iamv2/phase6_acquisition_integration_test.go   |  214 +++
 .../internal/iamv2/phase6_acquisition_test.go      |   41 +
 data-plane/internal/iamv2/phase6_config.go         |  125 ++
 data-plane/internal/iamv2/phase6_config_test.go    |   94 +
 data-plane/internal/iamv2/phase6_time_mode_test.go |   93 +
 data-plane/internal/pmsd/pg_integration_test.go    |   67 +-
 data-plane/internal/staygrant/staygrant.go         |   43 +-
 .../migrations/0030_phase6_foundation.down.sql     |   47 +
 .../migrations/0030_phase6_foundation.up.sql       |  418 +++++
 .../0031_phase6_guest_device_self_service.down.sql |   17 +
 .../0031_phase6_guest_device_self_service.up.sql   |  176 ++
 ...phase6_release_admission_serialization.down.sql |   58 +
 ...2_phase6_release_admission_serialization.up.sql |  158 ++
 .../0033_phase6_runtime_least_privilege.down.sql   |   21 +
 .../0033_phase6_runtime_least_privilege.up.sql     |  142 ++
 .../0034_phase6_policy_boundaries.down.sql         |   23 +
 .../0034_phase6_policy_boundaries.up.sql           |  153 ++
 ...6_setting_serialization_and_list_audit.down.sql |   41 +
 ...se6_setting_serialization_and_list_audit.up.sql |  113 ++
 .../0036_phase6_aggregate_online_time.down.sql     |   19 +
 .../0036_phase6_aggregate_online_time.up.sql       |  295 +++
 ...e6_aggregate_window_and_exact_crossing.down.sql |  181 ++
 ...ase6_aggregate_window_and_exact_crossing.up.sql |  242 +++
 ...hase6_aggregate_respects_data_crossing.down.sql |  205 +++
 ..._phase6_aggregate_respects_data_crossing.up.sql |  250 +++
 .../0039_phase6_acctd_aggregate_privilege.down.sql |   16 +
 .../0039_phase6_acctd_aggregate_privilege.up.sql   |   80 +
 .../0040_phase6_acctd_expiry_writer.down.sql       |   12 +
 .../0040_phase6_acctd_expiry_writer.up.sql         |  156 ++
 ...e6_expiry_writer_derives_the_condition.down.sql |   95 +
 ...ase6_expiry_writer_derives_the_condition.up.sql |  232 +++
 ...e6_exhaustion_instant_must_be_provable.down.sql |  287 +++
 ...ase6_exhaustion_instant_must_be_provable.up.sql |  361 ++++
 ...austion_instant_from_the_real_crossing.down.sql |   57 +
 ...xhaustion_instant_from_the_real_crossing.up.sql |  146 ++
 ..._phase6_exhaustion_instant_lower_bound.down.sql |  128 ++
 ...44_phase6_exhaustion_instant_lower_bound.up.sql |  145 ++
 .../0045_phase6_over_budget_fail_closed.down.sql   |  124 ++
 .../0045_phase6_over_budget_fail_closed.up.sql     |  248 +++
 ...ase6_suspension_reason_is_not_terminal.down.sql |   70 +
 ...phase6_suspension_reason_is_not_terminal.up.sql |   85 +
 ...se6_guest_surface_can_resolve_a_device.down.sql |   18 +
 ...hase6_guest_surface_can_resolve_a_device.up.sql |   88 +
 .../scripts/phase6-controlled-validation-body.sh   |  252 +++
 deploy/scripts/phase6-controlled-validation.sh     |  545 ++++++
 deploy/scripts/phase6-validation-scope.sql         |  195 ++
 deploy/scripts/phase6-validation-teardown.sql      |   76 +
 docs/ROLE_AND_SCOPE_MATRIX.md                      |   16 +
 ...ct-IAM-Phase6-Development-Appliance-Evidence.md |  217 +++
 docs/architecture/Phase3-Privilege-Matrix.md       |    2 +-
 .../StayConnect-IAM-Phase0-Contract.md             |   14 +-
 docs/architecture/StayConnect-IAM-Phase1A-Plan.md  |   14 +-
 docs/architecture/StayConnect-IAM-Phase1B-Plan.md  |   14 +-
 docs/architecture/StayConnect-IAM-Phase3-Plan.md   |    3 +-
 docs/architecture/StayConnect-IAM-Phase5-Plan.md   |    3 +-
 docs/architecture/StayConnect-IAM-Phase6-Plan.md   |  509 ++++++
 docs/context/StayConnect-IAM-Handoff.md            |   20 +-
 docs/manifests/Phase3-change-manifest.md           |  417 +++--
 docs/manifests/Phase6-change-manifest.md           |  400 ++++
 .../reports/StayConnect-IAM-Phase3-Final-Report.md |  420 +++--
 .../reports/StayConnect-IAM-Phase6-Final-Report.md |  608 +++++++
 .../StayConnectEnterprise-ChatGPT-Project-Pack.zip |  Bin 306526 -> 312455 bytes
 .../StayConnectEnterprise-Phase-Evidence-Pack.zip  |  Bin 115922 -> 117793 bytes
 ...StayConnectEnterprise-Phase1B-Planning-Pack.zip |  Bin 42174 -> 42191 bytes
 .../chatgpt/phase-evidence/GIT_STAT_22106ea.txt    |    4 +
 .../chatgpt/phase-evidence/GIT_STAT_3da826c.txt    |    4 -
 exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt |   10 +-
 .../REPOSITORY_ARTIFACT_SHA256SUMS.txt             |    8 +-
 .../governance/decision-register.json              |   27 +
 .../chatgpt/phase-evidence/tools/project-state.py  |    2 +-
 .../phase-evidence/tools/validate-project-state.sh |    2 +-
 exports/chatgpt/phase1b-planning/MANIFEST.md       |    2 +-
 .../chatgpt/phase1b-planning/PACK_SHA256SUMS.txt   |    6 +-
 .../REPOSITORY_ARTIFACT_SHA256SUMS.txt             |   12 +-
 .../StayConnect-IAM-Phase1B-Plan.md                |   14 +-
 .../chatgpt/stayconnectenterprise/00-START-HERE.md |   18 +-
 exports/chatgpt/stayconnectenterprise/MANIFEST.md  |   72 +-
 .../stayconnectenterprise/PROJECT-INSTRUCTIONS.md  |   14 +-
 .../Phase3-Privilege-Matrix.md                     |    2 +-
 .../Phase3-change-manifest.md                      |  435 +++--
 .../StayConnect-IAM-Handoff.md                     |   20 +-
 .../StayConnect-IAM-Phase0-Contract.md             |   14 +-
 .../StayConnect-IAM-Phase1A-Plan.md                |   14 +-
 .../StayConnect-IAM-Phase1B-Plan.md                |   14 +-
 .../StayConnect-IAM-Phase3-Plan.md                 |    3 +-
 governance/artifact-registry.json                  |  323 +++-
 governance/decision-register.json                  |   27 +
 governance/project-state.json                      |   77 +-
 governance/transitions/T0056.json                  |   53 +
 governance/transitions/T0057.json                  |   85 +
 governance/transitions/T0058.json                  |   61 +
 governance/transitions/T0059.json                  |   69 +
 governance/transitions/T0060.json                  |   85 +
 .../app/(app)/guest-device-self-service/page.tsx   |   25 +
 hotel-admin/app/(app)/online-time/page.tsx         |   11 +
 hotel-admin/components/nav.tsx                     |   11 +
 .../components/phase6/aggregate-time-view.tsx      |  122 ++
 .../phase6/guest-device-self-service-view.tsx      |  217 +++
 hotel-admin/lib/roles.ts                           |    8 +
 hotel-admin/test/phase6-aggregate-time.test.tsx    |   80 +
 .../test/phase6-guest-device-self-service.test.tsx |  202 ++
 iam_v2_scratch/00_platform_fixture.sql             |   32 +
 iam_v2_scratch/phase6_0030_foundation.sh           |  370 ++++
 iam_v2_scratch/phase6_0031_device_self_service.sh  |  196 ++
 .../phase6_0036_aggregate_online_time.sh           |  422 +++++
 iam_v2_scratch/phase6_backup_restore.sh            |  103 ++
 iam_v2_scratch/phase6_least_privilege.sh           |  276 +++
 iam_v2_scratch/phase6_rollback_rehearsal.sh        |  204 +++
 iam_v2_scratch/run.sh                              |    2 +-
 tools/embed-report-manifest.py                     |   33 +-
 tools/phase6-flag-coherence.sh                     |  114 ++
 tools/project-state.py                             |    2 +-
 tools/tests/current_state_parity/run_negative.py   |   12 +
 .../tests/project_state_validator/run_mutations.py |   17 +-
 tools/validate-current-state-parity.py             |   93 +
 tools/validate-project-state.sh                    |    2 +-
 160 files changed, 19535 insertions(+), 674 deletions(-)
```

## Working-tree status (`git status --short --untracked-files=all`)
```text
M  docs/manifests/Phase6-change-manifest.md
M  docs/reports/StayConnect-IAM-Phase6-Final-Report.md
M  exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip
M  exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip
M  exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip
A  exports/chatgpt/phase-evidence/GIT_STAT_22106ea.txt
D  exports/chatgpt/phase-evidence/GIT_STAT_56c448d.txt
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
HISTORICAL: 22106ea gofmt: a missing blank line of mine, and an import block that predates this branch
HISTORICAL: 6cc8be9 Phase 6 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
HISTORICAL: 56c448d Phase 6: the final report, and a delivery tool that no longer hard-codes Phase 3
HISTORICAL: b42a7de Phase 6: correct T0060's timestamp, which described its own future by seventy seconds
HISTORICAL: de53d73 Phase 6 M4: T0060 records milestone M4 complete at verified controlled LIVE-DARK
HISTORICAL: 82be604 Phase 6 M4: the documentation says what the appliance actually is
HISTORICAL: 2cd957a Phase 6 M4: a restoration that drifted, and the appliance proven dark across a second reboot
HISTORICAL: ba8de92 Phase 6 M4: the dark-grant allow-list learns about 0047, and the rehearsal checks the grants come back
HISTORICAL: c6b3bb1 Phase 6 M4: the guest capability proven on the real appliance, and three privilege gaps only that could find
HISTORICAL: f2fc56b Phase 6 M4: the harness found three of its own defects by being run, and the appliance's fail-closed prerequisite
HISTORICAL: 1624503 Phase 6 M4: suspension evidence tells the truth one level down, and the rehearsal fails where it used to pass
HISTORICAL: 1c1c233 Phase 6 M4: corrected runtime and the Hotel Admin bundle deployed DARK, with a fail-safe harness proven first
HISTORICAL: 8bb3dab Phase 6 M4: undatable is not a licence to keep browsing -- over-budget access fails closed
HISTORICAL: 8f14ba1 Phase 6 M4: the Phase-6 runtime is deployed on the development appliance, and inert
HISTORICAL: d777fa0 Phase 6 M4: the adjustment history bounds consumption from below; it does not reconstruct it
HISTORICAL: 9a145b7 Phase 6 M4: the development appliance carries the Phase-6 schema, DARK and reboot-verified
HISTORICAL: eb0c21d Phase 6 M4: date exhaustion at the real crossing, and rehearse the whole rollback
HISTORICAL: 64040e4 Phase 6 (delivery_head): M4 evidence pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: 253266c Phase 6 M4: record the repository-side evidence, and what remains appliance-bound
HISTORICAL: 1c6b15c Phase 6 M4: a real backup and restore, not a simulated one
HISTORICAL: 0c54d40 Phase 6 M4: rollback rehearsal, in the order a rollback must actually be performed
HISTORICAL: e5d0a15 Phase 6 M4: an exhaustion instant must be provable, accounting has an owner in every configuration, and the suite is re-runnable
HISTORICAL: 0da2830 Phase 6 (delivery_head): M3 receipt pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: 259a151 Phase 6: correct the T0059 receipt timestamp to UTC
HISTORICAL: 9ac0486 Phase 6 (delivery_head): M3 receipt pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: 86b5f21 Phase 6: M3 governance receipt (T0059) under D25
HISTORICAL: a496e78 Phase 6 M3: the expiry writer establishes the terminal condition itself
HISTORICAL: 43ccae5 Phase 6 M3: operator view of online-time budgets, and the M4 flag-coherence check
HISTORICAL: 288463e Phase 6 M3/M4: accrual is data-driven, guest sees both clocks, and the disable path cannot create unlimited access
HISTORICAL: ef441c2 Phase 6 M3: the earliest terminal condition wins, and acctd runs the whole sweep as itself
HISTORICAL: 43da658 Phase 6 M3: one acquisition rule for every free path, DATA as an accrual ceiling, and svc_acctd's exact privilege
HISTORICAL: 6b5eb26 Phase 6 M3: load the Phase-6 config before anything consumes it, and fail closed on NEW aggregate acquisition
HISTORICAL: e959cf7 Phase 6 M3: the accounting mode is a property of the immutable plan revision
HISTORICAL: 17f92c1 Phase 6 M3: the outer window is a hard accrual ceiling, and the crossing is exact (migration 0037)
HISTORICAL: b144b63 Phase 6 (delivery_head): M3 accrual-wiring pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: bfec9fd Phase 6 M3: wire the accrual tick into the one expiry sweep, and fix the fixtures 0032 caught
HISTORICAL: 60e4f5e Phase 6 M3: the AGGREGATE_ONLINE_TIME accrual core (migration 0036), DARK
HISTORICAL: 0a5ca0d Phase 6 (delivery_head): M2 pointer, complete staged manifest, rebuilt packs and report-embedded manifest
HISTORICAL: 65e11d0 Phase 6 M2 COMPLETE: the Guest Device Self-Service slice, end to end and local-first
HISTORICAL: 2f1551b Phase 6: the portal source-identity boundary and real Hotel-Admin RBAC (two load-bearing HTTP findings)
HISTORICAL: 408b6a8 Phase 6 M2: the HTTP surfaces, with the identity boundaries as the load-bearing part
HISTORICAL: 104dc6b Phase 6: order the first setting write, and stop claiming LIST is audited (0035)
HISTORICAL: 76a0be7 Phase 6: close the remaining privilege bypasses and remove the speculative grants (0034)
HISTORICAL: 5df9594 Phase 6: the runtime privilege shape, proven as the real roles, and the plan synchronized with the as-built 0032 design
HISTORICAL: 50d1eab Phase 6 M2: reconcile the guest release with the REAL admission path, and make the forbidden state unrepresentable
HISTORICAL: 460c745 Phase 6 M2: the guest device self-service service layer, proven against real PostgreSQL
HISTORICAL: 976e09f Phase 6: least privilege measured and fixed, and the M2 device-release core with its concurrency proof
HISTORICAL: 63cb017 Phase 6: evidence bound to real state, both aggregate outcomes proven, current facts reconciled from data
HISTORICAL: fbdb3e8 Phase 6 M2 fix-forward: both aggregate terminal outcomes are representable; the pre-live guard catches the claim, not one spelling
HISTORICAL: 5c03be1 Phase 6 foundation fix-forward: accounting eligibility, scope integrity, evidence bound to the real transition
HISTORICAL: 7512257 Phase 6 M1 fix-forward: finish the pre-live reconciliation, stop inventing contract vocabulary, anchor scope server-side, and ground the online-time model
HISTORICAL: 843cb14 Phase 6 M1: the pre-live clarification, the as-built reconciliation, and the additive foundation (D24/T0056, D25/T0057)
```
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
