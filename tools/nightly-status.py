#!/usr/bin/env python3
"""WHAT DID LAST NIGHT'S AUTHORITATIVE VALIDATION DECIDE? -- the first thing an agent session asks.

Under the nightly delivery model nothing tells an agent that a gate failed at 06:00. The push that caused it
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
import importlib.util
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request

_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_spec = importlib.util.spec_from_file_location("nd", os.path.join(_ROOT, "tools", "nightly_delivery.py"))
nd = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(nd)

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


def read_verdict(tok, run):
    """The verdict and tested head a run recorded, read from its own job log.

    The orchestrator prints `NIGHTLY_DELIVERY_VERDICT = X` and `NIGHTLY_TESTED_HEAD = Y` as its last act. They
    are read back here rather than inferred from the run conclusion, because BOTH the no-op firing and a
    successful merge exit 0 -- the conclusion cannot tell them apart, and that is the whole difficulty.

    An unreadable log yields no verdict, which select_authoritative_run() treats as authoritative: not being
    able to read a run must never become a way to skip a red night.
    """
    out = {"verdict": "", "tested_head": ""}
    try:
        jobs = api(tok, "/actions/runs/%d/jobs" % run["id"]).get("jobs") or []
    except urllib.error.HTTPError:
        return out
    for j in jobs:
        try:
            log = joblog(tok, j["id"])
        except Exception:                                        # noqa: BLE001
            continue
        m = re.findall(r"NIGHTLY_DELIVERY_VERDICT = (\S+)", log)
        if m:
            out["verdict"] = m[-1]
        h = re.findall(r"NIGHTLY_TESTED_HEAD = (\S+)", log)
        if h and h[-1] != "none":
            out["tested_head"] = h[-1]
        if out["verdict"]:
            break
    return out


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise urllib.error.HTTPError(req.full_url, code, newurl, headers, fp)


def joblog(tok, job_id):
    op = urllib.request.build_opener(_NoRedirect)
    req = urllib.request.Request(
        "https://api.github.com/repos/%s/actions/jobs/%d/logs" % (REPO, job_id),
        headers={"Authorization": "Bearer " + tok, "Accept": "application/vnd.github+json",
                 "User-Agent": "stayconnect-nightly-status"})
    try:
        return op.open(req).read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        if e.code in (301, 302, 307, 308):
            url = e.reason if isinstance(e.reason, str) else e.headers.get("Location")
            return urllib.request.urlopen(
                urllib.request.Request(url, headers={"User-Agent": "sc"})).read().decode("utf-8", "replace")
        raise


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

    # THE NEWEST RUN IS NOT NECESSARILY THE ONE THAT DECIDED ANYTHING. The workflow fires at 00:10Z and
    # 01:10Z; one firing works and the other exits 0 having done nothing, and under EEST the no-op is the
    # LATER of the two. Reading runs[0] would therefore report CLEAR on the strength of a no-op while the
    # night's real validation was failing -- the tool whose whole purpose is to notice a failure would be the
    # thing hiding it. So each run is annotated with the verdict it recorded, and the first one that got past
    # the window guard is the authoritative one.
    annotated = [dict(r, **read_verdict(tok, r)) for r in runs[:6]]
    last = nd.select_authoritative_run(annotated)
    if last is None:
        out("NO_AUTHORITATIVE_RUN",
            "every recent orchestrator run was the non-authoritative cron firing, so no night has decided "
            "anything yet.", {"inspected": [r["id"] for r in annotated]})
        return 0
    info = {"run_id": last["id"], "url": last["html_url"], "created_at": last["created_at"],
            "conclusion": last.get("conclusion"), "status": last.get("status"),
            "verdict": last.get("verdict") or "unreadable",
            "tested_head": last.get("tested_head") or "unreadable"}

    if last.get("status") != "completed":
        out("IN_PROGRESS", "the latest nightly run is still %s." % last.get("status"), info)
        return 0
    if last.get("conclusion") == "success":
        out("CLEAR", "the latest nightly run succeeded. Nothing is waiting: it either merged, found no "
                     "candidate, or correctly waited for a newer head.", info)
        return 0

    # RED -- BUT "RED" IS NOT THE SAME AS "WAITING FOR SOMEBODY". A red night followed by a fix is the normal
    # path through this model, so the question is whether the head that failed is STILL the delivery head. If a
    # commit has landed since, the next nightly run judges that new head and there is nothing for this session
    # to repair.
    # A FAILED LOOKUP IS NOT AN EMPTY LIST. This substituted [] on an HTTP error, and an empty candidate list
    # reads as "the head moved on, nothing is owed" -- so an API failure produced a CLEAR verdict in the one
    # check CLAUDE.md tells every session to run before starting work. Now it is indistinguishable from nothing
    # only if we make it so, and we do not: None means unreadable, and the decision module fails closed on it.
    try:
        pulls = api(tok, "/pulls?state=open&per_page=100")
    except urllib.error.HTTPError as e:
        info["open_candidates"] = "UNREADABLE (HTTP %s)" % e.code
        unresolved, why = nd.failure_is_unresolved(last, None)
        info["resolution"] = why
        out("UNKNOWN",
            "the last authoritative nightly validation FAILED and the open-candidate list could not be read "
            "(HTTP %s), so whether a repair is owed cannot be determined. Treat this as UNKNOWN, not as "
            "clear." % e.code, info)
        return 2
    cands = [p for p in pulls
             if (p.get("base") or {}).get("ref") == "master" and not p.get("draft")
             and nd.HOLD_LABEL not in [l.get("name") for l in (p.get("labels") or [])]]
    heads = [(p.get("head") or {}).get("sha") for p in cands]
    info["open_candidates"] = [{"number": p["number"], "head_sha": (p.get("head") or {}).get("sha"),
                               "head_ref": (p.get("head") or {}).get("ref")} for p in cands]

    unresolved, why = nd.failure_is_unresolved(last, heads)
    info["resolution"] = why
    if not unresolved:
        out("SUPERSEDED_FAILURE",
            "the last authoritative nightly validation FAILED, but %s. Nothing is owed before new work; the "
            "next nightly run judges the current head." % why, info)
        return 0

    out("UNRESOLVED_FAILURE",
        "the latest nightly authoritative validation FAILED (%s) and %s. Per the Product-Owner delivery "
        "model, inspect and repair this BEFORE starting new work, then continue on the same delivery line -- "
        "the next nightly run judges the resulting head." % (last.get("conclusion"), why), info)
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
