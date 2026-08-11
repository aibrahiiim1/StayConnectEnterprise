#!/usr/bin/env python3
"""THE EVIDENCE ARTIFACT MAY NOT PRESENT SUPERSEDED WORK AS CURRENT.

The Phase-3 software artifact is the most authoritative file a reviewer opens: it is signed by a manifest,
uploaded by CI, and read as the answer to "where is this project". For that reason it is also the surface where
staleness does the most damage — and it went stale in exactly the way this repository's other validators exist
to prevent.

`scripts/ci/phase3_evidence.py` carried the Live Increment-9 verdicts as a literal, including
`corrected_software_deployed: false` and a five-item `remaining_live_work` list. Those were true on 2026-08-10.
They were still being emitted into every new artifact after the blocked subset was re-validated, after Phase 3
was accepted and closed, and after PR #6 was merged — so an artifact generated on final `master` described
finished work as outstanding, and its README told readers to look up "the subset of live work that remains".

Nothing was wrong with the history. What was wrong was that history had no label and current state had no
source. So the generator now derives current state from `governance/project-state.json` ->
`current_state_facts`, and this suite runs it for real and asserts the contract:

  1. the historical block is LABELLED historical, and its date-bound fields say which date they describe;
  2. current state is present, and matches the recorded facts rather than a literal in the generator;
  3. when the phase is closed, `remaining_live_work` is EMPTY -- and the README says so;
  4. no top-level key presents outstanding work as current;
  5. the negative case: with facts that say work DOES remain, the artifact must say so. A check that only ever
     sees a closed project would pass just as happily on a generator that hard-codes "nothing remains".

Usage:  python tools/tests/evidence_artifact/run_artifact_staleness.py
Exit:   0 = the artifact reflects the recorded facts, 1 = it does not.
"""
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
GEN = os.path.join("scripts", "ci", "phase3_evidence.py")

failures = []
notes = []


def bad(msg):
    failures.append(msg)


def ok(msg):
    notes.append(msg)


def generate(facts_override=None, playwright_counts=None):
    """Run the real generator against a disposable workspace, optionally with patched facts.

    The workspace is a copy so that a patched project-state cannot touch the repository, and the generator is
    invoked as a subprocess exactly as CI invokes it -- importing it would test a different thing than the
    thing that runs.
    """
    d = tempfile.mkdtemp(prefix="artifact-")
    ws = os.path.join(d, "ws")
    os.makedirs(os.path.join(ws, "governance"))
    os.makedirs(os.path.join(ws, "scripts", "ci"))
    shutil.copy2(os.path.join(ROOT, GEN), os.path.join(ws, GEN))

    state = json.load(io.open(os.path.join(ROOT, "governance", "project-state.json"), encoding="utf-8"))
    if facts_override:
        state["current_state_facts"].update(facts_override)
    io.open(os.path.join(ws, "governance", "project-state.json"), "w", encoding="utf-8", newline="\n").write(
        json.dumps(state, indent=2, ensure_ascii=False) + "\n")

    evid = os.path.join(d, "evid")
    art = os.path.join(d, "art")
    os.makedirs(os.path.join(evid, "counts"))
    os.makedirs(art)
    io.open(os.path.join(evid, "env.json"), "w", encoding="utf-8", newline="\n").write(json.dumps({
        "repository": "aibrahiiim1/StayConnectEnterprise", "workflow": "Phase 3 Software CI", "job": "gate",
        "run_id": "STALENESS-SUITE", "run_attempt": "1", "start_utc": "2026-01-01T00:00:00Z",
        "delivery_head": "0" * 40}))
    io.open(evid + os.sep + "steps.tsv", "w", encoding="utf-8", newline="\n").write("go-unit\t0\t1\n")
    if playwright_counts is not None:
        io.open(os.path.join(evid, "counts", "playwright.json"), "w", encoding="utf-8", newline="\n").write(
            json.dumps(playwright_counts))

    env = dict(os.environ, EVID=evid, ART=art, GITHUB_WORKSPACE=ws, PYTHONIOENCODING="utf-8")
    r = subprocess.run([sys.executable, os.path.join(ws, GEN)], capture_output=True, env=env, cwd=ws)
    if r.returncode != 0:
        shutil.rmtree(d, ignore_errors=True)
        raise AssertionError("the evidence generator failed: %s" % r.stderr.decode("utf-8", "replace")[:400])
    meta = json.load(io.open(os.path.join(art, "RUN_META.json"), encoding="utf-8"))
    readme = io.open(os.path.join(art, "README.md"), encoding="utf-8").read()
    pw_out = None
    pw_path = os.path.join(art, "counts", "playwright.json")
    if os.path.isfile(pw_path):
        pw_out = json.load(io.open(pw_path, encoding="utf-8"))
    shutil.rmtree(d, ignore_errors=True)
    return meta, readme, pw_out


def main():
    real_facts = (json.load(io.open(os.path.join(ROOT, "governance", "project-state.json"),
                                    encoding="utf-8")).get("current_state_facts") or {})

    print("== the artifact as CI would generate it now ==")
    meta, readme, _ = generate()

    # ---- 1. history must be labelled as history -------------------------------------------------------------
    hist = meta.get("live_increment9_historical")
    if not isinstance(hist, dict):
        bad("RUN_META has no live_increment9_historical block; the Increment-9 verdicts must be preserved")
    else:
        if hist.get("_record_type") != "HISTORICAL":
            bad("the Increment-9 block is not marked _record_type=HISTORICAL")
        elif not hist.get("_as_observed_on"):
            bad("the Increment-9 block is marked historical but does not say which date it describes")
        else:
            ok("the Increment-9 verdicts are preserved and labelled HISTORICAL (%s)" % hist["_as_observed_on"])
        # Date-bound fields must carry the date in their own name: a reader who scrolls past the label must
        # still be unable to mistake `corrected_software_deployed: false` for the present tense.
        for legacy in ("corrected_software_deployed", "remaining_live_work"):
            if legacy in hist:
                bad("historical field %r is not date-qualified; it reads as current state" % legacy)
        if any(k.endswith("_as_at_2026_08_10") for k in hist):
            ok("date-bound historical fields name their own date")
        if "live_increment9" in meta:
            bad("RUN_META still carries the unlabelled live_increment9 key alongside the historical one")

    # ---- 2 & 3. current state must be derived, and must match the facts -------------------------------------
    cur = meta.get("project_state_at_generation")
    if not isinstance(cur, dict):
        bad("RUN_META has no project_state_at_generation block; current state has no source")
    else:
        for key in ("phase_status", "accepted", "closed", "merged", "corrected_software_deployed",
                    "accepted_runtime_head", "merge_commit"):
            if key in real_facts and cur.get(key) != real_facts.get(key):
                bad("project_state_at_generation.%s is %r but current_state_facts says %r"
                    % (key, cur.get(key), real_facts.get(key)))
        if not failures:
            ok("current state in the artifact equals governance/project-state.json -> current_state_facts")

        remaining = cur.get("remaining_live_work")
        if not isinstance(remaining, list):
            bad("project_state_at_generation.remaining_live_work is missing; 'nothing remains' must be stated")
        elif real_facts.get("accepted") and real_facts.get("closed"):
            if remaining:
                bad("the phase is accepted and closed but the artifact still lists %d item(s) of remaining "
                    "live work: %s" % (len(remaining), remaining[:2]))
            else:
                ok("the closed phase reports an EMPTY remaining_live_work list")

    # ---- 4. the README must not advertise outstanding work that does not exist -------------------------------
    if real_facts.get("accepted") and real_facts.get("closed"):
        lowered = readme.lower()
        for phrase in ("of live work that remains", "live work remains outstanding.",
                       "subset of live work that remains"):
            if phrase in lowered and "no live work remains" not in lowered:
                bad("the artifact README advertises remaining live work: %r" % phrase)
        if "no live work remains" in lowered:
            ok("the artifact README states that no live work remains")
        else:
            bad("the artifact README never states the current project state")

    # ---- 5. the negative case: it must still be able to say work DOES remain --------------------------------
    print("\n== negative case: facts that say the work is NOT finished ==")
    meta2, readme2, _ = generate({"accepted": False, "closed": False, "live_increment9_revalidated": False,
                               "corrected_software_deployed": False})
    rem2 = (meta2.get("project_state_at_generation") or {}).get("remaining_live_work")
    if not rem2:
        bad("with facts saying the work is unfinished, the artifact still reports nothing remaining; the "
            "'nothing remains' answer is hard-coded rather than derived")
    else:
        ok("with unfinished facts the artifact reports %d outstanding item(s), so the result is derived" % len(rem2))
    if "no live work remains" in readme2.lower():
        bad("with unfinished facts the README still claims no live work remains")
    else:
        ok("the README tracks the facts in both directions")

    # ---- 6. the artifact must not ship raw repository content -----------------------------------------------
    #
    # Playwright embeds `config.metadata.gitDiff` — the whole PR diff, truncated at 100,000 characters — into
    # its JSON report on pull_request events, and that report is copied into the artifact. The artifact's own
    # README promises "derived summaries only", so this is content it must not carry, and unlike a test count
    # it cannot be bounded by review. It survived unnoticed because the truncation is in path order: earlier,
    # larger PRs used up the 100 KB long before reaching anything the PII gate objects to.
    print("\n== the artifact must not ship the raw repository diff Playwright embeds ==")
    leak = "diff --git a/fixture.md b/fixture.md\n" + ("+leaked line\n" * 50)
    _, _, pw = generate(playwright_counts={
        "config": {"metadata": {"gitDiff": leak,
                                "gitCommit": {"hash": "0" * 40, "subject": "provenance must survive"},
                                "ci": {"buildHref": "https://example.invalid/run/1"}}},
        "stats": {"expected": 1, "unexpected": 0}})
    if pw is None:
        bad("the generator did not copy counts/playwright.json into the artifact at all")
    elif "gitDiff" in ((pw.get("config") or {}).get("metadata") or {}):
        bad("the artifact still carries Playwright's embedded repository diff (gitDiff)")
    else:
        ok("the embedded repository diff is stripped from counts/playwright.json")
        prov = ((pw.get("config") or {}).get("metadata") or {}).get("gitCommit") or {}
        if prov.get("subject") == "provenance must survive":
            ok("commit provenance survives the strip; only the unbounded blob is dropped")
        else:
            bad("stripping the diff also removed the commit provenance that makes the report traceable")

    print()
    for n in notes:
        print("  [PASS] %s" % n)
    for f in failures:
        print("  [FAIL] %s" % f)
    print("=" * 60)
    print("EVIDENCE_ARTIFACT_STALENESS: pass=%d fail=%d -> %s"
          % (len(notes), len(failures), "PASS" if not failures else "FAIL"))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
