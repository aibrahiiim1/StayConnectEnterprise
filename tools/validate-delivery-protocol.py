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

UNDER D42 (T0196) the rules changed shape, not purpose. The comprehensive gates are FULL CHECK only --
dispatchable, never triggered by a pull request, a push or a clock -- and the one required context is the
Product Owner merge authorization. What is asserted below is that shape.

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
    """The FULL CHECK gate workflows, taken from the protection model rather than hard-coded here.

    Hard-coding the list would let a gate be dropped from the model and silently stop being checked by this
    file too -- the two would agree, and both would be wrong.
    """
    if not os.path.isfile(SPEC):
        fail("governance/branch-protection.json is missing; the set of FULL CHECK gates is unknowable")
        return {}
    with open(SPEC, encoding="utf-8") as fh:
        spec = json.load(fh)
    bindings = spec.get("full_check_gates") or {}
    return {k: v for k, v in bindings.items() if not k.startswith("_")}


FULL_CHECK_TOOL = "tools/full-check.py"
AUTH_WORKFLOW = "po-merge-authorization.yml"
AUTH_CONTEXT = "po-merge-authorization"
AUTH_LOGIC = "tools/po_merge_authorization.py"
AUTH_TESTS = "tools/tests/po_merge_authorization/run_negative.py"
RETIRED_ORCHESTRATOR = ".github/workflows/nightly-authoritative-validation.yml"

# The delivery model this repository is operating, read from the authoritative register rather than guessed
# from the files. ONE accepted value.
#
# HISTORY, KEPT ON PURPOSE. NIGHTLY_AUTHORITATIVE_VALIDATION (T0182..T0195) required a failing daytime sentinel
# on every pull_request and a 06:00 Africa/Cairo orchestrator that re-ran the four gates and merged unattended.
# The Product Owner retired it by D42 (T0196): normal work is implemented, tested, deployed to PRE-LIVE and
# accepted by the Product Owner, the merge waits for an explicit approval that GitHub itself enforces, and the
# comprehensive gates run only on "FULL CHECK THE WHOLE CODE". The old value is refused, so the model cannot
# be half-restored by a register edit.
MODEL_ACTIVE = "PO_LED_DELIVERY"


def delivery_model():
    try:
        with open(os.path.join(ROOT, "governance", "project-state.json"), encoding="utf-8") as fh:
            st = json.load(fh)
    except Exception:                                            # noqa: BLE001
        fail("governance/project-state.json could not be read, so the delivery model is unknowable")
        return None
    m = str(((st.get("current_state_facts") or {}).get("delivery_model") or "")).strip()
    if m != MODEL_ACTIVE:
        fail("current_state_facts.delivery_model is %r; the only accepted value is %r (D42)" % (m, MODEL_ACTIVE))
        return None
    ok("the register declares the delivery model: %s" % m)
    return m


def triggers(code):
    """Top-level event names under `on:` (two-space indent), from comment-stripped text."""
    m = re.search(r"(?ms)^on:\s*\n(.*?)(?=^\S)", code + "\nEND\n")
    return re.findall(r"(?m)^  ([a-z_]+):", m.group(1)) if m else []


def check_full_check_gate(name, text):
    """A comprehensive gate runs ONLY when FULL CHECK dispatches it: never on a pull request, a push or a clock.

    A pull_request or push trigger would make every normal change pay for the full matrix again, which D42
    removed; a schedule would bring back unattended validation. workflow_dispatch is what FULL CHECK uses, so
    without it the comprehensive path is unreachable.
    """
    code = noncomment(text)
    ev = triggers(code)
    if "workflow_dispatch" not in ev:
        fail("%s cannot be dispatched, so FULL CHECK THE WHOLE CODE could not run it (triggers: %s)" % (name, ev))
    for bad, why in (("pull_request", "every normal pull request would run the full gate automatically"),
                     ("pull_request_target", "every normal pull request would run the full gate automatically"),
                     ("push", "every merge to master would run the full gate automatically"),
                     ("schedule", "the gate would run unattended on a clock"),
                     ("workflow_run", "the gate would be chained onto another run automatically")):
        if bad in ev:
            fail("%s is triggered by %s: %s. D42 makes the comprehensive gates opt-in only" % (name, bad, why))
    if ev == ["workflow_dispatch"]:
        ok("%s runs only when FULL CHECK dispatches it" % name)
    if re.search(r"(?m)^\s+- name: Daytime sentinel", code):
        fail("%s still carries the retired daytime sentinel step" % name)
    if not re.search(r"(?m)^\s+NIGHTLY_VALIDATION:\s*'true'", code):
        fail("%s does not force fresh execution (NIGHTLY_VALIDATION: 'true'), so a FULL CHECK could be "
             "satisfied by earlier evidence instead of executing" % name)
    else:
        ok("%s executes fresh on every FULL CHECK" % name)


def check_concurrency(name, text):
    """One FULL CHECK per gate and ref, and a running one is never cancelled by the next."""
    m = re.search(r"(?m)^concurrency:\s*$\n((?:^\s+.*$\n?)+)", text)
    if not m:
        fail("%s has no top-level concurrency: block, so two FULL CHECKs of one ref could race" % name)
        return
    block = m.group(1)
    g = re.search(r"(?m)^\s+group:\s*(.+)$", block)
    if not g or "github.workflow" not in g.group(1) or "github.ref" not in g.group(1):
        fail("%s concurrency group must key on github.workflow and github.ref" % name)
    else:
        ok("%s serialises per workflow and ref" % name)
    cip = re.search(r"(?m)^\s+cancel-in-progress:\s*(.+)$", block)
    if not cip or cip.group(1).strip().lower() != "false":
        fail("%s must never cancel a running FULL CHECK (cancel-in-progress: false)" % name)
    else:
        ok("%s never cancels a running FULL CHECK" % name)


def check_no_unattended_automation():
    """Nothing in .github/workflows runs on a clock, and nothing but a human merges.

    The nightly orchestrator merged unattended at 06:00 Africa/Cairo. D42 forbids a scheduled automatic merge
    and an automatic nightly validation, so NO workflow may declare a schedule, and no workflow may call the
    merge endpoint.
    """
    before = len(_failures)
    if read(RETIRED_ORCHESTRATOR) is not None:
        fail("%s still exists; the unattended nightly validation-and-merge was retired by D42"
             % RETIRED_ORCHESTRATOR)
    for path in sorted(glob.glob(os.path.join(WORKFLOWS, "*.y*ml"))):
        rel = os.path.relpath(path, ROOT).replace(os.sep, "/")
        code = noncomment(read(rel) or "")
        if "schedule" in triggers(code) or re.search(r"(?m)^\s*-\s*cron:", code):
            fail("%s declares a schedule; nothing may run unattended on a clock (D42)" % rel)
        if re.search(r"/pulls/\S*/merge|gh pr merge|mergePullRequest", code):
            fail("%s performs a merge; only an explicit Product-Owner-authorized merge may land on master" % rel)
    if len(_failures) == before:
        ok("the nightly orchestrator is retired, no workflow runs on a schedule, and no workflow merges")


def check_merge_authorization():
    """The one required context: present, triggered by the events that can grant and revoke it, and tested."""
    raw = read(".github/workflows/%s" % AUTH_WORKFLOW)
    if raw is None:
        fail(".github/workflows/%s is missing: nothing would stop a merge before Product Owner acceptance"
             % AUTH_WORKFLOW)
        return
    code = noncomment(raw)
    ev = triggers(code)
    if ev != ["pull_request"]:
        fail("%s must be triggered by pull_request only (a pull_request run is the only kind whose check "
             "satisfies a ruleset-required context); found %s" % (AUTH_WORKFLOW, ev))
    types = re.search(r"(?m)^\s+types:\s*\[([^\]]*)\]", code)
    have = {t.strip() for t in (types.group(1).split(",") if types else [])}
    need = {"opened", "reopened", "synchronize", "labeled", "unlabeled", "ready_for_review", "converted_to_draft"}
    if not need <= have:
        fail("%s does not listen for %s; approval must be granted by a label, revoked by removing it, and "
             "revoked by any new push" % (AUTH_WORKFLOW, sorted(need - have)))
    else:
        ok("%s is re-evaluated on every event that can grant or revoke approval" % AUTH_WORKFLOW)
    if not re.search(r"(?m)^    name: %s\s*$" % AUTH_CONTEXT, code):
        fail("%s does not define the job %r that the ruleset requires" % (AUTH_WORKFLOW, AUTH_CONTEXT))
    if AUTH_LOGIC not in code or not re.search(r'(?m)^\s+python3 "\$f"\s*$', code):
        fail("%s does not run %s; the context would report without deciding anything" % (AUTH_WORKFLOW, AUTH_LOGIC))
    else:
        ok("%s runs the fail-closed decision in %s" % (AUTH_WORKFLOW, AUTH_LOGIC))
    if re.search(r"continue-on-error:\s*true", code):
        fail("%s ignores its own failure" % AUTH_WORKFLOW)
    for f in (AUTH_LOGIC, AUTH_TESTS, FULL_CHECK_TOOL):
        if read(f) is None:
            fail("%s is missing" % f)
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
    print("== delivery protocol: comprehensive gates are FULL CHECK only ==")
    bindings = gate_workflows()
    delivery_model()
    if not bindings:
        fail("no FULL CHECK gate workflows are recorded in the protection model")
    for wf in sorted(bindings):
        text = read(".github/workflows/%s" % wf)
        if text is None:
            fail("%s is recorded as a FULL CHECK gate (%r) but the workflow does not exist" % (wf, bindings[wf]))
            continue
        check_full_check_gate(wf, text)
        check_concurrency(wf, text)

    print("== delivery protocol: merge authorization and unattended automation ==")
    check_merge_authorization()
    check_no_unattended_automation()
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
