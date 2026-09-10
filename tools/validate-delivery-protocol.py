#!/usr/bin/env python3
"""Assert the Fast Delivery and Parallel-Agent protocol from the TREE, not from prose.

WHY THIS FILE EXISTS.

The delivery of PRs #108 and #109 was safe but slow, and the time went almost entirely into failures that a
machine could have refused earlier or prevented outright:

  * Nothing stopped a superseded run. None of the four gate workflows carried a `concurrency:` key, so every
    push to a pull-request branch started a run that nothing ever cancelled. Obsolete runs for abandoned
    commits kept a runner busy and kept reporting a REQUIRED check name.

  * A required check could be satisfied by the wrong event. Every gate accepted `workflow_dispatch`, and a
    required status context IS the job name -- so a dispatched run reported the same context as the real
    pull-request run. Two things follow, and both actually happened: a dispatched run that is still in
    progress BLOCKS the merge of a pull request whose own checks are already green, and a dispatched success
    is a candidate the evidence-reuse lookup may inherit, meaning a required gate could be satisfied by a run
    that never evaluated a pull request at all.

  * The expensive gates discovered cheap problems. A Windows-pruned lockfile, a disposable-Postgres fixture
    missing columns a real migration has, and a pull-request body missing its required governance metadata are
    all knowable in seconds on a workstation. Each instead surfaced deep inside a gate, after the entire Go
    and PG16 backend had already run.

The rules below are the machine half of that protocol. The prose half lives in
docs/FAST_DELIVERY_AND_PARALLEL_AGENT_PROTOCOL.md; prose alone is what let these conditions persist, so every
statement here that CAN be checked from the tree IS checked from the tree.

NOTHING HERE WEAKENS A GATE. This validator only ever demands MORE: that a required context is reachable
solely through the event that proposes the change, that a superseded run is cancelled while a master run is
not, and that the local preflight and its documentation exist. It can refuse a delivery; it can never permit
one that the gates would otherwise refuse.
"""
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SPEC = os.path.join(ROOT, "governance", "branch-protection.json")
WORKFLOWS = os.path.join(ROOT, ".github", "workflows")

PROTOCOL_DOC = "docs/FAST_DELIVERY_AND_PARALLEL_AGENT_PROTOCOL.md"
PREFLIGHT = "tools/preflight.sh"
PR_TEMPLATE = ".github/PULL_REQUEST_TEMPLATE.md"

_failures = []


def fail(msg):
    _failures.append(msg)
    print("  FAIL: %s" % msg)


def ok(msg):
    print("  ok: %s" % msg)


def read(relpath):
    p = os.path.join(ROOT, relpath.replace("/", os.sep))
    if not os.path.isfile(p):
        return None
    with open(p, encoding="utf-8") as fh:
        return fh.read()


def gate_workflows():
    """The gate workflows, taken from the protection model rather than hard-coded here.

    Hard-coding the list would let a gate be dropped from the model and silently stop being checked by this
    file too -- the two would agree, and both would be wrong.
    """
    if not os.path.isfile(SPEC):
        fail("governance/branch-protection.json is missing; the set of required gates is unknowable")
        return {}
    with open(SPEC, encoding="utf-8") as fh:
        spec = json.load(fh)
    bindings = spec.get("workflow_job_bindings") or {}
    return {k: v for k, v in bindings.items() if not k.startswith("_")}


def check_no_dispatch(name, text):
    # `on:` may legitimately mention the words elsewhere (comments explain WHY it is absent), so match the
    # trigger key at its own indent rather than anywhere in the file.
    if re.search(r"(?m)^\s{2}workflow_dispatch:\s*$", text):
        fail("%s declares workflow_dispatch. A dispatched run reports the SAME required context as the "
             "pull-request run, so it can both block a merge and be inherited as evidence. Re-run the failed "
             "jobs of the pull_request run instead." % name)
        return
    ok("%s cannot be satisfied by workflow_dispatch" % name)


def check_pull_request_trigger(name, text):
    if not re.search(r"(?m)^\s{2}pull_request:\s*$", text):
        fail("%s has no pull_request trigger, so its required context would never report on a PR" % name)
        return
    ok("%s still reports on pull_request" % name)


def check_concurrency(name, text):
    m = re.search(r"(?m)^concurrency:\s*$\n((?:^\s+.*$\n?)+)", text)
    if not m:
        fail("%s has no top-level concurrency: block, so a superseded run is never cancelled and an obsolete "
             "run keeps reporting a required check name" % name)
        return
    block = m.group(1)

    group = re.search(r"(?m)^\s+group:\s*(.+)$", block)
    if not group:
        fail("%s has a concurrency block with no group:" % name)
    else:
        g = group.group(1)
        if "github.workflow" not in g:
            fail("%s concurrency group %r does not include github.workflow, so unrelated workflows would "
                 "cancel each other" % (name, g))
        elif not ("pull_request" in g and "github.ref" in g):
            fail("%s concurrency group %r must key on the pull request number with github.ref as the "
                 "fallback, or runs for different refs will contend" % (name, g))
        else:
            ok("%s serialises per workflow and per ref" % name)

    cip = re.search(r"(?m)^\s+cancel-in-progress:\s*(.+)$", block)
    if not cip:
        fail("%s concurrency block has no cancel-in-progress:" % name)
        return
    value = cip.group(1).strip()
    if value in ("true", "'true'", '"true"'):
        fail("%s sets cancel-in-progress unconditionally true. A push to master would then be cancellable, "
             "and master's green record is exactly what the protection rule reads -- cancelling it leaves the "
             "default branch with a check that is neither passing nor failing." % name)
    elif "github.event_name" in value and "pull_request" in value:
        ok("%s cancels superseded pull-request runs and never a master run" % name)
    else:
        fail("%s cancel-in-progress is %r; it must be an expression restricting cancellation to "
             "pull_request events" % (name, value))


def check_supporting_files():
    for relpath, why in (
        (PROTOCOL_DOC, "the authoritative Fast Delivery and Parallel-Agent protocol"),
        (PREFLIGHT, "the local preflight that reproduces the gates before a push"),
        (PR_TEMPLATE, "the pull-request template that carries the governance metadata the governance gate "
                      "validates from the live PR body"),
    ):
        if read(relpath) is None:
            fail("%s is missing (%s)" % (relpath, why))
        else:
            ok("%s is present" % relpath)


def check_protocol_registered():
    """A permanent rule that nothing points at is a rule that quietly stops being followed."""
    registry = read("governance/artifact-registry.json")
    if registry is None:
        fail("governance/artifact-registry.json is missing")
        return
    if PROTOCOL_DOC not in registry:
        fail("%s is not registered in governance/artifact-registry.json, so the Zero-Stale-Leftovers rule "
             "does not know it exists and nothing would notice it being deleted" % PROTOCOL_DOC)
    else:
        ok("%s is registered as a tracked artifact" % PROTOCOL_DOC)


def check_preflight_covers_the_late_failures():
    """The preflight must actually RUN the four things that were caught late, not merely mention them.

    Each entry names a real failure this project paid for. A marker alone is too weak: the marker string
    appears more than once in the script, so deleting the stage that does the work can leave the marker
    behind and the check still reporting green. That is precisely the "hollowed out into a stub that exits 0"
    failure this is supposed to prevent, and an early version of this validator fell for it. So each entry
    requires BOTH the marker AND the command that actually performs the check.
    """
    text = read(PREFLIGHT)
    if text is None:
        return
    required = {
        "PREFLIGHT_LINUX_NPM_CI": (
            "the Linux `npm ci` reproduction (a Windows-pruned lockfile fails only on the Linux runner)",
            ("npm ci", "node:20")),
        "PREFLIGHT_FIXTURE_PARITY": (
            "the disposable-Postgres fixture-versus-migration parity check",
            ("tools/check-fixture-parity.py",)),
        "PREFLIGHT_PR_METADATA": (
            "the pull-request governance metadata check",
            ("tools/validate-pr-metadata.sh",)),
        "PREFLIGHT_E2E_INFRA": (
            "the end-to-end server-lifecycle check",
            ("e2e-infra-reporter",)),
    }
    for token, (why, commands) in required.items():
        label = token.replace("PREFLIGHT_", "").lower().replace("_", " ")
        if token not in text:
            fail("%s does not implement %s (marker %s absent)" % (PREFLIGHT, why, token))
            continue
        missing = [c for c in commands if c not in text]
        if missing:
            fail("%s still carries the %s marker but no longer invokes %s -- the stage was removed and the "
                 "label left behind, so the check reports green while doing nothing"
                 % (PREFLIGHT, token, " / ".join(repr(m) for m in missing)))
        else:
            ok("preflight actually runs the %s check" % label)


def main():
    print("== delivery protocol: gate workflows ==")
    bindings = gate_workflows()
    if not bindings:
        fail("no gate workflows are bound in the protection model")
    for wf in sorted(bindings):
        text = read(".github/workflows/%s" % wf)
        if text is None:
            fail("%s is required to report context %r but the workflow does not exist"
                 % (wf, bindings[wf]))
            continue
        check_pull_request_trigger(wf, text)
        check_no_dispatch(wf, text)
        check_concurrency(wf, text)

    print("== delivery protocol: supporting material ==")
    check_supporting_files()
    check_protocol_registered()

    print("== delivery protocol: preflight coverage ==")
    check_preflight_covers_the_late_failures()

    print("=" * 50)
    if _failures:
        print("DELIVERY_PROTOCOL = FAIL (%d)" % len(_failures))
        return 1
    print("DELIVERY_PROTOCOL = PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
