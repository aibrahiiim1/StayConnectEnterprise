#!/usr/bin/env python3
"""NO CONTROL CHARACTERS IN THE GOVERNANCE TOOLING.

A shell heredoc silently turned the two-character escape `\\b` into a literal 0x08 BACKSPACE byte inside
two regexes in tools/validate-current-state-parity.py. The file looked correct in every normal view - the
byte is invisible - and both alternatives it guarded became unmatchable:

  * `\\bNOT accepted\\b|\\bnot yet accepted\\b` in the acceptance-parity rule stopped matching anything, and
  * the phase-status rule could never fire, so it passed the very contradiction it was written for.

Nothing caught it because a broken alternative inside a working regex still compiles, still runs, and still
reports PASS. Only the bytes give it away.

Usage:  python tools/tests/tooling/run_control_chars.py
Exit:   0 = clean, 1 = a control character is present.
"""
import io
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))

# Tab, newline and carriage return are legitimate. Everything else below 0x20 is not.
ALLOWED = {0x09, 0x0A, 0x0D}

SCANNED_DIRS = ["tools", os.path.join("scripts", "ci")]
SUFFIXES = (".py", ".sh")


def main():
    offenders = []
    scanned = 0
    for rel_dir in SCANNED_DIRS:
        base = os.path.join(ROOT, rel_dir)
        for dirpath, _dirs, files in os.walk(base):
            if "__pycache__" in dirpath:
                continue
            for fn in files:
                if not fn.endswith(SUFFIXES):
                    continue
                path = os.path.join(dirpath, fn)
                rel = os.path.relpath(path, ROOT).replace(os.sep, "/")
                scanned += 1
                for offset, byte in enumerate(io.open(path, "rb").read()):
                    if byte < 0x20 and byte not in ALLOWED:
                        offenders.append((rel, offset, byte))
                        break

    for rel, offset, byte in offenders:
        print("  [FAIL] %s carries control byte 0x%02x at offset %d" % (rel, byte, offset))
    print("=" * 60)
    print("TOOLING_CONTROL_CHARS: scanned=%d offenders=%d -> %s"
          % (scanned, len(offenders), "PASS" if not offenders else "FAIL"))
    return 1 if offenders else 0


if __name__ == "__main__":
    sys.exit(main())
