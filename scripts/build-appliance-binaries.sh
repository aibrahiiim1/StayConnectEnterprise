#!/usr/bin/env bash
# THE APPLIANCE BUILD, WRITTEN DOWN ONCE.
#
# Every service binary on an appliance is produced here, by one recipe, from a CLEAN checkout, so that
# anybody can rebuild a running binary later and get the same bytes.
#
# WHY THIS FILE EXISTS
# --------------------
# It was hand-rolled. Six binaries on the PRE-LIVE appliance were built by six separate invocations typed at
# different times, and an audit of their embedded metadata found exactly what that produces:
#
#   * acctd carried NO VCS stamp at all and was built with go1.26.2 -- a toolchain this project does not use.
#     Its source could not be established from the artifact, at all. It had to be replaced rather than
#     identified, because the honest alternative was to guess.
#   * portald carried a VCS stamp, and its bytes could not be reproduced from the commit it named. Plain,
#     stripped and cleared-build-id variants at the recorded toolchain all missed. A stamp is a self-report;
#     without reproduction it is a claim, not evidence.
#   * Five of the six were missing `-tags stayconnect_production`, which §6 step 6 of
#     docs/DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md names as how a Production appliance is built.
#
# None of that was anybody being careless. It is what happens when the recipe lives in a person's shell
# history instead of in the repository.
#
# WHAT MAKES A BUILD REPRODUCIBLE HERE
# ------------------------------------
#   * A pinned toolchain, run in a container, so the host's Go version cannot leak in.
#   * -trimpath, so the build directory is not baked into the binary.
#   * CGO disabled and GOOS/GOARCH pinned, so the host platform cannot leak in either.
#   * A REAL .git directory. This is the one that is easy to get wrong: `git worktree` writes a .git FILE
#     whose gitdir points outside the mount, so inside the container Go cannot read the repository, silently
#     omits the whole vcs block, and produces a binary that differs from the identical source built elsewhere.
#     That single detail is why two of these binaries appeared irreproducible until it was found.
#   * NO -ldflags. Stripping is not used, because `-ldflags` is NOT recorded in Go's build metadata: a binary
#     built with it and one built without it are indistinguishable from the artifact, which defeats the point
#     of being able to check a running binary later.
#
# USAGE
#   scripts/build-appliance-binaries.sh [outdir]        # all services
#   SERVICES="edged pmsd" scripts/build-appliance-binaries.sh out/
#
# It refuses a dirty tree: a binary stamped vcs.modified=true names a commit that does not describe it.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$ROOT/../sc-appliance-build}"
GO_IMAGE="${GO_IMAGE:-golang:1.25.12}"
SERVICES="${SERVICES:-scd edged pmsd portald netd acctd}"
BUILD_TAGS="${BUILD_TAGS:-stayconnect_production}"

cd "$ROOT"

# THE TREE MUST BE CLEAN, and this is checked rather than trusted. Go stamps vcs.modified=true on a dirty
# build, which makes the recorded revision a lie by omission: it names a commit whose content is not what was
# compiled. Refusing here is cheaper than discovering it on an appliance.
if [ -n "$(git status --porcelain)" ]; then
  echo "REFUSED: the working tree is dirty. A binary built from it would carry vcs.modified=true and name a" >&2
  echo "         commit that does not describe it. Commit or stash first." >&2
  git status --short >&2
  exit 2
fi

HEAD_SHA="$(git rev-parse HEAD)"
mkdir -p "$OUT"

echo "== appliance build =="
echo "   commit:    $HEAD_SHA"
echo "   toolchain: $GO_IMAGE"
echo "   tags:      $BUILD_TAGS"
echo "   output:    $OUT"
echo

# The repository is mounted read-only. The build writes only to the output mount, so a build can never
# modify the source it is supposed to be reproducing.
for svc in $SERVICES; do
  [ -d "$ROOT/data-plane/cmd/$svc" ] || { echo "REFUSED: no such service: cmd/$svc" >&2; exit 2; }
  MSYS_NO_PATHCONV=1 docker run --rm \
    -v "$(cygpath -w "$ROOT" 2>/dev/null || echo "$ROOT")":/src:ro \
    -v "$(cygpath -w "$OUT" 2>/dev/null || echo "$OUT")":/out \
    -w /src/data-plane \
    -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \
    -e GOFLAGS=-p=2 -e GOMAXPROCS=2 \
    -e GOCACHE=/tmp/gocache -e GOMODCACHE=/tmp/gomodcache \
    "$GO_IMAGE" \
    go build -trimpath -tags "$BUILD_TAGS" -o "/out/$svc" "./cmd/$svc"
  printf '  %-8s %s\n' "$svc" "$(sha256sum "$OUT/$svc" | cut -d' ' -f1)"
done

echo
echo "== embedded provenance, read back from what was just written =="
# READ BACK, do not assume. The point of the exercise is that the artifact itself carries its origin, so the
# script proves that for every binary it produced rather than asserting it.
fail=0
for svc in $SERVICES; do
  info="$(MSYS_NO_PATHCONV=1 docker run --rm \
      -v "$(cygpath -w "$OUT" 2>/dev/null || echo "$OUT")":/out:ro "$GO_IMAGE" \
      go version -m "/out/$svc" 2>/dev/null || true)"
  rev="$(printf '%s' "$info" | grep -o 'vcs.revision=[0-9a-f]*' | cut -d= -f2 || true)"
  mod="$(printf '%s' "$info" | grep -o 'vcs.modified=[a-z]*' | cut -d= -f2 || true)"
  tag="$(printf '%s' "$info" | grep -o -- '-tags=[^[:space:]]*' | cut -d= -f2 || true)"
  ok="ok"
  [ "$rev" = "$HEAD_SHA" ]        || { ok="REVISION MISMATCH ($rev)"; fail=1; }
  [ "$mod" = "false" ]            || { ok="BUILT FROM A MODIFIED TREE"; fail=1; }
  [ "$tag" = "$BUILD_TAGS" ]      || { ok="TAG MISSING (got '${tag:-none}')"; fail=1; }
  printf '  %-8s revision=%s modified=%s tags=%s  -> %s\n' \
    "$svc" "${rev:0:12}" "${mod:-?}" "${tag:-none}" "$ok"
done

[ "$fail" = 0 ] || { echo; echo "APPLIANCE_BUILD = FAIL"; exit 1; }
echo
echo "APPLIANCE_BUILD = PASS (every binary names $HEAD_SHA, clean, with tags=$BUILD_TAGS)"
