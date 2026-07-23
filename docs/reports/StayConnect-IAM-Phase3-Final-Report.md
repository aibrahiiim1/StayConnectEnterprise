# StayConnect IAM — Phase 3 Final Report (Stay Resolution, PMS Auth Context, Checkout Grace)

> **Status: DARK ACCEPTANCE CANDIDATE.** The complete Phase-3 software scope is implemented, tested and
> delivered green. Every Phase-3 flag is OFF, PR #6 is open and unmerged, and **live Increment-9 evidence
> remains PENDING** — it can only be produced by an authorized operator on the target appliance and is
> deliberately absent from this report rather than inferred.

---

## 1. شرح مبسّط بالعامية المصرية

الفيز دي بتخلّي النظام يعرف الضيف من غير ما يسأله كلمة سر: بيقرا من نظام الفندق (الـPMS) مين ساكن فين، وبيربط
ده بالشبكة اللي الضيف متوصّل عليها. أهم حاجة اتعملت إن كل قرار بقى **متسجّل ومتثبت**: لو الضيف عمل تشيك-أوت،
النظام بيحدّد لحظة الخروج **من حدث الـPMS نفسه** مش من ساعة السيرفر، وبيدي الضيف فترة سماح (Grace) بقواعد
معتمدة، وبيقفل الوصول لأي جهاز مش من ضمن اللي كانوا شغالين وقت الخروج — من غير ما يعمل logout للي شغال فعلاً.
وكمان أي حاجة اتسجّلت غلط بتتصلّح بتسجيل تصحيح جديد، مش بمسح القديم. كله لسه **مقفول (DARK)** — يعني الكود
موجود ومتجرّب لكن مفيش أي حاجة اشتغلت على أي فندق حقيقي لحد ما يتاخد قرار التشغيل.

## 2. Current Phase and authorized scope

- **Phase:** 3 — Stay Resolution / PMS Auth Context / Checkout Grace (DARK).
- **Authorized scope:** the complete Phase-3 DARK software scope as one continuous execution stream on PR #6,
  under the standing constraints: all Phase-3 flags OFF; PR #6 open and unmerged; zero persistent runtime
  `iam_v2` privileges; no appliance, Production DB or live PMS contact; no `PS`/`PA`; no financial posting; no
  paid-access implementation; both CIs green on the same pushed HEAD; never fabricate live evidence.
- **PO authorization reference:** the Phase-3 execution directive and the eleven successive correction
  directives against the Increment-7 Checkout subsystem, followed by the closing scorecard.

## 2a. Where this candidate stands

**Software evidence: complete for every dimension marked `PASS — SOFTWARE` in the Acceptance Matrix (§6a).**
**Live Increment-9 evidence: PENDING.** Nothing in this report was produced by contacting an appliance, a
production database or a PMS, and no live result is simulated or inferred anywhere in it.

The Hotel-Admin operator surfaces once listed in `docs/PHASE3_SCOPE_AMENDMENT_PROPOSAL.md` are no longer
pending a decision: the Product Owner **rejected the proposal (D15, Option C) with no scope reduction**, and
they were built and are `PASS — SOFTWARE` in the matrix.

**Phase 3 is NOT marked accepted or closed by this report.**

## 3. What was implemented

**Stay domain and event application**
- One physical Stay-Event→Checkout transaction (`NewProcessorWithCheckout`); the legacy server-clock flip is
  **deleted**, and a `GO` event with no wired Converter fails closed rather than establishing an unverified
  boundary.
- Per-Interface ordered application under an advisory lock; proven under 24 concurrent processors.
- Exact event lineage (`stays.last_applied_event_id`), **structurally enforced and composite-scoped** so it can
  never reference another tenant/site/interface's event.
- Sharers (legal multi-occupancy, exactly one primary that moves rather than duplicating), Folios, and
  source-conflict detection routed to `MANUAL_REVIEW` with bounded codes.

**Entitlement lifecycle**
- **Bitemporal history**: `effective_at` (true business time, stored verbatim, never clamped) and `recorded_at`
  (system time, monotonic). Corrections are explicit — `supersede_entitlement_transition` and
  `terminate_entitlement_at_boundary` — and nothing is ever edited or deleted.
- Controlled-writer authorization boundary (`SECURITY DEFINER`, per-family owner resolved by `regprocedure`),
  append-only history, deferred status/history coherence.
- Controlled device authorization/deauthorization with the plan device limit enforced atomically.

**Checkout Grace**
- Boundary derived from the trusted PMS event; eligibility proven from history at the boundary; grandfathering
  by authorization interval; sessions rebound **without logout**; canonical Emergency-Grace catalog fallback
  with a resolvable operational alert; one conversion per episode.
- **Post-boundary revocation** on every outcome: nothing keeps forwarding traffic for access that has ended.

**Commerce, accounting and enforcement**
- Atomic Auth Context → Quote → Purchase → Entitlement → device authorization in one transaction; paid access
  refused (`ErrSettlementRequired`), never approximated.
- Accounting attributed by session→entitlement binding intervals; boundary watermarks freeze decision evidence;
  late samples recorded as delayed, never folded back.
- Derived netd shaping plan and acctd expiry enforcement at the true ending time.

**Resolution, surfaces and tooling**
- STRICT resolver fan-out over the complete candidate vector with idempotent `auth_resolutions`.
- Guest-portal uniform non-success contract (byte-identical failures).
- Hotel-Admin Phase-3 API + RBAC + four UI pages, dark-gated.
- Increment-9 offline preflight, evidence collector and deployment/rollback/reboot runbook.

## 4. Practical effect

Nothing changes for guests or operators today: every surface is dark. What now exists is a Stay/Checkout model
whose decisions are **reproducible** — the boundary comes from a durable event, eligibility from append-only
history, usage from binding-attributed accounting, and each decision's evidence is frozen at the moment it was
made. When the flags are eventually turned on, a checkout produces a grace period with the operator's approved
policy, keeps working devices online, cuts everything else at the boundary, and leaves an audit trail that can
be re-derived rather than trusted.

## 5. Risks and limitations

- **No live evidence exists.** Nothing in this repository has touched an appliance, a production database or a
  PMS. Every claim here is from local builds, unit tests and disposable PostgreSQL 16 containers.
- **Paid access is deliberately unimplemented.** Priced packages and settlement methods beyond `NOT_REQUIRED`
  fail closed; the fixtures used in F3 are zero-amount and are not payment evidence.
- **Gate-P privileges are prepared but NOT applied.** Every runtime service role holds zero `iam_v2` table and
  function privileges; the gate asserts it on every run.
- **Device-limit policies other than `REJECT_NEW_DEVICE`** are refused rather than approximated.
- The Phase-2 Commerce (`internal/iamv2`) direct-Entitlement writer is **eliminated** (§5 of this round): the
  admin path no longer sets Entitlement status with a raw `UPDATE`; it terminates a superseded Entitlement
  through `iam_v2.apply_entitlement_transition`, which also appends the transition history the raw path never
  wrote. There is no remaining Phase-3 family whose authoritative writes bypass the controlled-writer boundary,
  and nothing here waits on cutover for that. (Cutover itself remains a separate, unauthorized future step.)

## 6. Acceptance tests

Every row below is executed by the **Phase-3 Software workflow** (`.github/workflows/phase3-software.yml`,
job `phase3-full-software-gate`) on the delivery HEAD recorded in §12, in one run, and its totals are recorded
in the evidence artifact that run uploads (`phase3-software-evidence-<delivery-HEAD>`). "PASS" means the suite
ran to completion with no failing assertion and no skipped required test; nothing here is a workstation number
or inferred from a previous run. The totals below are deterministic for this HEAD and are the ones the
artifact records; the run's numeric run IDs, artifact ID and integrity-manifest SHA-256 are in the PR #6 body
(they cannot be embedded in the commit they describe — the same self-reference rule as the change manifest).

| Test | Result | Evidence |
|---|---|---|
| Offline preflight (build, flags, migration reversibility, zero runtime privilege, control-plane invariants, rollback ordering) | **PASS 18/18** | `scripts/phase3-preflight.sh --json` |
| Migration lifecycle gate (apply → behaviour → down → re-apply, disposable PG16) | **PASS 362/362** | `iam_v2_scratch/phase3_0010_lifecycle.sh` |
| PG16 integration suites (pmsd, stayengine, authctx, checkout, staygrant, pmsresolve, enforce, writerguard, edged, acctd, scd) | **PASS** (all eleven) | `scripts/pmsd-pg-integration.sh` |
| Go unit tests, whole module | **PASS** | `go test ./... -count=1` (JSON-counted) |
| Go race suite (pmsd, resolver, authctx, staygrant, checkout, scd, acctd, netd, writerguard, shape/shapeplan) | **PASS** | `go test -race` (CI) |
| F1–F7 named flow suite | **PASS** | `internal/checkout/f_flows_integration_test.go`, `internal/stayengine` |
| ≥24 concurrent checkout handlers / resolutions / grants / device authorizations | **PASS** | integration suites |
| Hotel-Admin component tests (Vitest) | **PASS 63/63** | `npx vitest run` (CI, JSON reporter) |
| Hotel-Admin + guest-portal E2E and accessibility (real browser) | **PASS 49/49** | `npx playwright test` (CI, JSON reporter) |
| TypeScript typecheck | **PASS** | `npx tsc --noEmit` (CI) |
| Production build with Phase-3 flags OFF | **PASS** | `npx next build` (CI) |
| Guest-portal uniform non-success contract (server) | **PASS** | `cmd/portald/pms_phase3_test.go`, `pms_phase3_handlers_test.go`, `pms_phase3_budget_test.go` |
| Guest-portal Phase-3 flow + resilience (real browser, real template) | **PASS** | `hotel-admin/e2e/phase3-guest-portal*.spec.ts` |
| Full Phase-3 Software CI + Governance CI on the same pushed HEAD, evidence artifact uploaded | **PASS** | §12 |
| Live read-only PMS protocol verification | **PENDING** | operator-executed; not simulated |
| Live-dark deployment, reboot drill, rollback rehearsal, flags-OFF confirmation | **PENDING** | operator-executed; runbook §2–§5 |

### On retries

Both disposable-PostgreSQL gates now separate an infrastructure failure (exit 2 — the container or the
baseline schema could not be built, and no assertion was ever reached) from a failed assertion (exit 1). CI
retries **only** exit 2, once. A failed assertion is final. The previous policy retried the whole script on
any failure, which would have let an order- or timing-dependent defect pass on a second attempt and be
reported green.

## 6a. Phase-3 Acceptance Matrix

Three verdicts are used: `PASS — SOFTWARE`, `PENDING — LIVE INCREMENT 9`, and
`OUT OF SCOPE BY APPROVED CONTRACT`. An earlier draft carried a fourth,
`PENDING — PO SCOPE DECISION`, for the Hotel-Admin surfaces in
`docs/PHASE3_SCOPE_AMENDMENT_PROPOSAL.md`. **The Product Owner decided (D15, Option C): the proposal was
REJECTED and no scope was reduced.** Those surfaces were built and are now `PASS — SOFTWARE` on the same
footing as the rest — real `edged`→PostgreSQL API tests, RBAC, cross-site refusal, step-up, optimistic
conflict, audit and redaction, plus Vitest, Playwright and accessibility. The fourth verdict no longer
appears.

| # | Dimension | Verdict | Evidence |
|---|---|---|---|
| 1 | Migration 0010 applies, behaves, rolls back and re-applies (disposable PG16) | **PASS — SOFTWARE** | `iam_v2_scratch/phase3_0010_lifecycle.sh` |
| 2 | Durable accounting checkpoints; absolute-counter ingestion; restart bills the gap exactly once | **PASS — SOFTWARE** | migration §4p; `cmd/acctd/phase3_pass_integration_test.go` |
| 3 | Accounting attribution at SAMPLE time across a Grace rebinding; no current-entitlement fallback | **PASS — SOFTWARE** | `cmd/acctd/phase3_boundary_integration_test.go` |
| 4 | Accounting writer boundary: raw INSERT/UPDATE/DELETE refused for a privileged non-owner | **PASS — SOFTWARE** | lifecycle gate §C7; `phase3_boundary_integration_test.go` |
| 5 | Controlled operations: SECURITY DEFINER, pinned `search_path`, PUBLIC EXECUTE revoked, zero runtime grants | **PASS — SOFTWARE** | lifecycle gate §1/§5/§6 |
| 6 | Every Phase-3 writing service refuses to start on an unenforced boundary or as the writer's owner | **PASS — SOFTWARE** | `internal/writerguard` + its PG16 suite |
| 7 | netd is the only Phase-3 tc writer (ADR-0002); acctd holds no tc client | **PASS — SOFTWARE** | `cmd/acctd/phase3_test.go`; preflight |
| 8 | The shaping producer is authenticated by `SO_PEERCRED` against one allowlisted uid | **PASS — SOFTWARE** | `cmd/netd/phase3_shaping_test.go` |
| 9 | A dark netd refuses every plan on its own authority, and discloses no class generations | **PASS — SOFTWARE** | `cmd/netd/phase3_shaping_test.go` |
| 10 | Scoped, versioned, expiring, hashed plan envelope; stale/replayed/out-of-scope plans refused across restart | **PASS — SOFTWARE** | `internal/shapeplan`; `cmd/netd/phase3_shaping_test.go` |
| 11 | Full-state reconciliation removes unclaimed classes, including on a bridge with no sessions | **PASS — SOFTWARE** | `cmd/netd/phase3_shaping_test.go` |
| 12 | Teardown precedes shaping; partial application and an unreadable kernel are reported degraded | **PASS — SOFTWARE** | `cmd/netd/phase3_shaping_test.go` |
| 13 | STRICT multi-interface resolution; ambiguity and indeterminacy grant nothing | **PASS — SOFTWARE** | `internal/pmsresolve`; `cmd/scd/phase3_auth_integration_test.go` |
| 14 | Server-derived identity only: no stay, interface, device, network or price from a guest body | **PASS — SOFTWARE** | `cmd/scd/phase3_auth.go`; `cmd/portald/pms_phase3_handlers_test.go` |
| 15 | One-time Auth Context: consumed exactly once, device-bound, expiring | **PASS — SOFTWARE** | `internal/authctx`; `cmd/scd/phase3_auth_integration_test.go` |
| 16 | Atomic Quote → Purchase → Entitlement → device authorization → Session; no session before its entitlement | **PASS — SOFTWARE** | `internal/staygrant`; `cmd/scd/phase3_auth_integration_test.go` |
| 17 | Paid packages refused even when named directly (no silent free grant) | **PASS — SOFTWARE** | `cmd/scd/phase3_auth_integration_test.go` |
| 18 | Uniform guest non-success contract: every failure identical, byte for byte | **PASS — SOFTWARE** | scd + portald suites; `hotel-admin/e2e/phase3-guest-portal.spec.ts` |
| 19 | Guest Portal Phase-3 flow in a real browser on the real template | **PASS — SOFTWARE** | `hotel-admin/e2e/phase3-guest-portal.spec.ts` |
| 20 | Hotel Admin: Stays, occupants, folios | **PASS — SOFTWARE** | vitest + Playwright |
| 21 | Hotel Admin: Stay-Event review queue and refusal reasons | **PASS — SOFTWARE** | vitest + Playwright |
| 22 | Hotel Admin: Checkout-Grace selector, publication, version conflict | **PASS — SOFTWARE** | vitest + Playwright; `cmd/edged` PG16 suite |
| 23 | Hotel Admin: operational alert triage, bounded actions, concurrent change | **PASS — SOFTWARE** | vitest + Playwright; `cmd/edged` PG16 suite |
| 24 | Hotel Admin: PMS Interfaces, immutable Revisions, current/published publish state | **PASS — SOFTWARE** | `cmd/edged/phase3_interfaces_api_integration_test.go`; `hotel-admin/test/phase3-interface-pages.test.tsx`; `e2e/phase3-pms-interfaces.spec.ts` |
| 25 | Hotel Admin: write-only Secret rotation (AES-256-GCM, no read path, refused without a key) | **PASS — SOFTWARE** | `cmd/edged/phase3_interfaces_api_integration_test.go`; `internal/pmsd/secret.go` seal path |
| 26 | Hotel Admin: Guest-Network→PMS routing, including the networks mapped to nothing | **PASS — SOFTWARE** | `cmd/edged/phase3_interfaces_api_integration_test.go`; `e2e/phase3-pms-interfaces.spec.ts` |
| 27 | Hotel Admin: transport / continuity / sync / occupancy health, ingestion backlog with oldest-waiting age | **PASS — SOFTWARE** | `cmd/edged/phase3_interfaces_api_integration_test.go` (derived-health + never-connected) |
| 28 | Hotel Admin: Resolution evidence (no guest PII), source conflicts naming both interfaces | **PASS — SOFTWARE** | `cmd/edged/phase3_interfaces_api_integration_test.go`; `e2e/phase3-pms-interfaces.spec.ts` |
| 29 | Flags OFF by default; a child flag without its master is a startup failure | **PASS — SOFTWARE** | `internal/iamv2/pms_config.go`; preflight |
| 30 | Dark appliance issues zero Phase-3 SQL, mounts no Phase-3 route, mutates no tc | **PASS — SOFTWARE** | acctd/netd/scd/edged dark tests |
| 30a | Accountable before forwarding: a managed class carries no guest packet before its origin is registered (staged prepare → register → activate) | **PASS — SOFTWARE** | `cmd/netd/phase3_provision.go`; `phase3_provision_test.go`; `internal/shape/shape_staged_test.go`; preflight |
| 30b | Every provisioning failure fails closed: nothing forwards, no epoch exposed, `Shaped` stays 0, plan admitted-not-converged, retry reuses the generation (no duplicate origin) | **PASS — SOFTWARE** | `phase3_provision_test.go` (15 adversarial paths) |
| 30c | An ordinary re-rate preserves counters and the generation (`tc class change`, never delete+add) | **PASS — SOFTWARE** | `internal/shape/shape_staged_test.go`; `phase3_provision_test.go` |
| 31 | Live read-only PMS protocol verification | **PENDING — LIVE INCREMENT 9** | operator-executed; never simulated |
| 32 | Live-dark deployment, reboot drill, rollback rehearsal, flags-OFF confirmation | **PENDING — LIVE INCREMENT 9** | runbook §2–§5 |
| 33 | Gate-P per-service EXECUTE grants and role separation | **OUT OF SCOPE BY APPROVED CONTRACT** | separately gated; zero runtime grants while dark |
| 34 | Paid access, financial posting, `PS`/`PA`, implicit FX, programmatic reversal | **OUT OF SCOPE BY APPROVED CONTRACT** | refused in code (`ErrSettlementRequired`) |
| 35 | Phase 4 | **OUT OF SCOPE BY APPROVED CONTRACT** | not started |

## 7. Production and guest impact

**Zero.** No appliance, production database or PMS was contacted at any point. Migration 0010 is undeployed.
All Phase-3 flags are OFF and their defaults are OFF in code. PR #6 is open and unmerged.

## 8. Rollback status

Two independent steps, both rehearsed on every change by the lifecycle gate:

- **Restore the previous release** (binaries + Hotel-Admin bundle). Usually sufficient — the schema is additive
  and inert while dark.
- **Remove the schema**: `0010_phase3_stay_resolution.down.sql` drops every table, trigger and controlled
  function the up script creates and removes its ledger row. The preflight asserts that coverage on every
  build, so a rollback cannot silently leave executable functions behind.

Full procedure and post-rollback confirmation queries: `docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md` §5.

## 9. Security and isolation results

- **Zero runtime privilege while dark**: every service role (`svc_scd`, `svc_edged`, `svc_portald`,
  `svc_acctd`, `svc_pmsd`) holds no `iam_v2` table or function privilege; gate-asserted.
- **Controlled-writer boundary**: a non-owner holding real table DML is refused raw status updates, forged
  history inserts, and direct grace-policy writes even with a correctly computed `config_version + 1`.
- **No guest PII in resolution evidence**: outcome code, guest-network id and a boolean `resolved` only —
  pinned by a test on the response type itself.
- **Guest portal is not an oracle**: every non-success is byte-identical, including HTTP status.
- **RBAC**: Phase-3 resources are gated in edged's authoritative matrix and mirrored in the UI hint matrix;
  read-only roles cannot publish policy, and unrelated roles cannot read the evidence at all.
- **Append-only histories**: entitlement transitions, device intervals, session bindings, watermarks, delayed
  samples and alert actions all reject `UPDATE`/`DELETE` except their one permitted mutation.

## 10. Complete generated changed-file manifest

> Embedded verbatim from `docs/manifests/Phase3-change-manifest.md` at delivery time. The evidence
> artifact's manifest-parity check confirms this equals the standalone generated manifest.

# Changed-file manifest (generated - do not hand-edit)

- **Base commit:** `ffb68e1ad325f5dd6d2096f2e30a782f8caef059`
- **HEAD commit:** `d8c23b623d5a37a4df46337db791c4613b7e4493`
- **Provenance (generation HEAD = inventory_head):** `d8c23b623d5a37a4df46337db791c4613b7e4493`  ·  path/status set covers the complete `base..delivery_head` diff (delivery_head = this staged content once committed).
- **Branch:** `phase/3-stay-resolution-grace`
- **Remote branch:** `origin/phase/3-stay-resolution-grace`
- **Changed files:** 225
- **Generated by:** `tools/generate-change-manifest.py ffb68e1ad325..STAGED`

## Files

| Path | Classification | Git status | Domain | Workstream | Rollback | Purpose (last commit subject in range) |
|---|---|---|---|---|---|---|
| `.github/workflows/phase3-software.yml` | CREATED | `A` | configuration | CI | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `.gitignore` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 3 Â§7 correction: make the Software CI the TRUE full same-HEAD gate with an uploaded evidence artifact |
| `data-plane/cmd/acctd/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/acctd/phase3.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/cmd/acctd/phase3_accounting.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/cmd/acctd/phase3_boundary_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `data-plane/cmd/acctd/phase3_envelope.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/acctd/phase3_envelope_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/acctd/phase3_health_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/cmd/acctd/phase3_ingest_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `data-plane/cmd/acctd/phase3_pass_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `data-plane/cmd/acctd/phase3_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/cmd/edged/auth.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§6 (backend): the PMS interface admin surface |
| `data-plane/cmd/edged/health_checks.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/edged/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§6 (backend): the PMS interface admin surface |
| `data-plane/cmd/edged/phase3_api_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (backend): the PMS interface admin surface |
| `data-plane/cmd/edged/phase3_grace_contract_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 2 items 1-4 (inventory_head): mandatory grace package, ONE shared exact validator, typed package selector, mandatory DB-level preconditions: gate 320/320 |
| `data-plane/cmd/edged/phase3_interfaces_api_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (backend): the PMS interface admin surface |
| `data-plane/cmd/edged/phase3_selector_contract_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/cmd/edged/resources_phase3.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/cmd/edged/resources_phase3_interfaces.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (backend): the PMS interface admin surface |
| `data-plane/cmd/edged/resources_phase3_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48 |
| `data-plane/cmd/netd/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§8/Â§9: self-audit findings, and the authoritative state brought up to date |
| `data-plane/cmd/netd/phase3_classstate.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `data-plane/cmd/netd/phase3_control.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/netd/phase3_mode.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/netd/phase3_mode_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/netd/phase3_origin.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `data-plane/cmd/netd/phase3_peer_linux.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/netd/phase3_peer_other.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/netd/phase3_provision.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `data-plane/cmd/netd/phase3_provision_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `data-plane/cmd/netd/phase3_shaping.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `data-plane/cmd/netd/phase3_shaping_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `data-plane/cmd/pmsd/main.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/portald/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/portald/pms_phase3.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 guest-portal uniform non-success contract (inventory_head): byte-identical failure responses, no oracle, audit reasons kept server-side |
| `data-plane/cmd/portald/pms_phase3_budget.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/portald/pms_phase3_budget_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/portald/pms_phase3_handlers.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/portald/pms_phase3_handlers_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/portald/pms_phase3_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 guest-portal uniform non-success contract (inventory_head): byte-identical failure responses, no oracle, audit reasons kept server-side |
| `data-plane/cmd/portald/social_handlers.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 guest-portal uniform non-success contract (inventory_head): byte-identical failure responses, no oracle, audit reasons kept server-side |
| `data-plane/cmd/portald/templates.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/scd/main.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/scd/otp_handlers.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/cmd/scd/phase3_atomicity_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/scd/phase3_auth.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/scd/phase3_auth_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `data-plane/cmd/scd/phase3_offers.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/internal/assignment/registry_store.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/assignment/registry_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/authctx/authctx.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/authctx/authctx_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Increment-7 Checkout historical-boundary + emergency-catalog + policy-consistency corrections (inventory_head): PG16-green + gate 157/157 |
| `data-plane/internal/authctx/authctx_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Auth Context lock-order + evidence-version enforcement + UUID pin validation (inventory_head): PG16-green + lifecycle-gate 131/131 |
| `data-plane/internal/checkout/checkout.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/checkout/checkout_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/internal/checkout/f_flows_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/enforce/enforce.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/enforce/enforce_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 netd shaping plan + acctd expiry enforcement (inventory_head): derived plan, true-time window/quota endings with revocation: PG16-green |
| `data-plane/internal/grace/grace.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 corrections: REJECT_NEW_DEVICE (no limit exception) + complete Auth Context pin set (inventory_head); lifecycle-gate 121/121 + PG16-green + race-green |
| `data-plane/internal/grace/grace_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 corrections: REJECT_NEW_DEVICE (no limit exception) + complete Auth Context pin set (inventory_head); lifecycle-gate 121/121 + PG16-green + race-green |
| `data-plane/internal/hwid/hwid.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/iamv2/commerce_domain.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/internal/iamv2/commerce_pms_eligibility_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/internal/iamv2/commerce_repo_pg.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/iamv2/commerce_validate.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `data-plane/internal/iamv2/pms_config.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 2 (inventory_head): migration 0010 + pms_config flags + machine-grounded gap audit |
| `data-plane/internal/iamv2/pms_config_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 2 (inventory_head): migration 0010 + pms_config flags + machine-grounded gap audit |
| `data-plane/internal/iamv2/repo_pg.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/identity/identity.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/livez/livez.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `data-plane/internal/localkeys/localkeys.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 CI-stability: fix internal/localkeys.EnsureGeneration concurrent mid-write flake (inventory_head) |
| `data-plane/internal/metrics/metrics.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/notifyloader/loader.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/pms/apaleo_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pms/fias_wire.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 hardening items 1-6 (inventory_head): strict FIAS parser, duplicate-field fail-closed, GuestName removed, atomic gap/resync txn, one serialized protocol writer; race + PG16 green |
| `data-plane/internal/pms/mews.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pms/mews_test.go` | MODIFIED | `M` | tests/tooling | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pms/pms.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pms/protel_fias.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pms/stub.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pmsd/adapter_fias.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green |
| `data-plane/internal/pmsd/adapter_fias_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green |
| `data-plane/internal/pmsd/adapter_frames_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 CI-stability (inventory_head): align Â§F write-failure + malformed-domain tests with the Â§G initial-DR flow |
| `data-plane/internal/pmsd/applier.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 correction item 1 (inventory_head): pmsd Stay-Event application worker composition root + process-level tests |
| `data-plane/internal/pmsd/applier_supervisor.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/internal/pmsd/applier_supervisor_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/internal/pmsd/applier_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 2 items 5-6 (inventory_head): synchronous fail-closed applier construction + supervised interface reconciliation |
| `data-plane/internal/pmsd/assignment.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pmsd/barrier_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green |
| `data-plane/internal/pmsd/errcodes.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pmsd/errcodes_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pmsd/events.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green |
| `data-plane/internal/pmsd/fias_adapter.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 (inventory_head): pmsd read-only PMS connector daemon (ADR-0001), DARK |
| `data-plane/internal/pmsd/lockkey.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (inventory_head): crypto lock key + typed error vocabulary + bounded event queue; pmsd race-green |
| `data-plane/internal/pmsd/lockkey_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (inventory_head): crypto lock key + typed error vocabulary + bounded event queue; pmsd race-green |
| `data-plane/internal/pmsd/pg.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/pmsd/pg_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§9 credential_mode NONE + Migration-0010 credential-aware pin coherence (inventory_head): truthful no-auth Protel FIAS; race-green + lifecycle-gate 121/121 + PG16-green |
| `data-plane/internal/pmsd/pmsd.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 2 items 5-6 (inventory_head): synchronous fail-closed applier construction + supervised interface reconciliation |
| `data-plane/internal/pmsd/pmsd_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§9 credential_mode NONE + Migration-0010 credential-aware pin coherence (inventory_head): truthful no-auth Protel FIAS; race-green + lifecycle-gate 121/121 + PG16-green |
| `data-plane/internal/pmsd/queue.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 hardening items 1-6 (inventory_head): strict FIAS parser, duplicate-field fail-closed, GuestName removed, atomic gap/resync txn, one serialized protocol writer; race + PG16 green |
| `data-plane/internal/pmsd/queue_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 hardening Â§A-Â§D CI-stability (inventory_head): fix benign measurement race in linearizable-close test |
| `data-plane/internal/pmsd/secret.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (backend): the PMS interface admin surface |
| `data-plane/internal/pmsd/secret_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§9-Â§16 COMPLETE: owner-bound AES-GCM AAD (inventory_head); connector hardening finished, race-green |
| `data-plane/internal/pmsd/strict_parse.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (inventory_head): strict-parse every inbound frame, prompt bounded shutdown, context-aware serialized writer, per-frame write-failure coverage; race-green |
| `data-plane/internal/pmsd/strict_parse_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 hardening items 1-6 (inventory_head): strict FIAS parser, duplicate-field fail-closed, GuestName removed, atomic gap/resync txn, one serialized protocol writer; race + PG16 green |
| `data-plane/internal/pmsd/supervisor.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `data-plane/internal/pmsd/worker.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§9 credential_mode NONE + Migration-0010 credential-aware pin coherence (inventory_head): truthful no-auth Protel FIAS; race-green + lifecycle-gate 121/121 + PG16-green |
| `data-plane/internal/pmsd/writer.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (inventory_head): strict-parse every inbound frame, prompt bounded shutdown, context-aware serialized writer, per-frame write-failure coverage; race-green |
| `data-plane/internal/pmsd/writer_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (inventory_head): strict-parse every inbound frame, prompt bounded shutdown, context-aware serialized writer, per-frame write-failure coverage; race-green |
| `data-plane/internal/pmsguard/guard.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/pmsresolve/fanout.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/pmsresolve/fanout_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 strict resolver fan-out + idempotent auth_resolutions (inventory_head): complete-vector concurrency, fail-closed indeterminacy, >=24 concurrent resolutions: PG16-green |
| `data-plane/internal/pmsresolve/resolve.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | @ Phase 3 Increment 5 foundation (inventory_head): STRICT multi-PMS resolver decision core (internal/pmsresolve), D1â€“D11 |
| `data-plane/internal/pmsresolve/resolve_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 Increment 5 foundation (inventory_head): STRICT multi-PMS resolver decision core (internal/pmsresolve), D1â€“D11 |
| `data-plane/internal/shape/shape.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `data-plane/internal/shape/shape_staged_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `data-plane/internal/shapeplan/plan.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `data-plane/internal/sms/twilio.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/social/google.go` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green |
| `data-plane/internal/stayengine/checkout_slice_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `data-plane/internal/stayengine/pg.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/stayengine/pg_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Increment-7 Checkout scorecard-gap closure (inventory_head): no old path, fail-closed, mandatory lineage, structural DB lineage, ordering, >=24 integrated concurrency, late-stage rollback: PG16-green + gate 225/225 |
| `data-plane/internal/stayengine/resolve.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 sharers + folios + source conflicts (inventory_head): legal multi-occupancy with one primary, contradictory payloads and folio claims to review: PG16-green |
| `data-plane/internal/stayengine/resolve_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | @ Phase 3 Increment 4 foundation (inventory_head): deterministic Stay-resolution decision core (internal/stayengine) |
| `data-plane/internal/stayengine/sharers.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 sharers + folios + source conflicts (inventory_head): legal multi-occupancy with one primary, contradictory payloads and folio claims to review: PG16-green |
| `data-plane/internal/staygrant/staygrant.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/staygrant/staygrant_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 atomic Auth-Context/Quote/Purchase/Entitlement grant + controlled device authorization (inventory_head): PG16-green + gate 267/267 |
| `data-plane/internal/writerguard/writerguard.go` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/internal/writerguard/writerguard_integration_test.go` | CREATED | `A` | tests/tooling | RUNTIME | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/migrations/0010_phase3_stay_resolution.down.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `data-plane/migrations/0010_phase3_stay_resolution.up.sql` | CREATED | `A` | database | MIGRATIONS | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `deploy/systemd/stayconnect-pmsd.service` | CREATED | `A` | configuration | DEPLOY | rollback REMOVES it | @ Phase 3 increment 3 (inventory_head): pmsd read-only PMS connector daemon (ADR-0001), DARK |
| `docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `docs/PHASE3_SCOPE_AMENDMENT_PROPOSAL.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `docs/architecture/Phase3-Controlled-Writer-Privilege-Manifest.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3 controlled-writer manifest documentation sync (inventory_head): doc-only |
| `docs/architecture/Phase3-Privilege-Matrix.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `docs/architecture/StayConnect-IAM-Phase0-Contract.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 3: governance activity transition T0016 (software candidate awaiting Increment 9), doc sync |
| `docs/architecture/StayConnect-IAM-Phase1A-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 3: governance activity transition T0016 (software candidate awaiting Increment 9), doc sync |
| `docs/architecture/StayConnect-IAM-Phase1B-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 3: governance activity transition T0016 (software candidate awaiting Increment 9), doc sync |
| `docs/architecture/StayConnect-IAM-Phase2-Plan.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `docs/architecture/StayConnect-IAM-Phase3-Plan.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `docs/architecture/adr/ADR-0001-pmsd-connector-ownership.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `docs/architecture/adr/ADR-0002-phase3-single-shaping-owner.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants |
| `docs/context/StayConnect-IAM-Handoff.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | Phase 3: governance activity transition T0016 (software candidate awaiting Increment 9), doc sync |
| `docs/evidence/StayConnect-IAM-Phase3-Schema-Gap-Audit.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `docs/manifests/Phase3-change-manifest.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `docs/reports/StayConnect-IAM-Phase2-Final-Report.md` | MODIFIED | `M` | documentation | DOCS | rollback RESTORES prior content | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `docs/reports/StayConnect-IAM-Phase3-Final-Report.md` | CREATED | `A` | documentation | DOCS | rollback REMOVES it | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase-evidence/GIT_STAT_0e1c7dd.txt` | EXPORTED | `A` | export | EXPORT | rollback REMOVES it | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase-evidence/GIT_STAT_9a1f356.txt` | EXPORTED | `D` | export | EXPORT | rollback RESTORES it | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase-evidence/Phase2-change-manifest.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/phase-evidence/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase-evidence/StayConnect-IAM-Phase2-Final-Report.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/phase-evidence/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/phase-evidence/governance/decision-register.json` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer |
| `exports/chatgpt/phase-evidence/tools/project-state.py` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/phase-evidence/tools/validate-project-state.sh` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase1b-planning/MANIFEST.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase1b-planning/PACK_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase1b-planning/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/phase1b-planning/StayConnect-IAM-Phase1B-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/00-START-HERE.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3: governance activity transition T0016 (software candidate awaiting Increment 9), doc sync |
| `exports/chatgpt/stayconnectenterprise/MANIFEST.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/PROJECT-INSTRUCTIONS.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3: governance activity transition T0016 (software candidate awaiting Increment 9), doc sync |
| `exports/chatgpt/stayconnectenterprise/Phase2-change-manifest.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/stayconnectenterprise/Phase3-Privilege-Matrix.md` | EXPORTED | `A` | export | EXPORT | rollback REMOVES it | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/stayconnectenterprise/Phase3-change-manifest.md` | EXPORTED | `A` | export | EXPORT | rollback REMOVES it | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Handoff.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase0-Contract.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase1A-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase1B-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase2-Final-Report.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase2-Plan.md` | EXPORTED | `M` | export | EXPORT | rollback RESTORES prior content | @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer |
| `exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase3-Plan.md` | EXPORTED | `A` | export | EXPORT | rollback REMOVES it | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `governance/decision-register.json` | MODIFIED | `M` | governance | GOVERNANCE | rollback RESTORES prior content | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `governance/project-state.json` | MODIFIED | `M` | governance | GOVERNANCE | rollback RESTORES prior content | Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest |
| `governance/transitions/T0015.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `governance/transitions/T0016.json` | CREATED | `A` | governance | GOVERNANCE | rollback REMOVES it | Phase 3: governance activity transition T0016 (software candidate awaiting Increment 9), doc sync |
| `hotel-admin/app/(app)/checkout-grace/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 1 (inventory_head): controlled alert lifecycle + governed grace publication + NOT VALID boundary CHECK; real API+PG contract tests: gate 310/310 |
| `hotel-admin/app/(app)/operational-alerts/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 1 (inventory_head): controlled alert lifecycle + governed grace publication + NOT VALID boundary CHECK; real API+PG contract tests: gate 310/310 |
| `hotel-admin/app/(app)/pms-interfaces/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/app/(app)/pms-resolutions/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/app/(app)/pms-routing/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/app/(app)/pms-source-conflicts/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/app/(app)/stay-events/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48 |
| `hotel-admin/app/(app)/stays/page.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48 |
| `hotel-admin/components/nav.tsx` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/components/phase3/checkout-grace-form.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `hotel-admin/components/phase3/operational-alerts-view.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 1 (inventory_head): controlled alert lifecycle + governed grace publication + NOT VALID boundary CHECK; real API+PG contract tests: gate 310/310 |
| `hotel-admin/e2e/phase3-guest-portal-resilience.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable |
| `hotel-admin/e2e/phase3-guest-portal.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `hotel-admin/e2e/phase3-pms-interfaces.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/e2e/phase3-stays-grace.spec.ts` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `hotel-admin/lib/api.ts` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/lib/roles.ts` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48 |
| `hotel-admin/package-lock.json` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§7: regenerate the hotel-admin lockfile on Linux so `npm ci` resolves on the runner |
| `hotel-admin/package.json` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Â§7: fix `npm ci` in the full gate â€” drop the unused, conflicting @vitejs/plugin-react |
| `hotel-admin/playwright.config.ts` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Hotel-Admin E2E + accessibility (inventory_head): 7 Playwright specs over mocked edged, named controls and labelled filters proven |
| `hotel-admin/test/nav.test.tsx` | MODIFIED | `M` | runtime | RUNTIME | rollback RESTORES prior content | Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48 |
| `hotel-admin/test/phase3-interface-pages.test.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface |
| `hotel-admin/test/phase3-pages.test.tsx` | CREATED | `A` | runtime | RUNTIME | rollback REMOVES it | Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence |
| `iam_v2_scratch/00_platform_fixture.sql` | MODIFIED | `M` | other | OTHER | rollback RESTORES prior content | Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice |
| `iam_v2_scratch/phase3_0010_lifecycle.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family |
| `scripts/ci/go-test-counted.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent |
| `scripts/ci/gofmt-check.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent |
| `scripts/ci/gojson_summary.py` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent |
| `scripts/ci/pg-gate.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent |
| `scripts/ci/phase3_evidence.py` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `scripts/ci/step.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent |
| `scripts/edge-migrate.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green |
| `scripts/phase3-evidence.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 Â§7: downloadable evidence artifact with a SHA-256 integrity manifest |
| `scripts/phase3-preflight.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `scripts/pmsd-pg-integration.sh` | CREATED | `A` | other | OTHER | rollback REMOVES it | Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority |
| `tools/embed-report-manifest.py` | CREATED | `A` | tests/tooling | TOOLING | rollback REMOVES it | Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation |
| `tools/project-state.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards |
| `tools/tests/project_state_validator/run_mutations.py` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 3: update mutation-suite fixtures for the new project-state values |
| `tools/validate-project-state.sh` | MODIFIED | `M` | tests/tooling | TOOLING | rollback RESTORES prior content | Phase 3: accept the Live-Increment-9 next-action phrasing in the zero-stale validator |

## Total diff statistics (`git diff --stat`)
```text
 .github/workflows/phase3-software.yml              |  218 ++
 .gitignore                                         |    9 +
 data-plane/cmd/acctd/main.go                       |   69 +-
 data-plane/cmd/acctd/phase3.go                     |  372 +++
 data-plane/cmd/acctd/phase3_accounting.go          |  231 ++
 .../cmd/acctd/phase3_boundary_integration_test.go  |  843 ++++++
 data-plane/cmd/acctd/phase3_envelope.go            |   92 +
 data-plane/cmd/acctd/phase3_envelope_test.go       |  176 ++
 data-plane/cmd/acctd/phase3_health_test.go         |  110 +
 .../cmd/acctd/phase3_ingest_integration_test.go    |  163 ++
 .../cmd/acctd/phase3_pass_integration_test.go      |  405 +++
 data-plane/cmd/acctd/phase3_test.go                |  129 +
 data-plane/cmd/edged/auth.go                       |   23 +-
 data-plane/cmd/edged/health_checks.go              |    6 +
 data-plane/cmd/edged/main.go                       |   36 +
 .../cmd/edged/phase3_api_integration_test.go       |  425 +++
 data-plane/cmd/edged/phase3_grace_contract_test.go |  219 ++
 .../phase3_interfaces_api_integration_test.go      |  580 ++++
 .../cmd/edged/phase3_selector_contract_test.go     |  211 ++
 data-plane/cmd/edged/resources_phase3.go           |  598 ++++
 .../cmd/edged/resources_phase3_interfaces.go       |  742 +++++
 data-plane/cmd/edged/resources_phase3_test.go      |   74 +
 data-plane/cmd/netd/main.go                        |   82 +-
 data-plane/cmd/netd/phase3_classstate.go           |  282 ++
 data-plane/cmd/netd/phase3_control.go              |  141 +
 data-plane/cmd/netd/phase3_mode.go                 |   67 +
 data-plane/cmd/netd/phase3_mode_test.go            |   75 +
 data-plane/cmd/netd/phase3_origin.go               |   93 +
 data-plane/cmd/netd/phase3_peer_linux.go           |   37 +
 data-plane/cmd/netd/phase3_peer_other.go           |   16 +
 data-plane/cmd/netd/phase3_provision.go            |  170 ++
 data-plane/cmd/netd/phase3_provision_test.go       |  481 ++++
 data-plane/cmd/netd/phase3_shaping.go              |  548 ++++
 data-plane/cmd/netd/phase3_shaping_test.go         | 1357 ++++++++++
 data-plane/cmd/pmsd/main.go                        |  139 +
 data-plane/cmd/portald/main.go                     |    8 +
 data-plane/cmd/portald/pms_phase3.go               |  101 +
 data-plane/cmd/portald/pms_phase3_budget.go        |  107 +
 data-plane/cmd/portald/pms_phase3_budget_test.go   |  319 +++
 data-plane/cmd/portald/pms_phase3_handlers.go      |  231 ++
 data-plane/cmd/portald/pms_phase3_handlers_test.go |  228 ++
 data-plane/cmd/portald/pms_phase3_test.go          |  126 +
 data-plane/cmd/portald/social_handlers.go          |    4 +-
 data-plane/cmd/portald/templates.go                |  126 +-
 data-plane/cmd/scd/main.go                         |   21 +
 data-plane/cmd/scd/otp_handlers.go                 |   10 +
 .../cmd/scd/phase3_atomicity_integration_test.go   |  342 +++
 data-plane/cmd/scd/phase3_auth.go                  |  600 ++++
 data-plane/cmd/scd/phase3_auth_integration_test.go |  757 ++++++
 data-plane/cmd/scd/phase3_offers.go                |  227 ++
 data-plane/internal/assignment/registry_store.go   |    8 +-
 data-plane/internal/assignment/registry_test.go    |    7 +-
 data-plane/internal/authctx/authctx.go             |  308 +++
 .../internal/authctx/authctx_integration_test.go   |  726 +++++
 data-plane/internal/authctx/authctx_test.go        |   90 +
 data-plane/internal/checkout/checkout.go           |  791 ++++++
 .../internal/checkout/checkout_integration_test.go |  959 +++++++
 .../internal/checkout/f_flows_integration_test.go  |  174 ++
 data-plane/internal/enforce/enforce.go             |  193 ++
 .../internal/enforce/enforce_integration_test.go   |  279 ++
 data-plane/internal/grace/grace.go                 |  159 ++
 data-plane/internal/grace/grace_test.go            |  126 +
 data-plane/internal/hwid/hwid.go                   |    4 +-
 data-plane/internal/iamv2/commerce_domain.go       |  197 +-
 .../iamv2/commerce_pms_eligibility_test.go         |  179 ++
 data-plane/internal/iamv2/commerce_repo_pg.go      |   40 +-
 data-plane/internal/iamv2/commerce_validate.go     |   67 +-
 data-plane/internal/iamv2/pms_config.go            |  120 +
 data-plane/internal/iamv2/pms_config_test.go       |  157 ++
 data-plane/internal/iamv2/repo_pg.go               |    6 +
 data-plane/internal/identity/identity.go           |   12 +-
 data-plane/internal/livez/livez.go                 |   24 +
 data-plane/internal/localkeys/localkeys.go         |   38 +-
 data-plane/internal/metrics/metrics.go             |   64 +-
 data-plane/internal/notifyloader/loader.go         |    8 +-
 data-plane/internal/pms/apaleo_test.go             |   25 +-
 data-plane/internal/pms/fias_wire.go               |   73 +
 data-plane/internal/pms/mews.go                    |    6 +-
 data-plane/internal/pms/mews_test.go               |   23 +-
 data-plane/internal/pms/pms.go                     |   49 +-
 data-plane/internal/pms/protel_fias.go             |   12 +-
 data-plane/internal/pms/stub.go                    |    8 +-
 data-plane/internal/pmsd/adapter_fias.go           |  286 ++
 data-plane/internal/pmsd/adapter_fias_test.go      |  542 ++++
 data-plane/internal/pmsd/adapter_frames_test.go    |  257 ++
 data-plane/internal/pmsd/applier.go                |  109 +
 data-plane/internal/pmsd/applier_supervisor.go     |  135 +
 .../internal/pmsd/applier_supervisor_test.go       |  341 +++
 data-plane/internal/pmsd/applier_test.go           |  228 ++
 data-plane/internal/pmsd/assignment.go             |  103 +
 data-plane/internal/pmsd/barrier_test.go           |  150 +
 data-plane/internal/pmsd/errcodes.go               |  215 ++
 data-plane/internal/pmsd/errcodes_test.go          |  136 +
 data-plane/internal/pmsd/events.go                 |  273 ++
 data-plane/internal/pmsd/fias_adapter.go           |   58 +
 data-plane/internal/pmsd/lockkey.go                |   86 +
 data-plane/internal/pmsd/lockkey_test.go           |   74 +
 data-plane/internal/pmsd/pg.go                     |  385 +++
 data-plane/internal/pmsd/pg_integration_test.go    |  613 +++++
 data-plane/internal/pmsd/pmsd.go                   |  541 ++++
 data-plane/internal/pmsd/pmsd_test.go              |  660 +++++
 data-plane/internal/pmsd/queue.go                  |  204 ++
 data-plane/internal/pmsd/queue_test.go             |  266 ++
 data-plane/internal/pmsd/secret.go                 |  144 +
 data-plane/internal/pmsd/secret_test.go            |   62 +
 data-plane/internal/pmsd/strict_parse.go           |  177 ++
 data-plane/internal/pmsd/strict_parse_test.go      |  150 +
 data-plane/internal/pmsd/supervisor.go             |  156 ++
 data-plane/internal/pmsd/worker.go                 |  342 +++
 data-plane/internal/pmsd/writer.go                 |  154 ++
 data-plane/internal/pmsd/writer_test.go            |  256 ++
 data-plane/internal/pmsguard/guard.go              |   16 +-
 data-plane/internal/pmsresolve/fanout.go           |  228 ++
 .../internal/pmsresolve/fanout_integration_test.go |  327 +++
 data-plane/internal/pmsresolve/resolve.go          |   96 +
 data-plane/internal/pmsresolve/resolve_test.go     |  112 +
 data-plane/internal/shape/shape.go                 |  252 +-
 data-plane/internal/shape/shape_staged_test.go     |  188 ++
 data-plane/internal/shapeplan/plan.go              |  175 ++
 data-plane/internal/sms/twilio.go                  |    8 +-
 data-plane/internal/social/google.go               |   14 +-
 .../stayengine/checkout_slice_integration_test.go  |  681 +++++
 data-plane/internal/stayengine/pg.go               |  337 +++
 .../internal/stayengine/pg_integration_test.go     |  198 ++
 data-plane/internal/stayengine/resolve.go          |  131 +
 data-plane/internal/stayengine/resolve_test.go     |  106 +
 data-plane/internal/stayengine/sharers.go          |  190 ++
 data-plane/internal/staygrant/staygrant.go         |  239 ++
 .../staygrant/staygrant_integration_test.go        |  436 +++
 data-plane/internal/writerguard/writerguard.go     |  333 +++
 .../writerguard/writerguard_integration_test.go    |  233 ++
 .../0010_phase3_stay_resolution.down.sql           |  213 ++
 .../migrations/0010_phase3_stay_resolution.up.sql  | 2863 ++++++++++++++++++++
 deploy/systemd/stayconnect-pmsd.service            |   41 +
 docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md     |  224 ++
 docs/PHASE3_SCOPE_AMENDMENT_PROPOSAL.md            |  114 +
 .../StayConnect-IAM-Phase2-Live-Dark-Acceptance.md |    2 +-
 .../Phase3-Controlled-Writer-Privilege-Manifest.md |   92 +
 docs/architecture/Phase3-Privilege-Matrix.md       |   34 +
 .../StayConnect-IAM-Phase0-Contract.md             |   16 +-
 docs/architecture/StayConnect-IAM-Phase1A-Plan.md  |   12 +-
 docs/architecture/StayConnect-IAM-Phase1B-Plan.md  |   12 +-
 docs/architecture/StayConnect-IAM-Phase2-Plan.md   |    2 +-
 docs/architecture/StayConnect-IAM-Phase3-Plan.md   |  185 ++
 .../adr/ADR-0001-pmsd-connector-ownership.md       |   53 +
 .../adr/ADR-0002-phase3-single-shaping-owner.md    |  156 ++
 docs/context/StayConnect-IAM-Handoff.md            |   16 +-
 .../StayConnect-IAM-Phase3-Schema-Gap-Audit.md     |  109 +
 docs/manifests/Phase3-change-manifest.md           |  666 +++++
 .../reports/StayConnect-IAM-Phase2-Final-Report.md |    4 +-
 .../reports/StayConnect-IAM-Phase3-Final-Report.md | 1093 ++++++++
 .../StayConnectEnterprise-ChatGPT-Project-Pack.zip |  Bin 250675 -> 284179 bytes
 .../StayConnectEnterprise-Phase-Evidence-Pack.zip  |  Bin 101471 -> 104127 bytes
 ...StayConnectEnterprise-Phase1B-Planning-Pack.zip |  Bin 41921 -> 42082 bytes
 .../chatgpt/phase-evidence/GIT_STAT_0e1c7dd.txt    |    4 +
 .../chatgpt/phase-evidence/GIT_STAT_9a1f356.txt    |    4 -
 exports/chatgpt/phase-evidence/PACK_SHA256SUMS.txt |   16 +-
 .../phase-evidence/Phase2-change-manifest.md       |   13 +-
 .../REPOSITORY_ARTIFACT_SHA256SUMS.txt             |    6 +-
 .../StayConnect-IAM-Phase2-Final-Report.md         |    4 +-
 .../StayConnect-IAM-Phase2-Live-Dark-Acceptance.md |    2 +-
 .../governance/decision-register.json              |   19 +-
 .../chatgpt/phase-evidence/tools/project-state.py  |   49 +-
 .../phase-evidence/tools/validate-project-state.sh |    2 +-
 exports/chatgpt/phase1b-planning/MANIFEST.md       |    2 +-
 .../chatgpt/phase1b-planning/PACK_SHA256SUMS.txt   |    6 +-
 .../REPOSITORY_ARTIFACT_SHA256SUMS.txt             |   18 +-
 .../StayConnect-IAM-Phase1B-Plan.md                |   12 +-
 .../chatgpt/stayconnectenterprise/00-START-HERE.md |   12 +-
 exports/chatgpt/stayconnectenterprise/MANIFEST.md  |   67 +-
 .../stayconnectenterprise/PROJECT-INSTRUCTIONS.md  |   12 +-
 .../Phase2-change-manifest.md                      |   13 +-
 .../Phase3-Privilege-Matrix.md                     |   34 +
 .../Phase3-change-manifest.md                      |  664 +++++
 .../StayConnect-IAM-Handoff.md                     |   16 +-
 .../StayConnect-IAM-Phase0-Contract.md             |   16 +-
 .../StayConnect-IAM-Phase1A-Plan.md                |   12 +-
 .../StayConnect-IAM-Phase1B-Plan.md                |   12 +-
 .../StayConnect-IAM-Phase2-Final-Report.md         |    4 +-
 .../StayConnect-IAM-Phase2-Live-Dark-Acceptance.md |    2 +-
 .../StayConnect-IAM-Phase2-Plan.md                 |    2 +-
 .../StayConnect-IAM-Phase3-Plan.md                 |  185 ++
 governance/decision-register.json                  |   19 +-
 governance/project-state.json                      |   73 +-
 governance/transitions/T0015.json                  |   18 +
 governance/transitions/T0016.json                  |   27 +
 hotel-admin/app/(app)/checkout-grace/page.tsx      |   19 +
 hotel-admin/app/(app)/operational-alerts/page.tsx  |   21 +
 hotel-admin/app/(app)/pms-interfaces/page.tsx      |  535 ++++
 hotel-admin/app/(app)/pms-resolutions/page.tsx     |  139 +
 hotel-admin/app/(app)/pms-routing/page.tsx         |  125 +
 .../app/(app)/pms-source-conflicts/page.tsx        |   98 +
 hotel-admin/app/(app)/stay-events/page.tsx         |  112 +
 hotel-admin/app/(app)/stays/page.tsx               |  161 ++
 hotel-admin/components/nav.tsx                     |   15 +
 .../components/phase3/checkout-grace-form.tsx      |  234 ++
 .../components/phase3/operational-alerts-view.tsx  |  156 ++
 .../e2e/phase3-guest-portal-resilience.spec.ts     |  414 +++
 hotel-admin/e2e/phase3-guest-portal.spec.ts        |  211 ++
 hotel-admin/e2e/phase3-pms-interfaces.spec.ts      |  338 +++
 hotel-admin/e2e/phase3-stays-grace.spec.ts         |  290 ++
 hotel-admin/lib/api.ts                             |  116 +
 hotel-admin/lib/roles.ts                           |    8 +
 hotel-admin/package-lock.json                      |   97 +-
 hotel-admin/package.json                           |    1 -
 hotel-admin/playwright.config.ts                   |    2 +-
 hotel-admin/test/nav.test.tsx                      |   37 +
 hotel-admin/test/phase3-interface-pages.test.tsx   |  282 ++
 hotel-admin/test/phase3-pages.test.tsx             |  262 ++
 iam_v2_scratch/00_platform_fixture.sql             |   19 +-
 iam_v2_scratch/phase3_0010_lifecycle.sh            | 1063 ++++++++
 scripts/ci/go-test-counted.sh                      |   16 +
 scripts/ci/gofmt-check.sh                          |   15 +
 scripts/ci/gojson_summary.py                       |   58 +
 scripts/ci/pg-gate.sh                              |   22 +
 scripts/ci/phase3_evidence.py                      |  496 ++++
 scripts/ci/step.sh                                 |   26 +
 scripts/edge-migrate.sh                            |  251 ++
 scripts/phase3-evidence.sh                         |  127 +
 scripts/phase3-preflight.sh                        |  253 ++
 scripts/pmsd-pg-integration.sh                     |   59 +
 tools/embed-report-manifest.py                     |   49 +
 tools/project-state.py                             |   49 +-
 .../tests/project_state_validator/run_mutations.py |   69 +-
 tools/validate-project-state.sh                    |    2 +-
 225 files changed, 41802 insertions(+), 458 deletions(-)
```

## Working-tree status (`git status --short --untracked-files=all`)
```text
M  governance/decision-register.json
M  governance/project-state.json
```

## Commits in range (`git log --oneline <base>..HEAD`)
```text
d8c23b6 Phase 3: update mutation-suite fixtures for the new project-state values
1b818f7 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
0e1c7dd Phase 3: accept the Live-Increment-9 next-action phrasing in the zero-stale validator
c885f98 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer + report-embedded manifest
5529f08 Phase 3: governance activity transition T0016 (software candidate awaiting Increment 9), doc sync
4a9f602 Phase 3: accountable-before-forwarding class provisioning, and Zero-Stale documentation
1f407ca Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
762909c Phase 3 Â§9: stop the Final Report Â§13 from citing a frozen (stale) HEAD
e65e254 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
36c9deb Phase 3 Â§7: upload the evidence artifact from the dot-prefixed staging dir
66429e5 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
d57715d Phase 3 Â§7: regenerate the hotel-admin lockfile on Linux so `npm ci` resolves on the runner
7d7e4ea Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
e42f1cd Phase 3 Â§7: fix `npm ci` in the full gate â€” drop the unused, conflicting @vitejs/plugin-react
90f7cc1 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
4184bf3 Phase 3 Â§7: fix the full-gate CI â€” make the step recorder exec-bit-independent
3b39a6a Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
650f158 Phase 3 Â§7 correction: make the Software CI the TRUE full same-HEAD gate with an uploaded evidence artifact
e836807 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
99e8a1c Phase 3 Â§7: downloadable evidence artifact with a SHA-256 integrity manifest
afd4ee3 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
9297379 Phase 3 Â§8/Â§9: self-audit findings, and the authoritative state brought up to date
028d356 Phase 3 Â§6 (frontend): four Hotel-Admin pages over the PMS interface surface
987c5bf Phase 3 Â§6 (backend): the PMS interface admin surface
0e312e5 Phase 3 Â§5: extend the controlled-writer boundary over every authoritative family
7d8d9f2 Phase 3 Â§4: bound the guest portal's failure response time, and make a lost reply recoverable
ae19eb2 Phase 3: fix the rollback ordering defect and stop it recurring
7fe70f2 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
d68ec4d Phase 3: fix the failed migration gate; real PMS eligibility, published-Revision pinning, offer-bound grants
7c57e42 Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
51bafc0 Phase 3 (D15 / Option C): accounting attribution, source binding, temporal order, class origin, generation authority
de2829c Phase 3 (delivery_head): complete staged manifest + rebuilt packs + pointer
fb15bf0 Phase 3: durable accounting, netd shaping control plane, controlled-writer boundary, guest vertical slice
df041a2 Phase 3 (C): durable accounting checkpoints + absolute-counter controlled ingestion + netd class epochs
359cb59 @ Phase 3 accounting reopen + shaping closure (delivery_head): complete staged manifest + rebuilt packs + pointer
12f4173 Phase 3 accounting item 6 reopen corrected + shaping 7-8 closed (inventory_head): right session domain, restart-durable sample identity, wiring-level tests, truthful shaping health, legacy loop stands down
7218654 @ Phase 3 correction item 8 (delivery_head): complete staged manifest + rebuilt packs + pointer
7dc27e8 Phase 3 correction item 8 (inventory_head): ADR-0002 single shaping owner - netd applies, acctd derives and submits; structural preflight check
baa5596 @ Phase 3 correction item 7 (delivery_head): complete staged manifest + rebuilt packs + pointer
de24d2c Phase 3 correction item 7 (inventory_head): controlled Phase-3 accounting ingestion wired into acctd with 11 composition-root cases
9c8fbb4 @ Phase 3 corrections round 3 items 1-5 (delivery_head): complete staged manifest + rebuilt packs + pointer
b7c7991 Phase 3 corrections round 3 items 1-5 (inventory_head): required exact policy version, whole reserved catalog excluded, selector on the authoritative validator, complete metadata, honest reconcile + two-process evidence
3566d97 @ Phase 3 corrections round 2 items 5-6 (delivery_head): complete staged manifest + rebuilt packs + pointer
8ebce2c Phase 3 corrections round 2 items 5-6 (inventory_head): synchronous fail-closed applier construction + supervised interface reconciliation
3c9c73a @ Phase 3 corrections round 2 items 1-4 (delivery_head): complete staged manifest + rebuilt packs + pointer
f322c84 Phase 3 corrections round 2 items 1-4 (inventory_head): mandatory grace package, ONE shared exact validator, typed package selector, mandatory DB-level preconditions: gate 320/320
c01d361 @ Phase 3 correction item 4 (delivery_head): complete staged manifest + rebuilt packs + pointer
4eb9768 Phase 3 correction item 4 (inventory_head): enforce composed into acctd (true-time expiry + derived shaping reconciliation) with composition-root tests
a97800d @ Phase 3 correction item 1 (delivery_head): complete staged manifest + rebuilt packs + pointer
86bda6b Phase 3 correction item 1 (inventory_head): pmsd Stay-Event application worker composition root + process-level tests
cca5c75 @ Phase 3 corrections round 1 (delivery_head): complete staged manifest + rebuilt packs + pointer
8aee1f3 Phase 3 corrections round 1 (inventory_head): controlled alert lifecycle + governed grace publication + NOT VALID boundary CHECK; real API+PG contract tests: gate 310/310
3a95ec1 @ Phase 3 final report (delivery_head): complete staged manifest + rebuilt packs + pointer
32b7701 Phase 3 final report (inventory_head): 17-section dark acceptance candidate report, live Increment-9 evidence recorded PENDING
960ac3e @ Phase 3 Increment-9 offline tooling + runbook (delivery_head): complete staged manifest + rebuilt packs + pointer
5c6d143 Phase 3 Increment-9 offline tooling + runbook (inventory_head): preflight 11/11, evidence collector, deployment/rollback/reboot runbook
23d9d12 @ Phase 3 guest-portal uniform contract (delivery_head): complete staged manifest + rebuilt packs + pointer
b8f49f4 Phase 3 guest-portal uniform non-success contract (inventory_head): byte-identical failure responses, no oracle, audit reasons kept server-side
2cafd9c @ Phase 3 Hotel-Admin E2E + accessibility (delivery_head): complete staged manifest + rebuilt packs + pointer
1592850 Phase 3 Hotel-Admin E2E + accessibility (inventory_head): 7 Playwright specs over mocked edged, named controls and labelled filters proven
834650c @ Phase 3 Hotel-Admin surface (delivery_head): complete staged manifest + rebuilt packs + pointer
a1f0c4e Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48
7f75249 @ Phase 3 netd shaping plan + acctd enforcement (delivery_head): complete staged manifest + rebuilt packs + pointer
30757b1 Phase 3 netd shaping plan + acctd expiry enforcement (inventory_head): derived plan, true-time window/quota endings with revocation: PG16-green
76d2029 @ Phase 3 F1-F7 flow suite (delivery_head): complete staged manifest + rebuilt packs + pointer
d0f57e0 Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green
d18e09b @ Phase 3 sharers + folios + source conflicts (delivery_head): complete staged manifest + rebuilt packs + pointer
0927baa Phase 3 sharers + folios + source conflicts (inventory_head): legal multi-occupancy with one primary, contradictory payloads and folio claims to review: PG16-green
166ff5b @ Phase 3 strict resolver fan-out + idempotent resolutions (delivery_head): complete staged manifest + rebuilt packs + pointer
32b382b Phase 3 strict resolver fan-out + idempotent auth_resolutions (inventory_head): complete-vector concurrency, fail-closed indeterminacy, >=24 concurrent resolutions: PG16-green
09619e3 @ Phase 3 post-boundary revocation + accounting attribution (delivery_head): complete staged manifest + rebuilt packs + pointer
b8aeca1 Phase 3 post-boundary revocation + accounting attribution intervals/watermarks/delayed samples (inventory_head): PG16-green + gate 282/282
1ffba2b @ Phase 3 atomic grant + controlled device authorization (delivery_head): complete staged manifest + rebuilt packs + pointer
d976419 Phase 3 atomic Auth-Context/Quote/Purchase/Entitlement grant + controlled device authorization (inventory_head): PG16-green + gate 267/267
3010a70 @ Phase 3 bitemporal entitlement history (delivery_head): complete staged manifest + rebuilt packs + pointer
c018f84 Phase 3 bitemporal entitlement history (inventory_head): true effective_at + recorded_at, explicit supersession, boundary termination without clamping: PG16-green + gate 254/254
cd24425 @ Phase 3 Increment-7 Checkout scorecard-gap closure (delivery_head): complete staged manifest + rebuilt packs + pointer
362aecd Phase 3 Increment-7 Checkout scorecard-gap closure (inventory_head): no old path, fail-closed, mandatory lineage, structural DB lineage, ordering, >=24 integrated concurrency, late-stage rollback: PG16-green + gate 225/225
e43bd28 @ Phase 3 one-transaction Checkout slice (delivery_head): complete staged manifest + rebuilt packs + pointer
60191c3 Phase 3 vertical slice: ONE physical Stay-Event->Checkout transaction + exact event lineage (inventory_head): PG16-green + gate 225/225
56b29b7 @ Phase 3 controlled-writer manifest doc sync (delivery_head): complete staged manifest + rebuilt packs + pointer
324dbeb Phase 3 controlled-writer manifest documentation sync (inventory_head): doc-only
f82cef2 @ Phase 3 Increment-7 EXECUTE-only caller proof (delivery_head): complete staged manifest + rebuilt packs + pointer
de9c189 Phase 3 Increment-7 EXECUTE-only caller proof for the controlled-writer model (inventory_head): PG16-green + gate 225/225
7d01e72 @ Phase 3 Increment-7 config-DELETE + per-family writer-owner (delivery_head): complete staged manifest + rebuilt packs + pointer
d5263d7 Phase 3 Increment-7 config-DELETE + per-family writer-owner gaps (inventory_head): PG16-green + gate 209/209
73e6b5d @ Phase 3 Increment-7 controlled-writer first-insert + full-policy (delivery_head): complete staged manifest + rebuilt packs + pointer
5bc3978 Phase 3 Increment-7 controlled-writer first-insert + full-policy gaps (inventory_head): PG16-green + gate 196/196
62f7e7a @ Phase 3 Increment-7 controlled-writer boundary (delivery_head): complete staged manifest + rebuilt packs + pointer
8a224b7 Phase 3 Increment-7 TRUE controlled-writer authorization boundary (inventory_head): PG16-green + gate 188/188
d8ed476 @ Phase 3 Increment-7 Checkout unspoofable state machine hardening (delivery_head): complete staged manifest + rebuilt packs + pointer
856fb33 Phase 3 Increment-7 Checkout unspoofable state machine + catalog/alert/provenance hardening (inventory_head): PG16-green + gate 181/181
36c5c62 @ Phase 3 Increment-7 Checkout history-integrity corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
eab5f5e Phase 3 Increment-7 Checkout history-integrity + emergency-catalog + alert + provenance corrections (inventory_head): PG16-green + gate 172/172
66f7029 @ Phase 3 CI-stability localkeys flake fix (delivery_head): complete staged manifest + rebuilt packs + pointer
0b334dd Phase 3 CI-stability: fix internal/localkeys.EnsureGeneration concurrent mid-write flake (inventory_head)
8e91fbf @ Phase 3 Increment-7 Checkout historical-boundary + emergency-catalog + policy-consistency corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
483d7cc Phase 3 Increment-7 Checkout historical-boundary + emergency-catalog + policy-consistency corrections (inventory_head): PG16-green + gate 157/157
4296d29 @ Phase 3 Increment-7 Checkout conversion safety corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
1bf4936 Phase 3 Increment-7 Checkout conversion safety + boundary corrections (inventory_head): fail-closed, boundary-eligibility, durable audit â€” PG16-green + gate 141/141
83f4abf @ Phase 3 Increment-7 atomic Checkout conversion (delivery_head): complete staged manifest + rebuilt packs + pointer
2c0df80 Phase 3 Increment-7 atomic Checkout conversion (inventory_head): Stay-first single-tx checkout+grace, PG16-green
20aaccd @ Phase 3 Auth Context lock-order + evidence-version enforcement + UUID validation (delivery_head): complete staged manifest + rebuilt packs + pointer
20980f3 Phase 3 Auth Context lock-order + evidence-version enforcement + UUID pin validation (inventory_head): PG16-green + lifecycle-gate 131/131
fb288cc @ Phase 3 Auth Context snapshot pin + status sync (delivery_head): complete staged manifest + rebuilt packs + pointer
49a9cff @ Phase 3 Auth Context episode + evidence-snapshot pin + cast-safe freshness + status sync (inventory_head): PG16-green + lifecycle-gate 121/121
06d2ad9 @ Phase 3 Auth Context provenance + status sync (delivery_head): complete staged manifest + rebuilt packs + pointer
3dd3713 @ Phase 3 Auth Context provenance + issuance validation + status sync (inventory_head): PG16-green
453998c @ Phase 3 corrections REJECT_NEW_DEVICE + Auth Context pins (delivery_head): complete staged manifest + rebuilt packs + pointer
96d4c7d @ Phase 3 corrections: REJECT_NEW_DEVICE (no limit exception) + complete Auth Context pin set (inventory_head); lifecycle-gate 121/121 + PG16-green + race-green
f703212 @ Phase 3 Increment 6 Auth Context extension (delivery_head): complete staged manifest + rebuilt packs + pointer
da7de53 @ Phase 3 Increment 6 Auth Context consumption extended (inventory_head): full pinned-context verification + atomic ConsumeTx; PG16-green
f360d65 @ Phase 3 Increment 7 corrected grace semantics (delivery_head): complete staged manifest + rebuilt packs + pointer
bfa8159 @ Phase 3 Increment 7 CORRECTED grace semantics (inventory_head): entitlement-based eligibility (origin-agnostic), config-invalid Emergency fallback; green
22b2f64 @ Phase 3 Increment 7 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
83253ea @ Phase 3 Increment 7 foundation (inventory_head): Checkout Grace + Emergency Grace decision core (internal/grace), F4â€“F6
66c9ddf @ Phase 3 Increment 6 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
bab09e9 @ Phase 3 Increment 6 foundation (inventory_head): one-time TTL-bounded PMS Auth Context (internal/authctx), PG16-green
125158c @ Phase 3 Increment 5 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
3efe3f5 @ Phase 3 Increment 5 foundation (inventory_head): STRICT multi-PMS resolver decision core (internal/pmsresolve), D1â€“D11
d2ef30f @ Phase 3 Increment 4 transactional processor (delivery_head): complete staged manifest + rebuilt packs + pointer
c973ab0 @ Phase 3 Increment 4 transactional processor (inventory_head): consume durable inbox â†’ apply Stay op â†’ terminal event, race-green + PG16-green
c42fbb5 @ Phase 3 Increment 4 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
c356a29 @ Phase 3 Increment 4 foundation (inventory_head): deterministic Stay-resolution decision core (internal/stayengine)
e6db8ea @ Phase 3 increment 3 Â§9-Â§16 complete (delivery_head): complete staged manifest + rebuilt packs + pointer
5cc06b0 @ Phase 3 increment 3 Â§9-Â§16 COMPLETE: owner-bound AES-GCM AAD (inventory_head); connector hardening finished, race-green
9684921 @ Phase 3 increment 3 Â§9 credential_mode + pin coherence (delivery_head): complete staged manifest + rebuilt packs + pointer
a2e733f @ Phase 3 increment 3 Â§9 credential_mode NONE + Migration-0010 credential-aware pin coherence (inventory_head): truthful no-auth Protel FIAS; race-green + lifecycle-gate 121/121 + PG16-green
e0d126f @ Phase 3 CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
b0ddce3 @ Phase 3 CI-stability (inventory_head): align Â§F write-failure + malformed-domain tests with the Â§G initial-DR flow
6916513 @ Phase 3 CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
4bc1872 @ Phase 3 CI-stability (inventory_head): fix concurrency bug in localkeys.CreateKeyIfAbsent (mid-write empty O_EXCL file)
a80a369 @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable admission (delivery_head): complete staged manifest + rebuilt packs + pointer
11fc3ff @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green
75d30a0 @ Phase 3 increment 3 Â§G data model + persistence (delivery_head): complete staged manifest + rebuilt packs + pointer
2e4c864 @ Phase 3 increment 3 Â§G data model + persistence (inventory_head): durable resync inbox (reuse stay_events), typed resync generation, immutable-rows + atomic publication boundary, ownership-safe append-first admission; lifecycle-gate 121/121 + PG16-green + race-green
2dc0004 @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (delivery_head): complete staged manifest + rebuilt packs + pointer
c5507c6 @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (inventory_head): strict-parse every inbound frame, prompt bounded shutdown, context-aware serialized writer, per-frame write-failure coverage; race-green
4d9f138 @ Phase 3 increment 3 hardening items 1-6 (delivery_head): complete staged manifest + rebuilt packs + pointer
6d6914d @ Phase 3 increment 3 hardening items 1-6 (inventory_head): strict FIAS parser, duplicate-field fail-closed, GuestName removed, atomic gap/resync txn, one serialized protocol writer; race + PG16 green
9c0c0f6 @ Phase 3 increment 3 hardening Â§A-Â§D CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
cda3836 @ Phase 3 increment 3 hardening Â§A-Â§D CI-stability (inventory_head): fix benign measurement race in linearizable-close test
bbc8e1d @ Phase 3 increment 3 hardening Â§A-Â§D (delivery_head): complete staged manifest + rebuilt packs + pointer
59cd031 @ Phase 3 increment 3 hardening Â§A-Â§D (inventory_head): finalize Event semantics â€” remove connector-owned Stay identity, complete-record fingerprint, no silent truncation; race-green
308d039 @ Phase 3 increment 3 hardening Â§1-Â§4 (delivery_head): complete staged manifest + rebuilt packs + pointer
c71f06a @ Phase 3 increment 3 hardening Â§1-Â§4 (inventory_head): Event-identity split (SourceEventFingerprint vs LogicalStayKey) + dedicated keyed HMAC + corrected timestamp semantics; race-green
c93d9a4 @ Phase 3 increment 3 REOPENED (delivery_head): complete staged manifest + rebuilt packs + pointer
a1dda4c @ Phase 3 increment 3 REOPENED (inventory_head): authoritative FIAS field map correction (RN=room, G#=reservation, GN/GF, GA/GD) + deterministic Event identity; status back to HARDENING
ffb9f0d @ Phase 3 increment 3 CI-stability hardening (delivery_head): complete staged manifest + rebuilt packs + pointer
62ec099 @ Phase 3 increment 3 CI-stability hardening (inventory_head): robust gate readiness + retry-once on flaky in-job postgres container steps
c4bcf64 @ Phase 3 increment 3 COMPLETE (delivery_head): complete staged manifest + rebuilt packs + pointer
2b6d250 @ Phase 3 increment 3 COMPLETE (inventory_head): pmsd runtime + both CIs green on a5e2d3a; increments 4-9 remain
a5e2d3a @ Phase 3 increment 3 integration-readiness fix (delivery_head): complete staged manifest + rebuilt packs + pointer
aafae76 @ Phase 3 increment 3 integration-readiness fix (inventory_head): robust postgres readiness in pmsd-pg-integration.sh
b70ed9a @ Phase 3 increment 3 software-CI scope fix (delivery_head): complete staged manifest + rebuilt packs + pointer
7f662af @ Phase 3 increment 3 software-CI scope fix (inventory_head): gofmt/vet check the Phase-3 pmsd surface (not pre-existing unformatted packages)
7f283fa @ Phase 3 increment 3 coordinated pmsd rewrite (delivery_head): complete staged manifest + rebuilt packs + pointer
54ee4d7 @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green
b0201db @ Phase 3 increment 3 continuation Â§1-Â§5 (delivery_head): complete staged manifest + rebuilt packs + pointer
f2b11f9 @ Phase 3 increment 3 continuation Â§1-Â§5 (inventory_head): linearizable queue + typed Events + logging-PII fix + apply/rollback role split + bootstrap target-kind; gate 117/117, pmsd race-green
3b5c1a9 @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (delivery_head): complete staged manifest + rebuilt packs + pointer
d015f7d @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (inventory_head): crypto lock key + typed error vocabulary + bounded event queue; pmsd race-green
d39404a @ Phase 3 increment 3 Part-A final runner corrections (local checkpoint): mandatory positive target identity + canonical dir + structural ledger verify + explicit checksum + separated bootstrap + deployment-parity service roles; gate 114/114
b63d18d @ Phase 3 increment 3 hardening PART A (delivery_head): complete staged manifest + rebuilt packs + pointer
c770325 @ Phase 3 increment 3 hardening PART A (inventory_head): 0010 secret-generation pin + event-id immutability + atomic lock-then-ledger runner + target-identity fail-closed + deployment-parity ownership; gate 98/98
323697c @ Phase 3 increment 3 (delivery_head): complete manifest + rebuilt packs + pointer
28858dd @ Phase 3 increment 3 (inventory_head): pmsd read-only PMS connector daemon (ADR-0001), DARK
7f16628 @ Phase 3 increment 2 final invariants (delivery_head): complete manifest + rebuilt packs + pointer
2dbe4cd @ Phase 3 increment 2 final invariants (inventory_head): event append-first/terminal rules, grace all-or-none, runner scope hardening
7601f40 @ Phase 3 increment 2 hardening (delivery_head): complete manifest + rebuilt packs + pointer
379a85f @ Phase 3 increment 2 hardening (inventory_head): migration 0010 corrections + authoritative runner + 55/55 gate
6116155 @ Phase 3 increment 2 (delivery_head): complete manifest (base..delivery_head, 54 files) + rebuilt packs + pointer
82330dc @ Phase 3 increment 2 (inventory_head): migration 0010 + pms_config flags + machine-grounded gap audit
5499534 @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer
b08b6cc @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards
```
## 11. All commits created

```text
960ac3e @ Phase 3 Increment-9 offline tooling + runbook (delivery_head): complete staged manifest + rebuilt packs + pointer
5c6d143 Phase 3 Increment-9 offline tooling + runbook (inventory_head): preflight 11/11, evidence collector, deployment/rollback/reboot runbook
23d9d12 @ Phase 3 guest-portal uniform contract (delivery_head): complete staged manifest + rebuilt packs + pointer
b8f49f4 Phase 3 guest-portal uniform non-success contract (inventory_head): byte-identical failure responses, no oracle, audit reasons kept server-side
2cafd9c @ Phase 3 Hotel-Admin E2E + accessibility (delivery_head): complete staged manifest + rebuilt packs + pointer
1592850 Phase 3 Hotel-Admin E2E + accessibility (inventory_head): 7 Playwright specs over mocked edged, named controls and labelled filters proven
834650c @ Phase 3 Hotel-Admin surface (delivery_head): complete staged manifest + rebuilt packs + pointer
a1f0c4e Phase 3 Hotel-Admin surface (inventory_head): dark-gated stays/events/resolutions/grace/alerts API + RBAC + four UI pages: tsc clean, Vitest 48/48
7f75249 @ Phase 3 netd shaping plan + acctd enforcement (delivery_head): complete staged manifest + rebuilt packs + pointer
30757b1 Phase 3 netd shaping plan + acctd expiry enforcement (inventory_head): derived plan, true-time window/quota endings with revocation: PG16-green
76d2029 @ Phase 3 F1-F7 flow suite (delivery_head): complete staged manifest + rebuilt packs + pointer
d0f57e0 Phase 3 F1-F7 named flow suite (inventory_head): room-move preservation, stale-event no-op, origin-agnostic conversion, grandfathering, validity window, emergency fallback, episode idempotency: PG16-green
d18e09b @ Phase 3 sharers + folios + source conflicts (delivery_head): complete staged manifest + rebuilt packs + pointer
0927baa Phase 3 sharers + folios + source conflicts (inventory_head): legal multi-occupancy with one primary, contradictory payloads and folio claims to review: PG16-green
166ff5b @ Phase 3 strict resolver fan-out + idempotent resolutions (delivery_head): complete staged manifest + rebuilt packs + pointer
32b382b Phase 3 strict resolver fan-out + idempotent auth_resolutions (inventory_head): complete-vector concurrency, fail-closed indeterminacy, >=24 concurrent resolutions: PG16-green
09619e3 @ Phase 3 post-boundary revocation + accounting attribution (delivery_head): complete staged manifest + rebuilt packs + pointer
b8aeca1 Phase 3 post-boundary revocation + accounting attribution intervals/watermarks/delayed samples (inventory_head): PG16-green + gate 282/282
1ffba2b @ Phase 3 atomic grant + controlled device authorization (delivery_head): complete staged manifest + rebuilt packs + pointer
d976419 Phase 3 atomic Auth-Context/Quote/Purchase/Entitlement grant + controlled device authorization (inventory_head): PG16-green + gate 267/267
3010a70 @ Phase 3 bitemporal entitlement history (delivery_head): complete staged manifest + rebuilt packs + pointer
c018f84 Phase 3 bitemporal entitlement history (inventory_head): true effective_at + recorded_at, explicit supersession, boundary termination without clamping: PG16-green + gate 254/254
cd24425 @ Phase 3 Increment-7 Checkout scorecard-gap closure (delivery_head): complete staged manifest + rebuilt packs + pointer
362aecd Phase 3 Increment-7 Checkout scorecard-gap closure (inventory_head): no old path, fail-closed, mandatory lineage, structural DB lineage, ordering, >=24 integrated concurrency, late-stage rollback: PG16-green + gate 225/225
e43bd28 @ Phase 3 one-transaction Checkout slice (delivery_head): complete staged manifest + rebuilt packs + pointer
60191c3 Phase 3 vertical slice: ONE physical Stay-Event->Checkout transaction + exact event lineage (inventory_head): PG16-green + gate 225/225
56b29b7 @ Phase 3 controlled-writer manifest doc sync (delivery_head): complete staged manifest + rebuilt packs + pointer
324dbeb Phase 3 controlled-writer manifest documentation sync (inventory_head): doc-only
f82cef2 @ Phase 3 Increment-7 EXECUTE-only caller proof (delivery_head): complete staged manifest + rebuilt packs + pointer
de9c189 Phase 3 Increment-7 EXECUTE-only caller proof for the controlled-writer model (inventory_head): PG16-green + gate 225/225
7d01e72 @ Phase 3 Increment-7 config-DELETE + per-family writer-owner (delivery_head): complete staged manifest + rebuilt packs + pointer
d5263d7 Phase 3 Increment-7 config-DELETE + per-family writer-owner gaps (inventory_head): PG16-green + gate 209/209
73e6b5d @ Phase 3 Increment-7 controlled-writer first-insert + full-policy (delivery_head): complete staged manifest + rebuilt packs + pointer
5bc3978 Phase 3 Increment-7 controlled-writer first-insert + full-policy gaps (inventory_head): PG16-green + gate 196/196
62f7e7a @ Phase 3 Increment-7 controlled-writer boundary (delivery_head): complete staged manifest + rebuilt packs + pointer
8a224b7 Phase 3 Increment-7 TRUE controlled-writer authorization boundary (inventory_head): PG16-green + gate 188/188
d8ed476 @ Phase 3 Increment-7 Checkout unspoofable state machine hardening (delivery_head): complete staged manifest + rebuilt packs + pointer
856fb33 Phase 3 Increment-7 Checkout unspoofable state machine + catalog/alert/provenance hardening (inventory_head): PG16-green + gate 181/181
36c5c62 @ Phase 3 Increment-7 Checkout history-integrity corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
eab5f5e Phase 3 Increment-7 Checkout history-integrity + emergency-catalog + alert + provenance corrections (inventory_head): PG16-green + gate 172/172
66f7029 @ Phase 3 CI-stability localkeys flake fix (delivery_head): complete staged manifest + rebuilt packs + pointer
0b334dd Phase 3 CI-stability: fix internal/localkeys.EnsureGeneration concurrent mid-write flake (inventory_head)
8e91fbf @ Phase 3 Increment-7 Checkout historical-boundary + emergency-catalog + policy-consistency corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
483d7cc Phase 3 Increment-7 Checkout historical-boundary + emergency-catalog + policy-consistency corrections (inventory_head): PG16-green + gate 157/157
4296d29 @ Phase 3 Increment-7 Checkout conversion safety corrections (delivery_head): complete staged manifest + rebuilt packs + pointer
1bf4936 Phase 3 Increment-7 Checkout conversion safety + boundary corrections (inventory_head): fail-closed, boundary-eligibility, durable audit â€” PG16-green + gate 141/141
83f4abf @ Phase 3 Increment-7 atomic Checkout conversion (delivery_head): complete staged manifest + rebuilt packs + pointer
2c0df80 Phase 3 Increment-7 atomic Checkout conversion (inventory_head): Stay-first single-tx checkout+grace, PG16-green
20aaccd @ Phase 3 Auth Context lock-order + evidence-version enforcement + UUID validation (delivery_head): complete staged manifest + rebuilt packs + pointer
20980f3 Phase 3 Auth Context lock-order + evidence-version enforcement + UUID pin validation (inventory_head): PG16-green + lifecycle-gate 131/131
fb288cc @ Phase 3 Auth Context snapshot pin + status sync (delivery_head): complete staged manifest + rebuilt packs + pointer
49a9cff @ Phase 3 Auth Context episode + evidence-snapshot pin + cast-safe freshness + status sync (inventory_head): PG16-green + lifecycle-gate 121/121
06d2ad9 @ Phase 3 Auth Context provenance + status sync (delivery_head): complete staged manifest + rebuilt packs + pointer
3dd3713 @ Phase 3 Auth Context provenance + issuance validation + status sync (inventory_head): PG16-green
453998c @ Phase 3 corrections REJECT_NEW_DEVICE + Auth Context pins (delivery_head): complete staged manifest + rebuilt packs + pointer
96d4c7d @ Phase 3 corrections: REJECT_NEW_DEVICE (no limit exception) + complete Auth Context pin set (inventory_head); lifecycle-gate 121/121 + PG16-green + race-green
f703212 @ Phase 3 Increment 6 Auth Context extension (delivery_head): complete staged manifest + rebuilt packs + pointer
da7de53 @ Phase 3 Increment 6 Auth Context consumption extended (inventory_head): full pinned-context verification + atomic ConsumeTx; PG16-green
f360d65 @ Phase 3 Increment 7 corrected grace semantics (delivery_head): complete staged manifest + rebuilt packs + pointer
bfa8159 @ Phase 3 Increment 7 CORRECTED grace semantics (inventory_head): entitlement-based eligibility (origin-agnostic), config-invalid Emergency fallback; green
22b2f64 @ Phase 3 Increment 7 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
83253ea @ Phase 3 Increment 7 foundation (inventory_head): Checkout Grace + Emergency Grace decision core (internal/grace), F4â€“F6
66c9ddf @ Phase 3 Increment 6 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
bab09e9 @ Phase 3 Increment 6 foundation (inventory_head): one-time TTL-bounded PMS Auth Context (internal/authctx), PG16-green
125158c @ Phase 3 Increment 5 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
3efe3f5 @ Phase 3 Increment 5 foundation (inventory_head): STRICT multi-PMS resolver decision core (internal/pmsresolve), D1â€“D11
d2ef30f @ Phase 3 Increment 4 transactional processor (delivery_head): complete staged manifest + rebuilt packs + pointer
c973ab0 @ Phase 3 Increment 4 transactional processor (inventory_head): consume durable inbox â†’ apply Stay op â†’ terminal event, race-green + PG16-green
c42fbb5 @ Phase 3 Increment 4 foundation (delivery_head): complete staged manifest + rebuilt packs + pointer
c356a29 @ Phase 3 Increment 4 foundation (inventory_head): deterministic Stay-resolution decision core (internal/stayengine)
e6db8ea @ Phase 3 increment 3 Â§9-Â§16 complete (delivery_head): complete staged manifest + rebuilt packs + pointer
5cc06b0 @ Phase 3 increment 3 Â§9-Â§16 COMPLETE: owner-bound AES-GCM AAD (inventory_head); connector hardening finished, race-green
9684921 @ Phase 3 increment 3 Â§9 credential_mode + pin coherence (delivery_head): complete staged manifest + rebuilt packs + pointer
a2e733f @ Phase 3 increment 3 Â§9 credential_mode NONE + Migration-0010 credential-aware pin coherence (inventory_head): truthful no-auth Protel FIAS; race-green + lifecycle-gate 121/121 + PG16-green
e0d126f @ Phase 3 CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
b0ddce3 @ Phase 3 CI-stability (inventory_head): align Â§F write-failure + malformed-domain tests with the Â§G initial-DR flow
6916513 @ Phase 3 CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
4bc1872 @ Phase 3 CI-stability (inventory_head): fix concurrency bug in localkeys.CreateKeyIfAbsent (mid-write empty O_EXCL file)
a80a369 @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable admission (delivery_head): complete staged manifest + rebuilt packs + pointer
11fc3ff @ Phase 3 increment 3 Â§G state machine + Â§H barrier + durable LIVE admission (inventory_head): DSâ†’DE resync lifecycle, application barrier, ownership-safe append-first admission; race-green + PG16-green
75d30a0 @ Phase 3 increment 3 Â§G data model + persistence (delivery_head): complete staged manifest + rebuilt packs + pointer
2e4c864 @ Phase 3 increment 3 Â§G data model + persistence (inventory_head): durable resync inbox (reuse stay_events), typed resync generation, immutable-rows + atomic publication boundary, ownership-safe append-first admission; lifecycle-gate 121/121 + PG16-green + race-green
2dc0004 @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (delivery_head): complete staged manifest + rebuilt packs + pointer
c5507c6 @ Phase 3 increment 3 Â§F-integration hardening items 1-4 (inventory_head): strict-parse every inbound frame, prompt bounded shutdown, context-aware serialized writer, per-frame write-failure coverage; race-green
4d9f138 @ Phase 3 increment 3 hardening items 1-6 (delivery_head): complete staged manifest + rebuilt packs + pointer
6d6914d @ Phase 3 increment 3 hardening items 1-6 (inventory_head): strict FIAS parser, duplicate-field fail-closed, GuestName removed, atomic gap/resync txn, one serialized protocol writer; race + PG16 green
9c0c0f6 @ Phase 3 increment 3 hardening Â§A-Â§D CI-stability (delivery_head): complete staged manifest + rebuilt packs + pointer
cda3836 @ Phase 3 increment 3 hardening Â§A-Â§D CI-stability (inventory_head): fix benign measurement race in linearizable-close test
bbc8e1d @ Phase 3 increment 3 hardening Â§A-Â§D (delivery_head): complete staged manifest + rebuilt packs + pointer
59cd031 @ Phase 3 increment 3 hardening Â§A-Â§D (inventory_head): finalize Event semantics â€” remove connector-owned Stay identity, complete-record fingerprint, no silent truncation; race-green
308d039 @ Phase 3 increment 3 hardening Â§1-Â§4 (delivery_head): complete staged manifest + rebuilt packs + pointer
c71f06a @ Phase 3 increment 3 hardening Â§1-Â§4 (inventory_head): Event-identity split (SourceEventFingerprint vs LogicalStayKey) + dedicated keyed HMAC + corrected timestamp semantics; race-green
c93d9a4 @ Phase 3 increment 3 REOPENED (delivery_head): complete staged manifest + rebuilt packs + pointer
a1dda4c @ Phase 3 increment 3 REOPENED (inventory_head): authoritative FIAS field map correction (RN=room, G#=reservation, GN/GF, GA/GD) + deterministic Event identity; status back to HARDENING
ffb9f0d @ Phase 3 increment 3 CI-stability hardening (delivery_head): complete staged manifest + rebuilt packs + pointer
62ec099 @ Phase 3 increment 3 CI-stability hardening (inventory_head): robust gate readiness + retry-once on flaky in-job postgres container steps
c4bcf64 @ Phase 3 increment 3 COMPLETE (delivery_head): complete staged manifest + rebuilt packs + pointer
2b6d250 @ Phase 3 increment 3 COMPLETE (inventory_head): pmsd runtime + both CIs green on a5e2d3a; increments 4-9 remain
a5e2d3a @ Phase 3 increment 3 integration-readiness fix (delivery_head): complete staged manifest + rebuilt packs + pointer
aafae76 @ Phase 3 increment 3 integration-readiness fix (inventory_head): robust postgres readiness in pmsd-pg-integration.sh
b70ed9a @ Phase 3 increment 3 software-CI scope fix (delivery_head): complete staged manifest + rebuilt packs + pointer
7f662af @ Phase 3 increment 3 software-CI scope fix (inventory_head): gofmt/vet check the Phase-3 pmsd surface (not pre-existing unformatted packages)
7f283fa @ Phase 3 increment 3 coordinated pmsd rewrite (delivery_head): complete staged manifest + rebuilt packs + pointer
54ee4d7 @ Phase 3 increment 3 coordinated pmsd rewrite (inventory_head): assignment scoping + typed secret/revision + atomic generation + axis CAS + real injectable FIAS adapter + write chokepoint + bounded typed events + PG16 integration + software CI; gate 121/121, race-green
b0201db @ Phase 3 increment 3 continuation Â§1-Â§5 (delivery_head): complete staged manifest + rebuilt packs + pointer
f2b11f9 @ Phase 3 increment 3 continuation Â§1-Â§5 (inventory_head): linearizable queue + typed Events + logging-PII fix + apply/rollback role split + bootstrap target-kind; gate 117/117, pmsd race-green
3b5c1a9 @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (delivery_head): complete staged manifest + rebuilt packs + pointer
d015f7d @ Phase 3 increment 3 Part-B Â§10/Â§14/Â§17 (inventory_head): crypto lock key + typed error vocabulary + bounded event queue; pmsd race-green
d39404a @ Phase 3 increment 3 Part-A final runner corrections (local checkpoint): mandatory positive target identity + canonical dir + structural ledger verify + explicit checksum + separated bootstrap + deployment-parity service roles; gate 114/114
b63d18d @ Phase 3 increment 3 hardening PART A (delivery_head): complete staged manifest + rebuilt packs + pointer
c770325 @ Phase 3 increment 3 hardening PART A (inventory_head): 0010 secret-generation pin + event-id immutability + atomic lock-then-ledger runner + target-identity fail-closed + deployment-parity ownership; gate 98/98
323697c @ Phase 3 increment 3 (delivery_head): complete manifest + rebuilt packs + pointer
28858dd @ Phase 3 increment 3 (inventory_head): pmsd read-only PMS connector daemon (ADR-0001), DARK
7f16628 @ Phase 3 increment 2 final invariants (delivery_head): complete manifest + rebuilt packs + pointer
2dbe4cd @ Phase 3 increment 2 final invariants (inventory_head): event append-first/terminal rules, grace all-or-none, runner scope hardening
7601f40 @ Phase 3 increment 2 hardening (delivery_head): complete manifest + rebuilt packs + pointer
379a85f @ Phase 3 increment 2 hardening (inventory_head): migration 0010 corrections + authoritative runner + 55/55 gate
6116155 @ Phase 3 increment 2 (delivery_head): complete manifest (base..delivery_head, 54 files) + rebuilt packs + pointer
82330dc @ Phase 3 increment 2 (inventory_head): migration 0010 + pms_config flags + machine-grounded gap audit
5499534 @ Phase 3 (delivery_head): complete manifest (base..delivery_head, 48 files) + rebuilt packs + pointer
b08b6cc @ Phase 3 (inventory_head): D14/T0015 authorization + plan + privilege matrix + connector ADR + governance guards
```

## 12. Branch and PR information

- **Branch:** `phase/3-stay-resolution-grace`
- **PR URL:** https://github.com/aibrahiiim1/StayConnectEnterprise/pull/6 (**OPEN, UNMERGED**)
- **PR base ← head:** `master` ← `phase/3-stay-resolution-grace`
- **CI on the delivery HEAD (both on the same pushed HEAD):**
  - **Phase 3 Software CI** — job `phase3-full-software-gate`. One run executes the WHOLE software gate:
    gofmt over the entire Phase-3 Go surface, `go build`, full `go vet`, the whole Go unit suite, the race
    detector over every Phase-3 concurrency-sensitive package, the Migration 0010 lifecycle gate, all eleven
    disposable-PG16 integration suites, the offline preflight, and — under a locked Node install from
    `hotel-admin/` — TypeScript typecheck, Vitest, the production build with Phase-3 flags OFF, and the
    Playwright browser suite (Hotel-Admin pages + guest-portal real template + accessibility). After every
    step passes it assembles and **uploads a downloadable evidence artifact**, `phase3-software-evidence-<HEAD>`
    (retention 90 days), whose `RUN_META.json` records the delivery/inventory/base HEADs, the run id, the UTC
    window, tool versions, lock/migration hashes, per-step exit codes and durations, per-suite test totals and
    skip totals, infrastructure retries, the restrictions confirmation and the Live-Increment-9 pending list,
    plus a `MANIFEST.sha256` over every file in the artifact.
  - **Project Governance** — SUCCESS on the same HEAD.
  - The numeric **run IDs**, the **artifact ID**, the **artifact size/retention** and the
    **integrity-manifest SHA-256** are recorded in the PR #6 body. They are run metadata and cannot be
    embedded in the commit they describe — the same self-reference rule the change manifest already follows.
  - **Correction of record:** an earlier revision of this report and the PR body stated that the Software CI
    proved the Vitest and Playwright suites and published the evidence artifact. That was not true of the
    then-current workflow, which ran only the Go/backend steps; the frontend suites had been run on a
    workstation. This is the corrected, full gate. No historical run is described as having contained steps it
    did not run.

## 13. Remote reachability of HEAD

- **Delivery HEAD:** the current tip of `phase/3-stay-resolution-grace`, pushed to
  `origin/phase/3-stay-resolution-grace` and identical local/remote. A frozen SHA is deliberately **not**
  written here: the delivery-only commit that adds this report cannot cite its own hash, so the authoritative
  delivery HEAD lives in `governance/project-state.json` (`acceptance_candidate_head` = inventory_head, one
  commit below the delivery HEAD) and in the PR #6 body, which also records the same-HEAD run IDs, the
  evidence artifact id/name/size/retention, and the integrity-manifest SHA-256.
- **Match:** local == remote (the push in §11 is fast-forward).

## 14. Full working-tree status

```text
(clean)
```

## 15. Documentation and governance synchronization

- `governance/project-state.json` carries the authoritative Phase-3 narrative and the pointer fields
  (`acceptance_candidate_head`, `inventory_head`) for this delivery; `PROJECT_STATE_GOVERNANCE = PASS`.
- `docs/manifests/Phase3-change-manifest.md` regenerated for the complete `base..delivery_head` path set.
- `docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md` added (deploy / prove-dark / reboot / rollback).
- `docs/architecture/Phase3-Controlled-Writer-Privilege-Manifest.md` remains **PREPARED, NOT APPLIED**.
- Export packs rebuilt deterministically with the source commit recorded.

## 16. Project / Evidence Pack paths and checksums

- `exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip`
- `exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip`
- `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip`

SHA-256 values are recorded in `exports/chatgpt/*/PACK_SHA256SUMS.txt`, regenerated with the packs.

**The authoritative Phase-3 software evidence is the artifact the Software CI uploads on the delivery HEAD**
(`phase3-software-evidence-<HEAD>`), not a repository ZIP. It is downloadable from the exact successful
Software run, contains only Phase-3 evidence, and carries a `MANIFEST.sha256` over all of its files. The
committed export packs above are the project/plan packs; **the older Phase-1A live-dark acceptance pack was
NOT reused, renamed or repurposed as Phase-3 evidence** — the Phase-3 artifact is generated fresh, in CI, per
run, and its `RUN_META.json` embeds this delivery HEAD.

`scripts/phase3-evidence.sh` remains as a local offline convenience bundle and is deliberately **not
committed**: a committed bundle is stale the moment the next commit lands yet still reads as current evidence.
It is not the same-HEAD CI artifact and is not cited as acceptance evidence.

## 17. `PROJECT_STATE_GOVERNANCE` result

**PASS** (`python tools/project-state.py validate`), alongside `ZERO_STALE_LEFTOVERS = PASS` and
`GENERATED_BLOCKS = PASS`.
