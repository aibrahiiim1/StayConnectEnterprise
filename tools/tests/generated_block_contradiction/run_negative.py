#!/usr/bin/env python3
"""NEGATIVE TESTS for check_appliance_facts_agree in tools/project-state.py.

The rule this file guards exists because of a specific green gate. `production_appliance` is the SOURCE of the
"Guest traffic:" clause in every generated current-state block, and it sat reading

    "NONE. No guest has authenticated: purchases=0, entitlements=0, sessions=0, and the first real Room Login
     has not been performed."

while the appliance held two real Room Logins, two purchases, two entitlements, two sessions and one session
enforced in the kernel. Eight documents and three packs said it. Governance was green, because the only rule
in that direction caught a summary claiming MORE than the facts, never one claiming LESS.

Prose cannot be checked against prose, so the register now carries the NUMBERS
(current_state_facts.live_counters) and the rules compare sentences to them. A rule that has never been
watched to fail is indistinguishable from `return []`, so each case below reintroduces a contradiction that
actually shipped — or a near neighbour of one — and must be caught, by the rule meant to catch it.

The last two cases are the other half of the contract: a correct repository must pass, and history must still
be allowed to be history.

Usage:  python tools/tests/generated_block_contradiction/run_negative.py
"""
import copy
import importlib.util
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))


def load_checker():
    """Import the rule under test directly.

    Running the CLI would also run the rendered-block drift check, and a mutated register drifts from its
    rendered output by construction — every case would then 'fail' for a second reason and prove nothing about
    the rule being tested.
    """
    path = os.path.join(ROOT, "tools", "project-state.py")
    spec = importlib.util.spec_from_file_location("project_state_under_test", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod.check_appliance_facts_agree


def good_state():
    with open(os.path.join(ROOT, "governance", "project-state.json"), encoding="utf-8") as fh:
        return json.load(fh)


CASES = []


def case(name, expect, why):
    def register(fn):
        CASES.append((name, fn, expect, why))
        return fn
    return register


@case("counters denied outright", "purchases=0", "the exact sentence that shipped in eight documents")
def _counters_denied(st):
    st["production_appliance"]["guest_traffic"] = (
        "NONE. No guest has authenticated: purchases=0, entitlements=0, sessions=0, and the first real Room "
        "Login has not been performed.")
    return st


@case("Room Login denied", "Room Login was performed", "a summary that denies an event the register records")
def _login_denied(st):
    st["production_appliance"]["guest_traffic"] = (
        "NONE. no guest has authenticated on this appliance.")
    return st


@case("sessions=0 in the proven-note", "guest_signin_end_to_end_proven_note",
      "the same zero, in the field an operator reads when asking whether sign-in works")
def _note_denied(st):
    st["current_state_facts"]["guest_signin_end_to_end_proven_note"] = (
        "NO end-to-end guest sign-in has been proven. purchases=0, entitlements=0, sessions=0.")
    return st


@case("no counters at all", "live_counters is missing",
      "without numbers the other rules are unenforceable, which is the state the drift happened in")
def _no_counters(st):
    st["current_state_facts"].pop("live_counters", None)
    return st


@case("traffic claimed without accounting", "guest_traffic_carried_ever",
      "enforcement being live is not the same fact as traffic having flowed")
def _traffic_claimed(st):
    st["current_state_facts"]["guest_traffic_carried_ever"] = True
    st["current_state_facts"]["live_counters"]["accounting_records"] = 0
    return st


@case("financial zero denied", "payment_transactions",
      "a financial counter that moved must not be reported as zero either")
def _financial_denied(st):
    st["current_state_facts"]["live_counters"]["payment_transactions"] = 3
    st["production_appliance"]["payment_or_financial_traffic"] = "NONE. payment_transactions=0 at all times."
    return st


def main():
    check = load_checker()
    base = good_state()
    failures = 0

    # The repository as it stands must pass. A negative suite whose baseline is already failing proves only
    # that everything fails.
    found = check(copy.deepcopy(base))
    if found:
        print("  *** FAIL: the CURRENT repository already contradicts itself:")
        for line in found:
            print("      " + line)
        failures += 1
    else:
        print("  ok: the current repository is self-consistent")

    for name, mutate, expect, why in CASES:
        found = check(mutate(copy.deepcopy(base)))
        if not found:
            print(f"  *** FAIL: {name} was NOT caught — {why}")
            failures += 1
        elif not any(expect.lower() in line.lower() for line in found):
            print(f"  *** FAIL: {name} was caught by the WRONG rule (wanted {expect!r}):")
            for line in found:
                print("      " + line)
            failures += 1
        else:
            print(f"  ok: {name} is refused")

    # HISTORY IS NOT DRIFT. A transition receipt recording that the counters were once zero is a true statement
    # about a past instant, and a rule that could not tell the two apart would make the register unwritable.
    historical = copy.deepcopy(base)
    historical.setdefault("_negative_test_history", {})["t0098"] = (
        "purchases=0, entitlements=0, sessions=0 at the time of the materialization deployment")
    if check(historical):
        print("  *** FAIL: a historical statement elsewhere in the register was treated as a current claim")
        failures += 1
    else:
        print("  ok: history outside the appliance summary is left alone")

    if failures:
        print(f"GENERATED_BLOCK_CONTRADICTION_NEGATIVE = FAIL ({failures})")
        return 1
    print("GENERATED_BLOCK_CONTRADICTION_NEGATIVE = PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
