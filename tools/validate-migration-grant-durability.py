#!/usr/bin/env python3
"""A GRANT ONLY A MIGRATION MAKES IS A GRANT THAT DISAPPEARS.

WHY THIS EXISTS, AND IT IS NOT A STYLE RULE.

deploy/gatep/gatep-grants.sql opens by taking everything away:

    REVOKE ALL PRIVILEGES ON ALL TABLES    IN SCHEMA public  FROM <each service role>
    REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public  FROM <each service role>
    REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA public  FROM <each service role>
    ... and the same four for iam_v2 ...

and then re-grants exactly what the per-service Gate-P files name. Gate-P runs AFTER the numbered migrations
-- in scripts/clean-install-reconstruction.sh and in a real install -- so a privilege that exists ONLY in a
migration is revoked moments after it was granted.

FOUR REAL CASES. Migrations 0080, 0081, 0082 and 0083 each added one operator read a Hotel-Admin screen
needs. None was mirrored in Gate-P. The generated factory-clean baseline -- a dump of the end state of the
whole install chain -- contained ZERO of the four, while the PRE-LIVE appliance had all four, because there
they were applied by hand after Gate-P last ran. So the appliance worked and a rebuilt appliance would not,
and the screens that would silently break were exactly the ones those migrations were written to fix: the
Audit log (which had already once reported "No audit entries" while 379 rows existed), the Usage Explorer,
Policy History and Guest Activity.

WHY NO EXISTING CHECK CAUGHT IT. scripts/factory-clean-baseline-verify.sh compares the baseline path against
the upgrade path, and its catalog DOES include TABLEGRANT rows -- but both paths end with the same revoke, so
both were equally wrong and the comparison was IDENTICAL. A defect that is symmetric is invisible to a
symmetry check. This check is asymmetric on purpose: it compares migrations against Gate-P, not a path
against a path.

THE RULE. Every grant a numbered migration makes to a service role (svc_*) must also be named in
deploy/gatep/. The migration is still the right place to record WHY -- that is where the reasoning lives --
but Gate-P is what survives a reconcile. The Room-Login privilege chain already followed this rule and says
so: a privilege is "fixed at its authoritative source -- migrations 0057 and 0058 plus the per-service
Gate-P files, so a reconcile keeps them".

WHAT IT CHECKS, deliberately narrowly:

  * GRANT statements in data-plane/migrations/*.up.sql whose grantee is a svc_* role.
  * Matching is on (privilege-kind, object) -- a TABLE grant on iam_v2.devices must find a TABLE grant on
    iam_v2.devices to the same role somewhere in deploy/gatep/. Column lists are compared as a set when
    both sides carry one.
  * EXECUTE grants on functions are matched by function NAME, not full signature: Gate-P writes the full
    argument list and migrations often do not, and a name match is the honest strength of this check.

WHAT IT DOES NOT CHECK. Whether the grant is correct, whether it is too broad, or whether Gate-P grants
things no migration asked for (that is normal -- Gate-P is the authority). Only durability.
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MIGRATIONS = os.path.join(ROOT, "data-plane", "migrations")
GATEP = os.path.join(ROOT, "deploy", "gatep")

# GRANT <privs> ON [TABLE|FUNCTION|SCHEMA|SEQUENCE] <object> TO <role>[, <role>]
GRANT_RE = re.compile(
    r"\bGRANT\s+(?P<privs>.+?)\s+ON\s+(?P<kind>TABLE\s+|FUNCTION\s+|SCHEMA\s+|SEQUENCE\s+|ALL\s+TABLES\s+IN\s+SCHEMA\s+)?"
    r"(?P<object>[A-Za-z0-9_.\"]+(?:\s*\([^)]*\))?)\s+TO\s+(?P<roles>[A-Za-z0-9_, \"]+)",
    re.I | re.S)

SERVICE_ROLE_RE = re.compile(r"^svc_[a-z0-9_]+$", re.I)

# GRANTS THAT PREDATE THIS CHECK, ENUMERATED RATHER THAN EXCUSED BY A PATTERN.
#
# Forty. They are listed one by one, by (role, kind, object), because the list IS the debt: it is visible,
# countable, and a FORTY-FIRST still fails, which is the property that matters. Nothing here is excused for
# a migration added from now on.
#
# WHAT THEY COST, stated plainly so the list is not mistaken for a formality. After a factory-clean install
# these privileges do not exist, so on a REBUILT appliance:
#
#   svc_pmsd (8)   the whole roster-reconciliation runtime is unprivileged -- pms_roster_reconcile,
#                  pms_record_resync_coverage, the room-inventory and coverage reads. That subsystem is what
#                  PRs #121-#124 built to fix a reconciliation that refused forever; it would be dead again,
#                  and this time for a privilege reason rather than an evidence one.
#   svc_edged (25) the entire PMS operator surface -- roster reconciliation, connection settings, room
#                  inventory, review disposition -- plus the Phase-6 setting write.
#   svc_scd (4)    Phase-6 guest device self-service: the release operation, its policy, the product setting
#                  and the action audit.
#   svc_acctd (3)  the Phase-6 aggregate-online-time reads.
#
# They are NOT fixed here, deliberately. Each belongs in a specific per-service Gate-P file with its own
# reasoning, and several -- pms_dispose_snapshot_cases, pms_rebaseline_room_inventory -- are privileged
# operations where granting the wrong thing is worse than the gap. Bulk-adding forty grants that cannot each
# be verified on an appliance would be trading a known gap for an unknown one. The five this delivery DID
# fix (0080-0083, plus its own 0085) are in deploy/gatep/ with the evidence, and are the worked example.
#
# Removing a line from this list is how the debt gets paid. Adding one needs a reason in the commit.
PREDATING_THIS_CHECK = {
    ("svc_acctd", "table", "iam_v2.entitlement_devices"),
    ("svc_acctd", "table", "iam_v2.entitlement_termination_evidence"),
    ("svc_acctd", "table", "iam_v2.session_online_watermarks"),
    ("svc_edged", "function", "iam_v2.p6_set_guest_device_self_service"),
    ("svc_edged", "function", "iam_v2.pms_accept_startup_data_gap"),
    ("svc_edged", "function", "iam_v2.pms_connection_settings_get"),
    ("svc_edged", "function", "iam_v2.pms_connection_settings_set"),
    ("svc_edged", "function", "iam_v2.pms_dispose_snapshot_cases"),
    ("svc_edged", "function", "iam_v2.pms_integration_blockers"),
    ("svc_edged", "function", "iam_v2.pms_known_room_inventory"),
    ("svc_edged", "function", "iam_v2.pms_rebaseline_room_inventory"),
    ("svc_edged", "function", "iam_v2.pms_reconciliation_settings_get"),
    ("svc_edged", "function", "iam_v2.pms_reconciliation_settings_set"),
    ("svc_edged", "function", "iam_v2.pms_reoffer_stay_event"),
    ("svc_edged", "function", "iam_v2.pms_roster_of_generation"),
    ("svc_edged", "function", "iam_v2.pms_roster_reconcile"),
    ("svc_edged", "table", "iam_v2.appliance_product_setting_changes"),
    ("svc_edged", "table", "iam_v2.pms_case_resolutions"),
    ("svc_edged", "table", "iam_v2.pms_connection_settings"),
    ("svc_edged", "table", "iam_v2.pms_connection_settings_changes"),
    ("svc_edged", "table", "iam_v2.pms_reconciliation_settings"),
    ("svc_edged", "table", "iam_v2.pms_reconciliation_settings_changes"),
    ("svc_edged", "table", "iam_v2.pms_resync_coverage"),
    ("svc_edged", "table", "iam_v2.pms_room_inventory"),
    ("svc_edged", "table", "iam_v2.pms_room_inventory_changes"),
    ("svc_edged", "table", "iam_v2.pms_roster_reconciliation_runs"),
    ("svc_edged", "table", "iam_v2.pms_unanswered_review_events"),
    ("svc_edged", "table", "iam_v2.stay_event_reoffers"),
    ("svc_pmsd", "function", "iam_v2.pms_connection_settings_get"),
    ("svc_pmsd", "function", "iam_v2.pms_known_room_inventory"),
    ("svc_pmsd", "function", "iam_v2.pms_reconciliation_settings_get"),
    ("svc_pmsd", "function", "iam_v2.pms_record_resync_coverage"),
    ("svc_pmsd", "function", "iam_v2.pms_roster_of_generation"),
    ("svc_pmsd", "function", "iam_v2.pms_roster_reconcile"),
    ("svc_pmsd", "table", "iam_v2.pms_resync_coverage"),
    ("svc_pmsd", "table", "iam_v2.pms_room_inventory"),
    ("svc_scd", "function", "iam_v2.p6_guest_release_device"),
    ("svc_scd", "function", "iam_v2.p6_guest_release_device_policy"),
    ("svc_scd", "table", "iam_v2.appliance_product_settings"),
    ("svc_scd", "table", "iam_v2.guest_device_actions"),
}

_failures = []


def bad(msg, where=""):
    _failures.append(msg)
    print("  FAIL %s%s" % (msg, ("\n           " + where) if where else ""))


def ok(msg):
    print("  ok: %s" % msg)


def strip_sql_comments(text):
    """Remove -- line comments so a grant quoted in prose is not read as a grant."""
    out = []
    for line in text.splitlines():
        s = line.lstrip()
        if s.startswith("--"):
            continue
        # An inline trailing comment cannot contain a grant we care about; cut it.
        i = line.find("--")
        out.append(line[:i] if i >= 0 else line)
    return "\n".join(out)


def normalise_object(kind, obj):
    """Return (kind, name, columns) with the function signature reduced to a bare name."""
    obj = obj.strip().strip('"')
    cols = frozenset()
    m = re.match(r"^([A-Za-z0-9_.\"]+)\s*\((.*)\)$", obj, re.S)
    if m:
        name = m.group(1).strip().strip('"')
        inner = m.group(2)
        if (kind or "").strip().upper().startswith("FUNCTION"):
            # A signature, not a column list. Match by name: Gate-P spells the full argument list and
            # migrations often do not, and a name match is the honest strength of this check.
            return ("FUNCTION", name.lower(), frozenset())
        cols = frozenset(c.strip().strip('"').lower() for c in inner.split(",") if c.strip())
        return ("TABLE", name.lower(), cols)
    k = (kind or "").strip().upper()
    if k.startswith("FUNCTION"):
        return ("FUNCTION", obj.lower(), cols)
    if k.startswith("SCHEMA"):
        return ("SCHEMA", obj.lower(), cols)
    if k.startswith("SEQUENCE"):
        return ("SEQUENCE", obj.lower(), cols)
    if k.startswith("ALL"):
        return ("ALL_TABLES_IN_SCHEMA", obj.lower(), cols)
    return ("TABLE", obj.lower(), cols)


def collect(paths):
    """{(role, kind, object): [(privs, columns, source)]}"""
    found = {}
    for path in paths:
        try:
            with open(path, encoding="utf-8", errors="replace") as fh:
                text = strip_sql_comments(fh.read())
        except OSError:
            continue
        rel = os.path.relpath(path, ROOT).replace(os.sep, "/")
        for m in GRANT_RE.finditer(text):
            privs = " ".join(m.group("privs").split()).upper()
            # A column list can sit in the PRIVS half: GRANT SELECT (a, b) ON t TO r
            pcols = frozenset()
            pm = re.match(r"^([A-Z, ]+?)\s*\((.*)\)$", privs, re.S)
            if pm:
                privs = " ".join(pm.group(1).split())
                pcols = frozenset(c.strip().lower() for c in pm.group(2).split(",") if c.strip())
            kind, obj, ocols = normalise_object(m.group("kind"), m.group("object"))
            cols = pcols or ocols
            for role in m.group("roles").split(","):
                role = role.strip().strip('"')
                if not SERVICE_ROLE_RE.match(role):
                    continue
                found.setdefault((role.lower(), kind, obj), []).append((privs, cols, rel))
    return found


def sql_files(directory, suffix):
    out = []
    if not os.path.isdir(directory):
        return out
    for fn in sorted(os.listdir(directory)):
        if fn.endswith(suffix):
            out.append(os.path.join(directory, fn))
    return out


def main():
    print("== migration grant durability (migrations vs deploy/gatep) ==")
    mig = collect(sql_files(MIGRATIONS, ".up.sql"))
    gate = collect(sql_files(GATEP, ".sql"))

    if not mig:
        bad("no service-role GRANT was found in any migration; the check cannot be meaningful")
    if not gate:
        bad("no service-role GRANT was found in deploy/gatep; the check cannot be meaningful")

    checked = 0
    carried = []
    stale = set(PREDATING_THIS_CHECK)
    for key, entries in sorted(mig.items()):
        role, kind, obj = key
        checked += 1
        if key in gate:
            continue
        # A schema-wide grant in Gate-P covers a table grant on that schema's tables.
        schema = obj.split(".")[0] if "." in obj else ""
        if kind == "TABLE" and schema and (role, "ALL_TABLES_IN_SCHEMA", schema) in gate:
            continue
        if (role, kind.lower(), obj) in PREDATING_THIS_CHECK:
            carried.append((role, kind.lower(), obj))
            stale.discard((role, kind.lower(), obj))
            continue
        sources = sorted({src for _, _, src in entries})
        privs = sorted({p for p, _, _ in entries})
        bad(
            "%s is granted %s on %s %s by a migration, and deploy/gatep/ never grants it -- "
            "gatep-grants.sql revokes all privileges from the service roles and runs AFTER the migrations, "
            "so this privilege does not survive a factory-clean install"
            % (role, "/".join(privs), kind.lower(), obj),
            "granted in: %s" % ", ".join(sources),
        )

    # AN EXCEPTION THAT NO LONGER APPLIES IS ITSELF A FAILURE. If a grant was mirrored into Gate-P, or the
    # migration that made it was changed, the list must shrink -- otherwise it decays into a blanket excuse.
    for role, kind, obj in sorted(stale):
        bad("(%s, %s, %s) is listed as predating this check but no migration grants it any more, or it is "
            "now mirrored in deploy/gatep/; remove it from PREDATING_THIS_CHECK" % (role, kind, obj))

    if not _failures:
        if carried:
            ok("%d of %d service-role grants made by migrations are named in deploy/gatep/; %d listed as "
               "predating this check" % (checked - len(carried), checked, len(carried)))
        else:
            ok("all %d service-role grants made by migrations are also named in deploy/gatep/" % checked)

    print("=" * 50)
    if _failures:
        print("MIGRATION_GRANT_DURABILITY = FAIL (%d)" % len(_failures))
        return 1
    print("MIGRATION_GRANT_DURABILITY = PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
