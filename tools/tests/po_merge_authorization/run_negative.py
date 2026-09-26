#!/usr/bin/env python3
"""Proves the merge-authorization decision is fail-closed: only an approved, non-draft, current head passes."""
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", ".."))
from po_merge_authorization import LABEL, decide  # noqa: E402

H, H2 = "a" * 40, "b" * 40
CASES = [
    # (why, action, draft, labels, event_head, live_head, passed, revoke)
    ("no label is refused", "opened", False, [], H, H, False, False),
    ("an unrelated label is refused", "labeled", False, ["nightly-hold"], H, H, False, False),
    ("the approval label passes", "labeled", False, [LABEL], H, H, True, False),
    ("label matching is case-insensitive", "labeled", False, ["PO-Merge-Approved"], H, H, True, False),
    ("a draft is refused even when approved", "labeled", True, [LABEL], H, H, False, False),
    ("a push after approval revokes it", "synchronize", False, [LABEL], H, H, False, True),
    ("a push without approval is simply refused", "synchronize", False, [], H, H, False, False),
    ("removing the label is refused", "unlabeled", False, [], H, H, False, False),
    ("a moved head is refused", "labeled", False, [LABEL], H, H2, False, False),
    ("an unknown head is refused", "labeled", False, [LABEL], "", H, False, False),
]

fails = 0
for why, action, draft, labels, eh, lh, want_pass, want_revoke in CASES:
    passed, revoke, reason = decide(action, draft, labels, eh, lh)
    good = passed == want_pass and revoke == want_revoke
    fails += not good
    print("  %s  %s -- %s" % ("PASS" if good else "FAIL", why, reason))
print("PO_MERGE_AUTHORIZATION_NEGATIVE = %s (%d cases)" % ("PASS" if not fails else "FAIL", len(CASES)))
sys.exit(1 if fails else 0)
