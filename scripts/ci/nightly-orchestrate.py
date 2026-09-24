#!/usr/bin/env python3
"""THE NIGHTLY AUTHORITATIVE VALIDATION AND PROTECTED MERGE, driven against the GitHub API.

This is the thin caller. Every judgement it makes comes from tools/nightly_delivery.py, which is exercised
adversarially by tools/tests/nightly_delivery/run_negative.py; nothing here decides anything on its own.

THE SEQUENCE, and what each step refuses:

  1. schedule sanity   the run really started near 03:10 Africa/Cairo. The platform owns the timezone
                       (one `cron:` with `timezone: Africa/Cairo`); this verifies it honoured it.
  2. one candidate     exactly one open, non-draft, non-held pull request to master. Zero is a quiet no-op;
                       two is a hard refusal.
  3. re-run            the EXISTING pull_request run of each gate for that exact head. Only a pull_request
                       run's checks satisfy the ruleset -- a workflow_dispatch run's do not, measured:
                       `HTTP 405 ... 4 of 4 required status checks are expected` with four green checks from
                       the pinned app already on the head. A re-run keeps the event and the head sha and
                       arrives as attempt 2+, which is where the gates actually execute.
  4. wait              until all four complete, or the budget runs out. Partial completion is refusal.
  5. classify          the same run ids that were re-run, on the expected sha, on a LATER attempt, green.
  6. re-read           the PR head again. If a commit landed during the run, the pass is stale and waits for
                       the next night -- that is the single most important refusal in this file.
  7. merge             `merge` method only, with the sha PINNED, so GitHub itself refuses if anything moved
                       between the decision and the call.

EXIT CODES MATTER, because a red nightly run is the signal the next agent session looks for first:
  0  merged, or a clean no-op (nothing to do tonight), or a stale pass that correctly waits
  1  something needs a human or an agent: a gate failed, candidates were ambiguous, a precondition blocked
  2  the orchestrator itself could not run (no token, API unusable)
"""
import datetime as dt
import importlib.util
import json
import os
import sys
import time
import urllib.error
import urllib.request

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
_spec = importlib.util.spec_from_file_location("nd", os.path.join(ROOT, "tools", "nightly_delivery.py"))
nd = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(nd)

UTC = dt.timezone.utc
REPO = os.environ.get("GITHUB_REPOSITORY", "aibrahiiim1/StayConnectEnterprise")
TOKEN = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN") or ""
DRY_RUN = os.environ.get("NIGHTLY_DRY_RUN", "").lower() in ("1", "true", "yes")
IGNORE_CHECK = os.environ.get("NIGHTLY_IGNORE_SCHEDULE_CHECK", "").lower() in ("1", "true", "yes")
EVENT_NAME = os.environ.get("NIGHTLY_EVENT_NAME", "schedule")
GATE_BUDGET_S = int(os.environ.get("NIGHTLY_GATE_BUDGET_SECONDS", "5400"))   # 90 minutes
POLL_S = int(os.environ.get("NIGHTLY_POLL_SECONDS", "30"))

_lines = []


def say(msg=""):
    print(msg, flush=True)
    _lines.append(msg)


def summary():
    path = os.environ.get("GITHUB_STEP_SUMMARY")
    if not path:
        return
    try:
        with open(path, "a", encoding="utf-8") as fh:
            fh.write("\n".join(_lines) + "\n")
    except OSError:
        pass


def api(path, method="GET", body=None, accept="application/vnd.github+json"):
    url = path if path.startswith("http") else "https://api.github.com/repos/%s%s" % (REPO, path)
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers={
        "Authorization": "Bearer " + TOKEN, "Accept": accept,
        "User-Agent": "stayconnect-nightly", "Content-Type": "application/json"})
    with urllib.request.urlopen(req) as r:
        raw = r.read()
        return json.loads(raw) if raw else {}


def graphql(query, variables=None):
    req = urllib.request.Request("https://api.github.com/graphql",
                                 data=json.dumps({"query": query,
                                                  "variables": variables or {}}).encode(),
                                 headers={"Authorization": "Bearer " + TOKEN,
                                          "User-Agent": "stayconnect-nightly",
                                          "Content-Type": "application/json"})
    with urllib.request.urlopen(req) as r:
        return json.loads(r.read())


_tested_head = ""


def finish(code, verdict, extra=None):
    say()
    say("=" * 78)
    say("NIGHTLY_DELIVERY_VERDICT = %s" % verdict)
    # THE HEAD THIS NIGHT ACTUALLY JUDGED, printed so the session-start check can tell a failure that is still
    # waiting from one a later commit has already superseded. Without it, every red night would look unresolved
    # forever and every future session would be told to repair something already repaired.
    say("NIGHTLY_TESTED_HEAD = %s" % (_tested_head or "none"))
    if extra:
        say(extra)
    out = os.environ.get("GITHUB_OUTPUT")
    if out:
        try:
            with open(out, "a", encoding="utf-8") as fh:
                fh.write("verdict=%s\n" % verdict)
        except OSError:
            pass
    summary()
    sys.exit(code)


def main():
    if not TOKEN:
        say("NO TOKEN: the orchestrator cannot read the repository, so it refuses rather than guessing.")
        finish(2, "ORCHESTRATOR_UNUSABLE")

    say("## Nightly authoritative validation")
    say()

    # ---- 1. the schedule sanity check ---------------------------------------------------------------
    now = dt.datetime.now(tz=UTC)
    win = nd.schedule_sanity(now, EVENT_NAME)
    say("**schedule** `%s` — %s" % (win.code, win.reason))
    for k in ("event", "now_utc", "now_local", "utc_offset_hours", "drift_minutes"):
        if k in (win.detail or {}):
            say("  - %s: `%s`" % (k, win.detail[k]))
    if not win.proceed:
        if IGNORE_CHECK:
            say()
            say("> **schedule check overridden** by an explicit operator request. This changes only WHEN the "
                "validation runs. Every gate still runs fresh on the exact head, and every fail-closed rule "
                "below still applies.")
        else:
            finish(1, win.code,
                   "Nothing ran. The declared `timezone: Africa/Cairo` appears not to have been honoured, "
                   "which would otherwise mean a nightly merge quietly running at the wrong hour.")

    # ---- 2. exactly one candidate -------------------------------------------------------------------
    pulls_raw = api("/pulls?state=open&per_page=100")
    pulls = [{"number": p["number"], "draft": p.get("draft", False),
              "base_ref": (p.get("base") or {}).get("ref"),
              "head_ref": (p.get("head") or {}).get("ref"),
              "head_sha": (p.get("head") or {}).get("sha"),
              "labels": [l.get("name") for l in (p.get("labels") or [])],
              "state": p.get("state")} for p in pulls_raw]
    cand = nd.select_candidate(pulls)
    say()
    say("**candidate** `%s` — %s" % (cand.code, cand.reason))
    if not cand.proceed:
        # No candidate is a correct no-op. Ambiguity needs an operator, so it is red.
        finish(0 if cand.code == "NO_CANDIDATE" else 1, cand.code)

    global _tested_head
    pr = cand.detail["chosen"]
    number, head_ref, expected_sha = pr["number"], pr["head_ref"], pr["head_sha"]
    _tested_head = expected_sha

    say("  - pull request: **#%s** (`%s`)" % (number, head_ref))
    say("  - head under test: `%s`" % expected_sha)
    say("  - freshness is proved by the ATTEMPT NUMBER: each re-run must come back on a later attempt "
        "than the sentinel it replaces")

    # ---- 3. re-run the pull_request run of each gate, at that exact head --------------------------
    #
    # A re-run, not a dispatch. Only a pull_request run's checks satisfy the ruleset; a dispatched run's do
    # not, which GitHub states plainly when asked to merge on them. The run to re-run is the one whose
    # head_sha is the head we decided on -- the sentinel attempt the daytime push created.
    say()
    say("**re-run** the pull_request run of each gate at `%s`" % expected_sha[:12])
    requested = {}
    for wf in nd.REQUIRED_GATES:
        run = find_pr_run(wf, expected_sha)
        if run is None:
            say("  - `%s`: NO pull_request run exists for this head. The sentinel attempt should have created "
                "one on push; without it there is no countable context to re-run." % wf)
            continue
        try:
            api("/actions/runs/%d/rerun" % run["id"], "POST", {})
            requested[wf] = {"id": run["id"], "attempt": run.get("run_attempt") or 1}
            say("  - `%s`: re-ran run %s (was attempt %s)" % (wf, run["id"], run.get("run_attempt")))
        except urllib.error.HTTPError as e:
            body = e.read().decode("utf-8", "replace")[:160]
            # A run already on a fresh attempt cannot be re-run again while it is active; that is not a
            # failure to report as one, so it is recorded and judged by the classifier like any other.
            say("  - `%s`: re-run refused (HTTP %s %s)" % (wf, e.code, body))
    if len(requested) != len(nd.REQUIRED_GATES):
        say()
        say("Not every gate could be re-run, so the four required contexts cannot all be freshly earned "
            "tonight. Refusing rather than merging on whatever is there.")
        finish(1, "RERUN_INCOMPLETE")

    # ---- 4. wait for all four ----------------------------------------------------------------------
    say()
    say("**waiting** up to %d minutes for all four re-runs to complete" % (GATE_BUDGET_S // 60))
    deadline = time.time() + GATE_BUDGET_S
    runs, last = [], ""
    while True:
        runs = read_runs(requested)
        done = sum(1 for r in runs
                   if r["status"] == "completed"
                   and int(r.get("run_attempt") or 0) > int(requested[r["workflow_file"]]["attempt"]))
        state = "%d/%d complete on a new attempt" % (done, len(nd.REQUIRED_GATES))
        if state != last:
            say("  - %s" % state)
            last = state
        if done >= len(nd.REQUIRED_GATES):
            break
        if time.time() > deadline:
            say("  - BUDGET EXHAUSTED with %s" % state)
            break
        time.sleep(POLL_S)

    # ---- 5. all four, fresh, this sha, green -------------------------------------------------------
    gates = nd.classify_rerun_runs(expected_sha, requested, runs)
    say()
    say("**gates** `%s` — %s" % (gates.code, gates.reason))
    for wf, info in sorted((gates.detail or {}).get("per_gate", {}).items()):
        say("  - `%s`: %s" % (wf, json.dumps(info)))

    # ---- 6. re-read the head; a newer commit invalidates the pass ----------------------------------
    fresh = api("/pulls/%d" % number)
    pr_now = {"number": number, "head_sha": (fresh.get("head") or {}).get("sha"),
              "mergeable": fresh.get("mergeable"), "mergeable_state": fresh.get("mergeable_state"),
              "state": fresh.get("state")}
    unresolved = unresolved_threads(number)
    say()
    say("**re-read** head is now `%s`; mergeable_state `%s`; unresolved review threads: %s"
        % (str(pr_now["head_sha"])[:12], pr_now["mergeable_state"], unresolved))

    decision = nd.merge_precondition(gates, expected_sha, pr_now, unresolved)
    say()
    say("**merge decision** `%s` — %s" % (decision.code, decision.reason))

    if not decision.proceed:
        # A stale pass is not a defect: the branch simply moved on, and tomorrow judges the new head.
        if decision.code in ("HEAD_MOVED",):
            finish(0, "STALE_PASS_WAITING_FOR_NEXT_NIGHT")
        finish(1, decision.code)

    if DRY_RUN:
        finish(0, "WOULD_MERGE_DRY_RUN",
               "Dry run: every gate ran fresh and every precondition held, and no merge was performed.")

    # ---- 7. the protected merge, with the sha pinned ----------------------------------------------
    try:
        res = api("/pulls/%d/merge" % number, "PUT",
                  {"merge_method": "merge", "sha": expected_sha,
                   "commit_title": "Merge PR #%d: nightly authoritative validation passed at %s"
                                   % (number, expected_sha[:12]),
                   "commit_message": "All four authoritative gates freshly re-ran against %s and passed. "
                                     "The required contexts were earned by pull_request runs on a later "
                                     "attempt than the daytime sentinel.\n\nThis merge introduces no content "
                                     "of its own." % expected_sha})
    except urllib.error.HTTPError as e:
        say("  - MERGE REFUSED by GitHub: HTTP %s %s"
            % (e.code, e.read().decode("utf-8", "replace")[:300]))
        finish(1, "MERGE_REFUSED_BY_GITHUB")

    merge_sha = res.get("sha")
    say()
    say("**merged** `%s` — %s" % (merge_sha, res.get("message")))

    master = api("/commits/master")
    say("  - master is now `%s`" % master.get("sha"))
    if master.get("sha") != merge_sha:
        finish(1, "MASTER_NOT_AT_MERGE_COMMIT")
    finish(0, "MERGED")


def find_pr_run(wf, head_sha):
    """The pull_request run of `wf` for exactly this head, newest first.

    There is normally exactly one: the sentinel attempt the daytime push created. If a branch was force-pushed
    to the same sha, or the pull request was reopened, there may be several -- the newest is the one whose
    checks are current for the commit, and re-running it is what updates them.
    """
    try:
        data = api("/actions/workflows/%s/runs?event=pull_request&per_page=40" % wf)
    except urllib.error.HTTPError:
        return None
    for r in data.get("workflow_runs", []):
        if str(r.get("head_sha") or "") == str(head_sha):
            return r
    return None


def read_runs(requested):
    """The requested runs, read back by id and shaped for classify_rerun_runs."""
    out = []
    for wf, want in (requested or {}).items():
        try:
            r = api("/actions/runs/%d" % int(want["id"]))
        except urllib.error.HTTPError:
            continue
        out.append({"workflow_file": os.path.basename(r.get("path") or ""),
                    "id": r.get("id"), "head_sha": r.get("head_sha"), "event": r.get("event"),
                    "status": r.get("status"), "conclusion": r.get("conclusion"),
                    "run_attempt": r.get("run_attempt")})
    return out


def unresolved_threads(number):
    q = ("query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){pullRequest(number:$n){"
         "reviewThreads(first:100){nodes{isResolved}}}}}")
    owner, name = REPO.split("/", 1)
    try:
        d = graphql(q, {"o": owner, "r": name, "n": number})
        nodes = d["data"]["repository"]["pullRequest"]["reviewThreads"]["nodes"]
        return sum(1 for t in nodes if not t.get("isResolved"))
    except Exception:                                            # noqa: BLE001
        return None      # unreadable -> merge_precondition refuses, which is the point


if __name__ == "__main__":
    main()
