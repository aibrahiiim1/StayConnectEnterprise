#!/usr/bin/env bash
# Self-test for scripts/ci/go-checks.sh (audit P0-1): it passes a clean module, and fails a module with a planted
# go vet error and a module that reaches a known-vulnerable library version.
# Needs go and network access to the module proxy and the vulnerability database (GOCHECKS_VULN_DB may point at
# a mirror). Touches nothing outside its temp dir.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
export GOCHECKS_TOOLS_DIR="$T/tools"
fail=0
ok()  { echo "PASS: $*"; }
bad() { echo "FAIL: $*"; fail=1; }

mkmod() { # mkmod <dir> <main.go body>
  mkdir -p "$1"
  printf 'module example.test/m\n\ngo 1.25.0\n' > "$1/go.mod"
  printf '%s\n' "$2" > "$1/main.go"
}

mkmod "$T/clean" 'package main

import "fmt"

func main() { fmt.Println("ok") }'
if bash "$HERE/go-checks.sh" "$T/clean" > "$T/clean.log" 2>&1; then ok "a clean module passes"
else bad "a clean module failed"; tail -20 "$T/clean.log"; fi

mkmod "$T/vet" 'package main

import "fmt"

func main() { fmt.Printf("%d\n", "not a number") }'
if GOCHECKS_SKIP="govulncheck" bash "$HERE/go-checks.sh" "$T/vet" > "$T/vet.log" 2>&1; then bad "a planted go vet error passed"
elif grep -q "FAILED: vet" "$T/vet.log"; then ok "a planted go vet error fails the check"
else bad "the vet module failed for another reason"; tail -20 "$T/vet.log"; fi

mkmod "$T/vuln" 'package main

import (
	"fmt"

	"golang.org/x/text/unicode/norm"
)

func main() { fmt.Println(norm.NFC.String("x")) }'
(cd "$T/vuln" && GOFLAGS=-mod=mod GOTOOLCHAIN="${GOCHECKS_TOOLCHAIN:-go1.25.12}" go get golang.org/x/text@v0.35.0 >/dev/null 2>&1)
if GOCHECKS_SKIP="vet staticcheck" bash "$HERE/go-checks.sh" "$T/vuln" > "$T/vuln.log" 2>&1; then
  bad "a module reaching golang.org/x/text v0.35.0 passed govulncheck"
elif grep -q "FAILED: govulncheck" "$T/vuln.log" && grep -q "golang.org/x/text@v0.35.0" "$T/vuln.log"; then
  ok "a module reaching a known-vulnerable library version fails the check"
else bad "the vulnerable module failed for another reason"; tail -20 "$T/vuln.log"; fi

exit "$fail"
