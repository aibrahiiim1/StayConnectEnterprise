#!/usr/bin/env python3
"""CLAIM-VERSUS-CODE PARITY for governance/project-state.json.

WHY THIS EXISTS.

Every other governance check asks whether the current-state surfaces agree with EACH OTHER. That is a
consistency check, and consistency is exactly what a stale fact preserves: `phase4_manual_review_frontend:
false` sat in project-state.json for ten transitions while the screen it denied was built, tested and gated
by CI, and every validator passed the whole time because nothing compared the claim to the repository.

So this compares each "is it built?" claim to a MEASURABLE FACT ABOUT THE TREE. A claim is a hypothesis; the
file, the function or the route is the evidence. Both directions fail:

    claimed built + no evidence   -> the claim is unsupported
    claimed NOT built + evidence  -> the claim is stale, which is the failure that actually happened

It deliberately does NOT try to judge quality. It cannot tell whether a screen is good, only whether it
exists. That is the right scope: the failure mode being closed is a fact that stopped being true and nobody
noticed, not a fact that was never true.

Historical receipts under governance/transitions/ are NEVER read or judged here. They record what was true
when they were written and rewriting them would destroy the audit trail this project runs on.

Usage: validate-state-parity.py [PATH-TO-project-state.json]

The optional path exists so the self-test can drive THIS file -- the one CI runs -- against synthetic stale
states, rather than a second copy of the rules that could drift from it.

Exit 0 when every claim matches the tree, 1 on any mismatch, 2 if the state file cannot be read.
"""
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def rd(rel):
    """File contents, or None when the path does not exist."""
    p = os.path.join(ROOT, rel)
    if not os.path.isfile(p):
        return None
    with open(p, encoding="utf-8", errors="replace") as fh:
        return fh.read()


def exists(rel):
    return os.path.exists(os.path.join(ROOT, rel))


def in_migrations(needle):
    """True when any Phase-4 UP migration defines/mentions the given symbol."""
    d = os.path.join(ROOT, "data-plane", "migrations")
    if not os.path.isdir(d):
        return False
    for name in os.listdir(d):
        if name.endswith(".up.sql"):
            with open(os.path.join(d, name), encoding="utf-8", errors="replace") as fh:
                if needle in fh.read():
                    return True
    return False


def phase4_migrations():
    """Every Phase-4 UP migration present in the tree, by version prefix."""
    d = os.path.join(ROOT, "data-plane", "migrations")
    out = []
    if os.path.isdir(d):
        for name in sorted(os.listdir(d)):
            m = re.match(r"^(00[1-9][0-9])_phase4_.*\.up\.sql$", name)
            if m:
                out.append(name[: -len(".up.sql")])
    return out


# ---------------------------------------------------------------------------------------------------
# The claims. Each entry is (state key, expected-when-built value, a predicate that MEASURES the tree,
# and the sentence a reader needs when it fails).
# ---------------------------------------------------------------------------------------------------
CLAIMS = [
    ("phase4_manual_review_frontend", True,
     lambda: exists("hotel-admin/components/phase4/manual-review-view.tsx"),
     "hotel-admin/components/phase4/manual-review-view.tsx"),

    ("phase4_operator_surface", True,
     lambda: all(exists("hotel-admin/components/phase4/%s-view.tsx" % v)
                 for v in ("financial-health", "financial-recovery", "manual-review", "settlements")),
     "the four hotel-admin/components/phase4/*-view.tsx screens"),

    ("phase4_runtime_integration", True,
     lambda: bool(rd("data-plane/cmd/edged/resources_phase4_finops.go"))
     and "financialOpsRoutes" in (rd("data-plane/cmd/edged/resources_phase4_finops.go") or ""),
     "edged's financial-ops routes"),

    ("phase4_entitlement_grant_wired", True,
     lambda: in_migrations("p4_entitlement_grant_kernel"),
     "the p4_entitlement_grant_kernel writer in a Phase-4 migration"),

    ("phase4_financial_recovery_mode", True,
     lambda: in_migrations("p4_declare_financial_recovery") and in_migrations("p4_hold_financial_rails"),
     "the FINANCIAL_RECOVERY_MODE functions in a Phase-4 migration"),

    ("phase4_observability", True,
     lambda: in_migrations("enqueued_at")
     and "financial" in (rd("data-plane/cmd/edged/resources_phase4_finops.go") or ""),
     "the observability column and the health surface"),
]

# Status STRINGS that must not still say a thing is unbuilt once the tree proves otherwise. The check is
# deliberately about the words a reader acts on, because that is what goes stale.
STATUS_CLAIMS = [
    ("phase4_manual_review_operator_workflow", r"FRONTEND_NOT_BUILT",
     lambda: exists("hotel-admin/components/phase4/manual-review-view.tsx"),
     "the Manual Review screen exists"),
    ("phase4_payments", r"GO_DOMAIN_NOT_BUILT",
     lambda: exists("data-plane/internal/payment") and in_migrations("p4_apply_provider_outcome"),
     "the Go payment domain exists"),
]

# Phrases that must not survive in the forward-looking surfaces once the tree proves the work landed.
FORWARD_SURFACES = ("phase4_remaining_scope",)
DELIVERED_PHRASES = [
    (re.compile(r"manual review operator workflow", re.I),
     lambda: exists("hotel-admin/components/phase4/manual-review-view.tsx"),
     "the Manual Review operator workflow is delivered"),
    (re.compile(r"payment-provider execution and the settlement boundary", re.I),
     lambda: exists("data-plane/internal/payment"),
     "the payment/settlement boundary is delivered"),
    (re.compile(r"restore\s*/\s*FINANCIAL_RECOVERY_MODE", re.I),
     lambda: in_migrations("p4_declare_financial_recovery"),
     "FINANCIAL_RECOVERY_MODE is delivered"),
    (re.compile(r"operator UI and observability", re.I),
     lambda: exists("hotel-admin/components/phase4/financial-health-view.tsx"),
     "the operator UI and observability are delivered"),
]


def main():
    state_path = sys.argv[1] if len(sys.argv) > 1 else os.path.join(
        ROOT, "governance", "project-state.json")
    try:
        with open(state_path, encoding="utf-8") as fh:
            state = json.load(fh)
    except Exception as exc:                                   # unreadable state is an infrastructure fault
        print("INFRA: cannot read %s: %s" % (state_path, exc))
        return 2

    facts = state.get("current_state_facts", {})
    ok = 0
    bad = 0

    def good(msg):
        nonlocal ok
        print("  ok: %s" % msg)
        ok += 1

    def fail(msg):
        nonlocal bad
        print("  FAIL: %s" % msg)
        bad += 1

    print("== A. boolean build claims match the tree ==")
    for key, built_value, measure, evidence in CLAIMS:
        if key not in facts:
            fail("%s is absent; a build claim that disappears is how a surface goes unmeasured" % key)
            continue
        claimed = facts[key]
        actual = bool(measure())
        if actual and claimed != built_value:
            fail("%s says %r but %s IS present -- the claim is stale" % (key, claimed, evidence))
        elif not actual and claimed == built_value:
            fail("%s claims built, but %s is NOT present -- the claim is unsupported" % (key, evidence))
        else:
            good("%s agrees with the tree" % key)

    print("== B. status strings do not deny delivered work ==")
    for key, stale_pattern, measure, why in STATUS_CLAIMS:
        val = str(facts.get(key, ""))
        if re.search(stale_pattern, val) and measure():
            fail("%s still reads %r, but %s" % (key, val, why))
        else:
            good("%s agrees with the tree" % key)

    print("== C. forward-looking scope does not list delivered work ==")
    for key in FORWARD_SURFACES:
        blob = json.dumps(facts.get(key, ""))
        hits = 0
        for pattern, measure, why in DELIVERED_PHRASES:
            if pattern.search(blob) and measure():
                fail("%s still lists work that is delivered: %s" % (key, why))
                hits += 1
        if hits == 0:
            good("%s lists no delivered work" % key)

    print("== D. the migration inventory names every Phase-4 migration in the tree ==")
    declared = str(facts.get("phase4_migration", ""))
    present = phase4_migrations()
    if not present:
        fail("no Phase-4 migrations found on disk; the inventory cannot be checked")
    else:
        missing = [m for m in present if m not in declared]
        if missing:
            fail("phase4_migration omits %s" % ", ".join(missing))
        else:
            good("phase4_migration names all %d Phase-4 migrations" % len(present))
        # ...and the reverse: a named migration that no longer exists is a different kind of stale.
        for name in re.findall(r"00\d\d_phase4_[a-z0-9_]+", declared):
            if name not in present:
                fail("phase4_migration names %s, which is not in data-plane/migrations/" % name)

    print("== E. the CI gate label covers the migrations it actually runs ==")
    wf = rd(".github/workflows/phase4-financial-core.yml")
    if wf is None:
        fail(".github/workflows/phase4-financial-core.yml is missing")
    elif present:
        highest = present[-1][:4]
        labels = re.findall(r"Migrations 0011-(\d{4})", wf)
        if not labels:
            fail("the CI migration step has no 'Migrations 0011-NNNN' label to check")
        elif labels[0] != highest:
            fail("the CI step says it covers 0011-%s but the tree ends at %s" % (labels[0], highest))
        else:
            good("the CI migration label matches the highest Phase-4 migration (%s)" % highest)

    print("=" * 50)
    if bad:
        print("STATE_PARITY = FAIL (%d)" % bad)
        return 1
    print("STATE_PARITY = PASS (%d checks)" % ok)
    return 0


if __name__ == "__main__":
    sys.exit(main())
