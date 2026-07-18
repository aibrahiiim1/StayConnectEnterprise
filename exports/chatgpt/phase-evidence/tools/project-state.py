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
    if s('1B') == "IN_PROGRESS":
        p1b_note = "(DARK — implementation in progress; no production iam_v2 use)"
    elif s('1B') in CLOSED:
        p1b_note = "(DARK — accepted & closed; no cutover; no production iam_v2 use)"
    else:
        p1b_note = "(NOT implemented)"
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
def cmd_validate(deep=True, manifest_equality=True):
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

    # Phase-1B live-dark state-consistency: reject stale contradictions that a keyword scan alone missed.
    act = st.get("current_activity", "")
    p1b_mat = st.get("phases", {}).get("1B", {}).get("maturity", "")
    p1b_exec = st.get("phase1b_execution", {}) or {}
    blockers = st.get("blockers", []) or []
    allowed = st.get("allowed_actions", []) or []
    na_txt = na if isinstance(na, str) else ""
    # A: activity says live-dark deployed, but maturity still says Gate P/throttle/OTP/live-dark PENDING.
    if "LIVE_DARK_DEPLOYED" in act and re.search(r"(gate\s*p|throttle|otp|dark-code|live-dark)[^.]*pending", p1b_mat, re.I):
        fail("stale-state contradiction: current_activity says LIVE_DARK_DEPLOYED but phases.1B.maturity still says Gate P/throttle/OTP/live-dark pending")
    # B: Gate-P cutover recorded done, but a blocker still claims services use the superuser.
    if p1b_exec.get("gate_p_least_privilege_cutover") is True:
        for b in blockers:
            if re.search(r"superuser", str(b), re.I) and re.search(r"still\s+(connect|use)", str(b), re.I):
                fail("stale-state contradiction: gate_p_least_privilege_cutover=true but a blocker says services still use the superuser")
    # C: next action is PO acceptance, but allowed_actions still say to execute Phase 1B.
    if re.search(r"accept", na_txt, re.I):
        for a in allowed:
            if re.search(r"execute\s+(the\s+)?(approved\s+)?phase\s*1b", str(a), re.I):
                fail("stale-state contradiction: next action is Product-Owner acceptance but allowed_actions still say execute Phase 1B")
    # D: after T0010, no current-state field may present the stale authoritative HEAD or "Production unchanged/untouched".
    if str(st.get("latest_transition_id", "")) >= "T0010":
        cur_blob = " ".join([act, st.get("current_maturity", ""), p1b_mat, st.get("service_routing_state", ""),
                             " ".join(str(x) for x in blockers), " ".join(str(x) for x in allowed), na_txt])
        if "1844da2" in cur_blob:
            fail("stale-state contradiction: stale HEAD 1844da2 present in a current-state field after T0010")
        if re.search(r"production\s+(unchanged|untouched)", cur_blob, re.I):
            fail("stale-state contradiction: 'Production unchanged/untouched' presented as current state after T0010")

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
    p1b_accepted = st.get("phase1b_execution", {}).get("transition_accepted") is True
    if p1b_accepted:
        # PO acceptance recorded (transition_accepted=true): Phase 1B must be ACCEPTED_AND_CLOSED —
        # closed at DARK maturity, never FINAL_CLOSED and never reopened.
        if p1b != "ACCEPTED_AND_CLOSED":
            fail(f"Phase 1B transition_accepted=true but status {p1b} is not ACCEPTED_AND_CLOSED")
        # Post-acceptance stale-field guards: no current field may still say acceptance is pending, and
        # no allowed action may still instruct merging the (already-merged) PR #2.
        for e in st.get("verified_evidence", []) or []:
            if re.search(r"pending (po|product-owner) acceptance", str(e.get("kind", "")), re.I):
                fail("stale-state contradiction: Phase 1B is ACCEPTED_AND_CLOSED but a verified-evidence entry still says PENDING PO acceptance")
        for a in st.get("allowed_actions", []) or []:
            if re.search(r"merge\s+pr\s*#?\s*2\b", str(a), re.I):
                fail("stale-state contradiction: Phase 1B is closed/merged but an allowed_action still instructs merging PR #2")
    else:
        # Until the PO acceptance is recorded, Phase 1B may not be marked accepted/closed/cutover.
        if p1b not in ("PLANNING", "IN_PROGRESS"):
            fail(f"Phase 1B status {p1b} must be PLANNING or IN_PROGRESS until PO acceptance is recorded (phase1b_execution.transition_accepted=true)")
    if st["live_scratch_dark_cutover"].get("cutover_performed"): fail("cutover_performed must be false")
    if st["live_scratch_dark_cutover"].get("live_iam_v2_in_use"): fail("live_iam_v2_in_use must be false in Phase 1B")
    if st["database_schema_state"].get("iam_v2_data_migration"): fail("iam_v2_data_migration must be false")

    # decision register agreement: D1 not reopenable; D9 active; required decisions present
    dmap = {d["id"]: d for d in dec.get("decisions", [])}
    for need in ["D1","D2","D3","D4","D5","D6","D7","D8","D9","D10","D11","D12","D13","PH-1B-SCOPE","PH-CUTOVER","SEC-NO-IAMV2-PRIV",
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

    # Phase-scope self-consistency (deterministic): once the authorized current phase advances, the
    # prohibition set must NOT still forbid implementing the current phase itself (a stale prior-phase
    # prohibition), and it MUST still forbid the next phase. Fails in BOTH directions.
    cp = str(st.get("current_phase", ""))
    prohibited = st.get("prohibited_actions", []) or []
    if cp == "2":
        for pa in prohibited:
            s = str(pa)
            forbids_current = (
                re.search(r"beyond\s+(the\s+authorized\s+)?phase\s*1b", s, re.I)  # forbids everything after 1B (incl. Phase 2)
                or re.search(r"implement\w*\s+phase\s*2\b", s, re.I)              # explicitly forbids implementing Phase 2
                or re.search(r"\(\s*phase\s*2\b", s, re.I)                        # names Phase 2 inside the forbidden set
            )
            if forbids_current:
                fail("Phase-2 scope contradiction: current_phase=2 but a prohibited_action still forbids implementing Phase 2")
        if not any(re.search(r"beyond\s+the\s+authorized\s+phase\s*2|phase\s*3\b", str(pa), re.I) for pa in prohibited):
            fail("Phase-2 scope guard: prohibited_actions must still forbid Phase 3+ (beyond the authorized Phase 2)")

    # Phase-2 transition-pointer coherence (deterministic): once the ledger has advanced to the live-dark
    # deployment transition (T0013) and the activity reflects it, phase2_execution.transition_id must point
    # at the CURRENT (deployment) transition — not still at the T0012 authorization/start transition. The
    # authorization transition is preserved separately in phase2_execution.authorization_transition_id.
    p2exec = st.get("phase2_execution", {}) or {}
    if str(st.get("latest_transition_id", "")) == "T0013" and cp == "2" \
       and "LIVE_DARK_DEPLOYED" in str(st.get("current_activity", "")):
        if p2exec.get("transition_id") != "T0013":
            fail("stale-state contradiction: latest transition is T0013 and activity is live-dark deployed, "
                 "but phase2_execution.transition_id does not point at T0013")
        if p2exec.get("authorization_transition_id") != "T0012":
            fail("phase2_execution.authorization_transition_id must record the D12 authorization transition T0012")
    # Once the ledger has advanced to the closure transition (T0014) and the activity reflects acceptance,
    # phase2_execution.transition_id must point at the CLOSURE transition, the D12 authorization transition
    # must remain recorded separately, and transition_accepted must be true.
    if str(st.get("latest_transition_id", "")) == "T0014" and cp == "2" \
       and "ACCEPTED_AND_CLOSED" in str(st.get("current_activity", "")):
        if p2exec.get("transition_id") != "T0014":
            fail("stale-state contradiction: latest transition is T0014 (Phase-2 closure) but phase2_execution.transition_id does not point at T0014")
        if p2exec.get("authorization_transition_id") != "T0012":
            fail("phase2_execution.authorization_transition_id must record the D12 authorization transition T0012")
        if p2exec.get("transition_accepted") is not True:
            fail("Phase 2 is closed (T0014) but phase2_execution.transition_accepted is not true")

    # Manifest-HEAD coherence: when an acceptance-candidate HEAD is recorded and a change-manifest exists,
    # the manifest's recorded HEAD commit must equal that acceptance-candidate HEAD (they cannot drift
    # apart between the final report / PR and the generated manifest).
    ach = st.get("acceptance_candidate_head")
    man_path = os.path.join(ROOT, "docs/manifests/Phase2-change-manifest.md")
    if manifest_equality and ach and os.path.isfile(man_path):
        mtext = open(man_path, encoding="utf-8").read()
        m = re.search(r"HEAD commit:\*\*\s*`([0-9a-f]{7,40})`", mtext)
        head_in_manifest = m.group(1) if m else ""
        if not head_in_manifest:
            fail("change-manifest has no recorded HEAD commit")
        elif not (head_in_manifest.startswith(ach) or ach.startswith(head_in_manifest)):
            fail(f"manifest HEAD {head_in_manifest} != acceptance_candidate_head {ach}")

    # Manifest <-> Git path/status equality (GH-COMPLETE-MANIFEST + self-reference protocol 4.1):
    # the committed manifest's complete {path: git-status-letter} set must equal
    # `git diff --name-status base..HEAD`. The manifest is generated at inventory_head but, because
    # the delivery-only commit introduces zero unlisted paths, its path/status set equals
    # base..delivery_head (the current HEAD at final validation). Skipped inside build-packs
    # (manifest_equality=False), which validates a pre-regeneration source tree.
    if manifest_equality and os.path.isfile(man_path):
        mtext = open(man_path, encoding="utf-8").read()
        bmm = re.search(r"Base commit:\*\*\s*`([0-9a-f]{7,40})`", mtext)
        base_sha = bmm.group(1) if bmm else ""
        # only enforce when the base commit is resolvable in this checkout (CI does a full checkout;
        # a shallow/partial clone that cannot resolve base is skipped rather than falsely failed).
        if base_sha and git("cat-file", "-t", base_sha) == "commit":
            man_map = {}
            for row in re.finditer(r"^\|\s*`([^`]+)`\s*\|[^|]*\|\s*`([^`]+)`\s*\|", mtext, re.M):
                disp = row.group(1).strip(); letter = row.group(2).strip()[:1]
                path = disp.split(" -> ")[-1].strip() if " -> " in disp else disp
                man_map[path] = letter
            git_map = {}
            for line in git("diff", "--name-status", "--find-renames", "--find-copies", f"{base_sha}..HEAD").splitlines():
                if not line.strip(): continue
                parts = line.split("\t")
                letter = parts[0][:1]
                path = parts[2] if (letter in ("R", "C") and len(parts) >= 3) else parts[-1]
                git_map[path] = letter
            missing = sorted(set(git_map) - set(man_map))
            extra = sorted(set(man_map) - set(git_map))
            mismatch = sorted(p for p in (set(man_map) & set(git_map)) if man_map[p] != git_map[p])
            if missing: fail(f"manifest omits {len(missing)} path(s) present in git base..HEAD (e.g. {missing[:3]}) — complete-manifest self-reference protocol (docs/GITHUB_EXECUTION_AND_DELIVERY_RULE.md §4.1)")
            if extra:   fail(f"manifest lists {len(extra)} path(s) not in git base..HEAD (e.g. {extra[:3]})")
            if mismatch:fail(f"manifest status differs from git for {len(mismatch)} path(s) (e.g. {mismatch[:3]})")

    # current phase plan + privilege matrix exist; each asserts the ZERO-production-iam_v2 posture.
    plan = st.get("current_phase_plan")
    if not plan or not os.path.isfile(os.path.join(ROOT, plan)): fail(f"current_phase_plan missing/not found: {plan}")
    mtx = st.get("privilege_matrix")
    if not mtx or not os.path.isfile(os.path.join(ROOT, mtx)): fail(f"privilege_matrix missing/not found: {mtx}")
    else:
        mt = open(os.path.join(ROOT, mtx), encoding="utf-8").read()
        # machine assertion: production runtime roles hold ZERO iam_v2 DML/EXECUTE (current-phase matrix)
        if "PRODUCTION_IAM_V2_DML: NONE" not in mt: fail("privilege matrix missing machine assertion PRODUCTION_IAM_V2_DML: NONE")
        if re.search(r"PRODUCTION_IAM_V2_DML:\s*(GRANTED|SOME|ANY)", mt): fail("privilege matrix asserts a production iam_v2 grant (must be NONE)")
    # current-phase plan self-consistency: the Phase-2 plan must carry its DARK runtime sentinel so the
    # current pointer can never go stale relative to the phase it names.
    if cp == "2" and plan and os.path.isfile(os.path.join(ROOT, plan)):
        p2t = open(os.path.join(ROOT, plan), encoding="utf-8").read()
        if "PHASE_2_PRODUCTION_RUNTIME: DARK" not in p2t:
            fail("Phase 2 plan missing sentinel 'PHASE_2_PRODUCTION_RUNTIME: DARK'")

    # ---- Phase-2 Zero-Stale reconciliation guards (narrow, deterministic) ----
    def _read(rel):
        p = os.path.join(ROOT, rel)
        return open(p, encoding="utf-8").read() if os.path.isfile(p) else None
    rep = _read("docs/reports/StayConnect-IAM-Phase2-Final-Report.md")
    gate = _read("docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md")
    live = _read("docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md")
    man = _read("docs/manifests/Phase2-change-manifest.md")
    # 1. report cannot claim "no UI test harness" once the software gate records UI tests
    if rep and gate and re.search(r"Vitest|Playwright|\b36\b\s*(component|Vitest|tests)", gate):
        if re.search(r"no\s+JS\s+component/?E2E\s+test\s+harness\s+exists|no\s+UI\s+test\s+harness", rep, re.I):
            fail("Phase-2 report claims no UI test harness while the software gate records UI tests")
    # 2. report changed-file count must match the manifest and must not present 67 as current
    if rep and man:
        mm = re.search(r"Changed files:\*\*\s*(\d+)", man)
        if mm:
            cnt = mm.group(1)
            if cnt not in rep:
                fail(f"Phase-2 report does not state the manifest changed-file count ({cnt})")
        # the legitimate superseded mention uses "67 files"; presenting "67 changed files" is the stale claim.
        if re.search(r"\b67\b\s+changed\s+files?", rep, re.I):
            fail("Phase-2 report presents 67 changed files as current (must be the acceptance-candidate manifest count)")
    # 3. live-evidence current artifact table must show the current hotel-admin bundle, not the superseded one
    if live:
        cur_ui = "678c793ea46f23241eba05bde66929b19a5473fc8d3752d2a5eb083f4ff0dd95"
        old_ui = "e25126737341d8f248ae3a4589ba3a72778705a00f25b8caf6312c64a723999d"
        if cur_ui not in live:
            fail("Phase-2 live evidence missing the current hotel-admin bundle hash 678c793e")
        if old_ui in live:
            idx = live.index(old_ui)
            preceding = live[:idx]
            if "HISTORICAL" not in preceding and not re.search(r"supersed", live[max(0, idx-200):idx+200], re.I):
                fail("Phase-2 live evidence presents the superseded hotel-admin bundle e25126737341 as current (needs a HISTORICAL/superseded marker)")
    # 4. pending PO acceptance must not coexist with an allowed_action to keep implementing Phase 2
    if "PENDING_PO_ACCEPTANCE" in str(st.get("current_activity", "")):
        for a in st.get("allowed_actions", []) or []:
            if re.search(r"(execute|implement|continue)[^.]{0,60}phase\s*2", str(a), re.I) and re.search(r"end-to-end|implement", str(a), re.I):
                fail("Phase 2 is pending PO acceptance but an allowed_action still says to continue implementing Phase 2")
    # 4b. Phase-2 acceptance/closure coherence (mirrors the Phase-1B acceptance guards). Once
    #     phase2_execution.transition_accepted=true, Phase 2 must be ACCEPTED_AND_CLOSED, the activity must
    #     reflect it, the recorded acceptance decision must be a registered decision, and no current field
    #     may still present acceptance as pending/candidate. Until then Phase 2 stays IN_PROGRESS.
    if cp == "2":
        p2 = st["phases"].get("2", {}).get("status")
        p2acc = (st.get("phase2_execution", {}) or {}).get("transition_accepted") is True
        if p2acc:
            if p2 != "ACCEPTED_AND_CLOSED":
                fail(f"Phase 2 transition_accepted=true but phases.2 status {p2} is not ACCEPTED_AND_CLOSED")
            if str(st.get("current_activity", "")) != "PHASE_2_ACCEPTED_AND_CLOSED":
                fail("Phase 2 accepted but current_activity is not PHASE_2_ACCEPTED_AND_CLOSED")
            if "PENDING_PO_ACCEPTANCE" in str(st.get("current_activity", "")):
                fail("Phase 2 accepted but current_activity still says PENDING_PO_ACCEPTANCE")
            if st.get("latest_accepted_po_decision") not in dmap:
                fail(f"latest_accepted_po_decision {st.get('latest_accepted_po_decision')!r} is not a registered decision")
            for e in st.get("verified_evidence", []) or []:
                if re.search(r"pending.*(po|product-owner).*(decision|acceptance)|acceptance\s+candidate|candidate\s*--\s*pending", str(e.get("kind", "")), re.I):
                    fail("Phase 2 is ACCEPTED_AND_CLOSED but a verified-evidence entry still says pending PO acceptance / candidate")
            for a in st.get("allowed_actions", []) or []:
                if re.search(r"(execute|implement)[^.]{0,60}phase\s*2\b", str(a), re.I) and re.search(r"end-to-end|implement|dark", str(a), re.I):
                    fail("Phase 2 is ACCEPTED_AND_CLOSED but an allowed_action still says to execute/implement Phase 2")
            # 4c. Post-merge coherence: once the Phase-2 PR is recorded merged (merge_commit present), no
            #     CURRENT-state field may still present the (completed) merge or its post-merge verification as
            #     a pending/future action. Historical D13/T0014 records legitimately authorized the merge and
            #     are NOT scanned here (only live current-state fields are).
            p2x = st.get("phase2_execution", {}) or {}
            if p2x.get("merged") is True or p2x.get("merge_commit"):
                cur_blob = " ".join([str(st.get("current_maturity", "")), str(st.get("next_authorized_action", "")),
                                     str(p2x.get("stage", "")), str(st["phases"].get("2", {}).get("maturity", ""))]
                                    + [str(x) for x in (st.get("allowed_actions", []) or [])]
                                    + [str(x) for x in (st.get("blockers", []) or [])])
                for pat, why in [
                    (r"merge\s+pr\s*#?\s*4\s+to\s+master", "a current-state field still says to merge PR #4 to master (already merged)"),
                    (r"pr\s*#?\s*4\s+authorized\s+to\s+merge", "a current-state field still says PR #4 is authorized to merge (already merged)"),
                    (r"run\s+post-merge\s+governance\s+verification", "a current-state field still lists 'run post-merge governance verification' as a pending action (already done)"),
                ]:
                    if re.search(pat, cur_blob, re.I):
                        fail(f"post-merge stale action: {why}")
                if re.search(r"merge\s+pr\s*#?\s*4", str(st.get("next_authorized_action", "")), re.I):
                    fail("next_authorized_action still points to the already-completed PR #4 merge")
        else:
            if p2 not in ("IN_PROGRESS",):
                fail(f"Phase 2 transition_accepted!=true but phases.2 status {p2} is not IN_PROGRESS (PO acceptance not yet recorded)")
    # 5. the Project Pack SOURCE list must include the current Phase-2 plan/acceptance/evidence (checking the
    #    build source list, not the extracted dir, so this never deadlocks the pack rebuild).
    for req in ["StayConnect-IAM-Phase2-Plan.md", "StayConnect-IAM-Phase2-Final-Report.md",
                "StayConnect-IAM-Phase2-Live-Dark-Acceptance.md", "StayConnect-IAM-Phase2-Software-Gate.md",
                "StayConnect-IAM-Phase2-Live-Dark-Evidence.md"]:
        if req not in PACK_DOCS:
            fail(f"Project Pack source list (PACK_DOCS) omits the current Phase-2 file {req}")
    # 6. the Phase Evidence Pack SOURCE list must include the Phase-2 evidence set.
    for req in ["docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md",
                "docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md"]:
        if req not in EVIDENCE_PHASE2_DOCS:
            fail(f"Phase Evidence Pack source list (EVIDENCE_PHASE2_DOCS) omits {req}")
    # once a Project Pack has been built, it must physically contain the Phase-2 final report (post-build check).
    built_report = os.path.join(ROOT, "exports/chatgpt/stayconnectenterprise/StayConnect-IAM-Phase2-Final-Report.md")
    built_manifest = os.path.join(ROOT, "exports/chatgpt/stayconnectenterprise/MANIFEST.md")
    if os.path.isfile(built_manifest) and "StayConnect-IAM-Phase2-Final-Report.md" in open(built_manifest, encoding="utf-8").read() and not os.path.isfile(built_report):
        fail("Project Pack MANIFEST lists the Phase-2 Final Report but the file is not in the pack")
    # 7. the Phase-1B Planning Pack MANIFEST generator must mark the pack HISTORICAL (checking the generator
    #    source, not the extracted file, so it holds at the delivery-wrapper HEAD and never deadlocks a rebuild).
    self_src = _read("tools/project-state.py") or ""
    if not re.search(r"PLANNING_PACK_STATUS:\s+HISTORICAL", self_src):
        fail("Phase-1B Planning Pack MANIFEST generator does not mark the pack HISTORICAL (sentinel missing)")
    # 8. two named public-column fingerprints must carry a reconciliation note (never conflicting unnamed values)
    dss = st.get("database_schema_state", {}) or {}
    if dss.get("public_columns_sha256_current") and dss.get("public_columns_phase2_livedark_sha256"):
        if dss["public_columns_sha256_current"] != dss["public_columns_phase2_livedark_sha256"] and not dss.get("public_columns_fingerprint_reconciliation"):
            fail("public column fingerprints differ but carry no reconciliation note (must be distinctly named + documented, not comparable)")

    # The Phase-1B least-privilege artifacts remain the authoritative BASE for every later DARK phase
    # (D1 rolled-back-write ban, zero production iam_v2 runtime, matrix NONE). Validate them by FIXED path
    # regardless of which phase's plan/matrix is the current pointer, so the base posture cannot be
    # silently weakened by repointing the current-phase fields.
    base_mtx_p = os.path.join(ROOT, "docs/architecture/Phase1B-Privilege-Matrix.md")
    if os.path.isfile(base_mtx_p):
        bm = open(base_mtx_p, encoding="utf-8").read()
        if "PRODUCTION_IAM_V2_DML: NONE" not in bm: fail("Phase 1B base matrix missing PRODUCTION_IAM_V2_DML: NONE")
        if re.search(r"PRODUCTION_IAM_V2_DML:\s*(GRANTED|SOME|ANY)", bm): fail("Phase 1B base matrix asserts a production iam_v2 grant (must be NONE)")
    base_plan_p = os.path.join(ROOT, "docs/architecture/StayConnect-IAM-Phase1B-Plan.md")
    if os.path.isfile(base_plan_p):
        pt = open(base_plan_p, encoding="utf-8").read()
        if "Phase1B-Privilege-Matrix.md" not in pt: fail("Phase 1B plan does not reference the privilege matrix")
        if "rolled-back" not in pt.lower(): fail("Phase 1B plan must address rolled-back-transaction writes (D1)")
        # Phase 1B plan / canonical-state coherence (no PLANNING-ONLY vs IN_PROGRESS; no production iam_v2
        # runtime grant / shadow / rolled-back contradiction). Sentinel + multiple structural assertions,
        # gated on the live phase status — not a single grep.
        if "PHASE_1B_PRODUCTION_IAM_V2_RUNTIME: NONE" not in pt:
            fail("Phase 1B plan missing sentinel 'PHASE_1B_PRODUCTION_IAM_V2_RUNTIME: NONE'")
        if p1b in ("IN_PROGRESS", "ACCEPTED_AND_CLOSED") and re.search(r"PLANNING ONLY|NOT APPROVED FOR IMPLEMENTATION", pt):
            fail(f"Phase 1B plan states 'PLANNING ONLY / NOT APPROVED' while Phase 1B is implemented (canonical status {p1b})")
        _plan_contradictions = [
            (r"prepared for cutover", "production runtime iam_v2 grant 'prepared for cutover' (Phase 1B production roles hold ZERO iam_v2; future grants belong only in the FUTURE DESIGN appendix)"),
            (r"read-mostly/rolled-back", "production rolled-back iam_v2 transaction (D1 forbids all production iam_v2 access incl. rolled-back)"),
            (r"shadow-only in production", "production iam_v2 shadow execution (all iam_v2 adapter/engine execution is scratch/test only)"),
            (r"unless D1 explicitly approved shadow writes", "stale D1 shadow-write exception (D1 rejects shadow writes)"),
        ]
        for pat, why in _plan_contradictions:
            if re.search(pat, pt): fail(f"Phase 1B plan contradiction: {why}")

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
 "StayConnect-IAM-Phase2-Plan.md": ("docs/architecture/StayConnect-IAM-Phase2-Plan.md",None),
 "Phase2-Privilege-Matrix.md": ("docs/architecture/Phase2-Privilege-Matrix.md",None),
 "StayConnect-IAM-Phase2-Software-Gate.md": ("docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md",None),
 "StayConnect-IAM-Phase2-Live-Dark-Evidence.md": ("docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md",None),
 "StayConnect-IAM-Phase2-Live-Dark-Acceptance.md": ("docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md",None),
 "StayConnect-IAM-Phase2-Final-Report.md": ("docs/reports/StayConnect-IAM-Phase2-Final-Report.md",None),
 "Phase2-change-manifest.md": ("docs/manifests/Phase2-change-manifest.md",None),
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
# Phase-2 evidence docs copied into the Phase Evidence Pack (also asserted present by the reconciliation guard).
EVIDENCE_PHASE2_DOCS = [
 "docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md",
 "docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md",
 "docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md",
 "docs/reports/StayConnect-IAM-Phase2-Final-Report.md",
 "docs/manifests/Phase2-change-manifest.md",
]
# MANIFEST row order + status label
MROWS = [
 ("00-START-HERE.md","*(generated)*","Entry point"),
 ("PROJECT-INSTRUCTIONS.md","*(generated)*","Project config"),
 ("StayConnect-IAM-Phase0-Contract.md","`docs/architecture/StayConnect-IAM-Phase0-Contract.md`","**Authoritative** *(sanitized)*"),
 ("StayConnect-IAM-Handoff.md","`docs/context/StayConnect-IAM-Handoff.md`","**Authoritative**"),
 ("StayConnect-IAM-Phase1A-Plan.md","`docs/architecture/StayConnect-IAM-Phase1A-Plan.md`","**Authoritative (closed phase)**"),
 ("StayConnect-IAM-Phase1B-Plan.md","`docs/architecture/StayConnect-IAM-Phase1B-Plan.md`","**Authoritative — ACCEPTED_AND_CLOSED at DARK maturity (D11/T0011); PR #2 merged**"),
 ("Phase1B-Privilege-Matrix.md","`docs/architecture/Phase1B-Privilege-Matrix.md`","**Authoritative — as-built grant matrix (Gate P deployed)**"),
 ("StayConnect-IAM-Phase2-Plan.md","`docs/architecture/StayConnect-IAM-Phase2-Plan.md`","**Authoritative — Phase 2 ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014); PR #4 authorized to merge**"),
 ("Phase2-Privilege-Matrix.md","`docs/architecture/Phase2-Privilege-Matrix.md`","**Authoritative — zero new Phase-2 runtime privilege (live-verified)**"),
 ("StayConnect-IAM-Phase2-Software-Gate.md","`docs/evidence/StayConnect-IAM-Phase2-Software-Gate.md`","**Authoritative — Phase 2 software-gate evidence (Go + 45 UI tests + build)**"),
 ("StayConnect-IAM-Phase2-Live-Dark-Evidence.md","`docs/evidence/StayConnect-IAM-Phase2-Live-Dark-Evidence.md`","**Authoritative — Phase 2 live-dark + two-reboot darkness evidence**"),
 ("StayConnect-IAM-Phase2-Live-Dark-Acceptance.md","`docs/acceptance/StayConnect-IAM-Phase2-Live-Dark-Acceptance.md`","**Acceptance record — PRODUCT-OWNER ACCEPTED_AND_CLOSED at DARK maturity (D13/T0014)**"),
 ("StayConnect-IAM-Phase2-Final-Report.md","`docs/reports/StayConnect-IAM-Phase2-Final-Report.md`","**Authoritative — Phase 2 final report (accepted)**"),
 ("Phase2-change-manifest.md","`docs/manifests/Phase2-change-manifest.md`","**Generated — complete Phase 2 changed-file manifest (base..delivery_head; inventory_head provenance)**"),
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
    # Normalize CRLF->LF so packed text is byte-identical regardless of the builder's OS / working-tree
    # line endings (git stores LF via .gitattributes; a CRLF working copy must not change pack checksums).
    os.makedirs(os.path.dirname(dst),exist_ok=True)
    with open(src,"rb") as f: data=f.read()
    if b"\x00" not in data:  # text file: force LF
        data=data.replace(b"\r\n",b"\n")
    with open(dst,"wb") as f: f.write(data)

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
    # Phase 1A + 1B acceptance records — retained as CLOSED historical baselines
    _cp(os.path.join(ROOT,"docs/acceptance/StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md"),os.path.join(EVID,"StayConnect-IAM-Phase1A-Live-Dark-Acceptance.md"))
    _cp(os.path.join(ROOT,"docs/acceptance/StayConnect-IAM-Phase1B-Live-Dark-Acceptance.md"),os.path.join(EVID,"StayConnect-IAM-Phase1B-Live-Dark-Acceptance.md"))
    # Phase 2 authoritative evidence set (current)
    for rel in EVIDENCE_PHASE2_DOCS:
        _cp(os.path.join(ROOT,rel),os.path.join(EVID,os.path.basename(rel)))
    # D12/D13 + T0012 (authorization) + T0013 (live-dark deployment) + T0014 (PO acceptance/closure) records
    _cp(os.path.join(ROOT,"governance/decision-register.json"),os.path.join(EVID,"governance","decision-register.json"))
    for tid in ["T0012","T0013","T0014"]:
        _cp(os.path.join(ROOT,"governance/transitions",tid+".json"),os.path.join(EVID,"governance","transitions",tid+".json"))
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
    _w(os.path.join(PLAN_PACK,"MANIFEST.md"),f"# Phase 1B Plan Evidence Pack — MANIFEST  (HISTORICAL)\n\n<!-- PLANNING_PACK_STATUS: HISTORICAL -->\n\n> **HISTORICAL — Phase-1B planning artifact.** Phase 1B was **ACCEPTED_AND_CLOSED at DARK maturity** via decision **D11** / transition **T0011**, and **PR #2 was merged**. This pack is retained for provenance of the Phase-1B planning stage only; it is **not** a current status/evidence pack. For current state see the Project Pack (`00-START-HERE.md`, Phase-2 Plan, Phase-2 Final Report, Phase-2 acceptance candidate) and the Phase Evidence Pack.\n\n- SOURCE_COMMIT: `{src_commit}`\n- HISTORICAL status at the time this planning pack was authored: Phase 1B planning (the earlier D10/IN_PROGRESS wording is superseded — Phase 1B is now accepted/closed via D11/T0011 and PR #2 merged; current phase is Phase 2, live-dark deployed and pending one Product-Owner acceptance decision).\n- Contents: Phase 1B plan (matrices+blueprint), privilege matrix, three code inventories, blueprint extract, README, two checksum lists.\n- Current next authorized action (see project-state): one Product-Owner Phase-2 acceptance decision.\n")
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

def cmd_build_packs(allow_dirty=False):
    # clean-source check scoped to governance-relevant paths (ignores unrelated build artifacts).
    # --allow-dirty is used ONLY for the final delivery-only commit, which intentionally bundles the
    # finalized source docs (regenerated manifest, report changed-file count, pointer/provenance) with
    # their freshly built packs in one commit; the delivery HEAD is still fully validated afterwards.
    if not allow_dirty:
        dirty=git("status","--porcelain","--","governance","docs","tools","iam_v2_scratch")
        if dirty.strip():
            print("BUILD-PACKS REFUSED — governance source is dirty. Commit source first (or use --allow-dirty for the delivery-only commit).\n"+dirty); return 3
    src_commit=git("rev-parse","--short","HEAD")
    # skip the manifest<->Git equality guard here: build-packs validates the pre-regeneration source tree,
    # where the on-disk manifest may not yet describe the not-yet-committed delivery HEAD. The delivery
    # HEAD itself is validated (with the equality guard ON) after the commit.
    rc,st=cmd_validate(deep=True, manifest_equality=False)
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
        sys.exit(cmd_build_packs(allow_dirty=("--allow-dirty" in sys.argv[2:])))
    else:
        print(__doc__); sys.exit(2)

if __name__ == "__main__":
    main()
