# Product-Owner-Led Delivery Model (D42)

**Authoritative. Product-Owner governance decision D42, 2026-09-26, recorded by T0196.** It supersedes the
Nightly Authoritative Delivery model ([`NIGHTLY_AUTHORITATIVE_DELIVERY.md`](NIGHTLY_AUTHORITATIVE_DELIVERY.md),
T0182–T0195), which is kept as history. Where anything in this repository conflicts with this file, this file
wins.

## 1. Default mode: normal product development

Every normal feature, UI change, bug fix, refactor or code change follows:

```
REQUEST → SCOPE → IMPLEMENT → FOCUSED SELF-REVIEW → TARGETED TESTS → FIX RELATED ISSUES
→ BUILD IF NEEDED → DEPLOY TO PRE-LIVE WHEN AUTHORIZED → SMOKE / HEALTH CHECK
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
* **PRE-LIVE.** When the mission or the standing PRE-LIVE authorization covers it: deploy the exact tested
  commit to PRE-LIVE `172.21.60.25`, keep rollback, run a practical smoke/health check, confirm the change is
  there to test, and report the exact deployed source. **Deploying before the merge is intended.** No Central,
  Production or other environment unless that target is explicitly authorized.
* **Then STOP** and report `READY FOR PRODUCT OWNER TESTING`. Requested changes continue on the same branch
  and pull request: fix, targeted tests, redeploy the affected component, return for testing. No new
  governance cycle per correction.

## 2. Merge authorization — enforced, not remembered

**NO PRODUCT OWNER ACCEPTANCE = NO MERGE.**

A merge to master needs an explicit Product-Owner instruction — *Approved*, *Merge it*, *Approved, merge*, or
equally explicit — or a mission that said "implement and merge" in advance. Implementation completion and
PRE-LIVE deployment never authorize a merge.

GitHub enforces it:

| Mechanism | What it does |
|---|---|
| Ruleset `master-protected-delivery` | Requires exactly one status context, `po-merge-authorization`, pinned to the GitHub Actions app (15368). No bypass actors. |
| `.github/workflows/po-merge-authorization.yml` | Re-evaluates on opened / reopened / synchronize / ready_for_review / converted_to_draft / labeled / unlabeled. Runs in seconds. |
| `tools/po_merge_authorization.py` | Passes **only** while the pull request carries the label **`po-merge-approved`** on the exact head being judged and is not a draft. A push after approval **removes the label** and fails: what was tested is no longer what would merge. Every error fails. |

**Recording approval.** After the Product Owner's explicit instruction, the agent (or the Product Owner) applies
`po-merge-approved`. GitHub keeps the labelling event, actor and time in the PR timeline, and the check prints
them in its log. Then merge with a merge commit through the pull request.

*Limit, stated plainly:* the Product Owner and the agent use one GitHub account, so the check cannot tell who
applied the label. It makes an accidental or automatic merge impossible and every approval auditable. See
`governance/branch-protection.json` → `residual_risks`.

## 3. Master protection (unchanged except the required context)

PR-only delivery; no direct push; no force push; no deletion; **merge commits only**; **no bypass actors**;
conversation resolution required; **required approvals 0** (GitHub refuses self-approval on this
single-collaborator repository); strict (branch current with master). Recorded in
`governance/branch-protection.json` and compared with the live ruleset by `tools/validate-branch-protection.py`.

## 4. FULL CHECK THE WHOLE CODE — explicit opt-in only

The comprehensive validation runs **only** when the Product Owner says **"FULL CHECK THE WHOLE CODE"**. Then:

1. `python tools/full-check.py --ref <branch>` — dispatches the four comprehensive gates (`governance`,
   `phase3-full-software-gate`, `phase4-financial-core-gate`, `phase5-post-stay-transfer-gate`) at the ref,
   waits, and reports each verdict for the exact head. The gates execute fresh (no evidence reuse).
2. Full repository/code review, full relevant local suites and browser suites.
3. Complete governance/state consistency: `bash tools/preflight.sh` (all stages), stale-state and closure
   validation, packs, manifests and evidence synchronization.
4. Complete provenance checks, and full live verification where deployment is authorized.
5. Correct related failures; final comprehensive report.

FULL CHECK does **not** authorize a merge, Go-Live, destructive database changes, new PMS or financial
traffic, networking-architecture changes, or a deployment to an unnamed environment.

The four gates are `workflow_dispatch` only. No pull request, push or schedule triggers them; that is asserted
by `tools/validate-delivery-protocol.py`.

## 5. Controlled boundaries (stop for a new decision)

Product semantics or architecture · authentication / entitlement / security boundary · Root CA / trust ·
destructive data or DB migration · PMS configuration, posting or traffic · financial behaviour · networking
architecture/topology · enrollment / assignment / licensing semantics · deployment to an unnamed or
unauthorized environment · Go-Live. Routine actions already authorized inside a mission are not asked again.

## 6. Governance overhead in normal mode

No routine stage 1/9 loops, governance suites, per-commit pack or manifest regeneration, receipt/state edits
during implementation, or record-only pull requests restating a merge. Keep the authoritative record accurate
with the smallest practical final update, at a milestone or on request.

## 7. Nothing runs unattended

No workflow has a schedule and no workflow merges. The nightly orchestrator
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
