#!/usr/bin/env python3
"""A GENERATED CURRENT-STATE BLOCK MUST NOT CONTRADICT THE CANONICAL PMS STATE, OR PRESENT A DATED SNAPSHOT
AS TODAY'S TOTALS.

Both escaped every existing gate at once. governance/project-state.json recorded, correctly, that the PMS is
INTENTIONALLY STOPPED by the Product Owner and reads DISCONNECTED / RESYNC_REQUIRED / DIAL_FAILED -- while the
first thing a reader opens, 00-START-HERE.md, still said the feed was "live and healthy", CONNECTED / IN_SYNC /
CONTINUOUS at generation 195. The same block presented a 2026-09-06 counter snapshot (purchases=5,
entitlements=5, sessions=5, accounting_records=1808) as the current totals, after D40/T0120 had already
recorded two further accepted grants.

PROJECT_STATE_GOVERNANCE, ZERO_STALE_LEFTOVERS and CURRENT_STATE_PARITY all passed. They compared documents
against each other and against fields, but nothing asserted that the RENDERED CURRENT PROSE agrees with the
canonical PMS transport state, or that a snapshot carries its date when later evidence has superseded it.

Scope is deliberately narrow: this is the exact class that escaped, not a new framework. Historical evidence
stays allowed -- the feed really was connected on 2026-09-05, and deleting that would be its own defect -- but
only when the surrounding sentence labels it HISTORICAL.
"""
import json
import os
import re
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
STATE = os.path.join(ROOT, "governance", "project-state.json")

# The rendered current-state surfaces a reader actually opens first.
BLOCK_DOCS = [
    os.path.join(ROOT, "exports", "chatgpt", "stayconnectenterprise", "00-START-HERE.md"),
    os.path.join(ROOT, "docs", "context", "StayConnect-IAM-Handoff.md"),
]

# Phrases that assert a CURRENTLY healthy feed. Matched only outside a HISTORICAL label.
LIVE_CLAIMS = [
    r"live and healthy",
    r"\bconnected\s*/\s*in[_ ]sync\s*/\s*continuous\b",
    r"feed is (?:currently )?(?:live|connected|healthy)",
    r"pms(?:[^.]{0,40})is (?:live|connected)\b",
]
STOPPED_MARKERS = [r"intentionally stopped", r"\bdisconnected\b", r"resync[_ ]required", r"dial[_ ]failed"]
HISTORICAL_MARKERS = [r"historical", r"do not read as current", r"superseded", r"snapshot"]

fails = []
notes = []


def sentences(text):
    """Split on sentence-ish boundaries and on the ';' the generated block uses between facts."""
    return [s.strip() for s in re.split(r"(?<=[.!?])\s+|;\s*", text) if s.strip()]


def canonical_pms_is_stopped(state):
    blob = " ".join(str(v) for v in [
        state["current_state_facts"].get("pms_feed_connected_note", ""),
        state.get("production_appliance", {}).get("pms_traffic", ""),
        state.get("pms_financial_state", ""),
    ]).lower()
    return any(re.search(m, blob) for m in STOPPED_MARKERS)


def check_pms(state):
    stopped = canonical_pms_is_stopped(state)
    notes.append(f"canonical PMS state reads as {'STOPPED/DISCONNECTED' if stopped else 'not stopped'}")
    if not stopped:
        # Nothing to enforce: the canonical state does not claim the feed is down.
        return
    for doc in BLOCK_DOCS:
        if not os.path.isfile(doc):
            fails.append(f"{os.path.relpath(doc, ROOT)}: missing, so the rendered claim cannot be checked")
            continue
        text = open(doc, encoding="utf-8").read()
        for s in sentences(text):
            low = s.lower()
            hit = next((c for c in LIVE_CLAIMS if re.search(c, low)), None)
            if not hit:
                continue
            # A labelled historical statement is legitimate and must stay legitimate.
            if any(re.search(h, low) for h in HISTORICAL_MARKERS):
                continue
            fails.append(
                f"{os.path.relpath(doc, ROOT)}: canonical PMS state is STOPPED but a current sentence claims "
                f"a live feed ({hit!r}): {s[:150]!r}")


def check_counters(state):
    lc = state["current_state_facts"].get("live_counters", {})
    as_at = str(lc.get("as_at", ""))
    note = str(lc.get("_note", ""))
    superseded = bool(lc.get("superseded_by")) or "supersed" in note.lower()
    notes.append(f"live_counters superseded={superseded}")
    if not superseded:
        return
    # Once superseded, the snapshot may still be published -- but never undated and never as "in total".
    if not re.search(r"historical|snapshot|superseded", as_at, re.I):
        fails.append("live_counters.as_at does not mark the superseded snapshot as historical: " + as_at[:120])
    for doc in BLOCK_DOCS:
        if not os.path.isfile(doc):
            continue
        text = open(doc, encoding="utf-8").read()
        for s in sentences(text):
            low = s.lower()
            if not re.search(r"accounting_records\s*=\s*\d|purchases\s*=\s*\d|sessions\s*=\s*\d", low):
                continue
            if any(re.search(h, low) for h in HISTORICAL_MARKERS):
                continue
            fails.append(
                f"{os.path.relpath(doc, ROOT)}: a superseded counter snapshot is stated without a historical "
                f"label: {s[:150]!r}")


def check_historical_still_allowed(state):
    """The opposite failure: scrubbing real evidence. The connected period must remain recorded somewhere."""
    blob = json.dumps(state).lower()
    if "2026-09-05" not in blob or "generation 195" not in blob:
        fails.append("the historical connected-feed evidence (2026-09-05 / generation 195) has been deleted; "
                     "it must be retained, labelled HISTORICAL")
    else:
        notes.append("historical connected-feed evidence is retained")


def main():
    state = json.load(open(STATE, encoding="utf-8"))
    check_pms(state)
    check_counters(state)
    check_historical_still_allowed(state)
    for n in notes:
        print(f"  note: {n}")
    for f in fails:
        print(f"  FAIL: {f}")
    print("=" * 60)
    print("PMS_AND_COUNTER_STALENESS =", "PASS" if not fails else f"FAIL ({len(fails)})")
    return 0 if not fails else 1


if __name__ == "__main__":
    sys.exit(main())
