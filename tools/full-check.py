#!/usr/bin/env python3
"""FULL CHECK THE WHOLE CODE -- the CI half of the explicit, opt-in comprehensive validation (D42).

Dispatches the four comprehensive gates at one ref, waits for them, and reports each verdict against the
exact commit they ran on. It never merges, never deploys and never changes a label: a FULL CHECK validates,
and a merge still requires the Product Owner's approval (po-merge-authorization).

These gates run ONLY when this is invoked on the Product Owner's instruction "FULL CHECK THE WHOLE CODE".
Nothing else triggers them -- no pull request, no push, no schedule (tools/validate-delivery-protocol.py
asserts that). The local half of a FULL CHECK -- full repository review, full local suites and browser
suites, governance synchronization, live verification where deployment is authorized -- is described in
docs/PO_LED_DELIVERY_MODEL.md.

Usage:  python tools/full-check.py --ref <branch>  [--no-wait]
Token:  GITHUB_TOKEN, or the git credential helper for github.com.
Exit:   0 all four passed on the ref's head; 1 any failed or did not finish; 2 could not dispatch or read.
"""
import argparse
import datetime as dt
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

REPO = "aibrahiiim1/StayConnectEnterprise"
API = "https://api.github.com"
GATES = {
    "project-governance.yml": "governance",
    "phase3-software.yml": "phase3-full-software-gate",
    "phase4-financial-core.yml": "phase4-financial-core-gate",
    "phase5-post-stay-transfer.yml": "phase5-post-stay-transfer-gate",
}


def token():
    t = os.environ.get("GITHUB_TOKEN")
    if t:
        return t
    out = subprocess.run(["git", "credential", "fill"], input="protocol=https\nhost=github.com\n\n",
                         capture_output=True, text=True).stdout
    for line in out.splitlines():
        if line.startswith("password="):
            return line[len("password="):]
    return ""


def call(method, path, tok, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request("%s/%s" % (API, path), data=data, method=method,
                                 headers={"Accept": "application/vnd.github+json",
                                          "Authorization": "Bearer %s" % tok,
                                          "User-Agent": "stayconnect-full-check"})
    with urllib.request.urlopen(req, timeout=30) as r:
        raw = r.read().decode()
        return json.loads(raw) if raw else {}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--ref", required=True)
    ap.add_argument("--no-wait", action="store_true")
    a = ap.parse_args()
    tok = token()
    try:
        head = call("GET", "repos/%s/commits/%s" % (REPO, a.ref), tok)["sha"]
        since = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(seconds=30)).strftime("%Y-%m-%dT%H:%M:%SZ")
        for wf in GATES:
            call("POST", "repos/%s/actions/workflows/%s/dispatches" % (REPO, wf), tok, {"ref": a.ref})
    except (urllib.error.URLError, KeyError, ValueError) as exc:
        print("FULL_CHECK = CANNOT_DISPATCH (%s)" % exc)
        return 2
    print("FULL CHECK dispatched at %s (%s): %s" % (a.ref, head[:12], ", ".join(GATES.values())))
    if a.no_wait:
        return 0
    runs = {}
    deadline = time.time() + 3 * 3600
    while time.time() < deadline:
        for wf in GATES:
            try:
                rs = call("GET", "repos/%s/actions/workflows/%s/runs?event=workflow_dispatch&branch=%s"
                          "&created=%%3E%%3D%s&per_page=5" % (REPO, wf, a.ref, since), tok)["workflow_runs"]
            except (urllib.error.URLError, KeyError, ValueError):
                continue
            rs = [r for r in rs if r["head_sha"] == head]
            if rs:
                runs[wf] = rs[0]
        if len(runs) == len(GATES) and all(r["status"] == "completed" for r in runs.values()):
            break
        time.sleep(60)
    ok = True
    for wf, ctx in GATES.items():
        r = runs.get(wf)
        if not r:
            print("  %-32s NOT FOUND" % ctx)
            ok = False
            continue
        print("  %-32s %-10s run %s  %s" % (ctx, r.get("conclusion") or r["status"], r["id"], r["html_url"]))
        ok = ok and r.get("conclusion") == "success"
    print("FULL_CHECK = %s at %s" % ("PASS" if ok else "FAIL", head))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
