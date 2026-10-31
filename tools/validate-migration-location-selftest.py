#!/usr/bin/env python3
"""Self-test of tools/validate-migration-location.py: reserved numbers, pinned identities, contiguity.

Builds a throwaway tree under a temp directory (MIGRATION_LOCATION_ROOT) and runs the real validator against it, so each
rule is proven to FAIL on the defect it exists for and to PASS on the legitimate shape. No database, no network.
"""
import hashlib
import os
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
VALIDATOR = os.path.join(HERE, "validate-migration-location.py")
failures = []


def sha(s):
    return hashlib.sha256(s.encode()).hexdigest()


def build(root, numbers, reserved="0003-0004", pin=True):
    d = os.path.join(root, "data-plane", "migrations")
    os.makedirs(d)
    os.makedirs(os.path.join(root, "control-plane", "migrations"))
    ids = []
    for n in numbers:
        stem = "%04d_m%d" % (n, n)
        up = "-- up %d\n" % n
        open(os.path.join(d, stem + ".up.sql"), "w", newline="\n").write(up)
        open(os.path.join(d, stem + ".down.sql"), "w", newline="\n").write("-- down\n")
        ids.append("%s %s" % (stem, sha(up)))
    if reserved:
        open(os.path.join(d, "RESERVED_NUMBERS"), "w").write("%s   test hole\n" % reserved)
    if pin:
        open(os.path.join(d, "IDENTITIES"), "w").write("\n".join(ids) + "\n")
    return d


def run(root):
    env = dict(os.environ, MIGRATION_LOCATION_ROOT=root)
    p = subprocess.run([sys.executable, VALIDATOR], env=env, capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr


def case(name, want_rc, must_contain, mutate=None, **kw):
    root = tempfile.mkdtemp(prefix="migloc-")
    try:
        d = build(root, kw.pop("numbers", [1, 2, 5, 6]), **kw)
        if mutate:
            mutate(d)
        rc, out = run(root)
        ok = rc == want_rc and all(s in out for s in must_contain)
        print("%s %s" % ("ok  " if ok else "FAIL", name))
        if not ok:
            failures.append(name)
            print(out)
    finally:
        shutil.rmtree(root, ignore_errors=True)


def add(d, name, text="-- x\n"):
    open(os.path.join(d, name), "w", newline="\n").write(text)


case("the declared hole is accepted (3-4 reserved, 1,2,5,6 present)", 0, ["MIGRATION_LOCATION = PASS"])
case("an undeclared gap is still refused", 1, ["migration-contiguity"], reserved="")
case("a NEW file in a reserved range is refused (0003_x.up.sql)", 1, ["migration-reserved", "0003"],
     mutate=lambda d: (add(d, "0003_x.up.sql"), add(d, "0003_x.down.sql")))
case("a stray down in a reserved range is refused (0004)", 1, ["migration-reserved", "0004"],
     mutate=lambda d: add(d, "0004_x.down.sql"))
case("a malformed declaration is refused", 1, ["migration-reserved-declaration"], reserved="0009-0001")
case("editing a shipped migration is refused (hash pinned)", 1, ["changed after it shipped"],
     mutate=lambda d: add(d, "0002_m2.up.sql", "-- edited\n"))
case("renaming a shipped migration is refused", 1, ["migration-identity"],
     mutate=lambda d: (os.rename(os.path.join(d, "0002_m2.up.sql"), os.path.join(d, "0002_other.up.sql")),
                       os.rename(os.path.join(d, "0002_m2.down.sql"), os.path.join(d, "0002_other.down.sql"))))
case("a new unpinned migration ABOVE the highest pinned is allowed", 0, ["MIGRATION_LOCATION = PASS"],
     mutate=lambda d: (add(d, "0007_new.up.sql"), add(d, "0007_new.down.sql")))
def drop_pin_0002(d):
    path = os.path.join(d, "IDENTITIES")
    keep = [l for l in open(path).read().splitlines() if not l.startswith("0002_")]
    open(path, "w").write(chr(10).join(keep) + chr(10))


case("an unpinned migration BELOW the highest pinned is refused", 1, ["not pinned"], numbers=[1, 2, 5, 6, 7],
     mutate=drop_pin_0002)

# The real tree, unmodified.
rc, out = run(os.path.dirname(HERE))
print("%s the repository tree passes" % ("ok  " if rc == 0 else "FAIL"))
if rc != 0:
    failures.append("repository tree")
    print(out)

print("=" * 50)
if failures:
    print("MIGRATION_LOCATION_SELFTEST = FAIL (%s)" % ", ".join(failures))
    sys.exit(1)
print("MIGRATION_LOCATION_SELFTEST = PASS")
