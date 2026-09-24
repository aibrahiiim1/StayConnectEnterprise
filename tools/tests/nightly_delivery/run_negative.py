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


def gate_run(wf, sha=SHA_A, corr=CORR, event="workflow_dispatch", status="completed",
             conclusion="success", rid=1):
    return {"workflow_file": wf, "head_sha": sha, "event": event, "status": status,
            "conclusion": conclusion, "id": rid,
            "display_title": "nightly %s %s" % (corr, sha[:12])}


def all_four(**kw):
    return [gate_run(wf, rid=i, **kw) for i, wf in enumerate(nd.REQUIRED_GATES, start=1)]


def pr(number=200, head=SHA_A, draft=False, base="master", labels=None, state="open",
       mergeable=True, mergeable_state="clean"):
    return {"number": number, "head_sha": head, "draft": draft, "base_ref": base,
            "head_ref": "delivery/x", "labels": labels or [], "state": state,
            "mergeable": mergeable, "mergeable_state": mergeable_state}


# =========================================================================================================
print("== 8. SCHEDULING AND TIMEZONE CORRECTNESS (one timezone-aware cron; verified, not computed) ==")
# The workflow now declares a single `- cron: '10 3 * * *'` with `timezone: "Africa/Cairo"`, so the PLATFORM
# resolves Egypt's DST -- UTC+2 in winter, UTC+3 from late April to late October. What is asserted here is the
# narrow thing that remains this repository's problem: that a run which did NOT start near 03:10 local is
# refused, because an ignored `timezone:` would fire at 03:10 UTC and merge quietly at the wrong hour forever.
WINTER = dt.datetime(2027, 1, 15, tzinfo=UTC)      # Cairo is UTC+2 -> 03:10 local == 01:10Z
SUMMER = dt.datetime(2027, 7, 15, tzinfo=UTC)      # Cairo is UTC+3 -> 03:10 local == 00:10Z

expect("winter: a run at 01:10Z is 03:10 Cairo and proceeds",
       nd.schedule_sanity(WINTER.replace(hour=1, minute=10)), True, "ON_SCHEDULE")
expect("summer: a run at 00:10Z is 03:10 Cairo and proceeds",
       nd.schedule_sanity(SUMMER.replace(hour=0, minute=10)), True, "ON_SCHEDULE")

# THE CASE THE CHECK EXISTS FOR: `timezone:` ignored, so the cron is read as UTC.
w_ignored = nd.schedule_sanity(WINTER.replace(hour=3, minute=10))
s_ignored = nd.schedule_sanity(SUMMER.replace(hour=3, minute=10))
expect("winter: an IGNORED timezone fires at 03:10Z = 05:10 Cairo and is refused",
       w_ignored, False, "SCHEDULE_DRIFT")
expect("summer: an IGNORED timezone fires at 03:10Z = 06:10 Cairo and is refused",
       s_ignored, False, "SCHEDULE_DRIFT")
if abs(w_ignored.detail["drift_minutes"] - 120.0) > 0.01:
    fails.append("winter drift measured %s, expected +120" % w_ignored.detail["drift_minutes"])
elif abs(s_ignored.detail["drift_minutes"] - 180.0) > 0.01:
    fails.append("summer drift measured %s, expected +180" % s_ignored.detail["drift_minutes"])
else:
    oks += 1
    print("  ok   the ignored-timezone drift really is +120 winter / +180 summer (measured, not assumed)")

# The offsets themselves are measured from the tz database rather than written down anywhere.
if abs(nd.schedule_sanity(WINTER.replace(hour=1, minute=10)).detail["utc_offset_hours"] - 2.0) > 0.01:
    fails.append("winter offset is not +2")
elif abs(nd.schedule_sanity(SUMMER.replace(hour=0, minute=10)).detail["utc_offset_hours"] - 3.0) > 0.01:
    fails.append("summer offset is not +3")
else:
    oks += 1
    print("  ok   Cairo really is UTC+2 in winter and UTC+3 in summer (read from the tz database)")

# A late scheduler must not be mistaken for a misconfiguration, and the threshold must sit between the two.
expect("a 40-minute-late scheduled start is tolerated",
       nd.schedule_sanity(WINTER.replace(hour=1, minute=50)), True, "ON_SCHEDULE")
expect("a 100-minute-late start is still tolerated (a late scheduler, not a wrong timezone)",
       nd.schedule_sanity(WINTER.replace(hour=2, minute=50)), True, "ON_SCHEDULE")
expect("a 120-minute drift is refused -- that is exactly the winter UTC misreading",
       nd.schedule_sanity(WINTER.replace(hour=3, minute=10)), False, "SCHEDULE_DRIFT")
expect("the middle of the working day is refused for a SCHEDULED run",
       nd.schedule_sanity(WINTER.replace(hour=12, minute=0)), False, "SCHEDULE_DRIFT")

# A MANUAL run is expected at any hour; the check is about the platform's timing, not about permission.
expect("a workflow_dispatch at midday is exempt, because that is what manual means",
       nd.schedule_sanity(WINTER.replace(hour=12, minute=0), event="workflow_dispatch"), True,
       "NOT_SCHEDULED")
expect("a workflow_dispatch on time is also fine",
       nd.schedule_sanity(WINTER.replace(hour=1, minute=10), event="workflow_dispatch"), True,
       "NOT_SCHEDULED")

# Every night of a leap year, in both offsets, a correctly-scheduled run proceeds and a UTC-read one does not.
good = bad_ = 0
for day in range(366):
    d = dt.datetime(2027, 1, 1, tzinfo=UTC) + dt.timedelta(days=day)
    local_target = d.astimezone(nd.ZoneInfo(nd.DELIVERY_TZ)).replace(hour=3, minute=10, second=0,
                                                                     microsecond=0)
    if nd.schedule_sanity(local_target.astimezone(UTC)).proceed:
        good += 1
    if not nd.schedule_sanity(d.replace(hour=3, minute=10)).proceed:
        bad_ += 1
if good != 366 or bad_ != 366:
    fails.append("across 366 nights: %d/366 correct starts accepted, %d/366 UTC-read starts refused"
                 % (good, bad_))
else:
    oks += 1
    print("  ok   366/366 nights: a correct 03:10-Cairo start proceeds and a UTC-read start is refused")

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
       nd.classify_gate_runs(SHA_A, CORR, all_four(sha=SHA_B)), False, "GATES_NOT_ALL_GREEN")
expect("no expected sha supplied at all",
       nd.classify_gate_runs("", CORR, all_four()), False, "NO_EXPECTED_SHA")
expect("a truncated sha is not an identity",
       nd.classify_gate_runs(SHA_A[:12], CORR, all_four(sha=SHA_A[:12])), False, "NO_EXPECTED_SHA")

print()
print("== EARLIER / REUSED EVIDENCE MUST NOT SUBSTITUTE FOR TONIGHT'S FRESH RUN ==")
expect("all four green but from a PREVIOUS night's dispatch",
       nd.classify_gate_runs(SHA_A, CORR, all_four(corr="nightly-20260901-oldoldold")),
       False, "GATES_NOT_ALL_GREEN")
expect("all four green but they were pull_request runs",
       nd.classify_gate_runs(SHA_A, CORR, all_four(event="pull_request")), False, "GATES_NOT_ALL_GREEN")
expect("all four green but they were master push runs",
       nd.classify_gate_runs(SHA_A, CORR, all_four(event="push")), False, "GATES_NOT_ALL_GREEN")
expect("no correlation id to distinguish nights",
       nd.classify_gate_runs(SHA_A, "", all_four()), False, "NO_CORRELATION_ID")
expect("two runs for one gate -- which verdict was meant is unknowable",
       nd.classify_gate_runs(SHA_A, CORR, all_four() + [gate_run(nd.REQUIRED_GATES[0], rid=99)]),
       False, "GATES_NOT_ALL_GREEN")

print()
print("== 5. PARTIAL GATE COMPLETION -- three of four is a refusal, not an opportunity ==")
for i, wf in enumerate(nd.REQUIRED_GATES):
    runs = [r for r in all_four() if r["workflow_file"] != wf]
    expect("only three gates reported (%s absent)" % wf.replace(".yml", ""),
           nd.classify_gate_runs(SHA_A, CORR, runs), False, "GATES_NOT_ALL_GREEN")
expect("one gate still in progress",
       nd.classify_gate_runs(SHA_A, CORR,
                             [gate_run(nd.REQUIRED_GATES[0], status="in_progress", conclusion=""),
                              gate_run(nd.REQUIRED_GATES[1], rid=2), gate_run(nd.REQUIRED_GATES[2], rid=3),
                              gate_run(nd.REQUIRED_GATES[3], rid=4)]),
       False, "GATES_NOT_ALL_GREEN")
expect("no runs at all (the dispatch produced nothing)",
       nd.classify_gate_runs(SHA_A, CORR, []), False, "GATES_NOT_ALL_GREEN")

print()
print("== 6. A FAILED GATE ==")
for concl in ("failure", "cancelled", "timed_out", "skipped", "neutral", "action_required", ""):
    runs = [gate_run(nd.REQUIRED_GATES[0], conclusion=concl, rid=1)] + \
           [gate_run(wf, rid=i) for i, wf in enumerate(nd.REQUIRED_GATES[1:], start=2)]
    expect("governance concluded %r" % (concl or "unreported"),
           nd.classify_gate_runs(SHA_A, CORR, runs), False, "GATES_NOT_ALL_GREEN")

print()
print("== THE POSITIVE PATH -- all four fresh, this sha, this night ==")
green = nd.classify_gate_runs(SHA_A, CORR, all_four())
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
    ("gates never ran", nd.classify_gate_runs(SHA_A, CORR, [])),
    ("a gate failed", nd.classify_gate_runs(
        SHA_A, CORR, [gate_run(nd.REQUIRED_GATES[0], conclusion="failure")] +
        [gate_run(wf, rid=i) for i, wf in enumerate(nd.REQUIRED_GATES[1:], start=2)])),
    ("evidence was from a previous night", nd.classify_gate_runs(SHA_A, CORR, all_four(corr="old"))),
    ("evidence was a pull_request run", nd.classify_gate_runs(SHA_A, CORR, all_four(event="pull_request"))),
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


print()
print("=" * 78)
if fails:
    for f in fails:
        print("  FAIL: %s" % f)
    print("NIGHTLY_DELIVERY_NEGATIVE = FAIL (%d)" % len(fails))
    sys.exit(1)
print("NIGHTLY_DELIVERY_NEGATIVE = PASS (%d assertions)" % oks)
