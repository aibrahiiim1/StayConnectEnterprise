#!/usr/bin/env python3
"""NEGATIVE TESTS FOR validate-standing-records.py.

A validator that passes on a correct repository is indistinguishable from one that returns zero. Each case
below plants a REAL historical defect -- every one of these is a line that actually shipped -- runs the real
validator against the modified tree, and requires it to fail with the expected rule. Then it restores the
tree and requires a pass.

The four defects, as they were found:

  retired-host-default       deploy/scripts/phase7-final-reboot.sh defaulted PHASE7_APPLIANCE to the RETIRED
                             172.21.60.23, and that script issues a real reboot.
  onboarding-milestone       environment_scoped_iam_state.production.lifecycle read "PRE-LIVE, deployed,
                             awaiting onboarding" while appliance_enrolled/claimed/assigned/licensed were all
                             recorded true in the same file.
  central-telemetry          docs/DEPLOYMENT_CLOUD.md told an on-call engineer to "restore quorum" for a NATS
                             cluster that migration 0045 removed.
  stated-master-head         CONTINUATION.md announced "Master is at merge commit 0582eb78 (PR #120)" for
                             nine days, 52 merges late.

Run: python tools/tests/standing_records_negative.py
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
VALIDATOR = os.path.join(ROOT, "tools", "validate-standing-records.py")
STATE = os.path.join(ROOT, "governance", "project-state.json")


def run_validator():
    out = subprocess.run([sys.executable, VALIDATOR], cwd=ROOT, capture_output=True, text=True, timeout=900)
    return out.returncode, (out.stdout or "") + (out.stderr or "")


class Planted:
    """Plant a defect, restore whatever was there afterwards -- including deleting a file we created."""

    def __init__(self, relpath):
        self.path = os.path.join(ROOT, relpath)
        self.relpath = relpath
        self.backup = None
        self.existed = False

    def __enter__(self):
        self.existed = os.path.exists(self.path)
        if self.existed:
            fd, self.backup = tempfile.mkstemp(prefix="standing-records-")
            os.close(fd)
            shutil.copy2(self.path, self.backup)
        return self

    def write(self, text):
        os.makedirs(os.path.dirname(self.path), exist_ok=True)
        with open(self.path, "w", encoding="utf-8", newline="\n") as fh:
            fh.write(text)

    def append(self, text):
        with open(self.path, "a", encoding="utf-8", newline="\n") as fh:
            fh.write(text)

    def __exit__(self, *exc):
        if self.existed:
            shutil.copy2(self.backup, self.path)
            os.unlink(self.backup)
        elif os.path.exists(self.path):
            os.unlink(self.path)
        return False


FAILED = []


def expect_fail(name, rule, relpath, plant):
    """Plant ONE defect, run the real validator, require a refusal naming `rule`, then restore.

    The plant is applied INSIDE the context manager. An earlier version had each plant function enter the
    manager itself and this function enter it again: the second __enter__ backed up the already-planted
    file, so __exit__ restored the PLANT rather than removing it, and every later case ran against a tree
    carrying all the earlier defects.
    """
    with Planted(relpath) as p:
        plant(p)
        rc, out = run_validator()
    if rc == 0:
        FAILED.append(name)
        print("  FAIL  %-26s the validator PASSED on a tree carrying this defect" % name)
    elif rule not in out:
        FAILED.append(name)
        print("  FAIL  %-26s failed, but not with %s" % (name, rule))
        print("        " + "\n        ".join(l for l in out.splitlines() if "FAIL" in l)[:400])
    else:
        print("  ok    %-26s refused with %s" % (name, rule))


def plant_retired_host_default(p):
    p.write('#!/usr/bin/env bash\nAPPL="${PHASE7_APPLIANCE:-172.21.60.23}"\nssh "root@$APPL" reboot\n')


def plant_onboarding_relapse(p):
    with open(STATE, encoding="utf-8") as fh:
        d = json.load(fh)
    d["environment_scoped_iam_state"]["production"]["lifecycle"] = "PRE-LIVE, deployed, awaiting onboarding"
    with open(STATE, "w", encoding="utf-8", newline="") as fh:
        fh.write(json.dumps(d, indent=2, ensure_ascii=False) + "\n")


def plant_telemetry_repair(p):
    p.write("# Planted runbook\n\n| NATS cluster degraded | fleet view goes stale | restore quorum; "
            "edges re-drain, dedupe absorbs replays |\n")


def plant_stated_master_head(p):
    p.append("\nMaster is at merge commit `0582eb78` (PR #120), all four required gates ALL_GREEN.\n")


def main():
    print("== standing-records negative tests ==")
    rc, _ = run_validator()
    if rc != 0:
        print("  REFUSED: the tree is already failing the validator; fix that before running negative tests")
        return 2

    expect_fail("retired host as default", "retired-host-default",
                "deploy/scripts/planted-retired-default.sh", plant_retired_host_default)
    expect_fail("onboarding relapse", "onboarding-milestone-parity",
                "governance/project-state.json", plant_onboarding_relapse)
    expect_fail("telemetry as a repair", "central-telemetry-as-repair",
                "docs/PLANTED_RUNBOOK.md", plant_telemetry_repair)
    expect_fail("stale stated master head", "stated-master-head",
                "CONTINUATION.md", plant_stated_master_head)

    rc, _ = run_validator()
    if rc != 0:
        FAILED.append("restore")
        print("  FAIL  the tree did not come back clean after the plants were removed")
    else:
        print("  ok    the tree is clean again after every plant was removed")

    print("=" * 50)
    if FAILED:
        print("STANDING_RECORDS_NEGATIVE = FAIL (%d)" % len(FAILED))
        return 1
    print("STANDING_RECORDS_NEGATIVE = PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
