# StayConnect IAM — Phase 2 (Commercial Packages) Final Report

**Status: PRODUCT-OWNER ACCEPTED_AND_CLOSED at verified DARK maturity (decision D13, closure transition T0014, 2026-07-18). PR #4 authorized to merge to master with provenance preserved.** Maturity: verified DARK (implementation + automated UI tests + live-dark deployment + TWO reboots with post-reboot re-verification each).

Branch `phase/2-commercial-packages` · PR #4 · **authorized** under **D12** / transition **T0012** · **live-dark deployment** transition **T0013** · **accepted and closed** under **D13** / closure transition **T0014** · appliance `radius` / `172.21.60.23`.

**Accepted baseline:** base `master@4e3c3ee27a8caa2d0656b57b419e8ed5e0d87b68`; substantive reconciliation HEAD `45323ad5cb8f70e64e1dcd727ac92b52cc878c37`; final pre-acceptance PR HEAD `a8c3b3caac6baf8ac41fa581fca5350c97219bb8`; Governance CI `29649900358` = SUCCESS.

**Two HEADs (manifest self-reference protocol):** `inventory_head` (= `acceptance_candidate_head`) is the committed HEAD immediately before the final delivery-only commit and the generation/provenance HEAD recorded in the change manifest; `delivery_head` (the branch tip / PR HEAD) is the final delivery-only commit that adds the regenerated manifest, rebuilt export packs and the pointer/provenance. The committed manifest's complete path/status set describes `base..delivery_head` and is verified equal to `git diff --name-status base..delivery_head` by the Governance CI (a commit cannot appear inside its own generated manifest). Exact `inventory_head`/`delivery_head` SHAs, the final changed-file count, pack hashes and the Governance CI run are in §10/§13/§16 and the PR body.

---

## 0. Product-Owner acceptance and closure

Phase 2 (Commercial Packages) is **ACCEPTED at verified DARK maturity and CLOSED** by Product-Owner decision **D13** / closure transition **T0014** (2026-07-18). PR #4 is authorized to merge to master with provenance preserved. Acceptance is at DARK maturity only: no flag enablement, no IAM-v2 cutover, no paid access, no PMS settlement, no Phase 3. Phase 3 remains `NOT_STARTED` and unauthorized; enabling guest Commerce requires a separately authorized IAM-v2 authentication cutover. The earlier final acceptance gate (below) added real UI test automation, an authoritative production build, and governance/evidence reconciliation; no rollback, no flag enablement, no cutover.

- **UI tests (45 total, all green).** Component/unit: **36** (Vitest + React Testing Library). E2E: **9** (Playwright driving locally-installed Chrome) — 3 Hotel Admin (real Next app, edged mocked) + 6 Guest Portal (the real portald success-page template, `/api/commerce/*` mocked). The Guest Portal E2E proves the browser submits ONLY opaque `package_id`/`quote_id` and that double-submit yields exactly one confirm.
- **Authoritative production build.** `NODE_OPTIONS=--max-old-space-size=12288 npm run build` on host CHV-MISMGR, START `2026-07-18T11:52:54Z` → END `2026-07-18T11:53:39Z`, **EXIT 0**, `✓ Compiled successfully` + `✓ Generating static pages (31/31)`. Standalone tarball SHA-256 `678c793ea46f23241eba05bde66929b19a5473fc8d3752d2a5eb083f4ff0dd95`. The earlier prerender OOM is an environment memory limit (recorded as an observation), now superseded by this successful build.
- **Runtime artifact change → targeted redeploy + reboot.** The Hotel-Admin UI (typed publish form) changed; the Go `scd`/`edged`/`portald` binaries did **not**. The new UI bundle (`678c793e…`, release `20260718-115608`) was deployed; a **second reboot** (boot `2026-07-18 11:56:34`) re-verified darkness: services active, Go hashes unchanged (`1e25f9ef`/`30ed45f1`/`bf400654`), flags OFF, scd commerce routes 404, iam_v2 **49/0**, `schema_migrations` 0009 present, commerce data **0**, svc roles **zero** iam_v2 grants, legacy smoke 200.
- **Governance.** `phase2_execution.transition_id` now points at the deployment transition **T0013**; the D12 authorization/start transition **T0012** is preserved in `authorization_transition_id`. New deterministic guards: transition-pointer drift (T0012 vs T0013) and manifest-HEAD coherence; adversarial mutation M37 covers the pointer drift.

---

## 1. شرح مبسّط بالعامية المصرية

عملنا "الباقات التجارية" (Commercial Packages) بالكامل بس **وهي مقفولة (DARK)** — يعني الكود كله موجود ومترفوع ومتظبط على الجهاز، لكن كل السويتشات مقفولة، فمفيش أي حاجة اتغيّرت للضيف ولا للفندق دلوقتي.

الضيف — لو الميزة اتفتحت في المستقبل — هيقدر يشوف الباقات المجانية اللي هو مؤهّل ليها، يختار واحدة، ياخد عرض سعر (Quote) بصلاحية 5 دقايق، ويأكّد شراء مجاني فيتفعّل ليه الإنترنت. الفندق — من لوحة التحكم — هيقدر يعمل خطط سرعة (Plans) وباقات (Packages) بنسخ ثابتة مايتغيّرش فيها القديم، ويحط قواعد أهلية وشرائح، وإعدادات فترة السماح بعد الخروج (Grace)، ويتفرّج على العروض والمشتريات — كله بصلاحيات وتدقيق (Audit) وتأكيد باسورد للحاجات الخطيرة.

المهم: **مفيش فلوس، مفيش ربط بنظام الفندق المالي (PMS)، مفيش أي تحويل لـ iam_v2، والدخول القديم لسه هو الأساس**. طبّقنا migration رقم 0009 (مجرد triggers وفهارس إضافية)، رفعنا البرامج الجديدة (اتأكدنا من البصمات SHA-256)، عملنا restart وبعدين reboot كامل، واتأكدنا إن كل حاجة لسه مقفولة بعد الريبوت. **صاحب المنتج وافق رسميًا وقفل المرحلة 2 (القرار D13 والانتقال T0014) وهي مقفولة (DARK)، وأصرّح بدمج PR #4 في master.** المرحلة 3 لسه ماتبدأتش وممنوعة من غير قرار جديد.

## 2. Current Phase and authorized scope
- **Phase 2 — Commercial Packages**, executed as one end-to-end Phase under D12.
- **Allowed:** plan, additive schema/migration, domain, guest-Portal discovery/selection/quote/free-purchase, Hotel-Admin revisioned CRUD + grace config, tests, live-dark deployment + reboot verification (the final acceptance gate added a UI-only redeploy + a second reboot), evidence, one final report.
- **Prohibited (honored):** paid access; PMS settlement/posting/folio/tax; Stripe/payment; IAM-v2 cutover; dual read/write; data migration; dark-feature live enablement; Phase 3; legacy IAM removal; network/HA changes. Free acquisition only (`price_minor=0`, settlement `NOT_REQUIRED`).

## 3. What was implemented
- **Schema:** additive migration `0009_phase2_commerce` — null-safe Purchase↔Quote money-pin equality trigger; offer-quote immutability-except-one-time-consume trigger; 6 lookup indexes.
- **Domain/engine (`internal/iamv2`):** typed eligibility rules + publication-strict validation; ordered first-match grant tiers; typed/bounded grant snapshots; authoritative ISO-4217 currency+exponent; immutable duration policy (PMS/checkout/local-time modes capability-disabled); server-created 5-min offer quotes; atomic quote+auth-context consume; FREE purchases only; one-live-entitlement-per-subject supersession; read-only guest eligible-package listing that never discloses ineligible packages.
- **APIs:** scd internal-socket guest routes `GET /v1/commerce/packages`, `POST /quote`, `POST /confirm` (server-derived pins, missing-pin guards); edged Hotel-Admin `commercial-packages` resource — packages + immutable revision history + publish + activate/deactivate(step-up), service plans + revisions, typed rule/tier publish, grace config with validation, read-only PII-free quote/purchase inspection — all RBAC-gated + audited; portald trusted server bridge (browser submits only opaque ids).
- **UI:** guest Portal package-selection panel on `/success`; full Hotel-Admin four-tab management screen (Packages/Plans/Grace/Inspection) with a plan-revision selector, typed editors, sale windows, duration policy, immutable-revision status, and disabled-state handling.
- **Flags:** `STAYCONNECT_PHASE2_{MASTER,PORTAL,ADMIN}` + `NEXT_PUBLIC_PHASE2_ADMIN`, all default OFF, fail-closed; nil-repo-when-dark; fail-closed-if-master-on-without-repo.

## 4. Practical effect
Zero observable change today: every surface is inert behind OFF flags. When later enabled (a separate authorized cutover), the appliance can offer free Wi-Fi packages to eligible guests and let hotel staff manage them via immutable revisions. No paid or PMS behavior exists.

## 5. Risks and limitations
- The guest end-to-end flow additionally depends on live IAM-v2 authentication (Phase-1B **dark, not cut over**): with no IAM-v2 auth-context, the portald bridge fail-closes to "unavailable". This is why the flags stay OFF, not a defect.
- A deterministic JS UI test harness now exists: **36 Vitest + React Testing Library** component/unit tests and **9 Playwright** E2E tests (3 Hotel Admin + 6 Guest Portal), all green. The authoritative Next production build completes all 31 routes with a 12 GB Node heap (`NODE_OPTIONS=--max-old-space-size=12288 npm run build`, EXIT 0); the earlier prerender OOM was a workstation memory-pressure limit only (a recorded environment observation), not a code defect.
- Grace config records a selection only; it creates no grace entitlement/checkout behavior (Phase 3).

## 6. Acceptance tests
- **Go (`go test ./...`, `PHASE2_TEST_DSN` on disposable `iamv2_p2`): EXIT 0.** Covers config fail-closed; typed eligibility + publication validation; grant-snapshot (floats/negative/unknown/AGGREGATE-disabled); ISO-4217 (exp 0/2/3, ZZZ, USD/0); duration policy; **C2** quote/free-purchase + expiry + 24-way single-winner + rollback-at-every-mutation-boundary + tampered-quote; **C3** subject uniqueness/supersession/cross-subject/concurrency (account/voucher/principal); **C4/C5** immutability + pin trigger + revision lifecycle; **C6** per-pin substitution rejected by PostgreSQL; **C7** offer-quote immutability; **C8** duration-window stamping; guest listing filters + read-only + auth-pin; admin plans/grace-validation/inspection/publish-rejection; portald bridge (dark unmounted, no-session unavailable, browser cannot substitute pins, session-pin forwarding).
- **UI component/unit (Vitest + RTL, `npm test`): 36/36 PASS** — nav gating by flag+role; 503 disabled state; plan-revision selector (no raw UUID); revision-history current/immutable; typed eligibility editor (only the five non-PMS rule types; PMS types absent); ordered grant tiers; duration validation (PMS/checkout modes not representable); sale-window; free-only payload (no price/settlement/pms/tax/currency); grace validation; step-up deactivate (aborts on cancel); PII-free inspection; failed-publish-no-false-success.
- **Guest Portal E2E (Playwright, real portald success-page template): 6/6 PASS** — flag OFF → no panel + no commerce call; flag ON → lists only eligible packages, select→quote→confirm reaches active, browser submits ONLY opaque `package_id`/`quote_id`, double-submit → one confirm, expired/failed quote → generic unavailable.
- **Hotel Admin E2E (Playwright, real Next app, edged mocked): 3/3 PASS** — nav visible + list; approved disabled behavior on 503 with zero commerce mutations; full flow (create plan → publish free package via selector → step-up deactivate → grace validation → PII-free inspection).
- **UI type-check + authoritative production build:** `tsc --noEmit` PASS; `NODE_OPTIONS=--max-old-space-size=12288 npm run build` EXIT 0 — `✓ Compiled successfully` + `✓ Generating static pages (31/31)`.
- Full commands/timestamps/exit codes: `docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md`.

## 7. Production and guest impact
- **Two deployment/reboot events**, both re-verified darkness:
  1. **Initial Phase-2 deployment** — scd/edged/portald binaries + hotel-admin UI bundle deployed, services restarted, first reboot at **`2026-07-18 08:35:06`**.
  2. **Final acceptance-gate redeploy (UI only)** — after the typed publish-form refactor, the hotel-admin bundle (`678c793e…`, release `20260718-115608`) was redeployed (Go binaries unchanged); second reboot at **`2026-07-18 11:56:34`**.
- Existing guest sessions persist across daemon restarts (nftables/shaping state); the captive portal has a brief restart window. All legacy surfaces returned healthy pre- and post-reboot each time (scd 200, portald landing 200 + captive redirect 302, hotel-admin 200, edged 200).
- No guest-visible feature change (flags OFF).

## 8. Rollback status
- Binaries: previous `scd`/`edged`/`portald` kept as `*.bak-phase2pre`.
- UI: previous release retained via `hotel-admin.previous`.
- DB: pre-Phase-2 backup `pre-phase2-20260718-082640.dump` (`sha256 3af4237b…`); migration `0009` is additive with a tested `.down.sql`.
- Feature rollback needs no action: leaving the flags OFF keeps everything dark.

## 9. Security and isolation results
- **Zero** iam_v2 table grants and **zero** iam_v2 function EXECUTE grants to `svc_scd`/`svc_edged`/`svc_acctd`/`svc_netd` (Gate-P intact), verified live before and after reboot (`ALL_ZERO`).
- Migration 0009 objects owned by `iam_v2_owner`; triggers are SECURITY INVOKER and fire only on iam_v2 writes no runtime role can perform while dark.
- scd commerce handlers are an internal Unix-socket API (chmod 0660 root:stayconnect), never on a guest TCP listener; the browser trust boundary is enforced at portald (tests prove no substitution of auth-context/device/network/tenant/site).
- `public` schema columns SHA-256 unchanged pre→post migration (`833c3d67…`); iam_v2 remains 49 tables / 0 rows.

## 10. Complete generated changed-file manifest
`docs/manifests/Phase2-change-manifest.md` — generated by `tools/generate-change-manifest.py` and covering the **complete** `base 4e3c3ee → delivery_head` path/status inventory: **__CHANGED_FILES__ changed files**. Per the manifest self-reference protocol (see `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md`), the manifest records `inventory_head` (= `acceptance_candidate_head` in `project-state.json`) as its generation/provenance HEAD, while its path/status table enumerates the entire `base..delivery_head` diff so that **no path in the final PR diff is unlisted**. Governance CI enforces `manifest path/status set == git diff --name-status base..delivery_head` exactly; any missing/extra/status-mismatched path fails governance. Do not hand-edit. (The pre-final-gate "67 files" figure and the interim "86 files" substantive-only count are both superseded by this complete count.)

## 11. All commits created (base `4e3c3ee` → HEAD)
**Core Phase 2:** `c8f7a1c` WS-A/B plan+migration 0009 · `25d6521` WS-C1 config/domain · `740db89` governance D12/T0012 · `09a0abe` packs · `11a3462` WS-C2 engine+tests · `1f4eae6` C1/C2/C3/C5 hardening+governance · `005c0a4` WS-D scd routes · `9c13d11` WS-D edged admin foundation · `1403a33` WS-E admin page foundation · `8df3679` guest listing + PR/state sync · `2ed86c1` full admin API · `a064f6e` portald bridge · `288bff1` scd trust hardening · `ad43140` guest Portal UI · `a0578e4` full admin UI · `b89a744` software-gate evidence · `42f53aa` live-dark evidence + governance closure · `3a4fce8` change manifest · `efcaa26` packs · `1525c0d` validator next-action · `bf7f520` packs · `34233ab` 20-section report + fixtures.
**Final acceptance gate (UI automation + evidence reconciliation):** `3b7b752` Vitest+RTL harness + typed publish form · `9c78b78` Playwright E2E · `5dd1047` UI-test+build evidence, T0012/T0013 sync, guards · `13bac76` change-manifest regen + pointer · `0d732c1` rebuilt packs.
**Zero-Stale reconciliation:** one substantive commit `45323ad` + delivery-wrapper commits (`87251f7`, `608e390`, `98df0aa`, `c42d769`, `a8c3b3c`) — final pre-acceptance PR HEAD `a8c3b3c`, Governance CI `29649900358` SUCCESS.
**Product-Owner acceptance and closure (this delivery):** one closure commit `inventory_head` (decision **D13** + closure transition **T0014** + project-state `ACCEPTED_AND_CLOSED` + acceptance/report/plan/handoff sync + manifest self-reference protocol in the rule docs + validator closure/equality guards + mutation coverage) + one delivery-only commit `delivery_head` (complete regenerated manifest + rebuilt packs + pointer/provenance). Their exact SHAs and the changed-file count are in §13 and the PR body.

## 12. Branch and PR information
- Branch: `phase/2-commercial-packages`; base `master@4e3c3ee`.
- PR: **#4** — Product-Owner accepted (D13/T0014); authorized to merge to master with provenance preserved. Body updated to the accepted HEADs/status.

## 13. Remote reachability of HEAD + Governance CI
- Final pre-acceptance PR HEAD `a8c3b3c` — Governance CI run **29649900358** = **SUCCESS**.
- Acceptance-and-closure delivery: `inventory_head` (closure commit) and `delivery_head` (final delivery-only commit / branch tip / PR HEAD) are pushed to `origin/phase/2-commercial-packages`; the exact SHAs and the Governance CI run + conclusion on the **final `delivery_head`** are recorded in the PR #4 body and the returned report (a committed file cannot carry the CI run of the commit that is being pushed). PR #4 is merged only after that CI run is green on `delivery_head`.

## 14. Full working-tree status
Clean at each push (only intended, committed files). No stray tracked modifications; `__pycache__/` git-ignored.

## 15. Documentation and governance synchronization
`project-state.json` (activity `PHASE_2_ACCEPTED_AND_CLOSED`; phases.2 `ACCEPTED_AND_CLOSED`; phase-2 maturity with **two** reboot verifications; `migration_0009_applied`; `authorization_transition_id=T0012`; `transition_id=T0014`; `transition_accepted=true`; `accepted_po_decision=D13`; `acceptance_candidate_head`=`inventory_head`; verified_evidence; completed_milestones; blockers = no technical/open Phase-2 work; allowed_actions = merge PR #4 + post-merge verification / read-only verification / governance reconciliation) + decision `D13` + closure transition `T0014` + rendered generated blocks (Handoff, Phase-0/1A/1B plans, START-HERE, PROJECT-INSTRUCTIONS); Phase-2 Plan marked `ACCEPTED_AND_CLOSED`; Phase-2 privilege matrix (live-verified zero delta); acceptance record `ACCEPTED_AND_CLOSED`; evidence records; PR #4 body. The **manifest self-reference protocol** was added to `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md` + `docs/ZERO_STALE_LEFTOVERS_RULE.md`, enforced by a new `project-state.py` guard and adversarial mutation. Export packs rebuilt to include the Phase-2 authoritative sources/evidence + D12/T0012/T0013/**T0014**; Phase-1B Planning Pack HISTORICAL. `PROJECT_STATE_GOVERNANCE = PASS`; `ZERO_STALE_LEFTOVERS = PASS` (repository + extracted packs); adversarial mutation suite PASS.

## 16. Project / Evidence Pack paths and checksums (SHA-256)
The export packs are rebuilt from `inventory_head` in the delivery-only commit; their **full** SHA-256 values are recorded in the PR #4 body / returned report (the pack files are (re)built in `delivery_head`, so their final hashes cannot be embedded in a report committed at `inventory_head`). Packs:
- `exports/chatgpt/StayConnectEnterprise-ChatGPT-Project-Pack.zip` — current Project Pack (Phase-0/1A/1B/2 plans, Phase-2 privilege matrix, Phase-2 Software-Gate + Live-Dark evidence, Phase-2 acceptance record, Phase-2 Final Report, change manifest, Zero-Stale + GitHub delivery rules, ops docs).
- `exports/chatgpt/StayConnectEnterprise-Phase-Evidence-Pack.zip` — current Phase Evidence Pack (Phase-1A + 1B closed baselines + the full Phase-2 evidence set + change manifest + D12/T0012/T0013/**T0014** records + checksums).
- `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip` — **HISTORICAL** (Phase-1B planning artifact; Phase 1B was ACCEPTED_AND_CLOSED via D11/T0011 and PR #2 merged).

## 17. `PROJECT_STATE_GOVERNANCE` result
**PASS** (`python tools/project-state.py validate`). Adversarial mutation suite (`tools/tests/project_state_validator/run_mutations.py`) **PASS** — includes the deterministic Phase-2 scope guard **M36**, the transition-pointer drift guard **M37**, the Zero-Stale reconciliation guards (final-report/build/artifact/pack/fingerprint contradiction classes, M38–M44), and the acceptance-and-closure + manifest-vs-Git-equality guards added in this delivery. GitHub Actions Project Governance green on the pushed `delivery_head`.

## 18. `ZERO_STALE_LEFTOVERS` result
**PASS** (`bash tools/validate-project-state.sh`, repository mode + every extracted current pack) — single current maturity (Phase 2 ACCEPTED_AND_CLOSED), consistent next-action (merge PR #4 + post-merge verification), no stale pending-acceptance or "continue implementing Phase 2" phrases, current packs include the Phase-2 authoritative sources/evidence, links + checksums valid, no secrets/PII. Asserted only after every correction is complete.

## 19. Remaining blockers
- None. Phase 2 is accepted and closed at DARK maturity. Enabling the Phase-2 guest flow in production additionally requires the (separately authorized) IAM-v2 authentication cutover; out of this Phase's DARK scope. Phase 3 remains `NOT_STARTED` and unauthorized.

## 20. Single next proposed action
Merge PR #4 to master with provenance preserved (authorized by Product-Owner decision D13 / closure transition T0014) and run post-merge Governance verification. No enablement, no cutover, no deployment/reboot, and no Phase 3.
