#!/usr/bin/env python3
"""
project-state.py — StayConnect Enterprise project-state governance (Python 3 stdlib only).

Single machine-readable current-state source: governance/project-state.json.
Append-only transitions: governance/transitions/*.json.
Decision register: governance/decision-register.json. Artifact registry: governance/artifact-registry.json.

Commands:
  validate        structural validation (PROJECT_STATE_GOVERNANCE = PASS/FAIL); exit non-zero on fail
  render          inject generated status blocks into current-status carriers from the canonical state
  check-generated verify on-disk generated blocks exactly match the rendered output; fail on drift
  transition      append a new immutable transition receipt (governance/transitions/T####.json)
  build-packs     deterministic export (refuse dirty tree, validate, render-check, build, extract, re-validate)

Generated blocks are delimited by:
  <!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->
  ...
  <!-- END GENERATED PROJECT STATE -->
Dynamic current-state wording must live ONLY inside these blocks.
"""
import json, os, sys, re, hashlib, subprocess, datetime, glob, io, zipfile, shutil, tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GOV = os.path.join(ROOT, "governance")
STATE = os.path.join(GOV, "project-state.json")
DECISIONS = os.path.join(GOV, "decision-register.json")
ARTIFACTS = os.path.join(GOV, "artifact-registry.json")
TRANSITIONS = os.path.join(GOV, "transitions")
PACK = os.path.join(ROOT, "exports/chatgpt/stayconnectenterprise")
EVID = os.path.join(ROOT, "exports/chatgpt/phase-evidence")
PLAN_PACK = os.path.join(ROOT, "exports/chatgpt/phase1b-planning")

BEGIN = "<!-- BEGIN GENERATED PROJECT STATE — DO NOT EDIT -->"
END = "<!-- END GENERATED PROJECT STATE -->"

# phase-status lattice (higher = more advanced; a phase must never regress)
STATUS_RANK = {"NOT_STARTED": 0, "PLANNING": 1, "IN_PROGRESS": 2, "DARK_CREATED": 3,
               "ACCEPTED_AND_CLOSED": 4, "FINAL_CLOSED": 5}
CLOSED = {"ACCEPTED_AND_CLOSED", "FINAL_CLOSED"}

# files that carry a generated status block; (path, anchor_after_line_containing)
BLOCK_TARGETS = [
    (os.path.join(ROOT, "docs/context/StayConnect-IAM-Handoff.md"), "# StayConnect IAM"),
    (os.path.join(ROOT, "docs/architecture/StayConnect-IAM-Phase1B-Plan.md"), "# StayConnect IAM — Phase 1B"),
    (os.path.join(ROOT, "docs/architecture/StayConnect-IAM-Phase1A-Plan.md"), "# StayConnect IAM — Phase 1A"),
    (os.path.join(ROOT, "docs/architecture/StayConnect-IAM-Phase0-Contract.md"), "# StayConnect Internet Access Management"),
    (os.path.join(PACK, "00-START-HERE.md"), "# StayConnect Enterprise — START HERE"),
    (os.path.join(PACK, "PROJECT-INSTRUCTIONS.md"), "# StayConnect Enterprise — ChatGPT Project Instructions"),
]

def load(p):
    with open(p, encoding="utf-8") as f: return json.load(f)
def sha256_bytes(b):
    h = hashlib.sha256(); h.update(b); return h.hexdigest()
def sha256_file(p):
    h = hashlib.sha256()
    with open(p, "rb") as f:
        for c in iter(lambda: f.read(65536), b""): h.update(c)
    return h.hexdigest()
def git(*a):
    return subprocess.run(["git", "-C", ROOT, *a], capture_output=True, text=True).stdout.strip()

# ---------- generated block ----------
def render_block(st):
    ph = st["phases"]
    def s(k): return ph[k]["status"]
    p1b_note = "(DARK — implementation in progress; no production iam_v2 use)" if s('1B') == "IN_PROGRESS" else "(NOT implemented)"
    lines = [BEGIN,
        f"<!-- source: governance/project-state.json (schema {st['schema_version']}) @ transition {st['latest_transition_id']} -->",
        f"**Current phase:** {st['current_phase']} — {ph[st['current_phase']].get('title','')}",
        f"**Current activity:** `{st['current_activity']}`",
        f"**Phase status:** 0 {s('0')} · 1A **{s('1A')}** (DARK, NOT CUT OVER) · 1B {s('1B')} {p1b_note} · 2 {s('2')} · 3 {s('3')} · 4 {s('4')} · 5 {s('5')} · 6 {s('6')} · 7 {s('7')}",
        f"**Phase 1A maturity:** {ph['1A']['maturity']}",
        f"**iam_v2:** {st['database_schema_state']['iam_v2_tables']} tables, {st['database_schema_state']['iam_v2_rows']} rows, dark; no service routed; no data migration; legacy public schema is the sole production authority.",
        f"**Single next authorized action:** {st['next_authorized_action']}",
        f"**Governance:** current state is generated from `governance/project-state.json`; do not edit this block by hand. Latest accepted PO decision: `{st['latest_accepted_po_decision']}`.",
        END]
    return "\n".join(lines)

def inject(text, block, anchor):
    if BEGIN in text and END in text:
        return re.sub(re.escape(BEGIN) + r".*?" + re.escape(END), lambda m: block, text, count=1, flags=re.S)
    # insert after the anchor line (or after first line if not found)
    lines = text.split("\n")
    idx = 0
    for i, ln in enumerate(lines):
        if anchor in ln: idx = i; break
    out = lines[:idx+1] + ["", block, ""] + lines[idx+1:]
    return "\n".join(out)

def cmd_render(st, write=True):
    block = render_block(st)
    changed = []
    for path, anchor in BLOCK_TARGETS:
        if not os.path.isfile(path):
            print(f"  MISSING target: {path}"); continue
        with open(path, encoding="utf-8") as f: text = f.read()
        new = inject(text, block, anchor)
        if new != text:
            changed.append(os.path.relpath(path, ROOT))
            if write:
                with open(path, "w", encoding="utf-8", newline="\n") as f: f.write(new)
    return block, changed

def cmd_check_generated(st):
    block = render_block(st)
    bad = []
    for path, _ in BLOCK_TARGETS:
        if not os.path.isfile(path): bad.append((path, "missing")); continue
        with open(path, encoding="utf-8") as f: text = f.read()
        m = re.search(re.escape(BEGIN) + r".*?" + re.escape(END), text, flags=re.S)
        if not m: bad.append((path, "no generated block")); continue
        if m.group(0).replace("\r\n","\n") != block: bad.append((path, "block drift vs canonical state"))
    return bad

# ---------- validation ----------
def cmd_validate(deep=True):
    fails = []
    def fail(m): fails.append(m)
    # JSON parse + required fields
    try: st = load(STATE)
    except Exception as e: print(f"FAIL: project-state.json parse: {e}"); print("PROJECT_STATE_GOVERNANCE = FAIL"); return 1, None
    req = ["schema_version","project","current_phase","current_activity","current_maturity","phases",
           "next_authorized_action","current_phase_plan","latest_accepted_po_decision","latest_transition_id",
           "prohibited_actions","allowed_actions","live_scratch_dark_cutover","database_schema_state",
           "authoritative_remote","delivery_governance"]
    for k in req:
        if k not in st: fail(f"project-state.json missing required field: {k}")
    try: dec = load(DECISIONS)
    except Exception as e: dec = {"decisions": []}; fail(f"decision-register.json parse: {e}")
    try: art = load(ARTIFACTS)
    except Exception as e: art = {"artifacts": []}; fail(f"artifact-registry.json parse: {e}")
    trans = []
    for p in sorted(glob.glob(os.path.join(TRANSITIONS, "*.json"))):
        try: trans.append(load(p))
        except Exception as e: fail(f"transition {os.path.basename(p)} parse: {e}")

    # exactly one current phase / activity / next action
    if not isinstance(st.get("current_phase"), str) or not st.get("current_phase"): fail("no single current phase")
    if not isinstance(st.get("current_activity"), str) or not st.get("current_activity"): fail("no single current activity")
    na = st.get("next_authorized_action")
    if not isinstance(na, str) or not na: fail("no single next authorized action")
    elif (";" in na) or re.search(r"\band then\b|\balso\b", na) or "\n" in na: fail("next_authorized_action is not a single action")
    # at most one non-closed 'current' phase (status PLANNING or IN_PROGRESS), and it must be current_phase
    open_phases = [k for k, v in st.get("phases", {}).items() if v.get("status") in ("PLANNING", "IN_PROGRESS")]
    if len(open_phases) > 1: fail(f"more than one current/open phase: {open_phases}")
    if open_phases and open_phases[0] != st.get("current_phase"): fail(f"open phase {open_phases[0]} != current_phase {st.get('current_phase')}")

    # transitions ordered + snapshot matches latest + no regression + closed cannot reopen
    if trans:
        seqs = [t.get("seq", -1) for t in trans]
        if seqs != sorted(seqs) or len(set(seqs)) != len(seqs): fail("transitions not strictly ordered/unique by seq")
        latest = max(trans, key=lambda t: t.get("seq", -1))
        if st.get("latest_transition_id") != latest.get("transition_id"):
            fail(f"snapshot latest_transition_id {st.get('latest_transition_id')} != latest ledger {latest.get('transition_id')}")
        ns = latest.get("new_state", {})
        if ns.get("phase") != st.get("current_phase"): fail("snapshot current_phase != latest transition new_state.phase")
        if ns.get("activity") != st.get("current_activity"): fail("snapshot current_activity != latest transition new_state.activity")
        # per-phase monotonic non-regression across ordered transitions
        seen = {}
        for t in sorted(trans, key=lambda x: x.get("seq", -1)):
            ph = t.get("phase_affected"); nstat = t.get("new_state", {}).get("phase_status")
            if ph is None or nstat is None: continue
            if nstat not in STATUS_RANK: fail(f"{t['transition_id']}: unknown phase_status {nstat}"); continue
            prev = seen.get(ph)
            if prev is not None and STATUS_RANK[nstat] < STATUS_RANK[prev]:
                fail(f"{t['transition_id']}: phase {ph} regressed {prev} -> {nstat}")
            seen[ph] = nstat

    # Phase 1A cannot appear pending/current/not-started; must be closed/accepted
    p1a = st["phases"].get("1A", {}).get("status")
    if p1a not in CLOSED: fail(f"Phase 1A status {p1a} is not ACCEPTED_AND_CLOSED/FINAL_CLOSED (must not be pending/current/not-started)")
    if st.get("current_phase") == "1A": fail("Phase 1A must not be the current phase")
    # exactly one non-closed 'current' phase in {1B..} and it equals current_phase
    p1b = st["phases"].get("1B", {}).get("status")
    if p1b not in ("PLANNING", "IN_PROGRESS"): fail(f"Phase 1B status {p1b} must be PLANNING or IN_PROGRESS (not accepted/closed/cutover until PO acceptance)")
    if st["live_scratch_dark_cutover"].get("cutover_performed"): fail("cutover_performed must be false")
    if st["live_scratch_dark_cutover"].get("live_iam_v2_in_use"): fail("live_iam_v2_in_use must be false in Phase 1B")
    if st["database_schema_state"].get("iam_v2_data_migration"): fail("iam_v2_data_migration must be false")

    # decision register agreement: D1 not reopenable; D9 active; required decisions present
    dmap = {d["id"]: d for d in dec.get("decisions", [])}
    for need in ["D1","D2","D3","D4","D5","D6","D7","D8","D9","PH-1B-SCOPE","PH-CUTOVER","SEC-NO-IAMV2-PRIV",
                 "GH-SOURCE-OF-TRUTH","GH-BRANCH-PR","GH-COMPLETE-MANIFEST","GH-FINAL-REPORT","GH-MANDATORY-CI",
                 "GH-AGENT-ONLY-OPERATIONS","GH-LF-CONSISTENCY"]:
        if need not in dmap: fail(f"decision-register missing {need}")
    if dmap.get("D1", {}).get("reopenable", False) is not False: fail("D1 must not be reopenable")
    if dmap.get("D1", {}).get("status") != "ACTIVE": fail("D1 must be ACTIVE")
    if dmap.get("D9", {}).get("status") != "ACTIVE": fail("D9 (Phase 1A accepted) must be ACTIVE")

    # GitHub execution & delivery governance (permanent rule)
    remote = st.get("authoritative_remote","")
    if "aibrahiiim1/StayConnectEnterprise" not in remote:
        fail(f"authoritative_remote must point to aibrahiiim1/StayConnectEnterprise (got: {remote!r})")
    dg = st.get("delivery_governance", {})
    for key, want in [("rule_doc","docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md"),
                      ("manifest_tool","tools/generate-change-manifest.py"),
                      ("report_template","docs/templates/PHASE_FINAL_REPORT_TEMPLATE.md"),
                      ("ci_workflow",".github/workflows/project-governance.yml")]:
        got = dg.get(key)
        if got != want: fail(f"delivery_governance.{key} must be '{want}' (got: {got!r})")
        elif not os.path.isfile(os.path.join(ROOT, want)): fail(f"delivery_governance {key} file missing on disk: {want}")

    # mandatory GitHub Actions governance CI must exist, run on PRs to master, run every required
    # command, and not be weakened (GH-MANDATORY-CI). Text checks only (stdlib; no YAML dependency).
    wf = os.path.join(ROOT, ".github/workflows/project-governance.yml")
    if not os.path.isfile(wf):
        fail("mandatory governance CI missing: .github/workflows/project-governance.yml (GH-MANDATORY-CI)")
    else:
        w = open(wf, encoding="utf-8").read()
        if "pull_request:" not in w: fail("governance CI must run on pull_request (targeting master)")
        if "master" not in w: fail("governance CI must target the master branch")
        if "workflow_dispatch" not in w: fail("governance CI must allow manual workflow_dispatch")
        for cmd in ["tools/project-state.py validate", "tools/project-state.py check-generated",
                    "tools/tests/project_state_validator/run_mutations.py", "tools/validate-project-state.sh"]:
            if cmd not in w: fail(f"governance CI missing required validation command: {cmd}")
        if re.search(r"continue-on-error:\s*true", w): fail("governance CI must not ignore failures (continue-on-error: true)")
        if "git status --porcelain" not in w: fail("governance CI must assert a clean working tree")
        if "Project Governance" not in w: fail("governance CI workflow name must be 'Project Governance'")
        if not re.search(r"(?m)^\s{2,}governance:\s*$", w): fail("governance CI must define the job id 'governance'")

    # cross-platform LF consistency: a committed .gitattributes pins text to LF and marks ZIP binary
    # (GH-LF-CONSISTENCY) so Windows/Linux/CI produce byte-identical, checksum-stable pack files.
    ga = os.path.join(ROOT, ".gitattributes")
    if not os.path.isfile(ga):
        fail("missing .gitattributes (GH-LF-CONSISTENCY): checksum-controlled text must be pinned to LF")
    else:
        g = open(ga, encoding="utf-8").read()
        if "eol=lf" not in g: fail(".gitattributes must pin text to eol=lf (GH-LF-CONSISTENCY)")
        if not re.search(r"(?m)^\*\.zip\s+binary\b", g): fail(".gitattributes must mark *.zip as binary (protect pack checksums)")
        if not re.search(r"(?m)^\*\s+text=auto\s+eol=lf\b", g): fail(".gitattributes must default '* text=auto eol=lf'")
    if dg.get("operations_owner") != "AGENT":
        fail(f"delivery_governance.operations_owner must be 'AGENT' (got: {dg.get('operations_owner')!r})")
    rd_path = os.path.join(ROOT, "docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md")
    if os.path.isfile(rd_path):
        rd = open(rd_path, encoding="utf-8").read()
        for tok in ["aibrahiiim1/StayConnectEnterprise","CREATED","MODIFIED","DELETED","RENAMED",
                    "COPIED","GENERATED","EXPORTED","UNCHANGED-BUT-VERIFIED"]:
            if tok not in rd: fail(f"GitHub delivery rule doc missing required token: {tok}")
        # agent-owned Git/GitHub operations (GH-AGENT-ONLY-OPERATIONS)
        if "GIT_OPERATIONS_OWNER: AGENT" not in rd:
            fail("GitHub delivery rule doc missing assertion GIT_OPERATIONS_OWNER: AGENT")
        if re.search(r"GIT_OPERATIONS_OWNER:\s*(PRODUCT_OWNER|PO\b|MANUAL|HUMAN)", rd):
            fail("GitHub delivery rule doc must not assign Git operations to the Product Owner (GIT_OPERATIONS_OWNER must be AGENT)")

    # current phase plan matches canonical + exists; privilege matrix agreement
    plan = st.get("current_phase_plan")
    if not plan or not os.path.isfile(os.path.join(ROOT, plan)): fail(f"current_phase_plan missing/not found: {plan}")
    mtx = st.get("privilege_matrix")
    if not mtx or not os.path.isfile(os.path.join(ROOT, mtx)): fail(f"privilege_matrix missing/not found: {mtx}")
    else:
        mt = open(os.path.join(ROOT, mtx), encoding="utf-8").read()
        # machine assertion: production runtime roles hold ZERO iam_v2 DML/EXECUTE
        if "PRODUCTION_IAM_V2_DML: NONE" not in mt: fail("privilege matrix missing machine assertion PRODUCTION_IAM_V2_DML: NONE")
        if re.search(r"PRODUCTION_IAM_V2_DML:\s*(GRANTED|SOME|ANY)", mt): fail("privilege matrix asserts a production iam_v2 grant (must be NONE)")
        pt = open(os.path.join(ROOT, plan), encoding="utf-8").read() if plan else ""
        if "Phase1B-Privilege-Matrix.md" not in pt: fail("Phase 1B plan does not reference the privilege matrix")
        for phrase in ["zero production `iam_v2` write", "ZERO `iam_v2` DML"]:
            pass  # soft
        if "rolled-back" not in pt.lower(): fail("Phase 1B plan must address rolled-back-transaction writes (D1)")

    # verified evidence + authoritative files exist
    for ev in st.get("verified_evidence", []):
        if not os.path.isfile(os.path.join(ROOT, ev["path"])): fail(f"verified evidence missing: {ev['path']}")
    for a in art.get("artifacts", []):
        pth = a.get("path","")
        # only check file-like authoritative/generated single-file artifacts
        if a.get("status") in ("AUTHORITATIVE","GENERATED") and pth.endswith((".md",".json",".py",".sh",".zip")):
            if not os.path.isfile(os.path.join(ROOT, pth)): fail(f"artifact missing on disk: {pth} ({a['id']})")
    # retained/deprecated/blocked must carry a removal_gate
    for a in art.get("artifacts", []):
        if a.get("status") in ("RETAINED","DEPRECATED","BLOCKED"):
            if not a.get("removal_gate"): fail(f"artifact {a['id']} ({a['status']}) has no removal_gate")
            if "runtime_active" not in a: fail(f"artifact {a['id']} ({a['status']}) missing runtime_active")
    # no active runtime path classified HISTORICAL_ONLY
    for a in art.get("artifacts", []):
        if a.get("status") == "HISTORICAL_ONLY" and a.get("runtime_active"): fail(f"active runtime path {a['id']} classified HISTORICAL_ONLY")

    # generated blocks match canonical state
    if deep:
        for path, why in cmd_check_generated(st):
            fail(f"generated block: {os.path.relpath(path, ROOT)} — {why}")

    ok = len(fails) == 0
    for m in fails: print(f"  FAIL: {m}")
    print("PROJECT_STATE_GOVERNANCE =", "PASS" if ok else f"FAIL ({len(fails)})")
    return (0 if ok else 1), st

# ---------- transition ----------
def cmd_transition(args):
    # tools/project-state.py transition <file.json>
    if not args: print("usage: transition <receipt.json>"); return 2
    src = args[0]
    rec = load(src)
    dst = os.path.join(TRANSITIONS, f"{rec['transition_id']}.json")
    if os.path.exists(dst): print(f"refuse: {dst} exists (append-only)"); return 1
    with open(dst, "w", encoding="utf-8", newline="\n") as f: json.dump(rec, f, indent=2)
    print(f"appended {dst}")
    return 0

# ---------- deterministic pack assembly ----------
PII = [("14215","«ROOM-REDACTED»"),("262224","«RES-REDACTED»"),("3c2ffe67","«redacted»"),("81a3edc5","«redacted»")]
# pack filename -> (source path rel to ROOT, sanitize mode: None|'pii'|'spike')
PACK_DOCS = {
 "StayConnect-IAM-Phase0-Contract.md": ("docs/architecture/StayConnect-IAM-Phase0-Contract.md","pii"),
 "StayConnect-IAM-Handoff.md": ("docs/context/StayConnect-IAM-Handoff.md",None),
 "StayConnect-IAM-Phase1A-Plan.md": ("docs/architecture/StayConnect-IAM-Phase1A-Plan.md",None),
 "StayConnect-IAM-Phase1B-Plan.md": ("docs/architecture/StayConnect-IAM-Phase1B-Plan.md",None),
 "Phase1B-Privilege-Matrix.md": ("docs/architecture/Phase1B-Privilege-Matrix.md",None),
 "StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md": ("docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md",None),
 "Protel-FIAS-Phase0-Spike.md": ("docs/spikes/Protel-FIAS-Phase0-Spike.md","spike"),
 "ZERO_STALE_LEFTOVERS_RULE.md": ("docs/ZERO_STALE_LEFTOVERS_RULE.md",None),
 "GITHUB_EXECUTION_AND_DELIVERY_RULE.md": ("docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md",None),
 "SYSTEM_OVERVIEW.md": ("docs/SYSTEM_OVERVIEW.md",None),
 "TARGET_ARCHITECTURE.md": ("docs/TARGET_ARCHITECTURE.md",None),
 "STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md": ("docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md",None),
 "DEPLOYMENT_APPLIANCE.md": ("docs/DEPLOYMENT_APPLIANCE.md",None),
 "OFFLINE_OPERATION.md": ("docs/OFFLINE_OPERATION.md",None),
 "MIGRATION_RUNBOOK.md": ("docs/MIGRATION_RUNBOOK.md",None),
}
PACKED_NAMES = set(PACK_DOCS) | {"00-START-HERE.md","PROJECT-INSTRUCTIONS.md","MANIFEST.md"}
# MANIFEST row order + status label
MROWS = [
 ("00-START-HERE.md","*(generated)*","Entry point"),
 ("PROJECT-INSTRUCTIONS.md","*(generated)*","Project config"),
 ("StayConnect-IAM-Phase0-Contract.md","`docs/architecture/StayConnect-IAM-Phase0-Contract.md`","**Authoritative** *(sanitized)*"),
 ("StayConnect-IAM-Handoff.md","`docs/context/StayConnect-IAM-Handoff.md`","**Authoritative**"),
 ("StayConnect-IAM-Phase1A-Plan.md","`docs/architecture/StayConnect-IAM-Phase1A-Plan.md`","**Authoritative (closed phase)**"),
 ("StayConnect-IAM-Phase1B-Plan.md","`docs/architecture/StayConnect-IAM-Phase1B-Plan.md`","**Authoritative (planning-only)**"),
 ("Phase1B-Privilege-Matrix.md","`docs/architecture/Phase1B-Privilege-Matrix.md`","**Authoritative (planning-only) — grant matrix**"),
 ("StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md","`docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md`","**Authoritative (acceptance record)**"),
 ("Protel-FIAS-Phase0-Spike.md","`docs/spikes/Protel-FIAS-Phase0-Spike.md`","**Authoritative** *(sanitized)*"),
 ("ZERO_STALE_LEFTOVERS_RULE.md","`docs/ZERO_STALE_LEFTOVERS_RULE.md`","**Permanent rule**"),
 ("GITHUB_EXECUTION_AND_DELIVERY_RULE.md","`docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md`","**Permanent rule**"),
 ("SYSTEM_OVERVIEW.md","`docs/SYSTEM_OVERVIEW.md`","Historical snapshot"),
 ("TARGET_ARCHITECTURE.md","`docs/TARGET_ARCHITECTURE.md`","Supporting"),
 ("STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md","`docs/STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md`","Supporting"),
 ("DEPLOYMENT_APPLIANCE.md","`docs/DEPLOYMENT_APPLIANCE.md`","Supporting"),
 ("OFFLINE_OPERATION.md","`docs/OFFLINE_OPERATION.md`","Supporting"),
 ("MIGRATION_RUNBOOK.md","`docs/MIGRATION_RUNBOOK.md`","Supporting"),
]
def _sanitize_pii(t):
    for a,b in PII: t=t.replace(a,b)
    return t
def _flatten(t):
    def repl(m):
        txt,tgt=m.group(1),m.group(2)
        if tgt.startswith(("http://","https://","mailto:")): return m.group(0)
        path,_,anc=tgt.partition("#"); base=path.rsplit("/",1)[-1]
        if base in PACKED_NAMES: return f"[{txt}]({base}"+(f"#{anc}" if anc else "")+")"
        if anc and not path: return m.group(0)
        return txt
    return re.sub(r"\[([^\]]+)\]\(([^)]+)\)",repl,t)
def _w(p,txt):
    os.makedirs(os.path.dirname(p),exist_ok=True)
    with open(p,"w",encoding="utf-8",newline="\n") as f: f.write(txt)
def _cp(src,dst):
    os.makedirs(os.path.dirname(dst),exist_ok=True); shutil.copyfile(src,dst)

def _build_project_pack(block, src_commit, tid, schema, ts):
    for name,(rel,mode) in PACK_DOCS.items():
        t=open(os.path.join(ROOT,rel),encoding="utf-8").read()
        if mode=="pii": t=_sanitize_pii(t)
        elif mode=="spike":
            t=_sanitize_pii(t)
            note="> **Export note:** guest-linked identifiers redacted for external sharing; technical findings preserved verbatim."
            if note not in t:
                t=t.replace("**Spike status:", note+"\n\n**Spike status:",1)
        t=_flatten(t)
        if re.search("|".join(re.escape(a) for a,_ in PII), t): raise SystemExit(f"PII leak in {name}")
        _w(os.path.join(PACK,name),t)
    # MANIFEST (generated block + new provenance; SOURCE_COMMIT only, no self-reference export stamp)
    L=[f"# StayConnect Enterprise — ChatGPT Project Pack MANIFEST","",block,"",
       "## Provenance",
       f"- **SOURCE_COMMIT (clean source this pack was built from):** `{src_commit}`",
       f"- **State transition:** `{tid}`  ·  **schema:** `{schema}`  ·  **build timestamp:** `{ts}`",
       "- **PROJECT_PACK_EXPORT_COMMIT:** *external* — the commit that commits this pack (recorded in the execution report; a pack never contains the commit that commits it). Verify with `git log -1 -- exports/chatgpt/stayconnectenterprise`.",
       "- **Sanitization:** guest-linked identifiers redacted in the two *(sanitized)* files; no secrets/DSNs/guest PII.","",
       "## Files","","| # | Exported filename | Original repository path | Source | Status | SHA-256 |","|---|---|---|---|---|---|"]
    for i,(fn,src,stt) in enumerate(MROWS,1):
        L.append(f"| {i} | `{fn}` | {src} | `{src_commit}` | {stt} | `{sha256_file(os.path.join(PACK,fn))}` |")
    L += ["","*(MANIFEST is not self-referential.)*","",
          "## Content checksum",
          "- pack_content_sha256 is the SHA-256 over the sorted `sha256(file)` lines of all non-MANIFEST pack files (see PACK_SHA256SUMS in the Evidence/Planning packs for physical lists)."]
    _w(os.path.join(PACK,"MANIFEST.md"),"\n".join(L)+"\n")

def _build_evidence_pack(src_commit):
    for fn in ["CATALOG_FINGERPRINT.txt","COMMAND_LOG.txt","CONSTRAINT_INVENTORY.txt","DEVIATIONS.md","FIDELITY_MATRIX.md",
               "OBJECT_INVENTORY.txt","ROLE_GRANT_INVENTORY.txt","SHA256SUMS.txt","TEST_MATRIX.md","TRIGGER_FUNCTION_INVENTORY.txt"]:
        _cp(os.path.join(ROOT,"iam_v2_scratch/review",fn),os.path.join(EVID,"review",fn))
    for fn in ["PROD_LIVE_DARK_EVIDENCE_V2.txt","PROD_LIVE_DARK_EVIDENCE.txt"]:
        _cp(os.path.join(ROOT,"iam_v2_scratch/review/prod",fn),os.path.join(EVID,"review/prod",fn))
    _cp(os.path.join(ROOT,"iam_v2_scratch/EVIDENCE.txt"),os.path.join(EVID,"EVIDENCE.txt"))
    _cp(os.path.join(ROOT,"docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md"),os.path.join(EVID,"StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md"))
    _cp(os.path.join(ROOT,"tools/validate-project-state.sh"),os.path.join(EVID,"tools/validate-project-state.sh"))
    _cp(os.path.join(ROOT,"tools/project-state.py"),os.path.join(EVID,"tools/project-state.py"))
    migs=[os.path.join("iam_v2_scratch",x) for x in (["mg0.sh"]+["migrations/"+f for f in sorted(os.listdir(os.path.join(ROOT,"iam_v2_scratch/migrations")))])]
    _w(os.path.join(EVID,"MIGRATION_CHECKSUMS.txt"),"# SHA-256 of committed iam_v2 migration groups\n"+"".join(f"{sha256_file(os.path.join(ROOT,m))}  {m}\n" for m in migs))
    _w(os.path.join(EVID,f"GIT_STAT_{src_commit}.txt"),git("show","--stat","--no-patch","--format=commit %H%nDate:   %ad%n%n    %s%n",src_commit)+"\n")
    for old in glob.glob(os.path.join(EVID,"GIT_STAT_*.txt")):
        if os.path.basename(old)!=f"GIT_STAT_{src_commit}.txt": os.remove(old)
    repo_art=["iam_v2_scratch/prod_verify.sql","iam_v2_scratch/prod_live_dark.sh","iam_v2_scratch/mg0.sh","iam_v2_scratch/roles.sql",
      "docs/ZERO_STALE_LEFTOVERS_RULE.md","governance/project-state.json","governance/decision-register.json","governance/artifact-registry.json"]+["iam_v2_scratch/migrations/"+f for f in sorted(os.listdir(os.path.join(ROOT,"iam_v2_scratch/migrations")))]
    _w(os.path.join(EVID,"REPOSITORY_ARTIFACT_SHA256SUMS.txt"),f"# repo artifacts referenced, not packaged. Verify at SOURCE_COMMIT {src_commit}.\n"+"".join(f"{sha256_file(os.path.join(ROOT,a))}  {a}\n" for a in repo_art))
    _pack_sums(EVID)

def _build_planning_pack(src_commit):
    def flat_copy(rel,dst):
        t=open(os.path.join(ROOT,rel),encoding="utf-8").read()
        t=re.sub(r"\[([^\]]+)\]\(([^)#][^)]*)\)",lambda m:(m.group(0) if m.group(2).startswith(("http","mailto")) else m.group(1)),t)
        _w(os.path.join(PLAN_PACK,dst),t)
    flat_copy("docs/architecture/StayConnect-IAM-Phase1B-Plan.md","StayConnect-IAM-Phase1B-Plan.md")
    flat_copy("docs/architecture/Phase1B-Privilege-Matrix.md","Phase1B-Privilege-Matrix.md")
    _w(os.path.join(PLAN_PACK,"MANIFEST.md"),f"# Phase 1B Planning Evidence Pack — MANIFEST\n\n- SOURCE_COMMIT: `{src_commit}`\n- Status: PLANNING ONLY — Phase 1B NOT implemented. Phase 1A accepted/closed.\n- Contents: Phase 1B plan (matrices+blueprint), privilege matrix, three code inventories, blueprint extract, README, two checksum lists.\n- Next authorized action: Product-Owner approval or rejection of the corrected Phase 1B plan.\n")
    cited=["docs/architecture/StayConnect-IAM-Phase1B-Plan.md","docs/architecture/Phase1B-Privilege-Matrix.md","data-plane/internal/pmsguard/guard.go",
      "data-plane/cmd/scd/main.go","data-plane/cmd/acctd/main.go","data-plane/cmd/edged/main.go","data-plane/cmd/netd/main.go","data-plane/cmd/portald/main.go",
      "data-plane/internal/session/session.go","data-plane/internal/voucher/voucher.go","data-plane/internal/otp/otp.go",
      "data-plane/cmd/scd/credentials_handlers.go","data-plane/cmd/scd/otp_handlers.go","data-plane/cmd/scd/social_handlers.go","data-plane/cmd/scd/pms_handlers.go",
      "iam_v2_scratch/roles.sql"]+["iam_v2_scratch/migrations/"+f for f in sorted(os.listdir(os.path.join(ROOT,"iam_v2_scratch/migrations")))]
    _w(os.path.join(PLAN_PACK,"REPOSITORY_ARTIFACT_SHA256SUMS.txt"),f"# cited committed source, not packaged. Verify at SOURCE_COMMIT {src_commit}.\n"+"".join((f"{sha256_file(os.path.join(ROOT,a))}  {a}\n" if os.path.isfile(os.path.join(ROOT,a)) else f"MISSING  {a}\n") for a in cited))
    _pack_sums(PLAN_PACK)

def _pack_sums(base):
    files=[]
    for dp,_,fs in os.walk(base):
        for fn in fs:
            if fn=="PACK_SHA256SUMS.txt": continue
            full=os.path.join(dp,fn); files.append((os.path.relpath(full,base).replace("\\","/"),full))
    files.sort()
    _w(os.path.join(base,"PACK_SHA256SUMS.txt"),"# SHA-256 of every file physically in this pack (excludes this file).\n"+"".join(f"{sha256_file(full)}  {rel}\n" for rel,full in files))

def _zip(srcdir,zippath):
    files=[]
    for dp,_,fs in os.walk(srcdir):
        for fn in fs: files.append(os.path.join(dp,fn))
    files.sort()
    with zipfile.ZipFile(zippath,"w",zipfile.ZIP_DEFLATED) as z:
        for full in files:
            arc=os.path.relpath(full,srcdir).replace("\\","/")
            zi=zipfile.ZipInfo(arc,date_time=(1980,1,1,0,0,0))  # deterministic timestamp
            zi.compress_type=zipfile.ZIP_DEFLATED; zi.external_attr=0o644<<16
            with open(full,"rb") as f: z.writestr(zi,f.read())

def _verify_extracted(proj, evid):
    errs=[]
    # 1. project MANIFEST hashes match extracted files
    m=os.path.join(proj,"MANIFEST.md")
    if not os.path.isfile(m): errs.append("project MANIFEST missing"); return errs
    for line in open(m,encoding="utf-8"):
        row=re.match(r"^\| \d+ \| `([A-Za-z0-9._-]+\.md)` \|.*`([0-9a-f]{64})` \|$", line.strip())
        if row:
            fp=os.path.join(proj,row.group(1))
            if not os.path.isfile(fp): errs.append(f"MANIFEST lists missing file {row.group(1)}")
            elif sha256_file(fp)!=row.group(2): errs.append(f"MANIFEST hash mismatch {row.group(1)}")
    # 2. permanent rule + core links resolve inside project pack
    if not os.path.isfile(os.path.join(proj,"ZERO_STALE_LEFTOVERS_RULE.md")): errs.append("ZERO_STALE_LEFTOVERS_RULE.md missing from project pack")
    for f in glob.glob(os.path.join(proj,"*.md")):
        for mo in re.finditer(r"\]\(([^)]+)\)", open(f,encoding="utf-8").read()):
            t=mo.group(1).split("#")[0]
            if not t or t.startswith(("http://","https://","mailto:")): continue
            if not os.path.isfile(os.path.join(proj,t)): errs.append(f"broken link in {os.path.basename(f)} -> {t}")
    # 3. evidence PACK_SHA256SUMS match extracted evidence files + validator present
    ps=os.path.join(evid,"PACK_SHA256SUMS.txt")
    if not os.path.isfile(ps): errs.append("evidence PACK_SHA256SUMS missing")
    else:
        for line in open(ps,encoding="utf-8"):
            line=line.strip()
            if not line or line.startswith("#"): continue
            h,_,rel=line.partition("  ")
            fp=os.path.join(evid,rel)
            if not os.path.isfile(fp): errs.append(f"evidence missing {rel}")
            elif sha256_file(fp)!=h: errs.append(f"evidence hash mismatch {rel}")
    if not os.path.isfile(os.path.join(evid,"tools/validate-project-state.sh")): errs.append("validator missing from evidence pack")
    if not os.path.isfile(os.path.join(evid,"tools/project-state.py")): errs.append("governance tool missing from evidence pack")
    # 4. no PII in extracted packs (excluding the bundled validators which contain redaction literals)
    pii=re.compile("|".join(re.escape(a) for a,_ in PII))
    for baseD in (proj,evid):
        for dp,_,fs in os.walk(baseD):
            for fn in fs:
                if fn in ("validate-project-state.sh","project-state.py"): continue
                fp=os.path.join(dp,fn)
                try: txt=open(fp,encoding="utf-8").read()
                except Exception: continue
                if pii.search(txt.replace("«","")): errs.append(f"PII token in {os.path.relpath(fp,baseD)}")
    return errs

def cmd_build_packs():
    # clean-source check scoped to governance-relevant paths (ignores unrelated build artifacts)
    dirty=git("status","--porcelain","--","governance","docs","tools","iam_v2_scratch")
    if dirty.strip():
        print("BUILD-PACKS REFUSED — governance source is dirty. Commit source first.\n"+dirty); return 3
    src_commit=git("rev-parse","--short","HEAD")
    rc,st=cmd_validate(deep=True)
    if rc!=0: print("BUILD-PACKS REFUSED — structural validation failed."); return rc
    if cmd_check_generated(st): print("BUILD-PACKS REFUSED — generated block drift."); return 4
    block=render_block(st)
    ts=datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    _build_project_pack(block,src_commit,st["latest_transition_id"],st["schema_version"],ts)
    _build_evidence_pack(src_commit)
    _build_planning_pack(src_commit)
    base=os.path.join(ROOT,"exports/chatgpt")
    _zip(PACK,os.path.join(base,"StayConnectEnterprise-ChatGPT-Project-Pack.zip"))
    _zip(EVID,os.path.join(base,"StayConnectEnterprise-Phase-Evidence-Pack.zip"))
    _zip(PLAN_PACK,os.path.join(base,"StayConnectEnterprise-Phase1B-Planning-Pack.zip"))
    # extract + re-validate the extracted packs (Python-native; environment-robust).
    # NOTE: the bash keyword validator (tools/validate-project-state.sh) is ALSO run in extracted-pack
    # mode as an explicit runbook step (Git Bash) — build-packs performs the deterministic structural
    # extracted-pack checks here so the build is self-verifying without a bash interop dependency.
    tmp=tempfile.mkdtemp(prefix="ps_packs_")
    try:
        with zipfile.ZipFile(os.path.join(base,"StayConnectEnterprise-ChatGPT-Project-Pack.zip")) as z: z.extractall(os.path.join(tmp,"project"))
        with zipfile.ZipFile(os.path.join(base,"StayConnectEnterprise-Phase-Evidence-Pack.zip")) as z: z.extractall(os.path.join(tmp,"evidence"))
        errs=_verify_extracted(os.path.join(tmp,"project"),os.path.join(tmp,"evidence"))
        print("extracted-pack structural validation:", "PASS" if not errs else "FAIL")
        for e in errs: print("   ", e)
        if errs: return 5
    finally:
        shutil.rmtree(tmp,ignore_errors=True)
    print(json.dumps({"SOURCE_COMMIT":src_commit,"transition_id":st["latest_transition_id"],"schema_version":st["schema_version"],"build_timestamp":ts}))
    print("BUILD-PACKS = PASS (deterministic; SOURCE_COMMIT recorded; export commit is external)")
    return 0

def main():
    cmd = sys.argv[1] if len(sys.argv) > 1 else "validate"
    if cmd == "validate":
        rc, _ = cmd_validate(deep=True); sys.exit(rc)
    elif cmd == "render":
        st = load(STATE); block, changed = cmd_render(st, write=True)
        print("rendered generated block into:", ", ".join(changed) if changed else "(no changes)")
        sys.exit(0)
    elif cmd == "render-block":
        print(render_block(load(STATE))); sys.exit(0)
    elif cmd == "check-generated":
        st = load(STATE); bad = cmd_check_generated(st)
        for p, why in bad: print(f"  DRIFT: {os.path.relpath(p, ROOT)} — {why}")
        print("GENERATED_BLOCKS =", "PASS" if not bad else f"FAIL ({len(bad)})"); sys.exit(0 if not bad else 1)
    elif cmd == "transition":
        sys.exit(cmd_transition(sys.argv[2:]))
    elif cmd == "build-packs":
        sys.exit(cmd_build_packs())
    else:
        print(__doc__); sys.exit(2)

if __name__ == "__main__":
    main()
