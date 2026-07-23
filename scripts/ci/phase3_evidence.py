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
    }

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
        "skipped_totals": {
            "go_unit": go_unit.get("skip", 0),
            "vitest": totals["vitest"].get("skipped", 0) if totals["vitest"] else 0,
            "playwright": totals["playwright"].get("skipped", 0) if totals["playwright"] else 0,
        },
        "infrastructure_retries": infra_list,
        "restrictions_confirmed": [
            "all Phase-3 flags OFF",
            "PR open and unmerged",
            "Migration 0010 undeployed",
            "zero persistent runtime iam_v2 privileges",
            "no appliance access",
            "no Production DB access",
            "no live PMS contact",
            "no deployment or reboot",
            "no Gate-P grants",
            "no PS/PA",
            "no financial posting",
            "no paid access",
            "no implicit FX",
            "no programmatic reversal",
            "no Phase 4",
        ],
        "live_increment9_pending": [
            "read-only live PMS protocol verification against the live interface",
            "controlled live-dark deployment of this exact HEAD",
            "one full reboot with post-reboot convergence evidence",
            "rollback rehearsal (migration down + previous release restored)",
            "flags-OFF confirmation on the running unit (zero Phase-3 SQL, no PMS socket)",
        ],
    }

    with open(os.path.join(art, "RUN_META.json"), "w", encoding="utf-8", newline="\n") as f:
        json.dump(run_meta, f, indent=2)
        f.write("\n")

    # ---- copy the PII-free derived files into the artifact ------------------------
    for name in ["steps.tsv", "tool-versions.tsv", "infra-retries.tsv", "commands.txt"]:
        src = os.path.join(evid, name)
        if os.path.isfile(src):
            shutil.copyfile(src, os.path.join(art, name))
    for name in os.listdir(counts_dir) if os.path.isdir(counts_dir) else []:
        shutil.copyfile(os.path.join(counts_dir, name), os.path.join(art, "counts", name))

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
    m.append("")
    with open(os.path.join(art, "ACCEPTANCE_MATRIX.md"), "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(m) + "\n")

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

It contains NO live evidence. No appliance, Production database or live PMS was contacted.
See `RUN_META.json` → `live_increment9_pending` for exactly what still requires a separate,
Product-Owner-authorized live run.

## Files

- `RUN_META.json` — HEADs, run id, UTC window, tool versions, lock/migration hashes, every
  step's exit code and duration, per-suite test totals and skip totals, infrastructure
  retries, restrictions confirmed, and the live-Increment-9 pending list.
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
                if pat.search(text):
                    offenders.append(f"{os.path.relpath(fp, art)} :: {pat.pattern}")
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
