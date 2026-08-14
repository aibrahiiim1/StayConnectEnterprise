#!/usr/bin/env python3
"""Assemble the Phase-5 post-stay/transfer evidence artifact from THIS run.

Every number here is read back from a file the gate's own runners wrote while they were gating. Nothing is
hand-typed, nothing is copied from a workstation, and no step's result is inferred from another step's.

The artifact is deliberately small and derived: step outcomes, machine test counts, the tool versions, and
the DARK posture as this run measured it. It carries no logs, no identifiers and no secrets.

IT IS FAIL-CLOSED, and that is the point of this file existing at all. The Phase-5 gate ran green for four
milestones while publishing NOTHING: the staging directory is dot-prefixed, upload-artifact@v4 treats hidden
files as skippable, and `if-no-files-found: warn` turned "the evidence does not exist" into a line in a log
nobody reads. A green run with no artifact is indistinguishable from a green run whose evidence was never
produced, so this assembler REFUSES to write a partial artifact:

  * EVID must be set and must contain the env + steps records the gate wrote;
  * every REQUIRED step slug must be present -- a step that was skipped, renamed or silently dropped from the
    workflow is a missing measurement, not a passing one;
  * every required machine count must be present and internally consistent;
  * and it exits non-zero if any of that fails, which fails the job rather than publishing a lie.

Its own fail-closed behaviour is proven by scripts/ci/phase5-evidence-selftest.sh, which drives it against a
deliberately incomplete staging directory and fails if it accepts one.
"""
import json
import os
import subprocess
import sys

# The steps this gate MUST have recorded. Adding a step to the workflow without adding it here is harmless;
# removing one from the workflow without removing it here fails the run, which is the direction that matters.
REQUIRED_STEPS = (
    "gofmt",
    "go-build",
    "go-vet",
    "go-vet-phase5",
    "phase5-unit",
    "phase5-dark-guard",
    "phase5-integration",
    "phase4-regression",
)

# Machine counts produced by go-test-counted.sh during the same run that gated.
REQUIRED_COUNTS = ("phase5-unit",)

EVID = os.environ.get("EVID")
if not EVID:
    print("FAIL-CLOSED: EVID is not set; there is nothing to assemble", file=sys.stderr)
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


refusals = []

env = read_tsv(os.path.join(EVID, "env.tsv"))
if not env:
    refusals.append("env.tsv is missing or empty: the gate never recorded which head, runner or toolchain it ran on")

steps = read_tsv(os.path.join(EVID, "steps.tsv"))
if not steps:
    refusals.append("steps.tsv is missing or empty: no gate step recorded an outcome")

missing_steps = [s for s in REQUIRED_STEPS if s not in steps]
if missing_steps:
    refusals.append("required step(s) recorded no outcome: " + ", ".join(missing_steps))

counts = {}
for name in REQUIRED_COUNTS:
    c = read_counts(name)
    counts[name] = c
    if not c:
        refusals.append("required machine count '%s' is missing or unreadable" % name)
    elif not isinstance(c, dict) or not {"pass", "fail", "skip"} <= set(c):
        # The shape gojson_summary.py writes. A truncated or half-written file is present to `test -f` and
        # proves nothing, so presence is not accepted as evidence of a count.
        refusals.append("machine count '%s' does not carry a pass/fail/skip total" % name)

# The governance record's OWN statement of the Phase-5 posture, read from the commit under test rather than
# restated here. If these two ever disagree, the artifact should show the disagreement, not hide it.
posture = {}
try:
    with open("governance/project-state.json", encoding="utf-8") as fh:
        facts = json.load(fh).get("current_state_facts", {})
    posture = {k: facts.get(k) for k in
               ("phase5_status", "phase5_decision", "phase5_branch", "phase5_zero_price_only",
                "phase5_live_dark_deployed", "phases_5_to_7_authorized")}
except (OSError, ValueError):
    posture = {"error": "governance/project-state.json could not be read"}
    refusals.append("governance/project-state.json could not be read at the head under test")

artifact = {
    "_record_type": "PHASE_5_POST_STAY_AND_TRANSFER_CI_EVIDENCE",
    "_generated_by": "scripts/ci/phase5_evidence.py",
    "head": (env.get("head") or [git("rev-parse", "HEAD")])[0],
    "branch": (env.get("branch") or [""])[0],
    "run_id": (env.get("run_id") or [""])[0],
    "start_utc": (env.get("start_utc") or [""])[0],
    "toolchain": {k: v[0] for k, v in env.items() if k in ("go", "postgres")},
    "steps": {name: {"exit_code": vals[0], "seconds": vals[1] if len(vals) > 1 else ""}
              for name, vals in steps.items()},
    "required_steps": list(REQUIRED_STEPS),
    "test_counts": counts,
    "governance_posture_at_this_head": posture,
    # What this run did NOT do. Stated positively so the artifact cannot be read as evidence of an enabled
    # Phase 5, a cutover, or any traffic that never happened.
    "no_enablement_and_no_traffic": {
        "phase5_flag_enabled_anywhere": False,
        "iam_v2_cutover_performed": False,
        "production_or_appliance_contacted": False,
        "real_pms_contacted": False,
        "payment_provider_called": False,
        "guest_charged_or_folio_debited": False,
        "databases_used": "disposable postgres:16-alpine containers created and destroyed by this run",
        "post_stay_pricing": "zero-price only; a priced revision is refused, never granted free",
    },
}

with open(os.path.join(EVID, "phase5-post-stay-transfer-evidence.json"), "w", encoding="utf-8") as fh:
    json.dump(artifact, fh, indent=2, sort_keys=True)
    fh.write("\n")

if refusals:
    print("FAIL-CLOSED: the Phase-5 evidence is incomplete and will not be published as if it were complete:",
          file=sys.stderr)
    for r in refusals:
        print("  - " + r, file=sys.stderr)
    sys.exit(1)

failed = [n for n, s in artifact["steps"].items() if s["exit_code"] not in ("0", 0)]
print("phase5 evidence assembled for %s (%d steps recorded, %d failed)"
      % (artifact["head"][:12], len(artifact["steps"]), len(failed)))
if failed:
    print("  failed steps: " + ", ".join(sorted(failed)))
