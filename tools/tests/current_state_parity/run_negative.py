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
    "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
    "docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md",
    "docs/reports/StayConnect-IAM-Phase3-Final-Report.md",
    "docs/context/StayConnect-IAM-Handoff.md",
]


def sandbox():
    d = tempfile.mkdtemp(prefix="parity-")
    for rel in COPY:
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
    patch_state(d, lambda s: s.replace(
        '"current_activity": "PHASE_3_DARK_ACCEPTANCE_CANDIDATE",',
        '"current_activity": "PHASE_3_SOMETHING_ELSE_ENTIRELY",', 1))


@case("live evidence exists while the facts deny appliance contact", "facts-coherence")
def _(d):
    patch_facts(d, "appliance_contact_occurred", False)


@case("the facts claim Phase 3 is accepted while every other surface says IN_PROGRESS", "facts-coherence")
def _(d):
    patch_facts(d, "accepted", True)


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
