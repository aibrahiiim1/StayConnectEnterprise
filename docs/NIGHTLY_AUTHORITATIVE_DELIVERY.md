# Nightly Authoritative Delivery, and the Standing PRE-LIVE Deployment Decision

**Product-Owner decision, 2026-09-24. Authoritative. Where an earlier delivery or deployment rule in this
repository contradicts this document, this document is current and the earlier wording is historical.**

---

## 1. Why this exists

The four authoritative gates cost ~29–31 minutes of wall clock on every push, measured across a full day of
deliveries: `governance` alone is 1 704–1 851 s and it is the critical path in every run, because all four
start together. Queue time is zero, so none of that was GitHub being slow — it was the gates being thorough.

That is the right cost for *accepting* a change and the wrong cost for *making* one. Paying it on every push
meant the working day was spent waiting on validation of commits that were about to be superseded anyway.

**So the cost moved rather than shrank.** Nothing was removed, shortened, or made unable to fail. The same
four gates run in full — once a night, against the one commit that is actually being proposed for master.

## 2. Daytime: development is not interrupted

The four gate workflows **do not run on `pull_request` at all**, and on `push` only for `master`. During the
working day an agent therefore:

1. modifies code, commits, pushes — **no full-gate cycle starts**;
2. deploys completed changes to **PRE-LIVE `172.21.60.25`** under the standing decision in §3;
3. verifies the deployment and records the exact deployed commit;
4. continues working.

`bash tools/preflight.sh` remains the local instrument for finding what the gates would refuse. It is now
more valuable, not less: it is the only fast signal between pushes.

## 3. The standing PRE-LIVE deployment decision

> **Until explicitly revoked or changed by the Product Owner, every completed application/code change may be
> deployed promptly to the PRE-LIVE appliance at `172.21.60.25` without waiting for the nightly CI run.**

This is a **continuing** authorization. An agent session does not ask for it again, and asking again is itself
a defect in reading this document.

**It covers**, as directly required parts of deploying: building and packaging, installing, service restart,
deployment verification, retry and rollback, and recording exact deployed-source provenance.

**It does not authorize** — each remains a separate Product-Owner decision:

- a new product, architecture or security decision;
- destructive or historical data mutation;
- a new database migration or schema decision;
- networking or topology changes;
- PMS configuration, posting or traffic;
- financial or payment-provider activity;
- Guest activation or Guest Go-Live;
- Root-CA or trust-root changes;
- Go-Live.

### PRE-LIVE may be ahead of master, on purpose

A consequence of §2 and §3 together: during the day the deployed PRE-LIVE source is often **ahead of master**,
because it carries changes the nightly run has not yet judged. That is the intended state, not drift.

Two rules make it safe to read:

- **Always record the exact deployed commit SHA**, read back from the installed artifact rather than from the
  build host. `governance/project-state.json` → `current_state_facts.deployed_runtime_services` is where it
  lives, and it has been wrong immediately after three consecutive deployments in the past, so this is the
  field to update *as part of* deploying.
- **Never treat deployed PRE-LIVE provenance as authoritative master state.** "It is on the appliance" says
  nothing about whether it passed the gates. Only master says that.

## 4. Nightly: 03:10 Africa/Cairo, one complete fresh validation

`.github/workflows/nightly-authoritative-validation.yml` is the only thing that merges anything.

### The schedule is Africa/Cairo, not a frozen UTC offset

GitHub cron is UTC-only and has no timezone support, and **Egypt observes DST** — UTC+2 in winter, UTC+3 from
the last Friday of April to the last Thursday of October. A single hard-coded UTC cron is therefore an hour
wrong for half the year.

So the workflow fires **twice**, and `tools/nightly_delivery.py::schedule_window` decides which firing is
tonight's by converting 03:10 Africa/Cairo — read from the tz database — back to UTC:

| Season | Offset | 03:10 Cairo is | `10 0 * * *` | `10 1 * * *` |
|---|---|---|---|---|
| Winter (EET) | UTC+2 | `01:10Z` | skips, `TOO_EARLY` | **runs** |
| Summer (EEST) | UTC+3 | `00:10Z` | **runs** | skips, `TOO_LATE` |

The window is 55 minutes: wide enough to absorb a late scheduler, and narrower than the one-hour gap between
firings so the wrong firing can never also qualify. Asserted for **all 366 nights of a leap year**, across
both DST transitions, in `tools/tests/nightly_delivery/run_negative.py`.

### The sequence, and what each step refuses

| Step | Refuses |
|---|---|
| **1. Window** | Any firing that is not tonight's 03:10 Cairo. |
| **2. One candidate** | Zero candidates → quiet no-op (green). **Two or more → hard refusal**, naming them; choosing between them would invent an intent nobody expressed. |
| **3. Dispatch** | `workflow_dispatch` at the candidate's branch, carrying `expected_sha` and tonight's `correlation_id`. |
| **4. Wait** | Partial completion. Three of four green is a refusal, not an opportunity. |
| **5. Classify** | Any run that is not `workflow_dispatch`, not on `expected_sha`, not carrying tonight's correlation id, not `completed`, or not `success`. Two runs for one gate is also refused. |
| **6. Re-read** | **A stale pass.** The head is read again after the gates finish; if a commit landed during the run, tonight's verdict is about a commit that is no longer the tip, and it waits for the next night. |
| **7. Merge** | `merge` method only, **`sha` pinned**, so GitHub itself refuses if anything moved between decision and call. |

A candidate is an **open, non-draft pull request targeting `master`** without the `nightly-hold` label. Marking
a PR draft or labelling it `nightly-hold` are the two supported ways to keep work open overnight without it
being merged — neither requires weakening or disabling anything.

### Why a dispatch, and why that is not a loophole

A required context is pinned to `integration_id: 15368` (the GitHub Actions app) and branch protection reads
the check runs on the **pull-request head SHA**. Two alternatives were therefore unavailable rather than
merely worse:

- a `schedule`-triggered run always runs on the default branch, so its checks attach to **master**; and
- a commit status posted through the Statuses API carries a different integration, which the pin refuses —
  exactly what the pin is for.

A `workflow_dispatch` at `ref: <delivery branch>` produces a run whose `head_sha` **is** the branch tip, so
its checks land on the PR head honestly.

The earlier rule "never use `workflow_dispatch` to satisfy a required check" existed for two real hazards, and
both are closed by construction rather than by assertion:

| Old hazard | Closed by |
|---|---|
| A dispatched run blocks a PR whose own checks are green | There are no other checks. The dispatch is the only source of these contexts. |
| A dispatched success is inherited as reuse evidence | The nightly run **declines reuse outright** (`NIGHTLY_VALIDATION=true`), and the orchestrator counts only runs carrying **tonight's** correlation id. |
| A dispatch names a ref, so the branch can move under it | `scripts/ci/assert-dispatch-head.sh` fails the gate unless the head it is validating is exactly the decided sha. |

### Fresh execution is guaranteed, not hoped for

`scripts/ci/evidence-reuse.sh` refuses before it looks at anything when `NIGHTLY_VALIDATION=true`. This
matters most in the case that would otherwise be easiest to hit: a nightly run **after a failed night**, where
the tree is unchanged for the gates that passed, ancestry is trivially satisfied, the environment matches and
it is well under 24 hours. Reuse would skip precisely the steps the run exists to execute.

## 5. When a night fails

The branch is preserved, nothing is merged, and the failure evidence stays in the workflow run.

**The next agent session's first action is `python tools/nightly-status.py`.** It reports:

| State | Meaning | Exit |
|---|---|---|
| `CLEAR` | merged, no candidate, or correctly waiting | 0 |
| `IN_PROGRESS` | tonight's run is still going | 0 |
| `UNRESOLVED_FAILURE` | **repair this before starting new work** | 1 |
| `UNKNOWN` | status could not be read — treat as unknown, not as clear | 2 |

"Unresolved" is deliberately not "the last run was red": a red night followed by fixes is what the model
expects. It is red **and** not yet superseded.

Repair the related failures first, then continue normal work on the **same delivery line**. The following
night judges the resulting head.

## 6. What is unchanged

- **Protected master**: no direct push, no force push, **merge commits only**, **no bypass actor**, required
  approvals **0**.
- **All four gates run in full.** No test was removed, shortened, or made unable to fail.
- **One active delivery owner per branch.**
- `required_review_thread_resolution` is on, so an unresolved review thread blocks the nightly merge — the
  orchestrator says so plainly instead of returning an opaque API error at 03:10.
- `strict_required_status_checks_policy` is on, so a branch **behind** master cannot merge. The orchestrator
  reports and waits rather than rebasing, because writing a new commit would invalidate the verdict it just
  earned.

## 7. Where the fail-closed behaviour is proven

`tools/tests/nightly_delivery/run_negative.py`, wired into the `governance` gate and run again by the
orchestrator itself before it decides anything:

wrong candidate/head · stale pass after a newer commit · missing candidate · multiple ambiguous candidates ·
partial gate completion · a failed gate · a merge attempted without a fresh pass · scheduling and timezone
correctness · earlier daytime/PR/master/previous-night evidence attempting to substitute · and the positive
path, because a module that refuses everything would pass every negative case.
