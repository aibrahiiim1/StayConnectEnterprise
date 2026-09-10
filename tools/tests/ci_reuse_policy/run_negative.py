#!/usr/bin/env python3
"""DOES THE REUSE-POLICY VALIDATOR ACTUALLY REFUSE THE MISTAKES IT EXISTS TO CATCH?

tools/validate-ci-reuse-policy.py passes on this repository, which is also exactly what a validator that
returns zero looks like. This drives the REAL validator -- the same file CI runs, never a copy -- against
fixture repositories carrying one specific defect each, and fails if any of them is allowed through.

The defects are the real ones. Case 2 is the exact mistake that shipped: a step whose verdict depends on git
history carrying a reuse guard, which is how the transition-timestamp check came to be skipped on the very
run where its inputs could differ. Case 1 is how that mistake would recur without anybody deciding to make
it -- a new step added, nobody classifying it, the guard copied from the step above.
"""
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
VALIDATOR = os.path.join(ROOT, "tools", "validate-ci-reuse-policy.py")
GUARD = "steps.reuse.outputs.hit != 'true'"

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


def fixture():
    """A copy of the real gates and the real policy, which must pass before anything is broken in it."""
    d = tempfile.mkdtemp(prefix="cireuse-")
    os.makedirs(os.path.join(d, ".github", "workflows"))
    os.makedirs(os.path.join(d, "governance"))
    for f in os.listdir(os.path.join(ROOT, ".github", "workflows")):
        shutil.copy(os.path.join(ROOT, ".github", "workflows", f),
                    os.path.join(d, ".github", "workflows", f))
    shutil.copy(os.path.join(ROOT, "governance", "ci-reuse-policy.json"),
                os.path.join(d, "governance", "ci-reuse-policy.json"))
    return d


def run(root):
    env = dict(os.environ)
    env["CI_REUSE_POLICY_ROOT"] = root
    p = subprocess.run([sys.executable, VALIDATOR], capture_output=True, text=True, env=env)
    return p.returncode, (p.stdout or "") + (p.stderr or "")


def policy_path(d):
    return os.path.join(d, "governance", "ci-reuse-policy.json")


def load(d):
    return json.loads(io.open(policy_path(d), encoding="utf-8").read())


def save(d, pol):
    io.open(policy_path(d), "w", encoding="utf-8", newline="\n").write(
        json.dumps(pol, indent=2, ensure_ascii=False) + "\n")


def wf_path(d, wf):
    return os.path.join(d, ".github", "workflows", wf)


def case(title, mutate, expect_in_output):
    d = fixture()
    try:
        rc, out = run(d)
        if rc != 0:
            no(title, "the UNMUTATED fixture already failed, so this case proves nothing: %s" % out.strip())
            return
        mutate(d)
        rc, out = run(d)
        if rc == 0:
            no(title, "the validator PASSED a policy with this defect in it")
        elif expect_in_output not in out:
            no(title, "refused, but not for the stated reason. Output: %s" % out.strip())
        else:
            ok(title)
    finally:
        shutil.rmtree(d, ignore_errors=True)


# 1. A NEW STEP NOBODY CLASSIFIED. The guard is copied from the step above, so it is silently reuse-eligible.
def add_unclassified_step(d):
    p = wf_path(d, "project-governance.yml")
    t = io.open(p, encoding="utf-8").read()
    anchor = "      - name: Structural project-state validation\n"
    assert anchor in t, "the anchor step is gone; this fixture needs updating"
    t = t.replace(anchor,
                  "      - name: A brand new check nobody classified\n"
                  "        if: %s\n"
                  "        run: echo hi\n\n" % GUARD + anchor, 1)
    io.open(p, "w", encoding="utf-8", newline="\n").write(t)


case("a NEW step with no classification is refused", add_unclassified_step, "has NO classification")


# 2. THE MISTAKE THAT SHIPPED: a step classified `always` because its inputs are not the tree, but carrying a
#    reuse guard anyway, so a reused verdict skips it.
def guard_an_always_step(d):
    p = wf_path(d, "project-governance.yml")
    t = io.open(p, encoding="utf-8").read()
    anchor = "      - name: Transition-receipt timestamps\n"
    assert anchor in t, "the transition-timestamp step is gone; this fixture needs updating"
    t = t.replace(anchor, anchor + "        if: %s\n" % GUARD, 1)
    io.open(p, "w", encoding="utf-8", newline="\n").write(t)


case("a history-dependent step that carries a reuse guard is refused",
     guard_an_always_step, "classified ALWAYS")


# 3. A step classified tree-pure but carrying no guard is a policy that has drifted from the workflow. Harmless
#    to safety, fatal to the classification being trustworthy -- if the file can lie in the safe direction it
#    can lie in the other one, and nobody would know which.
def unguard_a_tree_pure_step(d):
    p = wf_path(d, "project-governance.yml")
    t = io.open(p, encoding="utf-8").read()
    anchor = "      - name: Structural project-state validation\n        if: %s\n" % GUARD
    assert anchor in t, "the anchor step is gone; this fixture needs updating"
    t = t.replace(anchor, "      - name: Structural project-state validation\n", 1)
    io.open(p, "w", encoding="utf-8", newline="\n").write(t)


case("a tree-pure step whose guard was removed is refused", unguard_a_tree_pure_step, "carries no")


# 4. AN EXEMPTION WITH NO REASON. `always` is the safe direction, which is what makes it the easy place to
#    park a step somebody could not be bothered to think about.
def blank_the_reason(d):
    pol = load(d)
    for e in pol["workflows"]["project-governance.yml"]["steps"]:
        if e["reuse"] == "always":
            e["why"] = "   "
            break
    save(d, pol)


case("an `always` exemption with no recorded reason is refused", blank_the_reason, "no reason recorded")


# 5. A POLICY ENTRY FOR A STEP THAT NO LONGER EXISTS. Drift in the other direction: the classification is
#    stale, and a stale classification is not evidence of anything.
def rename_a_step(d):
    p = wf_path(d, "phase5-post-stay-transfer.yml")
    t = io.open(p, encoding="utf-8").read()
    t = t.replace("      - name: Phase-5 DARK guard\n", "      - name: Phase-5 DARK guard (renamed)\n", 1)
    io.open(p, "w", encoding="utf-8", newline="\n").write(t)


case("a policy entry naming a step that no longer exists is refused", rename_a_step, "no longer exists")


# 6. A WHOLE GATE ADDED WITHOUT CLASSIFICATION. A new required check must not arrive reuse-eligible by default.
def add_unclassified_workflow(d):
    io.open(wf_path(d, "brand-new-gate.yml"), "w", encoding="utf-8", newline="\n").write(
        "name: Brand New Gate\non:\n  push:\njobs:\n  g:\n    runs-on: ubuntu-latest\n"
        "    steps:\n      - name: Do a thing\n        run: echo hi\n")


case("a whole new gate with no classification is refused", add_unclassified_workflow, "is not classified")


# 7. A MISSING POLICY FILE MUST FAIL CLOSED, not degrade into "nothing to check".
def delete_policy(d):
    os.remove(policy_path(d))


case("a missing policy file fails closed", delete_policy, "is missing")


print("-" * 60)
print("CI_REUSE_POLICY_NEGATIVE pass=%d fail=%d" % (passed, failed))
if failed:
    sys.exit(1)
print("the policy validator refuses an unclassified step, a guarded history-dependent step, guard drift in "
      "both directions, an unexplained exemption, an unclassified gate, and its own absence")
