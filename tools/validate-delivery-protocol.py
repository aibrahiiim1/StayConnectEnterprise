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


def noncomment(text):
    """The file with every whole-line comment removed.

    A CHECK MAY NEVER BE SATISFIED BY A COMMENT, and in this repository that is not a hypothetical. It has now
    happened four times in one delivery line: the ledger-completeness HEADING standing in for the ledger loop;
    a guard letting either of two ledger loops vouch for the other; the gates passing a
    "does it run assert-dispatch-head.sh" check on the strength of the explanatory comment above their dispatch
    (that script and that check are both retired now; the defect they illustrate is not)
    trigger; and this validator confirming the orchestrator proves its own rules because the file NAME appears
    in a comment eleven lines into the header.

    The shape is always the same: documentation quotes the identifier of the thing it documents, and a
    substring search cannot tell the quotation from the code. So every string check below runs against this,
    not against the raw text.
    """
    return "\n".join(l for l in text.splitlines() if not l.lstrip().startswith("#"))


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
RUNNER = "scripts/ci/nightly-orchestrate.py"
DELIVERY_TZ = "Africa/Cairo"

# The delivery model this repository is operating, read from the authoritative register rather than guessed
# from the files.
#
# THERE IS ONLY ONE ACCEPTED VALUE, AND THAT IS DELIBERATE. A second value, NIGHTLY_MODEL_LANDING, existed for
# exactly one delivery -- T0182, which put the orchestrator and the gates\' dispatch inputs on the default
# branch and so could not be validated by a mechanism that was not there yet. While it was accepted, the
# no-daytime-trigger checks below were deferred. It is deleted rather than left in place, because a transition
# value that outlives its transition is just a way to switch the checks off.
MODEL_ACTIVE = "NIGHTLY_AUTHORITATIVE_VALIDATION"


def delivery_model():
    try:
        with open(os.path.join(ROOT, "governance", "project-state.json"), encoding="utf-8") as fh:
            st = json.load(fh)
    except Exception:                                            # noqa: BLE001
        fail("governance/project-state.json could not be read, so the delivery model is unknowable")
        return None
    m = str(((st.get("current_state_facts") or {}).get("delivery_model") or "")).strip()
    if m != MODEL_ACTIVE:
        fail("current_state_facts.delivery_model is %r; the only accepted value is %r. The transitional "
             "landing value was deleted with the delivery that used it, so the no-daytime-trigger checks can "
             "no longer be deferred" % (m, MODEL_ACTIVE))
        return None
    ok("the register declares the delivery model: %s" % m)
    return m


def check_sentinel_and_rerun(name, text):
    """The gate must report a CHEAP, NON-PASSING context by day and the full gate on the nightly re-run.

    WHY THE MECHANISM IS THIS AND NOT A DISPATCH. Only a pull_request run's checks satisfy a ruleset-required
    status check. A workflow_dispatch run puts green checks with the right names, from the pinned app, on the
    pull-request head, and GitHub even associates them with the pull request -- and the ruleset still refuses
    them: `HTTP 405 ... 4 of 4 required status checks are expected`. That was measured on PR #181, not reasoned
    about, and it is why the trigger is pull_request again.

    THE COST IS REMOVED BY THE SENTINEL INSTEAD. Attempt 1 fails in seconds as the FIRST step, so the context
    exists (the rule can be evaluated) and does not pass (nothing merges on a check that validated nothing),
    and every heavy step below is skipped. The nightly re-run arrives as attempt 2+, where the gate executes.

    Each assertion below closes a way that could quietly stop being true.
    """
    code = noncomment(text)
    if not re.search(r"(?m)^\s{2}pull_request:\s*$", code):
        fail("%s has no pull_request trigger. Only a pull_request run's checks satisfy the ruleset, so without "
             "it the required context can never be earned and master is permanently unmergeable" % name)
        return
    if re.search(r"(?m)^\s{2}workflow_dispatch:\s*$", code):
        fail("%s still declares workflow_dispatch. It was retired because a dispatched run's checks do NOT "
             "satisfy a ruleset-required status check, and leaving it invites the same dead end again" % name)

    m = re.search(r"(?ms)^      - name: Daytime sentinel[^\n]*\n(.*?)(?=^      - name: )", text)
    if not m:
        fail("%s has no 'Daytime sentinel' step. Without it every daytime push runs the full gate, which is "
             "the 29-31 minute cost this model exists to remove" % name)
        return
    block = m.group(1)

    # It must be FIRST, or the heavy steps run before it and the saving is imaginary.
    steps = re.findall(r"(?m)^      - name: (.+)$", text)
    if not steps or not steps[0].startswith("Daytime sentinel"):
        fail("%s runs %r before the sentinel; the sentinel must be the FIRST step or the work it is meant to "
             "skip has already happened" % (name, steps[0] if steps else "nothing"))

    if "github.run_attempt == 1" not in block:
        fail("%s sentinel is not restricted to attempt 1, so it would also fire on the nightly re-run and the "
             "gate would never execute" % name)
    if "github.event_name == 'pull_request'" not in block:
        fail("%s sentinel is not restricted to pull_request, so a push to master would be sentinel-failed and "
             "master would carry a red required check" % name)
    if not re.search(r"(?m)^\s+exit 1\s*$", block):
        fail("%s sentinel does not fail. A PASSING daytime context would let master become mergeable on a "
             "check that validated nothing -- the one thing this must not do" % name)
    else:
        ok("%s reports a cheap NON-PASSING context by day, as the first step" % name)

    # `always()` MEANS "EVEN IF AN EARLIER STEP FAILED", AND ON THE SENTINEL ATTEMPT ONE ALWAYS DID.
    # Six steps in phase4/phase5 assemble and upload an evidence artifact under `always()`. Left alone they
    # would run on every daytime push, against a workspace nothing was checked out into, and publish an
    # artifact named as Phase evidence from a run that validated nothing. A junk artifact under an
    # authoritative name is worse than no artifact, so each one must exclude the sentinel attempt.
    stray = [c for c in re.findall(r"(?m)^\s+if: (always\(\).*)$", text)
             if "run_attempt == 1" not in c]
    if stray:
        fail("%s has %d step(s) guarded by always() that do not exclude the sentinel attempt (%s). They would "
             "run on every daytime push with nothing checked out, and publish evidence-named artifacts from a "
             "run that validated nothing" % (name, len(stray), stray[0][:60]))
    else:
        ok("%s runs nothing but the sentinel on attempt 1, always() steps included" % name)

    if "NIGHTLY_VALIDATION" not in code:
        fail("%s does not tell the evidence-reuse step when it is the authoritative attempt, so the nightly "
             "re-run could be satisfied by earlier evidence instead of executing" % name)
    elif "github.run_attempt != 1" not in code:
        fail("%s computes NIGHTLY_VALIDATION without reference to the attempt number; the authoritative run "
             "IS the later attempt, so that is what must disable reuse" % name)
    else:
        ok("%s forbids evidence reuse on the authoritative re-run attempt" % name)


def check_no_daytime_full_cycle(name, text, model):
    """A daytime push must not start the full four-gate cycle, and on push only master may run at all."""
    code = noncomment(text)
    if re.search(r"(?ms)^  push:\s*$(.*?)(?=^  \S|^\S)", code):
        blk = re.search(r"(?ms)^  push:\s*$(.*?)(?=^  \S|^\S)", code).group(1)
        branches = re.findall(r"[\[\s,]'?\"?([A-Za-z0-9_./*-]+)'?\"?", blk)
        stray = [b for b in branches if b not in ("master", "branches")]
        if stray:
            fail("%s runs a full gate on push to %s. Only master may do that; a delivery or phase branch push "
                 "would re-introduce the interruption this model removes"
                 % (name, ", ".join(sorted(set(stray)))))
        else:
            ok("%s runs on push only for master" % name)
    # The pull_request trigger is REQUIRED now (see check_sentinel_and_rerun); what must not happen is the
    # full gate running on it unconditionally. That is the sentinel's job and is asserted there.


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
        elif "pull_request.number" not in group and "github.ref" not in group:
            fail("%s concurrency group %r keys on neither the pull request nor the ref, so unrelated "
                 "deliveries would serialise against each other" % (name, group))
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
    raw = read(ORCHESTRATOR)
    if raw is None:
        fail("%s is missing; nothing would validate a delivery candidate or merge it" % ORCHESTRATOR)
        return
    # STRIPPED, because this function already passed once on a comment. M61d removed the step that runs the
    # fail-closed proofs and this check stayed quiet, because the header comment names run_negative.py while
    # explaining the DST window. The mutation was detected only after this line changed.
    text = noncomment(raw)
    crons = re.findall(r"(?m)^\s*-\s*cron:\s*'([^']+)'", text)
    if crons != ["10 3 * * *"]:
        fail("%s declares crons %r; it must declare exactly one, '10 3 * * *'. More than one firing means a "
             "deliberate no-op run every night and a session-start check that has to tell a no-op from a real "
             "verdict; none means nothing ever validates a candidate" % (ORCHESTRATOR, crons))
    else:
        ok("%s declares exactly one schedule: 03:10" % ORCHESTRATOR)
    # THE TIMEZONE IS THE WHOLE SCHEDULE. Without it the same cron means 03:10 UTC -- 05:10 or 06:10 in Cairo
    # -- and the nightly merge would run at the wrong hour while still going green, which is the kind of
    # misconfiguration that lasts for months.
    if not re.search(r"(?m)^\s*timezone:\s*[\"\']?%s[\"\']?\s*$" % re.escape(DELIVERY_TZ), text):
        fail("%s does not declare `timezone: %s` beside its cron. Without it the cron is interpreted as UTC "
             "and the nightly validation would run at 05:10 or 06:10 Cairo time instead of 03:10"
             % (ORCHESTRATOR, DELIVERY_TZ))
    else:
        ok("%s states its schedule in %s, so the platform owns the DST arithmetic" % (ORCHESTRATOR, DELIVERY_TZ))
    if "cancel-in-progress: false" not in text:
        fail("%s may be cancellable; a cancelled orchestrator can leave four re-running gates with nothing "
             "to read their verdict or merge on it" % ORCHESTRATOR)
    else:
        ok("%s is never cancelled mid-flight" % ORCHESTRATOR)
    if DECISION_TESTS not in text:
        fail("%s does not run %s before deciding anything, so the rules that can refuse a merge would go "
             "unproven on the night they are used" % (ORCHESTRATOR, DECISION_TESTS))
    else:
        ok("%s proves its own fail-closed rules before deciding anything" % ORCHESTRATOR)
    for need, why in (("actions: write", "re-running the four gates"),
                      ("contents: write", "the protected merge"),
                      ("pull-requests: write", "reading and merging the candidate")):
        if need not in text:
            fail("%s does not grant %s, which it needs for %s" % (ORCHESTRATOR, need, why))
    # HOW THE CONTEXTS ARE EARNED IS THE ONE THING THAT CANNOT DRIFT. Asserted against the RUNNER, which is
    # where the API call lives -- not against the workflow that merely schedules it.
    runner = read(RUNNER)
    if runner is None:
        fail("%s is missing; the orchestrator workflow would have nothing to run" % RUNNER)
    else:
        rcode = noncomment(runner)
        if "/rerun" not in rcode:
            fail("%s does not RE-RUN anything. A required context can only be earned by a pull_request run, "
                 "so the nightly validation must re-run each gate's existing pull_request run; a dispatch "
                 "puts green checks on the head and the ruleset still refuses them" % RUNNER)
        elif "workflows/%s/dispatches" in rcode or "/dispatches" in rcode:
            fail("%s still dispatches a gate. A dispatched run's checks do not satisfy a ruleset-required "
                 "status check, so a dispatch can only produce a run that looks authoritative and cannot "
                 "merge" % RUNNER)
        else:
            ok("%s earns the required contexts by re-running each gate's pull_request run" % RUNNER)
        if "classify_rerun_runs" not in rcode:
            fail("%s does not put its re-run results through classify_rerun_runs, so the freshness, event, "
                 "head and attempt rules would not be applied to them" % RUNNER)
    for f, why in ((DECISION_MODULE, "the decision logic every refusal comes from"),
                   (DECISION_TESTS, "the adversarial proofs of those refusals")):
        if read(f) is None:
            fail("%s is missing (%s)" % (f, why))
        else:
            ok("%s is present" % f)


def check_reuse_refuses_nightly():
    """Earlier evidence -- including a previous night's -- may never stand in for tonight's fresh run."""
    raw = read("scripts/ci/evidence-reuse.sh")
    if raw is None:
        fail("scripts/ci/evidence-reuse.sh is missing")
        return
    text = noncomment(raw)          # the refusal is explained in a comment there too
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
        check_sentinel_and_rerun(wf, text)
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
