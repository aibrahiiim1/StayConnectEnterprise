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

## 2. Daytime: a cheap sentinel, and development is not interrupted

The four gate workflows **do** run on `pull_request` — that is the only trigger whose checks a ruleset-required
status check accepts (§4, *How the contexts are earned*). What changed is what they **do** on a daytime push.

Attempt 1 of a pull-request run executes exactly one step, first, before anything is checked out:

```yaml
- name: Daytime sentinel (attempt 1 establishes the required context and fails on purpose)
  if: github.event_name == 'pull_request' && github.run_attempt == 1
  run: |
    ...
    exit 1
```

Two properties, both deliberate:

- the required context **exists**, so GitHub can evaluate the rule at all; and
- it does **not pass**, so master cannot become mergeable before the nightly validation has actually run.

Because the step is first and fails, every heavy step beneath it is skipped: no Go build, no PostgreSQL, no
browser, no mutation matrix. The cost is seconds instead of 29–31 minutes.

**Nobody waits for attempt 1.** It is expected to be red, it says so in its own log, and it is not evidence
about the change. During the working day an agent therefore:

1. modifies code, commits, pushes — **the four contexts go red in seconds and no full-gate cycle starts**;
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

### The schedule is stated once, in Africa/Cairo

GitHub Actions accepts an IANA `timezone:` beside `cron:`, so the schedule is one line and Egypt's DST — UTC+2
in winter, UTC+3 from the last Friday of April to the last Thursday of October — is the platform's arithmetic
rather than ours:

```yaml
on:
  schedule:
    - cron: '10 3 * * *'
      timezone: "Africa/Cairo"
```

**What this replaced, kept here because it matters if `timezone:` is ever removed.** The first implementation
used two crons — `10 0 * * *` and `10 1 * * *`, one per possible offset — and computed which firing was the
real 03:10 local, exiting as a deliberate no-op on the other. It was correct, and it cost: one wasted run every
night, and a session-start check that then had to tell a no-op from a real verdict, because both exit zero.

**One check survives, and it is not that selection logic.** `schedule_sanity()` verifies the platform actually
honoured the timezone. If `timezone:` were dropped, mistyped, or unsupported, the same cron means 03:10 **UTC**
— 05:10 or 06:10 in Cairo — and nothing else in the system would notice: the gates would run, the merge would
happen, and a nightly process would quietly be a morning one for as long as nobody looked. A misconfiguration
that still produces green merges is the kind that lasts.

So the drift from 03:10 local is measured, and more than **105 minutes** refuses. That threshold is chosen to
separate two things that look alike from a distance:

| | drift | verdict |
|---|---|---|
| A late scheduler | minutes, occasionally tens of minutes | tolerated |
| `timezone:` ignored, winter | **+120 min** | `SCHEDULE_DRIFT`, refused |
| `timezone:` ignored, summer | **+180 min** | `SCHEDULE_DRIFT`, refused |

A `workflow_dispatch` run is exempt — it is expected at any hour, which is what manual means. Asserted for all
366 nights of a leap year in both directions: a correct 03:10-Cairo start proceeds, and a UTC-read start is
refused, with the ±120/±180 drift measured from the tz database rather than written down.

### The sequence, and what each step refuses

| Step | Refuses |
|---|---|
| **1. Schedule sanity** | A scheduled run that did not start near 03:10 Cairo — the signature of an ignored `timezone:`. |
| **2. One candidate** | Zero candidates → quiet no-op (green). **Two or more → hard refusal**, naming them; choosing between them would invent an intent nobody expressed. |
| **3. Re-run** | Each gate's existing `pull_request` run for that exact head is re-run. A gate with no such run to re-run is a refusal (`RERUN_INCOMPLETE`), not something to work around. |
| **4. Wait** | Partial completion. Three of four green is a refusal, not an opportunity. |
| **5. Classify** | Any run that is not the run we re-ran (matched by **run id**), not `pull_request`, not on `expected_sha`, not on a **later attempt** than the one it was on when we asked, not `completed`, or not `success`. |
| **6. Re-read** | **A stale pass.** The head is read again after the gates finish; if a commit landed during the run, tonight's verdict is about a commit that is no longer the tip, and it waits for the next night. |
| **7. Merge** | `merge` method only, **`sha` pinned**, so GitHub itself refuses if anything moved between decision and call. |

A candidate is an **open, non-draft pull request targeting `master`** without the `nightly-hold` label. Marking
a PR draft or labelling it `nightly-hold` are the two supported ways to keep work open overnight without it
being merged — neither requires weakening or disabling anything.

### How the contexts are earned, and the three mechanisms that do not work

A required context here is pinned to `integration_id: 15368` (the GitHub Actions app) and is evaluated for the
pull request. Three mechanisms were tried or considered, and only the third works:

| Mechanism | Outcome |
|---|---|
| A `schedule` run | Always runs on the default branch, so its checks attach to **master**, never to the PR. Unavailable. |
| A commit status via the Statuses API | Carries a different integration, which the pin refuses — exactly what the pin is for. Unavailable. |
| A `workflow_dispatch` at the delivery ref | **Refused by the ruleset, measured.** See below. |
| **Re-running the PR's own `pull_request` run** | **Works.** A re-run keeps `event=pull_request` and the same `head_sha`, so its checks count, and it arrives as attempt 2+, where the gates execute in full. |

#### The dispatch model was built, deployed, and refused by the platform

This is recorded rather than quietly removed, because every visible signal said it worked.

A `workflow_dispatch` at `ref: <delivery branch>` produces a run whose `head_sha` **is** the branch tip. On
PR #181 that run produced four green check runs, **under exactly the four required context names**, **from app
15368**, **attached to the pull-request head**, which GitHub reported as associated with the pull request. Asked
to merge on them, GitHub answered:

```
HTTP 405  Repository rule violations found
4 of 4 required status checks are expected.
```

— with all four green, and GraphQL `statusCheckRollup` **null** for that commit. **A dispatched run's check
runs do not satisfy a ruleset-required status check.** Attaching to the head, coming from the pinned app, and
being associated with the PR are each necessary and none is sufficient.

**The mistake worth naming is not the design, it is what was accepted as proof of it.** Seeing the four checks
land on the head was reported as the model working. It was evidence that the *artefact* appeared, not that the
*requirement* was satisfied, and the only thing that could tell the difference was asking GitHub to merge.

So the original repository rule — *never use `workflow_dispatch` to satisfy a required check* — turns out to
have been right for a reason nobody had written down: it cannot be done. `workflow_dispatch` is retired from
all four gates, and `scripts/ci/assert-dispatch-head.sh` is retired with it; under a re-run the head SHA is set
by GitHub from the run being re-run, so a wrong-commit run is not a thing that can occur.

#### Why a re-run is not a loophole either

| Hazard | Closed by |
|---|---|
| A cheap daytime attempt could satisfy the requirement | It **fails**. A non-passing context cannot merge anything, which is the sentinel's entire purpose. |
| A previous night's pass is reused as this night's | The orchestrator records the attempt each run was on **before** it asked, and requires a **later** attempt. An unchanged tree cannot produce one. |
| A green run for a different commit is counted | Matched by run id **and** `head_sha` **and** event. |
| A dispatched success is inherited as reuse evidence | The nightly attempt **declines reuse outright** (`NIGHTLY_VALIDATION=true`). |

### Fresh execution is guaranteed, not hoped for

The authoritative attempt is identified by the run itself — `github.event_name == 'pull_request' &&
github.run_attempt != 1` — so `NIGHTLY_VALIDATION` cannot be true on a run that is not one.

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
| `SUPERSEDED_FAILURE` | it failed, but a later commit has already moved the head on | 0 |
| `UNRESOLVED_FAILURE` | **repair this before starting new work** | 1 |
| `UNKNOWN` | status could not be read — treat as unknown, not as clear | 2 |

It reads the **verdict** each run recorded rather than its conclusion, because a dry run and a drift-refused
run both exit zero and either can sit above a red night. An unreadable verdict counts as authoritative, and an
unreadable tested head counts as unresolved: *"I cannot tell whether this was fixed"* is not *"it was fixed"*.

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
correctness (including an ignored `timezone:` in both DST halves, across 366 nights) · **a run still on attempt
1, so only the sentinel ran** · **a previous night's attempt offered as tonight's** · **a `workflow_dispatch`
run offered as a required context** · a master-push run offered instead · nothing having been re-run at all · a
different run id for the right gate · unreadable attempt numbers · a non-deciding run being mistaken for the
night's verdict · a repaired failure being reported as still owed · and the positive path, because a module
that refuses everything would pass every negative case.

**81 assertions**, run by the `governance` gate and again by the orchestrator before it decides anything.

The **fixtures** of the other suite are audited too: `run_mutations.py --anchors` resolves every mutation
case's anchor in a single read pass. That exists because fixture drift has broken this suite three separate
ways — aborting the whole matrix, silently mutating nothing while reporting `[MISS]`, and continuing to match
while the configuration it called a defect became the required state. `tools/preflight.sh` runs it in
seconds, rather than discovering it twenty gate-minutes later.
