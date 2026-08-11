#!/usr/bin/env python3
"""Assemble the Phase-3 software-gate evidence artifact from real run outputs.

This runs as the FINAL step of the Phase-3 Software workflow, after every mandatory
gate has passed. It reads what the gate actually produced — per-step exit codes and
durations, per-suite test counts, infrastructure retries, the preflight result, tool
versions and lock/migration hashes — and writes a curated, PII-safe evidence directory
plus a SHA-256 integrity manifest over it.

Two directories:

  EVID  staging. Written to throughout the job. Contains raw per-step logs under
        logs/, which can carry test-fixture names and rooms, so nothing under logs/
        is ever copied into the artifact.
  ART   the artifact. This script populates it with DERIVED, PII-free summaries only,
        then manifests it. This is what actions/upload-artifact uploads.

The manifest convention (documented here and in the artifact README): MANIFEST.sha256
lists every file in ART EXCEPT MANIFEST.sha256 itself — a file cannot contain its own
hash. This script prints the manifest's own SHA-256 to stdout and to $GITHUB_OUTPUT so
the run surfaces the single integrity root a verifier checks the manifest against.
"""
import hashlib
import io
import json
import os
import re
import shutil
import sys
import time


def sha256_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 16), b""):
            h.update(chunk)
    return h.hexdigest()


def read_json(path: str, default=None):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except (OSError, json.JSONDecodeError):
        return default


def read_text(path: str, default: str = "") -> str:
    try:
        with open(path, encoding="utf-8") as f:
            return f.read()
    except OSError:
        return default


def extract_acceptance_matrix(report_path):
    """Pull the §6a Phase-3 Acceptance Matrix table out of the Final Report.

    Returns (markdown, rows) where rows is a list of {num, dimension, verdict}. The table is the block of
    pipe-rows following the '## 6a' heading; extraction stops at the next heading.
    """
    text = read_text(report_path)
    idx = text.find("## 6a")
    if idx < 0:
        return "", []
    end = text.find("\n## ", idx + 5)
    block = text[idx:end if end > 0 else len(text)]
    rows = []
    md_lines = ["# Phase 3 — Complete Acceptance Matrix (from the Final Report §6a)", ""]
    for line in block.splitlines():
        s = line.strip()
        if not s.startswith("|"):
            continue
        md_lines.append(s)
        cells = [c.strip() for c in s.strip("|").split("|")]
        # Data rows have a leading dimension id; header/separator rows do not. The id may be a plain number
        # (`7`) OR a SUB-DIMENSION (`30a`, `30b`, …). An earlier version tested `cells[0].isdigit()`, which
        # silently dropped every sub-dimension: the artifact reported "35 dimensions" while the report showed
        # more rows, and nothing flagged the difference. Sub-dimensions are real verdicts and are counted.
        if len(cells) >= 3 and re.match(r"^\d+[a-z]?$", cells[0]):
            rows.append({"id": cells[0], "dimension": cells[1], "verdict": cells[2]})
    return "\n".join(md_lines) + "\n", rows


def _manifest_paths(md_text):
    """Return the set of repo-relative paths a change-manifest markdown table lists (first table column)."""
    paths = set()
    for line in md_text.splitlines():
        s = line.strip()
        if not s.startswith("| `"):
            continue
        first = s.split("|")[1].strip()
        # cell is `path` or `old -> new`; take the code-spanned path(s)
        for tok in re.findall(r"`([^`]+)`", first):
            tok = tok.strip()
            if " -> " in tok:
                tok = tok.split(" -> ")[-1].strip()
            paths.add(tok)
    return paths


def manifest_parity_result(report_path, manifest_path):
    """Compare the manifest embedded in the Final Report to the standalone generated manifest."""
    report = read_text(report_path)
    # the report embeds its own changed-file table; compare its path set to the generated manifest's path set
    gen_paths = _manifest_paths(read_text(manifest_path))
    rep_paths = _manifest_paths(report)
    only_report = sorted(rep_paths - gen_paths)
    only_generated = sorted(gen_paths - rep_paths)
    return {
        "generated_manifest": "docs/manifests/Phase3-change-manifest.md",
        "generated_path_count": len(gen_paths),
        "report_embedded_path_count": len(rep_paths),
        "match": not only_report and not only_generated and len(gen_paths) > 0,
        "in_report_not_generated": only_report[:20],
        "in_generated_not_report": only_generated[:20],
    }


def run_zero_stale(root):
    """Run the governance zero-stale + project-state validators and record their verdicts."""
    import subprocess
    out = {}
    for name, cmd in [
        ("project_state_validate", ["python3", "tools/project-state.py", "validate"]),
        ("zero_stale_leftovers", ["bash", "tools/validate-project-state.sh"]),
    ]:
        try:
            r = subprocess.run(cmd, cwd=root, capture_output=True, text=True, timeout=600)
            tail = (r.stdout or "")[-400:]
            out[name] = {"exit": r.returncode, "verdict": "PASS" if r.returncode == 0 else "FAIL",
                         "tail": tail.strip().splitlines()[-1] if tail.strip() else ""}
        except Exception as e:  # noqa: BLE001 — record the failure rather than aborting evidence assembly
            out[name] = {"exit": -1, "verdict": "ERROR", "tail": str(e)[:120]}
    return out


def main() -> int:
    evid = os.environ["EVID"]
    art = os.environ["ART"]
    root = os.environ.get("GITHUB_WORKSPACE", os.getcwd())
    os.makedirs(art, exist_ok=True)
    os.makedirs(os.path.join(art, "counts"), exist_ok=True)

    env = read_json(os.path.join(evid, "env.json"), {}) or {}
    end_utc = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

    # ---- per-step ledger (slug, exit, seconds) ------------------------------------
    steps = []
    for line in read_text(os.path.join(evid, "steps.tsv")).splitlines():
        if not line.strip():
            continue
        slug, rc, secs = line.split("\t")
        steps.append({"step": slug, "exit_code": int(rc), "duration_seconds": int(secs)})
    all_zero = all(s["exit_code"] == 0 for s in steps) and bool(steps)

    # ---- test totals, each from the same run that gated on them --------------------
    counts_dir = os.path.join(evid, "counts")
    go_unit = read_json(os.path.join(counts_dir, "go-unit.json"), {})
    go_race = read_json(os.path.join(counts_dir, "go-race.json"), {})
    vitest = read_json(os.path.join(counts_dir, "vitest.json"), {})
    playwright = read_json(os.path.join(counts_dir, "playwright.json"), {})
    preflight = read_json(os.path.join(counts_dir, "preflight.json"), {})
    provisioning = read_json(os.path.join(counts_dir, "provisioning.json"), {})
    enforcement = read_json(os.path.join(counts_dir, "enforcement.json"), {})
    foundation = read_json(os.path.join(counts_dir, "foundation.json"), {})

    logs_dir = os.path.join(evid, "logs")

    # ---- the real-kernel suite (disposable Linux network namespaces) -------------------------------------
    # Written by scripts/ci/kernel-netns-suite.sh as key=value, including the kernel and tool versions it ran
    # against and whether the host's own ruleset was proven unchanged.
    kernel_netns = {}
    for line in read_text(os.path.join(counts_dir, "kernel-netns.txt")).splitlines():
        if "=" in line:
            k, v = line.split("=", 1)
            kernel_netns[k.strip()] = v.strip()
    if kernel_netns:
        kernel_netns["evidence_class"] = "REAL KERNEL (disposable namespaces) — NOT live appliance evidence"

    # ---- migration lifecycle summary (parsed from the gate log — the truthful assertion total) ------------
    # The lifecycle gate prints "PHASE3_0010_LIFECYCLE: pass=N fail=M -> PASS". We extract the assertion
    # totals, never the raw log (which we keep out of the artifact), so the number is provably the one CI saw.
    migration_summary = {}
    m = re.search(r"PHASE3_0010_LIFECYCLE:\s*pass=(\d+)\s+fail=(\d+)",
                  read_text(os.path.join(logs_dir, "migration-lifecycle.log")))
    if m:
        migration_summary = {"assertions_passed": int(m.group(1)), "assertions_failed": int(m.group(2)),
                             "gate": "iam_v2_scratch/phase3_0010_lifecycle.sh"}

    # ---- the eleven disposable-PG16 integration suites, each with its result -----------------------------
    pg16_suites = []
    for line in read_text(os.path.join(logs_dir, "pg16-integration.log")).splitlines():
        mm = re.match(r"^(ok|FAIL|---)\s+(github\.com/\S+)\s", line)
        if mm and "data-plane/" in mm.group(2):
            pg16_suites.append({"suite": mm.group(2).split("data-plane/")[-1],
                                "result": "PASS" if mm.group(1) == "ok" else "FAIL"})

    def vitest_totals(v):
        return {
            "passed": v.get("numPassedTests", 0),
            "skipped": v.get("numPendingTests", 0),
            "failed": v.get("numFailedTests", 0),
            "total": v.get("numTotalTests", 0),
        }

    def playwright_totals(p):
        st = p.get("stats", {})
        return {
            "passed": st.get("expected", 0),
            "skipped": st.get("skipped", 0),
            "failed": st.get("unexpected", 0),
            "flaky": st.get("flaky", 0),
        }

    totals = {
        "go_unit": go_unit,
        "go_race": go_race,
        "vitest": vitest_totals(vitest) if vitest else {},
        "playwright": playwright_totals(playwright) if playwright else {},
        "preflight": {"pass": preflight.get("pass"), "fail": preflight.get("fail")} if preflight else {},
        "provisioning": {"passed": provisioning.get("pass", 0), "failed": provisioning.get("fail", 0),
                         "skipped": provisioning.get("skip", 0)} if provisioning else {},
        "network_enforcement": {"passed": enforcement.get("pass", 0), "failed": enforcement.get("fail", 0),
                                "skipped": enforcement.get("skip", 0)} if enforcement else {},
        "nft_foundation": {"passed": foundation.get("pass", 0), "failed": foundation.get("fail", 0),
                           "skipped": foundation.get("skip", 0)} if foundation else {},
        # The REAL-KERNEL suite is reported separately and labelled as such everywhere it appears. It is the
        # only entry in this artifact produced by running nft and tc against a kernel, and conflating it with
        # the modelled suites would be the exact overclaim this evidence exists to avoid.
        "kernel_netns": kernel_netns,
    }

    # ---- the complete Phase-3 Acceptance Matrix (dimensional, ~35 rows) ----------------------------------
    # The authoritative matrix lives in the Final Report §6a. It is extracted here so the artifact carries the
    # WHOLE Phase-3 verdict set, not only the gate steps — and so a verifier can read it without the report.
    report_path = os.path.join(root, "docs/reports/StayConnect-IAM-Phase3-Final-Report.md")
    accept_matrix_md, accept_matrix_rows = extract_acceptance_matrix(report_path)

    # ---- embedded-report / generated-manifest parity ----------------------------------------------------
    manifest_parity = manifest_parity_result(report_path,
                                             os.path.join(root, "docs/manifests/Phase3-change-manifest.md"))

    # ---- Zero-Stale document + governance checks, run here and recorded ----------------------------------
    zero_stale = run_zero_stale(root)

    # ---- integrity inputs the artifact makes claims about --------------------------
    hashed_inputs = {}
    for rel in [
        "data-plane/migrations/0010_phase3_stay_resolution.up.sql",
        "data-plane/migrations/0010_phase3_stay_resolution.down.sql",
        "data-plane/go.sum",
        "hotel-admin/package-lock.json",
        ".github/workflows/phase3-software.yml",
        "governance/project-state.json",
        "docs/manifests/Phase3-change-manifest.md",
    ]:
        p = os.path.join(root, rel)
        if os.path.isfile(p):
            hashed_inputs[rel] = sha256_file(p)

    tool_versions = {}
    for line in read_text(os.path.join(evid, "tool-versions.tsv")).splitlines():
        if "\t" in line:
            k, v = line.split("\t", 1)
            tool_versions[k] = v

    infra_retries = read_text(os.path.join(evid, "infra-retries.tsv")).strip()
    infra_list = [ln for ln in infra_retries.splitlines() if ln.strip()]

    # ---- current project state, read from the repository rather than restated ---------------------------
    #
    # governance/project-state.json -> current_state_facts is the single machine-readable record of where the
    # project actually is. Reading it here is what stops this generator from becoming another surface that has
    # to be remembered and updated by hand after every decision.
    facts = (read_json(os.path.join(root, "governance", "project-state.json"), {}) or {}).get(
        "current_state_facts") or {}

    increment9_historical = {
        "executed_on": "2026-08-10",
        "executed_against_head": "83449200a8aca7018fac5b38a96b3a1aafc66ba2",
        "appliance": "172.21.60.23",
        "item_1_read_only_pms": "PASS",
        "item_2_live_dark_deployment": "BLOCKED/PARTIAL - migration 0010 applied cleanly and the nft foundation installed surgically with byte-identical legacy parity, but it did not survive the next netd start",
        "item_3_reboot": "FAIL for the required post-reboot persistence - phase3_auth_ipv4 absent, table structurally identical to the pre-install baseline",
        "item_4_rollback_rehearsal": "PASS functionally, with a confirmed false-PASS defect in the binary-rollback tooling (corrected; see scripts/binary-rollback.sh)",
        "item_5_flags_off": "PASS",
        "legacy_live_session_continuity": "NOT PROVEN - zero legacy guests were online during the window; no guest or session state was fabricated",
        "migration_0010_state": "APPLIED to the production site database with 0 rows; reversible, and its down/up round-trip was rehearsed live",
        "corrected_software_deployed_as_at_2026_08_10": False,
        "remaining_live_work_as_at_2026_08_10": [
            "deploy the corrected HEAD",
            "prove phase3_auth_ipv4 is present and empty while DARK",
            "prove a netd restart issues NO nft mutation and preserves the structure",
            "prove reboot reconstruction, then repeat once for idempotence",
            "re-run the binary rollback rehearsal with scripts/binary-rollback.sh",
        ],
    }

    if facts.get("live_increment9_revalidated"):
        increment9_superseded_note = (
            "SUPERSEDED. The blocked subset above was re-validated live on %s and every item PASSED. The "
            "verdicts are kept as observed; the fields ending _as_at_2026_08_10 describe that date only and "
            "are NOT the current state. See project_state_at_generation."
            % facts.get("live_increment9_revalidated_on", "2026-08-11"))
    else:
        increment9_superseded_note = (
            "NOT YET SUPERSEDED as at generation: the recorded facts do not show a completed re-validation.")

    # Work still outstanding is DERIVED. A closed, accepted phase has none, and saying so with an empty list is
    # what makes "nothing remains" checkable instead of merely absent.
    if facts.get("accepted") and facts.get("closed"):
        remaining_live_work = []
    elif facts.get("live_increment9_revalidated"):
        remaining_live_work = []
    else:
        remaining_live_work = list(increment9_historical["remaining_live_work_as_at_2026_08_10"])

    project_state_block = {
        "read_from": "governance/project-state.json -> current_state_facts",
        "phase": facts.get("phase"),
        "phase_status": facts.get("phase_status"),
        "accepted": facts.get("accepted"),
        "closed": facts.get("closed"),
        "accepted_at_maturity": facts.get("accepted_at_maturity"),
        "accepted_runtime_head": facts.get("accepted_runtime_head"),
        "merged": facts.get("merged"),
        "merge_commit": facts.get("merge_commit"),
        "cut_over": facts.get("cut_over"),
        "dark": facts.get("dark"),
        "corrected_software_deployed": facts.get("corrected_software_deployed"),
        "live_increment9_revalidated": facts.get("live_increment9_revalidated"),
        "legacy_live_session_continuity": facts.get("legacy_live_session_continuity"),
        "remaining_live_work": remaining_live_work,
    }

    run_meta = {
        "artifact_kind": "phase3-software-gate-evidence",
        "note": "Phase 3 software gate ONLY. Contains no live-appliance, Production-DB or live-PMS evidence.",
        "delivery_head": env.get("delivery_head"),
        "inventory_head": env.get("inventory_head"),
        "base_head": env.get("base_head"),
        "branch": env.get("branch"),
        "pull_request": env.get("pr_number"),
        "repository": env.get("repository"),
        "workflow": env.get("workflow"),
        "job": env.get("job"),
        "run_id": env.get("run_id"),
        "run_attempt": env.get("run_attempt"),
        "started_utc": env.get("start_utc"),
        "completed_utc": end_utc,
        "all_steps_passed": all_zero,
        "tool_versions": tool_versions,
        "lock_and_migration_hashes": hashed_inputs,
        "steps": steps,
        "test_totals": totals,
        "migration_lifecycle": migration_summary,
        "pg16_integration_suites": pg16_suites,
        "acceptance_matrix_rows": accept_matrix_rows,
        "acceptance_matrix_row_count": len(accept_matrix_rows),
        "manifest_parity": manifest_parity,
        "zero_stale_checks": zero_stale,
        "skipped_totals": {
            "go_unit": go_unit.get("skip", 0),
            "vitest": totals["vitest"].get("skipped", 0) if totals["vitest"] else 0,
            "playwright": totals["playwright"].get("skipped", 0) if totals["playwright"] else 0,
        },
        "infrastructure_retries": infra_list,
        # THIS RUN's restrictions, and they are scoped to this run on purpose.
        #
        # The list used to read as a project-wide claim and included "Migration 0010 undeployed" and "no
        # appliance access". Both stopped being true on 2026-08-10, when Live Increment 9 deliberately applied
        # 0010 to the site database and deployed to the appliance under Product-Owner authorization — and the
        # evidence artifact, which is the most authoritative surface a reviewer opens, went on asserting them.
        # What a CI run can honestly certify is what the CI run itself did.
        "run_restrictions_confirmed": [
            "this run contacted no appliance",
            "this run contacted no Production database",
            "this run contacted no live PMS",
            "this run deployed nothing and rebooted nothing",
            "this run enabled no Phase-3 flag",
            "every database used by this run was a disposable container",
            "every nft/tc operation in this run was inside a disposable network namespace",
            "the host ruleset was proven unchanged by the kernel gate",
        ],
        # PROJECT-LEVEL standing restrictions: still true, and each one is a thing this delivery must not do.
        "standing_restrictions_confirmed": [
            "all Phase-3 flags OFF",
            "zero persistent runtime iam_v2 privileges",
            "no Gate-P grants",
            "no PS/PA",
            "no financial posting",
            "no paid access",
            "no implicit FX",
            "no programmatic reversal",
            "no IAM-v2 cutover",
            "no Phase 4",
        ],
        # LIVE INCREMENT 9, AS OBSERVED ON 2026-08-10 — A HISTORICAL RECORD, NOT CURRENT STATE.
        #
        # The verdicts below are preserved exactly as observed, including the two that failed. That is the
        # project's evidence model and it does not change. What DID change is everything around them: the
        # blocked subset was re-validated live on 2026-08-11 with every item PASS, Phase 3 was accepted and
        # closed, and PR #6 was merged. This block used to carry `corrected_software_deployed: false` and a
        # five-item `remaining_live_work` list, and it went on emitting them into every new artifact — so an
        # artifact generated after closure described finished work as outstanding, in the most authoritative
        # file a reviewer opens.
        #
        # The fix is not to delete the history. It is to label it, and to derive the CURRENT state from
        # governance/project-state.json -> current_state_facts rather than restating it in a literal here.
        # A fact recorded in one place can be diffed; a fact copied into a generator drifts silently.
        "live_increment9_historical": dict(
            increment9_historical,
            _record_type="HISTORICAL",
            _as_observed_on="2026-08-10",
            _superseded_note=increment9_superseded_note,
        ),
        # CURRENT project state at the moment this artifact was generated, read from the repository's
        # machine-readable facts. `remaining_live_work` is [] once the phase is closed — an empty list is the
        # honest answer, and it is computed rather than asserted.
        "project_state_at_generation": project_state_block,
    }

    with open(os.path.join(art, "RUN_META.json"), "w", encoding="utf-8", newline="\n") as f:
        json.dump(run_meta, f, indent=2)
        f.write("\n")

    # ---- copy the PII-free derived files into the artifact ------------------------
    # kernel-limitations.txt is copied when it exists: a limitation that is not in the artifact is a
    # limitation nobody reading the artifact knows about.
    for name in ["steps.tsv", "tool-versions.tsv", "infra-retries.tsv", "commands.txt", "kernel-limitations.txt"]:
        src = os.path.join(evid, name)
        if os.path.isfile(src):
            shutil.copyfile(src, os.path.join(art, name))
    for name in os.listdir(counts_dir) if os.path.isdir(counts_dir) else []:
        shutil.copyfile(os.path.join(counts_dir, name), os.path.join(art, "counts", name))

    # ---- strip the raw repository diff Playwright embeds in its JSON report ------------------------------
    #
    # On a pull_request event Playwright's git-info capture writes `config.metadata.gitDiff` — the PR's ENTIRE
    # diff, truncated at 100,000 characters — into playwright.json, and that file is copied into the artifact
    # verbatim. So the artifact has been shipping up to 100 KB of raw repository content while its own README
    # promises "derived summaries only". It went unnoticed because the diff is truncated in path order: every
    # earlier PR's 100 KB was used up long before it reached anything the hygiene gate objects to. A small PR
    # is what finally let the whole diff through, and the gate refused it — correctly, and for the second-order
    # reason rather than the first.
    #
    # Provenance is kept: the commit hash, subject and CI links stay. What is dropped is the unbounded blob,
    # which is not evidence about this gate and cannot be bounded by review.
    # Dropping `gitDiff` by name is a blocklist, and a blocklist is the wrong shape here: the reporter decides
    # what it puts in `metadata`, a version bump can add another unbounded field, and the artifact would carry
    # it until something happened to notice. So `metadata` is reduced to an ALLOWLIST of the provenance this
    # artifact actually needs — which run, which commit — and everything else goes, named or not.
    PW_METADATA_KEEP = {
        "ci": ("commitHref", "commitHash", "prHref", "prBaseHash", "buildHref"),
        "gitCommit": ("shortHash", "hash", "subject"),
        "actualWorkers": None,   # a scalar; kept whole
    }
    pw_path = os.path.join(art, "counts", "playwright.json")
    if os.path.isfile(pw_path):
        try:
            with io.open(pw_path, encoding="utf-8") as f:
                pw = json.load(f)
            meta = (pw.get("config") or {}).get("metadata")
            if isinstance(meta, dict):
                kept, dropped = {}, []
                for key, value in meta.items():
                    if key not in PW_METADATA_KEEP:
                        dropped.append(key)
                        continue
                    fields = PW_METADATA_KEEP[key]
                    if fields is None or not isinstance(value, dict):
                        kept[key] = value
                    else:
                        kept[key] = {k: v for k, v in value.items() if k in fields}
                        extra = [k for k in value if k not in fields]
                        if extra:
                            dropped.append("%s.{%s}" % (key, ",".join(sorted(extra))))
                pw["config"]["metadata"] = kept
                with io.open(pw_path, "w", encoding="utf-8", newline="\n") as f:
                    json.dump(pw, f, indent=2)
                    f.write("\n")
                if dropped:
                    print("counts/playwright.json: kept only allowlisted metadata; dropped %s"
                          % ", ".join(sorted(dropped)))
        except Exception as exc:  # noqa: BLE001
            # A report we cannot parse is a report we cannot certify as clean. Fail rather than ship it.
            sys.stderr.write("could not sanitise counts/playwright.json: %s\n" % exc)
            return 2

    # The complete dimensional acceptance matrix, as its own artifact file.
    if accept_matrix_md:
        with open(os.path.join(art, "PHASE3_ACCEPTANCE_MATRIX.md"), "w", encoding="utf-8", newline="\n") as f:
            f.write(accept_matrix_md)

    # Render the preflight checks into a human-readable file, from the structured output.
    if preflight and isinstance(preflight.get("checks"), list):
        lines = [f"Phase-3 offline preflight — {preflight.get('pass',0)} passed, {preflight.get('fail',0)} failed", ""]
        for c in preflight["checks"]:
            mark = "PASS" if c.get("status") == "PASS" else "FAIL"
            lines.append(f"  [{mark}] {c.get('check','')}")
        with open(os.path.join(art, "preflight.txt"), "w", encoding="utf-8", newline="\n") as f:
            f.write("\n".join(lines) + "\n")

    # ---- the acceptance matrix, derived — never hand-typed ------------------------
    def row(name, ok, detail):
        return f"| {name} | {'PASS' if ok else 'FAIL'} | {detail} |"

    m = []
    m.append("# Phase 3 — Software Acceptance Matrix")
    m.append("")
    m.append(f"Delivery HEAD `{env.get('delivery_head')}` · run `{env.get('run_id')}` · {end_utc}")
    m.append("")
    m.append("| Gate | Result | Detail |")
    m.append("| --- | --- | --- |")
    step_ok = {s["step"]: s["exit_code"] == 0 for s in steps}
    for s in steps:
        m.append(row(s["step"], s["exit_code"] == 0, f"exit {s['exit_code']}, {s['duration_seconds']}s"))
    m.append("")
    m.append("## Test totals (from the same runs that gated)")
    m.append("")
    if go_unit:
        m.append(f"- **Go unit** — {go_unit.get('pass',0)} passed, {go_unit.get('skip',0)} skipped, "
                 f"{go_unit.get('fail',0)} failed across {go_unit.get('packages_ok',0)} packages")
    if go_race:
        m.append(f"- **Go race** — {go_race.get('pass',0)} passed, {go_race.get('skip',0)} skipped, "
                 f"{go_race.get('fail',0)} failed")
    if totals["vitest"]:
        v = totals["vitest"]
        m.append(f"- **Vitest** — {v['passed']} passed, {v['skipped']} skipped, {v['failed']} failed "
                 f"of {v['total']}")
    if totals["playwright"]:
        p = totals["playwright"]
        m.append(f"- **Playwright** — {p['passed']} passed, {p['skipped']} skipped, {p['failed']} failed, "
                 f"{p.get('flaky',0)} flaky")
    if preflight:
        m.append(f"- **Preflight** — {preflight.get('pass',0)} passed, {preflight.get('fail',0)} failed")
    if totals.get("provisioning"):
        pr = totals["provisioning"]
        m.append(f"- **Staged-provisioning failure tests** — {pr['passed']} passed, {pr['failed']} failed "
                 "(accountable-before-forwarding)")
    if totals.get("network_enforcement"):
        ne = totals["network_enforcement"]
        m.append(f"- **Network-enforcement system tests** — {ne['passed']} passed, {ne['failed']} failed "
                 "(nft authorization + accountable tc + Session; FAKE-KERNEL orchestration + command contracts)")
    if totals.get("nft_foundation"):
        nf = totals["nft_foundation"]
        m.append(f"- **Surgical live-dark nft foundation** — {nf['passed']} passed, {nf['failed']} failed "
                 "(install/rollback preserving a populated legacy authorization set)")
    if totals.get("kernel_netns"):
        kn = totals["kernel_netns"]
        m.append(f"- **REAL-KERNEL contract suite** — {kn.get('passed','?')} passed, {kn.get('failed','?')} failed, "
                 f"{kn.get('skipped','0')} skipped, on kernel {kn.get('kernel','?')} with {kn.get('nft','?')} "
                 f"(host ruleset unchanged: {kn.get('host_ruleset_unchanged','?')})")
        m.append("  - This is the ONLY entry produced by running nft and tc against a kernel with real "
                 "packets. It ran in disposable network namespaces on the CI runner; it contacted no "
                 "appliance, no production database and no PMS, and it is NOT live appliance evidence.")
        if kn.get("ifb_available") == "0":
            m.append("  - LIMITATION: the ifb module was unavailable on this runner, so the tc half of the "
                     "suite was skipped. See `kernel-limitations.txt`.")
    m.append("")
    if migration_summary:
        m.append(f"## Migration 0010 lifecycle — {migration_summary['assertions_passed']} assertions passed, "
                 f"{migration_summary['assertions_failed']} failed")
        m.append("")
    if pg16_suites:
        m.append("## Disposable-PG16 integration suites")
        m.append("")
        for s in pg16_suites:
            m.append(f"- {s['result']} · `{s['suite']}`")
        m.append("")
    m.append("## Governance")
    m.append("")
    mp = manifest_parity
    m.append(f"- Embedded-report / generated-manifest parity: **{'MATCH' if mp['match'] else 'MISMATCH'}** "
             f"({mp['report_embedded_path_count']} report paths vs {mp['generated_path_count']} generated)")
    for k, v in zero_stale.items():
        m.append(f"- {k}: **{v['verdict']}**")
    m.append("")
    m.append(f"## Complete Phase-3 Acceptance Matrix — {len(accept_matrix_rows)} dimensions "
             "(including sub-dimensions; see `PHASE3_ACCEPTANCE_MATRIX.md`)")
    m.append("")
    verdict_counts = {}
    for rrow in accept_matrix_rows:
        key = re.sub(r"[*`]", "", rrow["verdict"]).strip()
        verdict_counts[key] = verdict_counts.get(key, 0) + 1
    for k in sorted(verdict_counts):
        m.append(f"- {k}: {verdict_counts[k]}")
    m.append("")
    with open(os.path.join(art, "ACCEPTANCE_MATRIX.md"), "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(m) + "\n")

    # The one sentence in this README that speaks about the PROJECT rather than the run. It said "the subset
    # of live work that remains" in every artifact ever generated, including the ones generated after that
    # work was finished, accepted and merged. It is now derived from the same facts the metadata is.
    if project_state_block.get("remaining_live_work"):
        project_state_sentence = (
            "CURRENT project state, read from `governance/project-state.json` at generation time and recorded "
            "in `RUN_META.json` → `project_state_at_generation`: %d item(s) of live work remain."
            % len(project_state_block["remaining_live_work"]))
    else:
        closed = project_state_block.get("accepted") and project_state_block.get("closed")
        project_state_sentence = (
            "CURRENT project state, read from `governance/project-state.json` at generation time and recorded "
            "in `RUN_META.json` → `project_state_at_generation`: %s NO live work remains outstanding. "
            "Nothing in the historical block above describes work that is still to be done."
            % ("Phase 3 is ACCEPTED and CLOSED, and" if closed else "the blocked subset was re-validated, and"))

    # ---- the human README --------------------------------------------------------
    readme = f"""# Phase 3 software-gate evidence

Generated by the Phase-3 Software workflow, run `{env.get('run_id')}`, on delivery HEAD
`{env.get('delivery_head')}` at {end_utc}.

## What this is, and is not

This is the evidence for the Phase-3 SOFTWARE gate: every mandatory backend and frontend
test, run in one workflow on one HEAD. It contains derived summaries only. Raw per-step
test logs are deliberately excluded — they can carry test-fixture names and room numbers,
and this artifact must contain no such data; the full logs remain in the workflow's own
job log.

This run contains NO live evidence: it contacted no appliance, no Production database and no
live PMS, and every database and network namespace it used was disposable.

That is a statement about THIS RUN, not about the project. Live Increment 9 was executed on
2026-08-10 under a separate Product-Owner authorization: it applied migration 0010 to the
production site database and deployed to the appliance, and it returned one blocker. Those
verdicts are preserved exactly as observed in `RUN_META.json` → `live_increment9_historical`,
which describes 2026-08-10 and no later date.

{project_state_sentence}

## Files

- `RUN_META.json` — HEADs, run id, UTC window, tool versions, lock/migration hashes, every
  step's exit code and duration, per-suite test totals and skip totals, infrastructure
  retries, this run's restrictions, the standing restrictions, the CURRENT project state read from
  `governance/project-state.json`, and the HISTORICAL Live Increment-9 verdicts as observed on 2026-08-10.
- `ACCEPTANCE_MATRIX.md` — one row per gate, derived from the recorded results.
- `steps.tsv` — the raw step ledger (slug, exit code, seconds).
- `counts/` — the per-suite machine counts, as emitted by each test runner's own reporter.
- `tool-versions.tsv`, `preflight.txt`, `commands.txt`, `infra-retries.tsv` — provenance.
- `MANIFEST.sha256` — SHA-256 of every file in this artifact EXCEPT itself.

## Verifying integrity

From inside this directory:

    sha256sum -c MANIFEST.sha256

`MANIFEST.sha256` cannot list its own hash (a file cannot contain its own digest). The
workflow prints the manifest's own SHA-256; that single value is the integrity root the
manifest is checked against, and it is recorded in the final report.
"""
    with open(os.path.join(art, "README.md"), "w", encoding="utf-8", newline="\n") as f:
        f.write(readme)

    # ---- PII / secret hygiene gate over the ARTIFACT (not the staging logs) -------
    forbidden = [
        re.compile(r"postgres://[^:\s]+:[^@\s]+@"),          # a credentialed DSN
        re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----"),   # a private key
        re.compile(r"956608a"),                              # the Phase-1A source commit
        re.compile(r"Phase-?1A", re.IGNORECASE),
        re.compile(r"Live-?Dark-?Acceptance", re.IGNORECASE),
    ]
    offenders = []
    for dirpath, _dirs, files in os.walk(art):
        for fn in files:
            fp = os.path.join(dirpath, fn)
            text = read_text(fp)
            for pat in forbidden:
                m = pat.search(text)
                if not m:
                    continue
                # Name the offending TEXT, not just the file. "counts/playwright.json :: Phase-?1A" says a
                # 700 KB machine-generated report contains something, somewhere — which is a fact, not a lead.
                # The surrounding characters are what turn it into one, and the pattern that fired here is
                # about document names rather than secrets, so a short window is safe to print.
                lo, hi = max(0, m.start() - 60), min(len(text), m.end() + 60)
                window = " ".join(text[lo:hi].split())
                offenders.append("%s :: %s\n      ...%s..."
                                 % (os.path.relpath(fp, art), pat.pattern, window))
    if offenders:
        sys.stderr.write("EVIDENCE HYGIENE FAILED — forbidden content in the artifact:\n")
        sys.stderr.write("\n".join("  " + o for o in offenders) + "\n")
        return 2

    # ---- the integrity manifest --------------------------------------------------
    entries = []
    for dirpath, _dirs, files in os.walk(art):
        for fn in files:
            fp = os.path.join(dirpath, fn)
            rel = os.path.relpath(fp, art).replace(os.sep, "/")
            if rel == "MANIFEST.sha256":
                continue
            entries.append((rel, sha256_file(fp)))
    entries.sort()
    man_path = os.path.join(art, "MANIFEST.sha256")
    with io.open(man_path, "w", encoding="utf-8", newline="\n") as f:
        for rel, digest in entries:
            f.write(f"{digest}  {rel}\n")
    manifest_root = sha256_file(man_path)

    print(f"evidence artifact assembled: {len(entries)} files under {art}")
    print(f"MANIFEST.sha256 covers {len(entries)} files (excludes itself)")
    print(f"integrity_manifest_sha256={manifest_root}")
    gh_out = os.environ.get("GITHUB_OUTPUT")
    if gh_out:
        with open(gh_out, "a", encoding="utf-8") as f:
            f.write(f"integrity_manifest_sha256={manifest_root}\n")
            f.write(f"artifact_file_count={len(entries)}\n")

    if not all_zero:
        sys.stderr.write("a gate step reported a non-zero exit; evidence records it but the gate did not pass\n")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
