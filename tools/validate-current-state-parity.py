#!/usr/bin/env python3
"""CURRENT-STATE SEMANTIC PARITY.

Every zero-stale check that existed before this one stopped at a keyword: it looked for a phrase and objected
to it. That is why a fully green Governance gate shipped a project-state file whose `current_activity` said
"INCREMENT-9 DURABILITY CORRECTION" while its `current_maturity`, three sentences later, said the project was
still "awaiting one separate Product-Owner decision to authorize Live Increment 9" — and recorded iam_v2 as
49/0 while another field in the same file recorded 63. Neither sentence contains a forbidden word. They are
only wrong *about each other*.

So this validator does not police vocabulary. It reads the machine-readable facts in
`governance/project-state.json` -> `current_state_facts`, and then asserts that no surface which presents
itself as CURRENT contradicts them. A fact recorded as data can be diffed against prose; a fact that exists
only as prose cannot.

HISTORY IS NOT A CONTRADICTION. A statement that was true when it was written stays in the record — this
project's whole evidence model depends on that. A hit is excused when its own paragraph marks it as historical
(HISTORICAL, SUPERSEDED, "as at", "as written", "was then", "no longer", "at the time"). What is refused is a
superseded claim presented as the current state, unlabelled.

Usage:  python tools/validate-current-state-parity.py [--json]
Exit:   0 = parity holds, 1 = a current surface contradicts the recorded facts, 2 = the facts are unusable.
"""
import io
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
STATE = os.path.join(ROOT, "governance", "project-state.json")
TRANSITIONS_DIR = os.path.join(ROOT, "governance", "transitions")

# Surfaces that speak in the present tense about the project. Packs and exports are generated from these and
# are checked by the pack validators; transition receipts are dated records and are deliberately excluded.
DOC_SURFACES = [
    "governance/project-state.json",
    "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
    "docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md",
    "docs/reports/StayConnect-IAM-Phase3-Final-Report.md",
    "docs/context/StayConnect-IAM-Handoff.md",
]

HISTORY_MARKERS = re.compile(
    r"historical|superseded|as at |as written|was then|no longer|at the time|"
    r"earlier (draft|version|revision)|previously|used to|before live increment|"
    r"an earlier|deliberately excluded|this document is preserved",
    re.I,
)


def paragraphs(text):
    """Yield (paragraph, offset). A paragraph is the unit of labelling: a historical marker excuses the
    statements around it, not the whole file."""
    off = 0
    for para in re.split(r"\n\s*\n", text):
        yield para, off
        off += len(para) + 2


def scan(text, pattern):
    """Yield paragraphs matching pattern that are NOT marked historical."""
    for para, _ in paragraphs(text):
        if pattern.search(para) and not HISTORY_MARKERS.search(para):
            yield " ".join(para.split())[:180]


def load_json(rel):
    path = os.path.join(ROOT, rel)
    try:
        return json.load(io.open(path, encoding="utf-8"))
    except Exception:  # noqa: BLE001 -- a malformed JSON surface is the schema validator's failure to report
        return None


def json_strings(node):
    """Every string VALUE in a JSON document. A JSON file has no blank lines, so its paragraph is the whole
    file; the unit a reader actually reads -- and the unit a history label can honestly cover -- is the value."""
    if isinstance(node, str):
        yield node
    elif isinstance(node, dict):
        for v in node.values():
            for s in json_strings(v):
                yield s
    elif isinstance(node, list):
        for v in node:
            for s in json_strings(v):
                yield s


def load_surface(rel):
    path = os.path.join(ROOT, rel)
    if not os.path.exists(path):
        return None
    return io.open(path, encoding="utf-8", errors="replace").read()


def main():
    as_json = "--json" in sys.argv
    try:
        state = json.load(io.open(STATE, encoding="utf-8"))
    except Exception as exc:  # noqa: BLE001
        print("PARITY = ERROR: project-state.json unreadable: %s" % exc)
        return 2

    facts = state.get("current_state_facts")
    if not isinstance(facts, dict) or not facts:
        print("PARITY = ERROR: governance/project-state.json has no current_state_facts block.")
        print("  Current state must be recorded as DATA, or prose can contradict prose and nothing notices.")
        return 2

    failures = []
    notes = []

    def bad(rule, detail, where):
        failures.append((rule, detail, where))

    def ok(rule):
        notes.append(rule)

    # ---- 1. internal coherence of the facts themselves ------------------------------------------------------
    # A facts block that contradicts itself would validate every surface against nonsense.
    if facts.get("live_increment9_executed"):
        for k in ("appliance_contact_occurred", "production_db_contact_occurred", "deployment_occurred"):
            if not facts.get(k):
                bad("facts-coherence", "live_increment9_executed is true but %s is false" % k, STATE)
    if facts.get("migration_0010_applied_production") and facts.get("iam_v2_tables") == 49:
        bad("facts-coherence", "migration 0010 recorded as applied while iam_v2_tables is still 49", STATE)
    # accepted/closed is a real state, not an impossible one — but it must agree with the phase record it
    # describes. This rule used to reject acceptance outright, which was correct only while acceptance could not
    # have happened; leaving it that way would have made the true final state unrepresentable.
    if facts.get("accepted") != facts.get("closed"):
        bad("facts-coherence", "accepted=%r and closed=%r disagree" % (facts.get("accepted"), facts.get("closed")), STATE)
    if facts.get("accepted") and facts.get("closed"):
        ph = (state.get("phases") or {}).get("3") or {}
        if ph.get("status") != "ACCEPTED_AND_CLOSED":
            bad("facts-coherence",
                "facts say Phase 3 is accepted and closed but phases.3.status is %r" % ph.get("status"), STATE)
        if "ACCEPTED_AND_CLOSED" not in state.get("current_activity", ""):
            bad("facts-coherence",
                "facts say Phase 3 is accepted and closed but current_activity is %r" % state.get("current_activity"), STATE)
        for k in ("accepted_decision", "accepted_transition", "accepted_runtime_head", "accepted_at_maturity"):
            if not facts.get(k):
                bad("facts-coherence", "acceptance is recorded without %s" % k, STATE)
    if not failures:
        ok("the recorded facts are internally coherent")

    # ---- 2. current_activity vs an authorization that is supposedly still awaited ---------------------------
    activity = state.get("current_activity", "")
    if facts.get("current_activity") != activity:
        bad("activity-parity",
            "current_activity is %r but current_state_facts.current_activity is %r" % (activity, facts.get("current_activity")),
            STATE)
    if facts.get("live_increment9_executed"):
        awaiting = re.compile(
            r"await(?:s|ing)?[^.]{0,120}(?:authoriz|decision)[^.]{0,60}(?:live )?increment[- ]?9|"
            r"increment[- ]?9 authorization requested|"
            r"pre-live safety candidate|"
            r"authorization is still awaited",
            re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, awaiting):
                bad("increment9-already-executed",
                    "presents Increment-9 authorization as still awaited: %s" % hit, rel)
        if not [f for f in failures if f[0] == "increment9-already-executed"]:
            ok("no current surface still awaits the Increment-9 authorization that was already given and executed")

    # ---- 3. iam_v2 table count parity ------------------------------------------------------------------------
    tables = facts.get("iam_v2_tables")
    if isinstance(tables, int):
        schema_tables = (state.get("database_schema_state") or {}).get("iam_v2_tables")
        if schema_tables is not None and schema_tables != tables:
            bad("iamv2-count-parity",
                "database_schema_state.iam_v2_tables=%s but current_state_facts.iam_v2_tables=%s" % (schema_tables, tables),
                STATE)
        stale_count = re.compile(r"iam_v2\s*49\b|49\s*tables?\s*/\s*0|iam_v2\s*49\s*/\s*0", re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, stale_count):
                bad("iamv2-count-parity",
                    "states iam_v2 49 as current while the recorded count is %s: %s" % (tables, hit), rel)
        if not [f for f in failures if f[0] == "iamv2-count-parity"]:
            ok("no current surface contradicts the recorded iam_v2 table count (%s)" % tables)

    # ---- 4. migration 0010 deployment parity ------------------------------------------------------------------
    if facts.get("migration_0010_applied_production"):
        undeployed = re.compile(r"(migration\s*)?0010[^.\n]{0,40}\bundeployed\b|\bundeployed\b[^.\n]{0,40}0010", re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, undeployed):
                bad("migration-0010-parity", "calls migration 0010 undeployed: %s" % hit, rel)
        if not [f for f in failures if f[0] == "migration-0010-parity"]:
            ok("no current surface calls migration 0010 undeployed")

    # ---- 5. live-contact parity --------------------------------------------------------------------------------
    if facts.get("live_increment9_executed"):
        no_contact = re.compile(
            r"no appliance (access|contact)|no production[- ]?(db|database) (access|contact)|"
            r"no live[- ]?pms contact|no live evidence exists|contacted no appliance|"
            r"no appliance, production database or (live )?pms was contacted",
            re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, no_contact):
                bad("live-contact-parity",
                    "denies appliance/Production-DB/live-PMS contact that has already occurred: %s" % hit, rel)
        if not [f for f in failures if f[0] == "live-contact-parity"]:
            ok("no current surface denies the live contact that Increment 9 actually made")

    # ---- 6. Guest-Portal budget parity ---------------------------------------------------------------------------
    budget = facts.get("guest_portal_uniform_budget_ms")
    if isinstance(budget, int):
        old_budget = re.compile(r"\b1200\s*ms\b", re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, old_budget):
                bad("portal-budget-parity",
                    "presents 1200ms as current while the recorded budget is %sms: %s" % (budget, hit), rel)
        if not [f for f in failures if f[0] == "portal-budget-parity"]:
            ok("no current surface presents a superseded Guest-Portal budget (recorded: %sms)" % budget)

    # ---- 7. nft deployment architecture parity ---------------------------------------------------------------------
    if facts.get("surgical_foundation_retired_from_procedure"):
        requires_foundation = re.compile(
            r"(phase3-foundation\s+(install|rollback))|"
            r"(cutover becomes flag-only[^.]{0,80}install)|"
            r"(only after that install is performed)",
            re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for para, _ in paragraphs(text):
                if not requires_foundation.search(para):
                    continue
                if HISTORY_MARKERS.search(para):
                    continue
                # A paragraph that RETIRES the tool naturally names it; that is not a requirement to run it.
                if re.search(r"retired|diagnostic|do \*\*not\*\* run|do not run|never part of", para, re.I):
                    continue
                bad("nft-architecture-parity",
                    "still presents the surgical foundation as a required deployment/rollback step: %s"
                    % " ".join(para.split())[:180], rel)
        if not [f for f in failures if f[0] == "nft-architecture-parity"]:
            ok("no current surface requires the retired surgical-foundation step (architecture: %s)"
               % facts.get("nft_deployment_architecture"))

    # ---- 8. ACCEPTANCE PARITY ---------------------------------------------------------------------------------------
    #
    # The Governance gate went green while current_maturity said an authorization was awaited, phase3_execution.stage
    # said migration 0010 was undeployed, the Final Report described the surgical foundation as the deployment
    # mechanism, the lower PR body said evidence was pending, and the rollback runbook promised that every previous
    # release carries authorization across. Each was a superseded statement wearing the present tense.
    if facts.get("accepted") and facts.get("closed"):
        # (a) nothing current may still call Phase 3 a candidate / in progress / not accepted
        not_accepted = re.compile(
            r"phase[- ]?3 is (not accepted|in_progress|a candidate)|"
            r"\bNOT accepted\b|\bnot yet accepted\b|"
            r"dark acceptance candidate|increment-9 durability correction candidate|"
            r"pending the product owner'?s? final",
            re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, not_accepted):
                bad("acceptance-parity", "still presents Phase 3 as unaccepted/in-progress: %s" % hit, rel)
        if not [f for f in failures if f[0] == "acceptance-parity"]:
            ok("no current surface still presents Phase 3 as unaccepted or in progress")

        # (b) the corrected software IS deployed; nothing current may say otherwise
        undeployed = re.compile(
            r"corrected software is \*{0,2}not( yet)? deployed|"
            r"is NOT deployed|still runs the binaries from|blocked subset (is |remains )?(still )?pending",
            re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, undeployed):
                bad("deployment-parity", "says the corrected software is not deployed: %s" % hit, rel)
        if not [f for f in failures if f[0] == "deployment-parity"]:
            ok("no current surface claims the corrected software is undeployed or work is still pending")

        # (c) the accepted runtime head must be recorded and agree with itself
        head = facts.get("accepted_runtime_head") or ""
        if not re.fullmatch(r"[0-9a-f]{40}", head):
            bad("acceptance-parity", "accepted_runtime_head is not a full commit id: %r" % head, STATE)
        elif "ACCEPTED_AND_CLOSED" in state.get("current_activity", ""):
            # This used to be `endswith("ACCEPTED_AND_CLOSED_AT_DARK_MATURITY")`, which silently stopped
            # emitting the moment the activity gained a suffix — exactly what closing the phase does. A rule
            # that disappears when the state advances is not a rule.
            ok("the accepted runtime head is recorded (%s)" % head[:12])

        # (d) the accepted limitation must NOT have been promoted
        promoted = re.compile("legacy live[- ]session continuity[^.]{0,40}" + chr(92) + "bPASS" + chr(92) + "b", re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, promoted):
                bad("limitation-parity", "promotes the accepted NOT-PROVEN limitation to PASS: %s" % hit, rel)
        if not [f for f in failures if f[0] == "limitation-parity"]:
            ok("the accepted NOT-PROVEN limitation is not promoted to PASS anywhere")

    # ---- 8e. MERGE PARITY -------------------------------------------------------------------------------------------
    #
    # "PR #6 remains OPEN and UNMERGED. Its merge is a separate Product-Owner decision and is the only next
    # authorized action" was true in six documents on the morning of 2026-08-11 and false that afternoon. It is
    # the same class of defect as every rule above it — a sentence that was accurate when written, left standing
    # in the present tense after the world moved — and the merge is precisely the moment nobody re-reads the
    # documentation. So the merge state is recorded as data and the prose is checked against it, in BOTH
    # directions: a repository that has merged may not still promise the merge, and one that has not merged may
    # not claim it did.
    merged = facts.get("merged")
    # Pinned to the recorded PR number so an older, genuinely-merged phase ("PR #4 merged to master") is not
    # mistaken for a statement about this one.
    prn = re.escape(str(facts.get("pr_number") or r"\d+"))
    if merged:
        for k, pat in (("merge_commit", r"[0-9a-f]{40}"), ("merged_pr", r"\d+")):
            if not re.fullmatch(pat, str(facts.get(k) or "")):
                bad("merge-parity", "merged is true but %s is %r" % (k, facts.get(k)), STATE)
        for k in ("pr_open_and_unmerged", "pr6_open_and_unmerged"):
            if facts.get(k):
                bad("merge-parity", "merged is true while %s is still true" % k, STATE)
        still_open = re.compile(
            r"pr #?" + prn + r" (remains |is )?open and unmerged|"
            r"merge is a separate product-owner decision|"
            r"(decision|authorization) on merging pr #?" + prn + r"|"
            r"merge_decision_pending|not authorized to merge pr #?" + prn,
            re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, still_open):
                bad("merge-parity", "still presents the merge as pending: %s" % hit, rel)
        if not [f for f in failures if f[0] == "merge-parity"]:
            ok("no current surface still presents the merge as pending (merge commit %s)"
               % str(facts.get("merge_commit"))[:12])
    else:
        claims_merged = re.compile(r"pr #?" + prn + r" (was |is |has been )?merged", re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, claims_merged):
                bad("merge-parity", "claims a merge that the recorded facts do not record: %s" % hit, rel)
        if not [f for f in failures if f[0] == "merge-parity"]:
            ok("no current surface claims a merge that has not happened")

    # ---- 8f. RUNTIME-IDENTITY PARITY --------------------------------------------------------------------------------
    #
    # "the runtime tree is byte-for-byte identical to the accepted runtime candidate" was exactly true when the
    # merge happened and false the moment post-closure remediation touched deploy/ and scripts/. It survived in
    # four documents because it reads like an invariant, and invariants are the sentences nobody re-checks.
    #
    # The distinction that actually matters to acceptance is narrower and still holds: the accepted APPLICATION
    # binaries and their behaviour are unchanged. So the two claims are separated here - the narrow one may be
    # stated freely; the whole-tree one may only appear scoped to the moment it described.
    if facts.get("post_closure_operational_changes"):
        whole_tree = re.compile(
            r"(runtime tree|deploy/ and scripts/|deploy/, scripts/)[^.]{0,80}byte[- ]for[- ]byte identical|"
            r"byte[- ]for[- ]byte identical[^.]{0,80}(runtime tree|whole tree)",
            re.I)
        # A statement tied to when it was true is a record, not a claim about now.
        scoped = re.compile(
            r"at the merge|as at the merge|at that moment|at the time of|describes the merge|"
            r"was byte|were byte|not today|since changed|historical|superseded",
            re.I)
        offenders = []
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for para, _ in paragraphs(text):
                if not whole_tree.search(para):
                    continue
                if HISTORY_MARKERS.search(para) or scoped.search(para):
                    continue
                offenders.append((rel, " ".join(para.split())[:180]))
        for rel, hit in offenders:
            bad("runtime-identity-parity",
                "claims the whole runtime tree is byte-for-byte identical to the accepted candidate, "
                "unscoped, while post-closure operational changes are recorded: %s" % hit, rel)
        if not offenders:
            ok("no current surface claims an unscoped whole-tree identity with the accepted candidate")

        # The narrow claim must be RECORDED, not merely absent: dropping both statements would also pass the
        # check above while telling the reader nothing about what acceptance still rests on.
        if not facts.get("accepted_runtime_binaries_unchanged"):
            bad("runtime-identity-parity",
                "post-closure operational changes are recorded but accepted_runtime_binaries_unchanged is not "
                "asserted; the fact acceptance rests on must be stated, not merely implied", STATE)
        elif facts.get("accepted_runtime_binaries_head") != facts.get("accepted_runtime_head"):
            bad("runtime-identity-parity",
                "accepted_runtime_binaries_head %r disagrees with accepted_runtime_head %r"
                % (facts.get("accepted_runtime_binaries_head"), facts.get("accepted_runtime_head")), STATE)
        else:
            ok("the accepted-binaries invariant is recorded and agrees with the accepted runtime head")

    # ---- 8g. PHASE-STATUS PARITY ------------------------------------------------------------------------------------
    #
    # project-state.json recorded Phase 4 as the current AUTHORIZED PLANNING phase while the same file's
    # current_maturity narrative still ended "Phase 4 remains NOT_STARTED and unauthorized". Both sentences
    # were written in the same round; nothing compared them, because every existing rule was about Phase 3.
    #
    # Generic over phases, not another Phase-N special case: for EVERY phase whose recorded status is not
    # NOT_STARTED, no current surface may still call that phase not-started or unauthorized. It therefore
    # catches the same mistake at Phase 5, 6 and 7 with no new rule.
    #
    # The state FILE is checked field-by-field rather than as text. Paragraph-based history exclusion is
    # meaningless for a single-line JSON document: the whole file is one "paragraph" and it contains the word
    # "historical" somewhere, so a text scan silently excuses every contradiction in it. That is exactly how
    # the first version of this rule passed the very contradiction it was written for.
    phases = state.get("phases") or {}
    started = sorted(n for n, ph in phases.items()
                     if isinstance(ph, dict) and ph.get("status") not in (None, "NOT_STARTED"))
    stale_phase_hits = []
    for n in started:
        pat = re.compile(
            r"phase[\s-]*" + re.escape(n) + r"\b[^.]{0,60}?"
            r"(remains?|is|are)\s+(still\s+)?(not[_\s]started|unauthoriz|not\s+authoriz)",
            re.I)
        # (a) narrative FIELDS of the state file, examined individually
        fields = {"current_maturity": state.get("current_maturity", ""),
                  "next_authorized_action": state.get("next_authorized_action", "")}
        for i, b in enumerate(state.get("blockers") or []):
            fields["blockers[%d]" % i] = str(b)
        for i, a in enumerate(state.get("allowed_actions") or []):
            fields["allowed_actions[%d]" % i] = str(a)
        for pn, ph in phases.items():
            if isinstance(ph, dict) and ph.get("maturity"):
                fields["phases.%s.maturity" % pn] = str(ph["maturity"])
        for fname, val in fields.items():
            m = pat.search(val)
            if m and not HISTORY_MARKERS.search(val[max(0, m.start() - 160):m.end() + 60]):
                stale_phase_hits.append((n, "governance/project-state.json -> " + fname,
                                         " ".join(val[max(0, m.start() - 60):m.end() + 40].split())))
        # (b) the markdown surfaces, where paragraph labelling is meaningful
        for rel in DOC_SURFACES:
            if rel.endswith(".json"):
                continue
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, pat):
                stale_phase_hits.append((n, rel, hit))
    for n, where, hit in stale_phase_hits:
        bad("phase-status-parity",
            "phase %s is recorded as %s but a current surface still calls it not-started/unauthorized: %s"
            % (n, phases[n].get("status"), hit), where)
    if not stale_phase_hits:
        ok("no current surface calls a started phase not-started or unauthorized (checked %s)"
           % (", ".join(started) or "none"))

    # ---- 8h. AUTHORIZATION-MODEL PARITY -----------------------------------------------------------------------------
    #
    # allowed_actions said "Execute the authorized Phase 4 ... Posting Engine, outbox lanes, UNKNOWN handling"
    # while prohibited_actions two entries later still said "Posting Engine, posting outbox/worker, financial
    # UNKNOWN handling" were forbidden. Both were true of their own phase and the file authorized and forbade
    # the same software at once. Rule 8g did not see it: neither sentence calls a phase not-started.
    #
    # The distinction that matters is EGRESS and ENABLEMENT, not the existence of authorized software, so this
    # rule refuses a prohibition that names the current phase's own deliverables WITHOUT qualifying itself as
    # being about real traffic, enablement or a later phase.
    cur_phase = str(state.get("current_phase", ""))
    allowed = [str(a) for a in (state.get("allowed_actions") or [])]
    prohibited = [str(a) for a in (state.get("prohibited_actions") or [])]

    # (a) the current phase must not be forbidden outright.
    # "beyond the authorized Phase N" is the CORRECT idiom — it forbids the phases AFTER N, not N itself —
    # so only a prohibition on implementing N counts as a contradiction.
    cur_forbidden = re.compile(r"(?<!beyond the authorized )implement\w*\s+(any\s+)?phase\s*"
                               + re.escape(cur_phase) + r"\b", re.I)
    for i, pa in enumerate(prohibited):
        if cur_forbidden.search(pa):
            bad("authorization-model-parity",
                "prohibited_actions[%d] forbids implementing the CURRENT authorized phase %s: %s"
                % (i, cur_phase, " ".join(pa.split())[:150]), STATE)

    # (b) a prohibition naming this phase's own deliverables must say it is about REAL traffic/enablement
    deliverables = re.compile(r"posting engine|posting outbox|outbox/worker|outbox worker|"
                              r"financial unknown handling|settlement execution", re.I)
    qualifier = re.compile(r"\breal\b|egress|enablement|enabl\w+|live pms|guest folio|provider|"
                           r"authorized under|is authorized|note:", re.I)
    for i, pa in enumerate(prohibited):
        if deliverables.search(pa) and not qualifier.search(pa):
            bad("authorization-model-parity",
                "prohibited_actions[%d] forbids this phase's own authorized deliverables without limiting "
                "itself to real traffic or enablement: %s" % (i, " ".join(pa.split())[:150]), STATE)

    # (c) an unauthorized future phase must not appear in allowed_actions
    unauth = sorted(n for n, ph in (state.get("phases") or {}).items()
                    if isinstance(ph, dict) and ph.get("status") == "NOT_STARTED")
    for n in unauth:
        pat = re.compile(r"(execute|implement)\w*\s+(the\s+)?(authorized\s+)?phase\s*" + re.escape(n) + r"\b", re.I)
        for i, aa in enumerate(allowed):
            if pat.search(aa):
                bad("authorization-model-parity",
                    "allowed_actions[%d] permits executing phase %s, which is NOT_STARTED/unauthorized: %s"
                    % (i, n, " ".join(aa.split())[:150]), STATE)

    # (d) current_phase_plan must belong to the current phase
    plan_rel = str(state.get("current_phase_plan") or "")
    if plan_rel:
        m = re.search(r"Phase(\d+[AB]?)-", plan_rel) or re.search(r"Phase(\d+[AB]?)\b", plan_rel)
        if m and m.group(1).upper() != cur_phase.upper():
            bad("authorization-model-parity",
                "current_phase_plan %r belongs to phase %s but current_phase is %s"
                % (plan_rel, m.group(1), cur_phase), STATE)
        elif not os.path.isfile(os.path.join(ROOT, plan_rel)):
            bad("authorization-model-parity", "current_phase_plan %r does not exist" % plan_rel, STATE)

    # (e) facts.phase must agree with current_phase
    if facts.get("phase") and str(facts["phase"]) != cur_phase:
        bad("authorization-model-parity",
            "current_state_facts.phase=%r disagrees with current_phase=%r" % (facts["phase"], cur_phase), STATE)

    # (f) a recorded phase authorization must name the decision and transition that granted it
    dec, tr = facts.get("phase4_decision"), facts.get("phase4_transition")
    if facts.get("phase4_status"):
        if dec != "D18" or tr != "T0029":
            bad("authorization-model-parity",
                "Phase-4 authorization facts say decision=%r transition=%r; D18/T0029 granted it" % (dec, tr),
                STATE)
        elif not os.path.isfile(os.path.join(TRANSITIONS_DIR, "%s.json" % tr)):
            bad("authorization-model-parity", "Phase-4 authorization cites %s but no receipt exists" % tr, STATE)

    # (g) no current next-step may demand an authorization that already exists
    needs_auth = re.compile(r"phase\s*4[^.]{0,80}(require|await|need)\w*[^.]{0,40}"
                            r"(separate|explicit|product-owner)\s+authoriz", re.I)
    for fname in ("next_authorized_action",):
        val = str(state.get(fname) or "")
        if needs_auth.search(val) and not HISTORY_MARKERS.search(val):
            bad("authorization-model-parity",
                "%s says Phase 4 still requires an authorization that D18/T0029 already granted: %s"
                % (fname, " ".join(val.split())[:150]), STATE)
    ns = str(facts.get("next_step") or "")
    if needs_auth.search(ns) and not HISTORY_MARKERS.search(ns):
        bad("authorization-model-parity",
            "current_state_facts.next_step says Phase 4 still requires an authorization it already has: %s"
            % " ".join(ns.split())[:150], STATE)

    if not [f for f in failures if f[0] == "authorization-model-parity"]:
        ok("the authorization model is self-consistent (phase %s authorized, its deliverables not forbidden, "
           "no unauthorized phase permitted)" % cur_phase)

    # ---- 9. the rollback runbook must not promise authorization it cannot preserve -----------------------------------
    #
    # The boundary section says a pre-convergence target may not preserve authorization. A later generic paragraph
    # said the ruleset simply follows the binaries, which reads as the opposite promise. Two true-sounding sentences
    # in one document that contradict each other is exactly the failure this validator exists for.
    runbook = load_surface("docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md")
    if runbook:
        promise = re.compile(r"carr(y|ies|ying) live authorization across|authorization is (always )?carried across the change", re.I)
        offenders = []
        for para, _ in paragraphs(runbook):
            if not promise.search(para):
                continue
            if HISTORY_MARKERS.search(para):
                continue
            # a paragraph that also states the boundary/condition is not an unconditional promise
            if re.search(r"depends entirely on the target|convergence-capable|empty|refus|boundary", para, re.I):
                continue
            offenders.append(" ".join(para.split())[:180])
        for hit in offenders:
            bad("rollback-promise-parity",
                "promises that rollback carries authorization across without stating the boundary: %s" % hit,
                "docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md")
        if not offenders:
            ok("the rollback runbook never promises preserved authorization without stating the boundary")

    # ---- 10. EVERY REFERENCED EVIDENCE FILE MUST ACTUALLY BE IN THE REPOSITORY --------------------------------------
    #
    # T0024 listed docs/evidence/Phase3-Final-Live-Acceptance-Record.md as evidence and the Final Report pointed
    # readers at it. The file was written, and then silently never committed: .gitignore carried an UNANCHORED
    # `evidence/` rule meant for generated bundles, which also matches docs/evidence/ at any depth. Every gate
    # passed, because every gate only ever read files that were there.
    #
    # A receipt that cites evidence nobody else can open is worse than one that cites none: it reads as proof.
    import subprocess
    tdir = os.path.join(ROOT, "governance", "transitions")
    # The tracked set is the strong form of the check. Where git cannot answer — an extracted pack, a test
    # sandbox — fall back to "does the file exist under ROOT". Weaker, but still catches a citation of
    # something that is not there, which is the defect. What must never happen is a silent pass.
    tracked = None
    try:
        r = subprocess.run(["git", "-C", ROOT, "ls-files"], capture_output=True, text=True, timeout=60)
        if r.returncode == 0 and r.stdout.strip():
            tracked = set(r.stdout.split())
    except Exception:  # noqa: BLE001
        tracked = None

    def expand_braces(rel):
        """Expand ONE level of {a,b} alternation in a cited path.

        Receipts cite a migration's two halves compactly, as
        `data-plane/migrations/0026_....{up,down}.sql`. That names two real, tracked files; treating the
        literal string as a filename reported a citation of something that does exist, which is the
        opposite of what this rule is for. Only a single simple alternation is expanded -- anything more
        elaborate stays literal and fails loudly rather than being guessed at.
        """
        a = rel.find("{")
        b = rel.find("}", a + 1)
        if a < 0 or b < 0 or "{" in rel[a + 1:b]:
            return [rel]
        return [rel[:a] + part + rel[b + 1:] for part in rel[a + 1:b].split(",")]

    def cited_present(rel):
        for one in expand_braces(rel):
            if tracked is not None:
                if one not in tracked:
                    return False
            elif not os.path.exists(os.path.join(ROOT, one)):
                return False
        return True

    if not os.path.isdir(tdir):
        bad("evidence-reference", "governance/transitions is missing, so cited evidence cannot be checked", ROOT)
    else:
        how = "tracked in git" if tracked is not None else "present on disk (git unavailable)"
        missing = []
        for rel in sorted(os.listdir(tdir)):
            if not rel.endswith(".json"):
                continue
            try:
                t = json.load(io.open(os.path.join(tdir, rel), encoding="utf-8"))
            except Exception:  # noqa: BLE001
                continue
            for ev in t.get("evidence_files") or []:
                ev = str(ev).strip()
                if not ev or ev.endswith("/"):
                    continue
                if not cited_present(ev):
                    missing.append("%s cites %s" % (rel, ev))
        for m in missing:
            bad("evidence-reference", "a transition receipt cites evidence that is not %s: %s" % (how, m),
                "governance/transitions")
        if not missing:
            ok("every file cited as evidence by a transition receipt is %s" % how)

    # ---- 9. TRANSITION / PHASE-STATUS COHERENCE --------------------------------------------------------------------
    #
    # THE FALSE PASS THIS CLOSES. T0044 recorded new_state.phase_status = ACCEPTED_AND_CLOSED for phase 4, and
    # phases["4"].status stayed IN_PROGRESS. Every existing rule passed: the receipt was well formed, the facts
    # block was coherent with itself, and no forbidden word appeared anywhere. The two records simply disagreed
    # about the same thing, and the generated START-HERE / Handoff / Contract blocks then printed BOTH -- "Phase 4
    # accepted and closed" and "4 IN_PROGRESS" -- in the same paragraph, because those blocks read the phases map
    # while the prose read the receipt.
    #
    # A transition receipt is the instrument that MOVES the state. If the state it moved to is not the state the
    # phases map records, one of them is a lie and there is no way to tell which from inside either.
    latest_id = state.get("latest_transition_id")
    cur_phase = str(state.get("current_phase", ""))
    if latest_id and cur_phase:
        rpath = os.path.join(TRANSITIONS_DIR, "%s.json" % latest_id)
        try:
            receipt = json.load(io.open(rpath, encoding="utf-8"))
        except Exception as exc:  # noqa: BLE001
            receipt = None
            bad("transition-phase-coherence",
                "latest_transition_id is %s but %s.json is unreadable: %s" % (latest_id, latest_id, exc), STATE)
        if receipt is not None:
            recorded = ((receipt.get("new_state") or {}).get("phase_status") or "").strip()
            affected = str(receipt.get("phase_affected") or receipt.get("new_state", {}).get("phase") or "").strip()
            phases = state.get("phases") or {}
            # The receipt speaks about the phase it names; fall back to current_phase when it names none.
            target = affected if affected in phases else cur_phase
            actual = ((phases.get(target) or {}).get("status") or "").strip()
            if not recorded:
                bad("transition-phase-coherence",
                    "%s records no new_state.phase_status, so nothing constrains phases[%s].status"
                    % (latest_id, target), rpath)
            elif recorded != actual:
                bad("transition-phase-coherence",
                    "%s moved phase %s to %r but phases[%s].status is %r -- the receipt and the phases map "
                    "disagree, and every generated block prints the phases map"
                    % (latest_id, target, recorded, target, actual), STATE)
            else:
                ok("the latest transition (%s) and phases[%s].status agree (%s)" % (latest_id, target, actual))

    # ---- 10. ACCEPTED-PHASE SEMANTICS ------------------------------------------------------------------------------
    #
    # THE FALSE PASS THIS CLOSES. With phase 4 recorded ACCEPTED_AND_CLOSED, four current surfaces still described
    # it as unfinished: current_maturity narrated implementation-in-progress and remaining work, allowed_actions
    # still authorized "Execute the authorized Phase 4 ... end-to-end", and the Phase-4 Plan carried both an
    # accepted/closed header AND the sentences "Phase 4 is NOT accepted and NOT closed" and "Not accepted, not
    # closed." A keyword gate cannot catch that, because in a repository where the phase really is unfinished
    # every one of those sentences is correct. What makes them wrong is the recorded status, so the rule has to
    # be relative to it.
    #
    # Generic over phases: it derives the set of accepted phases from the phases map and needs no new rule when
    # phase 5, 6 or 7 closes. It is also deliberately two-sided in scope -- it fires on the STATE FILE's own
    # narrative fields and on the plan/handoff surfaces, because that is where both halves of the contradiction
    # actually lived.
    phases = state.get("phases") or {}
    accepted_phases = sorted(k for k, v in phases.items()
                             if str((v or {}).get("status", "")).upper() in ("ACCEPTED_AND_CLOSED", "FINAL_CLOSED"))
    PHASE_PLANS = {
        "1A": "docs/architecture/StayConnect-IAM-Phase1A-Plan.md",
        "1B": "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
        "3": "docs/architecture/StayConnect-IAM-Phase3-Plan.md",
        "4": "docs/architecture/StayConnect-IAM-Phase4-Plan.md",
    }
    for pk in accepted_phases:
        num = re.escape(pk)
        # "phase 4 is not accepted", "phase-4 ... not closed", "phase 4 remains in progress", and the bare
        # headline forms the Plan used.
        # A short intervening noun must not defeat the match: "Phase 1B IMPLEMENTATION is ... IN_PROGRESS"
        # slipped through while "Phase 1B is IN_PROGRESS" was caught, and they say the same thing. Bounded to
        # ~40 characters and stopped at a sentence end so it cannot reach across into an unrelated clause.
        unfinished = re.compile(
            r"phase[- ]?" + num + r"\b(?:(?!phase)[^.;\n]){0,40}?\b(?:is|remains|stays)\s+(?:(?!phase)[^.;\n]){0,30}?\b"
            r"(?:not\s+accepted|not\s+closed|in[_ -]?progress|"
            r"not\s+started|unauthori[sz]ed|a\s+candidate)|"
            r"phase[- ]?" + num + r"\s+is\s+NOT\s+accepted\s+and\s+NOT\s+closed|"
            r"\bnot\s+accepted,\s+not\s+closed\b|"
            r"phase[- ]?" + num + r"[^.\n]{0,40}awaiting\s+product[- ]owner\s+acceptance|"
            r"awaiting\s+product[- ]owner\s+acceptance\s+of\s+phase[- ]?" + num,
            re.I)
        # The PACK entry points are highest-authority surfaces too -- 00-START-HERE is the FIRST file a
        # reader opens -- and this rule was reading neither, so a "Phase 1B implementation is authorized and
        # IN_PROGRESS" section there was invisible to it.
        surfaces = list(DOC_SURFACES) + [
            "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
            "exports/chatgpt/stayconnectenterprise/PROJECT-INSTRUCTIONS.md",
        ]
        if pk in PHASE_PLANS:
            surfaces.append(PHASE_PLANS[pk])
        for rel in surfaces:
            text = load_surface(rel)
            if text is None:
                continue
            for para, _ in paragraphs(text):
                if not unfinished.search(para):
                    continue
                if HISTORY_MARKERS.search(para):
                    continue
                # A sentence that quotes the wrong wording IN ORDER TO CORRECT IT is not the wrong wording.
                if re.search(r"corrected|the line this replaces|read \"|reads \"|used to say|overstat", para, re.I):
                    continue
                bad("accepted-phase-semantics",
                    "phases[%s].status is accepted/closed, but this presents phase %s as unfinished: %s"
                    % (pk, pk, " ".join(para.split())[:180]), rel)

        # allowed_actions must not still authorize EXECUTING a phase that is closed. Governance and
        # documentation maintenance for a closed phase is legitimate and is not matched.
        execute = re.compile(r"(execute|continue|deliver|implement)[^.\n]{0,60}phase[- ]?" + num + r"\b", re.I)
        for i, act in enumerate(state.get("allowed_actions") or []):
            if not isinstance(act, str) or not execute.search(act):
                continue
            if re.search(r"governance|documentation|maintenance|historical|closed", act, re.I):
                continue
            bad("accepted-phase-semantics",
                "allowed_actions[%d] still authorizes executing phase %s, which is recorded %s: %s"
                % (i, pk, phases[pk].get("status"), " ".join(act.split())[:160]), STATE)
    if not [f for f in failures if f[0] == "accepted-phase-semantics"]:
        ok("no current surface presents an accepted/closed phase as unfinished (accepted: %s)"
           % ", ".join(accepted_phases))

    # ---- 11. STATIC CURRENT-STATE PROSE OUTSIDE THE GENERATED BLOCK ------------------------------------------
    #
    # THE FALSE PASS THIS CLOSES. Every rule above reads either the machine-readable facts or the GENERATED
    # block, and the generated blocks were all correct: each said Phase 4 was ACCEPTED_AND_CLOSED. The prose
    # AROUND them was not, and nothing looked at it:
    #
    #   00-START-HERE.md      "FIAS connector is lookup-only today; the financial posting engine is a FUTURE
    #                          COMPONENT" -- the engine exists and is accepted at LIVE-DARK maturity. It is
    #                          disabled, not absent, and those are opposite claims to anyone planning work.
    #   Handoff.md            "CURRENT (see the generated block): Phase 2 ... " -- a restatement that pointed
    #                          at the block and then contradicted it, two phases later.
    #   Phase0-Contract.md    "Next authorized activity: Product-Owner acceptance of Phase 1A, then Phase 1B
    #                          planning" -- true in July, still present in the status table in August.
    #   Phase4-Plan.md        "What does not exist -- the Phase-4 build ... the execution runtime is
    #                          greenfield" -- authorization-time history reading as current status.
    #
    # A generated block cannot protect the document it sits in. So the highest-authority surfaces are read
    # OUTSIDE their generated region, and prose that DENOTES CURRENT STATE must either agree with canonical
    # state or admit to being history. The existing paragraph-level HISTORY_MARKERS excuse applies unchanged:
    # this rule refuses unlabelled claims, never history that says so.
    STATIC_SURFACES = [
        "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
        "exports/chatgpt/stayconnectenterprise/PROJECT-INSTRUCTIONS.md",
        "docs/context/StayConnect-IAM-Handoff.md",
        "docs/architecture/StayConnect-IAM-Phase0-Contract.md",
        "docs/architecture/StayConnect-IAM-Phase4-Plan.md",
        "docs/architecture/StayConnect-IAM-Phase1A-Plan.md",
        "docs/architecture/StayConnect-IAM-Phase1B-Plan.md",
    ]
    BEGIN_BLOCK = "<!-- BEGIN GENERATED PROJECT STATE"
    END_BLOCK = "<!-- END GENERATED PROJECT STATE"

    def outside_generated(text):
        """The document minus its generated region. The block is machine-written and already validated;
        what this rule is for is the hand-written prose that surrounds it."""
        i = text.find(BEGIN_BLOCK)
        if i < 0:
            return text
        j = text.find(END_BLOCK, i)
        if j < 0:
            return text[:i]
        return text[:i] + text[j:].split("\n", 1)[-1] if "\n" in text[j:] else text[:i]

    phases = state.get("phases") or {}
    p4_closed = str((phases.get("4") or {}).get("status", "")).upper() in ("ACCEPTED_AND_CLOSED", "FINAL_CLOSED")

    static_rules = []
    if p4_closed:
        # The financial posting engine EXISTS. "Future component", "does not exist", "greenfield" and
        # "zero Go references" describe a repository that no longer exists.
        static_rules.append((
            re.compile(r"posting engine is a[^.\n]{0,20}(future|new) component|"
                       r"financial[^.\n]{0,40}engine[^.\n]{0,30}\bfuture\b|"
                       r"lookup-only today|"
                       r"what does not exist\s*[-—]{0,2}\s*the phase-4 build|"
                       r"execution runtime is greenfield|"
                       r"runtime is greenfield|"
                       r"all seven financial tables have zero go references", re.I),
            "describes the Phase-4 financial runtime as future/nonexistent, but phase 4 is recorded "
            "%s -- the engine exists and is DISABLED, which is not the same claim"
            % (phases.get("4") or {}).get("status")))

    # A next-authorized-activity or current-activity restatement outside the generated block is a second
    # copy of the one fact that must have exactly one home.
    static_rules.append((
        re.compile(r"next authorized activity\s*[:|]|is the current activity|"
                   r"\bCURRENT\b[^.\n]{0,30}:\s*Phase\s*\d", re.I),
        "restates the current activity or next authorized activity outside the generated block, which is "
        "the only surface allowed to carry it"))

    # Awaiting acceptance of, or future authorization for, a phase that is already closed.
    closed_nums = [k for k, v in phases.items()
                   if str((v or {}).get("status", "")).upper() in ("ACCEPTED_AND_CLOSED", "FINAL_CLOSED")]
    if closed_nums:
        alt = "|".join(re.escape(k) for k in closed_nums)
        static_rules.append((
            re.compile(r"awaiting[^.\n]{0,40}acceptance of phase[- ]?(%s)\b|"
                       r"phase[- ]?(%s)\b[^.\n]{0,40}(remains|is) under[^.\n]{0,20}future authorization" % (alt, alt),
                       re.I),
            "presents a closed phase as awaiting acceptance or as future work"))

    for rel in STATIC_SURFACES:
        text = load_surface(rel)
        if text is None:
            continue
        body = outside_generated(text)
        for para, _ in paragraphs(body):
            if HISTORY_MARKERS.search(para):
                continue
            # A paragraph that quotes the wrong wording in order to correct it is not the wrong wording.
            if re.search(r"the line this replaces|corrected forward|reads \"|read \"|overstat|"
                         r"is the only surface allowed|carries none|does not carry", para, re.I):
                continue
            for pattern, why in static_rules:
                if pattern.search(para):
                    bad("static-current-prose", "%s: %s" % (why, " ".join(para.split())[:170]), rel)
                    break
    if not [f for f in failures if f[0] == "static-current-prose"]:
        ok("no unlabelled current-state prose outside the generated blocks contradicts canonical state")

    # ---- 12. STRUCTURAL: A SECTION MAY NOT CLAIM TO CARRY MUTABLE CURRENT STATE ------------------------------
    #
    # THE FALSE PASS THIS CLOSES. Rule 11 above reads paragraphs and refuses known stale ASSERTIONS. It went
    # green on 00-START-HERE while that file still contained, below a correct generated block:
    #
    #     ## 8. Current approved plan (Phase 1A)
    #     ## 9. Next authorized action
    #        "The single next authorized action is complete Phase 1B execution and live-dark verification."
    #        "Phase 1B implementation is Product-Owner authorized and IN_PROGRESS"
    #        "Gate P (in progress)"
    #     | StayConnect-IAM-Phase1B-Plan.md | ... awaiting PO approval/rejection; not implemented |
    #
    # Chasing those with more keywords is a losing game -- the wording changes every phase. What does NOT
    # change is the STRUCTURE: a heading that announces itself as the current plan, the current status or the
    # next authorized action is claiming to carry mutable state, and exactly one surface is allowed to do
    # that. So this rule reads HEADINGS, not sentences, and refuses the claim itself unless the heading or
    # its opening lines say the section is historical.
    #
    # It cannot be satisfied by rewording the body: the section either owns current state or admits it does
    # not.
    heading_claim = re.compile(
        r"^\s{0,3}#{1,6}\s+.*?("
        r"current\s+(approved\s+)?(plan|phase|status|state|activity|position)|"
        r"next\s+(authorized|approved)\s+(action|activity|step)|"
        r"current\s+project\s+phase"
        r")", re.I)
    # A heading that says it is history, or whose first lines do, is a record and not a claim.
    heading_history = re.compile(
        r"historical|as at|as[- ]of|superseded|no longer|archive|"
        r"is the generated project state block|generated project state block at the top",
        re.I)

    for rel in STATIC_SURFACES:
        text = load_surface(rel)
        if text is None:
            continue
        body = outside_generated(text)
        lines = body.split("\n")
        for i, line in enumerate(lines):
            if not heading_claim.match(line):
                continue
            # The heading itself, plus the few lines under it, may carry the label.
            window = "\n".join(lines[i:i + 6])
            if heading_history.search(window):
                continue
            # A heading that explicitly DEFERS to the block is the correct pattern, not a violation:
            # "## 3. Current project phase & status" whose body immediately says the block is authoritative.
            if re.search(r"is the GENERATED PROJECT STATE block|"
                         r"Do not maintain a second current-state description|"
                         r"the only surface|carries them|carries it", window, re.I):
                continue
            bad("static-current-prose",
                "a section heading claims to carry mutable current state without being marked historical "
                "or deferring to the generated block: %s" % " ".join(line.split())[:120], rel)

    if not [f for f in failures if f[0] == "static-current-prose"]:
        ok("no section outside a generated block claims to carry the current plan, status or next action")

    # ---- 12. A MERGED PULL REQUEST DESCRIBED AS OPEN -----------------------------------------------------------
    #
    # THE FALSE PASS THIS CLOSES. After PR #12 was merged under D20/T0048, two current surfaces still said it
    # was not:
    #
    #   project-state.json    current_maturity: "THE ONLY OPEN ITEM is the Product Owner's separate decision on
    #                         merging Phase-4 pull request #12, which is OPEN and UNMERGED."
    #   Phase4-Plan.md        "The Phase-4 pull request is OPEN and UNMERGED; merging requires a separate
    #                         explicit Product-Owner decision."
    #
    # Every rule above passed on that tree. The phase status was right, the generated blocks were right, and
    # the prose was PLAUSIBLE -- it had been true for a day. Merge state is exactly the kind of fact that goes
    # stale silently, because merging happens on GitHub and nothing in the repository changes.
    #
    # So the merged PR numbers are DERIVED from the recorded facts rather than hardcoded (any `*merged_pr`, or
    # a `*pr_state` recording a merge alongside its `*pr_number`), and no current surface may describe one of
    # them as open, unmerged or not-to-be-merged. Labelled history is excused exactly as everywhere else: the
    # decision register's D16 and D19 entries record what was authorized AT THE TIME and are dated records, so
    # they are not read here, and a paragraph that says it is historical is skipped.
    merged_prs = {}
    for key, val in facts.items():
        m = re.match(r"^(?:(phase[0-9a-z]*)_)?merged_pr$", key)
        if m and isinstance(val, int):
            merged_prs[val] = m.group(1) or "the project"
    for key, val in facts.items():
        m = re.match(r"^(?:(phase[0-9a-z]*)_)?pr_state$", key)
        if not (m and isinstance(val, str) and re.search(r"\bMERGED\b", val, re.I)):
            continue
        num = facts.get(("%s_pr_number" % m.group(1)) if m.group(1) else "pr_number")
        if isinstance(num, int):
            merged_prs.setdefault(num, m.group(1) or "the project")

    if merged_prs:
        merge_surfaces = sorted(set(
            list(DOC_SURFACES) + list(STATIC_SURFACES) + list(PHASE_PLANS.values()) + [
                "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
                "exports/chatgpt/stayconnectenterprise/PROJECT-INSTRUCTIONS.md",
                "docs/reports/StayConnect-IAM-Phase4-Final-Report.md",
                "docs/acceptance/StayConnect-IAM-Phase4-Live-Dark-Acceptance.md",
            ]))
        for num, owner in sorted(merged_prs.items()):
            n = re.escape(str(num))
            # Either the PR is named and then called open, or the "do not merge" instruction is still standing
            # within reach of its number. Bounded so it cannot bridge a sentence end into an unrelated clause.
            stale = re.compile(
                r"(?:pull\s+request|\bPR\b)[^.;\n]{0,60}?#?" + n + r"\b(?:[^.;\n]{0,80}?)"
                r"(?:is|remains|stays|left|be)\s+(?:[^.;\n]{0,30}?)"
                r"(?:open\s+and\s+unmerged|unmerged|not\s+merged|open,\s*unmerged)|"
                r"#" + n + r"\b[^.;\n]{0,60}?\bDO\s+NOT\s+MERGE\b|"
                r"\bDO\s+NOT\s+MERGE\b[^.;\n]{0,60}?#" + n + r"\b",
                re.I)
            # ...and the un-numbered form the Plan used, on a surface that is ABOUT that phase, where "the
            # Phase-4 pull request" can only mean this one.
            bare = re.compile(
                r"(?:the\s+)?phase[- ]?" + re.escape(str(owner).replace("phase", "")) +
                r"\s+pull\s+request[^.;\n]{0,40}?\b(?:is|remains|stays)\b[^.;\n]{0,30}?"
                r"(?:open\s+and\s+unmerged|unmerged|not\s+merged)", re.I) if owner.startswith("phase") else None
            for rel in merge_surfaces:
                text = load_surface(rel)
                if text is None:
                    continue
                # project-state.json is ONE paragraph -- it has no blank lines -- and it contains the words
                # "AS AT". Paragraph-level history excusing therefore excuses the ENTIRE file, which is how a
                # stale `current_maturity` sentence sat inside the canonical state document and passed. The
                # unit of labelling in a JSON document is the VALUE, so it is scanned value by value.
                if rel.endswith(".json"):
                    units = [v for v in json_strings(load_json(rel))]
                else:
                    units = [p for p, _ in paragraphs(text)]
                for para in units:
                    if HISTORY_MARKERS.search(para):
                        continue
                    hit = stale.search(para) or (bare.search(para) if bare else None)
                    if not hit:
                        continue
                    bad("merged-pr-state",
                        "the recorded facts say PR #%d is MERGED, but this presents it as open/unmerged: %s"
                        % (num, " ".join(para.split())[:180]), rel)
    if not [f for f in failures if f[0] == "merged-pr-state"]:
        ok("no current surface describes a merged pull request as open or unmerged (merged: %s)"
           % (", ".join("#%d" % n for n in sorted(merged_prs)) or "none recorded"))

    # ---- report ----------------------------------------------------------------------------------------------------
    if as_json:
        print(json.dumps({
            "pass": len(notes), "fail": len(failures),
            "checks": [{"status": "PASS", "rule": n} for n in notes] +
                      [{"status": "FAIL", "rule": r, "detail": d, "where": w} for r, d, w in failures],
        }))
    else:
        for n in notes:
            print("  [PASS] %s" % n)
        for rule, detail, where in failures:
            print("  [FAIL] %s — %s" % (rule, detail))
            print("         in %s" % where)
        print("=" * 60)
        print("CURRENT_STATE_PARITY: pass=%d fail=%d -> %s"
              % (len(notes), len(failures), "PASS" if not failures else "FAIL"))
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
