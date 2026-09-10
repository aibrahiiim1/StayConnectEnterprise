#!/usr/bin/env python3
"""Compare the disposable-Postgres platform fixture against the real migrations it stands in for.

WHY THIS EXISTS -- and it is the single most expensive gap this project has measured.

`iam_v2_scratch/00_platform_fixture.sql` is a hand-maintained stand-in for the `public.*` tables a real
appliance gets from `data-plane/migrations/`. The integration suites run against the fixture. When the fixture
falls behind a migration, a query that SELECTs the missing column does not fail loudly -- in Go it fails at
`rows.Scan`, the row is skipped, and the caller receives an EMPTY RESULT SET. An empty result reads as "this
site has no guest networks", which is a confident, wrong answer that nothing in the response contradicts.

That exact fault reached GitHub twice in one delivery, from two different missing objects, and accounted for
eight of the sixteen failed gate attempts in PRs #108/#109 -- more than every other category combined. The
fixture's own comments record it happening a third time, for `public.operators`. Nothing compared the two
files, so each recurrence was rediscovered by hand, deep inside a gate, minutes after a runner started.

WHAT IT CHECKS, and why it is narrower than "the two files must match".

The fixture is DELIBERATELY reduced -- its own header says so, and it is meant to carry only what the foreign
keys and the gates actually use. So raw column drift is the normal, intended state, and failing on it would
red-flag a tree that is perfectly green. The real invariant is narrower and is exactly the one that broke:

  a column that a query ACTUALLY SELECTS must exist in the fixture that query runs against.

So this reads the SQL string literals in `data-plane/cmd/**/*.go`, finds the ones that name a `public.` table
the fixture defines, resolves the table's alias inside that statement, and collects every `alias.column` the
statement references. Those columns -- and only those -- are required to exist in the fixture. A missing TABLE
that integration tests reference is always an error, because a JOIN against it cannot degrade quietly.

Everything else is reported as informational drift, never as a failure.

WHAT IT DOES NOT CHECK. Types, constraints, defaults, indexes and triggers. This is a cheap structural check
that runs in milliseconds with no database; the gates still do the real thing. It is aimed squarely at the
failure that actually recurs -- a column or table that silently is not there.
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIXTURE = os.path.join(ROOT, "iam_v2_scratch", "00_platform_fixture.sql")
MIGRATIONS = os.path.join(ROOT, "data-plane", "migrations")

# `public.` is optional in the migrations and usually written bare.
#
# The body is found by BALANCING PARENTHESES, not by a regex. A regex that ended the body at a closing paren
# on its own line silently matched NOTHING in the fixture -- whose tables close inline, e.g.
# `id uuid PRIMARY KEY DEFAULT gen_random_uuid());` -- and a parser that finds zero tables reports zero
# mismatches, which is the same false all-clear this checker exists to prevent.
CREATE_HEAD_RE = re.compile(
    r"CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?([a-z_][a-z0-9_]*)\s*\(",
    re.IGNORECASE)
ALTER_RE = re.compile(
    r"ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?(?:public\.)?([a-z_][a-z0-9_]*)\s+(.*?);",
    re.IGNORECASE | re.DOTALL)
ADD_COL_RE = re.compile(
    r"ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_][a-z0-9_]*)", re.IGNORECASE)
DROP_COL_RE = re.compile(
    r"DROP\s+COLUMN\s+(?:IF\s+EXISTS\s+)?([a-z_][a-z0-9_]*)", re.IGNORECASE)
DROP_TABLE_RE = re.compile(
    r"DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:public\.)?([a-z_][a-z0-9_]*)", re.IGNORECASE)

# Lines inside a CREATE TABLE body that introduce a constraint rather than a column.
NOT_A_COLUMN = re.compile(
    r"^\s*(PRIMARY\s+KEY|FOREIGN\s+KEY|UNIQUE|CHECK|CONSTRAINT|EXCLUDE|LIKE)\b", re.IGNORECASE)


def strip_sql_comments(text):
    text = re.sub(r"/\*.*?\*/", " ", text, flags=re.DOTALL)
    return re.sub(r"--[^\n]*", "", text)


def columns_from_body(body):
    """Split a CREATE TABLE body into column names, ignoring table-level constraints."""
    cols, depth, current = [], 0, []
    for ch in body:
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        if ch == "," and depth == 0:
            cols.append("".join(current))
            current = []
        else:
            current.append(ch)
    cols.append("".join(current))

    out = []
    for frag in cols:
        frag = frag.strip()
        if not frag or NOT_A_COLUMN.match(frag):
            continue
        m = re.match(r"([a-z_][a-z0-9_]*)", frag, re.IGNORECASE)
        if m:
            out.append(m.group(1).lower())
    return out


def parse(text):
    """Return {table: set(columns)} after replaying CREATE / ADD COLUMN / DROP in file order."""
    text = strip_sql_comments(text)
    tables = {}
    for m in CREATE_HEAD_RE.finditer(text):
        start = m.end()          # first char after the opening paren
        depth, i = 1, start
        while i < len(text) and depth:
            if text[i] == "(":
                depth += 1
            elif text[i] == ")":
                depth -= 1
            i += 1
        if depth:
            continue             # unbalanced; not something to guess at
        body = text[start:i - 1]
        tables.setdefault(m.group(1).lower(), set()).update(columns_from_body(body))
    for m in ALTER_RE.finditer(text):
        table, rest = m.group(1).lower(), m.group(2)
        if table not in tables:
            continue
        for c in ADD_COL_RE.finditer(rest):
            tables[table].add(c.group(1).lower())
        for c in DROP_COL_RE.finditer(rest):
            tables[table].discard(c.group(1).lower())
    for m in DROP_TABLE_RE.finditer(text):
        tables.pop(m.group(1).lower(), None)
    return tables


def columns_selected_in_go(fixture_tables):
    """Find the columns Go SQL literals actually reference on each fixture table.

    Returns ({table: set(columns)}, set(tables referenced by *_test.go)).

    Only backtick-quoted Go raw strings are considered: that is how every multi-line query in this codebase is
    written, and it avoids trying to reassemble concatenated fragments. Within one statement the table's alias
    is resolved from `public.<table> <alias>` (or `... AS <alias>`), then every `<alias>.<column>` in that same
    statement is collected. Unaliased bare column names are deliberately ignored -- they cannot be attributed
    to a table without a real parser, and guessing would produce exactly the false alarms this check avoids.
    """
    selected = {}
    referenced = set()
    self_provisioned = set()
    cmd_root = os.path.join(ROOT, "data-plane", "cmd")
    if not os.path.isdir(cmd_root):
        return selected, referenced, self_provisioned

    for dirpath, _dirs, files in os.walk(cmd_root):
        for f in files:
            if not f.endswith(".go"):
                continue
            path = os.path.join(dirpath, f)
            with open(path, encoding="utf-8", errors="replace") as fh:
                src = fh.read()
            if f.endswith("_test.go"):
                referenced.update(m.lower() for m in re.findall(r"public\.([a-z_][a-z0-9_]*)", src))
                # A suite that CREATEs its own stand-in does not depend on the fixture for that table, and
                # flagging it would be a false alarm on a tree that is green -- which is the fastest way to
                # get a check ignored, and an ignored check protects nothing.
                self_provisioned.update(
                    m.lower() for m in re.findall(
                        r"CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?public\.([a-z_][a-z0-9_]*)",
                        src, re.IGNORECASE))
            for literal in re.findall(r"`([^`]*)`", src, re.DOTALL):
                if "public." not in literal:
                    continue
                # A table JOINED INTO a query that already touches a fixture-modelled table must itself be
                # in the fixture: that whole statement runs against the fixture during the integration
                # suites. This is what makes a missing TABLE visible. Restricting it to statements that
                # already involve the fixture keeps out the many public tables other daemons query and no
                # fixture-backed test ever executes.
                tables_here = {t.lower() for t in re.findall(
                    r"public\.([a-z_][a-z0-9_]*)", literal, re.IGNORECASE)}
                if tables_here & fixture_tables:
                    referenced.update(tables_here)
                for table in fixture_tables:
                    for am in re.finditer(
                            r"public\.%s\s+(?:AS\s+)?([a-z_][a-z0-9_]*)" % re.escape(table),
                            literal, re.IGNORECASE):
                        alias = am.group(1).lower()
                        if alias in ("on", "where", "set", "using", "group", "order", "left", "join",
                                     "inner", "right", "full", "cross", "as", "and", "or"):
                            continue
                        cols = {c.lower() for c in re.findall(
                            r"\b%s\.([a-z_][a-z0-9_]*)" % re.escape(alias), literal, re.IGNORECASE)}
                        if cols:
                            selected.setdefault(table, set()).update(cols)
    return selected, referenced - self_provisioned


def migration_files():
    if not os.path.isdir(MIGRATIONS):
        return []
    return [os.path.join(MIGRATIONS, f)
            for f in sorted(os.listdir(MIGRATIONS))
            if f.endswith(".up.sql")]


def main():
    if not os.path.isfile(FIXTURE):
        print("FAIL: %s is missing" % FIXTURE)
        return 1

    with open(FIXTURE, encoding="utf-8") as fh:
        fixture = parse(fh.read())

    real_text = []
    for p in migration_files():
        with open(p, encoding="utf-8") as fh:
            real_text.append(fh.read())
    if not real_text:
        print("FAIL: no .up.sql migrations found under data-plane/migrations")
        return 1
    real = parse("\n".join(real_text))

    problems = 0
    print("== disposable-Postgres fixture vs data-plane/migrations ==")
    print("   fixture tables: %d   migration tables: %d" % (len(fixture), len(real)))

    selected, referenced = columns_selected_in_go(set(fixture))[:2]

    for table in sorted(fixture):
        if table not in real:
            continue  # a helper table that exists only for the fixture is legitimate
        needed = selected.get(table, set())
        missing_needed = sorted((needed & real[table]) - fixture[table])
        if missing_needed:
            problems += 1
            print("  FAIL: public.%s is missing %d column(s) that a query actually SELECTs: %s"
                  % (table, len(missing_needed), ", ".join(missing_needed)))
            print("        A SELECT of these returns no error -- Scan fails, the row is dropped, and the")
            print("        caller sees an EMPTY RESULT that reads as 'this site has none'.")
        else:
            drift = len(real[table] - fixture[table])
            note = "" if not drift else "  (%d further migration column(s) intentionally not modelled)" % drift
            print("  ok: public.%s carries every column its queries select%s" % (table, note))

    # A table referenced by the integration suites but absent from the fixture fails the same silent way,
    # and cannot be excused as "intentionally reduced" -- a JOIN against a missing table cannot degrade.
    for table in sorted(referenced):
        if table in real and table not in fixture:
            problems += 1
            print("  FAIL: integration tests reference public.%s, the migrations create it, and the fixture "
                  "does not define it at all" % table)
            print("        A JOIN against it errors and the section returns [] -- indistinguishable from "
                  "'this site is empty'.")

    print("=" * 50)
    if problems:
        print("FIXTURE_PARITY = FAIL (%d)" % problems)
        print("Fix %s to match the migrations, then re-run." % os.path.relpath(FIXTURE, ROOT))
        return 1
    print("FIXTURE_PARITY = PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
