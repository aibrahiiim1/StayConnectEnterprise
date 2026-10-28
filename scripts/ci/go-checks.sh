#!/usr/bin/env bash
# On-demand static-analysis and vulnerability check for the Go modules (audit P0-1).
#
# For each module: go vet ./..., staticcheck ./..., govulncheck ./... . Exit 0 only when all three are clean
# for every module. It is a check to run by hand (or from FULL CHECK), never a required merge check (D42).
#
# The tools are built at pinned versions into a private directory (nothing installed globally). The analysis
# itself runs with the toolchain the release images build with (golang:1.25.12), so govulncheck reports what
# ships, not what happens to be installed.
#
# Usage: scripts/ci/go-checks.sh [module-dir ...]        (default: data-plane control-plane license)
# Environment:
#   GOCHECKS_TOOLCHAIN     toolchain for the analysis       (default go1.25.12; keep equal to GO_IMAGE)
#   GOCHECKS_TOOLS_DIR     where the pinned tools are built (default: a temp dir, removed on exit)
#   GOCHECKS_VULN_DB       vulnerability database URL       (default: govulncheck's own, https://vuln.go.dev)
#   GOCHECKS_SKIP          space-separated subset of "vet staticcheck govulncheck" to skip
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATICCHECK_VERSION="2026.2.2"
GOVULNCHECK_VERSION="v1.9.0"
TOOLCHAIN="${GOCHECKS_TOOLCHAIN:-go1.25.12}"
SKIP=" ${GOCHECKS_SKIP:-} "

[ "$#" -gt 0 ] || set -- "$ROOT/data-plane" "$ROOT/control-plane" "$ROOT/license"
command -v go >/dev/null || { echo "go-checks: go not found" >&2; exit 2; }

TOOLS="${GOCHECKS_TOOLS_DIR:-}"
if [ -z "$TOOLS" ]; then TOOLS="$(mktemp -d)"; trap 'rm -rf "$TOOLS"' EXIT; fi
mkdir -p "$TOOLS"
build_tool() { # build_tool <name> <package@version>; the tools may need a newer Go than the analysed code
  [ -x "$TOOLS/$1" ] && return 0
  (cd "$TOOLS" && GOBIN="$TOOLS" GOTOOLCHAIN=auto GOFLAGS= go install "$2") \
    || { echo "go-checks: cannot build $2" >&2; exit 2; }
}
case "$SKIP" in *" staticcheck "*) ;; *) build_tool staticcheck "honnef.co/go/tools/cmd/staticcheck@$STATICCHECK_VERSION" ;; esac
case "$SKIP" in *" govulncheck "*) ;; *) build_tool govulncheck "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION" ;; esac

vulndb=()
[ -n "${GOCHECKS_VULN_DB:-}" ] && vulndb=(-db "$GOCHECKS_VULN_DB")

failed=0
step() { # step <name> <module> <command...>
  local name="$1" mod="$2"; shift 2
  case "$SKIP" in *" $name "*) echo "-- $name $(basename "$mod"): skipped"; return 0 ;; esac
  echo "-- $name $(basename "$mod")"
  if (cd "$mod" && GOTOOLCHAIN="$TOOLCHAIN" GOOS="${GOOS:-linux}" "$@"); then
    echo "   ok"
  else
    echo "   FAILED: $name in $mod"; failed=1
  fi
}

for mod in "$@"; do
  [ -f "$mod/go.mod" ] || { echo "go-checks: $mod has no go.mod" >&2; exit 2; }
  step vet "$mod" go vet ./...
  step staticcheck "$mod" "$TOOLS/staticcheck" ./...
  step govulncheck "$mod" "$TOOLS/govulncheck" "${vulndb[@]}" ./...
done

if [ "$failed" = 0 ]; then echo "GO_CHECKS = PASS"; exit 0; fi
echo "GO_CHECKS = FAIL"; exit 1
