#!/usr/bin/env python3
"""STANDING RECORDS MUST BE TRUE EVERYWHERE, INCLUDING IN THE FILES NOBODY VALIDATES.

WHY THIS EXISTS, SEPARATELY FROM validate-current-state-parity.py. That validator is the right tool and it
works: 51 checks, history excused per-paragraph, a fact recorded as data and prose diffed against it. But its
reach is five markdown/json files in DOC_SURFACES and seven in STATIC_SURFACES, and it reads markdown and
JSON only. A sweep of the tree against the standing records found 44 current-state contradictions outside
that reach, and the two most dangerous were SHELL SCRIPTS -- which no validator read at all:

    deploy/scripts/phase7-appliance-m4.sh   APPL="${PHASE7_APPLIANCE:-<retired development appliance>}"
    deploy/scripts/phase7-final-reboot.sh   APPL="${PHASE7_APPLIANCE:-<retired development appliance>}"

The retired development reference appliance is RETIRED and must not be contacted. The second issues a real reboot. Neither was a sentence
anybody would have caught by reading prose, and the largest cluster -- seventeen surfaces describing Central
telemetry that migration 0045 dropped -- survived because the removal delivery touched no architecture doc.

So this validator has the opposite shape: FEW rules, WHOLE tree, every text file. Each rule is keyed to a
fact in governance/project-state.json, so the rule and the truth cannot drift apart -- and where a fact did
not exist, the delivery that added this validator added the fact.

THE FOUR RULES

  1. RETIRED HOSTS may not appear as an operational default or target. Keyed to
     current_state_facts.retired_hosts. Scans executable and config surfaces for a retired address used as a
     DEFAULT (`${VAR:-<addr>}`), which is the form that sends an unsuspecting run at the forbidden machine.
     Prose mentions are NOT flagged: historical evidence must be preserved, and the standing record says so
     explicitly.

  2. ONBOARDING MILESTONES that are recorded as done may not be described as awaited. Keyed to the
     appliance_* booleans, which were all recorded true while three fields in the SAME FILE said the
     appliance was "awaiting onboarding" and "awaiting enrollment/claim/signed assignment". Those booleans
     existed and no rule read them.

  3. REMOVED CENTRAL TELEMETRY may not be presented as current, and never as a repair. Keyed to
     current_state_facts.central_scope == LICENSING_ONLY. The "repair" half matters most: the standing record
     forbids proposing reconnection as a fix, and a runbook step saying "restore quorum" or "telemetry
     ingest resumes" is exactly that instruction, addressed to whoever is on call.

  4. A STATED MASTER HEAD must be a head. Keyed to current_state_facts.master_head_at_delivery_base, and
     satisfied by that base or by the live `git rev-parse master`. Nothing recorded a master head before, so
     the parity validator read an empty field and dropped master from its acceptable set -- which is how
     CONTINUATION.md announced PR #120 as the head, 52 merges late, through four green gates.

WHAT IT DELIBERATELY DOES NOT DO. It does not judge prose about the past. Every rule either targets a
machine-readable form (a shell default, a stated sha) or requires a historical marker in the same paragraph,
because the standing record on retirement is explicit: "Retiring a target does not falsify its records."
"""
import json
import os
import re
import subprocess
import sys

# The findings quote real source lines, which contain real arrows and dashes. Without this the validator
# dies on a UnicodeEncodeError under the Windows console codepage while reporting a genuine defect.
if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
STATE = os.path.join(ROOT, "governance", "project-state.json")

# Trees whose contents are, by decision, records of what was true then.
HISTORICAL_TREES = (
    "docs/acceptance/",
    "docs/evidence/",
    "docs/reports/",
    "docs/spikes/",
    "governance/transitions/",
    # A REGISTER OF DATED DECISIONS IS A HISTORY. D37's title necessarily quotes the obsolete
    # "awaiting enrollment" state it was the decision to retire; a decision cannot be recorded without
    # naming what it changed.
    "governance/decision-register.json",
    # Dated security/delivery evidence, same class as docs/evidence.
    "docs/TERMINAL_DELIVERY_SECURITY_EVIDENCE.md",
    "evidence/",
    "exports/chatgpt/phase-evidence/",
)

SKIP_TREES = (
    ".git/", "node_modules/", ".cleanroom/", "iam_v2_scratch/accepted/",
    # BUILD OUTPUT IS NOT A SURFACE ANYBODY READS. The Next.js bundles under .deploy/ and .next/ contain
    # every string the UI can render, minified, so they match any vocabulary rule by construction.
    "cloud-admin/.deploy/", "hotel-admin/.deploy/", "web-admin/.deploy/",
    ".next/", "dist/", "build/",
)

# THE RULES AND THEIR TESTS BOTH HAVE TO CONTAIN THE DEFECTS.
#
# This validator quotes the defects it catches, in its own docstring, because a rule whose reason is not
# written down is a rule the next person deletes. Its negative tests go further: they PLANT each real
# defect, run the validator, and require a refusal -- so the plant strings live in the test file. Scanning
# either would report their own examples.
SELF = (
    "tools/validate-standing-records.py",
    "tools/tests/standing_records_negative.py",
)

TEXT_EXT = {
    ".md", ".json", ".sh", ".py", ".go", ".ts", ".tsx", ".js", ".yml", ".yaml",
    ".sql", ".conf", ".env", ".txt", ".service", ".timer",
}

# A paragraph carrying one of these is talking about the past on purpose.
HISTORY_MARKERS = (
    "historical", "historic", "was true", "at that time", "superseded", "no longer",
    "used to", "formerly", "previously", "retired", "removed", "deleted", "dropped",
    "correction of record", "do not read as current", "predating",
)

# CURRENT-STATE SURFACES: the files that describe THIS system as it is now. The onboarding, telemetry and
# master-head rules apply here only.
#
# WHY NOT EVERYWHERE. The first run of the onboarding rule over the whole tree flagged
# `slog.Warn("awaiting assignment: edged running without a tenant/site (generic appliance)")` and a
# control-plane comment reading "Everything AWAITING ASSIGNMENT, not merely awaiting claim". Both are
# CORRECT: they are product code handling an appliance that genuinely has no assignment yet, and the
# vocabulary is the product's, not a claim about 172.21.60.25. A rule that cannot tell a description of this
# appliance from a code path for any appliance would have to be silenced to be usable, and a silenced rule
# is worse than none.
#
# The retired-host rule is deliberately NOT scoped this way: its target is a machine-readable default in a
# shell script, which is precisely the surface no validator was reading.
def is_current_state_surface(path):
    if any(path.startswith(t) for t in HISTORICAL_TREES):
        return False
    if path in ("CONTINUATION.md", "README.md", "CLAUDE.md"):
        return True
    if path.startswith("docs/") and path.endswith(".md"):
        return True
    if path.startswith("governance/") and path.endswith(".json"):
        return True
    if path.startswith("exports/") and path.endswith(".md"):
        return True
    return False


_failures = []


def bad(kind, msg, where=""):
    _failures.append(kind)
    print("  FAIL [%s] %s%s" % (kind, msg, ("\n           " + where) if where else ""))


def ok(msg):
    print("  ok: %s" % msg)


def rel(path):
    return os.path.relpath(path, ROOT).replace(os.sep, "/")


def text_files():
    for dirpath, dirnames, filenames in os.walk(ROOT):
        d = rel(dirpath)
        d = "" if d == "." else d + "/"
        if any(s in "/" + d for s in SKIP_TREES):
            dirnames[:] = []
            continue
        for fn in filenames:
            if os.path.splitext(fn)[1].lower() not in TEXT_EXT:
                continue
            if (d + fn) in SELF:
                continue
            p = os.path.join(dirpath, fn)
            r = rel(p)
            try:
                with open(p, encoding="utf-8", errors="replace") as fh:
                    yield r, fh.read()
            except OSError:
                continue


# One compiled alternation instead of twenty substring scans per line. These rules run over every line of
# every text file in the repository, so this is the difference between seconds and minutes.
HISTORY_RE = re.compile("|".join(re.escape(m) for m in HISTORY_MARKERS), re.I)

# A PROHIBITION IS NOT THE THING IT PROHIBITS. The telemetry rule's first run flagged CLAUDE.md's own
# sentence -- "Do not propose reconnecting telemetry as a repair" -- and the note in project-state.json that
# records the same decision. A rule that fires on the statement of itself is unusable, and deleting that
# statement to satisfy it would remove the only place the reason is written down.
PROHIBITION_RE = re.compile(
    r"\b(do not|don't|never|must not|cannot|may not|no longer|refus\w*|forbid\w*"
    r"|not a (?:defect|fault|repair))\b", re.I)

# A QUOTED PHRASE IS A CITATION. Correcting a stale sentence means quoting it -- It said 'awaiting
# onboarding' until 2026-09-22 IS the correction, not a relapse -- so a match immediately preceded by a
# quote character is read as a citation rather than an assertion.
QUOTE_CHARS = "\"'‘’“”`"


# MARKDOWN EMPHASIS BREAKS WORD-BOUNDARY MATCHING, and the surfaces these rules read are mostly markdown.
# "reconnecting telemetry is **not** a repair" does not match /not a repair/ because of the asterisks, so a
# sentence stating the prohibition was itself reported as a violation of it. Emphasis is presentation; the
# rules judge words.
EMPHASIS_RE = re.compile(r"[*_`~]+")


def historical(path, line_text):
    if any(path.startswith(t) for t in HISTORICAL_TREES):
        return True
    plain = EMPHASIS_RE.sub("", line_text)
    if HISTORY_RE.search(plain):
        return True
    return PROHIBITION_RE.search(plain) is not None


def quoted_citation(line_text, start):
    """True when the phrase matched at `start` is immediately preceded by a quote character."""
    i = start - 1
    while i >= 0 and line_text[i] == " ":
        i -= 1
    return i >= 0 and line_text[i] in QUOTE_CHARS


# ---- 1. RETIRED HOSTS -------------------------------------------------------------------------------------
def check_retired_hosts(facts, files):
    retired = facts.get("retired_hosts") or {}
    if not retired:
        bad("retired-hosts-unrecorded",
            "current_state_facts.retired_hosts is absent, so the retirement of a host cannot be checked "
            "against anything. Record it there.")
        return
    hits = 0
    # An entry is keyed by NAME and carries its address as octets, so the literal address of a retired host is
    # not written in the tree (T0194). An entry keyed by the address itself is still accepted.
    def address_of(key, entry):
        octets = (entry or {}).get("address_octets")
        return ".".join(str(int(o)) for o in octets) if octets else key
    for key in retired:
        addr = address_of(key, retired[key])
        # The form that actually causes harm: a retired address as a DEFAULT, so a run with no environment
        # variable goes there. Matches ${VAR:-addr}, ${VAR:=addr} and a bare `VAR=addr` assignment.
        default_form = re.compile(
            r"(\$\{[A-Za-z_][A-Za-z0-9_]*:[-=]\s*%s\s*\})|(^\s*[A-Za-z_][A-Za-z0-9_]*=\"?%s\"?\s*$)"
            % (re.escape(addr), re.escape(addr)), re.M)
        for path, body in files:
            if addr not in body:
                continue
            for m in default_form.finditer(body):
                line_no = body.count("\n", 0, m.start()) + 1
                line = body.splitlines()[line_no - 1]
                if historical(path, line):
                    continue
                hits += 1
                bad("retired-host-default",
                    "%s is RETIRED (%s) and is used here as a DEFAULT target, so a run that does not set "
                    "the variable contacts it" % (key, retired[key].get("status", "RETIRED")),
                    "%s:%d  %s" % (path, line_no, line.strip()))
    if not hits:
        ok("no retired host is used as a default target (%s)" % ", ".join(sorted(retired)))


# ---- 2. ONBOARDING MILESTONES -----------------------------------------------------------------------------
AWAITED = {
    "appliance_enrolled": ("enrollment", r"await\w*\s+(?:\w+\s+){0,3}enroll|not yet enrolled|pending enroll"),
    "appliance_claimed": ("claim", r"await\w*\s+(?:\w+\s+){0,3}claim|not yet claimed|pending claim"),
    "appliance_assigned": ("signed assignment",
                           r"await\w*\s+(?:\w+\s+){0,3}assignment|not yet assigned|pending assignment"),
    "appliance_licensed": ("licence", r"await\w*\s+(?:\w+\s+){0,3}licen[sc]|not yet licen[sc]ed"),
}
ONBOARDING_GENERIC = r"await\w*\s+onboarding"


# THE AUTHORITATIVE STORE, and the checkpoint that is read as one.
#
# Rule 2's unique value is that it reads the appliance_* booleans, which live here and which nothing read.
# Scoping it wider immediately produced false positives that are not defects: docs/DEPLOYMENT_CLOUD.md
# documents that a failed registration "reports only 'awaiting enrollment'", and
# docs/user-guide/hotel-admin-reference.md lists "Awaiting enrollment" as a lifecycle STATE. Both describe
# product vocabulary for any appliance. Prose about this appliance across docs/ is already the parity
# validator's job.
ONBOARDING_SURFACES = ("governance/project-state.json", "CONTINUATION.md")


def check_onboarding_vocabulary(facts, files):
    done = [k for k in AWAITED if facts.get(k) is True]
    if not done:
        ok("no onboarding milestone is recorded as complete, so nothing to check")
        return
    patterns = [(AWAITED[k][0], re.compile(AWAITED[k][1], re.I)) for k in done]
    if all(facts.get(k) is True for k in AWAITED):
        patterns.append(("onboarding", re.compile(ONBOARDING_GENERIC, re.I)))
    hits = 0
    for path, body in files:
        if path not in ONBOARDING_SURFACES:
            continue
        if "await" not in body.lower() and "not yet" not in body.lower():
            continue
        lines = body.splitlines()
        for i, line in enumerate(lines, 1):
            if historical(path, line):
                continue
            for milestone, pat in patterns:
                m = pat.search(line)
                if m and not quoted_citation(line, m.start()):
                    hits += 1
                    bad("onboarding-milestone-parity",
                        "%s is recorded COMPLETE in current_state_facts, but this says it is awaited"
                        % milestone,
                        "%s:%d  %s" % (path, i, line.strip()[:160]))
    if not hits:
        ok("no current surface says an already-completed onboarding milestone is awaited (%d checked)"
           % len(done))


# ---- 3. CENTRAL SCOPE -------------------------------------------------------------------------------------
# Naming the removed things is fine in a history; presenting them as something to RESTORE is not.
TELEMETRY_REPAIR = re.compile(
    r"(restore|resume|reconnect|re-enable|reenable|bring back)\w*\s+(?:\w+\s+){0,4}"
    r"(telemetry|fleet_telemetry|nats\s+quorum|quorum)"
    r"|telemetry\s+ingest\s+resumes"
    r"|restore\s+quorum",
    re.I)


def check_central_scope(facts, files):
    if facts.get("central_scope") != "LICENSING_ONLY":
        ok("central_scope is not LICENSING_ONLY; the telemetry-removal rule does not apply")
        return
    hits = 0
    for path, body in files:
        if not is_current_state_surface(path):
            continue
        low = body.lower()
        if "telemetry" not in low and "quorum" not in low:
            continue
        lines = body.splitlines()
        for i, line in enumerate(lines, 1):
            if historical(path, line):
                continue
            if TELEMETRY_REPAIR.search(line):
                hits += 1
                bad("central-telemetry-as-repair",
                    "Central is LICENSING_ONLY by standing decision and its telemetry was deliberately "
                    "removed; this reads as an instruction to restore it",
                    "%s:%d  %s" % (path, i, line.strip()[:160]))
    if not hits:
        ok("no current surface presents the removed Central telemetry as something to restore")


# ---- 4. STATED MASTER HEAD --------------------------------------------------------------------------------
# THE WORDING HAS TO MATCH THE WORDING THAT SHIPPED. The first version of this pattern required the sha to
# follow the verb directly, and the defect it exists to catch read "Master is at merge commit `0582eb78`
# (PR #120)" -- two words in between, so the rule did not fire on its own worked example. Up to four words
# are now allowed, which covers "merge commit", "commit" and "the merge commit".
STATED_HEAD = re.compile(
    r"master\s+(?:is\s+at|head\s+is|is\s+now\s+at|sits\s+at)\s+(?:\w+\s+){0,4}`?([0-9a-f]{7,40})`?"
    r"|current\s+master\s+head\s+(?:is\s+)?(?:\w+\s+){0,4}`?([0-9a-f]{7,40})`?",
    re.I)


def live_master():
    try:
        out = subprocess.run(["git", "rev-parse", "master"], cwd=ROOT, capture_output=True, text=True,
                             timeout=20)
        return out.stdout.strip() if out.returncode == 0 else ""
    except Exception:
        return ""


def check_stated_master_head(facts, files):
    base = str(facts.get("master_head_at_delivery_base") or "").strip()
    if not base:
        bad("master-head-unrecorded",
            "current_state_facts.master_head_at_delivery_base is absent, so no stated master head can be "
            "checked against anything. Record it there.")
        return
    acceptable = {base}
    lm = live_master()
    if lm:
        acceptable.add(lm)
    hits = 0
    for path, body in files:
        if not is_current_state_surface(path):
            continue
        if "master" not in body.lower():
            continue
        lines = body.splitlines()
        for i, line in enumerate(lines, 1):
            if historical(path, line):
                continue
            m = STATED_HEAD.search(line)
            if not m:
                continue
            sha = (m.group(1) or m.group(2) or "").lower()
            if not sha:
                continue
            if any(a.lower().startswith(sha) or sha.startswith(a.lower()) for a in acceptable):
                continue
            hits += 1
            bad("stated-master-head",
                "states master is at %s, which is neither the recorded delivery base (%s) nor live master "
                "(%s)" % (sha, base[:8], (lm or "unknown")[:8]),
                "%s:%d  %s" % (path, i, line.strip()[:160]))
    if not hits:
        ok("every stated master head matches the recorded base or live master")


def main():
    with open(STATE, encoding="utf-8") as fh:
        facts = json.load(fh).get("current_state_facts") or {}
    files = list(text_files())
    print("== standing records, over %d text files ==" % len(files))
    check_retired_hosts(facts, files)
    check_onboarding_vocabulary(facts, files)
    check_central_scope(facts, files)
    check_stated_master_head(facts, files)
    print("=" * 50)
    if _failures:
        print("STANDING_RECORDS = FAIL (%d)" % len(_failures))
        return 1
    print("STANDING_RECORDS = PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
