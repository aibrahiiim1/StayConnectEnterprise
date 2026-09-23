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

WHY THE REQUIRED CONTEXTS MUST COME FROM A DISPATCH AT THE DELIVERY REF. The ruleset pins each required
context to integration_id 15368, the GitHub Actions app, and branch protection reads the check runs attached
to the PULL REQUEST HEAD SHA. That rules out both alternatives:

  * a `schedule`-triggered run always runs on the default branch, so its check runs attach to master and
    would never satisfy the pull request; and
  * a commit status posted through the Statuses API carries a different integration (or none), so the pinned
    requirement refuses it -- which is exactly what the pin is for.

A `workflow_dispatch` at `ref: <delivery branch>` produces a run whose head_sha IS the branch tip, so its
check runs land on the PR head and satisfy the requirement honestly. That is the only mechanism that both
reports the pinned contexts and validates the intended commit.
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
# A scheduled GitHub run can start late. The window must be wide enough to absorb that and NARROWER than the
# gap between the two cron firings, or the wrong firing would also be inside it. The crons are one hour apart,
# so 55 minutes is the widest value that keeps exactly one firing eligible per night.
WINDOW_MINUTES = 55

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
# 1. THE SCHEDULE, IN Africa/Cairo SEMANTICS RATHER THAN A FROZEN UTC OFFSET
# ---------------------------------------------------------------------------------------------------------
def schedule_window(now_utc, tz_name=DELIVERY_TZ, hour=TARGET_LOCAL_HOUR, minute=TARGET_LOCAL_MINUTE,
                    window_minutes=WINDOW_MINUTES):
    """Is `now_utc` inside tonight's 03:10 local window?

    GITHUB CRON IS UTC-ONLY AND HAS NO TIMEZONE SUPPORT, and Egypt observes DST -- UTC+2 in winter, UTC+3
    from the last Friday of April to the last Thursday of October. A single hard-coded UTC cron is therefore
    wrong for half the year, which is precisely what this must not be.

    So the workflow fires at BOTH 00:10 and 01:10 UTC and this function decides which firing is the real one:
    it converts `now` to Africa/Cairo, takes 03:10 on that local date, converts THAT back to UTC, and admits
    the run only if `now` is in [target, target + window).

        winter (UTC+2): target = 01:10Z -> the 00:10Z firing is early and skips; the 01:10Z firing runs
        summer (UTC+3): target = 00:10Z -> the 00:10Z firing runs; the 01:10Z firing is an hour late,
                                           outside a 55-minute window, and skips

    Exactly one firing per night is admitted in both halves of the year, with no offset written down
    anywhere. DST transitions in Egypt happen at midnight local, so 03:10 always exists and is never
    ambiguous -- there is no gap or fold to resolve.
    """
    if ZoneInfo is None:
        return Decision(False, "NO_TZDB",
                        "zoneinfo is unavailable, so Africa/Cairo cannot be resolved; refusing rather than "
                        "assuming a UTC offset")
    if now_utc.tzinfo is None:
        now_utc = now_utc.replace(tzinfo=UTC)
    now_utc = now_utc.astimezone(UTC)

    zone = ZoneInfo(tz_name)
    local_now = now_utc.astimezone(zone)
    target_local = local_now.replace(hour=hour, minute=minute, second=0, microsecond=0)
    target_utc = target_local.astimezone(UTC)
    delta_s = (now_utc - target_utc).total_seconds()

    detail = {
        "now_utc": now_utc.isoformat(),
        "now_local": local_now.isoformat(),
        "target_local": target_local.isoformat(),
        "target_utc": target_utc.isoformat(),
        "utc_offset_hours": local_now.utcoffset().total_seconds() / 3600.0,
        "minutes_after_target": round(delta_s / 60.0, 2),
        "window_minutes": window_minutes,
    }
    if 0 <= delta_s < window_minutes * 60:
        return Decision(True, "IN_WINDOW",
                        "local time %s is inside tonight's %02d:%02d %s window"
                        % (local_now.strftime("%Y-%m-%d %H:%M"), hour, minute, tz_name), detail)
    if delta_s < 0:
        return Decision(False, "TOO_EARLY",
                        "local time %s is %.0f minutes BEFORE tonight's %02d:%02d %s target; this is the "
                        "other cron firing"
                        % (local_now.strftime("%H:%M"), -delta_s / 60.0, hour, minute, tz_name), detail)
    return Decision(False, "TOO_LATE",
                    "local time %s is %.0f minutes after tonight's %02d:%02d %s target, outside the "
                    "%d-minute window"
                    % (local_now.strftime("%H:%M"), delta_s / 60.0, hour, minute, tz_name, window_minutes),
                    detail)


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
def classify_gate_runs(expected_sha, correlation_id, runs, required_gates=REQUIRED_GATES):
    """Decide whether the four gates have freshly and successfully validated exactly `expected_sha`.

    `runs` is a list of dicts: workflow_file, head_sha, event, display_title, status, conclusion, id.

    FOUR INDEPENDENT THINGS ARE REQUIRED OF EVERY GATE, and each one closes a specific way this could be
    satisfied by something other than tonight's fresh validation of tonight's commit:

      event == workflow_dispatch   a `pull_request` or `push` run is not this mechanism. Under the new model
                                   the gates do not run on pull_request at all, but asserting the event means
                                   a future re-introduction cannot quietly start counting.
      head_sha == expected_sha     the commit actually validated. A branch can move between the moment the
                                   candidate is read and the moment a runner checks it out, and then the run
                                   is a true verdict about the WRONG commit.
      correlation id in the title  THIS night's dispatch, not a previous one. Tree-identical content from an
                                   earlier night would otherwise be indistinguishable, which is the whole
                                   point of demanding a fresh run.
      completed / success          a gate still running has not passed. Partial completion is refusal, not
                                   an opportunity to merge the three that finished.

    A gate reporting MORE than one matching run is also refused: two runs for one gate on one night means
    something dispatched twice and this cannot tell which verdict was meant.
    """
    expected_sha = str(expected_sha or "")
    correlation_id = str(correlation_id or "")
    if len(expected_sha) < 40:
        return Decision(False, "NO_EXPECTED_SHA",
                        "the expected head sha is missing or not a full 40-character sha, so no run could "
                        "be proved to be about the right commit")
    if not correlation_id:
        return Decision(False, "NO_CORRELATION_ID",
                        "no correlation id, so a run from a previous night could not be told from tonight's")

    per_gate, problems = {}, []
    for wf in required_gates:
        matches = []
        for r in runs or []:
            if str(r.get("workflow_file") or "") != wf:
                continue
            why = []
            if str(r.get("event") or "") != "workflow_dispatch":
                why.append("event is %r" % r.get("event"))
            if str(r.get("head_sha") or "") != expected_sha:
                why.append("head_sha is %s" % str(r.get("head_sha"))[:12])
            if correlation_id not in str(r.get("display_title") or ""):
                why.append("does not carry tonight's correlation id")
            if why:
                continue
            matches.append(r)

        if not matches:
            per_gate[wf] = {"state": "MISSING"}
            problems.append("%s has no run for %s carrying tonight's correlation id"
                            % (wf, expected_sha[:12]))
            continue
        if len(matches) > 1:
            per_gate[wf] = {"state": "DUPLICATE", "runs": [m.get("id") for m in matches]}
            problems.append("%s has %d matching runs (%s); which verdict was meant is unknowable"
                            % (wf, len(matches), ", ".join(str(m.get("id")) for m in matches)))
            continue

        r = matches[0]
        status, concl = str(r.get("status") or ""), str(r.get("conclusion") or "")
        per_gate[wf] = {"state": "FOUND", "id": r.get("id"), "status": status, "conclusion": concl}
        if status != "completed":
            problems.append("%s is still %s" % (wf, status or "unreported"))
        elif concl != "success":
            problems.append("%s concluded %s" % (wf, concl or "unreported"))

    detail = {"expected_sha": expected_sha, "correlation_id": correlation_id, "per_gate": per_gate}
    if problems:
        return Decision(False, "GATES_NOT_ALL_GREEN",
                        "the four authoritative gates did not all freshly pass %s: %s"
                        % (expected_sha[:12], "; ".join(problems)), detail)
    return Decision(True, "ALL_FOUR_FRESH_GREEN",
                    "all four authoritative gates freshly passed %s under tonight's dispatch"
                    % expected_sha[:12], detail)


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
    state = str((pr_now or {}).get("mergeable_state") or "")
    if state != "clean":
        return Decision(False, "MERGEABLE_STATE_%s" % (state.upper() or "UNKNOWN"),
                        "mergeable_state is %r, not 'clean'. %s"
                        % (state,
                           "The branch is behind master and strict status checks are on; updating it would "
                           "write a new commit and invalidate tonight's verdict, so this waits."
                           if state == "behind" else
                           "Refusing to merge on anything but a clean state."))

    return Decision(True, "MAY_MERGE",
                    "all four gates freshly passed %s, the branch tip is still %s, no thread is unresolved "
                    "and the pull request is clean"
                    % (str(expected_sha)[:12], str(expected_sha)[:12]),
                    {"pr": (pr_now or {}).get("number"), "sha": expected_sha})


def summarise(decisions):
    """One-line-per-decision trace, so the run log says exactly why it did what it did."""
    out = []
    for name, d in decisions:
        out.append("%-22s %-26s %s" % (name, d.code, d.reason))
    return "\n".join(out)
