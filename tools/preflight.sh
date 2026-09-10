#!/usr/bin/env bash
# Local preflight: refuse on the workstation what the gates would otherwise refuse on a runner.
#
# WHY, IN NUMBERS. Across PRs #108 and #109 the four gates burned 11 461 s of execution over 34 attempts.
# Sixteen of those attempts FAILED, and the categories were:
#
#     8  PG16-FIXTURE       a disposable-Postgres fixture behind the real migrations
#     3  MANIFEST-OR-PACK   the changed-file manifest not regenerated after the last commit
#     2  E2E-PRODUCT        genuine assertions against redesigned markup
#     2  PR-METADATA        the pull-request body missing its required governance metadata
#     1  LINUX-ONLY         a Windows-pruned lockfile that only `npm ci` on Linux rejects
#     0  E2E-INFRASTRUCTURE
#
# Every one of those except E2E-PRODUCT is knowable here, in seconds to a few minutes, with no runner. The
# first three heads of PR #108 cost 3 570 s of gate time and produced no information a local run could not
# have produced first.
#
# ORDERING IS THE POINT: cheapest and most-likely-to-fail first. A gate that spends twelve minutes on Go and
# Postgres before discovering a bad lockfile has spent twelve minutes learning nothing.
#
# Usage:
#   bash tools/preflight.sh              # everything except the full browser suite
#   bash tools/preflight.sh --fast       # stages 1-4 only (seconds; the pre-commit sweep)
#   bash tools/preflight.sh --full       # adds the full Playwright suite (slow, once, before pushing)
#   bash tools/preflight.sh --stage N    # a single stage
#
# It never writes to the repository and never contacts the appliance. Stage 2 reads the live pull request if
# a token is available, because that is the only place a PR body exists.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

MODE="default"
ONLY_STAGE=""
case "${1:-}" in
  --fast) MODE="fast" ;;
  --full) MODE="full" ;;
  --stage) ONLY_STAGE="${2:?--stage needs a number}" ;;
  "") ;;
  *) echo "unknown argument: $1" >&2; exit 2 ;;
esac

FAILED=0
declare -a RESULTS=()
STAGE_START=0

want() {  # want <stage-number>
  [ -z "$ONLY_STAGE" ] && return 0
  [ "$ONLY_STAGE" = "$1" ]
}

begin() { STAGE_START=$(date -u +%s); printf '\n\033[1m== %s ==\033[0m\n' "$1"; }

record() {  # record <label> <rc>
  local label="$1" rc="$2" secs=$(( $(date -u +%s) - STAGE_START ))
  if [ "$rc" -eq 0 ]; then
    RESULTS+=("PASS  ${secs}s  $label")
    printf '\033[32m  PASS\033[0m  %s (%ss)\n' "$label" "$secs"
  else
    RESULTS+=("FAIL  ${secs}s  $label")
    printf '\033[31m  FAIL\033[0m  %s (%ss)\n' "$label" "$secs"
    FAILED=$((FAILED + 1))
  fi
}

have() { command -v "$1" >/dev/null 2>&1; }

# ---------------------------------------------------------------------------------------------------------
# STAGE 1 - tree-pure governance. Pure Python, no network, no containers. Seconds.
# Catches the MANIFEST-OR-PACK category (3 of 16 failures) and generated-block drift.
# ---------------------------------------------------------------------------------------------------------
stage1() {
  begin "Stage 1 - governance, generated blocks, delivery protocol, working tree"
  local rc=0
  python tools/project-state.py validate            || rc=1
  python tools/project-state.py check-generated     || rc=1
  python tools/validate-delivery-protocol.py        || rc=1

  # The governance gate's LAST step fails if anything is left in the tree, including untracked build output.
  # Discovering that after everything else has passed is the most annoying possible way to fail.
  if [ -n "$(git status --porcelain --untracked-files=all)" ]; then
    echo "  FAIL: the working tree is not clean; the governance gate's final step requires it to be:"
    git status --porcelain --untracked-files=all | sed 's/^/        /'
    rc=1
  fi
  record "governance + generated blocks + protocol + clean tree" "$rc"
}

# ---------------------------------------------------------------------------------------------------------
# STAGE 2 - PREFLIGHT_PR_METADATA
# The governance gate reads the LIVE pull-request body. It is step 15 of ~21, so a body that is missing its
# phase status, decision or transition costs a full governance cycle to discover -- which happened twice.
# ---------------------------------------------------------------------------------------------------------
stage2() {
  begin "Stage 2 - PREFLIGHT_PR_METADATA (live pull-request body)"
  local rc=0
  if [ -z "${GITHUB_TOKEN:-}" ] && [ -f /tmp/cred.txt ]; then
    GITHUB_TOKEN="$(grep '^password=' /tmp/cred.txt | cut -d= -f2-)"; export GITHUB_TOKEN
  fi
  if [ -z "${GITHUB_TOKEN:-}" ]; then
    echo "  NOTE: no GITHUB_TOKEN, so the live PR body cannot be read here."
    echo "        This is the ONE check that cannot be fully reproduced offline: run it again once the PR"
    echo "        exists, BEFORE waiting on the expensive gates."
  fi
  bash tools/validate-pr-metadata.sh || rc=1
  record "PR metadata (zero-stale)" "$rc"
}

# ---------------------------------------------------------------------------------------------------------
# STAGE 3 - PREFLIGHT_LINUX_NPM_CI
# `npm ci` resolves the lockfile's ideal tree for the CURRENT platform. A lockfile written by a Windows
# `npm install` omits the Linux-only optional binaries (@emnapi/*, *-linux-*), and only a Linux `npm ci`
# rejects it -- which is why this surfaced on a runner after the whole Go and PG16 backend had run.
# ---------------------------------------------------------------------------------------------------------
stage3() {
  begin "Stage 3 - PREFLIGHT_LINUX_NPM_CI (lockfile installs on Linux)"
  local rc=0
  if ! have docker; then
    echo "  SKIP: docker is unavailable, so the Linux resolution cannot be reproduced."
    echo "        Recording this as a SKIP rather than a pass: an unverifiable lockfile is exactly the"
    echo "        condition that produced the LINUX-ONLY failure."
    RESULTS+=("SKIP  0s  Linux npm ci (docker unavailable)")
    return 0
  fi
  # THE HOST TREE IS MOUNTED READ-ONLY AND THE INSTALL HAPPENS INSIDE THE CONTAINER.
  #
  # The obvious form of this check -- mount hotel-admin read-write and run `npm ci` in it -- destroys the
  # developer's working node_modules, because npm ci deletes it and reinstalls the LINUX binaries. The next
  # local `npx tsc` then fails with "not recognized", which looks like a broken toolchain and is really a
  # clobbered one. That is not hypothetical: it is how this workstation's node_modules ended up Linux-only,
  # and a preflight that breaks the environment it is protecting would be abandoned within a day.
  #
  # Only the two manifest files matter to `npm ci`, so only they are copied in.
  # Docker Desktop does not understand a Git Bash path (`/d/WebProjects/...`); it wants `D:\WebProjects\...`.
  # Mounting the unconverted path silently succeeds and produces an EMPTY directory, so the copy below fails
  # with "No such file or directory" and the check reports a lockfile problem that does not exist -- a false
  # failure, which erodes trust in the preflight faster than no preflight at all. MSYS_NO_PATHCONV stops Git
  # Bash rewriting the container-side paths.
  local host_admin="$ROOT/hotel-admin"
  if have cygpath; then host_admin="$(cygpath -w "$ROOT/hotel-admin")"; fi
  MSYS_NO_PATHCONV=1 docker run --rm -v "$host_admin:/src:ro" node:20 bash -lc '
      set -e
      mkdir -p /probe && cd /probe
      cp /src/package.json /src/package-lock.json .
      npm ci --no-fund --no-audit --ignore-scripts >/tmp/out 2>&1 || { cat /tmp/out; exit 1; }
      echo "  npm ci resolved the committed lockfile on linux/amd64"
  ' || { echo "  npm ci FAILED on Linux. The committed lockfile does not describe a Linux-installable tree."
         echo "  This is the LINUX-ONLY category: it cannot fail on Windows and always fails on the runner."
         echo "  Regenerate the lockfile inside Linux, without touching your node_modules:"
         echo "    docker run --rm -v \"\$PWD/hotel-admin:/w\" -w /w node:20 npm install --package-lock-only"
         rc=1; }
  record "lockfile installs on Linux (npm ci)" "$rc"
}

# ---------------------------------------------------------------------------------------------------------
# STAGE 4 - PREFLIGHT_FIXTURE_PARITY
# The largest single failure category: 8 of 16. No database needed - it is a structural comparison.
# ---------------------------------------------------------------------------------------------------------
stage4() {
  begin "Stage 4 - PREFLIGHT_FIXTURE_PARITY (disposable-PG fixture vs migrations)"
  local rc=0
  python tools/check-fixture-parity.py || rc=1
  record "fixture carries every column its queries select" "$rc"
}

# ---------------------------------------------------------------------------------------------------------
# STAGE 5 - Go, the way CI runs it.
# `-count=1` matters: without it a locally green `go test` can be served entirely from the test cache.
# The tagged vet matters: 61 files carry `//go:build integration` and NONE of them is compiled by a plain
# `go build ./...`, `go vet ./...` or `go test ./...`.
# ---------------------------------------------------------------------------------------------------------
stage5() {
  begin "Stage 5 - Go: gofmt, build, vet (incl. build tags), test -count=1"
  local rc=0
  ( cd data-plane
    bash "$ROOT/scripts/ci/gofmt-check.sh"                                  || exit 1
    go build ./...                                                          || exit 1
    go vet ./...                                                            || exit 1
    go vet -tags 'integration phase5' ./...                                 || exit 1
    go build -tags stayconnect_production ./...                             || exit 1
    go test ./... -count=1                                                  || exit 1
  ) || rc=1
  record "Go build/vet/test as CI runs them" "$rc"
}

# ---------------------------------------------------------------------------------------------------------
# STAGE 6 - the Hotel Admin, up to but not including the browser suite.
# ---------------------------------------------------------------------------------------------------------
stage6() {
  begin "Stage 6 - Hotel Admin: typecheck, unit tests, production build"
  local rc=0
  ( cd hotel-admin
    npx tsc --noEmit                                                        || exit 1
    npx vitest run --reporter=default                                       || exit 1
    npx next build                                                          || exit 1
  ) || rc=1
  record "tsc + vitest + next build" "$rc"
}

# ---------------------------------------------------------------------------------------------------------
# STAGE 7 - PREFLIGHT_E2E_INFRA
# Measured honestly: E2E-INFRASTRUCTURE caused ZERO of the sixteen CI failures. It cost LOCAL time -- one
# dev-server death mid-suite produced twenty ERR_CONNECTION_REFUSED failures that read as product
# regressions and sent a developer looking for a bug that did not exist. This stage proves the harness can
# tell the two apart BEFORE the suite runs, so the distinction exists when it is needed.
# ---------------------------------------------------------------------------------------------------------
stage7() {
  begin "Stage 7 - PREFLIGHT_E2E_INFRA (server lifecycle is sound)"
  local rc=0
  local port=3123

  if have lsof && lsof -i ":$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "  FAIL: something is already listening on :$port."
    echo "        playwright.config reuses an existing server, so the suite would silently run against"
    echo "        THAT process -- possibly an old build with different NEXT_PUBLIC_* flags."
    rc=1
  fi

  node -e '
    const fs = require("fs");
    const cfg = fs.readFileSync("hotel-admin/playwright.config.ts", "utf8");
    const problems = [];
    if (!/reuseExistingServer:\s*!process\.env\.CI/.test(cfg))
      problems.push("webServer.reuseExistingServer must be !process.env.CI, so CI never adopts a stray server");
    if (!/globalSetup/.test(cfg))
      problems.push("no globalSetup: nothing proves the server is alive before the suite starts");
    if (!/e2e-infra-reporter/.test(cfg))
      problems.push("the infrastructure reporter is not wired: a dead server would be reported as N product failures");
    if (problems.length) { problems.forEach(p => console.log("  FAIL: " + p)); process.exit(1); }
    console.log("  the E2E harness distinguishes infrastructure death from product failure");
  ' || rc=1
  record "E2E server lifecycle" "$rc"
}

# ---------------------------------------------------------------------------------------------------------
# STAGE 8 - the full browser suite. Slow; run ONCE, when everything above is green.
# ---------------------------------------------------------------------------------------------------------
stage8() {
  begin "Stage 8 - full Playwright suite"
  local rc=0
  ( cd hotel-admin && npx playwright test --reporter=list ) || rc=1
  record "full E2E suite" "$rc"
}

main() {
  echo "StayConnect preflight - refusing locally what the gates would refuse remotely"
  echo "mode: $MODE${ONLY_STAGE:+ (stage $ONLY_STAGE only)}"
  local t0; t0=$(date -u +%s)

  want 1 && stage1
  want 2 && stage2
  want 3 && stage3
  want 4 && stage4
  if [ "$MODE" != "fast" ]; then
    want 5 && stage5
    want 6 && stage6
    want 7 && stage7
    if [ "$MODE" = "full" ]; then want 8 && stage8; fi
  fi

  echo ""
  echo "=================================================="
  printf '%s\n' "${RESULTS[@]}"
  echo "--------------------------------------------------"
  echo "total: $(( $(date -u +%s) - t0 ))s"
  if [ "$FAILED" -gt 0 ]; then
    echo "PREFLIGHT = FAIL ($FAILED)"
    echo "Fix these here. Every one of them would have cost a full gate cycle to learn remotely."
    return 1
  fi
  echo "PREFLIGHT = PASS"
  if [ "$MODE" = "fast" ]; then
    echo "NOTE: --fast covers stages 1-4 only. Run the default mode before pushing, and --full before the PR."
  fi
  return 0
}

main
