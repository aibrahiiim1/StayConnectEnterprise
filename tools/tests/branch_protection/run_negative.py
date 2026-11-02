#!/usr/bin/env python3
"""DOES THE PROTECTION CHECK ACTUALLY NOTICE WHEN PROTECTION IS WEAKENED?

tools/validate-branch-protection.py passes against the real repository, which is also exactly what a check
that returns zero looks like. So this stands up a local HTTP server that impersonates the GitHub API, serves
a DELIBERATELY WEAKENED configuration one defect at a time, and drives the real validator against it.

The direction matters. Protection checks are not usually wrong by refusing a good repository; they are wrong
by accepting a bad one, quietly, for months. Every case below is a way master could actually be weakened --
a context dropped, strict switched off, a bypass actor added, enforcement downgraded to evaluate-only,
merge methods widened, classic protection slipped back alongside the ruleset -- plus the case where the API
cannot be reached at all, which must FAIL rather than pass for lack of evidence.

It contacts no real network, no database, no appliance, and writes nothing inside the repository.
"""
import copy
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
VALIDATOR = os.path.join(ROOT, "tools", "validate-branch-protection.py")
SPEC = os.path.join(ROOT, "governance", "branch-protection.json")
WFDIR = os.path.join(ROOT, ".github", "workflows")

passed = 0
failed = 0


def ok(msg):
    global passed
    passed += 1
    print("  [PASS] %s" % msg)


def no(msg, detail=""):
    global failed
    failed += 1
    print("  [FAIL] %s :: %s" % (msg, detail))


spec = json.loads(io.open(SPEC, encoding="utf-8").read())
EXP = spec["expected"]

# The canned "healthy" GitHub, matching governance/branch-protection.json exactly.
GOOD_RULES = [
    {"type": "deletion", "parameters": {}},
    {"type": "non_fast_forward", "parameters": {}},
    {"type": "pull_request", "parameters": {
        "required_approving_review_count": EXP["pull_request"]["required_approving_review_count"],
        "required_review_thread_resolution": True,
        "dismiss_stale_reviews_on_push": True,
        "allowed_merge_methods": ["merge"]}},
]
GOOD_SETS = [{"id": 1, "name": spec["ruleset_name"]}]
GOOD_DETAIL = {"id": 1, "name": spec["ruleset_name"], "enforcement": "active", "bypass_actors": []}
GOOD_CLASSIC = {"message": "Branch not protected"}


class State:
    rules = GOOD_RULES
    sets = GOOD_SETS
    detail = GOOD_DETAIL
    classic = GOOD_CLASSIC
    dead = False


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_GET(self):
        if State.dead:
            self.send_error(500)
            return
        if self.path.endswith("/rules/branches/master"):
            body = State.rules
        elif self.path.endswith("/rulesets"):
            body = State.sets
        elif "/rulesets/" in self.path:
            body = State.detail
        elif self.path.endswith("/protection"):
            body = State.classic
        else:
            self.send_error(404)
            return
        raw = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


srv = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=srv.serve_forever, daemon=True).start()
BASE = "http://127.0.0.1:%d" % srv.server_address[1]


def fixture():
    d = tempfile.mkdtemp(prefix="protck-")
    os.makedirs(os.path.join(d, ".github", "workflows"))
    os.makedirs(os.path.join(d, "governance"))
    if os.path.isdir(WFDIR):
        for f in os.listdir(WFDIR):
            shutil.copy(os.path.join(WFDIR, f), os.path.join(d, ".github", "workflows", f))
    shutil.copy(SPEC, os.path.join(d, "governance", "branch-protection.json"))
    return d


def run(root):
    env = dict(os.environ)
    env["BRANCH_PROTECTION_ROOT"] = root
    env["BRANCH_PROTECTION_API"] = BASE
    env.pop("GITHUB_TOKEN", None)
    p = subprocess.run([sys.executable, VALIDATOR], capture_output=True, text=True, env=env)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def reset():
    State.rules = copy.deepcopy(GOOD_RULES)
    State.sets = copy.deepcopy(GOOD_SETS)
    State.detail = copy.deepcopy(GOOD_DETAIL)
    State.classic = copy.deepcopy(GOOD_CLASSIC)
    State.dead = False


def case(title, weaken, expect):
    reset()
    d = fixture()
    try:
        rc, out = run(d)
        if rc != 0:
            no(title, "the HEALTHY canned state already failed, so this case proves nothing: %s" % out.strip())
            return
        weaken(d)
        rc, out = run(d)
        if rc == 0:
            no(title, "the validator PASSED a weakened protection state")
        elif expect not in out:
            no(title, "refused, but not for the stated reason. Output: %s" % out.strip())
        else:
            ok(title)
    finally:
        shutil.rmtree(d, ignore_errors=True)


def rule(t):
    return [r for r in State.rules if r["type"] == t][0]


# 1. Adding a CI check violates the PO's explicit no-CI decision.
def add_ci_check(_):
    State.rules.append({"type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": True,
        "required_status_checks": [{"context": "po-merge-authorization", "integration_id": 15368}]}})


case("adding a required GitHub CI status rule is refused", add_ci_check, "still requires GitHub status checks")


# 2. Even an empty required-status rule must be removed.
def add_empty_ci_rule(_):
    State.rules.append({"type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": False,
        "required_status_checks": []}})


case("adding an empty required-status rule is refused", add_empty_ci_rule, "still requires GitHub status checks")

# 3. FORCE PUSH RE-ENABLED.
def drop_nff(_):
    State.rules = [r for r in State.rules if r["type"] != "non_fast_forward"]


case("force pushes re-enabled is refused", drop_nff, "force pushes")


# 4. DELETION PROTECTION REMOVED.
def drop_deletion(_):
    State.rules = [r for r in State.rules if r["type"] != "deletion"]


case("branch deletion unblocked is refused", drop_deletion, "could be deleted")


# 5. THE PULL-REQUEST REQUIREMENT REMOVED: direct pushes to master become possible again.
def drop_pr(_):
    State.rules = [r for r in State.rules if r["type"] != "pull_request"]


case("removing the pull-request requirement is refused", drop_pr, "pushed straight to")


# 6. A BYPASS ACTOR ADDED. This is the exact hole the mission closed.
def add_bypass(_):
    State.detail["bypass_actors"] = [{"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "always"}]


case("a bypass actor added to the ruleset is refused", add_bypass, "grants bypass")


# 7. ENFORCEMENT DOWNGRADED to evaluate-only: the rules are reported but not applied.
def evaluate_only(_):
    State.detail["enforcement"] = "evaluate"


case("enforcement downgraded to evaluate-only is refused", evaluate_only, "enforcement is")


# 8. The recorded ruleset name must match the actual protecting ruleset.
def wrong_ruleset_name(d):
    p = os.path.join(d, "governance", "branch-protection.json")
    doc = json.loads(io.open(p, encoding="utf-8").read())
    doc["ruleset_name"] = "different-unprotected-ruleset"
    io.open(p, "w", encoding="utf-8").write(json.dumps(doc, indent=2) + "\n")


case("an incorrect recorded ruleset name is refused", wrong_ruleset_name, "is not present")

# 9. MERGE METHODS WIDENED. Squash/rebase rewrite the tree, breaking the validated-head-to-master link the
#    evidence-reuse design depends on.
def widen_methods(_):
    rule("pull_request")["parameters"]["allowed_merge_methods"] = ["merge", "squash", "rebase"]


case("widening merge methods beyond merge commits is refused", widen_methods, "allowed merge methods")


# 10. CONVERSATION RESOLUTION SWITCHED OFF.
def drop_convo(_):
    rule("pull_request")["parameters"]["required_review_thread_resolution"] = False


case("conversation resolution turned off is refused", drop_convo, "conversation resolution is")


# 11. THE RULESET GONE. Nothing repository-side can prevent this, but it must at least be SEEN.
def no_ruleset(_):
    State.sets = []


case("the protecting ruleset disappearing is refused", no_ruleset, "is not present")


# 12. CLASSIC PROTECTION SLIPPED BACK alongside the ruleset, which is how 'admins bypass' returns.
def readd_classic(_):
    State.classic = {"required_status_checks": {"contexts": ["governance"], "strict": True},
                     "enforce_admins": {"enabled": False}}


case("classic branch protection re-added alongside the ruleset is refused", readd_classic, "re-added")


# 13. THE API UNREACHABLE. Unverifiable is not the same as fine.
def api_dead(_):
    State.dead = True


case("an unreachable API fails closed rather than passing", api_dead, "could not be read")


# 14. A changed approving-review count is visible, not silently accepted.
def change_reviews(_):
    rule("pull_request")["parameters"]["required_approving_review_count"] = 1


case("changed required approving-review count is refused", change_reviews, "required approving reviews")



print("-" * 60)
print("BRANCH_PROTECTION_NEGATIVE pass=%d fail=%d" % (passed, failed))
srv.shutdown()
if failed:
    sys.exit(1)
print("the protection check refuses added required CI rules, weakened force-push/deletion/PR/merge/thread protections, bypass actors, inconsistent rulesets and classic overlap")
