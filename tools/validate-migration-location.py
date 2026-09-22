#!/usr/bin/env python3
"""A MIGRATION THE RUNNER CANNOT SEE IS NOT A MIGRATION.

WHY THIS EXISTS, in one measured case. Migrations 0081, 0082 and 0083 were committed to a `migrations/`
directory at the repository ROOT. Every tool that matters reads `data-plane/migrations`:

    scripts/edge-migrate.sh:29        CANON_MIG_DIR="$HERE/data-plane/migrations"
                                      and --dir is REFUSED unless the target is disposable (line 106)
    scripts/clean-install-reconstruction.sh:21   MIG="$ROOT/data-plane/migrations"
    scripts/generate-production-baseline.sh:50   LAST_MIGRATION from data-plane/migrations/*.up.sql
    tools/check-fixture-parity.py                MIGRATIONS = data-plane/migrations

A grep of tools/, scripts/ and .github/ for the root path returned ZERO references. So the three files were
invisible to the supported live runner, to the factory-clean reconstruction, to the production-baseline
generator, to the fixture-parity check and to all four required gates -- which stayed green the whole time.

What that cost was not theoretical. All three are least-privilege GRANTs: svc_edged's read of
public.audit_log, of iam_v2.accounting_records and devices, and the five auth_contexts columns behind guest
attribution. They had been applied to the PRE-LIVE appliance by hand, so the appliance was at schema 0083
while a rebuild FROM THE REPOSITORY would stop at 0080 and call itself complete -- and the Audit log screen,
the Usage Explorer and Guest Activity would each silently break again, in exactly the way those three
migrations were written to fix. A disaster-recovery rebuild would have produced a working-looking appliance
that had quietly lost three privilege grants.

So the invariant is narrow and absolute: a numbered migration lives in a canonical directory, or it does not
exist. The check costs milliseconds and needs no database.

WHAT IT CHECKS

  1. LOCATION. Every NNNN_*.up.sql / NNNN_*.down.sql in the tree is inside a canonical directory. This is
     the rule the incident broke.
  2. PAIRING. Every up has a down. A migration with no down cannot be rolled back, and the rollback policy
     is not optional.
  3. UNIQUENESS. No two migrations in one directory share a number. Two 0084s mean one of them never runs,
     and which one depends on a sort order nobody chose.
  4. CONTIGUITY. The numbers run 1..N with no gap. A gap is the signature of exactly this defect -- a file
     that was moved, renamed or dropped -- and it is cheap to notice here and expensive to notice live.

WHAT IT DOES NOT CHECK. Content, order of application, whether a migration is applied anywhere, or whether
it is correct. Those belong to edge-migrate.sh, to the gates and to review. This answers one question: can
the tooling see it at all.
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# The directories a numbered migration may live in, and who reads each one.
CANONICAL = {
    # The appliance / site database. scripts/edge-migrate.sh, clean-install-reconstruction.sh and
    # generate-production-baseline.sh all resolve exactly this path.
    "data-plane/migrations": "the edge (site) database runner",
    # The Central control plane database, which has its own runner and its own ledger.
    "control-plane/migrations": "the Central control-plane runner",
}

# Trees that legitimately contain SQL that is NOT a numbered migration, or numbered files that are
# deliberately not part of a runner's sequence. Named rather than pattern-matched, so adding one is a
# decision somebody makes on purpose.
NOT_MIGRATIONS = {
    # The IAM-v2 base steps (mg0..mg9) and the generated production baseline. Both live UNDER a canonical
    # directory in their own subfolders and are applied by name, not by sequence.
    "data-plane/migrations/baseline",
    "data-plane/migrations/iam_base",
    # The disposable-Postgres gate fixture tree. Nothing here is applied to an appliance.
    "iam_v2_scratch",
    # Vendored, build and VCS trees.
    ".git",
    "node_modules",
    ".cleanroom",
}

NUMBERED = re.compile(r"^(\d{4})_(.+)\.(up|down)\.sql$")

# MIGRATIONS THAT HAVE NO DOWN AND PREDATE THIS CHECK.
#
# Thirteen Central control-plane migrations were written without a down half, before anything asked for one.
# They are listed BY EXACT FILENAME rather than excused by a pattern, for two reasons: the list is the debt,
# so it is visible and countable; and a FOURTEENTH unpaired migration still fails, which is the property
# that matters. Nothing here is excused for the edge database, where all 85 migrations already comply and
# the rollback policy is load-bearing (docs/ROLLBACK_POLICY.md).
#
# Writing thirteen down-migrations for schema this delivery does not touch would be a speculative change to
# the Central database, so they are recorded, not invented. Removing a name from this list is how the debt
# gets paid; adding one needs a reason in the commit that does it.
UNPAIRED_PREDATING_THIS_CHECK = {
    "control-plane/migrations": {
        "0023_appliance_pki",
        "0024_reassign_replace_alertstatus",
        "0025_lifecycle_grace_state",
        "0026_appliance_commands",
        "0027_offline_activation",
        "0028_appliance_updates",
        "0029_appliance_signed_assignments",
        "0030_assignment_signing_keys",
        "0031_commercial_controls",
        "0032_assignment_key_lifecycle",
        "0033_terminal_delivery_and_registry",
        "0034_drop_legacy_sessions_appliance_fk",
        "0036_pending_appliance_nullable",
    },
}

_failures = []


def bad(kind, msg, where=""):
    _failures.append((kind, msg, where))
    print("  FAIL [%s] %s%s" % (kind, msg, (" -- " + where) if where else ""))


def ok(msg):
    print("  ok: %s" % msg)


def rel(path):
    return os.path.relpath(path, ROOT).replace(os.sep, "/")


def skipped(relpath):
    for prefix in NOT_MIGRATIONS:
        if relpath == prefix or relpath.startswith(prefix + "/"):
            return True
    return False


def scan():
    """Every numbered migration file in the tree, as {directory: {number: {kind: filename}}}."""
    found = {}
    strays = []
    for dirpath, dirnames, filenames in os.walk(ROOT):
        d = rel(dirpath)
        if d == ".":
            d = ""
        # Prune whole trees rather than filtering file by file, so a huge node_modules costs nothing.
        dirnames[:] = [
            n for n in dirnames if not skipped((d + "/" + n).lstrip("/"))
        ]
        if d and skipped(d):
            continue
        for fn in filenames:
            m = NUMBERED.match(fn)
            if not m:
                continue
            number, _, kind = m.group(1), m.group(2), m.group(3)
            if d in CANONICAL:
                found.setdefault(d, {}).setdefault(number, {})[kind] = fn
            else:
                strays.append((d or "<repository root>", fn))
    return found, strays


def check_location(strays):
    if not strays:
        ok("every numbered migration is in a canonical directory (%s)" % ", ".join(sorted(CANONICAL)))
        return
    for where, fn in sorted(strays):
        bad(
            "migration-location",
            "%s is a numbered migration outside every canonical directory, so no runner, no "
            "reconstruction, no baseline generator and no gate can see it" % fn,
            where,
        )
    print(
        "         a numbered migration belongs in one of: %s"
        % ", ".join("%s (%s)" % (d, who) for d, who in sorted(CANONICAL.items()))
    )


def check_directory(d, by_number):
    numbers = sorted(int(n) for n in by_number)
    if not numbers:
        ok("%s holds no numbered migration" % d)
        return

    # 2. PAIRING
    known_unpaired = UNPAIRED_PREDATING_THIS_CHECK.get(d, set())
    unpaired = []
    carried = []
    stale_exception = set(known_unpaired)
    for n in sorted(by_number):
        kinds = by_number[n]
        if "up" not in kinds:
            # A down with no up is never excused: it is a rollback for a migration that does not exist.
            unpaired.append("%s has a down and no up" % kinds.get("down", n))
            continue
        if "down" in kinds:
            continue
        stem = kinds["up"][: -len(".up.sql")]
        if stem in known_unpaired:
            carried.append(stem)
            stale_exception.discard(stem)
        else:
            unpaired.append("%s has no .down.sql" % kinds["up"])
    for u in unpaired:
        bad("migration-pairing", u, d)
    # AN EXCEPTION THAT NO LONGER APPLIES IS ITSELF A FAILURE. If a down half was written, or the migration
    # renamed or removed, the list must shrink -- otherwise it decays into a permanent blanket excuse.
    for stem in sorted(stale_exception):
        bad(
            "migration-pairing-exception-stale",
            "%s is listed as predating this check but is now paired or gone; remove it from "
            "UNPAIRED_PREDATING_THIS_CHECK" % stem,
            d,
        )
    if not unpaired and not stale_exception:
        if carried:
            ok(
                "%s: %d of %d migrations have both halves; %d listed as predating this check"
                % (d, len(numbers) - len(carried), len(numbers), len(carried))
            )
        else:
            ok("%s: all %d migrations have both halves" % (d, len(numbers)))

    # 3. UNIQUENESS is structural in this shape -- two files with the same number and kind cannot coexist in
    # one directory -- so what is left to check is that the SAME number is not used by two different names.
    seen_names = {}
    for n in sorted(by_number):
        for kind, fn in by_number[n].items():
            stem = fn[: -len(".%s.sql" % kind)]
            prev = seen_names.setdefault(n, stem)
            if prev != stem:
                bad(
                    "migration-uniqueness",
                    "number %s is used by two different migrations (%s and %s)" % (n, prev, stem),
                    d,
                )

    # 4. CONTIGUITY
    lo, hi = min(numbers), max(numbers)
    missing = [n for n in range(lo, hi + 1) if n not in numbers]
    if missing:
        bad(
            "migration-contiguity",
            "%s: numbers %s are missing between %04d and %04d; a gap is the signature of a migration that "
            "was moved, renamed or dropped" % (d, ", ".join("%04d" % n for n in missing), lo, hi),
            d,
        )
    else:
        ok("%s: %04d..%04d with no gap (%d migrations)" % (d, lo, hi, len(numbers)))


def main():
    print("== migration location and sequence ==")
    found, strays = scan()
    check_location(strays)
    for d in sorted(CANONICAL):
        check_directory(d, found.get(d, {}))

    print("=" * 50)
    if _failures:
        print("MIGRATION_LOCATION = FAIL (%d)" % len(_failures))
        return 1
    print("MIGRATION_LOCATION = PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
