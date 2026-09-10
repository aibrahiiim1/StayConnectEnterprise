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


# 8. A REUSE-ELIGIBLE STEP THAT IS A GITHUB ACTION. An action's version is a moving tag the tree does not
#    fix, and a run cannot measure the behaviour of an action it never executed. Keeping every `uses:` step
#    unconditional is what lets the environment fingerprint restrict itself to what a setup action LEAVES
#    BEHIND rather than trying to identify the action itself.
def make_a_tree_pure_step_an_action(d):
    p = wf_path(d, "project-governance.yml")
    t = io.open(p, encoding="utf-8").read()
    old = "      - name: Structural project-state validation\n        if: %s\n        run: python tools/project-state.py validate\n" % GUARD
    assert old in t, "the anchor step is gone; this fixture needs updating"
    new = "      - name: Structural project-state validation\n        if: %s\n        uses: actions/some-action@v1\n" % GUARD
    io.open(p, "w", encoding="utf-8", newline="\n").write(t.replace(old, new, 1))


case("a reuse-eligible step that is a GitHub action is refused",
     make_a_tree_pure_step_an_action, "moving tag")


# 9. THE ENVIRONMENT FINGERPRINT REMOVED. Without it the lookup has nothing to compare machines with, and a
#    runner-image or toolchain change inside the recency window would be inherited in silence.
def remove_the_fingerprint_step(d):
    p = wf_path(d, "phase5-post-stay-transfer.yml")
    t = io.open(p, encoding="utf-8").read()
    old = "        run: bash scripts/ci/env-fingerprint.sh postgres:16-alpine\n"
    assert old in t, "the fingerprint step is gone; this fixture needs updating"
    io.open(p, "w", encoding="utf-8", newline="\n").write(t.replace(old, "        run: echo nothing\n", 1))


case("removing the execution-environment fingerprint is refused",
     remove_the_fingerprint_step, "exactly one execution-environment fingerprint")


# 10. THE FINGERPRINT MADE REUSE-CONDITIONAL, so it would be absent on exactly the runs that need it.
def guard_the_fingerprint_step(d):
    p = wf_path(d, "phase5-post-stay-transfer.yml")
    t = io.open(p, encoding="utf-8").read()
    anchor = "      - name: Execution-environment fingerprint\n"
    assert anchor in t
    io.open(p, "w", encoding="utf-8", newline="\n").write(
        t.replace(anchor, anchor + "        if: %s\n" % GUARD, 1))


case("a reuse-conditional environment fingerprint is refused",
     guard_the_fingerprint_step, "reuse-conditional")


# 11. A TOOLCHAIN SET UP AFTER THE MEASUREMENT. Its version would sit outside the key that decides whether
#     an earlier verdict is about the same machine. This is not hypothetical: Phase 4's Set up Node ran
#     after the reuse decision until this closure moved it.
def move_a_setup_after_the_fingerprint(d):
    # RELOCATE the real, already-classified Set up Go rather than inventing a step, so this case tests the
    # ordering rule and nothing else. An added step would be refused first for being unclassified, which
    # would prove case 1 over again and this one not at all.
    p = wf_path(d, "phase5-post-stay-transfer.yml")
    t = io.open(p, encoding="utf-8").read()
    setup = ("      - name: Set up Go\n        uses: actions/setup-go@v5\n        with:\n"
             "          go-version: '1.25'\n          cache-dependency-path: data-plane/go.sum\n\n")
    anchor = "      - name: Execution-environment fingerprint\n"
    assert setup in t and anchor in t, "the fixture's setup or fingerprint step has changed"
    t = t.replace(setup, "", 1)
    io.open(p, "w", encoding="utf-8", newline="\n").write(t.replace(anchor, anchor + "\n" + setup, 1))


case("a toolchain set up after the fingerprint is refused",
     move_a_setup_after_the_fingerprint, "AFTER the environment fingerprint")


# 12. THE LOOKUP NOT GIVEN THE FINGERPRINT: it would decide without ever comparing environments.
def unwire_the_fingerprint(d):
    p = wf_path(d, "project-governance.yml")
    t = io.open(p, encoding="utf-8").read()
    line = "          EVIDENCE_ENV_FINGERPRINT: ${{ steps.env.outputs.fingerprint }}\n"
    assert line in t
    io.open(p, "w", encoding="utf-8", newline="\n").write(t.replace(line, "", 1))


case("a lookup that is not given the environment fingerprint is refused",
     unwire_the_fingerprint, "not given EVIDENCE_ENV_FINGERPRINT")


print("-" * 60)
print("CI_REUSE_POLICY_NEGATIVE pass=%d fail=%d" % (passed, failed))
if failed:
    sys.exit(1)
print("the policy validator refuses an unclassified step, a guarded history-dependent step, guard drift in "
      "both directions, an unexplained exemption, an unclassified gate, its own absence, an action treated "
      "as reuse-eligible, and every way the execution-environment measurement could be removed, guarded, "
      "outrun by a later toolchain setup, or left unwired")
