#!/usr/bin/env python3
"""Adversarial mutation tests for the project-state governance validators.

Each mutation injects exactly one defect, runs the structural validator (tools/project-state.py validate)
and the keyword validator (tools/validate-project-state.sh), and asserts that AT LEAST ONE reports failure
(non-zero). A validator that only passes the good state without failing these negative cases is NOT accepted.

ISOLATION -- WHY THIS SUITE NO LONGER TOUCHES THE CHECKOUT.
==========================================================
It used to mutate the REAL governance/project-state.json in the active working tree and restore the original
bytes in a `finally`. That is safe only while exactly one runner exists and nothing ever interrupts it, and
neither held:

  * a run that is killed -- timeout, Ctrl-C, a CI cancellation -- never reaches its `finally`, and leaves the
    canonical state file mutated. Observed: `phases["1A"].status` left at NOT_STARTED, and
    `latest_transition_id` left at the fixture value T0008;
  * two runners overlap and each restores the bytes IT captured, so the second one's restore silently
    reverts the first one's legitimate edits. Observed twice in one round: a detached runner that outlived
    its wrapper wrote a stale project-state.json and a stale artifact-registry.json back over corrected
    content, and the corruption looked like a validator failure rather than like a test harness.

Mutating the authoritative file the whole governance model depends on, inside the working copy people are
editing, is a defect in the TOOLING. So the mutations now run in a DISPOSABLE SANDBOX:

    git clone --shared --no-checkout ROOT SANDBOX   objects are borrowed through objects/info/alternates --
                                                    nothing is copied and nothing can be written back; the
                                                    sandbox has its own index, refs and config
    copy every git-known working-tree file           so UNCOMMITTED corrections are what gets tested
    copy ROOT/.git/index                             so STAGED state is reproduced too, not just the files

FIDELITY IS PROVED, NOT ASSUMED. After building, the sandbox must satisfy two exact equalities against the
checkout, or the run aborts:

    git status --porcelain   identical  -> staged, unstaged, deleted, renamed and untracked-added all match
    git ls-files -s          identical  -> blob ids, stage numbers AND file modes (100644 vs 100755, and
                                           120000 for a symlink) all match

Those two together are the working state, not an approximation of it.

COVERAGE IS UNCHANGED. The sandbox is a real git repository, so the two git-dependent checks (SOURCE_COMMIT
existence and manifest-vs-git equality) run exactly as they do in the checkout instead of being skipped.

FAIL CLOSED. If the sandbox cannot be built or cannot be proved faithful, the suite exits non-zero instead of
falling back to the checkout.

TERMINATION, STATED PRECISELY.
  * Normal exit, exception, SIGINT and SIGTERM: the sandbox is removed by `finally` + atexit + handlers.
  * SIGKILL, or Windows TerminateProcess: NO handler can run, so THIS PROCESS CANNOT PROMISE TO CLEAN UP.
    What is guaranteed is the property that matters -- the canonical checkout is untouched either way,
    because nothing was ever written to it. The leftover sandbox is swept by an INDEPENDENT mechanism: every
    run, at startup, deletes `psmut-*` sandboxes older than SWEEP_AGE_S. That is a different process doing
    the cleanup, which is the only kind of promise a killed process can keep.

Finally, the suite verifies for itself that ROOT's governance/ directory is byte-identical before and after.
If this harness ever writes to the canonical tree again it says so, instead of leaving somebody to find out.

Run from anywhere:  python tools/tests/project_state_validator/run_mutations.py
Flags:  --require-full   fail if MUTATION_MAX_CASES is set. The authoritative CI gate passes this, so a
                         limited run can never be mistaken for the full matrix.
        --anchors        READ-ONLY. Resolve every case's anchor and exit. Seconds instead of minutes, and it
                         catches the fixture drift that otherwise aborts the matrix or silently turns a case
                         into a no-op. Run by preflight; not a substitute for the matrix.
Env:    MUTATION_MAX_CASES=N  TEST-ONLY. Runs the first N mutations. It exists so the isolation regression
                         can launch short overlapping child runs; CI leaves it unset and passes
                         --require-full.
"""
import subprocess, os, sys, shutil, json, io as _io
import atexit, hashlib, signal, tempfile, time

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))

SANDBOX_PREFIX = "psmut-"
SWEEP_AGE_S = 2 * 60 * 60      # a sandbox older than this belongs to a run that is gone

# WORK is where mutations happen. It is set to the sandbox before any mutation runs; the module-level
# default is ROOT only so the fixture anchors below can be READ, and main() refuses to proceed without a
# sandbox rather than falling back to it.
WORK = ROOT
_SANDBOX = None


def _rmtree(path):
    if path and os.path.isdir(path):
        shutil.rmtree(path, ignore_errors=True)


def _cleanup():
    global _SANDBOX
    p, _SANDBOX = _SANDBOX, None
    _rmtree(p)


def _on_signal(signum, _frame):
    _cleanup()
    os._exit(130)


atexit.register(_cleanup)
for _sig in (getattr(signal, "SIGINT", None), getattr(signal, "SIGTERM", None)):
    if _sig is not None:
        try:
            signal.signal(_sig, _on_signal)
        except (ValueError, OSError):      # not the main thread, or unsupported here
            pass


def sweep_stale_sandboxes():
    """Delete sandboxes left behind by runs that were FORCE-KILLED.

    A process killed with SIGKILL (or TerminateProcess on Windows) runs no handler and no atexit hook, so it
    cannot clean up after itself -- claiming otherwise would be false. The cleanup therefore belongs to a
    DIFFERENT process: every run sweeps old sandboxes before it builds its own. Age-based so a concurrent
    run's live sandbox is never touched.
    """
    removed = []
    base = tempfile.gettempdir()
    now = time.time()
    try:
        names = os.listdir(base)
    except OSError:
        return removed
    for name in names:
        if not name.startswith(SANDBOX_PREFIX):
            continue
        p = os.path.join(base, name)
        try:
            if os.path.isdir(p) and (now - os.path.getmtime(p)) > SWEEP_AGE_S:
                shutil.rmtree(p, ignore_errors=True)
                removed.append(name)
        except OSError:
            continue
    return removed


def _git(*args, cwd=None):
    return subprocess.run(["git", *args], cwd=cwd or ROOT, capture_output=True, text=True)


def build_sandbox():
    """A disposable, complete, writable copy of the repository, PROVED faithful. Returns (dir, repo, count)."""
    d = tempfile.mkdtemp(prefix=SANDBOX_PREFIX)
    target = os.path.join(d, "repo")
    r = _git("clone", "--shared", "--no-checkout", "--quiet", ROOT, target)
    if r.returncode != 0:
        _rmtree(d)
        raise RuntimeError("git clone --shared failed: %s" % (r.stderr or r.stdout).strip()[:300])

    # The WORKING TREE, not HEAD: this suite must test the candidate state as it stands, including
    # uncommitted corrections. Tracked + untracked-but-not-ignored is exactly "what git would show you",
    # which also keeps node_modules and other ignored bulk out of the copy. A tracked file that has been
    # DELETED in the checkout is simply not copied, and --no-checkout means the sandbox tree starts empty,
    # so the deletion is reproduced rather than papered over.
    listed = _git("ls-files", "-co", "--exclude-standard")
    if listed.returncode != 0:
        _rmtree(d)
        raise RuntimeError("git ls-files failed: %s" % listed.stderr.strip()[:300])
    n = 0
    for rel in listed.stdout.splitlines():
        rel = rel.strip()
        if not rel:
            continue
        src = os.path.join(ROOT, rel)
        if os.path.islink(src) or os.path.isfile(src):
            dst = os.path.join(target, rel.replace("/", os.sep))
            os.makedirs(os.path.dirname(dst), exist_ok=True)
            if os.path.islink(src):
                link = os.readlink(src)
                try:
                    os.symlink(link, dst)
                except (OSError, NotImplementedError):
                    with open(dst, "w", encoding="utf-8", newline="") as f:
                        f.write(link)      # how git materialises a symlink where they are unavailable
            else:
                shutil.copy2(src, dst)     # copy2 carries the mode bits
            n += 1
    if n == 0:
        _rmtree(d)
        raise RuntimeError("the sandbox copy is empty; refusing to run mutations against nothing")

    # The INDEX, so STAGED state is reproduced and not merely the file contents. The sandbox borrows ROOT's
    # object database, so every blob the index references resolves.
    src_index = os.path.join(ROOT, ".git", "index")
    dst_index = os.path.join(target, ".git", "index")
    if os.path.isfile(src_index):
        shutil.copy2(src_index, dst_index)

    if _git("rev-parse", "--git-dir", cwd=target).returncode != 0:
        _rmtree(d)
        raise RuntimeError("the sandbox is not a usable git repository")

    # ---- FIDELITY, PROVED --------------------------------------------------------------------------------
    # Two exact equalities. `status --porcelain` covers staged vs unstaged vs deleted vs renamed vs
    # untracked-added; `ls-files -s` covers blob ids, stage numbers and MODES (100644 / 100755 / 120000).
    for what, args in (("git status --porcelain", ("status", "--porcelain")),
                       ("git ls-files -s", ("ls-files", "-s"))):
        a = _git(*args)
        b = _git(*args, cwd=target)
        if a.returncode != 0 or b.returncode != 0:
            _rmtree(d)
            raise RuntimeError("fidelity check could not run (%s)" % what)
        if a.stdout.splitlines() != b.stdout.splitlines():
            only_root = sorted(set(a.stdout.splitlines()) - set(b.stdout.splitlines()))[:3]
            only_box = sorted(set(b.stdout.splitlines()) - set(a.stdout.splitlines()))[:3]
            _rmtree(d)
            raise RuntimeError("the sandbox does not reproduce the checkout (%s)\n"
                               "  only in checkout: %s\n  only in sandbox: %s" % (what, only_root, only_box))
    return d, target, n


def canonical_digest():
    """A digest of the CANONICAL governance directory, used to prove this suite never wrote to it."""
    h = hashlib.sha256()
    base = os.path.join(ROOT, "governance")
    for dirpath, dirnames, filenames in os.walk(base):
        dirnames.sort()
        for name in sorted(filenames):
            p = os.path.join(dirpath, name)
            h.update(os.path.relpath(p, ROOT).replace(os.sep, "/").encode("utf-8"))
            with open(p, "rb") as f:
                h.update(f.read())
    return h.hexdigest()

# The latest transition id changes every governance round, and M08 anchors on it. Hard-coding it meant the
# suite broke -- in CI, after the work was already done -- on T0024, T0025, T0026 and T0027 in turn. Read it
# instead: a fixture that tracks the file it mutates cannot drift out of step with it.
_STATE_DOC = json.load(_io.open(
    os.path.join(ROOT, "governance", "project-state.json"), encoding="utf-8"))
CUR_ACTIVITY = _STATE_DOC["current_activity"]
CUR_PHASE = _STATE_DOC["current_phase"]
# First 60 chars are enough to anchor uniquely without pinning the whole sentence.
CUR_NEXT_ACTION_PREFIX = _STATE_DOC["next_authorized_action"][:60]
# Derived anchors. These two sentences are rewritten on every phase advance, so pinning their exact
# wording made the suite drift silently: it kept passing its own fixtures until a run finally aborted
# on "fixture drift". Deriving them means the suite follows the authoritative state file.
# Matched on CONTENT, not on a prefix. Pinning the first words meant a legitimate rewording of the action
# ("Repository-only governance and documentation maintenance for the closed phases ...") aborted the whole
# suite with StopIteration -- a fixture failing as though the repository were broken.
# ...and it drifted AGAIN, on word order: "maintain project governance and documentation" does not contain
# the phrase "governance and documentation maintenance". Matching on the two WORDS rather than on a phrase
# removes the last ordering assumption. The explicit failure matters as much as the match: StopIteration
# aborts the whole suite with a traceback that reads like the repository is broken, when the truth is that a
# fixture anchor no longer matches -- which is a different problem with a different fix.
def _anchor(actions, *words):
    for a in actions:
        low = a.lower()
        if all(w in low for w in words):
            return a
    raise SystemExit(
        "FIXTURE ANCHOR DRIFT: no allowed_action mentions all of {}.\n"
        "  allowed_actions currently: {!r}\n"
        "  This is a TEST-FIXTURE problem, not a repository defect: the suite derives its anchors from the\n"
        "  authoritative state file so it follows rewording, and this wording moved beyond what it follows."
        .format(", ".join(words), actions))

CUR_GOV_MAINT = _anchor(_STATE_DOC["allowed_actions"], "governance", "documentation")
# A phase that is genuinely NOT the current one, used by the "two current phases" mutation to make a second.
#
# It was the first NOT_STARTED phase, and the suite aborted with "every phase is started, so the mutation has
# no target -- the case needs rewriting when it happens". It happened: D26 moved Phase 7 from NOT_STARTED to
# AUTHORIZED, so every phase in the roadmap is now started. That abort was the fixture behaving correctly --
# refusing to run a case with no target rather than reporting a pass it did not earn, which is precisely the
# failure M46 and M48 suffered silently for a whole phase.
#
# The rewrite: prefer a NOT_STARTED phase while one exists, and otherwise take the highest-numbered CLOSED
# phase. Promoting a closed phase to IN_PROGRESS is a sharper contradiction than promoting an unstarted one --
# it produces two current phases AND a phase that is simultaneously accepted and in progress -- so the case
# gets stronger as the roadmap fills up rather than expiring.
def _second_current_phase(phases):
    unstarted = sorted(k for k, v in phases.items()
                       if isinstance(v, dict) and v.get("status") == "NOT_STARTED")
    if unstarted:
        return unstarted[0]
    closed = sorted((k for k, v in phases.items()
                     if isinstance(v, dict) and str(v.get("status", "")).startswith("ACCEPTED")),
                    key=lambda k: (len(k), k))
    if closed:
        return closed[-1]
    raise SystemExit(
        "FIXTURE ANCHOR DRIFT: no phase is NOT_STARTED and none is accepted/closed, so the 'two current "
        "phases' mutation has no target at all. That is a real change in the project, not a defect.")

CUR_NOT_STARTED_PHASE = _second_current_phase(_STATE_DOC["phases"])
# The blockers sentence is rewritten on every phase closure ("... Phase 3 is ACCEPTED and CLOSED" became
# "... Phase 4 is ACCEPTED AND CLOSED"), so pinning its wording drifted the same way the other anchors did.
CUR_BLOCKER_HEAD = _STATE_DOC["blockers"][0][:48]
# Matched on WORDS through _anchor, not on a prefix. A bare next() over a startswith raises StopIteration and
# aborts the whole suite with a traceback that reads like the repository is broken -- which is exactly the
# failure mode the comments above this block were written about. The wording moved again at Phase 7
# ("Implementing work beyond the authorized Phase 7 scope"), so the anchor follows the words.
#
# ...and moved a third time at the Phase-7 CLOSURE, to "Re-executing, reopening or extending Phase 7, which is
# ACCEPTED_AND_CLOSED, and implementing any numbered development phase". That dropped "beyond" and the suite
# hard-stopped with FIXTURE ANCHOR DRIFT -- correctly: it refuses to run a case that would mutate nothing. The
# anchor now follows "implementing", which both the authorized-scope wording and the closed-phase wording use,
# so it survives the next rewording of the same prohibition instead of pinning one phase's phrasing.
CUR_PHASE_BEYOND = _anchor(_STATE_DOC["prohibited_actions"], "implementing", "phase")
CUR_TRANSITION = json.load(_io.open(
    os.path.join(ROOT, "governance", "project-state.json"), encoding="utf-8"))["latest_transition_id"]

def _find_bash():
    # Prefer Git Bash on Windows (Python's PATH 'bash' may resolve to WSL bash, which fails on Windows paths).
    for env in ("BASH", "GIT_BASH"):
        b = os.environ.get(env)
        if b and os.path.isfile(b): return b
    g = shutil.which("git")
    if g:
        for rel in ("../bin/bash.exe", "../../bin/bash.exe", "../usr/bin/bash.exe"):
            cand = os.path.normpath(os.path.join(os.path.dirname(g), rel))
            if os.path.isfile(cand): return cand
    for cand in (r"C:\Program Files\Git\bin\bash.exe", r"C:\Program Files\Git\usr\bin\bash.exe",
                 r"C:\Program Files (x86)\Git\bin\bash.exe"):
        if os.path.isfile(cand): return cand
    return shutil.which("bash") or "bash"

BASH = _find_bash()

def run(cmd):
    # cwd=WORK, never ROOT. The validators resolve their repository root from their own location,
    # so invoking the SANDBOX copies makes them read and judge the sandbox.
    return subprocess.run(cmd, cwd=WORK, capture_output=True, text=True)
def structural():
    """Every validator whose verdict is purely a function of the tree.

    This is deliberately BOTH tree-structural validators rather than just project-state.py. The delivery
    protocol (concurrency blocks, trigger hygiene, preflight coverage) is asserted from tracked files exactly
    like the project-state rules are, and a mutation that reintroduces `workflow_dispatch` on a required gate
    or deletes a concurrency block has to be DETECTED here or the mutation suite would be proving nothing
    about it. A case is detected when the tree is refused -- by whichever structural validator owns that
    rule.
    """
    rc = run([sys.executable, os.path.join(WORK, "tools", "project-state.py"), "validate"]).returncode
    if rc:
        return rc
    return run([sys.executable, os.path.join(WORK, "tools", "validate-delivery-protocol.py")]).returncode
def keyword():
    return run([BASH, os.path.join(WORK, "tools", "validate-project-state.sh")]).returncode
def both_status():
    return structural(), keyword()

# mutation = (name, relpath, op) ; op = ("replace",[(find,repl),...]) | ("append", text)

def _current_delivery_manifest_relpath():
    """The manifest tools/project-state.py currently range-checks, as a repo-relative path.

    THIS MUST TRACK THE VALIDATOR OR THE CASES BELOW STOP TESTING ANYTHING. M46 and M48 mutate a manifest and
    assert the validator notices. They named docs/manifests/Phase{current_phase}-change-manifest.md, which was
    right while a numbered phase was the current delivery.

    Under D36 it is not. Post-closure corrections are their own lineage, current_delivery names their
    manifest, and a closed phase's manifest is explicitly historical and not range-checked. Both cases went
    [MISS] the moment that landed -- not because the protection was gone, but because they were mutating a
    file nobody checks any more. A mutation test pointed at the wrong file reports safety it has not
    measured, which is worse than no test.
    """
    import io as _io
    import json as _json
    try:
        st = _json.load(_io.open(os.path.join(ROOT, "governance", "project-state.json"), encoding="utf-8"))
    except Exception:  # noqa: BLE001
        return "docs/manifests/NO-SUCH-MANIFEST.md"
    cur = (st.get("current_delivery") or {}).get("manifest")
    if cur:
        return cur.replace("\\", "/")
    phase = str(st.get("current_phase", "")).strip()
    if not phase:
        return "docs/manifests/NO-SUCH-MANIFEST.md"
    return "docs/manifests/Phase%s-change-manifest.md" % phase


CURRENT_MANIFEST_RELPATH = _current_delivery_manifest_relpath()


def _manifest_base_commit():
    """The base commit the CURRENT phase's change-manifest was generated against.

    M48 mutates that manifest's base so its path/status set stops matching git base..delivery_head. Pinning
    the value made the case silently expire the first time the manifest was regenerated against a new base:
    the search string matched nothing, the mutation became a no-op, and the suite aborted on fixture drift
    without printing a [FAIL] line -- a log that simply stopped mid-matrix.

    The manifest is resolved exactly as tools/project-state.py resolves it, so this reads the same file M46
    and M48 mutate rather than whichever manifest sorts first -- see _current_delivery_manifest_relpath.
    """
    import re as _re
    import io as _io
    path = os.path.join(ROOT, _current_delivery_manifest_relpath())
    if not os.path.exists(path):
        return "NO-MANIFEST-BASE-COMMIT-FOUND"
    txt = _io.open(path, encoding="utf-8").read()
    m = _re.search(r"\*\*Base commit:\*\*\s*`([0-9a-f]{40})`", txt)
    # A string that cannot appear, so a missing base reports drift rather than mutating something unrelated.
    return m.group(1) if m else "NO-MANIFEST-BASE-COMMIT-FOUND"


MUTATIONS = [
 ("M01 Phase 1A NOT_STARTED", "governance/project-state.json",
   ("json_set", [(["phases", "1A", "status"], "NOT_STARTED")])),
 ("M02 Phase 1A pending/planning", "governance/project-state.json",
   ("json_set", [(["phases", "1A", "status"], "PLANNING")])),
 # The phase this marks IN_PROGRESS is DERIVED: the case exists to create a SECOND concurrently-current
 # phase, and it was pinned to phase 5 -- which became the real current phase, making the mutation a no-op
 # and aborting the suite with "fixture drift: phases/5/status is already 'IN_PROGRESS'". Picking the first
 # NOT_STARTED phase recreates the intended condition whatever the project has reached.
 ("M03 two current phases", "governance/project-state.json",
   ("json_set", [(["phases", CUR_NOT_STARTED_PHASE, "status"], "IN_PROGRESS")])),
 ("M04 two next authorized actions", "governance/project-state.json",
   ("replace", [(f'"next_authorized_action": "{CUR_NEXT_ACTION_PREFIX}',
                 f'"next_authorized_action": "Also start Phase 9 now. Obtain a Product-Owner decision on the Increment-9 durability correction. {CUR_NEXT_ACTION_PREFIX}')])),
 ("M05 Phase 1B production iam_v2 grant", "docs/architecture/Phase1B-Privilege-Matrix.md",
   ("replace", [("PRODUCTION_IAM_V2_DML: NONE", "PRODUCTION_IAM_V2_DML: GRANTED")])),
 ("M06 Phase 1B rolled-back production write allowed", "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
   ("replace", [("rolled-back", "committed")])),
 ("M07 modified generated block", "docs/context/StayConnect-IAM-Handoff.md",
   ("replace", [(f"**Current phase:** {CUR_PHASE}", "**Current phase:** 9Z")])),
 ("M08 stale source commit / snapshot mismatch", "governance/project-state.json",
   ("replace", [(f'"latest_transition_id": "{CUR_TRANSITION}"', '"latest_transition_id": "T0008"')])),
 ("M09 missing acceptance record", "governance/project-state.json",
   ("replace", [('"path": "docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md"',
                 '"path": "docs/acceptance/MISSING.md"')])),
 # The registry was reformatted from one-entry-per-line to expanded JSON, so this mutation's old anchor --
 # path and status on a single line -- stopped existing and the case failed as fixture drift instead of
 # running. That is the failure mode a mutation suite exists to prevent in the code it tests, so it is worth
 # naming here: for as long as it drifted, NOTHING was proving that the validator notices the permanent rule
 # going missing. The anchor is now the path alone, which is what the mutation actually needs to change.
 ("M10 missing permanent rule", "governance/artifact-registry.json",
   ("replace", [('"path": "docs/ZERO_STALE_LEFTOVERS_RULE.md"',
                 '"path": "docs/MISSING_RULE.md"')])),
 ("M11 retained legacy item without removal gate", "governance/artifact-registry.json",
   ("replace", [('"removal_gate": "later separately-approved legacy-cleanup phase, AFTER the atomic complete-domain cutover + reconciliation"',
                 '"removal_gate": ""')])),
 ("M12 stale exported copy", "exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Handoff.md",
   ("append", "\n<!-- tampered export copy -->\n")),
 ("M13 broken pack link", "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
   ("append", "\n[dangling](this-file-does-not-exist.md)\n")),
 ("M14 pack hash mismatch", "exports/chatgpt/stayconnectenterprise/SYSTEM_OVERVIEW.md",
   ("append", "\n<!-- tamper -->\n")),
 ("M15 unmarked historical/current contradiction", "docs/context/StayConnect-IAM-Handoff.md",
   ("append", "\nPhase 1A is the current phase.\n")),
 ("M16 authoritative remote hijacked", "governance/project-state.json",
   ("replace", [("aibrahiiim1/StayConnectEnterprise.git", "attacker/Evil.git")])),
 ("M17 GH delivery decision removed", "governance/decision-register.json",
   ("replace", [('"id": "GH-SOURCE-OF-TRUTH"', '"id": "GH-SOURCE-OF-TRUTH-DISABLED"')])),
 ("M18 governance CI workflow missing", ".github/workflows/project-governance.yml",
   ("remove", None)),
 ("M19 required CI validation command removed", ".github/workflows/project-governance.yml",
   ("replace", [("python tools/project-state.py validate", "echo skip-validate")])),
 # M20 HAS NOW BEEN RETARGETED TWICE, AND THE SECOND TIME WAS A CORRECTION OF THE FIRST.
 #
 # Originally: "CI no longer runs on PRs to master", disabling `pull_request:`. T0183 removed that trigger in
 # favour of a nightly workflow_dispatch, so the anchor vanished and the case aborted the suite as fixture
 # drift; it was retargeted at the dispatch. T0184 then established by measurement that a dispatched run's
 # checks do NOT satisfy a ruleset-required status check, so `pull_request:` came back as the only trigger that
 # earns the context -- and this case comes back with it, testing exactly what it originally tested.
 #
 # Worth keeping the history visible: for the span of T0183 this case was pointed at a trigger that could not
 # earn the context at all, so it was proving the reachability of something unreachable.
 ("M20 CI no longer runs on pull requests, so its required context can never report",
  ".github/workflows/project-governance.yml",
   ("replace", [("  pull_request:\n    branches: [ master ]", "  pull_request_disabled:\n    branches: [ master ]")])),
 ("M21 CI job ignores failures", ".github/workflows/project-governance.yml",
   ("append", "\n    continue-on-error: true\n")),
 ("M22 agent-only-operations decision removed", "governance/decision-register.json",
   ("replace", [('"id": "GH-AGENT-ONLY-OPERATIONS"', '"id": "GH-AGENT-ONLY-OPERATIONS-DISABLED"')])),
 ("M23 rule flipped to require manual PO Git commands", "docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md",
   ("replace", [("GIT_OPERATIONS_OWNER: AGENT", "GIT_OPERATIONS_OWNER: PRODUCT_OWNER")])),
 ("M24 LF policy weakened (eol=lf removed)", ".gitattributes",
   ("replace", [("* text=auto eol=lf", "* text=auto")])),
 ("M25 .gitattributes missing", ".gitattributes",
   ("remove", None)),
 ("M26 plan says PLANNING ONLY while IN_PROGRESS", "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
   ("append", "\n\nStatus: PLANNING ONLY — NOT APPROVED FOR IMPLEMENTATION.\n")),
 ("M27 plan production-iam_v2 sentinel flipped", "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
   ("replace", [("PHASE_1B_PRODUCTION_IAM_V2_RUNTIME: NONE", "PHASE_1B_PRODUCTION_IAM_V2_RUNTIME: SHADOW")])),
 ("M28 plan reintroduces production iam_v2 runtime grant", "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
   ("append", "\n\n- `svc_scd` iam_v2 grants prepared for cutover: USAGE + SELECT/INSERT/UPDATE.\n")),
 # --- live-dark / acceptance stale-state contradictions (must be caught by project-state.py) ---
 ("M29 current_activity disagrees with the latest transition new_state.activity", "governance/project-state.json",
   ("replace", [(f'"current_activity": "{CUR_ACTIVITY}"',
                 '"current_activity": "PHASE_2_ACCEPTED_AND_CLOSED"')])),
 ("M30 gate_p cutover done but blocker says superuser", "governance/project-state.json",
   ("replace", [(CUR_BLOCKER_HEAD,
                 "Site-DB services still connect as superuser stayconnect and least-privilege roles are not yet applied. " + CUR_BLOCKER_HEAD)])),
 ("M31 stale 'Phase 3 not-started/unauthorized' in a current field after D14/T0015", "governance/project-state.json",
   ("replace", [(f'"{CUR_GOV_MAINT}"',
                 '"Phase 3 is NOT_STARTED and unauthorized; await explicit Product-Owner authorization"')])),
 ("M32 stale HEAD / production-unchanged in current state after T0010", "governance/project-state.json",
   # Anchored on the SHORT stable prefix. The full phrase used to end "the sole production authority", which
   # D24/T0056 corrected to "the sole CONFIGURED authentication/routing baseline" -- and this case then
   # stopped running as fixture drift, so nothing was proving that a stale HEAD / production-unchanged claim
   # is caught. The mutation only needs somewhere in a current-state string to plant the stale claim.
   ("replace", [("legacy public-schema IAM remains the sole",
                 "legacy public-schema IAM remains the sole. HEAD 1844da2 Production unchanged. Also")])),
 ("M33 phase 1B marked closed without recorded PO acceptance", "governance/project-state.json",
   ("replace", [('"transition_accepted": true', '"transition_accepted": false')])),
 ("M34 closed but evidence still says PENDING PO acceptance", "governance/project-state.json",
   ("replace", [("Product-Owner ACCEPTED_AND_CLOSED at DARK maturity via D11/T0011",
                 "reboot-validated; PENDING PO acceptance")])),
 ("M35 closed/merged but an allowed_action still says merge PR #2", "governance/project-state.json",
   ("replace", [(f'"{CUR_GOV_MAINT}"',
                 '"Merge PR #2 as governance/code delivery only"')])),
 ("M36 stale prohibition still forbids the authorized current Phase 3", "governance/project-state.json",
   ("replace", [(CUR_PHASE_BEYOND,
                 "Implementing any Phase beyond the authorized Phase 2 dark scope (Phase 3 or any later Phase)")])),
 ("M37 phase3_execution.transition_id not pointing at T0015 while in progress", "governance/project-state.json",
   ("replace", [('"transition_id": "T0015",', '"transition_id": "T0012",')])),
 # --- Zero-Stale reconciliation contradiction classes ---
 ("M38 final report claims no UI test harness after the gate records UI tests", "docs/reports/StayConnect-IAM-Phase2-Final-Report.md",
   ("append", "\n\nNote: no JS component/E2E test harness exists in hotel-admin.\n")),
 ("M39 final report presents 67 changed files as current", "docs/reports/StayConnect-IAM-Phase2-Final-Report.md",
   ("append", "\n\nThe manifest lists 67 changed files.\n")),
 ("M40 live evidence loses the current hotel-admin bundle hash", "docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md",
   ("replace", [("678c793ea46f23241eba05bde66929b19a5473fc8d3752d2a5eb083f4ff0dd95",
                 "e25126737341d8f248ae3a4589ba3a72778705a00f25b8caf6312c64a723999d")])),
 ("M41 Phase-3 plan drops the no-financial-posting sentinel", "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
   ("replace", [("PHASE_3_NO_FINANCIAL_POSTING: TRUE", "PHASE_3_NO_FINANCIAL_POSTING: FALSE")])),
 ("M42 phase1b planning pack generator drops the HISTORICAL marker", "tools/project-state.py",
   ("replace", [("PLANNING_PACK_STATUS: HISTORICAL", "PLANNING_PACK_STATUS: CURRENT")])),
 ("M43 public fingerprint reconciliation note removed (conflicting unnamed values)", "governance/project-state.json",
   ("replace", [('"public_columns_fingerprint_reconciliation"', '"public_columns_fingerprint_reconciliation_DISABLED"')])),
 ("M44 project pack source list drops the Phase-2 final report", "tools/project-state.py",
   ("replace", [('"StayConnect-IAM-Phase2-Final-Report.md": ("docs/reports/StayConnect-IAM-Phase2-Final-Report.md",None),',
                 "")])),
 # --- Phase-2 acceptance/closure + complete-manifest self-reference contradiction classes ---
 ("M45 Phase 2 accepted (transition_accepted=true) but status not ACCEPTED_AND_CLOSED", "governance/project-state.json",
   ("json_set", [(["phases", "2", "status"], "IN_PROGRESS")])),
 # M46/M48 target the manifest the VALIDATOR ACTUALLY READS, which is Phase{current_phase}. They pointed at
 # Phase 3's for as long as that was the only manifest; the moment Phase 6 published its own, mutating the
 # Phase-3 file changed nothing the validator looks at and both cases silently stopped biting -- a mutation
 # case that cannot fail is worse than no case, because it reports green.
 #
 # It happened AGAIN at the Phase-6/7 boundary, and for a better reason: the validator now correctly SKIPS the
 # path-set check for a closed phase's manifest, so once Phase 6 closed there was nothing for these two to
 # mutate until Phase 7 published its own. The fix is not to weaken them -- it is to give Phase 7 the
 # authoritative manifest they need, and repoint them at it. They must be repointed at every phase boundary,
 # and the CI failure when they are not is the mechanism that makes anyone do it.
 ("M46 change-manifest lists a path not present in git base..HEAD", CURRENT_MANIFEST_RELPATH,
   ("append", "\n| `zz-fabricated-extra-path.md` | CREATED | `A` | other | OTHER | rollback REMOVES it | fabricated |\n")),
 ("M47 acceptance decision D13 removed from the register", "governance/decision-register.json",
   ("replace", [('"id": "D13"', '"id": "D13-DISABLED"')])),
 # M48's anchor is DERIVED, not pinned. It used to hardcode the base commit that happened to be current when
 # the case was written; the moment a delivery regenerated the manifest against a different base, the search
 # string matched nothing, the mutation became a no-op, and the suite aborted on fixture drift WITHOUT
 # printing a [FAIL] line -- a green-looking log that simply stopped at M47. Reading the base out of the
 # manifest keeps the case testing what it is named for however often the manifest is regenerated.
 ("M48 manifest base repointed so its path/status set no longer equals git base..delivery_head", CURRENT_MANIFEST_RELPATH,
   ("replace", [(_manifest_base_commit(), "a8c3b3caac6baf8ac41fa581fca5350c97219bb8")])),
 # --- Phase-3 governance contradiction classes (D14/T0015; DARK; no financial posting; Phase 4 gated) ---
 ("M49 decision D14 removed while Phase 3 is IN_PROGRESS", "governance/decision-register.json",
   ("replace", [('"id": "D14"', '"id": "D14-DISABLED"')])),
 ("M50 Phase-3 plan production-runtime sentinel flipped to LIVE", "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
   ("replace", [("PHASE_3_PRODUCTION_RUNTIME: DARK", "PHASE_3_PRODUCTION_RUNTIME: LIVE")])),
 ("M51 Phase-3 privilege matrix asserts a production iam_v2 grant", "docs/architecture/Phase3-Privilege-Matrix.md",
   ("replace", [("PRODUCTION_IAM_V2_DML: NONE", "PRODUCTION_IAM_V2_DML: GRANTED")])),
 ("M52 phase3_execution.authorization_transition_id not T0015", "governance/project-state.json",
   ("replace", [('"authorization_transition_id": "T0015"', '"authorization_transition_id": "T0099"')])),
 ("M53 Phase-3 plan claims F8/F9 implemented", "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
   ("replace", [("F8/F9 NOT implemented", "F8/F9 implemented and accepted")])),
 # --- SPLIT BRAIN: corrected current_state_facts beside stale RENDERER SOURCES -------------------------------
 #
 # These five are the condition that shipped through a green gate with semantic parity reporting CLEAN: the
 # facts had been corrected, the fields the renderer actually reads had not, and six documents and three packs
 # went on rendering "sessions=3 (2 active)" and a 72-hour cache "disabled by a resync that never completed".
 # Parity could not see it because it was comparing prose against the facts that HAD been fixed.
 ("M54 live_counters do not add up (active + ended != total)", "governance/project-state.json",
   ("json_set", [(["current_state_facts", "live_counters", "sessions_active"], 2)])),
 ("M55 renderer source states an active-session count the counters deny", "governance/project-state.json",
   # RE-ANCHORED AGAIN, for the same reason and to the same effect: the accepted Session ended by itself when
   # its lease expired, so the summary now reads "ALL FOUR ENDED" and the previous anchor is gone. The case is
   # unchanged in substance - have a renderer source state an active-session count the recorded counters deny.
   # THIS IS THE MUTATION THAT MATTERED: when the counters were corrected to sessions_active 0, three prose
   # surfaces were still claiming a live Session, and this is the rule that caught them.
   ("replace", [("sessions=5 all ended", "sessions=5 with four ended and ONE ACTIVE")])),
   # RE-ANCHORED AGAIN. The counters block became a dated HISTORICAL snapshot when the PMS/counter
   # staleness defect was corrected, and the sentence lost its dashes: "sessions=5 - ALL FIVE ENDED"
   # is now "sessions=5 all ended". A replace whose anchor no longer exists ABORTS the harness, which
   # is what took the suite down after M54 rather than reporting a MISS. The case is unchanged in
   # substance: have a renderer source state an active-session count the recorded counters deny.
 ("M56 kernel records an authorization and a shaping class while no Session is active",
  "governance/project-state.json",
   # RE-AIMED AGAIN, SAME DEFECT CLASS, NOW THE OTHER DIRECTION. No Session is live and the kernel is empty,
   # so zeroing the counters is a no-op and cannot be the mutation. The contradiction to catch now is the
   # dangerous one: an authorization and a shaping class recorded in the kernel while NOTHING accounts for
   # them - a guest still enforced after every Session ended.
   ("json_set", [(["current_state_facts", "live_counters", "nft_authorizations"], 1),
                 (["current_state_facts", "live_counters", "tc_managed_classes"], 1)])),
 ("M57 a deployed capability still described as pending deployment", "governance/project-state.json",
   ("replace", [("DEPLOYED AND LIVE-VERIFIED (migration 0060",
                 "FIXED IN CODE, PENDING DEPLOYMENT at this commit (migration 0060")])),
 # --- THE TWO STALE CURRENT-STATE CONTRADICTIONS THAT SHIPPED (T0119) --------------------------------------
 #
 # Both were individually plausible sentences that nothing compared: four surfaces named PMS generation 184
 # after the feed had published 195, and the runtime head still named 29a6b21f after six binaries were
 # replaced from 480fd213. A validator that cannot fail on these is why they survived.
 ("M59 a current-state surface names a superseded PMS generation", "governance/project-state.json",
   ("json_set", [(["current_state_facts", "pms_published_generation"], 184)])),
 ("M60 the runtime head disagrees with the record carrying the per-service digests",
  "governance/project-state.json",
   ("json_set", [(["current_state_facts", "deployed_runtime_head"],
                  "29a6b21fcf69f216e90630d427b64ad956f3c6b9")])),
 # M58 IS INVERTED FROM WHAT IT WAS, because what it used to assert was the defect.
 #
 # It used to take a tree whose runtime_provenance said "SINGLE-COMMIT for every service binary, and stated
 # as one", rewrite it to say MIXED, and require validation to fail. Both halves were wrong. The appliance
 # ran binaries from FIVE commits the whole time -- so the tree this mutation started from carried a false
 # sentence, and the mutation "broke" it into the true one.
 #
 # The rule behind it was wrong in the same direction: it guarded on `len(head) >= 8`, and
 # deployed_head_on_appliance is legitimately allowed to hold the sentence "MIXED -- see
 # deployed_runtime_services; there is no single deployed head". That sentence is longer than eight
 # characters, so the rule read it as a recorded head and demanded the summary NOT say mixed -- requiring a
 # single-commit claim precisely when there was not one. It passed for months on the false wording and
 # failed the moment the wording was corrected to the truth.
 #
 # The rule now applies only when a head is actually a commit sha, and its real point is unchanged: if a
 # single deployed head IS recorded, the appliance summary may not go on denying that one exists. So the
 # mutation is the other way round -- record a real single head beside a provenance that says MIXED.
 ("M58 a single runtime head is recorded while the appliance summary still says MIXED",
  "governance/project-state.json",
   ("json_set", [(["current_state_facts", "deployed_head_on_appliance"],
                  "a4124ce595b8bc590f1eb5708bd75cc8a3579587")])),

 # ---- delivery protocol (tools/validate-delivery-protocol.py) --------------------------------------------
 # Each of these is a condition the repository was ACTUALLY IN before this rule existed, and each cost
 # measurable delivery time. They are mutations rather than prose precisely because prose is what allowed the
 # first two to persist unnoticed across all four gate workflows.
 # THE M61 FAMILY HAS TRACKED THREE DELIVERY MODELS, AND THE CHURN IS ITSELF THE LESSON.
 #
 # Under daytime-full-gates, M61 was "a gate can be satisfied by workflow_dispatch again". Under the T0183
 # nightly-dispatch model that became the REQUIRED state, so the case went [MISS]. Under T0184 -- where a
 # dispatched run's checks were measured NOT to satisfy the ruleset -- it is a defect once more, and for a
 # sharper reason than before: a dispatch trigger on a gate is an invitation back into a dead end that costs a
 # night per attempt to rediscover.
 #
 # An obsolete mutation is worse than no mutation. It fails the suite for the wrong reason and invites someone
 # to "fix" the validator until the case passes, which is how a protection gets removed by a green test.
 #
 # What these five defend is the Option E mechanism: a CHEAP NON-PASSING context by day, a FULL gate on the
 # nightly re-run, and nothing in between that could let a merge happen on a check that validated nothing.
 ("M61 a gate accepts workflow_dispatch again, reopening the dead end T0184 measured",
  ".github/workflows/phase3-software.yml",
   ("replace", [("  pull_request:\n    branches: [ master ]",
                 "  workflow_dispatch:\n  pull_request:\n    branches: [ master ]")])),
 ("M61b a gate stops forbidding evidence reuse during the nightly validation",
  ".github/workflows/phase4-financial-core.yml",
   ("replace", [("          NIGHTLY_VALIDATION: ${{ github.event_name == 'pull_request' "
                 "&& github.run_attempt != 1 }}",
                 "          NIGHTLY_VALIDATION_DISABLED: 'false'")])),
 # M61c WAS "loses the cron that covers half the year", which was the dual-cron design. With one
 # timezone-aware entry the equivalent defect is losing the TIMEZONE: the same cron then means 03:10 UTC,
 # which is 05:10 or 06:10 in Cairo, and the nightly merge runs at the wrong hour while still going green.
 ("M61c the nightly orchestrator loses the timezone that makes its cron mean 03:10 Cairo",
  ".github/workflows/nightly-authoritative-validation.yml",
   ("replace", [('      timezone: "Africa/Cairo"', "      # timezone removed")])),
 ("M61d the nightly orchestrator stops proving its own fail-closed rules before merging",
  ".github/workflows/nightly-authoritative-validation.yml",
   ("replace", [("        run: python tools/tests/nightly_delivery/run_negative.py",
                 "        run: true  # proofs skipped")])),
 ("M61e the register declares a delivery model nobody enforces",
  "governance/project-state.json",
   ("json_set", [(["current_state_facts", "delivery_model"], "SOMETHING_ELSE")])),
 # THE SINGLE WORST DEFECT THIS MODEL CAN HAVE, so it gets its own case. M61f used to be "a gate starts
 # running on pull_request again"; under Option E pull_request is REQUIRED, and the equivalent damage is the
 # sentinel that PASSES. A passing daytime context makes all four required checks green within a minute of a
 # push, on attempts that validated nothing -- so the ruleset is satisfied and master becomes mergeable
 # BEFORE any authoritative validation has run. Every other protection in this delivery is downstream of the
 # sentinel failing.
 ("M61f the daytime sentinel passes, so master becomes mergeable on checks that validated nothing",
  ".github/workflows/phase5-post-stay-transfer.yml",
   ("replace", [('reports what the last authoritative night decided."\n          exit 1',
                 'reports what the last authoritative night decided."\n          exit 0')])),
 # AND THE OTHER WAY THE SENTINEL CAN STOP WORKING: not by passing, but by never standing aside. Restricted to
 # the wrong attempt it fires on the nightly re-run too, so the gates never execute at all and the nightly
 # validation can only ever refuse. That fails safe -- nothing merges -- but it means the delivery path is
 # silently dead, which nobody discovers until a release is wanted.
 ("M61g the sentinel is not confined to attempt 1, so the nightly re-run never executes the gate",
  ".github/workflows/project-governance.yml",
   ("replace", [("        if: github.event_name == 'pull_request' && github.run_attempt == 1",
                 "        if: github.event_name == 'pull_request'")])),
 ("M62 a superseded run is never cancelled (concurrency block removed)",
  ".github/workflows/phase4-financial-core.yml",
   ("replace", [("concurrency:\n  group:", "removed_concurrency:\n  group:")])),
 ("M63 cancel-in-progress made unconditional, so a master run becomes cancellable",
  ".github/workflows/phase5-post-stay-transfer.yml",
   ("replace", [("cancel-in-progress: ${{ github.event_name == 'pull_request' }}",
                 "cancel-in-progress: true")])),
 ("M64 the preflight hollowed out into a stub that still exits 0",
  "tools/preflight.sh",
   ("replace", [("python tools/check-fixture-parity.py || rc=1",
                 "true  # check removed, label kept")])),
 ("M65 the permanent protocol deregistered from the artifact registry",
  "governance/artifact-registry.json",
   ("replace", [('"path": "docs/FAST_DELIVERY_AND_PARALLEL_AGENT_PROTOCOL.md"',
                 '"path": "docs/MISSING_PROTOCOL.md"')])),

 # ---- A CLOSED MISSION THAT KEEPS THE MEANS TO ACT ON ITSELF ------------------------------------------
 #
 # All six recreate the contradiction that stood on master after the functional-completeness closure had
 # actually finished: the state declared it complete while still carrying an executable authorisation for
 # it, and still naming an unclosed required gap. Each mutation reintroduces exactly one of those.
 ("M66 closure declared while next_authorized_action still authorises executing it",
  "governance/project-state.json",
   ("json_set", [(["next_authorized_action"],
                  "Execute to DONE the Product-Owner-authorised FUNCTIONAL-COMPLETENESS CLOSURE that D41 "
                  "asks for, including the controlled PRE-LIVE work on 172.21.60.25 the mission names.")])),
 ("M67 closure declared while blockers still present it as the current work",
  "governance/project-state.json",
   ("json_set", [(["blockers"],
                  ["THE CURRENT WORK IS THE FUNCTIONAL-COMPLETENESS CLOSURE D41 ASKS FOR. It is not "
                   "blocked. What remains is execution."])])),
 ("M68 closure declared while allowed_actions still authorises controlled PRE-LIVE work",
  "governance/project-state.json",
   ("json_set", [(["allowed_actions"],
                  ["Execute the authorised functional-completeness closure to DONE, including controlled "
                   "work on PRE-LIVE 172.21.60.25 as the mission authorises it."])])),
 # The whole list is replaced rather than one element's status, because json_set's drift message joins the
 # path with "/" and an integer index would raise a TypeError there instead of reporting drift.
 ("M69 closure declared while an activity is still AUTHORIZED_IN_PROGRESS",
  "governance/project-state.json",
   ("json_set", [(["authorized_activities"],
                  [{"name": "post-roadmap DEVELOPMENT appliance IAM-v2 operational trial",
                    "authorization": "D29/T0066, re-scoped by D31/T0068",
                    "status": "AUTHORIZED_IN_PROGRESS",
                    "scope": "DEVELOPMENT appliance (now retired) only."}])])),
 ("M70 closure declared while a key name still asserts an unclosed required gap",
  "governance/project-state.json",
   ("json_set", [(["current_state_facts", "functional_completeness_remaining", "increment_3",
                   "known_gap_not_closed"],
                  "scripts/pmsd-pg-integration.sh applies a curated migration list ending at 0079, so "
                  "0080-0087 have no gate-enforced integration coverage.")])),
 # And the coverage the NON-BLOCKING classification leans on: take away the glob and the limitation is a gap
 # again, so the classification must stop being accepted.
 ("M71 the factory-clean reconstruction stops applying migrations by glob",
  "scripts/clean-install-reconstruction.sh",
   ("replace", [('for f in $(ls "$MIG"/*.up.sql | sort); do',
                 'for f in "$MIG"/0010_phase3_stay_resolution.up.sql; do')])),

 # Both raised by review on PR #179, and both were real holes in the rule as first written.
 #
 # M72: the closure rules were CONDITIONAL on two sentinel fields that nothing else in the repository
 # required, so deleting both switched the entire safeguard off and the state still reported PASS.
 ("M72 both closure sentinels removed, switching the safeguard off",
  "governance/project-state.json",
   ("json_set", [(["current_state_facts", "functional_completeness_mission_status"], "REMOVED"),
                 (["current_state_facts", "functional_completeness_verdict"], "REMOVED")])),
 # M73: the ledger check matched the words "ledger completeness", which live in an echo banner -- so
 # the loop underneath could be deleted while the heading, the match and the PASS all survived.
 # apply_one swallows the ledger insert with `|| true`, so a migration really can apply unrecorded.
 ("M73 the ledger-completeness loop deleted while its heading survives",
  "scripts/clean-install-reconstruction.sh",
   ("replace", [('  [ "$(psql_q -c "SELECT count(*) FROM schema_migrations WHERE version=\'$n\'")" = "1" ] || {\n    bad "migration $n is not recorded in schema_migrations"; unrecorded=$((unrecorded+1)); }',
                 '  : # assertion removed; the heading above is untouched')])),
]

def apply(relpath, op):
    # binary I/O so restore is BYTE-EXACT (preserves original line endings; no CRLF<->LF drift)
    p = os.path.join(WORK, relpath)
    with open(p, "rb") as f: orig = f.read()
    kind = op[0]
    if kind == "remove":
        os.remove(p)                       # simulate a missing required file; restore() recreates it byte-exact
        return p, orig
    text = orig.decode("utf-8")
    if kind == "replace":
        for find, repl in op[1]:
            if find not in text: raise AssertionError(f"fixture drift: '{find[:40]}...' not found in {relpath}")
            text = text.replace(find, repl)
    elif kind == "append":
        text = text + op[1]
    elif kind == "json_set":
        # Formatting-independent. The textual fixtures below pinned the file's ONE-LINE serialisation
        # (`"1A": { "status": "ACCEPTED_AND_CLOSED"`), so a later round that rewrote project-state.json with
        # json.dumps(indent=2) expanded every object and those fixtures stopped matching. The suite then
        # aborted on "fixture drift" -- a test failing as though the repository were broken -- and, because
        # the Governance job had already stopped at an earlier step, nobody saw it for two rounds. Setting
        # the value through the parsed document cannot drift with whitespace.
        doc = json.loads(text)
        for path, value in op[1]:
            node = doc
            for k in path[:-1]:
                node = node[k]
            if node.get(path[-1]) == value:
                raise AssertionError("fixture drift: %s is already %r" % ("/".join(path), value))
            node[path[-1]] = value
        text = json.dumps(doc, indent=2, ensure_ascii=False) + chr(10)
    with open(p, "wb") as f: f.write(text.encode("utf-8"))
    return p, orig  # orig is raw bytes

def restore(p, orig):
    with open(p, "wb") as f: f.write(orig)

def audit_anchors():
    """Resolve every mutation's anchor against the real checkout, and run nothing else.

    WHY THIS EXISTS AS ITS OWN MODE. Fixture drift -- an anchor that no longer matches the file it points at --
    is the most common way this suite breaks, and it has broken in three distinct ways that all look different
    from the outside:

      it ABORTS the whole matrix        M20's anchor vanished when a trigger was removed, and all 78 cases died
                                       with it. One stale string, nothing tested.
      it SILENTLY TESTS NOTHING        M61f's anchor did not exist, so the "mutation" changed no bytes; the
                                       validators then passed on an unmutated tree and the case read [MISS]
                                       when it should have read "this case is broken".
      it BECOMES THE REQUIRED STATE    M61 kept matching while the model changed underneath it, so the case was
                                       asserting that the correct configuration is a defect.

    The full matrix costs minutes because it runs both validators per case. Anchor resolution costs a single
    pass over a handful of files, so it belongs in preflight, where drift is found in seconds instead of after
    the matrix has already thrown the answer away.

    This checks RESOLUTION, not meaning. Only a human reading the case can tell that an anchor still points at
    the thing the case is about -- which is the third failure above, and the reason each retargeted case
    carries a comment saying what it now tests and why the previous target stopped being right.
    """
    print("=== anchor audit: every mutation must still resolve against the checkout ===")
    bad, checked = [], 0
    for name, relpath, op in MUTATIONS:
        kind = op[0]
        p = os.path.join(ROOT, relpath)
        if not os.path.isfile(p):
            # "remove" mutations are ABOUT a file being absent, but the file must exist to be removed.
            bad.append((name, relpath, "the file does not exist"))
            continue
        if kind in ("append", "remove"):
            continue
        raw = _io.open(p, encoding="utf-8", newline="").read()
        if kind == "replace":
            for find, _repl in op[1]:
                checked += 1
                if find not in raw:
                    bad.append((name, relpath, "anchor not found: %r" % find[:72]))
        elif kind == "json_set":
            doc = json.loads(raw)
            for path, value in op[1]:
                checked += 1
                node = doc
                try:
                    # ONLY THE PARENT CHAIN MUST RESOLVE. The leaf deliberately may not exist: several cases
                    # (M70) work by RE-INTRODUCING a key that closure removed, and json_set creates it. An
                    # earlier version of this audit demanded the leaf and reported M70 as drift -- a check
                    # stricter than the thing it stands for, which is just a false alarm with a green badge.
                    for seg in path[:-1]:
                        node = node[seg]
                    if node.get(path[-1]) == value:
                        # The one real no-op: the mutation would write the value already there, so the case
                        # changes nothing and proves nothing. The applier raises on exactly this too.
                        bad.append((name, relpath,
                                    "%s already holds the mutated value, so the case is a no-op"
                                    % "/".join(map(str, path))))
                except (KeyError, TypeError, IndexError, AttributeError) as exc:
                    bad.append((name, relpath, "the parent key path does not resolve (%s): %s"
                                % (exc.__class__.__name__, "/".join(map(str, path[:-1])))))
    for name, relpath, why in bad:
        print("  DRIFT  %s" % name)
        print("         %s: %s" % (relpath, why))
    print("  %d anchors across %d cases; %d drifted" % (checked, len(MUTATIONS), len(bad)))
    if bad:
        print()
        print("  This is a TEST-FIXTURE problem, not a repository defect. A drifted anchor either aborts the")
        print("  matrix or turns its case into a no-op that reports [MISS]; either way the protection that case")
        print("  stands for is not being tested. Retarget the case at what it is actually about, and say in a")
        print("  comment why the old target stopped being right.")
        print("=" * 50)
        print("MUTATION_ANCHORS = FAIL (%d)" % len(bad))
        return 1
    print("=" * 50)
    print("MUTATION_ANCHORS = PASS")
    return 0



def main():
    global WORK, _SANDBOX
    if "--anchors" in sys.argv:
        # No sandbox, no validators, no mutation: the checkout is only READ.
        return audit_anchors()
    require_full = "--require-full" in sys.argv
    limit = os.environ.get("MUTATION_MAX_CASES")
    if require_full and limit:
        # The authoritative gate must never be satisfied by a partial run.
        print("=== case-limit check ===")
        print("  FAIL: --require-full was requested but MUTATION_MAX_CASES=%s is set." % limit)
        print("  MUTATION_MAX_CASES is TEST-ONLY (the isolation regression's short child runs). The")
        print("  authoritative gate must run the complete matrix.")
        return 2

    print("=== isolation: mutations run in a disposable sandbox, never in the checkout ===")
    swept = sweep_stale_sandboxes()
    if swept:
        print("  swept %d stale sandbox(es) from a previously force-killed run: %s"
              % (len(swept), ", ".join(swept[:3])))
    before = canonical_digest()
    try:
        _SANDBOX, WORK, copied = build_sandbox()
    except Exception as exc:                                        # noqa: BLE001
        # FAIL CLOSED. Falling back to the checkout is what made this harness dangerous.
        print("  SANDBOX FAILED: %s" % exc)
        print("  refusing to mutate the canonical checkout; nothing was changed")
        return 2
    print("  sandbox: %s" % WORK)
    print("  fidelity: %d working-tree files; git status --porcelain and git ls-files -s (blobs, stages,"
          " modes) both identical to the checkout" % copied)

    try:
        rc = _run_matrix(require_full)
    finally:
        _cleanup()
        WORK = ROOT

    after = canonical_digest()
    if before != after:
        print("=" * 60)
        print("ISOLATION VIOLATED: governance/ in the checkout changed while the suite ran")
        print("  before %s\n  after  %s" % (before[:16], after[:16]))
        return 1
    print("  isolation verified: canonical governance/ is byte-identical (%s)" % before[:16])
    return rc


def _worker_count(n_cases):
    """How many cases to evaluate at once.

    ONE CASE COSTS A FULL DOUBLE VALIDATION -- the structural validator plus the keyword validator over the
    whole tree -- which measured ~16s on a CI runner. Sixty of those in series is sixteen minutes, and it was
    the single largest step in the whole delivery path. The cases are independent by construction (each
    mutates one file, judges, and restores), so the only thing that ever made them serial was sharing ONE
    sandbox.

    Each worker therefore gets its OWN sandbox and the matrix is evaluated concurrently. Nothing about what
    is checked changes: every case still runs, both validators still run per case, and --require-full still
    refuses a partial matrix.

    SERIAL WHEN MUTATION_MAX_CASES IS SET. That knob exists for the isolation regression's short overlapping
    child runs, whose whole point is to observe concurrent sandbox behaviour; giving those children their own
    internal fan-out would change what that regression measures.
    """
    if (os.environ.get("MUTATION_MAX_CASES") or "").isdigit():
        return 1
    override = os.environ.get("MUTATION_WORKERS")
    if (override or "").isdigit() and int(override) > 0:
        return min(int(override), n_cases)
    return max(1, min(os.cpu_count() or 1, 8, n_cases))


def _evaluate(case):
    """Judge one mutation inside THIS process's sandbox. Returns (name, failed, which)."""
    name, relpath, op = case
    p, orig = apply(relpath, op)
    try:
        s, k = both_status()
        which = []
        if s != 0:
            which.append("structural")
        if k != 0:
            which.append("keyword")
        return (name, (s != 0 or k != 0), ",".join(which))
    finally:
        restore(p, orig)


def _worker_init():
    """Give this worker process its own sandbox.

    A worker mutates files, so two workers sharing a sandbox would judge each other's mutations and the
    matrix would report nonsense. The parent's sandbox is left alone for the baseline and restored-state
    checks that bracket the matrix.
    """
    global WORK, _SANDBOX
    _SANDBOX, WORK, _ = build_sandbox()


def _run_cases(cases):
    """Evaluate every case, concurrently where that is safe, and return verdicts IN THE GIVEN ORDER.

    Order is restored explicitly rather than left to completion order: this output is read by a human
    comparing runs, and a matrix that printed its cases in a different sequence each time would be unusable
    as a diff even though the verdicts were identical.

    FAIL CLOSED. A worker that dies takes the run with it -- an unevaluated case is never a passing one.
    """
    workers = _worker_count(len(cases))
    if workers <= 1:
        print("  workers: 1 (serial)")
        return [_evaluate(c) for c in cases]

    print("  workers: %d concurrent sandboxes (every case still runs; both validators still run per case)"
          % workers)
    import concurrent.futures as _cf
    verdicts = [None] * len(cases)
    with _cf.ProcessPoolExecutor(max_workers=workers, initializer=_worker_init) as ex:
        futures = {ex.submit(_evaluate, c): i for i, c in enumerate(cases)}
        for fut in _cf.as_completed(futures):
            verdicts[futures[fut]] = fut.result()   # an exception here propagates and fails the run
    missing = [cases[i][0] for i, v in enumerate(verdicts) if v is None]
    if missing:
        raise RuntimeError("mutation matrix did not return a verdict for: %s" % ", ".join(missing[:5]))
    return verdicts


def _run_matrix(require_full=False):
    print("=== baseline (good state) must PASS both validators ===")
    s0, k0 = both_status()
    if s0 != 0 or k0 != 0:
        print(f"  BASELINE FAIL: structural={s0} keyword={k0} — fix the good state before mutation testing"); return 2
    print("  baseline: structural=PASS keyword=PASS")
    print("=== mutation matrix (each must make validation FAIL non-zero) ===")
    results = []
    allok = True
    limit = os.environ.get("MUTATION_MAX_CASES")
    cases = MUTATIONS[:int(limit)] if (limit or "").isdigit() else MUTATIONS
    if cases is MUTATIONS:
        print("  case limit: NONE -- running the COMPLETE matrix, %d of %d cases%s"
              % (len(cases), len(MUTATIONS), " (--require-full)" if require_full else ""))
    else:
        print("  NOTE: MUTATION_MAX_CASES=%s -- running %d of %d cases. TEST-ONLY knob for the isolation"
              % (limit, len(cases), len(MUTATIONS)))
        print("        regression's short overlapping child runs. The authoritative gate passes")
        print("        --require-full, which refuses to run at all while this is set.")

    verdicts = _run_cases(cases)
    for name, failed, which in verdicts:
        results.append((name, failed, which))
        allok = allok and failed
        print(f"  [{'PASS' if failed else 'MISS'}] {name:52s} -> fails: {which or 'NONE (BAD)'}")
    print("=== restored good state must PASS again ===")
    s1, k1 = both_status()
    restored_ok = (s1 == 0 and k1 == 0)
    print(f"  restored: structural={'PASS' if s1==0 else 'FAIL'} keyword={'PASS' if k1==0 else 'FAIL'}")
    print("=" * 60)
    ok = allok and restored_ok
    # Machine-readable, so the count that actually ran is evidence rather than a claim in prose.
    print("MUTATION_CASES_EXECUTED=%d MUTATION_CASES_TOTAL=%d MUTATION_CASE_LIMIT=%s"
          % (len(cases), len(MUTATIONS), os.environ.get("MUTATION_MAX_CASES") or "none"))
    print("PROJECT_STATE_MUTATION_TESTS =", "PASS" if ok else "FAIL")
    return 0 if ok else 1

if __name__ == "__main__":
    sys.exit(main())
