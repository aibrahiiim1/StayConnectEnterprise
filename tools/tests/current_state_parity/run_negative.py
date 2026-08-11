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
            if ev and not ev.endswith("/") and ev not in rels:
                rels.append(ev)

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


@case("a transition receipt cites evidence that is not in the repository", "evidence-reference")
def _(d):
    # The sandbox is a copy, so "not tracked in git" is asserted against the REAL repo index; citing a path
    # that has never existed reproduces the untracked-reference defect exactly.
    p = os.path.join(d, "governance/transitions/T0024.json")
    doc = json.load(io.open(p, encoding="utf-8"))
    doc["evidence_files"].append("docs/evidence/this-file-was-never-committed.md")
    io.open(p, "w", encoding="utf-8", newline="").write(json.dumps(doc, indent=2, ensure_ascii=False) + chr(10))


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
