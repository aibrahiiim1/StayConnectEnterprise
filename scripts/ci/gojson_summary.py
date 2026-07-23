#!/usr/bin/env python3
"""Summarise a `go test -json` stream into truthful counts, for the Phase-3 evidence.

Usage:  go test ./... -count=1 -json | python3 scripts/ci/gojson_summary.py <counts-prefix>

Reads the JSON event stream on stdin. It writes a machine-readable count file to
"<counts-prefix>.json" ({pass, fail, skip, packages_ok, packages_failed}) and prints a
one-line human summary to stdout so the wrapping step's log stays readable. It exits
non-zero if any test or package failed, so it is a drop-in for the bare `go test` gate
— the counts are a by-product of the same run, never a second, weaker one.
"""
import json
import sys


def main() -> int:
    prefix = sys.argv[1] if len(sys.argv) > 1 else "go"
    tests = {"pass": 0, "fail": 0, "skip": 0}
    pkgs = {"pass": 0, "fail": 0}
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            # `go test` can interleave non-JSON build errors; surface them and keep going.
            sys.stderr.write(line + "\n")
            continue
        action = ev.get("Action")
        if ev.get("Test"):
            if action in tests:
                tests[action] += 1
        else:  # package-level result
            if action == "pass":
                pkgs["pass"] += 1
            elif action == "fail":
                pkgs["fail"] += 1
    out = {
        "pass": tests["pass"],
        "fail": tests["fail"],
        "skip": tests["skip"],
        "packages_ok": pkgs["pass"],
        "packages_failed": pkgs["fail"],
    }
    with open(prefix + ".json", "w", encoding="utf-8", newline="\n") as f:
        json.dump(out, f, indent=2)
        f.write("\n")
    print(
        f"go tests: {out['pass']} passed, {out['skip']} skipped, {out['fail']} failed "
        f"across {out['packages_ok'] + out['packages_failed']} packages "
        f"({out['packages_failed']} failed)"
    )
    return 1 if (out["fail"] or out["packages_failed"]) else 0


if __name__ == "__main__":
    sys.exit(main())
