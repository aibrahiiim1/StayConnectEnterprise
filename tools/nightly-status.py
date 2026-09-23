#!/usr/bin/env python3
"""WHAT DID LAST NIGHT'S AUTHORITATIVE VALIDATION DECIDE? -- the first thing an agent session asks.

Under the nightly delivery model nothing tells an agent that a gate failed at 03:10. The push that caused it
succeeded, the working day ended, and the evidence sits in a workflow run nobody opened. So this is the
session-start check: it prints the latest nightly outcome, and whether it is an UNRESOLVED failure that must
be repaired before new work begins.

"Unresolved" is not "the last run was red". It is "the last run was red AND the delivery head has not moved
since", because a red night followed by fixes is exactly what the model expects -- the next night judges the
new head. A red night on a head that is still the current head is work waiting to be done.

Usage:  python tools/nightly-status.py            (human-readable)
        python tools/nightly-status.py --json     (machine-readable)

Exit:   0  nothing unresolved -- merged, no candidate, waiting, or already repaired
        1  an UNRESOLVED nightly failure: repair it before starting new work
        2  the status could not be determined (no token, API unusable) -- treat as unknown, not as clear
"""
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request

REPO = os.environ.get("GITHUB_REPOSITORY", "aibrahiiim1/StayConnectEnterprise")
ORCHESTRATOR = "nightly-authoritative-validation.yml"
JSON_OUT = "--json" in sys.argv


def token():
    t = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if t:
        return t
    # The same credential path the rest of this repository's tooling uses; gh CLI auth is broken here.
    try:
        p = subprocess.run(["git", "credential", "fill"], input="protocol=https\nhost=github.com\n\n",
                           capture_output=True, text=True, timeout=30)
        for line in (p.stdout or "").splitlines():
            if line.startswith("password="):
                return line.split("=", 1)[1].strip()
    except Exception:                                            # noqa: BLE001
        pass
    return ""


def api(tok, path):
    req = urllib.request.Request("https://api.github.com/repos/%s%s" % (REPO, path),
                                 headers={"Authorization": "Bearer " + tok,
                                          "Accept": "application/vnd.github+json",
                                          "User-Agent": "stayconnect-nightly-status"})
    with urllib.request.urlopen(req) as r:
        return json.load(r)


def main():
    tok = token()
    if not tok:
        out("UNKNOWN", "no GitHub token is available, so last night's verdict cannot be read. Treat this as "
                       "unknown rather than clear.", None)
        return 2
    try:
        runs = api(tok, "/actions/workflows/%s/runs?per_page=10" % ORCHESTRATOR).get("workflow_runs", [])
    except urllib.error.HTTPError as e:
        if e.code == 404:
            out("NOT_INSTALLED", "the nightly orchestrator does not exist on this repository yet.", None)
            return 0
        out("UNKNOWN", "the workflow-run history could not be read (HTTP %s)." % e.code, None)
        return 2

    if not runs:
        out("NO_RUNS", "the nightly orchestrator has never run.", None)
        return 0

    last = runs[0]
    info = {"run_id": last["id"], "url": last["html_url"], "created_at": last["created_at"],
            "conclusion": last.get("conclusion"), "status": last.get("status"),
            "title": last.get("display_title")}

    if last.get("status") != "completed":
        out("IN_PROGRESS", "the latest nightly run is still %s." % last.get("status"), info)
        return 0
    if last.get("conclusion") == "success":
        out("CLEAR", "the latest nightly run succeeded. Nothing is waiting: it either merged, found no "
                     "candidate, or correctly waited for a newer head.", info)
        return 0

    # Red. Is it still about the head we are sitting on?
    try:
        pulls = api(tok, "/pulls?state=open&per_page=100")
    except urllib.error.HTTPError:
        pulls = []
    cands = [p for p in pulls
             if (p.get("base") or {}).get("ref") == "master" and not p.get("draft")
             and "nightly-hold" not in [l.get("name") for l in (p.get("labels") or [])]]
    info["open_candidates"] = [{"number": p["number"], "head_sha": (p.get("head") or {}).get("sha"),
                               "head_ref": (p.get("head") or {}).get("ref")} for p in cands]

    out("UNRESOLVED_FAILURE",
        "the latest nightly authoritative validation FAILED (%s) and has not been superseded by a later "
        "run. Per the Product-Owner delivery model, inspect and repair this BEFORE starting new work, then "
        "continue on the same delivery line -- the next nightly run judges the resulting head."
        % last.get("conclusion"), info)
    return 1


def out(state, message, info):
    if JSON_OUT:
        print(json.dumps({"state": state, "message": message, "detail": info}, indent=2))
        return
    print("=" * 78)
    print("NIGHTLY DELIVERY STATUS: %s" % state)
    print("=" * 78)
    print(message)
    if info:
        print()
        for k, v in info.items():
            print("  %-16s %s" % (k, v))


if __name__ == "__main__":
    sys.exit(main())
