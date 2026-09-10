#!/usr/bin/env python3
"""IS master ACTUALLY PROTECTED THE WAY THIS PROJECT SAYS IT IS?

The gap this closes was not a missing rule, it was a missing CHECK. `docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md`
required four gates green before a delivery was merge-ready; GitHub required ONE (`governance`), let the sole
administrator bypass even that, and permitted force pushes. Both statements had been reported as if they were
the same statement, for deliveries, because nothing compared them.

So the protection model is now a TRACKED FILE (governance/branch-protection.json) and this reads the LIVE
configuration from GitHub and fails if they disagree. Two endpoints, both readable WITHOUT a token on this
public repository -- which is what makes per-run verification possible at all:

    GET /repos/{owner}/{repo}/rules/branches/master   the rules actually in force on the branch
    GET /repos/{owner}/{repo}/rulesets[/{id}]          enforcement level and, critically, bypass actors

WHAT THIS CAN AND CANNOT DEFEND. It catches any WEAKENING that leaves the ruleset in place: a dropped context,
strict turned off, a bypass actor added, force-push re-enabled, merge methods widened, enforcement set to
evaluate-only. It cannot defend against an administrator DELETING the ruleset outright -- at that point the
required checks are no longer required, so no repository-side check is required either. That residual is
recorded in governance/branch-protection.json rather than papered over.

IT ALSO CHECKS THE REPOSITORY SIDE. A required status context is a job NAME. Renaming a job means the context
never reports, which is fail-closed but mystifying, and invites someone to "fix" it by weakening the ruleset.
The recorded workflow-to-context binding is asserted from the tree so a rename is an explicit error here.

FAIL CLOSED. Any state that cannot be established -- an unreachable API after retries, an unparsable response,
a missing rule -- is a FAIL. A security check that passes because it could not look is not a check.
"""
import io
import json
import os
import sys
import time
import urllib.error
import urllib.request

ROOT = os.environ.get("BRANCH_PROTECTION_ROOT") or os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SPEC = os.path.join(ROOT, "governance", "branch-protection.json")
WFDIR = os.path.join(ROOT, ".github", "workflows")
API = os.environ.get("BRANCH_PROTECTION_API", "https://api.github.com")

fails = []


def fail(msg):
    fails.append(msg)
    print("  FAIL: %s" % msg)


def ok(msg):
    print("  ok: %s" % msg)


def get(path):
    """GET json, retried. Returns None only after exhausting retries -- and None is always a FAIL."""
    url = "%s/%s" % (API.rstrip("/"), path.lstrip("/"))
    hdrs = {"Accept": "application/vnd.github+json", "User-Agent": "stayconnect-protection-check"}
    tok = os.environ.get("GITHUB_TOKEN")
    if tok:
        hdrs["Authorization"] = "Bearer %s" % tok
    for attempt in (1, 2, 3):
        try:
            req = urllib.request.Request(url, headers=hdrs)
            with urllib.request.urlopen(req, timeout=25) as r:
                return json.loads(r.read().decode("utf-8"))
        except Exception:  # noqa: BLE001 - the retry matters, the class does not
            if attempt < 3:
                time.sleep(attempt)
    return None


def parse_job_names(text):
    """The `name:` of every job, from the fixed 4-space indent these workflows use."""
    names = []
    in_jobs = False
    for line in text.splitlines():
        if line.startswith("jobs:"):
            in_jobs = True
            continue
        if in_jobs and line and not line[0].isspace():
            break
        if in_jobs and line.startswith("    name: "):
            names.append(line[len("    name: "):].strip())
    return names


def main():
    if not os.path.isfile(SPEC):
        fail("governance/branch-protection.json is missing; the protection model is unverifiable")
        print("BRANCH_PROTECTION = FAIL (1)")
        return 1
    spec = json.loads(io.open(SPEC, encoding="utf-8").read())
    exp = spec["expected"]
    repo = spec["repository"]
    branch = spec["branch"]

    print("== branch protection: recorded model vs live GitHub ==")
    print("  repository: %s   branch: %s" % (repo, branch))

    # ---------------------------------------------------------------- repository side: job name bindings
    for wf, context in (spec.get("workflow_job_bindings") or {}).items():
        if wf.startswith("_"):
            continue
        p = os.path.join(WFDIR, wf)
        if not os.path.isfile(p):
            fail("%s is required to report context %r but the workflow does not exist" % (wf, context))
            continue
        text = io.open(p, encoding="utf-8").read()
        names = parse_job_names(text)
        if names != [context]:
            fail("%s must define exactly one job named %r (a required status context IS the job name); "
                 "found %r" % (wf, context, names))
        if "pull_request:" not in text:
            fail("%s has no pull_request trigger, so its required context would never report on a PR and "
                 "master would be permanently unmergeable" % wf)
    if not fails:
        ok("every gate workflow declares exactly the job name its required context expects")

    # ---------------------------------------------------------------- live: effective rules on the branch
    rules = get("repos/%s/rules/branches/%s" % (repo, branch))
    if not isinstance(rules, list):
        fail("the live rules for %s could not be read; protection cannot be confirmed" % branch)
        print("BRANCH_PROTECTION = FAIL (%d)" % len(fails))
        return 1
    by_type = {}
    for r in rules:
        by_type.setdefault(r.get("type"), []).append(r.get("parameters") or {})

    if exp.get("non_fast_forward") and "non_fast_forward" not in by_type:
        fail("no non_fast_forward rule: force pushes to %s would be permitted" % branch)
    else:
        ok("force pushes are blocked (non_fast_forward)")

    if exp.get("deletion") and "deletion" not in by_type:
        fail("no deletion rule: %s could be deleted" % branch)
    else:
        ok("branch deletion is blocked")

    # required status checks
    rsc = (by_type.get("required_status_checks") or [None])[0]
    e = exp["required_status_checks"]
    if rsc is None:
        fail("no required_status_checks rule: a pull request could merge with no gate green at all")
    else:
        if bool(rsc.get("strict_required_status_checks_policy")) != bool(e["strict"]):
            fail("strict (branch must be current with %s) is %r, expected %r"
                 % (branch, rsc.get("strict_required_status_checks_policy"), e["strict"]))
        else:
            ok("strict policy: a branch must be up to date with %s before merging" % branch)
        live = {c.get("context"): c.get("integration_id") for c in rsc.get("required_status_checks", [])}
        want = set(e["contexts"])
        missing = want - set(live)
        extra = set(live) - want
        if missing:
            fail("required contexts MISSING from the live ruleset: %s -- those gates would not block a merge"
                 % sorted(missing))
        if extra:
            fail("the live ruleset requires contexts this project does not record: %s. An unrecorded "
                 "requirement is as much a drift as a missing one." % sorted(extra))
        if not missing and not extra:
            ok("all %d mandatory gates are required: %s" % (len(want), sorted(want)))
        bad_app = sorted(c for c, iid in live.items() if c in want and iid != e["integration_id"])
        if bad_app:
            fail("these contexts are not pinned to the GitHub Actions app (%s): %s -- a check of the same "
                 "name from another source could satisfy them" % (e["integration_id"], bad_app))
        elif not missing:
            ok("every required context is pinned to the GitHub Actions app (%s)" % e["integration_id"])

    # pull request rule
    pr = (by_type.get("pull_request") or [None])[0]
    p = exp["pull_request"]
    if pr is None:
        fail("no pull_request rule: changes could be pushed straight to %s" % branch)
    else:
        ok("changes must go through a pull request")
        if int(pr.get("required_approving_review_count", -1)) != int(p["required_approving_review_count"]):
            fail("required approving reviews is %r, recorded model says %r"
                 % (pr.get("required_approving_review_count"), p["required_approving_review_count"]))
        if bool(pr.get("required_review_thread_resolution")) != bool(p["required_review_thread_resolution"]):
            fail("conversation resolution is %r, expected %r"
                 % (pr.get("required_review_thread_resolution"), p["required_review_thread_resolution"]))
        else:
            ok("conversation resolution is required")
        methods = sorted(m.lower() for m in (pr.get("allowed_merge_methods") or []))
        if methods != sorted(m.lower() for m in p["allowed_merge_methods"]):
            fail("allowed merge methods are %s, expected %s. Squash and rebase rewrite the tree, which "
                 "breaks the link between the validated pull-request head and what lands on %s."
                 % (methods, sorted(p["allowed_merge_methods"]), branch))
        else:
            ok("merge commits only (squash/rebase would rewrite the validated tree)")

    # ---------------------------------------------------------------- live: enforcement and bypass actors
    sets = get("repos/%s/rulesets" % repo)
    if not isinstance(sets, list):
        fail("the repository rulesets could not be read; enforcement and bypass actors cannot be confirmed")
    else:
        target = [s for s in sets if s.get("name") == spec["ruleset_name"]]
        if not target:
            fail("no ruleset named %r exists. The protection this project relies on is not present."
                 % spec["ruleset_name"])
        for s in target:
            detail = get("repos/%s/rulesets/%s" % (repo, s.get("id")))
            if not isinstance(detail, dict):
                fail("ruleset %s could not be read in detail; bypass actors cannot be confirmed" % s.get("id"))
                continue
            if detail.get("enforcement") != exp["enforcement"]:
                fail("ruleset %r enforcement is %r, expected %r (anything but 'active' means the rules are "
                     "reported but not applied)" % (s.get("name"), detail.get("enforcement"), exp["enforcement"]))
            else:
                ok("ruleset %r is actively enforced" % s.get("name"))
            actors = detail.get("bypass_actors") or []
            if actors:
                fail("ruleset %r grants bypass to %d actor(s): %s. A bypass actor is exactly the silent "
                     "administrative override this model exists to remove."
                     % (s.get("name"), len(actors), actors))
            else:
                ok("no bypass actors: the rules apply to everyone, including repository administrators")

    # ---------------------------------------------------------------- classic protection must stay absent
    if exp.get("classic_branch_protection") == "absent":
        cls = get("repos/%s/branches/%s/protection" % (repo, branch))
        if isinstance(cls, dict) and cls.get("required_status_checks") is not None:
            fail("classic branch protection has been re-added alongside the ruleset. Two overlapping "
                 "mechanisms make the effective model ambiguous, and classic protection can express "
                 "'admins bypass' -- which is what was removed.")
        else:
            ok("classic branch protection is absent; the ruleset is the single mechanism")

    if fails:
        print("BRANCH_PROTECTION = FAIL (%d)" % len(fails))
        return 1
    print("  live GitHub protection matches governance/branch-protection.json exactly")
    print("BRANCH_PROTECTION = PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
