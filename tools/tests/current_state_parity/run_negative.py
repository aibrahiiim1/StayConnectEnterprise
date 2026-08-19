#!/usr/bin/env python3
"""NEGATIVE TESTS for tools/validate-current-state-parity.py.

A validator that has only ever seen a passing repository is indistinguishable from one that returns PASS
unconditionally. Every case below is a contradiction that ACTUALLY SHIPPED through a green Governance gate, or
a near neighbour of one, reintroduced into a disposable copy of the repository. Each must be caught, and each
must be caught by the rule that is supposed to catch it — a case that fails for the wrong reason is reported as
a failure here, because it would leave the real contradiction unguarded.

The last two cases are the other half of the contract: history must still be allowed to be history, and a
repository that records no facts at all must not quietly pass.

Usage:  python tools/tests/current_state_parity/run_negative.py
"""
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
VALIDATOR = os.path.join("tools", "validate-current-state-parity.py")

COPY = [
    "tools/validate-current-state-parity.py",
    "governance/project-state.json",
    "governance/decision-register.json",
    "docs/evidence/Phase3-Final-Live-Acceptance-Record.md",
    "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
    "docs/architecture/StayConnect-IAM-Phase4-Plan.md",
    "docs/architecture/StayConnect-IAM-Phase1A-Plan.md",
    "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
    "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
    "exports/chatgpt/stayconnectenterprise/PROJECT-INSTRUCTIONS.md",
    "docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md",
    "docs/reports/StayConnect-IAM-Phase3-Final-Report.md",
    "docs/context/StayConnect-IAM-Handoff.md",
]


# Every transition receipt, not a hand-listed subset: a new receipt must not silently drop out of the
# sandbox the evidence-reference rule reads.
COPY += sorted(
    "governance/transitions/" + n
    for n in os.listdir(os.path.join(ROOT, "governance", "transitions"))
    if n.endswith(".json")
)


def sandbox():
    d = tempfile.mkdtemp(prefix="parity-")

    # Every file COPY names, PLUS every file the copied transition receipts cite as evidence. Without the
    # second half the evidence-reference rule reads a sandbox where cited files are simply absent, so the
    # baseline fails the moment a new receipt cites a file nobody remembered to add to COPY — which is a
    # fixture defect reported as a repository defect. Derived, not enumerated, so it cannot drift again.
    rels = list(COPY)
    for rel in COPY:
        if not (rel.startswith("governance/transitions/") and rel.endswith(".json")):
            continue
        src = os.path.join(ROOT, rel)
        if not os.path.exists(src):
            continue
        try:
            doc = json.load(io.open(src, encoding="utf-8"))
        except Exception:  # noqa: BLE001
            continue
        for ev in doc.get("evidence_files") or []:
            ev = str(ev).strip()
            if not ev or ev.endswith("/"):
                continue
            # A receipt may cite a migration's two halves compactly as `...{up,down}.sql`. The validator
            # expands that; so must the sandbox, or the baseline fails because the fixture copied a
            # filename that never existed -- a fixture defect reported as a repository defect.
            a, b = ev.find("{"), ev.find("}")
            names = ([ev[:a] + part + ev[b + 1:] for part in ev[a + 1:b].split(",")]
                     if 0 <= a < b else [ev])
            for one in names:
                if one not in rels:
                    rels.append(one)

    # ...and the plan the state file currently points at. Same lesson as the evidence files above, learned
    # again on a different field: enumerating it in COPY means every new phase breaks the baseline until
    # somebody remembers, and the failure reads as a repository defect rather than a fixture one. Derived.
    try:
        st = json.load(io.open(os.path.join(ROOT, "governance", "project-state.json"), encoding="utf-8"))
        for key in ("current_phase_plan",):
            v = str(st.get(key) or "").strip()
            if v and v not in rels:
                rels.append(v)
    except Exception:  # noqa: BLE001
        pass

    for rel in rels:
        src = os.path.join(ROOT, rel)
        if not os.path.exists(src):
            continue
        dst = os.path.join(d, rel)
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        shutil.copy2(src, dst)
    return d


def run(d):
    r = subprocess.run([sys.executable, os.path.join(d, VALIDATOR), "--json"],
                       capture_output=True, text=True, cwd=d)
    try:
        return r.returncode, json.loads(r.stdout.strip().splitlines()[-1])
    except Exception:
        return r.returncode, {"pass": 0, "fail": 0, "checks": [], "raw": (r.stdout + r.stderr)[:300]}


def patch_facts(d, key, value):
    """Mutate current_state_facts specifically. Textual replacement is not safe here: several keys (notably
    iam_v2_tables) appear in more than one block, and hitting the wrong one tests a different rule than the
    one the case is named for."""
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    if doc["current_state_facts"].get(key) == value:
        raise AssertionError("mutation changed nothing: %s is already %r" % (key, value))
    doc["current_state_facts"][key] = value
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


def patch_state(d, fn):
    """Apply a textual mutation, and REFUSE to be a no-op.

    A case whose anchor has drifted mutates nothing, the validator legitimately passes, and the case reports
    itself as a pass — the exact silent-disable this suite exists to prevent. So a mutation that changes
    nothing is a hard error."""
    p = os.path.join(d, "governance/project-state.json")
    s = io.open(p, encoding="utf-8", newline="").read()
    out = fn(s)
    if out == s:
        raise AssertionError("mutation changed nothing: the fixture anchor has drifted")
    io.open(p, "w", encoding="utf-8", newline="").write(out)


def append_doc(d, rel, text):
    p = os.path.join(d, rel)
    with io.open(p, "a", encoding="utf-8", newline="") as f:
        f.write("\n\n" + text + "\n")


CASES = []


def case(name, rule):
    def deco(fn):
        CASES.append((name, rule, fn))
        return fn
    return deco


# ---- the seven contradictions the Product Owner enumerated -------------------------------------------------

@case("current_activity says Increment-9 correction while another current field awaits that authorization",
      "increment9-already-executed")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
               "## Status\n\nPhase 3 is a PRE-LIVE SAFETY CANDIDATE, awaiting one separate Product-Owner "
               "decision to authorize Live Increment 9.")


@case("current state says 63 tables while another current field says 49", "iamv2-count-parity")
def _(d):
    append_doc(d, "docs/context/StayConnect-IAM-Handoff.md",
               "Current live-dark schema state: iam_v2 49 tables / 0 rows; legacy auth is authoritative.")


@case("migration 0010 recorded as applied while a current restrictions block says undeployed",
      "migration-0010-parity")
def _(d):
    append_doc(d, "docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md",
               "## Restrictions in force\n\nAll Phase-3 flags OFF; PR open and unmerged; "
               "Migration 0010 undeployed; zero runtime privileges.")


@case("Increment-9 live evidence exists while a current block denies appliance/live-PMS contact",
      "live-contact-parity")
def _(d):
    append_doc(d, "docs/reports/StayConnect-IAM-Phase3-Final-Report.md",
               "## Production impact\n\nZero. No appliance, production database or PMS was contacted at any "
               "point, and no live PMS contact has occurred.")


@case("a current section presents the superseded 1200ms Portal budget", "portal-budget-parity")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
               "## Timing\n\nThe uniform guest-facing non-success budget is 1200ms across every Phase-3 "
               "refusal path.")


@case("the runbook still requires the retired surgical foundation install", "nft-architecture-parity")
def _(d):
    append_doc(d, "docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md",
               "## Deploy\n\nInstall the Phase-3 foundation before enabling anything:\n\n"
               "```bash\n/opt/stayconnect/bin/phase3-foundation install\n```")


@case("the facts block contradicts itself (0010 applied but 49 tables)", "facts-coherence")
def _(d):
    patch_facts(d, "iam_v2_tables", 49)


@case("current_activity disagrees with the recorded facts", "activity-parity")
def _(d):
    # Derived, not hard-coded: this anchor broke on T0025, T0028 and T0029 in turn. A fixture that reads the
    # value it mutates cannot drift out of step with the file.
    cur = json.load(io.open(os.path.join(ROOT, "governance", "project-state.json"),
                            encoding="utf-8"))["current_activity"]
    patch_state(d, lambda s: s.replace(
        '"current_activity": "%s",' % cur,
        '"current_activity": "PHASE_3_SOMETHING_ELSE_ENTIRELY",', 1))


@case("live evidence exists while the facts deny appliance contact", "facts-coherence")
def _(d):
    patch_facts(d, "appliance_contact_occurred", False)


@case("the facts deny acceptance while the phase record says ACCEPTED_AND_CLOSED", "facts-coherence")
def _(d):
    # accepted/closed must move together AND agree with the phase record. Half-retracting acceptance in the
    # facts while phases.3 still says closed is the shape a partially-applied reversal would take.
    patch_facts(d, "accepted", False)


# ---- the acceptance-state contradictions this closure round had to fix ------------------------------------

@case("a current surface still presents Phase 3 as an unaccepted candidate", "acceptance-parity")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
               "## Status" + chr(10)*2 + "Phase 3 is a DARK ACCEPTANCE CANDIDATE and is NOT accepted, pending the Product "
               "Owner's final decision.")


@case("a current surface still says the corrected software is not deployed", "deployment-parity")
def _(d):
    append_doc(d, "docs/reports/StayConnect-IAM-Phase3-Final-Report.md",
               "## Deployment" + chr(10)*2 + "The corrected software is not yet deployed; the appliance still runs the "
               "binaries from the previous candidate and the blocked subset remains pending.")


@case("the accepted NOT-PROVEN limitation is quietly promoted to PASS", "limitation-parity")
def _(d):
    append_doc(d, "docs/reports/StayConnect-IAM-Phase3-Final-Report.md",
               "| 99 | Legacy live-session continuity | **PASS** | verified during the closure round |")


@case("the runbook promises rollback carries authorization across, without the boundary", "rollback-promise-parity")
def _(d):
    append_doc(d, "docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md",
               "## Rollback" + chr(10)*2 + "Restoring any previous release is safe: the next start reconciles the ruleset "
               "and authorization is always carried across the change.")


@case("acceptance is recorded without the decision that granted it", "facts-coherence")
def _(d):
    patch_facts(d, "accepted_decision", "")


@case("facts claim acceptance while the phase record still says IN_PROGRESS", "facts-coherence")
def _(d):
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    doc["phases"]["3"]["status"] = "IN_PROGRESS"
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


# ---- merge parity: the contradiction this merge round would otherwise have shipped -------------------------

@case("a current surface still says the PR is open and unmerged after the merge", "merge-parity")
def _(d):
    # The exact sentence that stood, true, in six documents on the morning of the merge.
    append_doc(d, "docs/reports/StayConnect-IAM-Phase3-Final-Report.md",
               "## Merge status\n\nPR #6 remains open and unmerged. Its merge is a separate Product-Owner "
               "decision and is the only next authorized action.")


@case("the facts record a merge while still flagging the PR as open", "merge-parity")
def _(d):
    patch_facts(d, "pr_open_and_unmerged", True)


@case("the facts record a merge with no merge commit", "merge-parity")
def _(d):
    patch_facts(d, "merge_commit", "")


@case("a current surface claims a merge the facts do not record", "merge-parity")
def _(d):
    patch_facts(d, "merged", False)
    append_doc(d, "docs/context/StayConnect-IAM-Handoff.md",
               "## Merge status\n\nPR #6 was merged and the branch is gone.")


# ---- runtime identity: the claim that outlived the state it described ------------------------------------

@case("a current surface reinstates the unscoped whole-tree identity claim", "runtime-identity-parity")
def _(d):
    # The exact sentence that was true at the merge and false a round later.
    append_doc(d, "docs/reports/StayConnect-IAM-Phase3-Final-Report.md",
               "## Runtime identity" + chr(10) + chr(10) +
               "The runtime tree is byte-for-byte identical to the accepted runtime candidate.")


@case("post-closure changes are recorded but the accepted-binaries fact is dropped", "runtime-identity-parity")
def _(d):
    patch_facts(d, "accepted_runtime_binaries_unchanged", False)


@case("the accepted-binaries head disagrees with the accepted runtime head", "runtime-identity-parity")
def _(d):
    patch_facts(d, "accepted_runtime_binaries_head", "0" * 40)


# ---- phase status: the contradiction that shipped inside one file ------------------------------------------

@case("a started phase is still called NOT_STARTED in the state narrative", "phase-status-parity")
def _(d):
    # Exactly what shipped after PR #11: phases.4 recorded as PLANNING while current_maturity still ended
    # "Phase 4 remains NOT_STARTED and unauthorized". Both sentences were written in the same round.
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    doc["current_maturity"] = doc["current_maturity"] + " Phase 4 remains NOT_STARTED and unauthorized."
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("a started phase is called unauthorized in a blocker", "phase-status-parity")
def _(d):
    # The SECOND instance, which the first (broken) version of the rule missed entirely.
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    doc["blockers"] = list(doc.get("blockers") or []) + ["Phase 4 is not authorized."]
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


# ---- authorization model: allowed and prohibited must agree ------------------------------------------------

def _patch_list(d, key, value, front=True):
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    lst = list(doc.get(key) or [])
    lst.insert(0, value) if front else lst.append(value)
    doc[key] = lst
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("the Posting Engine is authorized and prohibited at the same time", "authorization-model-parity")
def _(d):
    # The exact contradiction that shipped: allowed_actions authorized the Phase-4 Posting Engine while
    # prohibited_actions, written for Phase 3, still forbade it by name.
    _patch_list(d, "prohibited_actions",
                "Any PMS financial posting, FIAS PS transaction, Posting Engine, posting outbox/worker, "
                "charge retry, financial UNKNOWN handling")


# The phase named here is DERIVED. It was written as "Phase 5" while Phase 5 was a future phase, and Phase 5
# then became authorized -- so the validator correctly stopped flagging the sentence and the NEGATIVE case
# failed, reporting a validator regression that had not happened. A case about "an unauthorized future phase"
# has to ask the state file which phase that currently is.
# ...and it drifted once more, in the direction the roadmap was always going to take it: D26 authorized
# Phase 7, so NO phase in the roadmap is unstarted any more and the derivation had nothing to return.
#
# The property under test is not "some phase is unstarted". It is that a current surface must never present
# unauthorized future work as authorized. A phase number BEYOND the roadmap is the purest instance of that --
# it is unauthorized by construction, it cannot become authorized underneath the fixture the way Phase 5 and
# Phase 7 each did, and it keeps the case meaningful for every future phase rather than expiring at each one.
def _first_not_started():
    doc = json.load(io.open(os.path.join(ROOT, "governance", "project-state.json"), encoding="utf-8"))
    phases = doc.get("phases") or {}
    for k, v in sorted(phases.items()):
        if isinstance(v, dict) and v.get("status") == "NOT_STARTED":
            return k
    numbered = [int(k) for k in phases if str(k).isdigit()]
    return str(max(numbered) + 1) if numbered else "99"


@case("an unauthorized future phase appears in allowed_actions", "authorization-model-parity")
def _(d):
    _patch_list(d, "allowed_actions",
                "Execute the authorized Phase %s end-to-end" % _first_not_started())


@case("current_phase_plan belongs to a different phase", "authorization-model-parity")
def _(d):
    # Also derived: the plan path moves with the phase, and pinning both ends made this a silent no-op the
    # moment the current phase changed.
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    cur = str(doc.get("current_phase"))
    other = next(k for k in sorted((doc.get("phases") or {}).keys()) if k != cur)
    doc["current_phase_plan"] = "docs/architecture/StayConnect-IAM-Phase%s-Plan.md" % other
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("current_state_facts.phase disagrees with current_phase", "authorization-model-parity")
def _(d):
    patch_facts(d, "phase", "3")


@case("the phase-4 authorization cites the wrong decision", "authorization-model-parity")
def _(d):
    patch_facts(d, "phase4_decision", "D99")


@case("a next-step demands an authorization that already exists", "authorization-model-parity")
def _(d):
    patch_facts(d, "next_step",
                "Phase 4 requires a separate explicit Product-Owner authorization before implementation.")


@case("a transition receipt cites evidence that is not in the repository", "evidence-reference")
def _(d):
    # The sandbox is a copy, so "not tracked in git" is asserted against the REAL repo index; citing a path
    # that has never existed reproduces the untracked-reference defect exactly.
    p = os.path.join(d, "governance/transitions/T0024.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    doc["evidence_files"].append("docs/evidence/this-file-was-never-committed.md")
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


# ---- the closure-round false passes (D19/T0044) -------------------------------------------------------------
#
# Every case below ACTUALLY SHIPPED through a green Project Governance gate on 581daa05: the receipt said the
# phase was closed, the phases map said IN_PROGRESS, and four current surfaces still narrated an unfinished
# phase. The gate passed because no rule compared a receipt to the phases map, and no rule made "unfinished"
# wrong RELATIVE TO A RECORDED STATUS.

@case("the phases map disagrees with the status the latest transition recorded",
      "transition-phase-coherence")
def _(d):
    # DERIVED from the latest receipt. This was pinned to phase 4 with the value IN_PROGRESS, which
    # contradicted the T0044 closure receipt at the time. Once the latest receipt moved on to a different
    # phase, setting phase 4 contradicted nothing and the rule correctly stayed silent -- so the negative case
    # failed and reported a validator regression that had not happened. What the case is really about is that
    # the map must agree with the receipt, whichever phase and whichever status those currently are.
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    tid = doc["latest_transition_id"]
    rec = json.load(io.open(os.path.join(d, "governance/transitions/%s.json" % tid), encoding="utf-8"))
    phase = str(rec.get("phase_affected") or rec.get("new_state", {}).get("phase"))
    recorded = rec.get("new_state", {}).get("phase_status")
    contradiction = "IN_PROGRESS" if recorded != "IN_PROGRESS" else "ACCEPTED_AND_CLOSED"
    if doc["phases"][phase]["status"] == contradiction:
        raise AssertionError("mutation changed nothing: phases.%s is already %s" % (phase, contradiction))
    doc["phases"][phase]["status"] = contradiction
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("a closed phase's plan still says it is NOT accepted and NOT closed", "accepted-phase-semantics")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase4-Plan.md",
               "## Status\n\nPhase 4 is NOT accepted and NOT closed - that decision is the Product Owner's.")


@case("a closed phase's plan still carries the bare 'Not accepted, not closed' headline",
      "accepted-phase-semantics")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase4-Plan.md",
               "**Not accepted, not closed.** Every Phase-4 flag is OFF and no real financial traffic has "
               "occurred.")


@case("a current surface says a closed phase remains in progress", "accepted-phase-semantics")
def _(d):
    append_doc(d, "docs/context/StayConnect-IAM-Handoff.md",
               "## Current position\n\nPhase 4 remains IN_PROGRESS on branch phase/4-financial-execution; "
               "implementation continues under D18/T0029.")


@case("a current surface still awaits Product-Owner acceptance of a closed phase",
      "accepted-phase-semantics")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase1A-Plan.md",
               "- **Current position: awaiting Product-Owner acceptance of Phase 1A**, then Phase 1B "
               "planning under separate authorization.")


@case("allowed_actions still authorizes executing a phase that is closed", "accepted-phase-semantics")
def _(d):
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    doc["allowed_actions"] = [
        "Execute the authorized Phase 4 (Financial Execution Layer) end-to-end as one Phase, DARK at "
        "NO-FINANCIAL-TRAFFIC maturity, per docs/architecture/StayConnect-IAM-Phase4-Plan.md under D18/T0029."
    ]
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("the latest transition receipt records no new phase_status at all", "transition-phase-coherence")
def _(d):
    p = os.path.join(d, "governance/project-state.json")
    latest = json.load(io.open(p, encoding="utf-8"))["latest_transition_id"]
    rp = os.path.join(d, "governance/transitions/%s.json" % latest)
    doc = json.load(io.open(rp, encoding="utf-8"))
    doc["new_state"].pop("phase_status", None)
    io.open(rp, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


# ---- STATIC CURRENT-STATE PROSE OUTSIDE THE GENERATED BLOCK -------------------------------------------------
#
# Every one of these SHIPPED on b26f24a with all three GitHub workflows green. The generated blocks were
# correct in each file; the hand-written prose around them was two to four phases stale, and no rule read it.
# These are the verbatim sentences, reintroduced.

@case("START-HERE calls the financial posting engine a future component", "static-current-prose")
def _(d):
    append_doc(d, "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
               "- **PMS integration:** FIAS connector is **lookup-only today**; the financial **posting "
               "engine is a future component** (see phase status).")


@case("the Phase-4 plan presents the runtime as greenfield / nonexistent", "static-current-prose")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase4-Plan.md",
               "## 3. What does not exist - the Phase-4 build\n\n**All seven financial tables have zero Go "
               "references.** The execution runtime is greenfield:")


@case("the Handoff restates a superseded phase as CURRENT outside the generated block",
      "static-current-prose")
def _(d):
    append_doc(d, "docs/context/StayConnect-IAM-Handoff.md",
               "**CURRENT (see the generated block): Phase 2 (Commercial Packages) is authorized under "
               "D12/T0012, implemented and live-dark deployed.**")


@case("the Phase-0 contract carries a stale next authorized activity", "static-current-prose")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase0-Contract.md",
               "| Next authorized activity | **Product-Owner acceptance of Phase 1A**, then **Phase 1B "
               "planning under separate authorization**. |")


@case("a plan says Phase 1B planning is the current activity", "static-current-prose")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
               "Phase 1A is accepted and closed. Phase 1B planning is the current activity. All status "
               "carriers corrected accordingly.")


@case("a surface presents a CLOSED phase as awaiting acceptance", "static-current-prose")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase1A-Plan.md",
               "The project is awaiting Product-Owner acceptance of Phase 1A before anything else may "
               "proceed.")



# ---- STRUCTURAL: a section claiming to carry mutable current state ------------------------------------------
#
# These are the sections that were STILL in 00-START-HERE on a2a17dbe with all three workflows green, below a
# generated block that correctly said Phase 4 was accepted and closed. Rule 11 refuses known stale sentences;
# it could not refuse a HEADING that announces itself as the current plan, because the sentence under it can
# be reworded every phase while the claim survives. Reproduced verbatim.

@case("a section heading claims to be the CURRENT APPROVED PLAN", "static-current-prose")
def _(d):
    append_doc(d, "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
               "## 8. Current approved plan (Phase 1A)\n\nBuild the entire clean-slate IAM schema into an "
               "isolated iam_v2 PostgreSQL schema inside the existing site database.")


@case("a section heading claims to be the NEXT AUTHORIZED ACTION", "static-current-prose")
def _(d):
    append_doc(d, "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
               "## 9. Next authorized action\n\nThe single next authorized action is complete Phase 1B "
               "execution and live-dark verification.")


@case("a section claims Phase 1B is authorized and IN_PROGRESS", "accepted-phase-semantics")
def _(d):
    append_doc(d, "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
               "## Status\n\nPhase 1B implementation is Product-Owner authorized and IN_PROGRESS (decision "
               "D10, 2026-07-17; W0 complete).")


@case("a section heading claims to carry the CURRENT PROJECT PHASE without deferring",
      "static-current-prose")
def _(d):
    append_doc(d, "docs/context/StayConnect-IAM-Handoff.md",
               "## Current project phase & status\n\nThe project is in Phase 1B, executing Gate P on branch "
               "phase/1b-dark-auth.")


# ---- A MERGED PULL REQUEST STILL DESCRIBED AS OPEN ---------------------------------------------------------
#
# Merge state goes stale in silence: the merge happens on GitHub and nothing in the repository changes, so
# every prose surface keeps whatever it said the day before. These are the sentences that were STILL on master
# at 573cf814 with all three workflows green and every other parity rule passing. Reproduced verbatim.

@case("current_maturity calls the merged PR the only open item", "merged-pr-state")
def _(d):
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    doc["current_maturity"] = (
        "Phases 0, 1A, 1B, 2, 3 and 4 are ALL ACCEPTED_AND_CLOSED. THE ONLY OPEN ITEM is the Product Owner's "
        "separate decision on merging Phase-4 pull request #12, which is OPEN and UNMERGED.")
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("the Phase-4 Plan status line says the pull request is open and unmerged", "merged-pr-state")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase4-Plan.md",
               "**Status:** ACCEPTED AND CLOSED. The Phase-4 pull request is OPEN and UNMERGED; merging "
               "requires a separate explicit Product-Owner decision.")


@case("a surface still carries the standing DO NOT MERGE instruction for a merged PR", "merged-pr-state")
def _(d):
    append_doc(d, "docs/reports/StayConnect-IAM-Phase4-Final-Report.md",
               "> **Status:** ACCEPTED_AND_CLOSED (D19/T0044). **DO NOT MERGE** PR #12 — merging requires a "
               "separate explicit Product-Owner decision.")


@case("the Phase-3 Plan still says PR #6 is not merged", "merged-pr-state")
def _(d):
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
               "Delivered DARK: all Phase-3 flags default OFF; PR #6 is not merged before the single final "
               "Product-Owner acceptance decision.")


# INVERTED: a TRUE statement about an OPEN pull request whose number merely CONTAINS a merged one's.
#
# a bare number match matched the "6" inside "#16", so an accurate sentence about the open PR #16 was reported as a stale
# claim about merged PR #6. The gate then failed on correct text, and the cheapest way to make it pass was to
# make the text vaguer -- the exact opposite of what this file exists to enforce. Pinned in both directions:
# the case above still requires a real "#6 is not merged" to be caught.
@case("a true statement about open PR #16 is not read as a stale claim about merged PR #6", None)
def _(d):
    append_doc(d, "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
               "## Delivery\n\nPR #16 remains open and unmerged pending a separate Product-Owner decision.")


@case("a pack entry point describes the merged pull request as still open", "merged-pr-state")
def _(d):
    append_doc(d, "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
               "## Delivery\n\nThe Phase-4 branch is delivered to GitHub and PR #12 remains open and unmerged "
               "pending a separate Product-Owner merge decision.")


# ---- the closure false-green: a phase closed in one field and still authorized in another ------------------
#
# Found independently AFTER the Phase-7 acceptance was recorded and every gate was green. The receipt, the
# phases map and the next action all said ACCEPTED_AND_CLOSED; three other current fields still described the
# previous world, and the generated blocks printed both at once. These four cases keep each half of that
# defect dead. They are written against whatever phase the state currently records as closed, not against
# phase 7, so they keep working after the next acceptance.

def _a_closed_phase(d):
    doc = json.load(io.open(os.path.join(d, "governance/project-state.json"), encoding="utf-8"))
    for ph, body in sorted((doc.get("phases") or {}).items()):
        if isinstance(body, dict) and str(body.get("status", "")).strip() in ("ACCEPTED_AND_CLOSED", "FINAL_CLOSED"):
            return ph
    raise AssertionError("no closed phase in the fixture: this suite has nothing to regress")


@case("latest_accepted_po_decision points at an older decision than the register's newest accepted one",
      "latest-decision-pointer")
def _(d):
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    reg = json.load(io.open(os.path.join(d, "governance/decision-register.json"), encoding="utf-8"))
    ids = [str(x.get("id")) for x in reg.get("decisions", []) if isinstance(x, dict) and x.get("accepted")]
    nums = sorted(int(i[1:]) for i in ids if i.startswith("D") and i[1:].isdigit())
    if len(nums) < 2:
        raise AssertionError("the register has fewer than two accepted decisions to regress between")
    doc["latest_accepted_po_decision"] = "D%d" % nums[-2]
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("a closed phase is described as AUTHORIZED in the current blockers",
      "closed-phase-still-authorized")
def _(d):
    ph = _a_closed_phase(d)
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    doc["blockers"] = ["No governance blocker. Phase %s is AUTHORIZED for planning and execution." % ph]
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("a closed phase is described as authorized in the current prohibited actions",
      "closed-phase-still-authorized")
def _(d):
    ph = _a_closed_phase(d)
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    acts = list(doc.get("prohibited_actions") or [])
    acts.append("Implementing work beyond the authorized Phase %s scope." % ph)
    doc["prohibited_actions"] = acts
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("a generated current-state block prints a stale latest-decision id",
      "generated-block-stale-decision")
def _(d):
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    cur = str(doc.get("latest_accepted_po_decision") or "")
    if not cur.startswith("D"):
        raise AssertionError("no latest_accepted_po_decision to regress against")
    stale = "D%d" % (int(cur[1:]) - 1)
    rel = "docs/context/StayConnect-IAM-Handoff.md"
    f = os.path.join(d, rel)
    s2 = io.open(f, encoding="utf-8", newline="").read()
    marker = "Latest accepted PO decision: `%s`" % cur
    if marker not in s2:
        raise AssertionError("the generated block does not carry the decision marker: anchor drifted")
    io.open(f, "w", encoding="utf-8", newline="").write(
        s2.replace(marker, "Latest accepted PO decision: `%s`" % stale))


# ---- the two closure survivors: stale authority, and a closed project still called active ------------------
#
# Both were found after the previous correction was already green, and both are the same shape as the cases
# above: a current field frozen at the moment it was written. Written against whatever the state currently
# records, so they keep working after the next decision and the next closure.

@case("a current prohibition settles what is authorized by citing a superseded decision",
      "superseded-decision-as-current-authority")
def _(d):
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    reg = json.load(io.open(os.path.join(d, "governance/decision-register.json"), encoding="utf-8"))
    ids = [str(x.get("id")) for x in reg.get("decisions", []) if isinstance(x, dict) and x.get("accepted")]
    nums = sorted(int(i[1:]) for i in ids if i.startswith("D") and i[1:].isdigit())
    if len(nums) < 2:
        raise AssertionError("the register has fewer than two accepted decisions to supersede between")
    older = "D%d" % nums[-2]
    acts = list(doc.get("prohibited_actions") or [])
    acts.append("IAM-v2 production cutover and go-live: none is authorized by %s." % older)
    doc["prohibited_actions"] = acts
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("current state calls the project under active development after every phase is closed",
      "closed-project-described-as-active-development")
def _(d):
    p = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    phases = doc.get("phases") or {}
    if any(str((b or {}).get("status", "")).strip() not in ("ACCEPTED_AND_CLOSED", "FINAL_CLOSED")
           for b in phases.values() if isinstance(b, dict)):
        raise AssertionError("a phase is still open in the fixture: this case has no subject")
    srs = doc.get("service_routing_state")
    if not isinstance(srs, str):
        raise AssertionError("service_routing_state is not prose: the fixture anchor has drifted")
    doc["service_routing_state"] = srs + " The system is under active development and controlled testing."
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


# ---- the post-merge survivors: records that did not move when the merge did --------------------------------
#
# After PR #15 merged, five current surfaces still described the world before it, and a sixth (the progress
# evidence) was only found when the rule was widened to scan every committed plan and report. Prose rules could
# not see the machine markers, and the merged-PR rule had only ever looked at OPEN pull requests.

@case("a merged phase's report still carries the machine marker PR_OPEN_UNMERGED",
      "merged-phase-still-called-unmerged")
def _(d):
    doc = json.load(io.open(os.path.join(d, "governance/project-state.json"), encoding="utf-8"))
    if not any((b or {}).get("merged") for b in (doc.get("phases") or {}).values() if isinstance(b, dict)):
        raise AssertionError("no merged phase in the fixture: this case has no subject")
    import glob as _g
    reps = _g.glob(os.path.join(d, "docs/reports/*-Final-Report.md"))
    if not reps:
        raise AssertionError("no committed final report to regress")
    f = sorted(reps)[-1]
    txt = io.open(f, encoding="utf-8", newline="").read()
    io.open(f, "w", encoding="utf-8", newline="").write(
        txt + "\n\n<!-- MERGE_STATE: PR_OPEN_UNMERGED -->\n")


@case("machine-readable phase status still reads IN_PROGRESS after the phase closed",
      "machine-state-behind-phase-status")
def _(d):
    p2 = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p2, encoding="utf-8"))
    closed = [ph for ph, b in (doc.get("phases") or {}).items()
              if isinstance(b, dict) and str(b.get("status", "")).strip() in ("ACCEPTED_AND_CLOSED", "FINAL_CLOSED")]
    if not closed:
        raise AssertionError("no closed phase in the fixture")
    ph = sorted(closed)[-1]
    doc["current_state_facts"]["phase%s_status" % ph.lower()] = "IN_PROGRESS"
    io.open(p2, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("a completed roadmap still carries an ACTIVE_DEVELOPMENT machine status",
      "completed-roadmap-still-active-development")
def _(d):
    p2 = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p2, encoding="utf-8"))
    if str((doc.get("roadmap_exhaustion") or {}).get("numbered_development_roadmap", "")).upper() != "COMPLETE":
        raise AssertionError("the roadmap is not recorded COMPLETE in the fixture")
    doc["current_state_facts"]["operational_status"] = "PRE_LIVE__ACTIVE_DEVELOPMENT_AND_CONTROLLED_TESTING"
    io.open(p2, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


@case("a receipt denies contacting an environment in the same claim that reports what it observed there",
      "runtime-contact-evidence-contradicts-itself")
def _(d):
    p2 = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p2, encoding="utf-8"))
    rid = doc.get("latest_transition_id")
    if not rid:
        raise AssertionError("no latest_transition_id to regress")
    rp = os.path.join(d, "governance/transitions/%s.json" % rid)
    rec = json.load(io.open(rp, encoding="utf-8"))
    rec.setdefault("runtime_effect", {})["development_appliance"] = (
        "not contacted for this change; uptime 14h32m and the endpoint still 404")
    io.open(rp, "w", encoding="utf-8", newline="").write(json.dumps(rec, indent=2, ensure_ascii=False) + chr(10))


# ---- the contradictions that lived INSIDE the authoritative state file --------------------------------------
#
# Every prose rule up to here scanned markdown DOC surfaces. None of them opened
# governance/project-state.json, so the authority itself was the one surface allowed to contradict its own
# structured fields -- and it did, in three places at once, through a green gate:
#
#   * phase_lifecycle_authority recorded Phase 7 MERGED while /phases/7/maturity still narrated
#     "PR #15, which remains OPEN and UNMERGED";
#   * the DEVELOPMENT trial had been authorized (D29/T0066, re-scoped D31/T0068) and was running, while
#     roadmap_exhaustion still said "that trial is not authorized yet";
#   * next_authorized_action still asked for the Hotel Admin rebuild, its deployment and the admin login,
#     all three of which had already completed.
#
# The docs were policed and the authority was not. These three cases keep it policed.

@case("the state file narrates a lifecycle-merged phase as an open, unmerged PR",
      "state-narrates-merged-phase-as-unmerged")
def _(d):
    p2 = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p2, encoding="utf-8"))
    auth = doc.get("phase_lifecycle_authority") or {}
    merged = [ph for ph, ev in auth.items() if isinstance(ev, dict) and "merged" in ev]
    if not merged:
        raise AssertionError("no lifecycle-merged phase in the fixture: this case has no subject")
    ph = sorted(merged)[0]
    doc.setdefault("phases", {}).setdefault(str(ph), {})["maturity"] = (
        "Phase %s was delivered to head 16819aa0 on its branch (PR #15, which remains OPEN and UNMERGED "
        "pending a separate merge decision)." % ph)
    io.open(p2, "w", encoding="utf-8", newline="\n").write(json.dumps(doc, indent=2, ensure_ascii=False) + "\n")


@case("an activity recorded as authorized and in progress is still called not authorized yet",
      "authorized-activity-described-as-unauthorized")
def _(d):
    p2 = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p2, encoding="utf-8"))
    acts = [a for a in (doc.get("authorized_activities") or [])
            if isinstance(a, dict) and str(a.get("status", "")).upper() not in ("PROPOSED", "NOT_AUTHORIZED")]
    if not acts:
        raise AssertionError("no authorized activity in the fixture: this case has no subject")
    name = acts[0]["name"]
    doc.setdefault("roadmap_exhaustion", {})["only_major_next_lifecycle_gate"] = (
        "The Product Owner intends a %s, and that trial is not authorized yet." % name)
    io.open(p2, "w", encoding="utf-8", newline="\n").write(json.dumps(doc, indent=2, ensure_ascii=False) + "\n")


@case("next_authorized_action still asks for work already recorded as complete",
      "completed-work-still-listed-as-next-action")
def _(d):
    p2 = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p2, encoding="utf-8"))
    done = [x for x in (doc.get("completed_activities") or []) if isinstance(x, str)]
    if not done:
        raise AssertionError("no completed activity in the fixture: this case has no subject")
    doc["next_authorized_action"] = (
        "Continue the authorized DEVELOPMENT trial by carrying out the %s, then verify the result end to end "
        "on the running appliance." % done[0])
    io.open(p2, "w", encoding="utf-8", newline="\n").write(json.dumps(doc, indent=2, ensure_ascii=False) + "\n")


# ---- and the other half of the contract: a correct scope limit must NOT be flagged --------------------------
#
# The first version of authorized-activity-described-as-unauthorized failed the repository over the sentence
# "Authorizing the DEVELOPMENT trial did NOT authorize any Production IAM transition" -- which is correct, and
# is exactly the kind of limit the Product Owner requires to be stated. A rule that cannot tell "X is not
# authorized" from "X does not authorize Y" would train everyone to delete the limits. This case asserts the
# distinction holds, and it is expected to PASS the validator.

@case("a correct scope limit ('did not authorize any Production transition') is not flagged",
      None)
def _(d):
    p2 = os.path.join(d, "governance/project-state.json")
    doc = json.load(io.open(p2, encoding="utf-8"))
    doc.setdefault("roadmap_exhaustion", {})["only_major_next_lifecycle_gate"] = (
        "The Product Owner intends a post-roadmap DEVELOPMENT appliance IAM-v2 operational trial, which is "
        "authorized and in progress. Authorizing the DEVELOPMENT trial did NOT authorize any Production IAM "
        "transition, and no Production cutover is authorized.")
    io.open(p2, "w", encoding="utf-8", newline="\n").write(json.dumps(doc, indent=2, ensure_ascii=False) + "\n")


def main():
    print("== baseline: the real repository must PASS ==")
    d = sandbox()
    rc, out = run(d)
    shutil.rmtree(d, ignore_errors=True)
    if rc != 0:
        print("  FAIL: the unmodified repository does not pass parity: %s" % out)
        return 1
    print("  PASS: baseline parity holds (%d checks)" % out.get("pass", 0))

    print("\n== each contradiction must be caught, by the right rule ==")
    passed = failed = 0
    for name, rule, mutate in CASES:
        d = sandbox()
        try:
            mutate(d)
            rc, out = run(d)
            rules = {c.get("rule") for c in out.get("checks", []) if c.get("status") == "FAIL"}
            # rule=None inverts the case: this text is CORRECT and must not be flagged. Over-firing is its own
            # failure mode -- a rule that refuses a true statement teaches people to delete true statements.
            if rule is None:
                if rc == 0:
                    print("  PASS  %s\n        -> correctly not flagged" % name)
                    passed += 1
                else:
                    print("  FAIL  %s\n        -> a correct statement was flagged by %s" % (name, sorted(rules)))
                    failed += 1
                continue
            if rc == 0:
                print("  FAIL  %s\n        -> validator PASSED a contradiction" % name)
                failed += 1
            elif rule not in rules:
                print("  FAIL  %s\n        -> caught, but by %s rather than %s" % (name, sorted(rules) or "nothing", rule))
                failed += 1
            else:
                print("  PASS  %s\n        -> %s" % (name, rule))
                passed += 1
        finally:
            shutil.rmtree(d, ignore_errors=True)

    print("\n== history must still be allowed to be history ==")
    # The same pre-build sentences the static-prose rule refuses, LABELLED. If this ever fails, the rule has
    # stopped telling a record from a claim, and the Phase-4 plan could no longer keep its own history.
    d = sandbox()
    append_doc(d, "docs/architecture/StayConnect-IAM-Phase4-Plan.md",
               "## HISTORICAL, as at authorization time (2026-08-11)\n\nAll seven financial tables had "
               "zero Go references and the execution runtime was greenfield. This is NOT the current state.")
    rc, out = run(d)
    if rc == 0:
        print("  PASS  labelled pre-build history is not treated as a current claim")
        passed += 1
    else:
        print("  FAIL  labelled pre-build history was refused -> %s"
              % sorted({c.get("rule") for c in out.get("checks", []) if c.get("status") == "FAIL"}))
        failed += 1
    shutil.rmtree(d, ignore_errors=True)

    d = sandbox()
    append_doc(d, "docs/context/StayConnect-IAM-Handoff.md",
               "At Phase-1B acceptance the schema was iam_v2 49 tables / 0 rows and migration 0010 was "
               "undeployed, with no appliance access. (HISTORICAL — superseded by the Increment-9 execution "
               "of 2026-08-10.)")
    rc, out = run(d)
    shutil.rmtree(d, ignore_errors=True)
    if rc != 0:
        print("  FAIL: a clearly-labelled historical statement was refused: %s"
              % [c for c in out.get("checks", []) if c.get("status") == "FAIL"][:2])
        failed += 1
    else:
        print("  PASS  a labelled historical statement is not a contradiction")
        passed += 1

    print("\n== a repository that records no facts must not quietly pass ==")
    d = sandbox()
    patch_state(d, lambda s: s.replace('"current_state_facts": {', '"current_state_facts_disabled": {', 1))
    rc, _ = run(d)
    shutil.rmtree(d, ignore_errors=True)
    if rc == 0:
        print("  FAIL: a missing current_state_facts block passed")
        failed += 1
    else:
        print("  PASS  a missing current_state_facts block is refused (exit %d)" % rc)
        passed += 1

    print("\nCURRENT_STATE_PARITY_NEGATIVE: %d passed, %d failed" % (passed, failed))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
