#!/usr/bin/env python3
"""THE NIGHTLY AUTHORITATIVE VALIDATION AND PROTECTED MERGE, driven against the GitHub API.

This is the thin caller. Every judgement it makes comes from tools/nightly_delivery.py, which is exercised
adversarially by tools/tests/nightly_delivery/run_negative.py; nothing here decides anything on its own.

THE SEQUENCE, and what each step refuses:

  1. schedule window   03:10 Africa/Cairo, computed from the tz database, not from a frozen UTC offset.
  2. one candidate     exactly one open, non-draft, non-held pull request to master. Zero is a quiet no-op;
                       two is a hard refusal.
  3. dispatch          workflow_dispatch at the candidate's branch, carrying the expected sha and tonight's
                       correlation id. The run's head_sha is therefore the PR head, which is the only way the
                       pinned required contexts can be satisfied for that commit.
  4. wait              until all four complete, or the budget runs out. Partial completion is refusal.
  5. classify          all four must be fresh (tonight's correlation id), on the expected sha, and green.
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
IGNORE_WINDOW = os.environ.get("NIGHTLY_IGNORE_SCHEDULE_WINDOW", "").lower() in ("1", "true", "yes")
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

    # ---- 1. the schedule window ---------------------------------------------------------------------
    now = dt.datetime.now(tz=UTC)
    win = nd.schedule_window(now)
    say("**schedule** `%s` — %s" % (win.code, win.reason))
    for k in ("now_utc", "now_local", "target_utc", "utc_offset_hours", "minutes_after_target"):
        if k in (win.detail or {}):
            say("  - %s: `%s`" % (k, win.detail[k]))
    if not win.proceed:
        if IGNORE_WINDOW:
            say()
            say("> **schedule window overridden** by an explicit operator request. This changes only WHEN "
                "the validation runs. Every gate still runs fresh on the exact head, and every fail-closed "
                "rule below still applies.")
        else:
            finish(0, "OUTSIDE_SCHEDULE_WINDOW", "Nothing ran. This is the other cron firing.")

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
    correlation = "nightly-%s-%s" % (now.strftime("%Y%m%dT%H%M%SZ"), expected_sha[:12])
    say("  - pull request: **#%s** (`%s`)" % (number, head_ref))
    say("  - head under test: `%s`" % expected_sha)
    say("  - correlation id: `%s`" % correlation)

    # ---- 3. dispatch all four gates at that exact ref ----------------------------------------------
    say()
    say("**dispatch** four authoritative gates at `%s`" % head_ref)
    dispatched_at = dt.datetime.now(tz=UTC)
    for wf in nd.REQUIRED_GATES:
        try:
            api("/actions/workflows/%s/dispatches" % wf, "POST",
                {"ref": head_ref, "inputs": {"expected_sha": expected_sha,
                                             "correlation_id": correlation,
                                             "nightly": "true"}})
            say("  - dispatched `%s`" % wf)
        except urllib.error.HTTPError as e:
            say("  - FAILED to dispatch `%s`: HTTP %s %s"
                % (wf, e.code, e.read().decode("utf-8", "replace")[:200]))
            finish(1, "DISPATCH_FAILED")

    # ---- 4. wait for all four ----------------------------------------------------------------------
    say()
    say("**waiting** up to %d minutes for all four to complete" % (GATE_BUDGET_S // 60))
    deadline = time.time() + GATE_BUDGET_S
    runs, last = [], ""
    while True:
        runs = collect_runs(correlation, dispatched_at)
        done = sum(1 for r in runs if r["status"] == "completed")
        state = "%d/%d complete" % (done, len(nd.REQUIRED_GATES))
        if state != last:
            say("  - %s" % state)
            last = state
        if len(runs) >= len(nd.REQUIRED_GATES) and done >= len(nd.REQUIRED_GATES):
            break
        if time.time() > deadline:
            say("  - BUDGET EXHAUSTED with %s" % state)
            break
        time.sleep(POLL_S)

    # ---- 5. all four, fresh, this sha, green -------------------------------------------------------
    gates = nd.classify_gate_runs(expected_sha, correlation, runs)
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
                   "commit_message": "All four authoritative gates ran fresh against %s under %s and "
                                     "passed.\n\nThis merge introduces no content of its own."
                                     % (expected_sha, correlation)})
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


def collect_runs(correlation, since):
    """Runs carrying tonight's correlation id, one entry per gate, shaped for classify_gate_runs."""
    out, seen = [], set()
    for wf in nd.REQUIRED_GATES:
        try:
            data = api("/actions/workflows/%s/runs?event=workflow_dispatch&per_page=25" % wf)
        except urllib.error.HTTPError:
            continue
        for r in data.get("workflow_runs", []):
            title = r.get("display_title") or r.get("name") or ""
            if correlation not in title:
                continue
            if r["id"] in seen:
                continue
            seen.add(r["id"])
            out.append({"workflow_file": os.path.basename(r.get("path") or ""),
                        "head_sha": r.get("head_sha"), "event": r.get("event"),
                        "display_title": title, "status": r.get("status"),
                        "conclusion": r.get("conclusion"), "id": r["id"]})
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
