#!/usr/bin/env bash
# gofmt over the complete Phase-3 Go surface: every internal package (which includes
# pmsd, scd's libraries, portald/edged/acctd/netd's libraries and the controlled-writer
# guard) plus the six Phase-3 command roots. Run from data-plane/.
#
# The one module file deliberately excluded is cmd/scd-enroll-test/main.go — a
# pre-existing, non-Phase-3 standalone test harness. Its formatting is out of this
# gate's scope, and including it would fail a Phase-3 gate on unrelated code.
set -uo pipefail
targets=(internal/ cmd/pmsd cmd/scd cmd/portald cmd/edged cmd/acctd cmd/netd)
unformatted="$(gofmt -l "${targets[@]}")"
if [ -n "$unformatted" ]; then
  echo "gofmt needs to be run on:"; echo "$unformatted"; exit 1
fi
echo "gofmt clean over: ${targets[*]}"
