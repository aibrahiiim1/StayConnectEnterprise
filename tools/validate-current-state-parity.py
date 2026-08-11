#!/usr/bin/env python3
"""CURRENT-STATE SEMANTIC PARITY.

Every zero-stale check that existed before this one stopped at a keyword: it looked for a phrase and objected
to it. That is why a fully green Governance gate shipped a project-state file whose `current_activity` said
"INCREMENT-9 DURABILITY CORRECTION" while its `current_maturity`, three sentences later, said the project was
still "awaiting one separate Product-Owner decision to authorize Live Increment 9" — and recorded iam_v2 as
49/0 while another field in the same file recorded 63. Neither sentence contains a forbidden word. They are
only wrong *about each other*.

So this validator does not police vocabulary. It reads the machine-readable facts in
`governance/project-state.json` -> `current_state_facts`, and then asserts that no surface which presents
itself as CURRENT contradicts them. A fact recorded as data can be diffed against prose; a fact that exists
only as prose cannot.

HISTORY IS NOT A CONTRADICTION. A statement that was true when it was written stays in the record — this
project's whole evidence model depends on that. A hit is excused when its own paragraph marks it as historical
(HISTORICAL, SUPERSEDED, "as at", "as written", "was then", "no longer", "at the time"). What is refused is a
superseded claim presented as the current state, unlabelled.

Usage:  python tools/validate-current-state-parity.py [--json]
Exit:   0 = parity holds, 1 = a current surface contradicts the recorded facts, 2 = the facts are unusable.
"""
import io
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
STATE = os.path.join(ROOT, "governance", "project-state.json")

# Surfaces that speak in the present tense about the project. Packs and exports are generated from these and
# are checked by the pack validators; transition receipts are dated records and are deliberately excluded.
DOC_SURFACES = [
    "governance/project-state.json",
    "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
    "docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md",
    "docs/reports/StayConnect-IAM-Phase3-Final-Report.md",
    "docs/context/StayConnect-IAM-Handoff.md",
]

HISTORY_MARKERS = re.compile(
    r"historical|superseded|as at |as written|was then|no longer|at the time|"
    r"earlier (draft|version|revision)|previously|used to|before live increment|"
    r"an earlier|deliberately excluded|this document is preserved",
    re.I,
)


def paragraphs(text):
    """Yield (paragraph, offset). A paragraph is the unit of labelling: a historical marker excuses the
    statements around it, not the whole file."""
    off = 0
    for para in re.split(r"\n\s*\n", text):
        yield para, off
        off += len(para) + 2


def scan(text, pattern):
    """Yield paragraphs matching pattern that are NOT marked historical."""
    for para, _ in paragraphs(text):
        if pattern.search(para) and not HISTORY_MARKERS.search(para):
            yield " ".join(para.split())[:180]


def load_surface(rel):
    path = os.path.join(ROOT, rel)
    if not os.path.exists(path):
        return None
    return io.open(path, encoding="utf-8", errors="replace").read()


def main():
    as_json = "--json" in sys.argv
    try:
        state = json.load(io.open(STATE, encoding="utf-8"))
    except Exception as exc:  # noqa: BLE001
        print("PARITY = ERROR: project-state.json unreadable: %s" % exc)
        return 2

    facts = state.get("current_state_facts")
    if not isinstance(facts, dict) or not facts:
        print("PARITY = ERROR: governance/project-state.json has no current_state_facts block.")
        print("  Current state must be recorded as DATA, or prose can contradict prose and nothing notices.")
        return 2

    failures = []
    notes = []

    def bad(rule, detail, where):
        failures.append((rule, detail, where))

    def ok(rule):
        notes.append(rule)

    # ---- 1. internal coherence of the facts themselves ------------------------------------------------------
    # A facts block that contradicts itself would validate every surface against nonsense.
    if facts.get("live_increment9_executed"):
        for k in ("appliance_contact_occurred", "production_db_contact_occurred", "deployment_occurred"):
            if not facts.get(k):
                bad("facts-coherence", "live_increment9_executed is true but %s is false" % k, STATE)
    if facts.get("migration_0010_applied_production") and facts.get("iam_v2_tables") == 49:
        bad("facts-coherence", "migration 0010 recorded as applied while iam_v2_tables is still 49", STATE)
    if facts.get("accepted") or facts.get("closed"):
        bad("facts-coherence", "Phase 3 is recorded as accepted/closed; every other surface says IN_PROGRESS", STATE)
    if not failures:
        ok("the recorded facts are internally coherent")

    # ---- 2. current_activity vs an authorization that is supposedly still awaited ---------------------------
    activity = state.get("current_activity", "")
    if facts.get("current_activity") != activity:
        bad("activity-parity",
            "current_activity is %r but current_state_facts.current_activity is %r" % (activity, facts.get("current_activity")),
            STATE)
    if facts.get("live_increment9_executed"):
        awaiting = re.compile(
            r"await(?:s|ing)?[^.]{0,120}(?:authoriz|decision)[^.]{0,60}(?:live )?increment[- ]?9|"
            r"increment[- ]?9 authorization requested|"
            r"pre-live safety candidate|"
            r"authorization is still awaited",
            re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, awaiting):
                bad("increment9-already-executed",
                    "presents Increment-9 authorization as still awaited: %s" % hit, rel)
        if not [f for f in failures if f[0] == "increment9-already-executed"]:
            ok("no current surface still awaits the Increment-9 authorization that was already given and executed")

    # ---- 3. iam_v2 table count parity ------------------------------------------------------------------------
    tables = facts.get("iam_v2_tables")
    if isinstance(tables, int):
        schema_tables = (state.get("database_schema_state") or {}).get("iam_v2_tables")
        if schema_tables is not None and schema_tables != tables:
            bad("iamv2-count-parity",
                "database_schema_state.iam_v2_tables=%s but current_state_facts.iam_v2_tables=%s" % (schema_tables, tables),
                STATE)
        stale_count = re.compile(r"iam_v2\s*49\b|49\s*tables?\s*/\s*0|iam_v2\s*49\s*/\s*0", re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, stale_count):
                bad("iamv2-count-parity",
                    "states iam_v2 49 as current while the recorded count is %s: %s" % (tables, hit), rel)
        if not [f for f in failures if f[0] == "iamv2-count-parity"]:
            ok("no current surface contradicts the recorded iam_v2 table count (%s)" % tables)

    # ---- 4. migration 0010 deployment parity ------------------------------------------------------------------
    if facts.get("migration_0010_applied_production"):
        undeployed = re.compile(r"(migration\s*)?0010[^.\n]{0,40}\bundeployed\b|\bundeployed\b[^.\n]{0,40}0010", re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, undeployed):
                bad("migration-0010-parity", "calls migration 0010 undeployed: %s" % hit, rel)
        if not [f for f in failures if f[0] == "migration-0010-parity"]:
            ok("no current surface calls migration 0010 undeployed")

    # ---- 5. live-contact parity --------------------------------------------------------------------------------
    if facts.get("live_increment9_executed"):
        no_contact = re.compile(
            r"no appliance (access|contact)|no production[- ]?(db|database) (access|contact)|"
            r"no live[- ]?pms contact|no live evidence exists|contacted no appliance|"
            r"no appliance, production database or (live )?pms was contacted",
            re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, no_contact):
                bad("live-contact-parity",
                    "denies appliance/Production-DB/live-PMS contact that has already occurred: %s" % hit, rel)
        if not [f for f in failures if f[0] == "live-contact-parity"]:
            ok("no current surface denies the live contact that Increment 9 actually made")

    # ---- 6. Guest-Portal budget parity ---------------------------------------------------------------------------
    budget = facts.get("guest_portal_uniform_budget_ms")
    if isinstance(budget, int):
        old_budget = re.compile(r"\b1200\s*ms\b", re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, old_budget):
                bad("portal-budget-parity",
                    "presents 1200ms as current while the recorded budget is %sms: %s" % (budget, hit), rel)
        if not [f for f in failures if f[0] == "portal-budget-parity"]:
            ok("no current surface presents a superseded Guest-Portal budget (recorded: %sms)" % budget)

    # ---- 7. nft deployment architecture parity ---------------------------------------------------------------------
    if facts.get("surgical_foundation_retired_from_procedure"):
        requires_foundation = re.compile(
            r"(phase3-foundation\s+(install|rollback))|"
            r"(cutover becomes flag-only[^.]{0,80}install)|"
            r"(only after that install is performed)",
            re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for para, _ in paragraphs(text):
                if not requires_foundation.search(para):
                    continue
                if HISTORY_MARKERS.search(para):
                    continue
                # A paragraph that RETIRES the tool naturally names it; that is not a requirement to run it.
                if re.search(r"retired|diagnostic|do \*\*not\*\* run|do not run|never part of", para, re.I):
                    continue
                bad("nft-architecture-parity",
                    "still presents the surgical foundation as a required deployment/rollback step: %s"
                    % " ".join(para.split())[:180], rel)
        if not [f for f in failures if f[0] == "nft-architecture-parity"]:
            ok("no current surface requires the retired surgical-foundation step (architecture: %s)"
               % facts.get("nft_deployment_architecture"))

    # ---- report ----------------------------------------------------------------------------------------------------
    if as_json:
        print(json.dumps({
            "pass": len(notes), "fail": len(failures),
            "checks": [{"status": "PASS", "rule": n} for n in notes] +
                      [{"status": "FAIL", "rule": r, "detail": d, "where": w} for r, d, w in failures],
        }))
    else:
        for n in notes:
            print("  [PASS] %s" % n)
        for rule, detail, where in failures:
            print("  [FAIL] %s — %s" % (rule, detail))
            print("         in %s" % where)
        print("=" * 60)
        print("CURRENT_STATE_PARITY: pass=%d fail=%d -> %s"
              % (len(notes), len(failures), "PASS" if not failures else "FAIL"))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
