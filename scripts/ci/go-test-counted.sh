#!/usr/bin/env bash
# Run `go test` while producing a truthful machine count as a by-product of the SAME run
# that gates. Never a second, weaker run.
#
# Usage (from data-plane/):  go-test-counted.sh <counts-name> <go test args...>
#
# It streams `go test <args> -json` through gojson_summary.py, which writes
# $EVID/counts/<counts-name>.json and prints a one-line human summary. pipefail + the
# summary's own exit code mean any test or build failure fails this script.
set -uo pipefail
name="${1:?counts name required}"; shift
: "${EVID:?EVID must be set}"
mkdir -p "$EVID/counts"
PY="$(command -v python3 || command -v python)"
set -o pipefail
go test "$@" -json | "$PY" "$GITHUB_WORKSPACE/scripts/ci/gojson_summary.py" "$EVID/counts/$name"
