#!/usr/bin/env python3
"""EVERY GATE STEP MUST BE CLASSIFIED BEFORE IT MAY BE SKIPPED.

Evidence reuse skips work on the strength of one claim: that the skipped step's verdict is a pure function of
the git tree. When reuse was first introduced that claim was assumed for every step in every gate, and it was
wrong for four of them -- transition-receipt timestamps and its self-test read the COMMIT GRAPH, the zero-stale
keyword validator checks a commit's EXISTENCE, and the production dependency gate queries the LIVE npm registry
and compares acceptances against TODAY'S DATE. Identical trees have neither the same history, nor the same
moment, nor the same external world, so those four could have reported PASS from evidence that no longer held.

The fix is not to remember better. It is to make the classification a tracked artifact that CI enforces, and to
make an unclassified step an ERROR rather than a silent inheritance of whatever guard someone typed:

    tree-pure   the verdict depends on nothing but the content of the tree, so an earlier run over an
                identical tree already answers it. MUST carry `if: ... steps.reuse.outputs.hit != 'true'`.

    always      the verdict depends on something the tree does not fix -- git history, wall-clock time, a live
                external service, pull-request context, or the artifact this run must itself produce -- or it
                is infrastructure the job cannot run without. MUST NOT reference the reuse output at all, and
                MUST record WHY in the policy.

    reuse-only  runs only when evidence was reused, to record that fact. MUST carry
                `if: steps.reuse.outputs.hit == 'true'`.

Checked in BOTH directions: a step in a workflow with no policy entry FAILS, and a policy entry naming a step
that no longer exists FAILS. Drift in either direction is the failure mode this exists to catch.

Parsed with a small line reader rather than a YAML library, because the runner is guaranteed a bare Python and
this must not depend on pyyaml being installed. The workflows are machine-written with a fixed step indent, and
the parser asserts that assumption instead of trusting it.
"""
import io
import json
import os
import re
import sys

# The root is overridable so the rule itself can be driven against fixtures by
# tools/tests/ci_reuse_policy/run_negative.py. A rule nobody has watched fail has not been shown to check
# anything, and this one exists precisely because an unwatched assumption shipped.
ROOT = os.environ.get("CI_REUSE_POLICY_ROOT") or os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
POLICY = os.path.join(ROOT, "governance", "ci-reuse-policy.json")
WFDIR = os.path.join(ROOT, ".github", "workflows")

STEP_RE = re.compile(r"^      - name: (.+?)\s*$")
GUARD_SKIP = "steps.reuse.outputs.hit != 'true'"
GUARD_ONLY = "steps.reuse.outputs.hit == 'true'"

fails = []


def fail(msg):
    fails.append(msg)
    print("  FAIL: %s" % msg)


def read(path):
    return io.open(path, encoding="utf-8").read()


def parse_steps(text):
    """[(name, body)] for every step in the single job these workflows each define."""
    steps = []
    name = None
    body = []
    for line in text.splitlines():
        m = STEP_RE.match(line)
        if m:
            if name is not None:
                steps.append((name, "\n".join(body)))
            name = m.group(1).strip()
            body = []
        elif name is not None:
            # A new top-level key at job level ends the steps block; these workflows have none after `steps:`.
            body.append(line)
    if name is not None:
        steps.append((name, "\n".join(body)))
    return steps


def main():
    if not os.path.isfile(POLICY):
        fail("governance/ci-reuse-policy.json is missing; no step may be skipped without a classification")
        print("CI_REUSE_POLICY = FAIL (1)")
        return 1
    try:
        pol = json.loads(read(POLICY))
    except Exception as exc:  # noqa: BLE001 - the message matters more than the class
        fail("governance/ci-reuse-policy.json is not readable JSON: %s" % exc)
        print("CI_REUSE_POLICY = FAIL (1)")
        return 1

    declared = pol.get("workflows") or {}
    on_disk = sorted(f for f in os.listdir(WFDIR) if f.endswith(".yml") or f.endswith(".yaml"))

    for f in on_disk:
        if f not in declared:
            fail("workflow %s exists but is not classified in the policy; every gate must be classified "
                 "before any of its steps may be skipped" % f)
    for f in declared:
        if f not in on_disk:
            fail("the policy classifies %s, which is not present in .github/workflows" % f)

    total = {"tree-pure": 0, "always": 0, "reuse-only": 0}

    for wf in on_disk:
        if wf not in declared:
            continue
        text = read(os.path.join(WFDIR, wf))
        steps = parse_steps(text)
        if not steps:
            fail("%s: no steps were parsed; the step indent is not what this validator assumes" % wf)
            continue

        entries = declared[wf].get("steps") or []
        by_name = {}
        for e in entries:
            nm = e.get("name")
            if nm in by_name:
                fail("%s: the policy lists %r twice" % (wf, nm))
            by_name[nm] = e

        seen = set()
        for name, body in steps:
            if name in seen:
                fail("%s: two steps are both named %r, so their classifications cannot be told apart"
                     % (wf, name))
                continue
            seen.add(name)

            e = by_name.get(name)
            if e is None:
                fail("%s: step %r has NO classification in governance/ci-reuse-policy.json. A step nobody "
                     "has classified must not inherit a reuse guard by accident -- classify it `tree-pure` "
                     "only if its verdict depends on nothing but the tree." % (wf, name))
                continue

            cls = e.get("reuse")
            if cls not in total:
                fail("%s: step %r has an unknown classification %r" % (wf, name, cls))
                continue
            total[cls] += 1

            has_skip = GUARD_SKIP in body
            has_only = GUARD_ONLY in body
            mentions = "steps.reuse.outputs.hit" in body

            if cls == "tree-pure":
                if not has_skip:
                    fail("%s: step %r is classified tree-pure but carries no `%s` guard, so it runs even "
                         "when its verdict was reused" % (wf, name, GUARD_SKIP))
                if has_only:
                    fail("%s: step %r is classified tree-pure but is guarded to run ONLY on a hit"
                         % (wf, name))
                # NO REUSE-ELIGIBLE STEP MAY BE A GITHUB ACTION. `actions/checkout@v4` and the setup
                # actions are moving tags: what they resolve to is not fixed by the tree, and a run cannot
                # measure the behaviour of an action it never executed. Keeping every `uses:` step
                # unconditional means action drift is always re-executed and never inherited -- which is
                # why the environment fingerprint can restrict itself to what a setup action LEAVES BEHIND
                # (the resolved toolchain version) instead of trying to identify the action itself.
                if re.search(r"^\s*uses:", body, re.M):
                    fail("%s: step %r is a GitHub action classified tree-pure. An action's version is a "
                         "moving tag the tree does not fix, so its result must never be inherited from an "
                         "earlier run; classify it `always`." % (wf, name))
            elif cls == "always":
                if mentions:
                    fail("%s: step %r is classified ALWAYS -- its verdict is not a function of the tree -- "
                         "but it references the reuse output, so a reused verdict would skip it. Reason on "
                         "record: %s" % (wf, name, e.get("why") or "(none given)"))
                if not (e.get("why") or "").strip():
                    fail("%s: step %r is classified always with no reason recorded; an unexplained "
                         "exemption is how the classification rots" % (wf, name))
            elif cls == "reuse-only":
                if not has_only:
                    fail("%s: step %r is classified reuse-only but carries no `%s` guard"
                         % (wf, name, GUARD_ONLY))

        for nm in by_name:
            if nm not in seen:
                fail("%s: the policy classifies step %r, which no longer exists in the workflow" % (wf, nm))

        # The reuse step itself, and the checkout it reads, can never be conditional on their own output.
        for name, body in steps:
            if "evidence-reuse.sh" in body and "steps.reuse.outputs.hit" in body:
                fail("%s: the reuse lookup %r is guarded by its own output" % (wf, name))

        # ---------------------------------------------------------------------------------------------
        # THE EXECUTION ENVIRONMENT MUST BE MEASURED, AND MEASURED FIRST.
        #
        # Tree equality fixes the code, not the machine. `ubuntu-latest`, `go-version: '1.25'` and
        # `node-version: '20'` all resolve at run time and can move inside any recency window, so the
        # lookup compares a fingerprint of the RESOLVED environment. That only works if the fingerprint is
        # produced on every run and produced BEFORE the decision that consumes it -- both asserted here
        # rather than left to whoever edits the workflow next.
        fp_idx = [i for i, (n, b) in enumerate(steps) if "env-fingerprint.sh" in b]
        reuse_idx = [i for i, (n, b) in enumerate(steps) if "evidence-reuse.sh" in b]
        if len(fp_idx) != 1:
            fail("%s: expected exactly one execution-environment fingerprint step, found %d. Without it "
                 "the lookup cannot show that an earlier verdict came from the same machine."
                 % (wf, len(fp_idx)))
        if len(reuse_idx) != 1:
            fail("%s: expected exactly one evidence-reuse lookup, found %d" % (wf, len(reuse_idx)))
        if len(fp_idx) == 1 and len(reuse_idx) == 1:
            if fp_idx[0] > reuse_idx[0]:
                fail("%s: the environment fingerprint is produced AFTER the reuse decision, so the decision "
                     "cannot have used it" % wf)
            fp_name, fp_body = steps[fp_idx[0]]
            if "steps.reuse.outputs.hit" in fp_body:
                fail("%s: the environment fingerprint step %r is reuse-conditional; it must run on every "
                     "run, or a hit could be decided against an unmeasured environment" % (wf, fp_name))
            reuse_name, reuse_body = steps[reuse_idx[0]]
            if "EVIDENCE_ENV_FINGERPRINT" not in reuse_body:
                fail("%s: the reuse lookup %r is not given EVIDENCE_ENV_FINGERPRINT, so it would decide "
                     "without comparing environments" % (wf, reuse_name))
            # The lookup carries offline overrides so its own self-test can drive it without a network.
            # Those are test hooks; a workflow that set one would be handing the gate its answer.
            for override in ("EVIDENCE_REUSE_RUNS_JSON", "EVIDENCE_REUSE_CANDIDATE_FP_JSON",
                             "EVIDENCE_REUSE_MAX_AGE_HOURS", "EVIDENCE_REUSE_LOOKBACK"):
                if override in reuse_body:
                    fail("%s: the reuse lookup %r sets %s. That is a self-test hook, and a gate must not "
                         "supply its own evidence or relax its own bounds." % (wf, reuse_name, override))
            # Everything before the fingerprint must be unconditional too: a toolchain installed after the
            # measurement is a toolchain the measurement does not describe.
            for n, b in steps[:fp_idx[0]]:
                if "steps.reuse.outputs.hit" in b:
                    fail("%s: step %r runs before the environment fingerprint but is reuse-conditional; "
                         "the fingerprint would then describe a different setup than a full run does"
                         % (wf, n))
            # A setup action AFTER the fingerprint installs a toolchain the fingerprint never saw.
            for n, b in steps[fp_idx[0]:]:
                if re.search(r"^\s*uses: actions/setup-", b, re.M):
                    fail("%s: %r sets up a toolchain AFTER the environment fingerprint, so that toolchain's "
                         "version is outside the key that decides whether a reused verdict is comparable"
                         % (wf, n))

    print("  classified: %d tree-pure, %d always (never skipped), %d reuse-only"
          % (total["tree-pure"], total["always"], total["reuse-only"]))
    if total["always"] == 0:
        fail("no step in any gate is classified `always`. At least the reuse lookup and the checkout it "
             "depends on cannot be reuse-conditional, so an all-tree-pure policy means the parser matched "
             "nothing real.")

    if fails:
        print("CI_REUSE_POLICY = FAIL (%d)" % len(fails))
        return 1
    print("  every gate step is classified, and every guard matches its classification")
    print("CI_REUSE_POLICY = PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
