#!/usr/bin/env bash
# THE LEDGER PACKAGE ON LINUX (GDPR final mission).
#
# internal/ledger reads the kernel's mount table and block-device tree to decide whether a folder is genuinely separate from the
# appliance's own disk (independence.go). Those paths exist only on Linux, and some tests need real symlinks, so they are skipped on a
# developer's Windows machine. This runs the whole package in a Go container on Linux.
#
#   scripts/ledger-linux-checks.sh
#
# Needs docker. Reads the repository read-only; the module cache lives in the container.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMG="${GO_IMAGE:-golang:1.25}"
HOSTROOT="$(cygpath -m "$ROOT" 2>/dev/null || echo "$ROOT")"   # Git Bash on Windows needs the Windows spelling for -v
MSYS_NO_PATHCONV=1 exec docker run --rm -v "$HOSTROOT:/src:ro" -e GOFLAGS=-mod=mod -v ledger-gocache:/tmp/gocache -v ledger-gomod:/tmp/gomod -e GOCACHE=/tmp/gocache -e GOMODCACHE=/tmp/gomod -w /src/data-plane "$IMG" \
  sh -c 'cp -r /src /work && cd /work/data-plane && go test -count=1 -v ./internal/ledger 2>&1 | grep -E "^(--- |FAIL|ok|PASS|panic)|_test.go:[0-9]+:"'
