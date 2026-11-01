# StayConnect — Direct Execution Mode

**Permanent project operating rules.** They override workflow preferences, governance, review and validation
habits and earlier agent conventions wherever they conflict. **The user (Product Owner, PO) is the final
authority.** Compacted on 2026-10-28 (T0265) without changing any rule; the full earlier wording is in Git
history.

---

## 0. TWO MODES — decide first, from what the work TOUCHES

Never decide the mode from how large, complex or file-heavy the work is. Many files, added tests or technical
complexity do **not** make work controlled. Safety controls stay; ceremony goes (this supersedes older wording
that demands heavy governance after routine work).

**FAST DEVELOPMENT — the default.** UI, wording, CSS, layout, button behaviour, small validation fixes, UI bugs,
bounded refactors, routine defects, and ordinary feature, API and business-logic work. Implement the outcome, fix
directly related routine defects, run only the **relevant targeted** build/tests, commit locally when useful,
report concisely. Do **not** automatically run or update: `governance/project-state.json`, transitions, evidence
or ChatGPT/project packs, change manifests, project-wide CI, PR creation or merge, project-wide stale-state audits,
unrelated documentation. Do not turn a small change into a release process. Related changes may accumulate across
requests; merge to master **only after PO acceptance** (§0F); documentation syncs once per finished batch; the
comprehensive validation runs only on **"FULL CHECK THE WHOLE CODE"**.

**CONTROLLED — only for work that touches:** database schema or migration · production/appliance deployment ·
destructive or historical-data modification · PMS protocol, configuration or traffic · financial posting, payment,
settlement, reversal or FX · guest authentication, Entitlement or enforcement core · enrollment/assignment/licensing
identity foundations · security, certificates, PKI or trust roots · networking architecture or appliance topology ·
an architecture, contract or product-semantics decision · Go-Live/cutover. Controlled work keeps its safety,
verification, evidence, CI and PO authorization; the governance rules in `docs/` apply in full, and only here.

**Governance sync is milestone-based:** an approved milestone, a verified live acceptance, a controlled deployment,
a product/architecture decision or an explicit release closure. Routine, cosmetic, UI, text, refactor and bug-fix
work creates no governance transition unless it materially changes authoritative production state.

**Scope discipline.** No side projects. Fix another issue in the same run **only** if it blocks the correctness or
safety of the task; if it needs a new PO decision, stop and state that exact decision; otherwise record it as
backlog. No project-wide audits, refactors, observability or documentation clean-ups unless asked.

**Never relaxed.** Authorization is never inferred for production changes, PMS traffic, financial actions,
destructive data operations, Root-CA changes or Go-Live (§12). No fake data, topology, managed state or protocol
behaviour. Approved product and architecture contracts bind until the PO changes them. Git is mandatory; the GitHub
repository is authoritative.

## 0A. ONE MISSION, END TO END (permanent PO rule; wins over any older "stop and ask again")

1. A PO authorization of a defined mission **is** the authorization for every routine step named in it or
   necessarily included in it — not a licence to begin and then ask to continue.
2. Carry it to DONE without routine intermediate approvals. Inside the mission the agent owns, where directly
   related: diagnosis, implementation, related fixes, targeted and regression tests, build/package, CI and required
   gates, lockfile/dependency and manifest/generated-artifact corrections, commits, pushes, PR creation, PR/CI fixes,
   protected merge, deployment **to an environment the mission includes**, deployment verification, rollback/retry,
   related documentation and governance sync, final post-merge/post-deployment verification.
3. A progress report is informational, never an approval checkpoint: running CI, a failed test or build, a stale
   manifest or pack, another needed commit, a routine deployment defect → diagnose, fix, verify, continue.
4. Return to the PO **only** for a genuinely new decision: changed product semantics · architecture · an
   unauthorized security/trust-boundary change · unauthorized destructive or historical data mutation · an
   unauthorized schema/migration · unauthorized PMS configuration/traffic or financial traffic · networking/topology ·
   deployment to an environment not authorized · Root-CA/trust-root change · Go-Live/cutover.
5. One mission may need many commits, PRs and fix cycles.
6. CONTROLLED stays CONTROLLED: no blanket authorization for future controlled actions; the mission must name the
   controlled boundary, after which the same authorization is not re-requested per step, and it does not carry into
   an unrelated later mission.
7. FAST stays lightweight: a simple request is not a release/deploy mission unless the PO put release, merge or
   deployment in the goal. Speed never buys out required tests, security checks, evidence, rollback safety or
   production-grade verification.
8. **One delivery owner per branch.** Two sessions never commit or push to the same delivery branch; on detecting a
   concurrent writer, settle one owner and have the other stand down.

Lifecycles: a task without release/deploy authorization is **REQUEST → EXECUTE → TARGETED CHECK → RESULT → STOP**
(§3, §10). An authorized mission is **AUTHORIZATION → EXECUTE → FIX DIRECT BLOCKERS → TEST → CI → PR → MERGE →
AUTHORIZED DEPLOYMENT → VERIFY → GOVERNANCE SYNC → ONE FINAL REPORT**; its own end is the stopping point. A mission
naming push, PR, merge or deployment **is** the solicitation §6 requires, for that mission only. Where a mission's
governance checks need a manifest refresh, block re-render or pack rebuild to merge, that is part of the mission.

## 0B. FAST DELIVERY, CI AND PARALLEL AGENTS

Authoritative: [`docs/FAST_DELIVERY_AND_PARALLEL_AGENT_PROTOCOL.md`](docs/FAST_DELIVERY_AND_PARALLEL_AGENT_PROTOCOL.md)
(enforced by `tools/validate-delivery-protocol.py`; that file wins).

* Normal work does not run the preflight, governance suites or the four gates (D42). **FULL CHECK THE WHOLE CODE**
  runs `python tools/full-check.py --ref <branch>` (the four gates, dispatched), `bash tools/preflight.sh` and the
  full local suites.
* **Master requires exactly one check, `po-merge-authorization`**: it passes only while the PR carries the label
  `po-merge-approved` on its current head (any later push removes it). Apply the label only after the PO's explicit
  merge instruction or a mission that authorized the merge. The four gates are `workflow_dispatch` only (they can
  never satisfy a ruleset-required check); nothing merges automatically.
* **Deployment order follows dependencies:** a backward-compatible API before the UI that needs it, or a verified
  atomic staged switch. Never expose a temporary 404.
* **Guest data never enters delivery evidence** (PR bodies, commits, governance records, export packs are
  published). Describe observations; never reproduce records. Commit messages are immutable: check before committing.
* **Parallel agents:** one Delivery Owner per branch owns commits, pushes, PR, merge and the final report; no two
  agents write, commit, push or `git add -A` on one branch or working tree; read-only investigation should run in
  parallel; code-producing subagents use isolated worktrees and non-overlapping scopes and never push; parallel
  tests use isolated databases, artifacts and ports; never competing E2E servers on one environment; never
  concurrent deployment or live mutation.

## 0C. OPERATIONAL VALUES ARE SETTINGS (standing PO rule, 2026-09-12)

Full form: [`docs/OPERATIONAL_SETTINGS_POLICY.md`](docs/OPERATIONAL_SETTINGS_POLICY.md).

> Operational values that a hotel administrator may reasonably need to change—such as attempt thresholds,
> waiting periods, retention periods and operational limits—must be available as persisted settings in the
> administration UI. Each setting requires a default, unit, explanation, appropriate scope, permission checks,
> validation and change auditing. Changing such values must not normally require editing code or redeploying.
> Reuse the established configuration system and maintain one source of truth.

Not required for protocol constants, cryptographic parameters or internal details, and not an authorization for a
project-wide settings refactor: it governs work as it is done.

## 0D. CURRENT ENVIRONMENT, AND WHAT IS NO LONGER A TARGET (PO record, 2026-10-28)

| Role | Host | Notes |
|---|---|---|
| **Production Central** | `41.128.240.133` (`central.onegate.getsemantics.com`; private `172.21.96.96`, same host) | Identity, activation, licensing only (§0E): `ctrlapi` behind Caddy `443`, appliance mutual TLS `9443`, `cloud-admin`, Postgres/TimescaleDB, Redis; `central-install.sh` / `central-deploy.sh` ([`docs/DEPLOYMENT_CLOUD.md`](docs/DEPLOYMENT_CLOUD.md)). Appliances dial the name in `deploy/config/central-endpoint.env`. **Production: tests and drills never default to it.** |
| **Test appliance** | `172.21.96.97` | Activated on the production Central; [installation manual](docs/runbooks/ONEGATE_FRESH_VM_INSTALLATION_MANUAL.md) Appendix 3. Standing deployment authorization: §0F. **Go-Live is not authorised.** |

**Retired targets.** One register: `governance/project-state.json` → `current_state_facts.retired_hosts`, enforced
by `tools/validate-standing-records.py`. It holds the retired former Central `172.21.96.196` (`sc-central.echofusion.com`)
and the retired former PRE-LIVE appliance `172.21.60.25` (both permanently retired 2026-10-28); the first Central
`150.0.0.252`, retired as a target since 2026-09-27 — all three servers permanently removed (PO, 2026-10-28); and the retired development reference appliance
(octets only, T0194). **Never contact, deploy to, test against or restore from them**, and never replace a retired
address with the production Central's: name the environment under test explicitly. Retiring a target does not
falsify its records: what it served and proved stays as recorded; Git history is never rewritten.

**PMS.** Preserve local-first behaviour: the appliance authorises guests from its last-good mirrored roster and
must keep doing so when the feed drops. A `DISCONNECTED` transport is a feed-health fact, not an authentication
outage; eligibility is still decided per stay by the mirror-trust and freshness rules; not every offline condition
has been tested.

## 0E. CENTRAL SERVES AN APPLIANCE FOR LICENSING ONLY (standing PO decision, 2026-09-13)

Guests, sessions, PMS, accounting and reports belong on the hotel's appliance; no guest data goes to Central. True
by construction: the appliance has no telemetry outbox, producer, NATS client, remote command/PMS channel, update
agent or cloud-mode setting. Everything it sends Central is HTTPS to `ctrlapi`: `/v1/appliances/register`
(token-less self-registration) · `/v1/appliance/csr`, `/certificate` · `/v1/appliance/license`,
`/offline-reconcile` · `/v1/appliance/hello` (licence enforcement: a deleted appliance stops admitting new guests) ·
`/v1/appliance/assignment`, `/assignment-registry`, `/assignment/ack` (signed tenant/site binding, mutual TLS) ·
`/v1/appliance/portal-tls/sync`, `/portal-tls/orders` (portal hostname and certificate lifecycle, placed with
Central 2026-10-13; no guest data; mutual TLS; [contract](docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md)
§7). **A new kind of appliance→Central message is a product decision**; never reclassify one as licensing because it
shares an endpoint, service or name. (History: telemetry was built and verified, switched off 2026-09-13; Central
migration 0045 and appliance migration 0093 removed it.)

## 0F. PO-LED DELIVERY (D42) AND STANDING DEPLOYMENT AUTHORIZATION

Full form: [`docs/PO_LED_DELIVERY_MODEL.md`](docs/PO_LED_DELIVERY_MODEL.md) (that file wins).

```
REQUEST → SCOPE → IMPLEMENT → FOCUSED SELF-REVIEW → TARGETED TESTS → FIX RELATED ISSUES → BUILD IF NEEDED
→ DEPLOY TO THE TEST APPLIANCE WHEN AUTHORIZED → SMOKE / HEALTH CHECK → STOP: READY FOR PRODUCT OWNER TESTING
→ PRODUCT OWNER ACCEPTANCE → PROTECTED MERGE TO MASTER → DONE
```

Unrelated findings are follow-up. Review is focused, tests targeted; corrections continue on the same branch and PR
with no new governance cycle. **NO PO ACCEPTANCE = NO MERGE:** merge only on an explicit instruction (*Approved*,
*Merge it*, *Approved, merge*) or a mission that said "implement and merge"; completion or deployment never
authorizes a merge. GitHub enforces it (`po-merge-authorization` + `po-merge-approved`,
`tools/po_merge_authorization.py`); direct push, force push, deletion and bypass are prohibited; merge commits only;
required approvals 0. **FULL CHECK THE WHOLE CODE** is opt-in only (full review, suites and browser suites,
governance and stale-state validation, packs/manifests/evidence, the four gates, provenance, live verification where
deployment is authorized, a comprehensive report); it authorizes no merge, Go-Live, destructive DB change,
PMS/financial traffic, networking change or unnamed environment. Nothing runs unattended: no schedule, no automatic
merge, no session-start check.

**Standing authorization (PO, 2026-10-28, D79): routine software deployments to the test appliance `172.21.96.97`
within approved work, without separate deployment approval each time.** It covers build and package, install,
service restart, deployment verification, retry and rollback, and recording exact deployed-source provenance. It
authorizes **none** of: the production Central · a new product/architecture/security decision · destructive or
historical data mutation · a new migration/schema decision · networking or topology changes · PMS configuration,
posting or traffic · financial or payment-provider activity · Guest activation or Go-Live · Root-CA or trust
changes. The test appliance will often be ahead of master: always record the **exact deployed commit SHA**, read back
from the installed artifact, and never treat deployed provenance as master state. (The 2026-09-24 standing decision
for the retired PRE-LIVE appliance is superseded by D79; its text is kept in the decision register.)

## 0G. PO-REQUESTED TEST MISSIONS RUN END TO END (D67)

Full form: [`docs/PO_LED_DELIVERY_MODEL.md`](docs/PO_LED_DELIVERY_MODEL.md) §9. A test (formerly PRE-LIVE) mission
the PO requests is the full end-to-end authorization for it in **the environment the PO names** (never the
production Central by default): deployments, migrations, test-database reset or reconstruction, test-account resets,
test-licence reissuance, directly related repairs, verification, documentation, and Git delivery including push, PR
and the protected merge. Test data is disposable (never a reason to stop; never touch a real person's account — use a
test operator). Do not stop for routine decisions, migration or deployment approval, test-data loss or recoverable
failures; return one final report, never `READY FOR APPROVAL` for something already authorized. Unchanged:
protected-master safeguards, tests, CI, security and validation; no real production users, external financial
systems, live PMS posting, Root CA/trust roots or networking outside the test environment; **Go-Live stays
unauthorized**; new product/architecture/security decisions stay the PO's. D67 does not turn a routine FAST change
into a Central deployment.

**Report format:** `STATUS: READY FOR PRODUCT OWNER TESTING / MERGED / BLOCKED` · Changed · Targeted verification ·
Deployment (deployed commit / not applicable, smoke result) · PO action (Test now / Approve merge / Decision
required) · Blocker (NONE / exact). No chronological diaries unless asked.

## 0H. ONE AGENT, TESTS ON REQUEST, FIVE-MINUTE TESTS, VISIBLE TIME (permanent PO rules, 2026-10-31)

Wins over §0B's parallel-agent text and over `docs/FAST_DELIVERY_AND_PARALLEL_AGENT_PROTOCOL.md` where they differ (that file is synced at
the next governance sync).

1. **No subagents, ever.** One primary agent does all the work in its own context: no subagents, child or task agents, parallel AI agents
   or delegated AI workers, for investigation, coding, review, documentation or testing. Ordinary shell and non-AI tools are allowed.
2. **Tests run only when the PO asks.** Without that request do not run unit, integration or browser suites, Docker labs, outage,
   long-duration, capacity or load tests, the four gates, governance validators, CI rounds or background test processes. Never weaken
   or remove a test. Prepare tests with the code. Static inspection of the change (formatting, compiling and vetting the touched packages)
   is not a test run.
3. **When testing is authorized:**
   - every accelerated scenario or process targets **at most 300 s of real time**;
   - simulate time with injected clocks, pre-dated records and the smallest real setting values;
   - never change production constants, and never ship a test control in a release;
   - what five minutes cannot prove stays **NOT VERIFIED** and is said so;
   - use the golden lab (`tools/oglab/golden/`) instead of rebuilding.

   The fresh-install path stays separate.
4. **The production database role is part of every test of a database writer:**
   - set `EDGED_ROLE_TEST_DSN` (and its equivalents);
   - grants that only a migration makes do not survive Gate-P;
   - a migration applied as `iam_v2_owner` cannot grant on objects it does not own.
5. **Visible time.**
   - Give an estimate when work starts and when any run longer than 10 minutes starts.
   - Report progress at least hourly and at every finding.
   - Never stay silent while something runs.
   - An unanswered question does not stop authorized work: continue and say so.
   - Record real elapsed time and preventable overhead in the final report.
6. **Branch cleanup after every protected merge.**
   - Delete the merged source branch locally and remotely, and remove its worktrees and the mission's temporary copies.
   - Only after verifying: the PR is merged; the merge commit is on master; the branch is not protected; it holds no unique unmerged work; no other owner depends on it; the provenance stays reachable.
   - Never delete unmerged work, or a branch still needed for pending verification.

---

## 1–13. DIRECT EXECUTION RULES

1. **Execute, do not review.** A requested code, configuration, database, deployment, production, file, deletion,
   migration, commit or push operation is executed directly. No preliminary, architectural, governance or
   project-safety review; no inspecting unrelated files for consequences; no scope expansion, problem hunting,
   hardening, clean-up or acceptance sweeps; no reinterpreting the request as a chance to improve adjacent systems.
   The requested change is the scope. This removes self-imposed review only: CONTROLLED work keeps the verification
   and authorization its action requires.
2. **No automatic project-wide checks.** Targeted verification of the change is expected, and a functional change
   carries the regression coverage it needs. Never automatically run, unless the user asks for that check or it
   belongs to a CONTROLLED action: the whole-project unit/integration/E2E suite, project-wide typecheck, lint,
   formatting, static analysis or security scans, governance, project-state, generated-block, stale-state, parity
   or transition-time validators, manifests, acceptance packs, inventory regeneration, CI status checks or Actions
   polling, git diff review, code review, repository-wide searches for related issues. A routine edit is complete when
   the edit is made and the relevant targeted check passes. Post-deployment verification, smoke tests and full
   regression belong to CONTROLLED work, where they stay required.
3. **Show the result immediately:** what changed, the directly relevant output, then stop — no review, validation,
   clean-up, documentation, governance sync, CI monitoring or further investigation unless asked.
4. **Production means execute.** An explicitly requested operation against production, deployment, live
   infrastructure, a live database or another named environment is intentional: no substitute review, no own
   approval workflow, no extra confirmation because the word production/live/deploy appears, no redirect to
   development; execute exactly the requested scope (the user owns that decision). Tool- or platform-enforced
   permission prompts may occur and are never bypassed.
5. **No governance ceremony.** Do not automatically modify, regenerate, inspect, reconcile or validate
   `governance/project-state.json`, generated state blocks, transition, decision or acceptance records, manifests,
   inventory or acceptance-candidate heads, generated packs, handoff documents, maturity fields, phase status or
   historical evidence — only when the request asks, or at a §0 governance-sync moment. A normal change needs no
   governance sync. Do not reopen old governance contradictions merely because you notice them.
6. **No unsolicited remote Git operations.** A local commit is allowed whenever useful. Never, unless asked or part of
   an authorized CONTROLLED delivery (§0, §12): amend, rebase, merge, tag, push, open a PR, wait for CI, poll
   Actions. `commit` → commit; `push` → push; `commit and push` → both, report, stop. Never wait for CI afterwards.
7. **Do not resurrect finished topics:** once the user moves on, do not reopen, re-audit or re-check them, nor bring
   back earlier legacy/governance/architecture concerns unless asked. Previous findings are not standing work items.
8. **Failure handling:** diagnose only the error blocking the requested operation, fix only what completes it, retry,
   stop once it works. No repository-wide investigation.
9. **User commands beat workflow conventions.** "نفذ", "عدل", "امسح", "اعمل deploy", "production", "commit", "push",
   "طبق ده" are execution instructions, not requests for analysis; never answer one with a plan for future checks.
10. **Default lifecycle: REQUEST → EXECUTE → SHOW RESULT → STOP.** Never, unless each step is asked for: REQUEST →
    INVESTIGATE → REVIEW → EXPAND SCOPE → VALIDATE EVERYTHING → GOVERNANCE → COMMIT → CI → MORE REVIEW.
11. **Persistence.** These rules are intentional and permanent. Do not drift back to governance, packs, manifests or
    full CI after routine work; do not ask in future sessions whether Direct Execution Mode still applies; do not
    reintroduce automatic reviews or checks; silence is not permission to restore earlier workflows. Stay in this
    mode until the user edits or revokes it.
12. **Authorization-bound actions** (§4 removes the agent's own ceremony, not the PO's) need explicit PO
    authorization: git operations (commit, push, tag, PR, merge), merges to master, deployment to any environment
    (the standing authorization in §0F counts for the test appliance), database migrations, production changes, PMS,
    payment-provider or financial traffic. Everything else: implement, fix routine blockers, continue. The
    authorization may be given **once, by the mission prompt**; do not re-request it per step or treat a progress
    report as its lapse. It never extends beyond that mission: anything it did not name (another environment, an
    unmentioned migration, PMS or financial traffic, Go-Live) needs its own decision (§0A).
13. **Review happens after delivery.** The PO reviews and tests live and sends corrections; do not pre-empt that with
    an own verification phase. Reports stay short: what changed, where, and anything preventing the result. Then stop.
