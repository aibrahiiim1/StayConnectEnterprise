#!/usr/bin/env python3
"""Adversarial mutation tests for the project-state governance validators.

Each mutation injects exactly one defect into the real files, runs the structural validator
(tools/project-state.py validate) and the keyword validator (tools/validate-project-state.sh),
and asserts that AT LEAST ONE reports failure (non-zero). The original bytes are restored
(try/finally) after every case. Finally the restored good state must PASS both validators.

A validator that only passes the good state without failing these negative cases is NOT accepted.
Run from anywhere:  python tools/tests/project_state_validator/run_mutations.py
"""
import subprocess, os, sys, shutil

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))

def _find_bash():
    # Prefer Git Bash on Windows (Python's PATH 'bash' may resolve to WSL bash, which fails on Windows paths).
    for env in ("BASH", "GIT_BASH"):
        b = os.environ.get(env)
        if b and os.path.isfile(b): return b
    g = shutil.which("git")
    if g:
        for rel in ("../bin/bash.exe", "../../bin/bash.exe", "../usr/bin/bash.exe"):
            cand = os.path.normpath(os.path.join(os.path.dirname(g), rel))
            if os.path.isfile(cand): return cand
    for cand in (r"C:\Program Files\Git\bin\bash.exe", r"C:\Program Files\Git\usr\bin\bash.exe",
                 r"C:\Program Files (x86)\Git\bin\bash.exe"):
        if os.path.isfile(cand): return cand
    return shutil.which("bash") or "bash"

BASH = _find_bash()

def run(cmd):
    return subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
def structural():
    return run([sys.executable, "tools/project-state.py", "validate"]).returncode
def keyword():
    return run([BASH, "tools/validate-project-state.sh"]).returncode
def both_status():
    return structural(), keyword()

# mutation = (name, relpath, op) ; op = ("replace",[(find,repl),...]) | ("append", text)
MUTATIONS = [
 ("M01 Phase 1A NOT_STARTED", "governance/project-state.json",
   ("replace", [('"1A": { "status": "ACCEPTED_AND_CLOSED"', '"1A": { "status": "NOT_STARTED"')])),
 ("M02 Phase 1A pending/planning", "governance/project-state.json",
   ("replace", [('"1A": { "status": "ACCEPTED_AND_CLOSED"', '"1A": { "status": "PLANNING"')])),
 ("M03 two current phases", "governance/project-state.json",
   ("replace", [('"4":  { "status": "NOT_STARTED"', '"4":  { "status": "IN_PROGRESS"')])),
 ("M04 two next authorized actions", "governance/project-state.json",
   ("replace", [('"next_authorized_action": "Execute the authorized Phase 3 end-to-end as one Phase',
                 '"next_authorized_action": "Execute the authorized Phase 3; also start Phase 4 now. Execute the authorized Phase 3 end-to-end as one Phase')])),
 ("M05 Phase 1B production iam_v2 grant", "docs/architecture/Phase1B-Privilege-Matrix.md",
   ("replace", [("PRODUCTION_IAM_V2_DML: NONE", "PRODUCTION_IAM_V2_DML: GRANTED")])),
 ("M06 Phase 1B rolled-back production write allowed", "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
   ("replace", [("rolled-back", "committed")])),
 ("M07 modified generated block", "docs/context/StayConnect-IAM-Handoff.md",
   ("replace", [("**Current phase:** 3", "**Current phase:** 9Z")])),
 ("M08 stale source commit / snapshot mismatch", "governance/project-state.json",
   ("replace", [('"latest_transition_id": "T0015"', '"latest_transition_id": "T0008"')])),
 ("M09 missing acceptance record", "governance/project-state.json",
   ("replace", [('"path": "docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md"',
                 '"path": "docs/acceptance/MISSING.md"')])),
 ("M10 missing permanent rule", "governance/artifact-registry.json",
   ("replace", [('"path": "docs/ZERO_STALE_LEFTOVERS_RULE.md", "status": "AUTHORITATIVE"',
                 '"path": "docs/MISSING_RULE.md", "status": "AUTHORITATIVE"')])),
 ("M11 retained legacy item without removal gate", "governance/artifact-registry.json",
   ("replace", [('"removal_gate": "later separately-approved legacy-cleanup phase, AFTER the atomic complete-domain cutover + reconciliation"',
                 '"removal_gate": ""')])),
 ("M12 stale exported copy", "exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Handoff.md",
   ("append", "\n<!-- tampered export copy -->\n")),
 ("M13 broken pack link", "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
   ("append", "\n[dangling](this-file-does-not-exist.md)\n")),
 ("M14 pack hash mismatch", "exports/chatgpt/stayconnectenterprise/SYSTEM_OVERVIEW.md",
   ("append", "\n<!-- tamper -->\n")),
 ("M15 unmarked historical/current contradiction", "docs/context/StayConnect-IAM-Handoff.md",
   ("append", "\nPhase 1A is the current phase.\n")),
 ("M16 authoritative remote hijacked", "governance/project-state.json",
   ("replace", [("aibrahiiim1/StayConnectEnterprise.git", "attacker/Evil.git")])),
 ("M17 GH delivery decision removed", "governance/decision-register.json",
   ("replace", [('"id": "GH-SOURCE-OF-TRUTH"', '"id": "GH-SOURCE-OF-TRUTH-DISABLED"')])),
 ("M18 governance CI workflow missing", ".github/workflows/project-governance.yml",
   ("remove", None)),
 ("M19 required CI validation command removed", ".github/workflows/project-governance.yml",
   ("replace", [("python tools/project-state.py validate", "echo skip-validate")])),
 ("M20 CI no longer runs on PRs to master", ".github/workflows/project-governance.yml",
   ("replace", [("pull_request:", "pull_request_disabled:")])),
 ("M21 CI job ignores failures", ".github/workflows/project-governance.yml",
   ("append", "\n    continue-on-error: true\n")),
 ("M22 agent-only-operations decision removed", "governance/decision-register.json",
   ("replace", [('"id": "GH-AGENT-ONLY-OPERATIONS"', '"id": "GH-AGENT-ONLY-OPERATIONS-DISABLED"')])),
 ("M23 rule flipped to require manual PO Git commands", "docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md",
   ("replace", [("GIT_OPERATIONS_OWNER: AGENT", "GIT_OPERATIONS_OWNER: PRODUCT_OWNER")])),
 ("M24 LF policy weakened (eol=lf removed)", ".gitattributes",
   ("replace", [("* text=auto eol=lf", "* text=auto")])),
 ("M25 .gitattributes missing", ".gitattributes",
   ("remove", None)),
 ("M26 plan says PLANNING ONLY while IN_PROGRESS", "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
   ("append", "\n\nStatus: PLANNING ONLY — NOT APPROVED FOR IMPLEMENTATION.\n")),
 ("M27 plan production-iam_v2 sentinel flipped", "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
   ("replace", [("PHASE_1B_PRODUCTION_IAM_V2_RUNTIME: NONE", "PHASE_1B_PRODUCTION_IAM_V2_RUNTIME: SHADOW")])),
 ("M28 plan reintroduces production iam_v2 runtime grant", "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
   ("append", "\n\n- `svc_scd` iam_v2 grants prepared for cutover: USAGE + SELECT/INSERT/UPDATE.\n")),
 # --- live-dark / acceptance stale-state contradictions (must be caught by project-state.py) ---
 ("M29 current_activity disagrees with the latest transition new_state.activity", "governance/project-state.json",
   ("replace", [('"current_activity": "PHASE_3_IMPLEMENTATION_IN_PROGRESS"',
                 '"current_activity": "PHASE_2_ACCEPTED_AND_CLOSED"')])),
 ("M30 gate_p cutover done but blocker says superuser", "governance/project-state.json",
   ("replace", [("No governance blocker: Phase 3 (PMS Stay Domain",
                 "Site-DB services still connect as superuser stayconnect and least-privilege roles are not yet applied. No governance blocker: Phase 3 (PMS Stay Domain")])),
 ("M31 stale 'Phase 3 not-started/unauthorized' in a current field after D14/T0015", "governance/project-state.json",
   ("replace", [('"Governance and documentation maintenance for Phase 3"',
                 '"Phase 3 is NOT_STARTED and unauthorized; await explicit Product-Owner authorization"')])),
 ("M32 stale HEAD / production-unchanged in current state after T0010", "governance/project-state.json",
   ("replace", [("legacy public-schema auth remains the sole production authority (iam_v2 49/0).",
                 "legacy public-schema auth remains the sole production authority (iam_v2 49/0). HEAD 1844da2 Production unchanged.")])),
 ("M33 phase 1B marked closed without recorded PO acceptance", "governance/project-state.json",
   ("replace", [('"transition_accepted": true', '"transition_accepted": false')])),
 ("M34 closed but evidence still says PENDING PO acceptance", "governance/project-state.json",
   ("replace", [("Product-Owner ACCEPTED_AND_CLOSED at DARK maturity via D11/T0011",
                 "reboot-validated; PENDING PO acceptance")])),
 ("M35 closed/merged but an allowed_action still says merge PR #2", "governance/project-state.json",
   ("replace", [('"Governance and documentation maintenance for Phase 3"',
                 '"Merge PR #2 as governance/code delivery only"')])),
 ("M36 stale prohibition still forbids the authorized current Phase 3", "governance/project-state.json",
   ("replace", [("Implementing any Phase beyond the authorized Phase 3 scope (Phase 4 or any later Phase)",
                 "Implementing any Phase beyond the authorized Phase 2 dark scope (Phase 3 or any later Phase)")])),
 ("M37 phase3_execution.transition_id not pointing at T0015 while in progress", "governance/project-state.json",
   ("replace", [('"transition_id": "T0015",', '"transition_id": "T0012",')])),
 # --- Zero-Stale reconciliation contradiction classes ---
 ("M38 final report claims no UI test harness after the gate records UI tests", "docs/reports/StayConnect-IAM-Phase2-Final-Report.md",
   ("append", "\n\nNote: no JS component/E2E test harness exists in hotel-admin.\n")),
 ("M39 final report presents 67 changed files as current", "docs/reports/StayConnect-IAM-Phase2-Final-Report.md",
   ("append", "\n\nThe manifest lists 67 changed files.\n")),
 ("M40 live evidence loses the current hotel-admin bundle hash", "docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md",
   ("replace", [("678c793ea46f23241eba05bde66929b19a5473fc8d3752d2a5eb083f4ff0dd95",
                 "e25126737341d8f248ae3a4589ba3a72778705a00f25b8caf6312c64a723999d")])),
 ("M41 Phase-3 plan drops the no-financial-posting sentinel", "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
   ("replace", [("PHASE_3_NO_FINANCIAL_POSTING: TRUE", "PHASE_3_NO_FINANCIAL_POSTING: FALSE")])),
 ("M42 phase1b planning pack generator drops the HISTORICAL marker", "tools/project-state.py",
   ("replace", [("PLANNING_PACK_STATUS: HISTORICAL", "PLANNING_PACK_STATUS: CURRENT")])),
 ("M43 public fingerprint reconciliation note removed (conflicting unnamed values)", "governance/project-state.json",
   ("replace", [('"public_columns_fingerprint_reconciliation"', '"public_columns_fingerprint_reconciliation_DISABLED"')])),
 ("M44 project pack source list drops the Phase-2 final report", "tools/project-state.py",
   ("replace", [('"StayConnect-IAM-Phase2-Final-Report.md": ("docs/reports/StayConnect-IAM-Phase2-Final-Report.md",None),',
                 "")])),
 # --- Phase-2 acceptance/closure + complete-manifest self-reference contradiction classes ---
 ("M45 Phase 2 accepted (transition_accepted=true) but status not ACCEPTED_AND_CLOSED", "governance/project-state.json",
   ("replace", [('"2":  { "status": "ACCEPTED_AND_CLOSED"', '"2":  { "status": "IN_PROGRESS"')])),
 ("M46 change-manifest lists a path not present in git base..HEAD", "docs/manifests/Phase3-change-manifest.md",
   ("append", "\n| `zz-fabricated-extra-path.md` | CREATED | `A` | other | OTHER | rollback REMOVES it | fabricated |\n")),
 ("M47 acceptance decision D13 removed from the register", "governance/decision-register.json",
   ("replace", [('"id": "D13"', '"id": "D13-DISABLED"')])),
 ("M48 manifest base repointed so its path/status set no longer equals git base..delivery_head", "docs/manifests/Phase3-change-manifest.md",
   ("replace", [("ffb68e1ad325f5dd6d2096f2e30a782f8caef059", "a8c3b3caac6baf8ac41fa581fca5350c97219bb8")])),
 # --- Phase-3 governance contradiction classes (D14/T0015; DARK; no financial posting; Phase 4 gated) ---
 ("M49 decision D14 removed while Phase 3 is IN_PROGRESS", "governance/decision-register.json",
   ("replace", [('"id": "D14"', '"id": "D14-DISABLED"')])),
 ("M50 Phase-3 plan production-runtime sentinel flipped to LIVE", "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
   ("replace", [("PHASE_3_PRODUCTION_RUNTIME: DARK", "PHASE_3_PRODUCTION_RUNTIME: LIVE")])),
 ("M51 Phase-3 privilege matrix asserts a production iam_v2 grant", "docs/architecture/Phase3-Privilege-Matrix.md",
   ("replace", [("PRODUCTION_IAM_V2_DML: NONE", "PRODUCTION_IAM_V2_DML: GRANTED")])),
 ("M52 phase3_execution.authorization_transition_id not T0015", "governance/project-state.json",
   ("replace", [('"authorization_transition_id": "T0015"', '"authorization_transition_id": "T0099"')])),
 ("M53 Phase-3 plan claims F8/F9 implemented", "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
   ("replace", [("F8/F9 NOT implemented", "F8/F9 implemented and accepted")])),
]

def apply(relpath, op):
    # binary I/O so restore is BYTE-EXACT (preserves original line endings; no CRLF<->LF drift)
    p = os.path.join(ROOT, relpath)
    with open(p, "rb") as f: orig = f.read()
    kind = op[0]
    if kind == "remove":
        os.remove(p)                       # simulate a missing required file; restore() recreates it byte-exact
        return p, orig
    text = orig.decode("utf-8")
    if kind == "replace":
        for find, repl in op[1]:
            if find not in text: raise AssertionError(f"fixture drift: '{find[:40]}...' not found in {relpath}")
            text = text.replace(find, repl)
    elif kind == "append":
        text = text + op[1]
    with open(p, "wb") as f: f.write(text.encode("utf-8"))
    return p, orig  # orig is raw bytes

def restore(p, orig):
    with open(p, "wb") as f: f.write(orig)

def main():
    print("=== baseline (good state) must PASS both validators ===")
    s0, k0 = both_status()
    if s0 != 0 or k0 != 0:
        print(f"  BASELINE FAIL: structural={s0} keyword={k0} — fix the good state before mutation testing"); return 2
    print("  baseline: structural=PASS keyword=PASS")
    print("=== mutation matrix (each must make validation FAIL non-zero) ===")
    results = []
    allok = True
    for name, relpath, op in MUTATIONS:
        p, orig = apply(relpath, op)
        try:
            s, k = both_status()
            failed = (s != 0 or k != 0)
            which = []
            if s != 0: which.append("structural")
            if k != 0: which.append("keyword")
            results.append((name, failed, ",".join(which) or "NONE"))
            allok = allok and failed
            print(f"  [{'PASS' if failed else 'MISS'}] {name:52s} -> fails: {','.join(which) or 'NONE (BAD)'}")
        finally:
            restore(p, orig)
    print("=== restored good state must PASS again ===")
    s1, k1 = both_status()
    restored_ok = (s1 == 0 and k1 == 0)
    print(f"  restored: structural={'PASS' if s1==0 else 'FAIL'} keyword={'PASS' if k1==0 else 'FAIL'}")
    print("=" * 60)
    ok = allok and restored_ok
    print("PROJECT_STATE_MUTATION_TESTS =", "PASS" if ok else "FAIL")
    return 0 if ok else 1

if __name__ == "__main__":
    sys.exit(main())
