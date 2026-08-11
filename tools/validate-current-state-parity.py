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

    def cited_present(rel):
        if tracked is not None:
            return rel in tracked
        return os.path.exists(os.path.join(ROOT, rel))

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
