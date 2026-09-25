#!/usr/bin/env python3
"""FAIL-CLOSED PROOFS FOR THE NIGHTLY AUTHORITATIVE DELIVERY DECISION.

Every function in tools/nightly_delivery.py can refuse a merge, so every one of them is driven here through
the states that would make an unattended merge wrong. The Product Owner named eight of these explicitly and
they are all below, each asserted by the code path that actually decides -- not by reading the YAML.

The suite also asserts the POSITIVE path, because a decision module that refuses everything would pass every
negative test and never deliver anything. A refusal is only correct if the good case still merges.

Run:  python tools/tests/nightly_delivery/run_negative.py
Exit: 0 all assertions hold, 1 otherwise.
"""
import datetime as dt
import importlib.util
import os
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
_spec = importlib.util.spec_from_file_location("nd", os.path.join(ROOT, "tools", "nightly_delivery.py"))
nd = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(nd)

UTC = dt.timezone.utc
SHA_A = "a" * 40
SHA_B = "b" * 40
CORR = "nightly-20260924-abc123"

fails, oks = [], 0


def expect(label, decision, proceed, code_contains=None):
    global oks
    if bool(decision.proceed) != bool(proceed):
        fails.append("%s: proceed=%s, wanted %s (code=%s: %s)"
                     % (label, decision.proceed, proceed, decision.code, decision.reason[:150]))
        return
    if code_contains and code_contains not in decision.code:
        fails.append("%s: code=%r, wanted it to contain %r" % (label, decision.code, code_contains))
        return
    oks += 1
    print("  ok   %-64s [%s]" % (label, decision.code))


# THE RE-RUN MODEL. The daytime push leaves attempt 1 -- the sentinel -- failed. The orchestrator re-runs that
# same run id; the re-run keeps event=pull_request and the head sha, and arrives as attempt 2. So a fixture is
# a run id, the attempt it was on when we asked, and the attempt it came back on.
REQUESTED = {wf: {"id": 100 + i, "attempt": 1} for i, wf in enumerate(nd.REQUIRED_GATES)}


def rerun_run(wf, sha=SHA_A, event="pull_request", status="completed", conclusion="success",
              attempt=2, rid=None):
    return {"workflow_file": wf, "id": rid if rid is not None else REQUESTED[wf]["id"],
            "head_sha": sha, "event": event, "status": status, "conclusion": conclusion,
            "run_attempt": attempt}


def all_four(**kw):
    return [rerun_run(wf, **kw) for wf in nd.REQUIRED_GATES]


def pr(number=200, head=SHA_A, draft=False, base="master", labels=None, state="open",
       mergeable=True, mergeable_state="clean"):
    return {"number": number, "head_sha": head, "draft": draft, "base_ref": base,
            "head_ref": "delivery/x", "labels": labels or [], "state": state,
            "mergeable": mergeable, "mergeable_state": mergeable_state}


# =========================================================================================================
print("== 8. SCHEDULING AND TIMEZONE CORRECTNESS (one timezone-aware cron; verified, not computed) ==")
# The workflow now declares a single `- cron: '0 6 * * *'` with `timezone: "Africa/Cairo"`, so the PLATFORM
# resolves Egypt's DST -- UTC+2 in winter, UTC+3 from late April to late October. What is asserted here is the
# narrow thing that remains this repository's problem: that a run which did NOT start near 06:00 local is
# refused, because an ignored `timezone:` would fire at 06:00 UTC and merge quietly at the wrong hour forever.
WINTER = dt.datetime(2027, 1, 15, tzinfo=UTC)      # Cairo is UTC+2 -> 06:00 local == 04:00Z
SUMMER = dt.datetime(2027, 7, 15, tzinfo=UTC)      # Cairo is UTC+3 -> 06:00 local == 03:00Z

expect("winter: a run at 04:00Z is 06:00 Cairo and proceeds",
       nd.schedule_sanity(WINTER.replace(hour=4, minute=0)), True, "ON_SCHEDULE")
expect("summer: a run at 03:00Z is 06:00 Cairo and proceeds",
       nd.schedule_sanity(SUMMER.replace(hour=3, minute=0)), True, "ON_SCHEDULE")

# THE CASE THE CHECK EXISTS FOR: `timezone:` ignored, so the cron is read as UTC.
w_ignored = nd.schedule_sanity(WINTER.replace(hour=6, minute=0))
s_ignored = nd.schedule_sanity(SUMMER.replace(hour=6, minute=0))
expect("winter: an IGNORED timezone fires at 06:00Z = 08:00 Cairo and is refused",
       w_ignored, False, "SCHEDULE_DRIFT")
expect("summer: an IGNORED timezone fires at 06:00Z = 09:00 Cairo and is refused",
       s_ignored, False, "SCHEDULE_DRIFT")
if abs(w_ignored.detail["drift_minutes"] - 120.0) > 0.01:
    fails.append("winter drift measured %s, expected +120" % w_ignored.detail["drift_minutes"])
elif abs(s_ignored.detail["drift_minutes"] - 180.0) > 0.01:
    fails.append("summer drift measured %s, expected +180" % s_ignored.detail["drift_minutes"])
else:
    oks += 1
    print("  ok   the ignored-timezone drift really is +120 winter / +180 summer (measured, not assumed)")

# The offsets themselves are measured from the tz database rather than written down anywhere.
if abs(nd.schedule_sanity(WINTER.replace(hour=4, minute=0)).detail["utc_offset_hours"] - 2.0) > 0.01:
    fails.append("winter offset is not +2")
elif abs(nd.schedule_sanity(SUMMER.replace(hour=3, minute=0)).detail["utc_offset_hours"] - 3.0) > 0.01:
    fails.append("summer offset is not +3")
else:
    oks += 1
    print("  ok   Cairo really is UTC+2 in winter and UTC+3 in summer (read from the tz database)")

# A late scheduler must not be mistaken for a misconfiguration, and the threshold must sit between the two.
expect("a 40-minute-late scheduled start is tolerated",
       nd.schedule_sanity(WINTER.replace(hour=4, minute=40)), True, "ON_SCHEDULE")
expect("a 100-minute-late start is still tolerated (a late scheduler, not a wrong timezone)",
       nd.schedule_sanity(WINTER.replace(hour=5, minute=40)), True, "ON_SCHEDULE")
expect("a 120-minute drift is refused -- that is exactly the winter UTC misreading",
       nd.schedule_sanity(WINTER.replace(hour=6, minute=0)), False, "SCHEDULE_DRIFT")
expect("the middle of the working day is refused for a SCHEDULED run",
       nd.schedule_sanity(WINTER.replace(hour=12, minute=0)), False, "SCHEDULE_DRIFT")

# A MANUAL run is expected at any hour; the check is about the platform's timing, not about permission.
expect("a workflow_dispatch at midday is exempt, because that is what manual means",
       nd.schedule_sanity(WINTER.replace(hour=12, minute=0), event="workflow_dispatch"), True,
       "NOT_SCHEDULED")
expect("a workflow_dispatch on time is also fine",
       nd.schedule_sanity(WINTER.replace(hour=4, minute=0), event="workflow_dispatch"), True,
       "NOT_SCHEDULED")

# Every night of a leap year, in both offsets, a correctly-scheduled run proceeds and a UTC-read one does not.
good = bad_ = 0
for day in range(366):
    d = dt.datetime(2027, 1, 1, tzinfo=UTC) + dt.timedelta(days=day)
    local_target = d.astimezone(nd.ZoneInfo(nd.DELIVERY_TZ)).replace(hour=6, minute=0, second=0,
                                                                     microsecond=0)
    if nd.schedule_sanity(local_target.astimezone(UTC)).proceed:
        good += 1
    if not nd.schedule_sanity(d.replace(hour=6, minute=0)).proceed:
        bad_ += 1
if good != 366 or bad_ != 366:
    fails.append("across 366 nights: %d/366 correct starts accepted, %d/366 UTC-read starts refused"
                 % (good, bad_))
else:
    oks += 1
    print("  ok   366/366 nights: a correct 06:00-Cairo start proceeds and a UTC-read start is refused")

# =========================================================================================================
print()
print("== 3. MISSING ACTIVE CANDIDATE -- a quiet, correct no-op ==")
expect("no pull requests at all", nd.select_candidate([]), False, "NO_CANDIDATE")
expect("only a draft", nd.select_candidate([pr(draft=True)]), False, "NO_CANDIDATE")
expect("only a PR held by label",
       nd.select_candidate([pr(labels=[nd.HOLD_LABEL])]), False, "NO_CANDIDATE")
expect("only a PR targeting something other than master",
       nd.select_candidate([pr(base="develop")]), False, "NO_CANDIDATE")
expect("only a closed PR", nd.select_candidate([pr(state="closed")]), False, "NO_CANDIDATE")
expect("a candidate with no readable head sha", nd.select_candidate([pr(head="")]), False, "NO_CANDIDATE")

print()
print("== 4. MULTIPLE AMBIGUOUS CANDIDATES -- hard refusal, never an arbitrary choice ==")
two = nd.select_candidate([pr(number=200), pr(number=201, head=SHA_B)])
expect("two open non-draft PRs to master", two, False, "AMBIGUOUS")
if two.proceed or "#200" not in two.reason or "#201" not in two.reason:
    fails.append("the ambiguous refusal must name both candidates; got %r" % two.reason[:160])
else:
    oks += 1
    print("  ok   the refusal names both candidates so the operator can park one")
expect("three candidates", nd.select_candidate([pr(200), pr(201, SHA_B), pr(202, SHA_B)]),
       False, "AMBIGUOUS")
expect("one real candidate beside a draft and a held one is UNAMBIGUOUS",
       nd.select_candidate([pr(200), pr(201, SHA_B, draft=True),
                            pr(202, SHA_B, labels=[nd.HOLD_LABEL])]), True, "ONE_CANDIDATE")

# =========================================================================================================
print()
print("== 1. WRONG CANDIDATE / WRONG HEAD -- a true verdict about the wrong commit ==")
expect("every gate green, but for a different sha",
       nd.classify_rerun_runs(SHA_A, REQUESTED, all_four(sha=SHA_B)), False, "GATES_NOT_ALL_GREEN")
expect("no expected sha supplied at all",
       nd.classify_rerun_runs("", REQUESTED, all_four()), False, "NO_EXPECTED_SHA")
expect("a truncated sha is not an identity",
       nd.classify_rerun_runs(SHA_A[:12], REQUESTED, all_four(sha=SHA_A[:12])), False, "NO_EXPECTED_SHA")
expect("a different run id for the right gate is not the run we re-ran",
       nd.classify_rerun_runs(SHA_A, REQUESTED, all_four(rid=999)), False, "GATES_NOT_ALL_GREEN")

print()
print("== EARLIER / REUSED EVIDENCE MUST NOT SUBSTITUTE FOR TONIGHT'S FRESH RUN ==")
# THE SENTINEL IS ATTEMPT 1 AND VALIDATES NOTHING. A run still on attempt 1 has only run the sentinel, so
# "green on attempt 1" must never be accepted -- and it cannot be, because the sentinel fails by design.
expect("all four still on attempt 1 -- only the sentinel ran",
       nd.classify_rerun_runs(SHA_A, REQUESTED, all_four(attempt=1)), False, "GATES_NOT_ALL_GREEN")
expect("a previous night's attempt is not tonight's: attempt must be LATER than when we asked",
       nd.classify_rerun_runs(SHA_A, {wf: {"id": 100 + i, "attempt": 5}
                                      for i, wf in enumerate(nd.REQUIRED_GATES)},
                              all_four(attempt=5)), False, "GATES_NOT_ALL_GREEN")
expect("all four green but they were workflow_dispatch runs, whose checks the ruleset refuses",
       nd.classify_rerun_runs(SHA_A, REQUESTED, all_four(event="workflow_dispatch")),
       False, "GATES_NOT_ALL_GREEN")
expect("all four green but they were master push runs",
       nd.classify_rerun_runs(SHA_A, REQUESTED, all_four(event="push")), False, "GATES_NOT_ALL_GREEN")
expect("nothing was re-run at all",
       nd.classify_rerun_runs(SHA_A, {}, all_four()), False, "NOTHING_WAS_RERUN")
expect("unreadable attempt numbers cannot establish freshness",
       nd.classify_rerun_runs(SHA_A, REQUESTED, all_four(attempt="?")), False, "GATES_NOT_ALL_GREEN")

print()
print("== 5. PARTIAL GATE COMPLETION -- three of four is a refusal, not an opportunity ==")
for wf in nd.REQUIRED_GATES:
    runs = [r for r in all_four() if r["workflow_file"] != wf]
    expect("only three gates came back (%s absent)" % wf.replace(".yml", ""),
           nd.classify_rerun_runs(SHA_A, REQUESTED, runs), False, "GATES_NOT_ALL_GREEN")
    short = {k: v for k, v in REQUESTED.items() if k != wf}
    expect("only three gates could be re-run (%s not requested)" % wf.replace(".yml", ""),
           nd.classify_rerun_runs(SHA_A, short, all_four()), False, "GATES_NOT_ALL_GREEN")
expect("one gate still in progress on its new attempt",
       nd.classify_rerun_runs(SHA_A, REQUESTED,
                              [rerun_run(nd.REQUIRED_GATES[0], status="in_progress", conclusion="")] +
                              [rerun_run(wf) for wf in nd.REQUIRED_GATES[1:]]),
       False, "GATES_NOT_ALL_GREEN")
expect("no runs came back at all",
       nd.classify_rerun_runs(SHA_A, REQUESTED, []), False, "GATES_NOT_ALL_GREEN")

print()
print("== 6. A FAILED GATE ==")
for concl in ("failure", "cancelled", "timed_out", "skipped", "neutral", "action_required", ""):
    runs = [rerun_run(nd.REQUIRED_GATES[0], conclusion=concl)] + \
           [rerun_run(wf) for wf in nd.REQUIRED_GATES[1:]]
    expect("governance concluded %r" % (concl or "unreported"),
           nd.classify_rerun_runs(SHA_A, REQUESTED, runs), False, "GATES_NOT_ALL_GREEN")

print()
print("== THE POSITIVE PATH -- all four fresh, this sha, this night ==")
green = nd.classify_rerun_runs(SHA_A, REQUESTED, all_four())
expect("all four gates freshly green on the expected sha", green, True, "ALL_FOUR_FRESH_GREEN")

# =========================================================================================================
print()
print("== 2. A STALE NIGHTLY PASS -- a newer commit invalidates it ==")
moved = nd.merge_precondition(green, SHA_A, pr(head=SHA_B), 0)
expect("the branch tip moved while the gates ran", moved, False, "HEAD_MOVED")
if "STALE" not in moved.reason.upper():
    fails.append("the stale refusal should say so plainly; got %r" % moved.reason[:160])
else:
    oks += 1
    print("  ok   the refusal explains that the next nightly run judges the new head")
expect("the head cannot be re-read at all",
       nd.merge_precondition(green, SHA_A, pr(head=""), 0), False, "HEAD_UNREADABLE")

print()
print("== 7. AN ATTEMPTED MERGE WITHOUT A VALID FRESH NIGHTLY PASS ==")
for label, gates in (
    ("gates never came back", nd.classify_rerun_runs(SHA_A, REQUESTED, [])),
    ("a gate failed", nd.classify_rerun_runs(
        SHA_A, REQUESTED, [rerun_run(nd.REQUIRED_GATES[0], conclusion="failure")] +
        [rerun_run(wf) for wf in nd.REQUIRED_GATES[1:]])),
    ("only the sentinel ran", nd.classify_rerun_runs(SHA_A, REQUESTED, all_four(attempt=1))),
    ("the evidence was a dispatched run", nd.classify_rerun_runs(
        SHA_A, REQUESTED, all_four(event="workflow_dispatch"))),
):
    expect("merge refused because %s" % label,
           nd.merge_precondition(gates, SHA_A, pr(), 0), False, "GATES_NOT_GREEN")

print()
print("== THE LIVE RULESET'S OWN PRECONDITIONS ==")
expect("an unresolved review thread blocks the merge",
       nd.merge_precondition(green, SHA_A, pr(), 1), False, "UNRESOLVED_THREADS")
expect("an unreadable thread count is refused, not assumed zero",
       nd.merge_precondition(green, SHA_A, pr(), None), False, "THREADS_UNREADABLE")
expect("a branch BEHIND master waits instead of being rebased",
       nd.merge_precondition(green, SHA_A, pr(mergeable_state="behind"), 0), False, "BEHIND")
# `blocked` IS ACCEPTED ONLY WITH THE REQUIREMENT ITSELF VERIFIED. Observed on PR #181: two third-party apps
# (`cursor`, `kilo-code-bot`) open an empty check suite on every push -- status queued, zero runs -- which never
# completes, so the rollup summary says `blocked` forever on a pull request whose four required contexts are
# green. Deferring to the summary meant never merging; deferring to the requirement is what the ruleset says.
expect("blocked WITH all four gates verified green is accepted",
       nd.merge_precondition(green, SHA_A, pr(mergeable_state="blocked"), 0), True, "MAY_MERGE")
expect("blocked WITHOUT green gates is still refused",
       nd.merge_precondition(nd.classify_rerun_runs(SHA_A, REQUESTED, []), SHA_A,
                             pr(mergeable_state="blocked"), 0), False, "GATES_NOT_GREEN")
expect("blocked with an unresolved thread is still refused",
       nd.merge_precondition(green, SHA_A, pr(mergeable_state="blocked"), 2), False, "UNRESOLVED_THREADS")
expect("blocked on a head that moved is still refused",
       nd.merge_precondition(green, SHA_A, pr(head=SHA_B, mergeable_state="blocked"), 0), False, "HEAD_MOVED")
expect("a dirty/conflicted branch is refused",
       nd.merge_precondition(green, SHA_A, pr(mergeable_state="dirty"), 0), False, "DIRTY")
expect("mergeable=false is refused",
       nd.merge_precondition(green, SHA_A, pr(mergeable=False, mergeable_state="blocked"), 0),
       False, "NOT_MERGEABLE")
expect("an unknown mergeable_state is refused rather than retried blindly",
       nd.merge_precondition(green, SHA_A, pr(mergeable_state="unknown"), 0), False, "MERGEABLE_STATE")
expect("a PR closed during the run is refused",
       nd.merge_precondition(green, SHA_A, pr(state="closed"), 0), False, "PR_NOT_OPEN")

print()
print("== AND THE ONE CASE THAT MUST MERGE ==")
may = nd.merge_precondition(green, SHA_A, pr(), 0)
expect("four fresh green gates, head unchanged, clean, no open thread", may, True, "MAY_MERGE")

# A module that refuses everything would pass every negative test above. Two positives are asserted, and
# this makes that requirement explicit rather than incidental.
if not (green.proceed and may.proceed):
    fails.append("the positive path does not merge; a decision module that always refuses is not correct")

# =========================================================================================================
print()
print("== THE SESSION-START CHECK MUST NOT BE FOOLED BY A RUN THAT DECIDED NOTHING (review P1, PR #180) ==")
# The nightly no-op firing is GONE with the dual cron -- one timezone-aware schedule has no second firing. What
# remains is the manual case: a dry run at midday, or a scheduled run refused for clock drift. Both exit 0 and
# either can sit above a red night, so the VERDICT and never the conclusion decides which run speaks for the
# night. Deleting this selection along with the no-op would have reopened the same false-CLEAR path.
NOOP = {"id": 2, "conclusion": "success", "verdict": "WOULD_MERGE_DRY_RUN", "tested_head": ""}
REDRUN = {"id": 1, "conclusion": "failure", "verdict": "GATES_NOT_ALL_GREEN", "tested_head": SHA_A}
MERGED = {"id": 1, "conclusion": "success", "verdict": "MERGED", "tested_head": SHA_A}


def pick(label, runs, want_id):
    global oks
    got = nd.select_authoritative_run(runs)
    gid = (got or {}).get("id")
    if gid != want_id:
        fails.append("%s: selected run %r, wanted %r" % (label, gid, want_id))
        return
    oks += 1
    print("  ok   %-64s [run %s]" % (label, gid))


pick("the newest run is a DRY RUN; the red run beneath it is authoritative", [NOOP, REDRUN], 1)
pick("a dry run above a drift-refused run: both skipped",
     [NOOP, dict(NOOP, id=3, verdict="SCHEDULE_DRIFT", conclusion="failure"), MERGED], 1)
pick("a genuine newest run is selected normally", [MERGED, NOOP], 1)
pick("an UNREADABLE verdict counts as authoritative, never as skippable",
     [{"id": 9, "conclusion": "failure", "verdict": "", "tested_head": SHA_A}, MERGED], 9)
if nd.select_authoritative_run([NOOP, dict(NOOP, id=3)]) is not None:
    fails.append("a history of nothing but non-deciding runs should select nothing")
else:
    oks += 1
    print("  ok   a history of nothing but non-deciding runs selects no authoritative run")

print()
print("== A RED NIGHT IS ONLY UNRESOLVED WHILE ITS HEAD IS STILL THE HEAD (review P2 on PR #180) ==")
# "Red" is not "waiting for somebody": a red night followed by a fix is the normal path. The first version
# labelled every red run UNRESOLVED forever, which would tell every future session to repair something that
# had already been repaired.
for label, run, heads, want in (
    ("still the delivery head -> unresolved", REDRUN, [SHA_A], True),
    ("the head has moved on -> superseded", REDRUN, [SHA_B], False),
    ("no open candidate at all -> superseded", REDRUN, [], False),
    # AN UNREADABLE LOOKUP IS NOT AN EMPTY LIST. nightly-status.py substituted [] on an HTTP error, so a failed
    # API call produced a CLEAR verdict in the check every session runs first. None now means "unreadable".
    ("the candidate list could not be read -> unresolved, not clear", REDRUN, None, True),
    ("the tested head is unreadable -> unresolved, not assumed fixed",
     dict(REDRUN, tested_head=""), [SHA_B], True),
    ("a successful run is never unresolved", MERGED, [SHA_A], False),
    ("no run at all is not unresolved", None, [SHA_A], False),
):
    got, why = nd.failure_is_unresolved(run, heads)
    if got is not want:
        fails.append("%s: got %r, wanted %r (%s)" % (label, got, want, why))
    else:
        oks += 1
        print("  ok   %-64s [%s]" % (label, "UNRESOLVED" if got else "clear"))

# THE REASON IS NOT DECORATION -- IT IS WHAT THE NEXT SESSION ACTS ON, so it is asserted too.
# The boolean above was right for the zero-candidate case from the start, and the sentence was not: joining an
# empty head list produced "the delivery head has moved to no open candidate since", which is not a sentence.
# The run that merged this very delivery printed it. A test that checks only the verdict cannot see that.
_, why_none = nd.failure_is_unresolved(REDRUN, [])
if "moved to no open candidate" in why_none:
    fails.append("the zero-candidate reason is malformed: %r" % why_none)
elif "no eligible candidate is visible" not in why_none:
    fails.append("the zero-candidate reason does not state the observation: %r" % why_none)
else:
    oks += 1
    print("  ok   %-64s [%s]" % ("no candidate visible: the reason states the observation", "clear"))

# AND DOES NOT INVENT A HISTORY. Review finding on PR #182: the first fix replaced a malformed sentence with a
# confident false one ("that pull request has been merged or closed since"). Nothing here knows that -- the
# list is also empty for a draft, a held candidate, and (before the caller was fixed) a failed API call. The
# reason must offer the possibilities, never pick one.
if "may have been" not in why_none:
    fails.append("the zero-candidate reason asserts a history it cannot know: %r" % why_none)
else:
    oks += 1
    print("  ok   %-64s [%s]" % ("no candidate visible: the reason does not invent a history", "clear"))

_, why_unread = nd.failure_is_unresolved(REDRUN, None)
if "could not be read" not in why_unread:
    fails.append("an unreadable candidate list does not say so: %r" % why_unread)
else:
    oks += 1
    print("  ok   %-64s [%s]" % ("unreadable candidate list: says so, and fails closed", "UNRESOLVED"))

_, why_moved = nd.failure_is_unresolved(REDRUN, [SHA_B])
if SHA_B[:12] not in why_moved:
    fails.append("the superseded reason does not name the head that superseded it: %r" % why_moved)
else:
    oks += 1
    print("  ok   %-64s [%s]" % ("head moved on: the reason names the new head", "clear"))


print()
print("=" * 78)
if fails:
    for f in fails:
        print("  FAIL: %s" % f)
    print("NIGHTLY_DELIVERY_NEGATIVE = FAIL (%d)" % len(fails))
    sys.exit(1)
print("NIGHTLY_DELIVERY_NEGATIVE = PASS (%d assertions)" % oks)
