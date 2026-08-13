#!/usr/bin/env python3
# The judgement half of the Phase-4 production dependency gate, in one file so the gate and its self-test
# run the IDENTICAL code. A self-test that exercises a second copy of the rules proves nothing about the
# copy that runs in CI.
#
# Usage: dependency-judgement.py <prod-audit.json> <full-audit.json> <acceptances.json> <triage-doc.md>
# Exit 0 when every production advisory is either absent or covered by a Product-Owner acceptance.
import json, sys, datetime

prod_f, full_f, acc_f, doc_f = sys.argv[1:5]
prod = json.load(open(prod_f, encoding="utf-8"))
full = json.load(open(full_f, encoding="utf-8"))
acc  = json.load(open(acc_f, encoding="utf-8"))
try:
    doc = open(doc_f, encoding="utf-8").read()
except OSError:
    doc = None

REQUIRED = ("package", "advisories", "severity", "rationale",
            "decided_by", "decision_ref", "decided_on", "expires_on")
today = datetime.date.today().isoformat()

def advisories_of(v):
    """The GHSA ids a package is flagged for, following `via` to the advisory objects."""
    out = set()
    for via in v.get("via", []):
        if isinstance(via, dict):
            url = via.get("url", "")
            out.add(url.rsplit("/", 1)[-1] if "GHSA-" in url else (via.get("title") or "?"))
    return out

rc = 0
prod_v = prod.get("vulnerabilities", {})
full_v = full.get("vulnerabilities", {})
print("  full tree:       %d advisory group(s)   (recorded, not gated)" % len(full_v))
print("  production tree: %d advisory group(s)" % len(prod_v))

# ---- the acceptance file must be well formed, whatever it contains -------------------------------------
accepted = {}
for e in acc.get("acceptances", []):
    missing = [k for k in REQUIRED if not e.get(k)]
    if missing:
        print("  [FAIL]      an acceptance for %r is missing %s; an acceptance without a named decision "
              "is not one" % (e.get("package", "?"), ", ".join(missing)))
        rc = 1
        continue
    if str(e["expires_on"]) < today:
        print("  [expired]   %s — accepted until %s, which has passed. An expired acceptance is no "
              "acceptance." % (e["package"], e["expires_on"]))
        rc = 1
        continue
    accepted.setdefault(e["package"], set()).update(e["advisories"])

# ---- every production advisory needs a decision ---------------------------------------------------------
for pkg in sorted(prod_v):
    ids = advisories_of(prod_v[pkg])
    sev = prod_v[pkg].get("severity", "?")
    have = accepted.get(pkg, set())
    uncovered = sorted(ids - have)
    if not uncovered:
        print("  [PO-accepted] %s (%s) — %d advisory/ies, accepted in governance/dependency-acceptances.json"
              % (pkg, sev, len(ids)))
        continue
    triaged = doc is not None and pkg in doc
    word = "triaged, NOT accepted" if triaged else "not assessed at all"
    print("  [FAIL]      %s (%s) — %s: %s" % (pkg, sev, word, ", ".join(uncovered[:6])))
    print("              A production vulnerability is only 'accepted' when the Product Owner accepted it,")
    print("              recorded in governance/dependency-acceptances.json with the advisory ids.")
    rc = 1

# ---- an acceptance for a risk that no longer exists hides the next real one -----------------------------
for pkg in sorted(accepted):
    if pkg not in prod_v:
        print("  [stale]     %s is accepted but is no longer reported in production; remove the acceptance"
              % pkg)
        rc = 1

if doc is None:
    print("  [FAIL]      docs/PHASE4_DEPENDENCY_TRIAGE.md is missing")
    rc = 1

if not prod_v and rc == 0:
    print("  the production tree reports NO advisories, so nothing is being accepted by anybody")

print()
if rc == 0:
    print("PHASE4_DEPENDENCY_GATE: PASS (no production advisory is unaccounted for)")
else:
    print("PHASE4_DEPENDENCY_GATE: FAIL — production dependency risk needs a Product Owner decision")
sys.exit(rc)
