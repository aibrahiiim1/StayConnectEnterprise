#!/usr/bin/env bash
# BUILD A ONEGATE APPLIANCE RELEASE — every binary, the Admin Console bundle, the deploy tree, the migrations and
# the installer, in ONE verified artefact built from ONE clean commit.
#
# Before this, a fresh appliance was assembled by hand: a binary build that left out two required programs, an
# Admin Console package built separately, the deploy tree and migrations copied over with scp, and nothing that
# proved the pieces came from the same commit. This is the appliance counterpart of central-build.sh.
#
# Run on a workstation or CI with docker, git and python3 (Go and Node run in pinned containers). Never on an appliance.
#
#   deploy/scripts/appliance-build.sh [outdir]                    default outdir: <repo>/dist/appliance
#
# Produces  <outdir>/onegate-appliance-<sha12>/                   the release directory
#           <outdir>/onegate-appliance-<sha12>.tar.gz             the one file that ships
#           <outdir>/onegate-appliance-<sha12>.tar.gz.sha256
#
# Release layout (mirrors /opt/stayconnect on the appliance, so every script finds its siblings the same way):
#   RELEASE.json                     commit, toolchains, per-binary sha256 + embedded revision + tags, console
#                                    build id, baseline coverage
#   SHA256SUMS                       every file in the release
#   bin/{scd,edged,netd,portald,acctd,pmsd,keybootstrap,svc-run}
#   bundles/hotel-admin-deploy.tgz   the Admin Console (Next standalone) + .sha256
#   deploy/                          units, Caddy, compose, gatep, env templates, scripts (incl. appliance-install.sh)
#   data-plane/migrations/           baseline + numbered migrations
#   scripts/                         edge-migrate.sh, gatep-reconcile.sh, binary-rollback.sh, client-journey-netns-test.sh
#   ONEGATE_FRESH_VM_INSTALLATION_MANUAL.md
#
# Reproducibility: a FRESH CLONE of the exact commit (a dirty tree is refused); binaries from
# scripts/build-appliance-binaries.sh in the pinned Go container with -tags stayconnect_production, whose
# embedded provenance is read back; the console from deploy-hotel-admin.sh package (npm ci on the committed
# lockfile). The baseline must embody the newest migration or the build stops.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
OUT="${1:-$ROOT/dist/appliance}"
SERVICES="scd edged netd portald acctd pmsd keybootstrap svc-run"
# The full (not -slim) image: it ships git and python3, which packaging needs, so no apt step runs in the build
# (an apt mirror that is unreachable from the build host otherwise hangs the release indefinitely).
NODE_IMAGE="${NODE_IMAGE:-node:20.18.0-bookworm}"
say() { echo "[appliance-build] $*"; }
die() { echo "[appliance-build] REFUSED: $*" >&2; exit 2; }
for t in docker git python3 sha256sum tar gzip; do command -v "$t" >/dev/null || die "$t is required"; done
cd "$ROOT"
if [ -n "$(git status --porcelain)" ]; then git status --short >&2; die "the working tree is dirty; a release names ONE commit"; fi
SHA="$(git rev-parse HEAD)"; SHORT="${SHA:0:12}"; NAME="onegate-appliance-$SHORT"; REL="$OUT/$NAME"
mkdir -p "$OUT"
[ ! -e "$REL" ] || die "$REL already exists — a release directory is never overwritten in place"
SRC="$OUT/.src-$SHORT"; rm -rf "$SRC"; trap 'rm -rf "$SRC"' EXIT
git init -q "$SRC"
git -C "$SRC" config core.autocrlf false
git -C "$SRC" fetch -q --depth 1 "$ROOT" "$SHA" 2>/dev/null || git -C "$SRC" fetch -q "$ROOT" HEAD
git -C "$SRC" checkout -q FETCH_HEAD
[ "$(git -C "$SRC" rev-parse HEAD)" = "$SHA" ] || die "the build clone is not at $SHA"
[ -z "$(git -C "$SRC" status --porcelain)" ] || die "the build clone is not clean"
say "commit $SHA -> $REL"

# shellcheck source=lib-baseline.sh
. "$SRC/deploy/scripts/lib-baseline.sh"
why="$(baseline_is_current "$SRC/data-plane/migrations/baseline/0000_production_baseline.sql" "$SRC/data-plane/migrations")" || die "$why"

# ---------------------------------------------------------------- 1. binaries (provenance read back by the build script)
mkdir -p "$REL/bin"
SERVICES="$SERVICES" bash "$SRC/scripts/build-appliance-binaries.sh" "$REL/bin" | tee "$OUT/.build-$SHORT.log"
grep -q 'APPLIANCE_BUILD = PASS' "$OUT/.build-$SHORT.log" || die "the binary build did not pass (see above)"
GO_IMAGE="$(sed -n 's/^   toolchain: //p' "$OUT/.build-$SHORT.log" | head -1)"
rm -f "$OUT/.build-$SHORT.log"

# ---------------------------------------------------------------- 2. the Admin Console bundle
# Packaged in the pinned Node container (the appliance runs Node 20), never with whatever Node the workstation has.
mkdir -p "$REL/bundles"
winpath() { cygpath -w "$1" 2>/dev/null || echo "$1"; }
MSYS_NO_PATHCONV=1 docker run --rm \
  -v "$(winpath "$SRC")":/src:ro -v "$(winpath "$REL/bundles")":/out \
  -e NEXT_TELEMETRY_DISABLED=1 -e npm_config_update_notifier=false \
  -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0='*' \
  "$NODE_IMAGE" bash -euc '
    cp -a /src /work && cd /work
    bash deploy/scripts/deploy-hotel-admin.sh package hotel-admin
    cp hotel-admin/hotel-admin-deploy.tgz hotel-admin/hotel-admin-deploy.tgz.sha256 /out/
    node --version > /out/.packaged-with-node
    chown -R "$(stat -c %u:%g /out)" /out 2>/dev/null || true
  '
PACKAGED_NODE="$(cat "$REL/bundles/.packaged-with-node")"; rm -f "$REL/bundles/.packaged-with-node"
( cd "$REL/bundles" && sha256sum -c --quiet hotel-admin-deploy.tgz.sha256 ) || die "the console bundle does not match its checksum"
CONSOLE_JSON="$(tar -xOzf "$REL/bundles/hotel-admin-deploy.tgz" ./hotel-admin-release.json 2>/dev/null || tar -xOzf "$REL/bundles/hotel-admin-deploy.tgz" hotel-admin-release.json)"

# ---------------------------------------------------------------- 3. the tree (committed content only)
mkdir -p "$REL/data-plane" "$REL/scripts"
( cd "$SRC" && git archive --format=tar HEAD deploy data-plane/migrations ) | tar -xf - -C "$REL"
for f in edge-migrate.sh gatep-reconcile.sh binary-rollback.sh client-journey-netns-test.sh; do cp "$SRC/scripts/$f" "$REL/scripts/$f"; done
cp "$SRC/docs/runbooks/ONEGATE_FRESH_VM_INSTALLATION_MANUAL.md" "$REL/"
# deploy/pki carries slots only (git-ignored contents); the trust files are supplied at install time.
find "$REL/deploy/pki" -type f ! -name README.md ! -name .gitignore -delete

# ---------------------------------------------------------------- 4. identity + checksums
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
python3 - "$REL" "$SHA" "$BUILT_AT" "${GO_IMAGE:-unknown}" "$PACKAGED_NODE ($NODE_IMAGE)" \
  "$(baseline_through "$REL/data-plane/migrations/baseline/0000_production_baseline.sql")" \
  "$(latest_migration "$REL/data-plane/migrations")" "$SERVICES" "$CONSOLE_JSON" <<'PY'
import hashlib, json, re, sys
rel, sha, at, goimg, node, through, latest, services, console = sys.argv[1:10]
bins = {}
for s in services.split():
    p = f"{rel}/bin/{s}"
    raw = open(p, "rb").read()
    rev = re.search(rb"vcs\.revision=([0-9a-f]{40})", raw)
    mod = re.search(rb"vcs\.modified=(true|false)", raw)
    tags = re.search(rb"-tags=([A-Za-z0-9_,]+)", raw)
    bins[s] = {"path": f"bin/{s}", "sha256": hashlib.sha256(raw).hexdigest(),
               "vcs_revision": rev.group(1).decode() if rev else "",
               "vcs_modified": (mod.group(1) == b"true") if mod else None,
               "tags": tags.group(1).decode() if tags else ""}
    if bins[s]["vcs_revision"] != sha or bins[s]["vcs_modified"] is not False or bins[s]["tags"] != "stayconnect_production":
        sys.exit(f"{s}: embedded provenance does not describe {sha} (production, unmodified): {bins[s]}")
c = json.loads(console) if console.strip() else {}
if c.get("source_commit") != sha:
    sys.exit(f"the console bundle was built from {c.get('source_commit')!r}, not {sha}")
bundle = f"{rel}/bundles/hotel-admin-deploy.tgz"
json.dump({
  "_note": "OneGate appliance release. appliance-install.sh refuses a release whose files do not match SHA256SUMS "
           "or whose binaries do not embed source_commit.",
  "product": "onegate-appliance", "source_commit": sha, "source_state": "clean", "built_at": at,
  "go_image": goimg, "build_tags": "stayconnect_production", "binaries": bins,
  "hotel_admin": {"path": "bundles/hotel-admin-deploy.tgz", "sha256": hashlib.sha256(open(bundle, "rb").read()).hexdigest(),
                  "build_id": c.get("build_id"), "source_commit": c.get("source_commit"), "packaged_with_node": node},
  "schema": {"baseline_through": through, "latest_migration": latest},
  "supported_platform": {"os": "Ubuntu 22.04 LTS", "arch": "amd64", "nics": 2},
}, open(f"{rel}/RELEASE.json", "w", encoding="utf-8"), indent=2)
PY
( cd "$REL" && find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS )
say "SHA256SUMS: $(wc -l < "$REL/SHA256SUMS") files"

# Root-owned, and the programs explicitly 0755 (a Windows workstation cannot mark an extensionless ELF executable).
rm -f "$OUT/$NAME.tar" "$OUT/$NAME.tar.gz"
tar -C "$OUT" --owner=0 --group=0 --numeric-owner --exclude="$NAME/bin" -cf "$OUT/$NAME.tar" "$NAME"
tar -C "$OUT" --owner=0 --group=0 --numeric-owner --mode=0755 -rf "$OUT/$NAME.tar" "$NAME/bin"
gzip -n "$OUT/$NAME.tar"
for s in $SERVICES; do
  tar -tvzf "$OUT/$NAME.tar.gz" "$NAME/bin/$s" | grep -q '^-rwxr-xr-x' || die "bin/$s is not executable in the archive"
done
( cd "$OUT" && sha256sum "$NAME.tar.gz" > "$NAME.tar.gz.sha256" )
say ""
say "RELEASE READY: $OUT/$NAME.tar.gz"
say "  sha256 $(cut -d' ' -f1 < "$OUT/$NAME.tar.gz.sha256")"
say "On a blank Ubuntu 22.04 VM with two NICs (as root):"
say "  sha256sum -c $NAME.tar.gz.sha256 && tar -xzf $NAME.tar.gz"
say "  bash $NAME/deploy/scripts/appliance-install.sh --trust-dir <dir with Central's public trust files> --admin-email <email>"
