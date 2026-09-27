#!/usr/bin/env bash
# BUILD A ONEGATE CENTRAL RELEASE — ctrlapi + the console bundle + the Central deploy tooling, in one artefact.
#
# Run on a workstation or CI with docker. Never on Central (no Go, no npm there — see deploy-hotel-admin.sh for
# the day a Next build on a server exhausted its memory).
#
#   deploy/scripts/central-build.sh [outdir]           default outdir: <repo>/dist/central
#
# Produces  <outdir>/onegate-central-<sha12>/              the release directory
#           <outdir>/onegate-central-<sha12>.tar.gz        the same, as the one file that ships
#           <outdir>/onegate-central-<sha12>.tar.gz.sha256
#
# Release layout (it mirrors the repository, so every script finds its siblings the same way in both):
#   RELEASE.json                    source commit, toolchains, binary sha256 + embedded revision, console BUILD_ID
#   SHA256SUMS                      every file in the release
#   bin/ctrlapi
#   cloud-admin/                    Next standalone: server.js, .next/ (incl. static), node_modules, cloud-admin-release.json
#   control-plane/migrations/       what central-migrate.sh applies
#   deploy/...                      compose, systemd units, Caddy template, env template, endpoint config, scripts
#
# REPRODUCIBILITY (same rules as scripts/build-appliance-binaries.sh, and for the same reasons):
#   * The build runs from a FRESH CLONE of the exact commit, never from the working tree: a dirty tree is refused,
#     and a git worktree's .git FILE would otherwise hide the repository from Go and drop the VCS stamp.
#   * Pinned toolchains in containers (GO_IMAGE, NODE_IMAGE), so the host's versions cannot leak in.
#   * go build -trimpath, CGO_ENABLED=0, GOOS/GOARCH pinned, NO -ldflags (ldflags are not recorded in the build
#     info, so a binary built with them cannot be told apart from one built without).
#   * The embedded provenance is READ BACK from the produced binary and must name the commit, unmodified.
#   * The console is built with `npm ci` from the committed lockfile.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
OUT="${1:-$ROOT/dist/central}"
GO_IMAGE="${GO_IMAGE:-golang:1.25.12}"
NODE_IMAGE="${NODE_IMAGE:-node:20.18.0-bookworm-slim}"
GOARCH_TARGET="${GOARCH_TARGET:-amd64}"

say() { echo "[central-build] $*"; }
die() { echo "[central-build] REFUSED: $*" >&2; exit 2; }
winpath() { cygpath -w "$1" 2>/dev/null || echo "$1"; }

command -v docker >/dev/null || die "docker is required"
cd "$ROOT"

# ---------------------------------------------------------------- 1. the source is exactly one commit
if [ -n "$(git status --porcelain)" ]; then
  git status --short >&2
  die "the working tree is dirty. A release names ONE commit; commit (or stash) first."
fi
SHA="$(git rev-parse HEAD)"
SHORT="${SHA:0:12}"
NAME="onegate-central-$SHORT"
REL="$OUT/$NAME"
say "commit:    $SHA"
say "toolchain: $GO_IMAGE / $NODE_IMAGE"
say "output:    $REL"

mkdir -p "$OUT"
[ ! -e "$REL" ] || die "$REL already exists — remove it to rebuild (a release directory is never overwritten in place)"
SRC="$OUT/.src-$SHORT"
rm -rf "$SRC"
trap 'rm -rf "$SRC"' EXIT

# A fresh clone of just this commit: a real .git directory, and nothing that is not committed.
git init -q "$SRC"
# Line endings exactly as committed, whatever the host's autocrlf says: these scripts run on Linux.
git -C "$SRC" config core.autocrlf false
git -C "$SRC" fetch -q --depth 1 "$ROOT" "$SHA" 2>/dev/null || git -C "$SRC" fetch -q "$ROOT" HEAD
git -C "$SRC" checkout -q FETCH_HEAD
[ "$(git -C "$SRC" rev-parse HEAD)" = "$SHA" ] || die "the build clone is not at $SHA"
[ -z "$(git -C "$SRC" status --porcelain)" ] || die "the build clone is not clean"

mkdir -p "$REL/bin"

# ---------------------------------------------------------------- 2. ctrlapi
say "building ctrlapi (go build -trimpath, CGO_ENABLED=0, linux/$GOARCH_TARGET)"
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(winpath "$SRC")":/src:ro \
  -v "$(winpath "$REL/bin")":/out \
  -w /src/control-plane \
  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH="$GOARCH_TARGET" \
  -e GOFLAGS=-p=2 -e GOCACHE=/tmp/gocache -e GOMODCACHE=/tmp/gomodcache \
  -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0='*' \
  "$GO_IMAGE" \
  go build -trimpath -o /out/ctrlapi ./cmd/ctrlapi

info="$(MSYS_NO_PATHCONV=1 docker run --rm -v "$(winpath "$REL/bin")":/out:ro "$GO_IMAGE" go version -m /out/ctrlapi)"
rev="$(printf '%s' "$info" | grep -o 'vcs.revision=[0-9a-f]*' | cut -d= -f2 || true)"
mod="$(printf '%s' "$info" | grep -o 'vcs.modified=[a-z]*' | cut -d= -f2 || true)"
gov="$(printf '%s' "$info" | head -1 | awk '{print $2}')"
[ "$rev" = "$SHA" ] || die "ctrlapi embeds vcs.revision='$rev', not $SHA"
[ "$mod" = "false" ] || die "ctrlapi embeds vcs.modified='$mod'"
CTRLAPI_SHA256="$(sha256sum "$REL/bin/ctrlapi" | cut -d' ' -f1)"
say "ctrlapi: $gov revision=${rev:0:12} modified=$mod sha256=$CTRLAPI_SHA256"

# ---------------------------------------------------------------- 3. the console (Next standalone)
say "building cloud-admin (npm ci + next build in $NODE_IMAGE)"
mkdir -p "$REL/cloud-admin"
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(winpath "$SRC/cloud-admin")":/src:ro \
  -v "$(winpath "$REL/cloud-admin")":/out \
  -e NEXT_TELEMETRY_DISABLED=1 -e npm_config_update_notifier=false \
  "$NODE_IMAGE" sh -euc '
    cp -a /src /work && cd /work
    rm -rf node_modules .next
    npm ci --no-fund --no-audit
    npm run build
    [ -f .next/standalone/server.js ] || { echo "standalone output missing (output: standalone in next.config?)" >&2; exit 1; }
    # Assemble: standalone server + static assets + public. The trailing "/." copies the HIDDEN .next directory
    # inside standalone/ — a "/*" glob silently drops it and Next then dies with "Could not find a production build".
    cp -a .next/standalone/. /out/
    mkdir -p /out/.next
    cp -a .next/static /out/.next/static
    if [ -d public ]; then cp -a public /out/public; fi
    node --version > /out/.built-with-node
    # Owned by the invoking user on the host, not root.
    chown -R "$(stat -c %u:%g /out)" /out 2>/dev/null || true
  '
[ -f "$REL/cloud-admin/server.js" ]      || die "console bundle has no server.js"
[ -f "$REL/cloud-admin/.next/BUILD_ID" ] || die "console bundle has no .next/BUILD_ID (hidden .next not copied)"
[ -d "$REL/cloud-admin/.next/static" ]   || die "console bundle has no .next/static"
BUILD_ID="$(cat "$REL/cloud-admin/.next/BUILD_ID")"
BUILT_NODE="$(cat "$REL/cloud-admin/.built-with-node")"; rm -f "$REL/cloud-admin/.built-with-node"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
python3 - "$REL/cloud-admin/cloud-admin-release.json" "$SHA" "$BUILD_ID" "$BUILT_AT" "$BUILT_NODE" <<'PY'
import json, sys
out, commit, bid, at, node = sys.argv[1:6]
json.dump({"source_commit": commit, "source_state": "clean", "build_id": bid, "built_at": at,
           "built_with_node": node}, open(out, "w", encoding="utf-8"), indent=2)
PY
say "console: BUILD_ID=$BUILD_ID (node $BUILT_NODE)"

# ---------------------------------------------------------------- 4. tooling + migrations (from the clone)
mkdir -p "$REL/control-plane" "$REL/deploy"/{scripts,systemd,caddy,compose,env,config,pki}
cp -a "$SRC/control-plane/migrations" "$REL/control-plane/migrations"
rm -f "$REL/control-plane/migrations/"*.go
for f in central-lib.sh central-install.sh central-deploy.sh central-export.sh central-cleanup-obsolete.sh \
         central-migrate.sh central-preflight.sh central-mint-tls.sh central-firewall.sh central-build.sh \
         vendor-signing-key.sh install-central-endpoint.sh lib-central-endpoint.sh \
         stayconnect-backup-cleanup.sh backup-retention.conf; do
  cp -a "$SRC/deploy/scripts/$f" "$REL/deploy/scripts/$f"
done
cp -a "$SRC/deploy/systemd/stayconnect-ctrlapi.service" "$SRC/deploy/systemd/stayconnect-cloud-admin.service" \
      "$SRC/deploy/systemd/stayconnect-backup-cleanup.service" "$SRC/deploy/systemd/stayconnect-backup-cleanup.timer" \
      "$REL/deploy/systemd/"
cp -a "$SRC/deploy/caddy/Caddyfile.central" "$SRC/deploy/caddy/stayconnect-caddy.central.service" "$REL/deploy/caddy/"
cp -a "$SRC/deploy/compose/central-infra.yml" "$REL/deploy/compose/"
cp -a "$SRC/deploy/env/ctrlapi.env.example" "$REL/deploy/env/"
cp -a "$SRC/deploy/config/central-endpoint.env" "$REL/deploy/config/"
cp -a "$SRC/deploy/pki/README.md" "$REL/deploy/pki/"
cp -a "$SRC/docs/DEPLOYMENT_CLOUD.md" "$REL/DEPLOYMENT_CLOUD.md"
chmod 0755 "$REL/deploy/scripts/"*.sh "$REL/bin/ctrlapi"

# ---------------------------------------------------------------- 5. identity + checksums
python3 - "$REL/RELEASE.json" "$SHA" "$GO_IMAGE" "$gov" "$NODE_IMAGE" "$BUILT_NODE" "$CTRLAPI_SHA256" "$BUILD_ID" "$BUILT_AT" "$GOARCH_TARGET" <<'PY'
import json, sys
out, sha, goimg, gov, nodeimg, node, csum, bid, at, arch = sys.argv[1:11]
json.dump({
  "_note": "OneGate Central release. central-install.sh / central-deploy.sh refuse a release whose files do not "
           "match SHA256SUMS or whose ctrlapi does not embed source_commit.",
  "product": "onegate-central", "source_commit": sha, "source_state": "clean", "built_at": at,
  "ctrlapi": {"path": "bin/ctrlapi", "sha256": csum, "go_image": goimg, "go_version": gov,
              "goos": "linux", "goarch": arch, "vcs_revision": sha, "vcs_modified": False},
  "cloud_admin": {"path": "cloud-admin", "build_id": bid, "node_image": nodeimg, "built_with_node": node,
                  "node_min_runtime": "18.17.0"},
}, open(out, "w", encoding="utf-8"), indent=2)
PY
( cd "$REL" && find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS )
say "SHA256SUMS: $(wc -l < "$REL/SHA256SUMS") files"

# Root-owned in the archive whatever the build user is: extracted as root on Central it must not carry a
# workstation uid.
tar -C "$OUT" --owner=0 --group=0 --numeric-owner -czf "$OUT/$NAME.tar.gz" "$NAME"
( cd "$OUT" && sha256sum "$NAME.tar.gz" > "$NAME.tar.gz.sha256" )
say ""
say "RELEASE READY: $OUT/$NAME.tar.gz"
say "  sha256 $(cut -d' ' -f1 < "$OUT/$NAME.tar.gz.sha256")"
say "Ship it to the Central host, then:"
say "  tar -xzf $NAME.tar.gz && sudo bash $NAME/deploy/scripts/central-install.sh --mode new|restore ...   (first install)"
say "  tar -xzf $NAME.tar.gz && sudo bash $NAME/deploy/scripts/central-deploy.sh                             (upgrade)"
