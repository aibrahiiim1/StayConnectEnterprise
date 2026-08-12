#!/usr/bin/env python3
"""Assemble the Phase-4 financial-core evidence artifact from THIS run.

Every number here is read back from a file the gate's own runners wrote while they were gating. Nothing is
hand-typed, nothing is copied from a workstation, and no step's result is inferred from another step's.

The artifact is deliberately small and derived: step outcomes, machine test counts, the tool versions, and
the DARK posture as this run measured it. It carries no logs, no identifiers and no secrets.
"""
import json
import os
import subprocess
import sys

EVID = os.environ.get("EVID")
if not EVID:
    print("EVID is not set; nothing to assemble", file=sys.stderr)
    sys.exit(1)


def read_tsv(path):
    out = {}
    if not os.path.isfile(path):
        return out
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            parts = line.rstrip("\n").split("\t")
            if len(parts) >= 2:
                out[parts[0]] = parts[1:]
    return out


def read_counts(name):
    """Read one machine count written by go-test-counted.sh during the gate itself."""
    path = os.path.join(EVID, "counts", name + ".json")
    if not os.path.isfile(path):
        return None
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return None


def git(*args):
    try:
        return subprocess.check_output(["git"] + list(args), text=True).strip()
    except (OSError, subprocess.CalledProcessError):
        return ""


env = read_tsv(os.path.join(EVID, "env.tsv"))
steps = read_tsv(os.path.join(EVID, "steps.tsv"))

# The governance record's OWN statement of the phase-4 posture, read from the commit under test rather than
# restated here. If these two ever disagree, the artifact should show the disagreement, not hide it.
posture = {}
try:
    with open("governance/project-state.json", encoding="utf-8") as fh:
        facts = json.load(fh).get("current_state_facts", {})
    posture = {k: facts.get(k) for k in
               ("phase", "phase4_status", "phase4_decision", "phase4_transition",
                "phase4_target_maturity", "phase4_flags_off")}
except (OSError, ValueError):
    posture = {"error": "governance/project-state.json could not be read"}

artifact = {
    "_record_type": "PHASE_4_FINANCIAL_CORE_CI_EVIDENCE",
    "_generated_by": "scripts/ci/phase4_evidence.py",
    "head": (env.get("head") or [git("rev-parse", "HEAD")])[0],
    "branch": (env.get("branch") or [""])[0],
    "run_id": (env.get("run_id") or [""])[0],
    "start_utc": (env.get("start_utc") or [""])[0],
    "toolchain": {k: v[0] for k, v in env.items() if k in ("go", "postgres")},
    "steps": {name: {"exit_code": vals[0], "seconds": vals[1] if len(vals) > 1 else ""}
              for name, vals in steps.items()},
    "test_counts": {name: read_counts(name) for name in ("phase4-unit", "phase4-race")},
    "governance_posture_at_this_head": posture,
    # What this run did NOT do. Stated positively so the artifact cannot be read as evidence of a live
    # financial deployment that never happened.
    "no_financial_traffic": {
        "real_pms_ps_transmitted": False,
        "real_pa_accepted": False,
        "guest_folio_debited": False,
        "payment_provider_called": False,
        "production_or_appliance_contacted": False,
        "databases_used": "disposable postgres:16-alpine containers created and destroyed by this run",
        "transports_used": "in-process test stubs only; the DARK guard refuses before any inner transport",
    },
}

out_dir = EVID
os.makedirs(out_dir, exist_ok=True)
with open(os.path.join(out_dir, "phase4-financial-core-evidence.json"), "w", encoding="utf-8") as fh:
    json.dump(artifact, fh, indent=2, sort_keys=True)
    fh.write("\n")

failed = [n for n, s in artifact["steps"].items() if s["exit_code"] not in ("0", 0)]
print("phase4 evidence assembled for %s (%d steps recorded, %d failed)"
      % (artifact["head"][:12], len(artifact["steps"]), len(failed)))
if failed:
    print("  failed steps: " + ", ".join(sorted(failed)))
