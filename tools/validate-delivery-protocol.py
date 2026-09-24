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
import glob
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


ORCHESTRATOR = ".github/workflows/nightly-authoritative-validation.yml"
DECISION_MODULE = "tools/nightly_delivery.py"
DECISION_TESTS = "tools/tests/nightly_delivery/run_negative.py"
HEAD_ASSERTION = "scripts/ci/assert-dispatch-head.sh"

# The delivery model this repository is operating, read from the authoritative register rather than guessed
# from the files. "ACTIVE" is the Product-Owner-approved nightly model in full force. "LANDING" exists for
# exactly one delivery -- the one that puts the orchestrator on the default branch, which cannot itself be
# validated by a mechanism that is not there yet -- and the delivery that flips to ACTIVE deletes it.
MODEL_ACTIVE = "NIGHTLY_AUTHORITATIVE_VALIDATION"
MODEL_LANDING = "NIGHTLY_MODEL_LANDING"


def delivery_model():
    try:
        with open(os.path.join(ROOT, "governance", "project-state.json"), encoding="utf-8") as fh:
            st = json.load(fh)
    except Exception:                                            # noqa: BLE001
        fail("governance/project-state.json could not be read, so the delivery model is unknowable")
        return None
    m = str(((st.get("current_state_facts") or {}).get("delivery_model") or "")).strip()
    if m not in (MODEL_ACTIVE, MODEL_LANDING):
        fail("current_state_facts.delivery_model is %r; it must be %r (or %r for the single landing "
             "delivery). The validator enforces what the register declares, so an undeclared model is "
             "refused rather than assumed" % (m, MODEL_ACTIVE, MODEL_LANDING))
        return None
    ok("the register declares the delivery model: %s" % m)
    return m


def check_nightly_dispatch(name, text):
    """The gate must be earnable by the nightly orchestrator, and only on stated terms."""
    if not re.search(r"(?m)^\s{2}workflow_dispatch:\s*$", text):
        fail("%s declares no workflow_dispatch trigger, so the nightly orchestrator cannot earn its "
             "required context on the delivery head at all" % name)
        return
    for inp in ("expected_sha", "correlation_id", "nightly"):
        if not re.search(r"(?m)^\s{6}%s:\s*$" % re.escape(inp), text):
            fail("%s has no workflow_dispatch input %r; without it the run cannot be tied to the commit "
                 "and the night the orchestrator decided on" % (name, inp))
    for inp in ("expected_sha", "correlation_id"):
        blk = re.search(r"(?ms)^\s{6}%s:\s*$(.*?)(?=^\s{6}\S|^\s{0,4}\S)" % re.escape(inp), text)
        if blk and not re.search(r"required:\s*true", blk.group(1)):
            fail("%s input %r is not required: true; a dispatch that omits it must not be possible"
                 % (name, inp))
    # A MENTION IS NOT A STEP, and the first version of this check accepted one. It searched the whole file
    # for the strings "assert-dispatch-head.sh" and "NIGHTLY_VALIDATION" -- both of which appear in the
    # EXPLANATORY COMMENT above the dispatch trigger. So every gate passed while carrying neither the step nor
    # the env, and the same comment also fooled the patcher that was supposed to insert them. What is required
    # now is the executable form: a `run:` that invokes the script, and an `env:` key spelled exactly.
    # COMMENTS ARE STRIPPED BEFORE ANYTHING BELOW IS ASKED, so a string that appears only in prose cannot
    # satisfy a check. That is not hypothetical tidiness: the first version searched the WHOLE FILE, both of
    # these strings appear in the explanatory comment above the dispatch trigger, and every gate therefore
    # passed while carrying neither the step nor the env. The same comment also fooled the patcher meant to
    # insert them, so the mention was the only thing that ever existed.
    code = "\n".join(l for l in text.splitlines() if not l.lstrip().startswith("#"))
    if HEAD_ASSERTION not in code:
        fail("%s does not RUN %s in any step. The string may appear in a comment, which proves nothing: "
             "without the step, a dispatch whose branch moved under it would validate the wrong commit and "
             "still report green" % (name, HEAD_ASSERTION))
    else:
        ok("%s runs the wrong-commit refusal as a step" % name)
    if not re.search(r"(?m)^\s+NIGHTLY_VALIDATION:\s", code):
        fail("%s does not pass NIGHTLY_VALIDATION as an env key to the evidence-reuse step, so the nightly "
             "run could be satisfied by earlier evidence instead of executing freshly. A mention in prose "
             "does not set an environment variable" % name)
    else:
        ok("%s forbids evidence reuse during the nightly authoritative validation" % name)
    if "run-name:" not in text:
        fail("%s has no run-name:, so the orchestrator cannot tell its own dispatch from an earlier one"
             % name)


def check_no_daytime_full_cycle(name, text, model):
    """Under the active model, a normal push must not start the full four-gate cycle."""
    if model != MODEL_ACTIVE:
        ok("%s daytime-trigger check deferred: the register declares %s" % (name, model))
        return
    if re.search(r"(?m)^\s{2}pull_request:\s*$", text):
        fail("%s still triggers on pull_request. Under %s the four full gates must not run on every push; "
             "they are dispatched once a night against the exact delivery head" % (name, MODEL_ACTIVE))
    else:
        ok("%s does not run on pull_request, so daytime pushes are not interrupted" % name)
    m = re.search(r"(?ms)^\s{2}push:\s*$(.*?)(?=^\s{2}\S|^\S)", text)
    if m:
        branches = re.findall(r"[\[\s,]'?\"?([A-Za-z0-9_./*-]+)'?\"?", m.group(1))
        stray = [b for b in branches if b not in ("master", "branches")]
        if stray:
            fail("%s triggers a full gate run on push to %s. Only master may do that; a delivery or phase "
                 "branch push would re-introduce the interruption this model removes"
                 % (name, ", ".join(sorted(set(stray)))))
        else:
            ok("%s runs on push only for master" % name)


def check_concurrency(name, text):
    """A superseded run must be cancellable; a master run and a nightly dispatch must not be.

    THIS EXISTS BECAUSE IT HAPPENED: none of the four gates carried a `concurrency:` key, so every push
    started a run that nothing ever stopped -- keeping a runner busy and reporting a REQUIRED context for a
    commit the branch had already moved past.

    Under the nightly model the same rule protects something else as well. A nightly dispatch is the ONLY
    source of the required contexts, and the orchestrator is waiting on it; cancelling one would leave the
    orchestrator unable to reach a verdict. A master push run must not be cancelled either, because master's
    green record is what the protection rule reads.
    """
    m = re.search(r"(?m)^concurrency:\s*$\n((?:^\s+.*$\n?)+)", text)
    if not m:
        fail("%s has no top-level concurrency: block, so a superseded run is never cancelled and an obsolete "
             "run can go on reporting a required context" % name)
        return
    block = m.group(1)
    g = re.search(r"(?m)^\s+group:\s*(.+)$", block)
    if not g:
        fail("%s has a concurrency block with no group:" % name)
    else:
        group = g.group(1).strip()
        if "github.workflow" not in group:
            fail("%s concurrency group %r does not include github.workflow, so unrelated workflows would "
                 "serialise against each other" % (name, group))
        elif "inputs.expected_sha" not in group:
            fail("%s concurrency group %r does not key on inputs.expected_sha. Two nights validating "
                 "different commits on the same branch would share a group, and one could cancel or queue "
                 "behind the other" % (name, group))
        else:
            ok("%s serialises per workflow and per commit under test" % name)

    cip = re.search(r"(?m)^\s+cancel-in-progress:\s*(.+)$", block)
    if not cip:
        fail("%s concurrency block has no cancel-in-progress:" % name)
        return
    value = cip.group(1).strip()
    if value.lower() == "true":
        fail("%s sets cancel-in-progress unconditionally true. A master push run would then be cancellable, "
             "and so would a nightly dispatch the orchestrator is waiting on" % name)
    elif value.lower() == "false":
        ok("%s never cancels a run" % name)
    elif "github.event_name == 'pull_request'" in value:
        ok("%s cancels only superseded pull-request runs, never a master or nightly run" % name)
    else:
        fail("%s cancel-in-progress is %r; it must be false or an expression restricting cancellation to "
             "pull_request runs" % (name, value))


def check_orchestrator():
    """The nightly orchestrator must exist, be scheduled for 03:10 Africa/Cairo, and prove its own rules."""
    text = read(ORCHESTRATOR)
    if text is None:
        fail("%s is missing; nothing would validate a delivery candidate or merge it" % ORCHESTRATOR)
        return
    crons = re.findall(r"(?m)^\s*-\s*cron:\s*'([^']+)'", text)
    if sorted(crons) != ["10 0 * * *", "10 1 * * *"]:
        fail("%s declares crons %r. It must declare BOTH '10 0 * * *' and '10 1 * * *': GitHub cron is "
             "UTC-only and Egypt moves between UTC+2 and UTC+3, so one firing per offset is the only way "
             "03:10 Africa/Cairo is hit all year. The decision module picks tonight's real firing"
             % (ORCHESTRATOR, crons))
    else:
        ok("%s fires at both 00:10Z and 01:10Z so 03:10 Africa/Cairo is hit in both halves of the year"
           % ORCHESTRATOR)
    if "cancel-in-progress: false" not in text:
        fail("%s may be cancellable; a cancelled orchestrator can leave four dispatched gates with nothing "
             "to read their verdict or merge on it" % ORCHESTRATOR)
    else:
        ok("%s is never cancelled mid-flight" % ORCHESTRATOR)
    if DECISION_TESTS not in text:
        fail("%s does not run %s before deciding anything, so the rules that can refuse a merge would go "
             "unproven on the night they are used" % (ORCHESTRATOR, DECISION_TESTS))
    else:
        ok("%s proves its own fail-closed rules before dispatching anything" % ORCHESTRATOR)
    for need, why in (("actions: write", "dispatching the four gates"),
                      ("contents: write", "the protected merge"),
                      ("pull-requests: write", "reading and merging the candidate")):
        if need not in text:
            fail("%s does not grant %s, which it needs for %s" % (ORCHESTRATOR, need, why))
    for f, why in ((DECISION_MODULE, "the decision logic every refusal comes from"),
                   (DECISION_TESTS, "the adversarial proofs of those refusals"),
                   (HEAD_ASSERTION, "the wrong-commit refusal")):
        if read(f) is None:
            fail("%s is missing (%s)" % (f, why))
        else:
            ok("%s is present" % f)


def check_reuse_refuses_nightly():
    """Earlier evidence -- including a previous night's -- may never stand in for tonight's fresh run."""
    text = read("scripts/ci/evidence-reuse.sh")
    if text is None:
        fail("scripts/ci/evidence-reuse.sh is missing")
        return
    if "NIGHTLY_VALIDATION" not in text:
        fail("scripts/ci/evidence-reuse.sh does not refuse reuse during the nightly authoritative "
             "validation. A nightly run after a failed night is the EASIEST case for reuse to hit -- same "
             "gate, identical tree, ancestry, same environment, well under 24h -- and it would skip exactly "
             "the steps the run exists to execute")
    else:
        ok("evidence reuse declines outright during the nightly authoritative validation")


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
    """The preflight must actually RUN the things that were caught late, not merely mention them.

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
        # The fifth, added after T0177 paid for it: the governance gate's mutation suite refuses to run at
        # all unless BOTH validators pass on the good state, and the keyword half ran nowhere local. A
        # one-alternative regex lag in an allowlist therefore cost a full governance cycle to discover.
        "PREFLIGHT_ZERO_STALE": (
            "the Zero-Stale keyword validator, the other half of the mutation suite's baseline",
            ("tools/validate-project-state.sh",)),
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


def check_no_gate_skips_the_receipt_timing_rule():
    """A RUNNER MUST NEVER TAKE A CALLER'S WORD FOR A CHECK.

    ZERO_STALE_RECEIPT_TIMING_ALREADY_RUN=1 tells tools/validate-project-state.sh that its receipt-timing
    invocation was already performed by the caller. That is true of tools/preflight.sh, which runs the same
    authoritative script in stage 1 and would otherwise spend eight and a half minutes running it twice.

    It is an assertion, not a check, so a gate workflow setting it would be a gate believing a claim about
    work nobody can see it do -- the same shape as satisfying a required status context with a dispatched
    run. Nothing in .github/ may set it, and this refuses the delivery if anything does.
    """
    for path in sorted(glob.glob(os.path.join(ROOT, ".github", "**", "*.yml"), recursive=True)):
        text = read(os.path.relpath(path, ROOT).replace(os.sep, "/"))
        if text and "ZERO_STALE_RECEIPT_TIMING_ALREADY_RUN" in text:
            fail("%s sets ZERO_STALE_RECEIPT_TIMING_ALREADY_RUN; a gate must run the receipt-timing rule, "
                 "not be told it was already run" % os.path.relpath(path, ROOT).replace(os.sep, "/"))
            return
    ok("no gate workflow skips the receipt-timing rule by assertion")


def main():
    print("== delivery protocol: gate workflows ==")
    bindings = gate_workflows()
    model = delivery_model()
    if not bindings:
        fail("no gate workflows are bound in the protection model")
    for wf in sorted(bindings):
        text = read(".github/workflows/%s" % wf)
        if text is None:
            fail("%s is required to report context %r but the workflow does not exist"
                 % (wf, bindings[wf]))
            continue
        check_nightly_dispatch(wf, text)
        check_no_daytime_full_cycle(wf, text, model)
        check_concurrency(wf, text)

    print("== delivery protocol: supporting material ==")
    check_orchestrator()
    check_reuse_refuses_nightly()
    check_supporting_files()
    check_protocol_registered()

    print("== delivery protocol: preflight coverage ==")
    check_preflight_covers_the_late_failures()
    check_no_gate_skips_the_receipt_timing_rule()

    print("=" * 50)
    if _failures:
        print("DELIVERY_PROTOCOL = FAIL (%d)" % len(_failures))
        return 1
    print("DELIVERY_PROTOCOL = PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
