#!/usr/bin/env python3
"""Local opt-in FULL CHECK after the 2026-11-02 removal of GitHub CI.

No GitHub Actions are dispatched. The local preflight alone does NOT replace
the three real integration matrices. Missing suite commands are INCOMPLETE,
not PASS. Independent review and authorized runtime proof remain separate.
"""
import argparse
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def execute(command):
    print("RUN:", " ".join(command), flush=True)
    return subprocess.run(command, cwd=ROOT, check=False).returncode == 0


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--ref", required=True)
    parser.add_argument("--phase3-command", help="Full existing local phase-3 integration suite command")
    parser.add_argument("--phase4-command", help="Full existing local financial-core integration suite command")
    parser.add_argument("--phase5-command", help="Full existing local post-stay integration suite command")
    args = parser.parse_args()
    current = subprocess.run(["git", "rev-parse", "HEAD"], cwd=ROOT, capture_output=True, text=True)
    target = subprocess.run(["git", "rev-parse", args.ref], cwd=ROOT, capture_output=True, text=True)
    if current.returncode or target.returncode or current.stdout.strip() != target.stdout.strip():
        print("FULL_CHECK = FAIL (checked-out HEAD must equal the requested ref)")
        return 1
    passed = execute(["bash", "tools/preflight.sh", "--full"])
    incomplete = False
    for suite, command in [("phase3", args.phase3_command),
                           ("phase4", args.phase4_command),
                           ("phase5", args.phase5_command)]:
        if not command:
            print("  NOT RUN: real local %s integration command missing" % suite)
            incomplete = True
            continue
        passed = execute(["bash", "-lc", command]) and passed
    if incomplete:
        print("FULL_CHECK = INCOMPLETE (no fabricated PASS; required real integration suites missing)")
        return 2
    print("FULL_CHECK = %s (LOCAL; independent review and runtime verification still required)" % ("PASS" if passed else "FAIL"))
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
