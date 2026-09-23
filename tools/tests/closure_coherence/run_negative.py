#!/usr/bin/env python3
"""CLOSURE COHERENCE, BOTH DIRECTIONS.

The mutation suite proves a rule can FAIL. It cannot prove a rule stays QUIET when it should, because every
case it runs is expected to be caught -- and two of the three defects found by review on PR #179 were of
exactly that second kind:

  * the rule was CONDITIONAL on two sentinel fields that nothing else required, so deleting both switched the
    whole safeguard off and the state still reported PASS; and
  * the rule banned EVERY in-progress activity, so any future Product-Owner-authorised work, under a new
    decision and unrelated to the closed mission, would have failed validation for existing.

The first is a false negative, the second a false positive, and a suite that only asserts detection would
have shipped both. So this runner asserts the rule's shape in both directions, against the real function and
the real repository state.

Run:  python tools/tests/closure_coherence/run_negative.py
Exit: 0 all assertions hold, 1 otherwise.
"""
import copy
import importlib.util
import io
import json
import os
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
STATE = os.path.join(ROOT, "governance", "project-state.json")
RECON = os.path.join(ROOT, "scripts", "clean-install-reconstruction.sh")

_spec = importlib.util.spec_from_file_location("ps", os.path.join(ROOT, "tools", "project-state.py"))
ps = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ps)

fails = []
oks = 0


def check(label, st, want, needle=None):
    """want: 'quiet' or 'caught'. needle: a substring the finding must carry when caught."""
    global oks
    found = ps.check_closure_coherence(st)
    if want == "quiet":
        if found:
            fails.append("%s: expected NO finding, got %d -- %s" % (label, len(found), found[0][:160]))
            return
    else:
        if not found:
            fails.append("%s: expected a finding, got none" % label)
            return
        if needle and not any(needle in f for f in found):
            fails.append("%s: caught, but no finding mentions %r -- %s" % (label, needle, found[0][:160]))
            return
    oks += 1
    print("  ok   %s" % label)


base = json.load(io.open(STATE, encoding="utf-8"))

print("== the reconciled state is coherent (no false positives on the real register) ==")
check("the committed state produces no closure finding", copy.deepcopy(base), "quiet")

print()
print("== the condition cannot be deleted (this failed open once) ==")
st = copy.deepcopy(base)
st["current_state_facts"].pop("functional_completeness_verdict", None)
st["current_state_facts"].pop("functional_completeness_mission_status", None)
check("deleting both closure sentinels is refused", st, "caught",
      "FUNCTIONAL_COMPLETENESS_MISSION_CLOSURE")

st = copy.deepcopy(base)
st["current_state_facts"]["functional_completeness_mission_status"] = "IN_PROGRESS"
check("a sentinel flipped back to unfinished is refused", st, "caught", "records the mission CLOSED")

print()
print("== a retained authorisation for the CLOSED mission is refused ==")
for field, value in (
    ("next_authorized_action",
     "Execute to DONE the Product-Owner-authorised FUNCTIONAL-COMPLETENESS CLOSURE that D41 asks for, "
     "including the controlled PRE-LIVE work on 172.21.60.25 the mission names."),
):
    st = copy.deepcopy(base)
    st[field] = value
    check("%s authorising closure execution is refused" % field, st, "caught", field)

st = copy.deepcopy(base)
st["blockers"] = ["THE CURRENT WORK IS THE FUNCTIONAL-COMPLETENESS CLOSURE D41 ASKS FOR. What remains is "
                  "execution."]
check("blockers presenting the closure as current work is refused", st, "caught", "blockers[0]")

st = copy.deepcopy(base)
st["allowed_actions"] = ["Execute the authorised functional-completeness closure to DONE, including "
                        "controlled work on PRE-LIVE 172.21.60.25."]
check("allowed_actions authorising controlled PRE-LIVE work is refused", st, "caught", "allowed_actions[0]")

print()
print("== an unclosed required gap cannot be named while closure is declared ==")
st = copy.deepcopy(base)
st["current_state_facts"]["functional_completeness_remaining"]["increment_3"]["known_gap_not_closed"] = (
    "scripts/pmsd-pg-integration.sh applies a curated migration list ending at 0079.")
check("a key named *_not_closed is refused", st, "caught", "UNCLOSED required gap")

st = copy.deepcopy(base)
st["current_state_facts"]["functional_completeness_remaining"]["development_gaps"]["a_new_one"] = (
    "Still outstanding: the thing nobody did.")
check("a gap entry not opening as closed is refused", st, "caught", "development_gaps")

print()
print("== BUT THE REGISTER MUST STILL BE ABLE TO STATE FUTURE AUTHORISED WORK ==")
st = copy.deepcopy(base)
st["authorized_activities"] = [{
    "name": "HA architecture evaluation on a provided lab appliance",
    "authorization": "D42/T0190",
    "status": "AUTHORIZED_IN_PROGRESS",
    "scope": "a lab appliance the Product Owner provides; not PRE-LIVE",
}]
check("a NEW activity under a NEW decision is allowed", st, "quiet")

st = copy.deepcopy(base)
st["next_authorized_action"] = (
    "Execute the Product-Owner-authorised HA architecture evaluation on the provided lab appliance to DONE.")
check("a next action for unrelated authorised work is allowed", st, "quiet")

st = copy.deepcopy(base)
st["blockers"] = ["No blocker. The functional-completeness closure has COMPLETED and is closed at T0181; "
                  "what remains is Product-Owner decisions."]
check("blockers DESCRIBING the completed closure is allowed", st, "quiet")

print()
print("== and the two kinds of in-progress entry this rule is about are still refused ==")
st = copy.deepcopy(base)
st["authorized_activities"] = [{
    "name": "post-roadmap DEVELOPMENT appliance IAM-v2 operational trial",
    "authorization": "D29/T0066",
    "status": "AUTHORIZED_IN_PROGRESS",
    "scope": "DEVELOPMENT appliance 172.21.60.23 only.",
}]
check("an in-progress activity against a RETIRED target is refused", st, "caught", "RETIRED")

st = copy.deepcopy(base)
st["authorized_activities"] = [{
    "name": "functional-completeness closure execution",
    "authorization": "T0175",
    "status": "AUTHORIZED_IN_PROGRESS",
    "scope": "the PRE-LIVE work the mission names",
}]
check("an in-progress activity tied to the CLOSED mission is refused", st, "caught",
      "functional-completeness mission")

print()
print("== the coverage the non-blocking classification leans on is checked, not trusted ==")
orig = io.open(RECON, encoding="utf-8", newline="").read()
gutted = orig
for tail in ("migration $n", "base step $n"):
    gutted = gutted.replace(
        '''  [ "$(psql_q -c "SELECT count(*) FROM schema_migrations WHERE version='$n'")" = "1" ] || {
    bad "%s is not recorded in schema_migrations"; unrecorded=$((unrecorded+1)); }''' % tail,
        '''  : # assertion removed''')
if "SELECT count(*) FROM schema_migrations WHERE version=" in gutted:
    fails.append("fixture drift: the ledger assertion could not be gutted; update this runner")
elif "== ledger completeness ==" not in gutted:
    fails.append("fixture drift: the ledger HEADING should survive the gutting, or the test proves nothing")
else:
    io.open(RECON, "w", encoding="utf-8", newline="").write(gutted)
    try:
        check("gutting the ledger loop while keeping its heading is refused",
              copy.deepcopy(base), "caught", "QUERIES schema_migrations")
    finally:
        io.open(RECON, "w", encoding="utf-8", newline="").write(orig)
    if io.open(RECON, encoding="utf-8", newline="").read() != orig:
        fails.append("the runner did not restore clean-install-reconstruction.sh byte-exact")
    else:
        print("  ok   clean-install-reconstruction.sh restored byte-exact")
        oks += 1

print()
print("=" * 60)
if fails:
    for f in fails:
        print("  FAIL: %s" % f)
    print("CLOSURE_COHERENCE_NEGATIVE = FAIL (%d)" % len(fails))
    sys.exit(1)
print("CLOSURE_COHERENCE_NEGATIVE = PASS (%d assertions)" % oks)
