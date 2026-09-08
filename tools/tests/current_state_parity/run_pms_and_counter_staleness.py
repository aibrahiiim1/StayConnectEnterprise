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
# Phrases that assert the feed is CURRENTLY down. The mirror image of LIVE_CLAIMS, and just as stale-able:
# when Protel was restored and pmsd restarted, "intentionally stopped" became the wrong tense everywhere it
# still appeared unlabelled.
STOPPED_CLAIMS = [
    r"intentionally stopped",
    r"pms(?:[^.]{0,40})is (?:stopped|down|disconnected)\b",
    r"feed is (?:currently )?(?:stopped|down|disconnected)",
    r"\bdisconnected\s*/\s*resync[_ ]required\b",
]
HISTORICAL_MARKERS = [r"historical", r"do not read as current", r"superseded", r"snapshot"]

fails = []
notes = []


def sentences(text):
    """Split on sentence-ish boundaries and on the ';' the generated block uses between facts."""
    return [s.strip() for s in re.split(r"(?<=[.!?])\s+|;\s*", text) if s.strip()]


def canonical_pms_is_stopped(state):
    """Read the CANONICAL FIELDS, not the prose.

    The first version of this grepped the notes for words like "disconnected" and "intentionally stopped".
    That worked while the feed was down and inverted the moment it came back: the notes still CONTAIN those
    words, correctly, inside the HISTORICAL clause that records the stopped period. So the check declared the
    canonical state STOPPED and then failed the very sentence that truthfully says the feed is connected.

    A staleness check that reads prose to decide what the prose should say is circular. project-state.json
    already carries the machine answer -- pms_feed_connected -- so that is what decides, and the prose is only
    ever the thing being checked.
    """
    csf = state.get("current_state_facts", {})
    if "pms_feed_connected" in csf:
        return not bool(csf["pms_feed_connected"])
    # No canonical field: fall back to prose, but only the part BEFORE any historical label, so a retained
    # history cannot be mistaken for the present.
    blob = " ".join(str(v) for v in [
        csf.get("pms_feed_connected_note", ""),
        state.get("production_appliance", {}).get("pms_traffic", ""),
    ]).lower()
    blob = re.split(r"historical \(do not read as current\)|historical:", blob)[0]
    return any(re.search(m, blob) for m in STOPPED_MARKERS)


def check_pms(state):
    stopped = canonical_pms_is_stopped(state)
    notes.append(f"canonical PMS state reads as {'STOPPED/DISCONNECTED' if stopped else 'not stopped'}")
    # SYMMETRIC. Staleness has two directions and only one of them was ever checked. When the feed was
    # stopped, the blocks still said "live and healthy"; when Protel was restored and pmsd restarted, the same
    # blocks would have gone on saying "intentionally stopped" just as wrongly. Whichever way the canonical
    # state points, an unlabelled sentence pointing the other way is stale.
    claims = STOPPED_CLAIMS if not stopped else LIVE_CLAIMS
    wrong = "STOPPED" if not stopped else "CONNECTED"
    for doc in BLOCK_DOCS:
        if not os.path.isfile(doc):
            fails.append(f"{os.path.relpath(doc, ROOT)}: missing, so the rendered claim cannot be checked")
            continue
        text = open(doc, encoding="utf-8").read()
        for s in sentences(text):
            low = s.lower()
            hit = next((c for c in claims if re.search(c, low)), None)
            if not hit:
                continue
            # A labelled historical statement is legitimate and must stay legitimate.
            if any(re.search(h, low) for h in HISTORICAL_MARKERS):
                continue
            fails.append(
                f"{os.path.relpath(doc, ROOT)}: canonical PMS state is "
                f"{'STOPPED' if stopped else 'CONNECTED'} but a current sentence claims the feed is {wrong} "
                f"({hit!r}): {s[:150]!r}")


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
