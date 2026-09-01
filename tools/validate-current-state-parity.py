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


HIST_RX = re.compile(r"\b(?:was|were|previously|formerly|historical(?:ly)?|until|before|no longer|superseded|earlier wording)\b", re.I)


def paragraphs(text):
    """Yield (paragraph, offset). A paragraph is the unit of labelling: a historical marker excuses the
    statements around it, not the whole file."""
    off = 0
    for para in re.split(r"\n\s*\n", text):
        yield para, off
        off += len(para) + 2


def split_clauses(text):
    """Yield (offset, clause) for sentence-sized clauses of one unit.

    A "unit" is a paragraph in markdown or a whole value in JSON, and a JSON value can be thousands of
    characters carrying many independent statements. Rules that excuse a unit because it contains a marker
    somewhere are only safe when the unit is about ONE thing, so anything longer is split here first.
    Splitting on sentence terminators is deliberately crude: it does not need to be linguistically right, it
    needs to stop one corrected sentence from vouching for the rest of the document.
    """
    for m in re.finditer(r"[^.;!?\n]+[.;!?\n]?", text):
        clause = m.group(0)
        if clause.strip():
            yield m.start(), clause

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
        # WHICH phase these acceptance facts describe must be RECORDED, not assumed. This rule read phases["3"]
        # literally and bound current_activity to the words ACCEPTED_AND_CLOSED — correct only while Phase 3 was
        # the phase in flight. Once a LATER phase was authorized and in progress, a perfectly true
        # current_activity of PHASE_5_IMPLEMENTATION_IN_PROGRESS_DARK failed a rule that was really asking about
        # Phase 3. Acceptance facts belong to a phase, so the phase is now named and the rule follows it.
        apk = str(facts.get("accepted_phase") or "").strip()
        if not apk:
            bad("facts-coherence",
                "acceptance is recorded without accepted_phase, so no rule can tell WHICH phase it describes",
                STATE)
        else:
            ph = (state.get("phases") or {}).get(apk) or {}
            if ph.get("status") != "ACCEPTED_AND_CLOSED":
                bad("facts-coherence",
                    "facts say Phase %s is accepted and closed but phases.%s.status is %r"
                    % (apk, apk, ph.get("status")), STATE)
            # current_activity describes what is happening NOW. It must echo the acceptance only while the
            # accepted phase is still the current one; a later in-progress phase legitimately says something else.
            if str(state.get("current_phase") or "") == apk and \
                    "ACCEPTED_AND_CLOSED" not in state.get("current_activity", ""):
                bad("facts-coherence",
                    "phase %s is the current phase and is accepted and closed, but current_activity is %r"
                    % (apk, state.get("current_activity")), STATE)
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
        # (a) nothing current may still call an ACCEPTED phase a candidate / in progress / not accepted.
        # The "phase N is not accepted" form is now checked for EVERY accepted phase rather than the literal
        # Phase 3 this rule was born with. The remaining literal phrases are Phase-3 status wording, and one of
        # them needed a real distinction rather than a looser pattern: Phase 3's maturity was named DARK, while
        # Phase 4 and Phase 5 are named LIVE-DARK. "the final LIVE-DARK acceptance candidate" is a true
        # statement about a later phase, and the negative lookbehind separates the two named maturities exactly.
        apks = sorted(k for k, v in (state.get("phases") or {}).items()
                      if isinstance(v, dict) and v.get("status") == "ACCEPTED_AND_CLOSED")
        alts = "|".join(r"phase[- ]?" + re.escape(k) + r" is (not accepted|in_progress|a candidate)"
                        for k in apks)
        not_accepted = re.compile(
            (alts + "|" if alts else "") +
            r"\bNOT accepted\b|\bnot yet accepted\b|"
            r"(?<!live-)dark acceptance candidate|increment-9 durability correction candidate|"
            r"pending the product owner'?s? final",
            re.I)
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if text is None:
                continue
            for hit in scan(text, not_accepted):
                bad("acceptance-parity", "still presents an accepted phase as unaccepted/in-progress: %s" % hit, rel)
        if not [f for f in failures if f[0] == "acceptance-parity"]:
            ok("no current surface still presents an accepted phase as unaccepted or in progress (accepted: %s)"
               % ", ".join(apks))

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
    #
    # NOT_STARTED is not the only way a phase is unauthorized. A phase that is not in the roadmap AT ALL is
    # unauthorized by construction, and the rule used to miss it entirely: with every recorded phase started,
    # "Execute the authorized Phase 8 end-to-end" passed unchallenged. That gap only became reachable when
    # D26 authorized Phase 7 and left nothing NOT_STARTED, which is how the negative suite found it.
    recorded = set(str(k) for k in (state.get("phases") or {}))
    unauth = sorted(n for n, ph in (state.get("phases") or {}).items()
                    if isinstance(ph, dict) and ph.get("status") == "NOT_STARTED")
    named = re.compile(r"(?:execute|implement)\w*\s+(?:the\s+)?(?:authorized\s+)?phase[\s-]*([0-9]+[A-Za-z]?)\b", re.I)
    for i, aa in enumerate(allowed):
        for m in named.finditer(aa):
            n = m.group(1)
            if n not in recorded:
                bad("authorization-model-parity",
                    "allowed_actions[%d] permits executing phase %s, which is not in the roadmap at all: %s"
                    % (i, n, " ".join(aa.split())[:150]), STATE)

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

    # ---- 8c. THE LATEST ACCEPTED DECISION, AND PHASES THAT ARE STILL "AUTHORIZED" AFTER BEING CLOSED ---------
    #
    # THE FALSE PASS THIS CLOSES, found independently after the Phase-7 acceptance was already recorded and every
    # gate was green. D27 accepted and closed Phase 7 and T0064 recorded it. phases["7"].status said
    # ACCEPTED_AND_CLOSED, the receipt agreed, the next action agreed, and rule 9 above compared the receipt with
    # the phases map and passed. But three OTHER current fields still described the previous world:
    #
    #   latest_accepted_po_decision  = "D26"          (D27 exists and is accepted)
    #   blockers[0]                  = "... Phase 7 is AUTHORIZED for planning and execution."
    #   prohibited_actions[3]        = "Implementing work beyond the authorized Phase 7 scope."
    #
    # Every generated block prints latest_accepted_po_decision, so the START-HERE, Handoff and Contract blocks
    # each said "D27/T0064 closed Phase 7" and "Latest accepted PO decision: D26" in the same breath. Nothing
    # caught it, because no rule compared the pointer with the register, and no rule read the blocker and action
    # prose for a phase that is no longer authorized.
    #
    # These three rules are general. They are not written for phase 7 and must not be special-cased to it: any
    # phase recorded ACCEPTED_AND_CLOSED or FINAL_CLOSED is subject to them, in every current field listed below.
    register = load_json("governance/decision-register.json") or {}
    decisions = register.get("decisions") or []
    accepted_ids = [str(d.get("id")) for d in decisions if isinstance(d, dict) and d.get("accepted")]
    pointer = str(state.get("latest_accepted_po_decision") or "").strip()

    def dnum(i):
        m = re.match(r"^D(\d+)$", i or "")
        return int(m.group(1)) if m else -1

    if not accepted_ids:
        bad("latest-decision-pointer", "the decision register records no accepted decision, so the pointer "
            "latest_accepted_po_decision cannot be checked against anything", "governance/decision-register.json")
    elif not pointer:
        bad("latest-decision-pointer", "latest_accepted_po_decision is empty while the register records "
            "accepted decisions up to %s" % max(accepted_ids, key=dnum), STATE)
    else:
        newest = max(accepted_ids, key=dnum)
        if pointer != newest:
            bad("latest-decision-pointer",
                "latest_accepted_po_decision is %r but the newest ACCEPTED decision in the register is %r -- "
                "every generated current-state block prints this pointer, so the blocks would announce the new "
                "decision and cite the old one in the same paragraph" % (pointer, newest), STATE)
        else:
            ok("latest_accepted_po_decision (%s) is the newest accepted decision in the register" % pointer)

    # A closed phase must not still be described as authorized or in progress by any CURRENT field.
    CLOSED_STATUSES = ("ACCEPTED_AND_CLOSED", "FINAL_CLOSED")
    closed_phases = [ph for ph, body in (state.get("phases") or {}).items()
                     if isinstance(body, dict) and str(body.get("status", "")).strip() in CLOSED_STATUSES]
    # WHAT COUNTS AS THE DEFECT. Not the mere co-occurrence of "phase 7" and "authorized" -- the correct
    # wording after closure necessarily says both, e.g. "Re-executing Phase 7 ... none is open or authorized".
    # The defect is the phase being PREDICATED as authorized or in progress, which has a small set of shapes:
    #
    #     "Phase 7 is AUTHORIZED for planning and execution"      <- the blocker that was found stale
    #     "beyond the authorized Phase 7 scope"                   <- the prohibited action that was found stale
    #     "Phase-7 ... IN_PROGRESS"
    #
    # Matching the claim rather than the vocabulary is what keeps this rule general and keeps it from punishing
    # the sentences that state the closure correctly.
    def still_open_claim(text, ph):
        if not isinstance(text, str):
            return None
        P = r"\bphase[\s\-_]*%s\b" % re.escape(ph)
        CLAIMS = (
            re.compile(P + r"[^.;!?]{0,60}?\b(?:is|are|remains?|stays?)\s+(?:still\s+|currently\s+)?authoriz", re.I),
            re.compile(r"\bauthoriz(?:ed|es)\b[^.;!?]{0,40}?" + P, re.I),
            re.compile(P + r"[^.;!?]{0,60}?\b(?:is|are|remains?)\s+(?:still\s+|currently\s+)?in[\s_-]?progress\b", re.I),
            re.compile(r"\bin[\s_-]?progress\b[^.;!?]{0,40}?" + P, re.I),
        )
        HISTORICAL = re.compile(r"\b(?:was|were|previously|formerly|historical(?:ly)?|until|before|no longer)\b", re.I)
        # A NEGATED authorization is the CORRECT wording after closure, not the defect: "no further Phase-7
        # action is authorized", "merging is not authorized", "unauthorized". Without this the rule fired on the
        # very sentences that state the closure properly, which would have taught the next person to delete them.
        NEGATED = re.compile(r"\b(?:not|no|never|neither|nor)\b[^.;!?]{0,40}?authoriz|\bunauthoriz", re.I)
        for _off, clause in split_clauses(text):   # split_clauses yields (offset, clause)
            # historical framing is allowed when the clause says so itself
            if HISTORICAL.search(clause) or NEGATED.search(clause):
                continue
            for rx in CLAIMS:
                if rx.search(clause):
                    return clause.strip()
        return None

    CURRENT_FIELDS = ("blockers", "allowed_actions", "prohibited_actions", "next_authorized_action",
                      "current_activity", "current_focus", "current_maturity")
    for ph in sorted(closed_phases):
        hits = []
        for field in CURRENT_FIELDS:
            val = state.get(field)
            for text in ([val] if isinstance(val, str) else (val if isinstance(val, list) else [])):
                claim = still_open_claim(text, ph)
                if claim:
                    hits.append("%s: %s" % (field, claim[:160]))
        if hits:
            bad("closed-phase-still-authorized",
                "phase %s is recorded %s, but current state still describes it as authorized or in progress -- %s"
                % (ph, (state.get("phases") or {}).get(ph, {}).get("status"), " | ".join(hits[:3])), STATE)
        else:
            ok("no current field describes closed phase %s as still authorized or in progress" % ph)

    # And the generated blocks must not carry a stale decision id, since they are what a reader actually sees.
    if pointer:
        for rel in DOC_SURFACES:
            text = load_surface(rel)
            if not text:
                continue
            for m in re.finditer(r"Latest accepted PO decision:\s*`?(D\d+)`?", text):
                if m.group(1) != pointer:
                    bad("generated-block-stale-decision",
                        "%s prints 'Latest accepted PO decision: %s' while the state records %s -- the block is "
                        "generated, so this means it was not re-rendered after the decision changed"
                        % (rel, m.group(1), pointer), rel)
                    break
            else:
                ok("%s prints the current latest accepted decision (%s)" % (rel, pointer))

    # ---- 8d. CURRENT STATE THAT CITES A SUPERSEDED DECISION, OR CALLS A CLOSED PROJECT ACTIVE --------------
    #
    # Two more survivors of the same closure, found after the previous correction was already green. Both are
    # the identical failure shape as 8c -- a current field frozen at the moment it was written -- so they are
    # checked the same way and stay general.
    #
    #   prohibited_actions[0]   "... none is authorized by D26."      D26 is no longer the latest decision, so
    #                                                                 a reader checking D26 learns nothing about
    #                                                                 what is authorized NOW. The prohibition is
    #                                                                 right; its authority reference was stale.
    #   service_routing_state   "... under active development and     every numbered development phase is
    #                            controlled testing"                  closed, so this describes a project that
    #                                                                 no longer exists in that state.
    #
    # RULE 1: a current field may not settle a question of authorization by citing a decision that has been
    # superseded. Citing an older decision as HISTORY is fine and says so; citing it as the current authority
    # is what goes stale silently.
    SUPERSEDED = [i for i in accepted_ids if i != pointer] if pointer else []
    AUTHORITY = re.compile(r"\b(?:authoriz(?:ed|es|ation)|permitted|allowed|granted)\b[^.;!?]{0,60}?\b(D\d+)\b"
                           r"|\b(D\d+)\b[^.;!?]{0,60}?\b(?:authoriz(?:ed|es)|permits?|allows?|grants?)\b", re.I)
    for field in ("blockers", "allowed_actions", "prohibited_actions", "next_authorized_action"):
        val = state.get(field)
        stale_cites = []
        for text in ([val] if isinstance(val, str) else (val if isinstance(val, list) else [])):
            if not isinstance(text, str):
                continue
            for _off, clause in split_clauses(text):
                if HIST_RX.search(clause):
                    continue
                for m in AUTHORITY.finditer(clause):
                    cited = m.group(1) or m.group(2)
                    if cited in SUPERSEDED:
                        stale_cites.append("%s cites %s: %s" % (field, cited, clause.strip()[:120]))
        if stale_cites:
            bad("superseded-decision-as-current-authority",
                "a current field settles what is authorized by citing a SUPERSEDED decision (latest accepted is "
                "%s) -- %s" % (pointer, " | ".join(stale_cites[:2])), STATE)
    ok("no current field cites a superseded decision as the authority for what is authorized now")

    # RULE 2: with every numbered development phase closed, no current field may describe the project as still
    # under development. PRE-LIVE and controlled testing remain true and are not touched by this.
    dev_phases = {ph: (body or {}).get("status", "") for ph, body in (state.get("phases") or {}).items()
                  if isinstance(body, dict)}
    open_phases = [ph for ph, st in dev_phases.items()
                   if str(st).strip() not in ("ACCEPTED_AND_CLOSED", "FINAL_CLOSED")]
    if dev_phases and not open_phases:
        ACTIVE_DEV = re.compile(r"\bunder\s+active\s+development\b|\bactively\s+(?:developed|under\s+development)\b"
                                r"|\bdevelopment\s+is\s+(?:ongoing|continuing|in\s+progress)\b", re.I)
        hits = []
        # current_state_facts is a DICT, and the first version of this loop skipped every non-str/list value --
        # so it missed a third copy of the same stale sentence sitting in the most authoritative current block
        # there is. Nested string values are walked, not skipped.
        def _strings(node):
            if isinstance(node, str):
                yield node
            elif isinstance(node, dict):
                for v in node.values():
                    for t in _strings(v):
                        yield t
            elif isinstance(node, list):
                for v in node:
                    for t in _strings(v):
                        yield t

        for field, val in state.items():
            if field in ("phases", "roadmap_exhaustion"):
                continue
            for text in _strings(val):
                if not isinstance(text, str):
                    continue
                for _off, clause in split_clauses(text):
                    if HIST_RX.search(clause):
                        continue
                    if ACTIVE_DEV.search(clause):
                        hits.append("%s: %s" % (field, clause.strip()[:130]))
        if hits:
            bad("closed-project-described-as-active-development",
                "every numbered phase is closed (%s), but current state still describes the project as under "
                "active development -- %s" % (", ".join(sorted(dev_phases)), " | ".join(hits[:2])), STATE)
        else:
            ok("no current field describes the project as under active development now that every phase is closed")

    # ---- 8e. A MERGED PHASE THAT THE REPOSITORY STILL CALLS UNMERGED, AND MACHINE STATE LEFT BEHIND -------
    #
    # After PR #15 merged, five current surfaces still described the world before it: the Phase-7 plan and the
    # Final Report said "PR #15 remains OPEN and UNMERGED", the report's machine marker said
    # MERGE_STATE: PR_OPEN_UNMERGED, current_state_facts.phase7_status said IN_PROGRESS, and
    # operational_status said ACTIVE_DEVELOPMENT after the roadmap had completed. Prose rules could not see the
    # machine markers, and the merged-PR rule only looked at OPEN pull requests -- so a merged PR's own body
    # went unchecked precisely when it mattered.
    #
    # All of it is one shape: a merge or a closure happened and the records that describe it did not move.
    merged_phases = {}
    for ph, body in (state.get("phases") or {}).items():
        if isinstance(body, dict) and body.get("merged"):
            merged_phases[ph] = body

    # (a) a committed plan or report for a MERGED phase may not present its PR as open, unmerged or unmergeable.
    UNMERGED_CLAIM = re.compile(r"\b(?:remains?|is|are)\s+(?:\*\*)?open\s+and\s+unmerged\b"
                                r"|\bopen\s+and\s+unmerged\b"
                                r"|\bPR_OPEN_UNMERGED\b"
                                r"|\bDO\s+NOT\s+MERGE\b", re.I)
    if merged_phases:
        # DOC_SURFACES is a Phase-3-era list of five files and does not include later phases' plans or reports,
        # which is why this rule saw nothing while the Phase-7 plan and report both said "OPEN and UNMERGED".
        # Every committed plan and final report is in scope, discovered rather than enumerated.
        import glob as _glob
        scan_rels = sorted(set(
            [r for r in DOC_SURFACES if r.endswith(".md")] +
            [os.path.relpath(f, ROOT).replace(os.sep, "/")
             for pat in ("docs/architecture/*-Plan.md", "docs/reports/*-Final-Report.md",
                         "docs/acceptance/*.md")
             for f in _glob.glob(os.path.join(ROOT, pat))]))
        for rel in scan_rels:
            text = load_surface(rel)
            if not text:
                continue
            hits = []
            for para, _off in paragraphs(text):
                if HIST_RX.search(para):
                    continue
                m = UNMERGED_CLAIM.search(para)
                if m:
                    hits.append(" ".join(para.split())[:140])
            if hits:
                bad("merged-phase-still-called-unmerged",
                    "%s still presents a merged phase's pull request as open/unmerged or says DO NOT MERGE "
                    "(merged phases: %s) -- %s" % (rel, ", ".join(sorted(merged_phases)), hits[0]), rel)
        ok("no committed surface presents a merged phase's pull request as open, unmerged or do-not-merge")

    # (b) machine-readable state for an accepted/merged phase may not still read IN_PROGRESS, and a completed
    #     roadmap may not still read ACTIVE_DEVELOPMENT. These are values, not prose, so no prose rule sees them.
    facts = state.get("current_state_facts") or {}
    for ph, body in sorted((state.get("phases") or {}).items()):
        if not isinstance(body, dict):
            continue
        st = str(body.get("status", "")).strip()
        if st not in ("ACCEPTED_AND_CLOSED", "FINAL_CLOSED"):
            continue
        key = "phase%s_status" % ph.lower()
        val = str(facts.get(key, "")).strip()
        if val and ("IN_PROGRESS" in val.upper() or "AUTHORIZED" == val.upper()):
            bad("machine-state-behind-phase-status",
                "current_state_facts.%s is %r while phases[%s].status is %s -- a machine-readable value left "
                "behind by the closure" % (key, val, ph, st), STATE)
    ok("no machine-readable phase status contradicts a closed phase")

    roadmap = state.get("roadmap_exhaustion") or {}
    if str(roadmap.get("numbered_development_roadmap", "")).strip().upper() == "COMPLETE":
        opstat = str(facts.get("operational_status", ""))
        if "ACTIVE_DEVELOPMENT" in opstat.upper():
            bad("completed-roadmap-still-active-development",
                "the numbered roadmap is recorded COMPLETE but current_state_facts.operational_status is %r"
                % opstat, STATE)
        else:
            ok("the completed roadmap is not contradicted by an ACTIVE_DEVELOPMENT machine status")

    # (c) a receipt that denies contacting an environment IN THE SAME BREATH as reporting what it observed
    #     there. Scoped to ONE field: the first version read the whole receipt, so Production's entirely
    #     legitimate "not contacted and not mutated" collided with the appliance field saying it was contacted,
    #     and it failed a truthful receipt. The detectable defect is narrower and real -- a single claim that
    #     denies contact while carrying observations (a uptime, an HTTP code, row counts, a release path, a
    #     service state) that can only have come from contacting the thing.
    latest_rid = state.get("latest_transition_id")
    if latest_rid:
        rpath2 = os.path.join(TRANSITIONS_DIR, "%s.json" % latest_rid)
        try:
            rec = json.load(io.open(rpath2, encoding="utf-8"))
        except Exception:  # noqa: BLE001
            rec = None
        DENIES = re.compile(r"\bnot\s+contacted\b|\bnever\s+contacted\b|\bno\s+contact\b", re.I)
        OBSERVED = re.compile(r"\buptime\b|\bhttp\s*\d{3}\b|\b404\b|\bstill\s+\d+\b|\brow counts?\b|"
                              r"\breleases?/[\w.-]+\b|\bis-active\b|\bsystemctl\b|\bcounts? (?:are|were)\b|"
                              r"\bobserved\b|\bmeasured\b", re.I)

        def _vals(node, path=""):
            if isinstance(node, str):
                yield path, node
            elif isinstance(node, dict):
                for k, v in node.items():
                    for x in _vals(v, path + "." + str(k)):
                        yield x
            elif isinstance(node, list):
                for i, v in enumerate(node):
                    for x in _vals(v, path + "[%d]" % i):
                        yield x

        if isinstance(rec, dict):
            clash = []
            for where, val in _vals(rec):
                # Per clause, and historical/correcting clauses are exempt. A receipt that says the earlier
                # wording "not contacted" was wrong is CORRECTING the defect, not committing it, and an early
                # version of this rule failed exactly that honest sentence.
                # Judged per FIELD, with the denial judged per clause. Per-clause on BOTH halves was too
                # narrow: "not contacted for this change; uptime 14h32m and the endpoint still 404" puts the
                # denial and the observation in adjacent clauses and slipped straight through. What matters is
                # a live denial anywhere in a claim that also reports observations.
                if not OBSERVED.search(val):
                    continue
                for _o, clause in split_clauses(val):
                    if HIST_RX.search(clause):
                        continue          # correcting or quoting the old wording is not committing the defect
                    if DENIES.search(clause):
                        clash.append("%s: %s" % (where, " ".join(clause.split())[:140]))
                        break
            if clash:
                bad("runtime-contact-evidence-contradicts-itself",
                    "%s denies contacting an environment in the same claim that reports what was observed "
                    "there -- %s" % (latest_rid, clash[0]), rpath2)
            else:
                ok("%s never denies contact in the same claim that reports observations" % latest_rid)

    # ---- 8f. AN ENVIRONMENT CALLED DARK WHILE ITS OWN RECORD SAYS IAM-v2 IS ENABLED THERE ------------------
    #
    # The DEVELOPMENT trial enabled IAM-v2 on one appliance while Production stayed dark. The global claims --
    # "iam_v2 is dark", "no deployed service is routed to iam_v2", "legacy IAM is the configured baseline" --
    # were then true of one environment and false of the other, and nothing noticed, because every rule read
    # them as one answer about one world.
    #
    # The rule is general: if the state records a per-environment block saying IAM-v2 is ENABLED or WIRED in
    # some environment, then a global dark/not-routed claim must say which environment it speaks for.
    envs = state.get("environment_scoped_iam_state") or {}
    enabled_envs = []
    for name, body in envs.items():
        if not isinstance(body, dict):
            continue
        blob = json.dumps(body).lower()
        if ("enabled" in blob or "installed" in blob) and "not cut over" not in blob:
            enabled_envs.append(name)
    if enabled_envs:
        SCOPED = re.compile(r"\bproduction scope\b|\bproduction only\b|\bon production\b|\bproduction:", re.I)
        DARKISH = re.compile(r"\biam[_ ]?v2 is dark\b|\bremains dark\b|\bno service is routed to iam_v2\b|"
                             r"\bno deployed service is routed\b", re.I)
        for field in ("service_routing_state", "database_schema_state", "blockers"):
            val = state.get(field)
            for text in ([val] if isinstance(val, str) else (val if isinstance(val, list) else [])):
                if not isinstance(text, str) or not DARKISH.search(text):
                    continue
                if not SCOPED.search(text):
                    bad("unscoped-dark-claim-vs-enabled-environment",
                        "%s makes a global IAM-v2 dark/not-routed claim while %s is recorded as having IAM-v2 "
                        "enabled or wired; the claim must name the environment it speaks for"
                        % (field, ", ".join(enabled_envs)), STATE)
        ok("global IAM-v2 dark claims name their environment, given %s is enabled" % ", ".join(enabled_envs))

    # ---- 8g. A DECISION ATTRIBUTED TO A TRANSITION THAT DOES NOT CARRY IT --------------------------------
    #
    # "D31/T0067" survived several rounds of review. Both ids are real, both appear in the register and the
    # ledger, and every existing rule was satisfied -- so nothing noticed that T0067 records D30's wiring while
    # D31 is carried by T0068. A pairing can be wrong while both halves are valid, and that is exactly the
    # shape that reads as authoritative and is not.
    #
    # General: wherever current state writes "Dnn/Tmmmm" or "Dnn (transition Tmmmm)", the named transition must
    # actually record that decision.
    PAIR = re.compile(r"\b(D\d+)\s*(?:/|\s*\(transition\s*)\s*(T\d{4})\b")
    seen = {}
    for _f, txt in [(k, v) for k, v in state.items() if isinstance(v, str)] + \
                   [(k, x) for k, v in state.items() if isinstance(v, list)
                    for x in v if isinstance(x, str)]:
        for dec, tr in PAIR.findall(txt):
            seen.setdefault((dec, tr), _f)
    bad_pairs = []
    for (dec, tr), where in sorted(seen.items()):
        rp = os.path.join(TRANSITIONS_DIR, "%s.json" % tr)
        if not os.path.exists(rp):
            bad_pairs.append("%s cites %s/%s but %s.json does not exist" % (where, dec, tr, tr))
            continue
        try:
            rec = json.load(io.open(rp, encoding="utf-8"))
        except Exception:  # noqa: BLE001
            continue
        if str(rec.get("decision", "")).strip() != dec:
            bad_pairs.append("%s cites %s/%s but %s records decision %r"
                             % (where, dec, tr, tr, rec.get("decision")))
    if bad_pairs:
        bad("decision-transition-attribution",
            "current state pairs a decision with a transition that does not carry it -- %s"
            % " | ".join(bad_pairs[:3]), STATE)
    else:
        ok("every decision/transition pair in current state is carried by the transition it names")

    # ---- 8h. AN EVENT ATTRIBUTED TO THE WRONG (BUT VALID) DECISION/TRANSITION PAIR -----------------------
    #
    # "Phase 7 merged under D31/T0068" passed rule 8g, because D31 really is carried by T0068. The PAIR was
    # valid and the ATTRIBUTION was false: that pair belongs to the post-roadmap DEVELOPMENT trial, not to the
    # merge, which was D28/T0065. Proving a pair exists is not proving it describes the event it is attached to.
    #
    # So the lifecycle events carry STRUCTURED fields (phase_lifecycle_authority) and prose is checked against
    # them. This generalises to any event in that map, and needs no sentence-specific pattern.
    auth = state.get("phase_lifecycle_authority") or {}
    EVENT_WORDS = {"merged": r"merged", "accepted_and_closed": r"accepted[ _]and[ _]closed|accepted and closed"}
    claims = []
    for ph, events in auth.items():
        if not isinstance(events, dict):
            continue
        for ev, rec in events.items():
            if not isinstance(rec, dict) or ev not in EVENT_WORDS:
                continue
            want = "%s/%s" % (rec.get("decision"), rec.get("transition"))
            # TEMPERED GAP. A plain [^.;!?]{0,40}? let "...ACCEPTED_AND_CLOSED and Phase 7 is MERGED to
            # master (D28/T0065)" match the ACCEPTED event against the MERGE pair -- the rule inventing a
            # contradiction inside a correct sentence. No other event keyword may sit between the event
            # word and the pair it is judged against.
            gap = r"(?:(?!merged|accepted)[^.;!?])"
            rx = re.compile(r"\b(?:phase[\s\-]*%s\b%s{0,60}?)?(?:%s)\b%s{0,40}?\b(D\d+/T\d{4})\b"
                            % (re.escape(str(ph)), gap, EVENT_WORDS[ev], gap), re.I)
            for _f, txt in [(k, v) for k, v in state.items() if isinstance(v, str)] + \
                           [(k, x) for k, v in state.items() if isinstance(v, list)
                            for x in v if isinstance(x, str)]:
                for m in rx.finditer(txt):
                    got = m.group(1)
                    if got != want and str(ph) in m.group(0).lower().replace("phase", "").replace(" ", ""):
                        claims.append("%s attributes phase %s '%s' to %s, but the lifecycle record says %s"
                                      % (_f, ph, ev, got, want))
    if claims:
        bad("event-attribution-vs-lifecycle-record",
            "current state attributes a lifecycle event to a decision/transition that did not carry it -- %s"
            % " | ".join(sorted(set(claims))[:3]), STATE)
    else:
        ok("every lifecycle-event attribution in current state matches phase_lifecycle_authority")

    # ---- 8i. THE AUTHORITATIVE STATE FILE CONTRADICTING ITS OWN LIFECYCLE FIELDS -------------------------
    #
    # merged-phase-still-called-unmerged (rule 8c) scans markdown DOC surfaces. It never opened
    # governance/project-state.json -- so the state file was free to record Phase 7 as merged in
    # phase_lifecycle_authority while /phases/7/maturity still narrated "PR #15, which remains OPEN and
    # UNMERGED", and every validator stayed green. The docs were policed; the authority was not.
    #
    # These three rules read the STATE's own string values and judge them against the STATE's own structured
    # fields. They are driven entirely by those fields, so a new phase or a new authorized activity is covered
    # the moment it is recorded -- no sentence patterns to maintain.
    def _state_strings():
        """Every string value in the state, with a path, so a violation names where it lives."""
        out = []

        def walk(o, path):
            if isinstance(o, dict):
                for k, v in o.items():
                    walk(v, path + "/" + str(k))
            elif isinstance(o, list):
                for i, v in enumerate(o):
                    walk(v, "%s/[%d]" % (path, i))
            elif isinstance(o, str):
                out.append((path, o))
        walk(state, "")
        return out

    STATE_STRINGS = _state_strings()
    _auth = state.get("phase_lifecycle_authority") or {}

    # (a) a phase the lifecycle records as MERGED must not be narrated as open/unmerged anywhere in the state.
    OPEN_CLAIM = re.compile(r"\bOPEN\s+AND\s+UNMERGED\b|\bremains?\s+(?:OPEN|UNMERGED)\b"
                            r"|\bstill\s+(?:open|unmerged)\b|\bnot\s+(?:yet\s+)?merged\b", re.I)
    HIST = re.compile(r"\bwas\s+(?:previously|formerly)\b|\bat\s+the\s+time\b|\bhistorical(?:ly)?\b"
                      r"|\bsince\s+MERGED\b|\bno\s+longer\b", re.I)
    open_hits = []
    for ph, events in _auth.items():
        if not isinstance(events, dict) or "merged" not in events:
            continue
        for path, txt in STATE_STRINGS:
            for sent in re.split(r"(?<=[.;!?])\s+", txt):
                if not OPEN_CLAIM.search(sent) or HIST.search(sent):
                    continue
                # only if this sentence is actually about that phase or its PR
                if re.search(r"\bphase[\s\-]*%s\b" % re.escape(str(ph)), sent, re.I) or \
                   re.search(r"\bPR\s*#?\d+\b", sent, re.I):
                    open_hits.append("%s: %s" % (path, " ".join(sent.split())[:120]))
    if open_hits:
        bad("state-narrates-merged-phase-as-unmerged",
            "project-state records the phase as MERGED in phase_lifecycle_authority but still narrates it as "
            "open/unmerged -- %s" % " | ".join(sorted(set(open_hits))[:3]), STATE)
    else:
        ok("no state prose narrates a lifecycle-merged phase as open or unmerged")

    # (b) an activity the state records as AUTHORIZED must not also be called unauthorized.
    #     Driven by authorized_activities[*].{name,authorization,status}; nothing is hard-coded.
    #     STATIVE forms only. "the trial is not authorized" is the contradiction; "authorizing the trial did
    #     NOT authorize any Production transition" is a correct and important scope limit, and an earlier
    #     version of this rule failed the file over exactly that sentence. The difference is grammatical: the
    #     transitive use takes an object ("authorize any/the/a X"), so it is excluded explicitly.
    UNAUTH = re.compile(r"\b(?:is|was|are|were|remains?|stays?)\s+not\s+(?:yet\s+)?authoriz\w*"
                        r"|\bnot\s+(?:yet\s+)?been\s+authoriz\w*"
                        r"|\b(?:is|was|remains?)\s+unauthoriz\w*"
                        r"|\bawaiting\s+authoriz\w*"
                        r"|\bnot\s+authorized\s+yet\b", re.I)
    AUTH_TRANSITIVE = re.compile(r"\bauthoriz\w*\s+(?:any|a|an|the|no|further|additional)\b", re.I)
    acts = state.get("authorized_activities") or []
    unauth_hits = []
    for act in acts:
        if not isinstance(act, dict):
            continue
        name = str(act.get("name", "")).strip()
        if not name or str(act.get("status", "")).upper() in ("PROPOSED", "NOT_AUTHORIZED"):
            continue
        # Match on the activity's distinctive keywords rather than the whole phrase, so a paraphrase in
        # prose ("that trial") is still caught when it names the same subject.
        keys = [w for w in re.findall(r"[A-Za-z][A-Za-z\-]{3,}", name) if w.lower() not in
                ("the", "and", "with", "from", "that", "this", "appliance")]
        if not keys:
            continue
        for path, txt in STATE_STRINGS:
            for sent in re.split(r"(?<=[.;!?])\s+", txt):
                if not UNAUTH.search(sent) or HIST.search(sent):
                    continue
                # "did not authorize any X" is a scope limit, not a claim about this activity's own status.
                if AUTH_TRANSITIVE.search(sent) and not re.search(
                        r"\b(?:is|was|remains?)\s+not\s+(?:yet\s+)?authoriz", sent, re.I):
                    continue
                if sum(1 for k in keys if re.search(r"\b%s\b" % re.escape(k), sent, re.I)) >= 2:
                    unauth_hits.append("%s (%s): %s" % (path, name, " ".join(sent.split())[:110]))
    if acts:
        if unauth_hits:
            bad("authorized-activity-described-as-unauthorized",
                "project-state records the activity as authorized but prose still calls it unauthorized -- %s"
                % " | ".join(sorted(set(unauth_hits))[:3]), STATE)
        else:
            ok("no authorized activity is described anywhere in state as not yet authorized")

    # (c) work the state records as COMPLETE must not still be listed as a pending next action.
    #     Driven by completed_activities[*] -- a plain list of short labels.
    done = [x for x in (state.get("completed_activities") or []) if isinstance(x, str)]
    nxt = state.get("next_authorized_action")
    stale_next = []
    if done and isinstance(nxt, str):
        # Only the part of the sentence that is actually a pending instruction: text after an explicit
        # REMAINING/ still-to-do marker is pending; text after COMPLETE is a record, not a next action.
        pending = re.split(r"\bAlready\s+COMPLETE\b|\bCOMPLETE\s+and\s+not\b", nxt, 1, flags=re.I)[0]
        for label in done:
            toks = [w for w in re.findall(r"[A-Za-z][A-Za-z\-]{3,}", label)
                    if w.lower() not in ("the", "and", "with", "from", "that", "this")]
            if len(toks) >= 2 and all(re.search(r"\b%s\b" % re.escape(t), pending, re.I) for t in toks):
                stale_next.append(label)
    if done and isinstance(nxt, str):
        if stale_next:
            bad("completed-work-still-listed-as-next-action",
                "next_authorized_action still asks for work recorded in completed_activities -- %s"
                % "; ".join(sorted(set(stale_next))[:3]), STATE)
        else:
            ok("next_authorized_action asks for no work already recorded as complete")

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
        # Phase 5 is listed for the reason the rule exists: the map is what makes a phase's PLAN readable to
        # this check, and a closed phase whose plan is unreadable is exactly where the contradiction that
        # created rule 10 lived. Adding the entry at closure keeps the coverage honest rather than nominal.
        "5": "docs/architecture/StayConnect-IAM-Phase5-Plan.md",
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

    # ---- 10b. PRE-LIVE OPERATIONAL PARITY -----------------------------------------------------------------------
    #
    # THE FALSE PASS THIS CLOSES. Every rule above this one is about a surface claiming MORE IMPLEMENTATION
    # PROGRESS than the record supports. This one is the mirror image, and nothing caught it: seven rendered
    # surfaces called the legacy public-schema path "the SOLE PRODUCTION authority", one said its rows "grow
    # with normal guest traffic", and two called the deployed voucher system "live and untouched" -- while the
    # recorded fact is that StayConnect has NOT yet entered real hotel guest/staff operation (D24/T0056).
    #
    # Each of those sentences described the CONFIGURATION correctly. What made them wrong was the operational
    # implication, and an implication is exactly what a keyword gate cannot judge on its own -- so, like the
    # accepted-phase rule, this one is RELATIVE TO THE RECORDED FACT and does nothing at all once real
    # operation begins and `real_hotel_guest_operation_started` becomes true.
    #
    # Historical records are not surfaces: docs/acceptance/ and docs/evidence/ say what was true when they
    # were written and are deliberately not scanned, and a paragraph carrying a history marker is excused
    # exactly as everywhere else.
    if facts.get("real_hotel_guest_operation_started") is False:
        live_claim = re.compile(
            r"sole\s+production\s+authority|"
            # The same claim wearing a different noun. The first pass caught only "production" and this form
            # survived in three places, which is why the pattern now names the CLAIM rather than one spelling
            # of it: any "sole ... authority" over authentication asserts an operational role that a pre-live
            # system does not have.
            r"sole\s+authentication\s+authority|"
            r"sole\s+(?:production\s+)?auth\w*\s+authority|"
            r"\bthe\s+live\s+authority\b|"
            r"normal\s+guest\s+traffic|"
            r"\blive\s+and\s+untouched\b|"
            r"(?:currently|presently|today)\s+serv\w*\s+(?:real\s+)?(?:hotel\s+)?guests|"
            r"break\s+all\s+guest\s+authentication",
            re.I)
        prelive_surfaces = sorted(set(
            list(DOC_SURFACES) + list(PHASE_PLANS.values()) + [
                "exports/chatgpt/stayconnectenterprise/00-START-HERE.md",
                "exports/chatgpt/stayconnectenterprise/PROJECT-INSTRUCTIONS.md",
                "exports/chatgpt/stayconnectenterprise/MANIFEST.md",
                "docs/architecture/Phase3-Privilege-Matrix.md",
                "governance/artifact-registry.json",
            ]))
        hits = []
        for rel in prelive_surfaces:
            text = load_surface(rel)
            if text is None:
                continue
            # A JSON document has no blank lines, so paragraph labelling would excuse the whole file. Scan it
            # value by value, exactly as the merged-PR rule learned to.
            if rel.endswith(".json"):
                units = [v for v in json_strings(load_json(rel))]
            else:
                units = [p for p, _ in paragraphs(text)]
            for para in units:
                # CLAUSE-LEVEL, not unit-level. A long JSON value such as current_maturity carries dozens of
                # independent claims in one string; excusing the whole value because a corrected clause
                # appears somewhere in it lets every stale clause before that point ride along. The unit is
                # therefore split into sentence-sized clauses and each is judged against markers found in
                # ITS OWN neighbourhood -- the clause itself plus a bounded window either side, so a
                # correction can still cover the sentence it is attached to and no further.
                for cl_start, clause in split_clauses(para):
                    m = live_claim.search(clause)
                    if not m:
                        continue
                    lo = max(0, cl_start - 160)
                    hi = min(len(para), cl_start + len(clause) + 160)
                    neighbourhood = para[lo:hi]
                    if HISTORY_MARKERS.search(neighbourhood):
                        continue
                    # A sentence that quotes the wrong wording IN ORDER TO CORRECT IT is not the wrong
                    # wording.
                    if re.search(r"corrected|PRE-LIVE|pre-live|used to say|earlier wording|D24",
                                 neighbourhood):
                        continue
                    hits.append((rel, " ".join(clause[max(0, m.start() - 70):m.end() + 70].split())))
        for rel, hit in hits:
            bad("pre-live-operational-parity",
                "the recorded facts say real hotel guest operation has NOT started, but this presents the "
                "system as operationally live: %s" % hit[:180], rel)
        if not hits:
            ok("no current surface presents the pre-live system as already serving real hotel guests")

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
        # NOT \bMERGED\b: the recorded values are of the form MERGED_AND_CLOSED, and underscore is a
        # word character, so that pattern matched nothing and this whole branch was dead. A negative
        # lookbehind for a letter keeps UNMERGED out while letting MERGED_AND_CLOSED in.
        if not (m and isinstance(val, str) and re.search(r"(?<![A-Za-z])MERGED", val, re.I)):
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
                "docs/reports/StayConnect-IAM-Phase5-Final-Report.md",
                "docs/acceptance/StayConnect-IAM-Phase5-Live-Dark-Acceptance.md",
                "docs/evidence/StayConnect-IAM-Phase5-Evidence.md",
            ]))
        for num, owner in sorted(merged_prs.items()):
            n = re.escape(str(num))
            # Either the PR is named and then called open, or the "do not merge" instruction is still standing
            # within reach of its number. Bounded so it cannot bridge a sentence end into an unrelated clause.
            #
            # The number must not match INSIDE a longer one. `#?6\b` happily matches the "6" of "#16", so a
            # true statement about PR #16 -- which IS open, and correctly says so -- was reported as a stale
            # claim about merged PR #6. A false positive is not harmless here: the gate fails on accurate text,
            # and the cheapest way to make it pass again is to make the text vaguer, which is the opposite of
            # what this file exists to enforce. The lookbehind also excludes "#", so "#16" cannot be read as
            # "#" followed by a "6".
            nb = r"(?<![\w#])#?" + n + r"\b"
            stale = re.compile(
                r"(?:pull\s+request|\bPR\b)[^.;\n]{0,60}?" + nb + r"(?:[^.;\n]{0,80}?)"
                r"(?:is|remains|stays|left|be)\s+(?:[^.;\n]{0,30}?)"
                r"(?:open\s+and\s+unmerged|unmerged|not\s+merged|open,\s*unmerged)|"
                + nb + r"[^.;\n]{0,60}?\bDO\s+NOT\s+MERGE\b|"
                r"\bDO\s+NOT\s+MERGE\b[^.;\n]{0,60}?" + nb,
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

    # ---- N. THE LIVE APPLIANCE: counters, kernel state, deployed head, and why Room Auth is refused ---------
    #
    # These rules exist because of one delivery. T0105-T0107 recorded, in order: that the 72-hour local-first
    # contract was fixed so an in-flight or failed resync no longer disables a fresh roster; that Room Auth is
    # refused ONLY because the last good roster has aged out; that all three Sessions had ended and the kernel
    # held nothing; and that the deployed head had moved. Every one of those facts contradicted a sentence that
    # was still sitting, unlabelled and in the present tense, in the authoritative state file - "sessions=3 (2
    # active)", an older deployed head, "no Full Resync was triggered" beside a resync that had been attempted
    # and failed. None of it contains a forbidden word. It is only wrong about the appliance.
    #
    # So the live appliance is recorded as DATA and the prose is held to it.
    live_rules = []

    def live_int(name):
        v = facts.get(name)
        return v if isinstance(v, int) else None

    active = live_int("live_sessions_active")
    if active is not None:
        # "N active sessions" anywhere on a current surface must agree. The count is what an operator reads to
        # decide whether a guest is online right now.
        rx = re.compile(r"sessions?\s*=\s*\d+\s*\((\d+)\s+active\)|(\d+)\s+active\s+sessions?\b", re.I)
        live_rules.append(("live-session-count", rx, lambda m: int(m.group(1) or m.group(2)) != active,
                           "the recorded facts say %d Session(s) are active" % active))
    if facts.get("nft_managed_authorizations") == 0 and facts.get("tc_managed_classes") == 0:
        # A surface claiming installed kernel state while the recorded state is empty sends an operator to look
        # for an authorization that is not there.
        rx = re.compile(r"\b(\d+)\s+nft\s+element|\bnft\s+set\s+holds\s+(?!no\b)|phase3_auth_ipv4\s+holds\s+(?!no\b)",
                        re.I)
        live_rules.append(("live-kernel-state", rx, lambda m: not (m.group(1) or "").startswith("0"),
                           "the recorded facts say the kernel holds no authorization and no managed class"))
    if facts.get("room_auth_blocked") and \
            facts.get("room_auth_blocked_reason") == "LAST_GOOD_ROSTER_OLDER_THAN_MAX_AUTH_CACHE_AGE":
        # THE ONE THAT MATTERS MOST. While the blocker is the cache age, no current surface may say the feed
        # state or an in-progress resync is what refuses a guest: that is the defect 0060 fixed, and repeating
        # it would send an operator to reconnect a PMS that is stopped on purpose.
        rx = re.compile(
            r"(?:room\s+(?:auth\w*|sign-?in|login)|guests?)[^.;\n]{0,80}?"
            r"(?:refused|blocked|denied|cannot\s+(?:sign|log)\s*in)[^.;\n]{0,80}?"
            r"(?:resync[_\s-]?in[_\s-]?progress|resync\s+is\s+in\s+flight|resync_started_at|"
            r"because[^.;\n]{0,40}?disconnect)", re.I)
        live_rules.append(("room-auth-blocker", rx, lambda m: True,
                           "the recorded blocker is the roster's AGE, not the feed state or a resync in flight"))

    deployed = str(facts.get("deployed_head_on_appliance") or "").strip()
    if len(deployed) >= 8:
        # A superseded deployed head presented as what the appliance runs. Any 8+ hex prefix that is claimed as
        # deployed and is neither the recorded head nor the recorded repository master is a contradiction.
        master = str(facts.get("repository_master_head") or "").strip()
        # PRECISION MATTERS MORE THAN REACH HERE. A blunt "any sha near the word runs" rule fires on CI run
        # identifiers (decimal is a subset of hex) and on every historical acceptance candidate, and a rule
        # that cries wolf gets switched off. Three conditions together:
        #   * the clause is ABOUT this appliance — an appliance, the PRE-LIVE box, or its deployed marker;
        #   * the token looks like a git object rather than a run id, so it must contain a hex LETTER;
        #   * and it is not a statement about what a workflow ran against.
        appliance_rx = re.compile(r"\bappliance\b|\bPRE-?LIVE\b|172\.21\.60\.25|\bDEPLOYED_SHA\b|\bon \.25\b", re.I)
        ci_rx = re.compile(r"\bCI\b|workflow|run \d{6,}|exact-head runs?|dispatch", re.I)
        rx = re.compile(r"(?:runs|running|deployed(?:\s+(?:at|on|from))?|appliance\s+(?:is\s+)?at)"
                        r"[^.;\n]{0,40}?\b([0-9a-f]{8,40})\b", re.I)

        def wrong_head(m):
            h = m.group(1).lower()
            clause = m.string
            if not appliance_rx.search(clause) or ci_rx.search(clause):
                return False
            if not re.search(r"[a-f]", h):
                return False  # a decimal identifier: a CI run, not a commit
            return not (deployed.startswith(h) or h.startswith(deployed[:8]) or
                        (master and (master.startswith(h) or h.startswith(master[:8]))))
        live_rules.append(("deployed-head", rx, wrong_head,
                           "the recorded deployed head is %s (repository master %s)" % (deployed[:8], master[:8])))

    for rel in DOC_SURFACES:
        text = load_surface(rel)
        if text is None:
            continue
        units = json_strings(load_json(rel)) if rel.endswith(".json") else [p for p, _ in paragraphs(text)]
        for unit in units:
            for off, clause in split_clauses(unit):
                # DELIBERATELY THE EXPLICIT MARKERS ONLY, not the looser verb list the other rules use. That
                # list counts "until" and "before" as history, and "no guest can sign in UNTIL the resync
                # completes" is a claim about the present that happens to contain the word — the exact sentence
                # this rule exists to refuse. Labelling a live-state claim as history has to be deliberate here:
                # write HISTORICAL, or "as at <date>", and it is excused.
                if HISTORY_MARKERS.search(clause):
                    continue
                for rule, rx, wrong, why in live_rules:
                    m = rx.search(clause)
                    if m and wrong(m):
                        bad(rule, "%s, but this says: %s" % (why, " ".join(clause.split())[:180]), rel)
    if not [f for f in failures if f[0] in
            ("live-session-count", "live-kernel-state", "room-auth-blocker", "deployed-head")]:
        ok("no current surface contradicts the recorded live appliance state "
           "(sessions, kernel, deployed head, Room-Auth blocker)")

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
