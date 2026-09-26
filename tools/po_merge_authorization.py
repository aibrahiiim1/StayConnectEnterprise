#!/usr/bin/env python3
"""NO PRODUCT OWNER ACCEPTANCE = NO MERGE.

The one required status context on master under the Product-Owner-led delivery model (D42). It reports
`po-merge-authorization` for a pull request, and it passes ONLY while the pull request carries the label
`po-merge-approved` on the exact head being judged. Everything else fails, so a merge is refused by GitHub
itself until the Product Owner's approval is recorded -- not by prose, and not by an agent remembering.

HOW APPROVAL IS RECORDED. Applying the label IS the approval record: GitHub keeps the labelling event, its
actor and its time in the pull request's timeline, and this check prints them into its own log on every run.
It is applied only after the Product Owner says "Approved", "Merge it" or an equally explicit instruction,
or when the mission itself authorized the merge in advance.

APPROVAL IS BOUND TO THE HEAD IT WAS GIVEN FOR. A push after approval (`synchronize`) removes the label and
fails: what the Product Owner tested is no longer what would merge. Re-approval is a new label.

FAIL CLOSED. A draft, a missing label, a head that moved while this ran, an unreadable pull request, or any
error is a FAIL. The check never passes because it could not look.

WHAT IT CANNOT DO. The Product Owner and the delivery agent act through the same GitHub account, so this
cannot tell WHO pressed the button; it makes an accidental or automatic merge impossible and every approval
auditable. That residual is recorded in governance/branch-protection.json.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

LABEL = "po-merge-approved"
API = os.environ.get("GITHUB_API_URL", "https://api.github.com")


def decide(action, draft, labels, event_head, live_head):
    """Pure decision. Returns (passed, revoke_label, reason)."""
    has = LABEL in [str(x).lower() for x in (labels or [])]
    if not event_head or not live_head:
        return False, False, "the pull request head could not be established; refusing"
    if event_head != live_head:
        return False, False, ("this run judged %s but the pull request head is now %s; a later run judges the "
                              "new head" % (event_head[:12], live_head[:12]))
    if action == "synchronize" and has:
        return False, True, ("new commits were pushed after Product Owner approval; the approval covered the "
                             "previous head and is revoked. Re-approve the new head to merge")
    if draft:
        return False, False, "the pull request is a draft; a draft is never merge-authorized"
    if not has:
        return False, False, ("NO PRODUCT OWNER ACCEPTANCE = NO MERGE. Apply the label '%s' only after the "
                              "Product Owner explicitly approves the merge" % LABEL)
    return True, False, "Product Owner merge authorization is recorded for head %s" % live_head[:12]


def call(method, path, token):
    req = urllib.request.Request("%s/%s" % (API.rstrip("/"), path.lstrip("/")), method=method,
                                 headers={"Accept": "application/vnd.github+json",
                                          "Authorization": "Bearer %s" % token,
                                          "User-Agent": "stayconnect-po-merge-authorization"})
    for attempt in (1, 2, 3):
        try:
            with urllib.request.urlopen(req, timeout=25) as r:
                body = r.read().decode("utf-8")
                return json.loads(body) if body else {}
        except urllib.error.HTTPError as e:
            if e.code == 404 and method == "DELETE":
                return {}
            if attempt == 3:
                raise
        except Exception:  # noqa: BLE001
            if attempt == 3:
                raise
        time.sleep(attempt)


def main():
    token = os.environ.get("GITHUB_TOKEN") or ""
    repo = os.environ.get("GITHUB_REPOSITORY") or ""
    try:
        event = json.load(open(os.environ["GITHUB_EVENT_PATH"], encoding="utf-8"))
        action = event.get("action") or ""
        pr = event["pull_request"]
        number = pr["number"]
        event_head = pr["head"]["sha"]
        live = call("GET", "repos/%s/pulls/%d" % (repo, number), token)
        labels = [l["name"] for l in live.get("labels") or []]
        passed, revoke, reason = decide(action, live.get("draft"), labels, event_head, live["head"]["sha"])
        print("pull request #%d  event=%s  head=%s  labels=%s" % (number, action, event_head, labels))
        if revoke:
            call("DELETE", "repos/%s/issues/%d/labels/%s" % (repo, number, LABEL), token)
            print("label '%s' removed: approval revoked by a new push" % LABEL)
        if passed:
            events = call("GET", "repos/%s/issues/%d/events?per_page=100" % (repo, number), token) or []
            grants = [e for e in events if e.get("event") == "labeled"
                      and (e.get("label") or {}).get("name") == LABEL]
            if grants:
                g = grants[-1]
                print("approval record: label applied by %s at %s"
                      % ((g.get("actor") or {}).get("login"), g.get("created_at")))
    except Exception as exc:  # noqa: BLE001 - any error is a refusal
        passed, reason = False, "the authorization could not be established (%s); refusing" % exc
    print(("PO_MERGE_AUTHORIZATION = PASS: " if passed else "PO_MERGE_AUTHORIZATION = FAIL: ") + reason)
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
