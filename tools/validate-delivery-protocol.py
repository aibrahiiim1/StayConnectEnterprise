#!/usr/bin/env python3
"""PO-approved no-GitHub-CI delivery invariants, 2026-11-02.

This validator runs LOCALLY and never dispatches or depends on GitHub Actions.
It refuses accidental reintroduction of workflow YAML or weakening of the
recorded protected-PR policy. Tests and review evidence remain mandatory.
"""
import json
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def main():
    errors = []
    try:
        with open(os.path.join(ROOT, "governance", "branch-protection.json"), encoding="utf-8") as fh:
            spec = json.load(fh)
    except (OSError, ValueError) as exc:
        print("DELIVERY_PROTOCOL = FAIL (unreadable protection model):", exc)
        return 1
    wfdir = os.path.join(ROOT, ".github", "workflows")
    workflows = sorted(x for x in os.listdir(wfdir) if x.endswith((".yml", ".yaml"))) if os.path.isdir(wfdir) else []
    if workflows:
        errors.append("GitHub Actions workflows still exist: " + ", ".join(workflows))
    exp = spec.get("expected", {})
    if (exp.get("required_status_checks") or {}).get("contexts"):
        errors.append("CI contexts still required by protection model")
    if spec.get("workflow_job_bindings") or spec.get("full_check_gates"):
        errors.append("obsolete CI workflow bindings remain")
    if exp.get("enforcement") != "active" or exp.get("bypass_actors") != []:
        errors.append("ruleset enforcement/no-bypass guard weakened")
    if not exp.get("deletion") or not exp.get("non_fast_forward"):
        errors.append("deletion or force-push protection weakened")
    pr = exp.get("pull_request") or {}
    if pr.get("allowed_merge_methods") != ["merge"] or not pr.get("required_review_thread_resolution"):
        errors.append("PR-only merge method or thread-resolution guard weakened")
    for rel in ["tools/preflight.sh", "tools/validate-branch-protection.py",
                ".github/PULL_REQUEST_TEMPLATE.md", "docs/PO_LED_DELIVERY_MODEL.md"]:
        if not os.path.isfile(os.path.join(ROOT, rel)):
            errors.append("missing local test or PR policy file: " + rel)
    for msg in errors:
        print("  FAIL:", msg)
    if errors:
        print("DELIVERY_PROTOCOL = FAIL (%d)" % len(errors))
        return 1
    print("DELIVERY_PROTOCOL = PASS (no workflows, local tests retained, PR protections recorded)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
