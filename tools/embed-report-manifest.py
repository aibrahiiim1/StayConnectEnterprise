#!/usr/bin/env python3
"""Embed the current generated change-manifest into the Final Report's §10, verbatim.

The Final Report §10 ("Complete generated changed-file manifest") must carry the EXACT current manifest, not a
frozen copy from an older delivery. This replaces the body of §10 with the current
docs/manifests/Phase3-change-manifest.md content, so the report's embedded manifest and the standalone
generated manifest describe the identical path set (the evidence collector's manifest-parity check verifies
this).

Run it during delivery, AFTER generating the manifest and BEFORE the final regenerate+packs.
"""
import io
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
REPORT = os.path.join(ROOT, "docs/reports/StayConnect-IAM-Phase3-Final-Report.md")
MANIFEST = os.path.join(ROOT, "docs/manifests/Phase3-change-manifest.md")

HEADING = "## 10. Complete generated changed-file manifest"


def main() -> int:
    report = io.open(REPORT, encoding="utf-8").read()
    manifest = io.open(MANIFEST, encoding="utf-8").read().strip("\n")

    start = report.find(HEADING)
    if start < 0:
        sys.stderr.write("§10 heading not found in the report\n")
        return 1
    nxt = report.find("\n## 11.", start)
    if nxt < 0:
        sys.stderr.write("§11 heading not found after §10\n")
        return 1

    new_section = (
        HEADING + "\n\n"
        "> Embedded verbatim from `docs/manifests/Phase3-change-manifest.md` at delivery time. The evidence\n"
        "> artifact's manifest-parity check confirms this equals the standalone generated manifest.\n\n"
        + manifest + "\n"
    )
    updated = report[:start] + new_section + report[nxt + 1:]
    io.open(REPORT, "w", encoding="utf-8", newline="\n").write(updated)
    sys.stderr.write("embedded the current manifest into the Final Report §10\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
