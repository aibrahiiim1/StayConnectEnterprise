# Product-Owner-Led Delivery Model (D42; amended 2026-11-02)

**CURRENT AMENDMENT — Product Owner decision 2026-11-02 (no GitHub CI).** The PO explicitly removed all GitHub Actions workflows and all required status checks. This overrides D42's former CI mechanics, not its local test, PR, review, security, environment or financial authorization boundaries. GitHub's active ruleset must retain PR-only merges, merge commits, review-thread resolution, no bypass, no force push and no deletion. Never claim the change effective until the live ruleset was checked. Historical D42 material below remains only where it does not contradict this amendment.

**Authoritative. Product-Owner governance decision D42, 2026-09-26, recorded by T0196.** It supersedes the
Nightly Authoritative Delivery model ([`NIGHTLY_AUTHORITATIVE_DELIVERY.md`](NIGHTLY_AUTHORITATIVE_DELIVERY.md),
T0182–T0195), which is kept as history. Where anything in this repository conflicts with this file, this file
wins.

## 1. Default mode: normal product development

Every normal feature, UI change, bug fix, refactor or code change follows:

```
REQUEST → SCOPE → IMPLEMENT → FOCUSED SELF-REVIEW → TARGETED TESTS → FIX RELATED ISSUES
→ BUILD IF NEEDED → DEPLOY TO THE TEST APPLIANCE WHEN AUTHORIZED → SMOKE / HEALTH CHECK
→ PRODUCT OWNER TESTING → PRODUCT OWNER ACCEPTANCE → PROTECTED MERGE TO MASTER → DONE
```

* **Scope.** Only the requested mission. Unrelated findings, cleanup, placeholders, wording or refactors are
  recorded as FOLLOW-UP. The one exception is an immediately related defect that stops the requested feature
  working correctly or safely: fix it and continue.
* **Implementation.** Complete, end to end. Routine decisions, related bugs and ordinary test failures are the
  agent's to solve; return only for a genuine new Product-Owner decision or a controlled boundary (§5).
* **Review.** Focused on the code and components the mission actually touched. No full-repository audit.
* **Testing.** Targeted tests for the changed components and the build of anything being deployed. Not the
  whole test matrix, not every browser suite, not the governance suites, not preflight stages 1/9, not the
  four comprehensive gates.
* **PRE-LIVE.** When the mission or a standing deployment authorization covers it: deploy the exact tested
  commit to the named test appliance, keep rollback, run a practical smoke/health check, confirm the change is
  there to test, and report the exact deployed source. **Deploying before the merge is intended.** No Central,
  Production or other environment unless that target is explicitly authorized; a PRE-LIVE mission the Product
  Owner requests authorizes Central as part of PRE-LIVE (§9, D67).
* **Then STOP** and report `READY FOR PRODUCT OWNER TESTING`. Requested changes continue on the same branch
  and pull request: fix, targeted tests, redeploy the affected component, return for testing. No new
  governance cycle per correction.

## 2. Merge authorization — PO controlled, without GitHub CI

**NO PO ACCEPTANCE = NO MERGE.** Approval is recorded in the mission/PR conversation and applies to the exact head reviewed; later changes require renewed approval. With GitHub CI removed, this is a mandatory delivery procedure, **not an automated GitHub status-check gate**. Do not apply or rely on `po-merge-approved` as a status-check substitute. No direct/force push or bypass, even for administrator accounts. GitHub cannot automatically prove PO approval once CI is removed.

## 3. Master protection

One active ruleset `master-protected-delivery`: protected PR-only merges, resolved review conversations, merge commits only, required approving reviews 0, deletion and force-push forbidden, no bypass actors, and **no required status checks**. There is no overlapping classic protection. Live ruleset and `governance/branch-protection.json` must match. Removing a required CI check must never remove the PR protection rule itself.

## 4. FULL CHECK THE WHOLE CODE — opt-in, local only

Run `python tools/full-check.py --ref <checked-out-head> --phase3-command '<real local command>' --phase4-command '<real local command>' --phase5-command '<real local command>'`, with each command executing the relevant complete existing suite against isolated infrastructure. The script runs `bash tools/preflight.sh --full` first and refuses to claim a full PASS if any local integration command is missing or fails. Also perform the required independent review, governance/state consistency, proven provenance and authorized environment verification. Never fake PASS from compilation or skip a required safety test.

No GitHub Actions workflow dispatches, pull-request CI or scheduled CI exist under the new PO decision. This removal does **not** authorize a merge, Go-Live, schema destruction, networking, Root CA, PMS/financial transactions, or unnamed deployments.

## 5. Controlled boundaries (stop for a new decision)

Product semantics or architecture · authentication / entitlement / security boundary · Root CA / trust ·
destructive data or DB migration · PMS configuration, posting or traffic · financial behaviour · networking
architecture/topology · enrollment / assignment / licensing semantics · deployment to an unnamed or
unauthorized environment · Go-Live. Routine actions already authorized inside a mission are not asked again.
Inside a PRE-LIVE mission, migrations and destructive changes to PRE-LIVE test data are already authorized (§9).

## 6. Governance overhead in normal mode

No routine stage 1/9 loops, governance suites, per-commit pack or manifest regeneration, receipt/state edits
during implementation, or record-only pull requests restating a merge. Keep the authoritative record accurate
with the smallest practical final update, at a milestone or on request.

## 7. Nothing runs unattended

No GitHub Actions workflows remain. No CI job runs automatically or on manual dispatch, and nothing merges automatically. The nightly orchestrator
(`nightly-authoritative-validation.yml`) and its tooling (`tools/nightly_delivery.py`,
`tools/nightly-status.py`, `scripts/ci/nightly-orchestrate.py`) were removed by T0196; the orchestrator was
first disabled in GitHub (`disabled_manually`). Their history remains in Git and in T0182–T0195.

## 8. Normal report format

```
STATUS: READY FOR PRODUCT OWNER TESTING / MERGED / BLOCKED
Changed:                 - ...
Targeted verification:   - ...
PRE-LIVE:                - deployed commit / not applicable; smoke result
Product Owner action:    - Test now / Approve merge / Decision required
Blocker:                 - NONE / exact blocker
```

## 9. PRE-LIVE missions run end to end, Appliance and Central (D67)

**Permanent Product-Owner decision D67, 2026-10-24, recorded by T0243.**

When the Product Owner requests a PRE-LIVE mission, that request is the full authorization for executing it
end to end across the PRE-LIVE appliance (`172.21.60.25`) and Central (`172.21.96.196`) — both permanently retired
on 2026-10-28 (Product-Owner record, T0264); the decision text is kept as recorded, and a mission now covers the
environment the Product Owner names in it, never the production Central by default:

* deployments to either host, including Central releases built from master or from the mission's branch;
* database migrations on either host;
* resetting or reconstructing test databases, resetting test accounts and reissuing test licences;
* directly related repairs, verification (including authenticated console checks), documentation and
  governance synchronization;
* Git delivery: commits, push, pull request, and the protected merge, with the `po-merge-approved` label applied
  for that mission's tested head.

**PRE-LIVE data is disposable.** Keeping test accounts, historical test records or test licences is not a reason
to interrupt a mission; recreate them when the mission needs them. A real person's account is not a test
account: verification uses a dedicated test operator.

**No intermediate checkpoints.** Routine technical decisions, migration or deployment approval, test-data loss
and recoverable failures inside PRE-LIVE are handled by the agent: investigate, fix, retry, complete, and return
one final report with the exact deployed versions, the real verification results and any genuine blocker. A
mission never ends at `READY FOR APPROVAL` for an action it already authorized.

**Unchanged.** Master protection (§3), the `po-merge-authorization` check and its per-head label (§2), tests, CI,
security and validation stay as they are. A PRE-LIVE mission must not affect real production users, external
financial systems, live PMS posting, Root CA or trust roots, or networking/topology outside PRE-LIVE, and it
does not authorize Go-Live. New product, architecture or security decisions still return to the Product Owner.

**Scope.** D67 applies to PRE-LIVE missions the Product Owner requests. A routine FAST change deploys only to an
appliance a standing authorization names, and Central is not part of that per-change deployment.

**Standing deployment authorization, 2026-10-28 (D79).** The Product Owner granted standing authorization for
routine software deployments to the test appliance `172.21.96.97` within approved work, without separate deployment
approval each time. It covers build and package, install, service restart, deployment verification, retry and
rollback, and recording the exact deployed commit read back from the installed artifact. It does not cover the
production Central, migrations or schema decisions, destructive or historical data changes, networking or topology,
PMS configuration, posting or traffic, financial or payment-provider activity, Guest activation, Root-CA or trust
changes, or Go-Live. It supersedes the standing per-change PRE-LIVE decision of 2026-09-24, whose appliance is
retired. In §1 and §9, "PRE-LIVE" now means the test environment the Product Owner names.

## 10. Production-real operating standard (permanent Product-Owner instruction, 2026-11-01)

Configured is not Working; Working is not Verified. A feature is done only when it operates end to end in its authorized
target environment, with runtime evidence: the service runs, state persists and moves correctly, enforcement is real, the
genuine configured endpoint is integrated, health and readiness report the actual runtime state, failures are handled and
recovered, and installation, upgrade and restart have been exercised. A configured, published, ACTIVE, mocked, dormant or
merely installed feature is not done, and a success or failure message that does not match what happened is a defect.
What cannot be proven is reported NOT VERIFIED. The standard grants no financial, destructive, network, PKI or Go-Live
authority. The binding text is in `CLAUDE.md` §0I.
