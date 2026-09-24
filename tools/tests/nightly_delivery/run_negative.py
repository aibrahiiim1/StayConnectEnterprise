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
print("== 8. SCHEDULING AND TIMEZONE CORRECTNESS (Africa/Cairo, not a frozen UTC offset) ==")
# Egypt: UTC+2 in winter, UTC+3 under DST. The workflow fires at 00:10Z and 01:10Z; exactly one firing per
# night must be admitted, in BOTH halves of the year, with no offset hard-coded anywhere.
WINTER = dt.datetime(2027, 1, 15, tzinfo=UTC)      # UTC+2 -> 03:10 local == 01:10Z
SUMMER = dt.datetime(2027, 7, 15, tzinfo=UTC)      # UTC+3 -> 03:10 local == 00:10Z

w0010 = nd.schedule_window(WINTER.replace(hour=0, minute=10))
w0110 = nd.schedule_window(WINTER.replace(hour=1, minute=10))
expect("winter: the 00:10Z firing is an hour early and skips", w0010, False, "TOO_EARLY")
expect("winter: the 01:10Z firing is 03:10 Cairo and runs", w0110, True, "IN_WINDOW")

s0010 = nd.schedule_window(SUMMER.replace(hour=0, minute=10))
s0110 = nd.schedule_window(SUMMER.replace(hour=1, minute=10))
expect("summer: the 00:10Z firing is 03:10 Cairo and runs", s0010, True, "IN_WINDOW")
expect("summer: the 01:10Z firing is an hour late and skips", s0110, False, "TOO_LATE")

if abs(w0110.detail["utc_offset_hours"] - 2.0) > 0.01:
    fails.append("winter offset measured as %s, expected +2" % w0110.detail["utc_offset_hours"])
else:
    oks += 1
    print("  ok   winter really is UTC+2 and summer really is UTC+3 (measured, not assumed)")
if abs(s0010.detail["utc_offset_hours"] - 3.0) > 0.01:
    fails.append("summer offset measured as %s, expected +3" % s0010.detail["utc_offset_hours"])

# Exactly one firing admitted per night, checked across a whole year including both DST transitions.
admitted = {}
for day in range(366):
    d = dt.datetime(2027, 1, 1, tzinfo=UTC) + dt.timedelta(days=day)
    n = sum(1 for hh in (0, 1) if nd.schedule_window(d.replace(hour=hh, minute=10)).proceed)
    admitted.setdefault(n, 0)
    admitted[n] += 1
if set(admitted) != {1}:
    fails.append("across 366 nights the number of admitted firings was %r, must always be exactly 1"
                 % admitted)
else:
    oks += 1
    print("  ok   exactly ONE firing admitted on each of 366 nights, across both DST transitions")

# A late start is tolerated; a very late one is not, because the next firing must not also qualify.
expect("a 40-minute-late start is still admitted",
       nd.schedule_window(WINTER.replace(hour=1, minute=50)), True, "IN_WINDOW")
expect("a 70-minute-late start is refused (the other firing must not qualify too)",
       nd.schedule_window(WINTER.replace(hour=2, minute=20)), False, "TOO_LATE")
expect("the middle of the working day is refused",
       nd.schedule_window(WINTER.replace(hour=12, minute=0)), False, "TOO_")

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
print("== THE SESSION-START CHECK MUST NOT BE FOOLED BY THE NO-OP FIRING (review P1 on PR #180) ==")
# The workflow fires twice and one firing exits 0 having done nothing. Under EEST the no-op is the LATER of
# the two, so runs[0] is the no-op -- and a session-start check reading runs[0] would report CLEAR while the
# night's real validation failed. The tool whose purpose is to notice a failure would be the thing hiding it.
NOOP = {"id": 2, "conclusion": "success", "verdict": "OUTSIDE_SCHEDULE_WINDOW", "tested_head": ""}
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


pick("the newest run is a no-op; the red run beneath it is authoritative", [NOOP, REDRUN], 1)
pick("two no-ops in a row are both skipped", [NOOP, dict(NOOP, id=3), MERGED], 1)
pick("a genuine newest run is selected normally", [MERGED, NOOP], 1)
pick("an UNREADABLE verdict counts as authoritative, never as skippable",
     [{"id": 9, "conclusion": "failure", "verdict": "", "tested_head": SHA_A}, MERGED], 9)
if nd.select_authoritative_run([NOOP, dict(NOOP, id=3)]) is not None:
    fails.append("all-no-op history should select nothing")
else:
    oks += 1
    print("  ok   a history of nothing but no-ops selects no authoritative run")

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
