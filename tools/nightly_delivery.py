#!/usr/bin/env python3
"""THE NIGHTLY AUTHORITATIVE DELIVERY DECISION, as pure functions.

WHAT THIS IS FOR. Normal development is no longer interrupted by the four full gates on every push: the gates
no longer run on `pull_request` at all. Instead, once a night at 03:10 Africa/Cairo, the exact current HEAD of
the single active delivery candidate is validated by ONE FRESH run of all four authoritative gates, and only
an all-green result on that exact HEAD is merged.

WHY THE LOGIC LIVES HERE AND NOT IN YAML. Every decision below can refuse a merge, and a decision that can
refuse a merge must be testable adversarially. Inline `if:` expressions and shell one-liners cannot be driven
through "what if the PR moved under us", "what if only three gates reported", "what if two candidates exist".
So the YAML is a thin caller and the judgement is here, exercised by tools/tests/nightly_delivery/.

EVERY FUNCTION FAILS CLOSED. The answer to any uncertainty -- no candidate, two candidates, a run that cannot
be matched, a head that moved, an unreadable field -- is "do not merge". None of them can answer "merge"
by accident, because each returns an explicit decision object whose default is refusal.

HOW THE REQUIRED CONTEXTS ARE EARNED, AND THE THREE MECHANISMS THAT DO NOT WORK. The ruleset pins each
required context to integration_id 15368 and evaluates it for the pull request. Measured on this repository:

  * a `schedule` run always runs on the default branch, so its checks attach to master -- never to the PR;
  * a commit status posted through the Statuses API carries a different integration, which the pin refuses;
  * a `workflow_dispatch` run at the delivery ref DOES put green checks with the right names, from app 15368,
    on the pull-request head, and GitHub even reports them as associated with the PR -- and the ruleset STILL
    refuses them. `HTTP 405 ... 4 of 4 required status checks are expected`, with statusCheckRollup null.
    Attaching to the head, the right app, and PR association are each necessary and none is sufficient.

WHAT WORKS IS A pull_request RUN, RE-RUN. The daytime push produces attempt 1, which fails in seconds as a
deliberate sentinel: the context exists so the rule can be evaluated, and is non-passing so nothing can merge
on a check that validated nothing. At 03:10 Africa/Cairo the orchestrator RE-RUNS that same run for the exact
head. A re-run keeps `event=pull_request` and the same head SHA, so its checks count, and it arrives as
attempt 2 or higher, where the gates execute in full.

The attempt number is therefore the freshness proof, and it is stronger than the correlation id it replaced:
this module is handed the exact run ids it asked to re-run and the attempt each was on beforehand, and it
requires those same runs to come back on a LATER attempt, green.
"""

from __future__ import annotations

import datetime as _dt

try:                                     # pragma: no cover - the stdlib path is the only one used in CI
    from zoneinfo import ZoneInfo
except ImportError:                       # pragma: no cover
    ZoneInfo = None                       # the caller reports this rather than guessing an offset

UTC = _dt.timezone.utc

DELIVERY_TZ = "Africa/Cairo"
TARGET_LOCAL_HOUR = 3
TARGET_LOCAL_MINUTE = 10
# How far from 03:10 local a SCHEDULED run may start before it is treated as evidence that the declared
# timezone was not honoured. A late scheduler drifts by minutes; a UTC-interpreted cron drifts by 120 minutes
# in winter and 180 in summer, so 105 separates the two without refusing a merely delayed run.
MAX_DRIFT_MINUTES = 105

REQUIRED_GATES = (
    "project-governance.yml",
    "phase3-software.yml",
    "phase4-financial-core.yml",
    "phase5-post-stay-transfer.yml",
)

# A label an agent can put on its own PR to say "not tonight" -- parked work, or a branch deliberately left
# open. It is the only way to have an open PR to master that the nightly will not act on, and it exists so
# that "park this" never requires weakening anything.
HOLD_LABEL = "nightly-hold"


class Decision:
    """An explicit verdict. `proceed` is False unless something actively set it True."""

    def __init__(self, proceed=False, code="REFUSED", reason="", detail=None):
        self.proceed = bool(proceed)
        self.code = code
        self.reason = reason
        self.detail = detail or {}

    def __repr__(self):                   # pragma: no cover - diagnostics only
        return "Decision(proceed=%r, code=%r, reason=%r)" % (self.proceed, self.code, self.reason)


# ---------------------------------------------------------------------------------------------------------
# 1. THE SCHEDULE -- STATED ONCE, AND VERIFIED RATHER THAN COMPUTED
# ---------------------------------------------------------------------------------------------------------
def schedule_sanity(now_utc, event="schedule", tz_name=DELIVERY_TZ, hour=TARGET_LOCAL_HOUR,
                    minute=TARGET_LOCAL_MINUTE, max_drift_minutes=MAX_DRIFT_MINUTES):
    """Did this run actually start near 03:10 Africa/Cairo?

    THE SCHEDULE ITSELF IS NO LONGER THIS FUNCTION'S JOB. GitHub Actions takes an IANA `timezone:` beside
    `cron:`, so the workflow states `10 3 * * *` in Africa/Cairo once and the platform resolves the offset
    across Egypt's DST transitions. What this function does now is much narrower, and it is the reason it still
    exists at all: IT CHECKS THAT THE PLATFORM DID WHAT THE WORKFLOW ASKED.

    If `timezone:` were ignored -- removed in an edit, unsupported on some runner, mistyped -- the cron would
    fire at 03:10 UTC, which is 05:10 or 06:10 in Cairo. Nothing else in this system would notice: the gates
    would run, the merge would happen, and a nightly process would silently be running in the morning for as
    long as nobody looked. A misconfiguration that still produces green merges is the kind that lasts.

    So the drift from 03:10 local is measured and a large one refuses. 105 minutes is chosen deliberately: a
    scheduled GitHub run can start late by minutes and occasionally more, while an ignored timezone shows up as
    a drift of at least 120 minutes (winter) or 180 (summer). The threshold separates the two cases without
    refusing a merely delayed run.

    A MANUAL RUN IS EXEMPT. `workflow_dispatch` is expected at any hour -- that is what it is for -- so the
    check applies only to the `schedule` event. It is a statement about the platform's timing, not about
    whether validation is allowed to happen.
    """
    if ZoneInfo is None:
        return Decision(False, "NO_TZDB",
                        "zoneinfo is unavailable, so Africa/Cairo cannot be resolved and the schedule cannot "
                        "be verified; refusing rather than assuming an offset")
    if now_utc.tzinfo is None:
        now_utc = now_utc.replace(tzinfo=UTC)
    now_utc = now_utc.astimezone(UTC)

    zone = ZoneInfo(tz_name)
    local_now = now_utc.astimezone(zone)
    target_local = local_now.replace(hour=hour, minute=minute, second=0, microsecond=0)
    drift_min = (local_now - target_local).total_seconds() / 60.0

    detail = {
        "event": event,
        "now_utc": now_utc.isoformat(),
        "now_local": local_now.isoformat(),
        "target_local": target_local.isoformat(),
        "utc_offset_hours": local_now.utcoffset().total_seconds() / 3600.0,
        "drift_minutes": round(drift_min, 2),
        "max_drift_minutes": max_drift_minutes,
    }

    if str(event) != "schedule":
        return Decision(True, "NOT_SCHEDULED",
                        "this is a %s run, so the schedule check does not apply; local time is %s"
                        % (event, local_now.strftime("%Y-%m-%d %H:%M %Z")), detail)

    if abs(drift_min) <= max_drift_minutes:
        return Decision(True, "ON_SCHEDULE",
                        "started %s, %.0f minutes from the %02d:%02d %s target -- the platform honoured the "
                        "declared timezone"
                        % (local_now.strftime("%Y-%m-%d %H:%M %Z"), drift_min, hour, minute, tz_name), detail)

    return Decision(False, "SCHEDULE_DRIFT",
                    "started %s, which is %.0f minutes from the %02d:%02d %s target. That is far more than a "
                    "late scheduler and is what an IGNORED `timezone:` looks like -- a UTC-interpreted cron "
                    "lands 120 minutes late in winter and 180 in summer. Refusing: a nightly merge running at "
                    "the wrong hour would otherwise go unnoticed indefinitely"
                    % (local_now.strftime("%Y-%m-%d %H:%M %Z"), drift_min, hour, minute, tz_name), detail)


# ---------------------------------------------------------------------------------------------------------
# 2. THE ACTIVE DELIVERY CANDIDATE -- EXACTLY ONE, OR NOTHING
# ---------------------------------------------------------------------------------------------------------
def select_candidate(pulls):
    """Pick the single active delivery candidate, or refuse.

    `pulls` is a list of dicts: number, draft, base_ref, head_ref, head_sha, labels, state.

    THE TWO REFUSALS ARE DIFFERENT AND BOTH MATTER. No candidate is a quiet, correct no-op: there is simply
    nothing to validate tonight, and reporting that as a failure would train everyone to ignore the run.
    MORE than one candidate is a hard refusal: choosing between them -- by number, by age, by branch name --
    would be this system inventing an intent nobody expressed, and the one thing worse than not merging
    tonight is merging the wrong branch unattended.

    A DRAFT IS NOT A CANDIDATE, and neither is a PR carrying the hold label. Those are the two supported ways
    to keep work open overnight without it being merged, and they exist so that "not tonight" never needs a
    workflow edit or a disabled check.
    """
    considered, skipped = [], []
    for p in pulls or []:
        if str(p.get("state") or "open").lower() != "open":
            skipped.append((p.get("number"), "not open"))
            continue
        if str(p.get("base_ref") or "") != "master":
            skipped.append((p.get("number"), "base is %r, not master" % p.get("base_ref")))
            continue
        if p.get("draft"):
            skipped.append((p.get("number"), "draft"))
            continue
        labels = [str(x).lower() for x in (p.get("labels") or [])]
        if HOLD_LABEL in labels:
            skipped.append((p.get("number"), "carries the %s label" % HOLD_LABEL))
            continue
        if not p.get("head_sha"):
            skipped.append((p.get("number"), "no head sha reported"))
            continue
        considered.append(p)

    detail = {"considered": [p.get("number") for p in considered],
              "skipped": [{"number": n, "why": w} for n, w in skipped]}

    if not considered:
        return Decision(False, "NO_CANDIDATE",
                        "no open, non-draft pull request targeting master without the %s label; there is "
                        "nothing to validate tonight and that is not a failure" % HOLD_LABEL, detail)
    if len(considered) > 1:
        return Decision(False, "AMBIGUOUS_CANDIDATES",
                        "%d delivery candidates are open at once (%s). Refusing: choosing between them "
                        "would invent an intent nobody expressed. Leave exactly one open, mark the others "
                        "draft, or label them %s"
                        % (len(considered), ", ".join("#%s" % p.get("number") for p in considered),
                           HOLD_LABEL), detail)

    pr = considered[0]
    detail["chosen"] = {"number": pr.get("number"), "head_ref": pr.get("head_ref"),
                        "head_sha": pr.get("head_sha")}
    return Decision(True, "ONE_CANDIDATE",
                    "pull request #%s (%s) at %s is the single active delivery candidate"
                    % (pr.get("number"), pr.get("head_ref"), str(pr.get("head_sha"))[:12]), detail)


# ---------------------------------------------------------------------------------------------------------
# 3. THE RUNS THAT MAY COUNT -- FRESH, THIS SHA, THIS NIGHT, ALL FOUR
# ---------------------------------------------------------------------------------------------------------
def classify_rerun_runs(expected_sha, requested, runs, required_gates=REQUIRED_GATES):
    """Have the four gates freshly and successfully re-run for exactly `expected_sha`?

    `requested` maps workflow file -> {"id": run id we asked to re-run, "attempt": its attempt BEFORE we asked}.
    `runs` is a list of dicts: workflow_file, id, head_sha, event, status, conclusion, run_attempt.

    FIVE INDEPENDENT THINGS ARE REQUIRED OF EVERY GATE, and each closes a specific way this could be satisfied
    by something other than tonight's full execution of tonight's commit:

      the SAME RUN we re-ran      identified by run id, not by name or recency. A different run for the same
                                  gate -- an older commit, a re-opened PR -- cannot be mistaken for this one.
      event == pull_request       the only event whose checks the ruleset counts. Asserted so that a future
                                  change back to dispatch cannot quietly start counting again.
      head_sha == expected_sha    the commit actually validated. The branch can move while the gates run.
      run_attempt > the attempt   the sentinel is attempt 1 and does nothing. A LATER attempt is the only one
      it was on before           that executed the gate, so the number is the freshness proof: a previous
                                  night's attempt on an unchanged tree cannot stand in for tonight's.
      completed / success         a gate still running has not passed. Partial completion is refusal.
    """
    expected_sha = str(expected_sha or "")
    if len(expected_sha) < 40:
        return Decision(False, "NO_EXPECTED_SHA",
                        "the expected head sha is missing or not a full 40-character sha, so no run could "
                        "be proved to be about the right commit")
    if not requested:
        return Decision(False, "NOTHING_WAS_RERUN",
                        "no run was re-run, so there is nothing that could have executed the gates tonight")

    by_id = {}
    for r in runs or []:
        by_id[str(r.get("id"))] = r

    per_gate, problems = {}, []
    for wf in required_gates:
        want = (requested or {}).get(wf)
        if not want:
            per_gate[wf] = {"state": "NOT_REQUESTED"}
            problems.append("%s was never re-run" % wf)
            continue
        r = by_id.get(str(want.get("id")))
        if r is None:
            per_gate[wf] = {"state": "MISSING", "id": want.get("id")}
            problems.append("%s run %s could not be read back" % (wf, want.get("id")))
            continue

        info = {"state": "FOUND", "id": r.get("id"), "status": r.get("status"),
                "conclusion": r.get("conclusion"), "attempt": r.get("run_attempt"),
                "attempt_before": want.get("attempt")}
        per_gate[wf] = info

        if str(r.get("event") or "") != "pull_request":
            problems.append("%s is a %r run; only pull_request checks satisfy the ruleset"
                            % (wf, r.get("event")))
            continue
        if str(r.get("head_sha") or "") != expected_sha:
            problems.append("%s validated %s, not %s"
                            % (wf, str(r.get("head_sha"))[:12], expected_sha[:12]))
            continue
        try:
            attempt_now = int(r.get("run_attempt") or 0)
            attempt_before = int(want.get("attempt") or 0)
        except (TypeError, ValueError):
            problems.append("%s attempt numbers are unreadable, so freshness cannot be established" % wf)
            continue
        if attempt_now <= attempt_before:
            problems.append("%s is still on attempt %d; the re-run did not start, so only the sentinel has "
                            "run" % (wf, attempt_now))
            continue
        status, concl = str(r.get("status") or ""), str(r.get("conclusion") or "")
        if status != "completed":
            problems.append("%s is still %s" % (wf, status or "unreported"))
        elif concl != "success":
            problems.append("%s concluded %s" % (wf, concl or "unreported"))

    detail = {"expected_sha": expected_sha, "per_gate": per_gate}
    if problems:
        return Decision(False, "GATES_NOT_ALL_GREEN",
                        "the four authoritative gates did not all freshly pass %s: %s"
                        % (expected_sha[:12], "; ".join(problems)), detail)
    return Decision(True, "ALL_FOUR_FRESH_GREEN",
                    "all four authoritative gates freshly re-ran and passed %s" % expected_sha[:12], detail)


# ---------------------------------------------------------------------------------------------------------
# 4. THE MERGE PRECONDITION -- INCLUDING THE ONE THAT INVALIDATES A PASS
# ---------------------------------------------------------------------------------------------------------
def merge_precondition(gates, expected_sha, pr_now, unresolved_threads):
    """May the protected merge proceed?

    `gates` is the Decision from classify_gate_runs. `pr_now` is the pull request re-read AFTER the gates
    finished: number, head_sha, mergeable, mergeable_state, state.

    THE RULE THAT MATTERS MOST IS THE RE-READ. A gate run takes about half an hour, and a commit pushed
    during it produces exactly the dangerous state this model must never merge: a genuine all-green verdict
    about a commit that is no longer the branch tip. So the head is read again at the end and must still be
    the sha that was tested. A newer commit does not fail -- it simply waits for the next night, which is
    what the Product Owner asked for.

    The other three come from the live ruleset rather than from opinion, and each would otherwise turn into a
    confusing API error at the moment of merging:

      mergeable / mergeable_state   `strict_required_status_checks_policy` is on, so a branch BEHIND master
                                    cannot merge. Bringing it up to date would mean writing a new commit,
                                    which would invalidate the very verdict just earned -- so this reports
                                    and waits instead of quietly rebasing.
      unresolved review threads     `required_review_thread_resolution` is true. An unresolved automated
                                    review thread blocks the merge, and saying so plainly is more useful at
                                    03:10 than a 405 from the merge endpoint.
    """
    if not getattr(gates, "proceed", False):
        return Decision(False, "GATES_NOT_GREEN", gates.reason, getattr(gates, "detail", {}))

    head_now = str((pr_now or {}).get("head_sha") or "")
    if not head_now:
        return Decision(False, "HEAD_UNREADABLE",
                        "the pull request head could not be re-read after the gates finished, so it cannot "
                        "be proved unchanged")
    if head_now != str(expected_sha):
        return Decision(False, "HEAD_MOVED",
                        "the pass is STALE: %s was validated but the branch tip is now %s. A newer commit "
                        "invalidates tonight's verdict, and the next nightly run judges the new head"
                        % (str(expected_sha)[:12], head_now[:12]),
                        {"tested": expected_sha, "now": head_now})

    if str((pr_now or {}).get("state") or "open").lower() != "open":
        return Decision(False, "PR_NOT_OPEN",
                        "pull request #%s is no longer open" % (pr_now or {}).get("number"))

    try:
        threads = int(unresolved_threads)
    except (TypeError, ValueError):
        return Decision(False, "THREADS_UNREADABLE",
                        "the number of unresolved review threads could not be read; the ruleset requires "
                        "thread resolution, so this refuses rather than guessing")
    if threads > 0:
        return Decision(False, "UNRESOLVED_THREADS",
                        "%d review thread(s) are unresolved and the ruleset requires resolution; the merge "
                        "would be refused" % threads)

    if (pr_now or {}).get("mergeable") is False:
        return Decision(False, "NOT_MERGEABLE",
                        "GitHub reports the pull request as not mergeable (state %r)"
                        % (pr_now or {}).get("mergeable_state"))

    # WHY `blocked` IS NOT AUTOMATICALLY A REFUSAL, and why this is not a weakening.
    #
    # `mergeable_state` is a SUMMARY. It reports `blocked` when ANY check in the rollup is unfinished --
    # including checks nothing requires. This repository has two third-party apps installed (observed on
    # PR #181: `cursor` and `kilo-code-bot`) that open an EMPTY check suite on every push, status `queued`,
    # zero runs, which never completes. The rollup is therefore permanently unfinished and the summary says
    # `blocked` forever, on a pull request whose four REQUIRED contexts are green.
    #
    # Demanding `clean` meant deferring to that summary instead of to the requirement. The requirement is the
    # ruleset: four named contexts, pinned to the Actions app, on this head -- and this orchestrator verifies
    # that far more precisely than the summary does, per gate, per sha, per attempt, refusing a run that
    # is not tonight's or not about this commit. Unresolved threads are checked separately above, and the
    # ruleset requires zero approvals.
    #
    # So `blocked` is accepted ONLY with all four gates positively established green and no unresolved thread,
    # and the merge call itself is PINNED to the sha -- GitHub applies the ruleset and refuses if anything is
    # genuinely unmet, which is reported as MERGE_REFUSED_BY_GITHUB rather than retried. Every other state is
    # still refused, and `behind` still waits rather than rebasing.
    state = str((pr_now or {}).get("mergeable_state") or "")
    if state not in ("clean", "blocked"):
        return Decision(False, "MERGEABLE_STATE_%s" % (state.upper() or "UNKNOWN"),
                        "mergeable_state is %r. %s"
                        % (state,
                           "The branch is behind master and strict status checks are on; updating it would "
                           "write a new commit and invalidate tonight's verdict, so this waits."
                           if state == "behind" else
                           "Refusing to merge on anything but 'clean', or 'blocked' with every required "
                           "context positively verified green."))

    return Decision(True, "MAY_MERGE",
                    "all four gates freshly passed %s, the branch tip is still %s, no thread is unresolved "
                    "and the pull request is mergeable (state %s)"
                    % (str(expected_sha)[:12], str(expected_sha)[:12], state or "unknown"),
                    {"pr": (pr_now or {}).get("number"), "sha": expected_sha,
                     "mergeable_state": state})


# ---------------------------------------------------------------------------------------------------------
# 5. READING BACK WHAT A NIGHT DECIDED -- used by the session-start check, not by the orchestrator
# ---------------------------------------------------------------------------------------------------------
# Verdicts that mean "this run deliberately decided nothing". They are SUCCESSES, so they are
# indistinguishable from a good night by conclusion alone -- which is the trap select_authoritative_run
# exists for.
#
# The dual-cron no-op that used to be the main entry here is GONE, because one timezone-aware schedule has no
# second firing. What remains is the manual case: a dry run, or a scheduled run refused for drift. Deleting
# this set along with the no-op would have reopened the same false-CLEAR path through `--dry_run`.
NOOP_VERDICTS = ("WOULD_MERGE_DRY_RUN", "SCHEDULE_DRIFT", "ORCHESTRATOR_UNUSABLE")


def select_authoritative_run(runs):
    """The newest orchestrator run that actually DECIDED something.

    THIS IS NOT runs[0], AND THE REASON SURVIVED THE SCHEDULE CHANGE. It used to be the nightly no-op firing:
    two crons, one of which exited 0 having done nothing, and under EEST that no-op was the LATER run -- so
    runs[0] was the no-op and a session-start check reading it would report CLEAR while the night's real
    validation failed. One timezone-aware schedule removed that firing.

    What did not go away is the manual run. A dry run at midday succeeds, a scheduled run refused for clock
    drift succeeds, and either can sit at runs[0] above a red night. The tool whose whole job is to notice a
    failure would again be the thing hiding it, so the verdict -- not the conclusion -- decides which run
    speaks for the night.

    A run whose verdict cannot be read at all is treated as authoritative rather than skipped: an unreadable
    verdict must not become a way to skip a red night.
    """
    for r in runs or []:
        v = str((r or {}).get("verdict") or "").strip().upper()
        if v in NOOP_VERDICTS:
            continue
        return r
    return None


def failure_is_unresolved(run, candidate_heads):
    """Is this red night still waiting for somebody, or has the branch already moved past it?

    THE DOCUMENTED RULE IS "RED AND NOT YET SUPERSEDED", and the first implementation only checked "red".
    That matters because a red night followed by a fix is the NORMAL path through this model: if every red run
    stayed unresolved forever, the session-start check would tell every future session to stop and repair
    something that had already been repaired.

    So the head that FAILED is compared with the heads currently on offer. Still a candidate head -> nobody
    has addressed it, and the next session must. No longer a candidate head -> a commit has landed since, and
    the next nightly run judges that new head, which is exactly what the model says happens.

    An unknown tested head is treated as UNRESOLVED, because "I cannot tell whether this was fixed" is not
    the same as "it was fixed".
    """
    if not run:
        return False, "there is no authoritative run to judge"
    concl = str(run.get("conclusion") or "").lower()
    if concl == "success":
        return False, "the authoritative run succeeded"
    tested = str(run.get("tested_head") or "").strip()
    heads = [str(h) for h in (candidate_heads or [])]
    if not tested:
        return True, ("the run failed and the head it tested could not be read, so it cannot be shown to "
                      "have been superseded")
    if tested in heads:
        return True, ("the run failed on %s, which is STILL the delivery head -- nothing has addressed it"
                      % tested[:12])
    # TWO DIFFERENT REASONS A RED NIGHT IS NO LONGER OWED, and they read as different sentences because a
    # future session acts on this text. Joining them produced "the delivery head has moved to no open
    # candidate since", which is not a sentence and was printed by the very run that merged this delivery.
    if not heads:
        return False, ("the run failed on %s, and no candidate is open any more -- that pull request has been "
                       "merged or closed since, so nothing is owed" % tested[:12])
    return False, ("the run failed on %s, but the delivery head has moved to %s since; the next nightly run "
                   "judges the new head"
                   % (tested[:12], ", ".join(h[:12] for h in heads)))


def summarise(decisions):
    """One-line-per-decision trace, so the run log says exactly why it did what it did."""
    out = []
    for name, d in decisions:
        out.append("%-22s %-26s %s" % (name, d.code, d.reason))
    return "\n".join(out)
