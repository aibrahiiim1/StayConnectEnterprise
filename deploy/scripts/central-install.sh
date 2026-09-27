#!/usr/bin/env bash
# INSTALL ONEGATE CENTRAL ON A CLEAN HOST — from a release built by central-build.sh, nothing else.
#
# Two modes, and choosing the wrong one is the only way to hurt the fleet with this script:
#
#   --mode new       A BRAND-NEW Central with a brand-new identity: new vendor licence key, new assignment
#                    signing key, new registry root key, new appliance Root/Intermediate CA, new internal TLS CA.
#                    No appliance in the world trusts it yet. Refuses to run over a database that already holds
#                    appliances or licences.
#
#   --mode restore   MOVE an existing Central to this host. Everything appliances trust is CARRIED OVER from a
#                    bundle made by central-export.sh on the old host — the vendor key, assignment key, registry
#                    root key, the appliance Root and Intermediate CA, the mTLS and :443 certificates and the
#                    internal TLS CA — together with the database (licence and assignment version history). Nothing
#                    that appliances pin is generated. Every piece is checked to belong with the others, and with
#                    the database, BEFORE ctrlapi is allowed to start: a ctrlapi that starts without the CA files
#                    silently mints a new CA, and every appliance would stop trusting Central.
#
# Usage (as root, from an extracted release):
#   bash onegate-central-<sha>/deploy/scripts/central-install.sh --mode new \
#        --admin-email ops@example.com [--admin-name admin.example.com] [--extra-names 203.0.113.10] \
#        [--tls internal|acme [--acme-email ops@example.com]]
#   bash onegate-central-<sha>/deploy/scripts/central-install.sh --mode restore --bundle /root/central-export-….tar.gz.enc \
#        [--extra-names …] [--yes]
#
# Options:
#   --release <dir|tar.gz>   the release to install (default: the release this script is part of)
#   --endpoint-config <file> install this instead of the release's deploy/config/central-endpoint.env (labs only)
#   --admin-email <e>        first platform admin (mode new). Password: $CENTRAL_ADMIN_PASSWORD, or prompted.
#   --skip-admin             do not seed an admin now (seed later: ctrlapi seed-admin)
#   --admin-name <host>      the console's public name(s), comma-separated (default: the appliance-facing name)
#   --extra-names <a,b>      more names/IPs the :443 site answers for and its certificate covers
#   --tls internal|acme      :443 certificate from the internal Central TLS CA (default; appliances install that
#                            CA) or an automatic public certificate from Caddy (needs public DNS + :80/:443)
#   --skip-firewall          leave ufw alone
#   --install-prereqs        apt-install docker, compose, caddy, node 20 (Ubuntu 22.04/24.04 / Debian 12)
#   --dry-run                print every change instead of making it
#   --yes                    no interactive confirmation (restore mode asks otherwise)
#
# Idempotent: re-running converges. It never regenerates a secret, a key, a CA or a certificate that exists,
# never overwrites trust material with DIFFERENT content, and never restores over a database that has data.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROG=central-install
# shellcheck source=central-lib.sh
. "$HERE/central-lib.sh"

MODE="" BUNDLE="" RELEASE_ARG="" ENDPOINT_CFG="" ADMIN_EMAIL="" SKIP_ADMIN=0 ADMIN_NAMES="" EXTRA_NAMES=""
TLS_MODE="" ACME_EMAIL="" SKIP_FW=0 INSTALL_PREREQS=0 YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --mode) MODE="$2"; shift 2 ;;
    --bundle) BUNDLE="$2"; shift 2 ;;
    --release) RELEASE_ARG="$2"; shift 2 ;;
    --endpoint-config) ENDPOINT_CFG="$2"; shift 2 ;;
    --admin-email) ADMIN_EMAIL="$2"; shift 2 ;;
    --skip-admin) SKIP_ADMIN=1; shift ;;
    --admin-name) ADMIN_NAMES="$2"; shift 2 ;;
    --extra-names) EXTRA_NAMES="$2"; shift 2 ;;
    --tls) TLS_MODE="$2"; shift 2 ;;
    --acme-email) ACME_EMAIL="$2"; shift 2 ;;
    --skip-firewall) SKIP_FW=1; shift ;;
    --install-prereqs) INSTALL_PREREQS=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    --yes) YES=1; shift ;;
    -h|--help) sed -n '2,45p' "$0"; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done
export DRY_RUN
case "$MODE" in
  new) [ -z "$BUNDLE" ] || die "--bundle belongs to --mode restore" ;;
  restore) [ -n "$BUNDLE" ] || die "--mode restore needs --bundle <central-export bundle>" ;;
  *) die "--mode new|restore is required. new = a new Central identity; restore = move an existing Central here." ;;
esac
need_root
[ "$DRY_RUN" = 1 ] && say "DRY RUN — nothing on this host will be changed"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
WORK="$(mktemp -d /tmp/central-install.XXXXXX)"
chmod 0700 "$WORK"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

# ================================================================= 1. the release
step "1/14 release"
REL=""
if [ -n "$RELEASE_ARG" ]; then
  if [ -d "$RELEASE_ARG" ]; then REL="$(cd "$RELEASE_ARG" && pwd)"
  elif [ -f "$RELEASE_ARG" ]; then
    tar -xzf "$RELEASE_ARG" -C "$WORK"
    REL="$(find "$WORK" -mindepth 1 -maxdepth 1 -type d -name 'onegate-central-*' | head -1)"
  fi
else
  REL="$(cd "$HERE/../.." && pwd)"
fi
[ -n "$REL" ] && [ -f "$REL/RELEASE.json" ] || die "no release found (${RELEASE_ARG:-$HERE/../..} has no RELEASE.json).
    Build one with deploy/scripts/central-build.sh; a repository checkout is not a release."
( cd "$REL" && sha256sum -c --quiet SHA256SUMS ) || die "release files do not match its SHA256SUMS — refusing a damaged or edited release"
REL_COMMIT="$(jget "$REL/RELEASE.json" 'd["source_commit"]')"
REL_STATE="$(jget "$REL/RELEASE.json" 'd["source_state"]')"
[ "$REL_STATE" = clean ] || die "release was built from a $REL_STATE tree"
[ "$(binary_revision "$REL/bin/ctrlapi")" = "$REL_COMMIT" ] || die "bin/ctrlapi does not embed $REL_COMMIT"
[ "$(jget "$REL/cloud-admin/$CONSOLE_MANIFEST" 'd["build_id"]')" = "$(cat "$REL/cloud-admin/.next/BUILD_ID")" ] \
  || die "console manifest does not describe the console bundle"
REL_NAME="onegate-central-${REL_COMMIT:0:12}"
say "release $REL_NAME  (commit $REL_COMMIT, console BUILD_ID $(cat "$REL/cloud-admin/.next/BUILD_ID"))"

# The endpoint every appliance dials — from the versioned config (or an explicit lab override).
ENDPOINT_SRC="${ENDPOINT_CFG:-$REL/deploy/config/central-endpoint.env}"
[ -f "$ENDPOINT_SRC" ] || die "no endpoint configuration at $ENDPOINT_SRC"
APPLIANCE_BASE="$(envfile_get "$ENDPOINT_SRC" CTRLAPI_APPLIANCE_BASE || envfile_get "$ENDPOINT_SRC" CENTRAL_BASE)" \
  || die "$ENDPOINT_SRC defines neither CTRLAPI_APPLIANCE_BASE nor CENTRAL_BASE"
PRIMARY="$(host_of_url "$APPLIANCE_BASE")"
say "appliance-facing endpoint: $APPLIANCE_BASE  (from $ENDPOINT_SRC)"

# ================================================================= 2. host prerequisites
step "2/14 host prerequisites"
install_prereqs() {
  . /etc/os-release
  case "$ID" in ubuntu|debian) ;; *) die "--install-prereqs supports Ubuntu/Debian only (this is $ID)";; esac
  run apt-get update -q
  run env DEBIAN_FRONTEND=noninteractive apt-get install -y -q ca-certificates curl gnupg openssl python3 ufw iproute2 debian-keyring debian-archive-keyring apt-transport-https
  if ! command -v docker >/dev/null; then
    run env DEBIAN_FRONTEND=noninteractive apt-get install -y -q docker.io
  fi
  if ! docker compose version >/dev/null 2>&1; then
    run env DEBIAN_FRONTEND=noninteractive apt-get install -y -q docker-compose-v2 \
      || run env DEBIAN_FRONTEND=noninteractive apt-get install -y -q docker-compose-plugin
  fi
  if ! command -v caddy >/dev/null; then
    # https://caddyserver.com/docs/install#debian-ubuntu-raspbian
    run sh -c "curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg"
    run sh -c "curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt > /etc/apt/sources.list.d/caddy-stable.list"
    run apt-get update -q
    run env DEBIAN_FRONTEND=noninteractive apt-get install -y -q caddy
  fi
  if ! command -v node >/dev/null || ! version_ge "$(node --version | tr -d v)" "$CENTRAL_NODE_MIN"; then
    # NodeSource 20.x (the major the console is built with).
    run sh -c "curl -fsSL https://deb.nodesource.com/setup_20.x | bash -"
    run env DEBIAN_FRONTEND=noninteractive apt-get install -y -q nodejs
  fi
  run systemctl enable --now docker
}
[ "$INSTALL_PREREQS" = 1 ] && install_prereqs

missing=0
need() { if command -v "$1" >/dev/null 2>&1; then say "  ok  $1"; else warn "missing: $1 — $2"; missing=1; fi; }
need systemctl "systemd host required"
need docker "apt-get install docker.io (or Docker CE)"
need caddy "https://caddyserver.com/docs/install (the stayconnect-caddy unit runs /usr/bin/caddy)"
need node "Node.js >= $CENTRAL_NODE_MIN at /usr/bin/node (20 LTS recommended)"
need openssl "apt-get install openssl"
need python3 "apt-get install python3"
need curl "apt-get install curl"
need ss "apt-get install iproute2"
need sha256sum "coreutils"
[ "$SKIP_FW" = 1 ] || need ufw "apt-get install ufw (or pass --skip-firewall and firewall the host yourself)"
if command -v docker >/dev/null && ! docker compose version >/dev/null 2>&1; then warn "missing: docker compose v2 plugin"; missing=1; fi
if command -v node >/dev/null; then
  nv="$(node --version | tr -d v)"
  if version_ge "$nv" "$CENTRAL_NODE_MIN"; then say "  ok  node $nv"; else warn "node $nv is older than $CENTRAL_NODE_MIN"; missing=1; fi
  [ -x /usr/bin/node ] || { warn "the cloud-admin unit runs /usr/bin/node, which does not exist"; missing=1; }
fi
if command -v caddy >/dev/null; then [ -x /usr/bin/caddy ] || { warn "the Caddy unit runs /usr/bin/caddy, which does not exist"; missing=1; }; fi
getent passwd caddy >/dev/null || { warn "no 'caddy' user (the caddy package creates it)"; missing=1; }
if [ "$missing" = 1 ]; then
  [ "$DRY_RUN" = 1 ] && warn "prerequisites missing — a real run would stop here (or use --install-prereqs)" \
    || die "prerequisites missing (see above). Re-run with --install-prereqs, or install them."
fi

# Refuse to run on a host whose Central infra came from somewhere else (e.g. the first host's hand-made
# compose file): this installer builds a fresh host; it does not adopt a running one.
if command -v docker >/dev/null && docker inspect "$CENTRAL_PG_CONTAINER" >/dev/null 2>&1; then
  cf="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.config_files"}}' "$CENTRAL_PG_CONTAINER" 2>/dev/null || true)"
  case "$cf" in
    *central-infra.yml) : ;;
    *) die "container $CENTRAL_PG_CONTAINER already exists here, started from '${cf:-outside compose}'.
    This host already runs a Central database that this installer did not create. Installing over it could
    destroy the licence database. See docs/DEPLOYMENT_CLOUD.md (existing hosts) instead." ;;
  esac
fi

# ================================================================= 3. account, directories, secrets
step "3/14 account, directories, secrets"
if getent passwd "$SC_RUN_USER" >/dev/null; then say "user $SC_RUN_USER exists"
else run useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$SC_RUN_USER"; fi
run install -d -m 0755 "$SC_OPT" "$SC_OPT/bin" "$SC_OPT/releases" "$CONSOLE_RELEASES" "$SC_OPT/releases/central" \
  "$CENTRAL_ROOT" "$CENTRAL_ROOT/compose" "$CENTRAL_PUBLIC_TRUST_DIR" "$SC_ETC" /var/log/stayconnect
run install -d -m 0700 "$CENTRAL_SECRETS_DIR" "$CENTRAL_TLS_DIR" "$SC_ETC/pki" "$SC_ETC/pki-offline" "$CENTRAL_DB_BACKUPS"
if getent passwd caddy >/dev/null; then
  run install -d -o caddy -g caddy -m 0750 /var/log/caddy
  run install -d -o root -g caddy -m 0750 "$(dirname "$CADDY_TLS_CRT")"
fi
gen_secret() {
  local f="$CENTRAL_SECRETS_DIR/$1"
  if [ -s "$f" ] || { [ "$DRY_RUN" = 1 ] && [ -e "$f" ]; }; then say "secret $1 exists — kept"; return; fi
  openssl rand -hex 32 | tr -d '\n' | write_file "$f" 0600
  say "generated secret $1"
}
gen_secret db_password
gen_secret redis_password

# ================================================================= 4. keep the release on the host
step "4/14 release artefacts"
STORED="$SC_OPT/releases/central/$REL_NAME"
if [ -d "$STORED" ] && [ "$DRY_RUN" = 0 ] && cmp -s "$STORED/SHA256SUMS" "$REL/SHA256SUMS"; then
  say "release tooling already stored at $STORED"
else
  run install -d -m 0755 "$STORED"
  run cp -a "$REL/RELEASE.json" "$REL/SHA256SUMS" "$REL/deploy" "$REL/control-plane" "$STORED/"
fi
if [ "$(readlink -f "$CENTRAL_TOOLING" 2>/dev/null)" != "$STORED" ]; then
  run ln -sfn "$STORED" "$CENTRAL_TOOLING.tmp"; run mv -Tf "$CENTRAL_TOOLING.tmp" "$CENTRAL_TOOLING"
fi
T="$REL/deploy/scripts"          # scripts are run from the release (identical to the stored tooling)

if [ -x "$CTRLAPI_BIN_PATH" ] && cmp -s "$CTRLAPI_BIN_PATH" "$REL/bin/ctrlapi"; then
  say "ctrlapi already installed ($(sha256_of "$CTRLAPI_BIN_PATH" | cut -c1-16)…)"
else
  [ -x "$CTRLAPI_BIN_PATH" ] && run cp -a "$CTRLAPI_BIN_PATH" "$CTRLAPI_BIN_PATH.bak-$STAMP"
  run install -m 0755 "$REL/bin/ctrlapi" "$CTRLAPI_BIN_PATH.new"
  run mv -f "$CTRLAPI_BIN_PATH.new" "$CTRLAPI_BIN_PATH"
  say "installed ctrlapi $(jget "$REL/RELEASE.json" 'd["ctrlapi"]["sha256"]' | cut -c1-16)…"
fi

# ================================================================= 5. backing services
step "5/14 Postgres + Redis (deploy/compose/central-infra.yml)"
if [ -f "$CENTRAL_COMPOSE_FILE" ] && cmp -s "$CENTRAL_COMPOSE_FILE" "$REL/deploy/compose/central-infra.yml"; then
  say "compose file current"
else
  [ -f "$CENTRAL_COMPOSE_FILE" ] && run cp -a "$CENTRAL_COMPOSE_FILE" "$CENTRAL_COMPOSE_FILE.bak-$STAMP"
  run install -m 0644 "$REL/deploy/compose/central-infra.yml" "$CENTRAL_COMPOSE_FILE"
fi
if [ "$DRY_RUN" = 1 ]; then
  say "DRY-RUN would run: docker compose -p $CENTRAL_COMPOSE_PROJECT -f $CENTRAL_COMPOSE_FILE up -d  (then wait healthy)"
else
  central_compose up -d
  pg_wait_healthy 180
  say "sc-central-pg and sc-central-redis healthy"
fi
DBQ_OK=0; [ "$DRY_RUN" = 0 ] && DBQ_OK=1

# ================================================================= 6. identity: carried (restore) or created (new)
KEY_VENDOR="$SC_ETC/vendor-license.key"
KEY_ASSIGN="$SC_ETC/assignment-signing.key"
KEY_REG="$SC_ETC/assignment-registry-root.key"
PKI_ROOT_CRT="$SC_ETC/pki/root-ca.crt"
PKI_INT_CRT="$SC_ETC/pki/intermediate-ca.crt"
PKI_INT_KEY="$SC_ETC/pki/intermediate-ca.key"

open_bundle() { # -> $BDIR (extracted, checksummed)
  local b="$BUNDLE"
  BDIR="$WORK/bundle"; mkdir -p "$BDIR"
  if [ -d "$b" ]; then cp -a "$b/." "$BDIR/"
  else
    [ -s "$b" ] || die "no such bundle: $b"
    case "$b" in
      *.enc)
        if [ -z "${CENTRAL_BUNDLE_PASSPHRASE:-}" ]; then
          [ -t 0 ] || die "set CENTRAL_BUNDLE_PASSPHRASE (no terminal to prompt on)"
          printf 'Passphrase for %s: ' "$(basename "$b")" >&2; read -rs CENTRAL_BUNDLE_PASSPHRASE; echo >&2
        fi
        CENTRAL_BUNDLE_PASSPHRASE="$CENTRAL_BUNDLE_PASSPHRASE" openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 \
          -in "$b" -pass env:CENTRAL_BUNDLE_PASSPHRASE | tar -xz -C "$BDIR" \
          || die "could not decrypt/extract $b (wrong passphrase?)" ;;
      *) tar -xzf "$b" -C "$BDIR" ;;
    esac
  fi
  # The tarball holds one top-level directory.
  local inner; inner="$(find "$BDIR" -mindepth 1 -maxdepth 1 -type d | head -1)"
  [ -f "$BDIR/MANIFEST.json" ] || { [ -n "$inner" ] && [ -f "$inner/MANIFEST.json" ] && BDIR="$inner"; }
  [ -f "$BDIR/MANIFEST.json" ] || die "$b is not a central-export bundle (no MANIFEST.json)"
  ( cd "$BDIR" && sha256sum -c --quiet SHA256SUMS ) || die "bundle content does not match its SHA256SUMS"
  say "bundle from $(jget "$BDIR/MANIFEST.json" 'd["source_host"]') at $(jget "$BDIR/MANIFEST.json" 'd["created_at"]') ($(jget "$BDIR/MANIFEST.json" 'd["kind"]'))"
}

# install_trust_file <bundle-relative-path> <mode>: never overwrites DIFFERENT content.
install_trust_file() {
  local mode="$2" src="$BDIR/files/$1" dest="/$1"
  [ -f "$src" ] || return 0
  if [ -f "$dest" ]; then
    if cmp -s "$src" "$dest"; then say "  same     $dest"; return 0; fi
    # LEAF material this host legitimately re-issues from the carried CAs (a new name on the :443 or :9443
    # certificate, the CA serial, the retention policy) differs on a re-run by design. The host's copy stays;
    # verify_trust_set below still proves it chains to the carried CA.
    case "$1" in
      etc/stayconnect/pki/server-mtls.*|opt/stayconnect/central/tls/server.*|opt/stayconnect/central/tls/ca.srl|\
      opt/stayconnect/central/tls/san.cnf|etc/stayconnect/backup-retention.*|etc/stayconnect/pki/ca-bundle.crt)
        say "  kept     $dest (re-issued/edited on this host since the restore)"; return 0 ;;
    esac
    die "$dest already exists with DIFFERENT content than the bundle. Refusing to replace trust material.
    If this host was half-installed from another bundle, rebuild it clean."
  fi
  run install -D -o root -g root -m "$mode" "$src" "$dest"
  say "  carried  $dest"
}

db_trust_crosscheck() { # the database and the carried keys must describe the same Central
  local aid bad=0 rows
  aid="$(ed25519_keyid "$KEY_ASSIGN" private)"
  if pg_table_exists assignment_signing_keys; then
    rows="$(pg_q -c "SELECT count(*) FROM assignment_signing_keys")"
    if [ "$rows" -gt 0 ]; then
      if [ "$(pg_q -c "SELECT count(*) FROM assignment_signing_keys WHERE key_id='$aid'")" = 1 ]; then
        say "  OK    the database lists assignment key $aid"
      else
        warn "the database's assignment_signing_keys does not contain the carried key $aid — keys and database come from different Centrals"; bad=1
      fi
    fi
  fi
  if pg_table_exists appliance_ca_versions; then
    local dbroot dbint
    dbroot="$(pg_q -c "SELECT cert_pem FROM appliance_ca_versions WHERE version=0" | tr -d '[:space:]')"
    dbint="$(pg_q -c "SELECT cert_pem FROM appliance_ca_versions WHERE version=1" | tr -d '[:space:]')"
    if [ -n "$dbroot" ]; then
      if [ "$dbroot" = "$(tr -d '[:space:]' < "$PKI_ROOT_CRT")" ]; then say "  OK    the database's Root CA is the carried Root CA"
      else warn "the database records a DIFFERENT Root CA than the carried root-ca.crt"; bad=1; fi
    fi
    if [ -n "$dbint" ]; then
      if [ "$dbint" = "$(tr -d '[:space:]' < "$PKI_INT_CRT")" ]; then say "  OK    the database's intermediate CA is the carried one"
      else warn "the database records a DIFFERENT intermediate CA than the carried intermediate-ca.crt"; bad=1; fi
    fi
  fi
  return "$bad"
}

restore_database() {
  local dump="$BDIR/db/$CENTRAL_DB.dump"
  [ -s "$dump" ] || die "bundle has no database dump (db/$CENTRAL_DB.dump)"
  local want_ts have_ts
  want_ts="$(jget "$BDIR/MANIFEST.json" 'd["db"]["timescaledb_version"]')"
  have_ts="$(pg_q -c "SELECT extversion FROM pg_extension WHERE extname='timescaledb'" || true)"
  if [ -n "$want_ts" ] && [ -n "$have_ts" ] && [ "$want_ts" != "$have_ts" ]; then
    die "the dump was taken with timescaledb $want_ts, this host runs $have_ts. A TimescaleDB dump restores only into
    the same extension version: use the same image as the old host."
  fi
  if [ -f "$CENTRAL_RESTORE_MARKER" ] && [ "$(jget "$CENTRAL_RESTORE_MARKER" 'd["bundle_id"]')" = "$(jget "$BDIR/MANIFEST.json" 'd["bundle_id"]')" ]; then
    say "database already restored from this bundle ($(jget "$CENTRAL_RESTORE_MARKER" 'd["restored_at"]')) — not restoring again"
    return 0
  fi
  local n; n="$(pg_public_table_count)"
  [ "$n" = 0 ] || die "the database already has $n tables. A restore goes into an EMPTY database only; this host is
    not clean (or was restored from a different bundle). Rebuild the infra volume deliberately if that is intended."
  say "restoring $(du -h "$dump" | cut -f1) dump (timescaledb_pre_restore → pg_restore → timescaledb_post_restore)"
  pg_q -c "SELECT timescaledb_pre_restore()" >/dev/null
  # post_restore MUST run even if pg_restore fails part-way, or the database stays in restore mode.
  local rc=0
  docker exec -i "$CENTRAL_PG_CONTAINER" pg_restore -U "$CENTRAL_DB_USER" -d "$CENTRAL_DB" --no-password \
    < "$dump" 2> "$WORK/pg_restore.err" || rc=$?
  pg_q -c "SELECT timescaledb_post_restore()" >/dev/null
  if [ "$rc" != 0 ]; then
    # pg_restore exits non-zero on ANY error, including benign ones about objects the image pre-creates. What
    # decides is the verification below, not the exit code — but every message is shown.
    warn "pg_restore reported errors (exit $rc):"; sed 's/^/        /' "$WORK/pg_restore.err" | head -40 >&2
  fi
  # VERIFY: the same rows in every table, the same hypertables, the same migration ledger.
  pg_rowcounts > "$WORK/rowcounts.after"
  if diff -u "$BDIR/db/rowcounts.tsv" "$WORK/rowcounts.after" > "$WORK/rowcounts.diff"; then
    say "row counts identical in all $(wc -l < "$WORK/rowcounts.after") tables"
  else
    cat "$WORK/rowcounts.diff" >&2
    die "the restored database does NOT match the exported one (row counts above). Do not start ctrlapi on it."
  fi
  local hw hh; hw="$(jget "$BDIR/MANIFEST.json" 'd["db"]["hypertables"]')"; hh="$(pg_hypertable_count)"
  [ "$hh" = "$hw" ] || die "hypertables: exported $hw, restored $hh — an unregistered hypertable refuses every write"
  say "hypertables registered: $hh"
  python3 - "$CENTRAL_RESTORE_MARKER" "$(jget "$BDIR/MANIFEST.json" 'd["bundle_id"]')" "$(jget "$BDIR/MANIFEST.json" 'd["source_host"]')" <<'PY'
import json, sys, datetime
json.dump({"bundle_id": sys.argv[2], "source_host": sys.argv[3],
           "restored_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")},
          open(sys.argv[1], "w"), indent=2)
PY
}

if [ "$MODE" = restore ]; then
  step "6/14 RESTORE: carry the existing Central's identity and data"
  open_bundle
  if [ "$YES" != 1 ]; then
    [ -t 0 ] || die "restore needs --yes when there is no terminal"
    echo
    echo "  This host becomes the Central that $(jget "$BDIR/MANIFEST.json" 'd["source_host"]') was."
    echo "  The old host must stay STOPPED from now on (central-export.sh --final): two Centrals issuing licences"
    echo "  from the same identity produce conflicting licence versions that appliances will refuse."
    printf '  Type the old host name to continue: '; read -r answer
    [ "$answer" = "$(jget "$BDIR/MANIFEST.json" 'd["source_host"]')" ] || die "not confirmed"
  fi
  say "checking the bundle's trust material belongs together"
  verify_trust_set "$BDIR/files" || die "the bundle's trust material is inconsistent (FAIL lines above) — nothing was installed"
  say "installing trust material (never over different content)"
  for item in "${CENTRAL_TRUST_ITEMS[@]}"; do
    IFS='|' read -r _kind path mode _what <<< "$item"
    install_trust_file "$path" "$mode"
  done
  for g in "${CENTRAL_TRUST_GLOBS[@]}"; do
    for f in "$BDIR"/files/$g; do [ -f "$f" ] && install_trust_file "${f#"$BDIR"/files/}" 0600; done
  done
  # The neutral CA-bundle name: carry the legacy file's content to it if the old host only had the old name.
  if [ ! -f "$CENTRAL_CA_BUNDLE" ] && [ -f "$BDIR/files/etc/stayconnect/pki/nats-ca-bundle.crt" ]; then
    run install -m 0644 "$BDIR/files/etc/stayconnect/pki/nats-ca-bundle.crt" "$CENTRAL_CA_BUNDLE"
    say "  carried  $CENTRAL_CA_BUNDLE (content of the legacy nats-ca-bundle.crt)"
  fi
  # Public halves: derived from the private keys when the bundle had none.
  for k in "$KEY_VENDOR" "$KEY_ASSIGN" "$KEY_REG"; do
    [ -f "${k%.key}.pub" ] || { [ "$DRY_RUN" = 1 ] || { ed25519_write_pub "$k" "${k%.key}.pub"; chmod 0644 "${k%.key}.pub"; }; }
  done
  if [ "$DRY_RUN" = 0 ]; then
    say "checking what is now installed on this host"
    verify_trust_set / || die "installed trust material is inconsistent (FAIL lines above)"
  fi
  # The Caddy copy of the :443 certificate.
  if [ -f "$CENTRAL_TLS_DIR/server.crt" ] && [ ! -f "$CADDY_TLS_CRT" ]; then
    run install -o root -g caddy -m 0644 "$CENTRAL_TLS_DIR/server.crt" "$CADDY_TLS_CRT"
    run install -o root -g caddy -m 0640 "$CENTRAL_TLS_DIR/server.key" "$CADDY_TLS_KEY"
  fi
  if [ "$DBQ_OK" = 1 ]; then
    restore_database
    say "cross-checking the database against the carried keys"
    db_trust_crosscheck || die "the database and the trust material do not belong to the same Central (see above)"
  else
    say "DRY-RUN would restore db/$CENTRAL_DB.dump with timescaledb_pre_restore/post_restore and verify row counts"
  fi
  [ -z "$TLS_MODE" ] && { if [ -f "$CENTRAL_TLS_DIR/ca.crt" ] || [ -f "$BDIR/files/opt/stayconnect/central/tls/ca.crt" ]; then TLS_MODE=internal; else TLS_MODE=acme; fi; }
else
  step "6/14 NEW: create a new Central identity"
  if [ "$DBQ_OK" = 1 ] && pg_table_exists appliances; then
    napp="$(pg_q -c "SELECT count(*) FROM appliances")"; nlic="$(pg_q -c "SELECT count(*) FROM licenses" 2>/dev/null || echo 0)"
    if [ "$napp" != 0 ] || [ "$nlic" != 0 ]; then
      for k in "$KEY_VENDOR" "$KEY_ASSIGN" "$KEY_REG" "$PKI_INT_KEY"; do
        [ -s "$k" ] || die "the database holds $napp appliances / $nlic licences but $k is missing.
    That is a Central being MOVED or repaired, not a new one: generating a key now would break every appliance.
    Use --mode restore with the old host's export bundle."
      done
    fi
  fi
  # A half-present appliance CA is not something to "complete": ctrlapi would pair a new root with an old
  # intermediate or vice versa.
  n=0; for f in "$PKI_ROOT_CRT" "$PKI_INT_CRT" "$PKI_INT_KEY"; do [ -e "$f" ] && n=$((n+1)); done
  [ "$n" = 0 ] || [ "$n" = 3 ] || die "the appliance CA under $SC_ETC/pki is partial ($n of 3 files). Refusing to guess."
  if [ -s "$KEY_VENDOR" ]; then say "vendor signing key exists — kept (key_id $(ed25519_keyid "$KEY_VENDOR" private))"
  else run env CTRLAPI_BIN="$CTRLAPI_BIN_PATH" CTRLAPI_VENDOR_KEY="$KEY_VENDOR" bash "$T/vendor-signing-key.sh" init; fi
  if [ -s "$KEY_ASSIGN" ]; then say "assignment signing key exists — kept"
  else run "$CTRLAPI_BIN_PATH" gen-assignment-key --out "$KEY_ASSIGN" --pub-out "${KEY_ASSIGN%.key}.pub"; fi
  if [ -s "$KEY_REG" ]; then say "registry root key exists — kept"
  else run "$CTRLAPI_BIN_PATH" gen-registry-key --out "$KEY_REG" --pub-out "${KEY_REG%.key}.pub"; fi
  [ -z "$TLS_MODE" ] && TLS_MODE=internal
  if [ "$TLS_MODE" = internal ]; then run bash "$T/central-mint-tls.sh" --init-ca; fi
  if [ "$n" = 3 ]; then say "appliance CA exists — kept (root $(cert_fpr "$PKI_ROOT_CRT" | cut -c1-23)…)"
  else say "the appliance Root/Intermediate CA is created by ctrlapi on its first start (step 11)"; fi
fi
case "$TLS_MODE" in internal|acme) ;; *) die "--tls must be internal or acme" ;; esac
[ "$TLS_MODE" = acme ] || [ "$DRY_RUN" = 1 ] || [ -s "$CENTRAL_TLS_DIR/ca.crt" ] || die "--tls internal but no Central TLS CA at $CENTRAL_TLS_DIR"

# ================================================================= 7. schema
step "7/14 schema (central-migrate.sh)"
MIG=(bash "$T/central-migrate.sh" --pg-exec "docker exec -i $CENTRAL_PG_CONTAINER" --db "$CENTRAL_DB")
if [ "$DRY_RUN" = 1 ]; then
  say "DRY-RUN would run: ${MIG[*]} up && ${MIG[*]} verify"
else
  "${MIG[@]}" up
  "${MIG[@]}" verify
fi

# ================================================================= 8. configuration
step "8/14 configuration (ctrlapi.env, central-endpoint.env, install settings)"
# Names the :443 site answers for: the appliance-facing name first, then the console name(s), then extras.
if [ -z "$ADMIN_NAMES" ] && [ "$MODE" = restore ] && [ -f "$BDIR/config/ctrlapi.env" ]; then
  ADMIN_NAMES="$(envfile_get "$BDIR/config/ctrlapi.env" CTRLAPI_ALLOW_ORIGINS | tr ',' '\n' \
    | sed -n 's|^[[:space:]]*https://\([^/:]*\).*|\1|p' | grep -v -E '^(localhost|127\.0\.0\.1)$' | paste -sd, - || true)"
fi
[ -n "$ADMIN_NAMES" ] || ADMIN_NAMES="$PRIMARY"
SITE_LIST="$(printf '%s,%s,%s' "$PRIMARY" "$ADMIN_NAMES" "$EXTRA_NAMES" | tr ',' '\n' | sed 's/[[:space:]]//g' | sed '/^$/d' | awk '!seen[$0]++' | paste -sd, -)"
SITE_ADDRESSES="$(printf '%s' "$SITE_LIST" | sed 's/,/, /g')"
TLS_SANS="$(printf '%s' "$SITE_LIST" | tr ',' '\n' | grep -vx "$PRIMARY" | paste -sd, - || true)"
ORIGINS="$(printf '%s' "$ADMIN_NAMES" | tr ',' '\n' | sed 's/[[:space:]]//g; /^$/d; s|^|https://|' | paste -sd, -)"
say "site addresses: $SITE_ADDRESSES"
if [ "$TLS_MODE" = acme ] && printf '%s' "$SITE_LIST" | tr ',' '\n' | grep -qE '^[0-9.]+$'; then
  warn "--tls acme with IP addresses in the site list: a public CA will not issue for those; drop them or use --tls internal"
fi
say "console origins: $ORIGINS"

if [ -f "$CTRLAPI_ENV_PATH" ]; then
  say "$CTRLAPI_ENV_PATH exists — kept as is"
  grep -qE "^[^#]*CHANGE_ME" "$CTRLAPI_ENV_PATH" && die "$CTRLAPI_ENV_PATH still contains CHANGE_ME placeholders"
else
  DBPW="$(cat "$CENTRAL_SECRETS_DIR/db_password" 2>/dev/null || echo DRYRUN)"
  RDPW="$(cat "$CENTRAL_SECRETS_DIR/redis_password" 2>/dev/null || echo DRYRUN)"
  sed -e "s|CHANGE_ME_DB_PASSWORD|$DBPW|" -e "s|CHANGE_ME_REDIS_PASSWORD|$RDPW|" \
      -e "s|^CTRLAPI_ALLOW_ORIGINS=.*|CTRLAPI_ALLOW_ORIGINS=$ORIGINS|" \
      -e "s|^CTRLAPI_APPLIANCE_BASE=.*|CTRLAPI_APPLIANCE_BASE=$APPLIANCE_BASE|" \
      "$REL/deploy/env/ctrlapi.env.example" > "$WORK/ctrlapi.env"
  if [ "$MODE" = restore ] && [ -f "$BDIR/config/ctrlapi.env" ]; then
    # Operational settings carry over; connection strings and file paths are this host's own.
    for k in CTRLAPI_MTLS_SANS CTRLAPI_LOG_LEVEL CTRLAPI_ASSIGN_TTL_SECONDS CTRLAPI_SERVER_TLS_ROTATE_DAYS CTRLAPI_MTLS_ADDR; do
      if v="$(envfile_get "$BDIR/config/ctrlapi.env" "$k")"; then
        DRY_RUN=0 envfile_set "$WORK/ctrlapi.env" "$k" "$v"; say "  carried setting $k"
      fi
    done
    for k in $(grep -oE '^[[:space:]]*CTRLAPI_[A-Z0-9_]+' "$BDIR/config/ctrlapi.env" | tr -d ' ' | sort -u); do
      grep -qE "^[#[:space:]]*$k=" "$WORK/ctrlapi.env" || warn "old ctrlapi.env set $k, which this release does not know — not carried; review $BDIR/config/ctrlapi.env"
    done
  fi
  if [ -n "$TLS_SANS" ] && ! envfile_get "$WORK/ctrlapi.env" CTRLAPI_MTLS_SANS >/dev/null; then
    DRY_RUN=0 envfile_set "$WORK/ctrlapi.env" CTRLAPI_MTLS_SANS "$TLS_SANS"
  fi
  write_file "$CTRLAPI_ENV_PATH" 0600 < "$WORK/ctrlapi.env"
  say "wrote $CTRLAPI_ENV_PATH"
fi
run env DEPLOY="$REL/deploy" bash "$T/install-central-endpoint.sh" "$ENDPOINT_SRC"

write_file "$CENTRAL_INSTALL_ENV" 0644 <<EOF
# Written by central-install.sh ($STAMP, mode $MODE). Read by central-deploy.sh to re-render the Caddyfile.
CENTRAL_PRIMARY_NAME=$PRIMARY
CENTRAL_ADMIN_NAMES=$ADMIN_NAMES
CENTRAL_SITE_ADDRESSES=$SITE_LIST
CENTRAL_TLS_MODE=$TLS_MODE
CENTRAL_TLS_SANS=$TLS_SANS
CENTRAL_ACME_EMAIL=$ACME_EMAIL
CENTRAL_INSTALL_MODE=$MODE
EOF

# ================================================================= 9. units
step "9/14 systemd units + backup retention"
install_unit() {
  local src="$1" dest="/etc/systemd/system/$2"
  if [ -f "$dest" ] && cmp -s "$src" "$dest"; then say "unit $2 current"
  else [ -f "$dest" ] && run cp -a "$dest" "$dest.bak-$STAMP"; run install -m 0644 "$src" "$dest"; say "installed unit $2"; fi
}
install_unit "$REL/deploy/systemd/stayconnect-ctrlapi.service"       stayconnect-ctrlapi.service
install_unit "$REL/deploy/systemd/stayconnect-cloud-admin.service"   stayconnect-cloud-admin.service
install_unit "$REL/deploy/caddy/stayconnect-caddy.central.service"   stayconnect-caddy.service
install_unit "$REL/deploy/systemd/stayconnect-backup-cleanup.service" stayconnect-backup-cleanup.service
install_unit "$REL/deploy/systemd/stayconnect-backup-cleanup.timer"   stayconnect-backup-cleanup.timer
run install -m 0755 "$REL/deploy/scripts/stayconnect-backup-cleanup.sh" "$SC_OPT/bin/stayconnect-backup-cleanup"
[ -f "$SC_ETC/backup-retention.conf" ] || run install -m 0644 "$REL/deploy/scripts/backup-retention.conf" "$SC_ETC/backup-retention.conf"
run systemctl daemon-reload
# The apt package's own caddy.service would fight stayconnect-caddy for :443.
caddy_state="$(systemctl is-enabled caddy.service 2>/dev/null || true)"
if [ -n "$caddy_state" ] && [ "$caddy_state" != masked ] && [ "$caddy_state" != not-found ]; then
  run systemctl disable --now caddy.service || true
  run systemctl mask caddy.service || true
fi
run systemctl enable stayconnect-ctrlapi.service stayconnect-cloud-admin.service stayconnect-caddy.service stayconnect-backup-cleanup.timer

# ================================================================= 10. console release
step "10/14 console release"
CUR_BID=""; [ -f "$CONSOLE_CURRENT/.next/BUILD_ID" ] && CUR_BID="$(cat "$CONSOLE_CURRENT/.next/BUILD_ID")"
NEW_BID="$(cat "$REL/cloud-admin/.next/BUILD_ID")"
if [ "$CUR_BID" = "$NEW_BID" ]; then
  say "console BUILD_ID $NEW_BID already current"
else
  CREL="$CONSOLE_RELEASES/$STAMP"
  run cp -a "$REL/cloud-admin" "$CREL"
  run chown -R root:root "$CREL"
  run chmod -R a+rX,go-w "$CREL"
  if [ -L "$CONSOLE_CURRENT" ]; then
    run ln -sfn "$(readlink -f "$CONSOLE_CURRENT")" "$CONSOLE_PREVIOUS.tmp"; run mv -Tf "$CONSOLE_PREVIOUS.tmp" "$CONSOLE_PREVIOUS"
  else
    # First install: previous = current, so the rollback pointer is valid from day one.
    run ln -sfn "$CREL" "$CONSOLE_PREVIOUS.tmp"; run mv -Tf "$CONSOLE_PREVIOUS.tmp" "$CONSOLE_PREVIOUS"
  fi
  run ln -sfn "$CREL" "$CONSOLE_CURRENT.tmp"; run mv -Tf "$CONSOLE_CURRENT.tmp" "$CONSOLE_CURRENT"
  say "console release $CREL (BUILD_ID $NEW_BID)"
fi

# ================================================================= 11. start ctrlapi
step "11/14 start ctrlapi"
if [ "$DRY_RUN" = 1 ]; then
  say "DRY-RUN would start stayconnect-ctrlapi and wait for /readyz"
else
  PRE_ROOT=""; PRE_INT=""
  if [ "$MODE" = restore ]; then
    # The last line of defence against a silent new CA: ctrlapi only mints one when these are missing.
    for f in "$PKI_ROOT_CRT" "$PKI_INT_CRT" "$PKI_INT_KEY" "$KEY_VENDOR" "$KEY_ASSIGN" "$KEY_REG"; do
      [ -s "$f" ] || die "$f is missing — refusing to start ctrlapi (it would create a NEW identity)"
    done
    PRE_ROOT="$(sha256_of "$PKI_ROOT_CRT")"; PRE_INT="$(sha256_of "$PKI_INT_CRT")"
  fi
  unit_restart_noblock stayconnect-ctrlapi
  ctrlapi_ready 90 || { journalctl -u stayconnect-ctrlapi -n 60 --no-pager >&2 || true; die "ctrlapi did not become ready"; }
  if [ "$MODE" = restore ]; then
    [ "$(sha256_of "$PKI_ROOT_CRT")" = "$PRE_ROOT" ] && [ "$(sha256_of "$PKI_INT_CRT")" = "$PRE_INT" ] \
      || die "the appliance CA certificates CHANGED when ctrlapi started. Stop it now (systemctl stop stayconnect-ctrlapi) and investigate."
    db_trust_crosscheck || die "after start, the database no longer matches the carried CA"
    say "appliance CA unchanged by start-up"
  else
    for f in "$PKI_ROOT_CRT" "$PKI_INT_CRT" "$PKI_INT_KEY"; do [ -s "$f" ] || die "ctrlapi started but did not create $f"; done
    say "appliance CA present: root $(cert_fpr "$PKI_ROOT_CRT" | cut -c1-23)…"
  fi
  if [ ! -s "$CENTRAL_CA_BUNDLE" ]; then
    cat "$PKI_INT_CRT" "$PKI_ROOT_CRT" | write_file "$CENTRAL_CA_BUNDLE" 0644
    say "wrote $CENTRAL_CA_BUNDLE (intermediate + root); restarting ctrlapi so offline packages carry it"
    unit_restart_noblock stayconnect-ctrlapi
    ctrlapi_ready 90 || die "ctrlapi did not become ready after the CA-bundle restart"
  fi
  aid="$(ed25519_keyid "$KEY_ASSIGN" private)"
  st="$(pg_q -c "SELECT state FROM assignment_signing_keys WHERE key_id='$aid'" || true)"
  case "$st" in active|verify_only) say "assignment key $aid registered ($st)" ;;
    *) warn "assignment key $aid is not registered as signing (state '${st:-none}') — activation will refuse; see journalctl -u stayconnect-ctrlapi" ;; esac
fi

# ================================================================= 12. first platform admin
step "12/14 platform admin"
if [ "$MODE" = new ] && [ "$SKIP_ADMIN" = 0 ]; then
  if [ "$DRY_RUN" = 1 ]; then
    say "DRY-RUN would seed platform admin ${ADMIN_EMAIL:-<--admin-email>} (password from CENTRAL_ADMIN_PASSWORD or prompt)"
  else
    have="$(pg_q -c "SELECT count(*) FROM operators o JOIN operator_roles r ON r.operator_id=o.id
                     WHERE r.tenant_id IS NULL AND r.role IN ('platform_admin','platform_owner') AND o.status='active'")"
    if [ "$have" != 0 ]; then
      say "an active platform admin already exists — not seeding (ctrlapi seed-admin resets one on purpose)"
    else
      [ -n "$ADMIN_EMAIL" ] || die "--admin-email is required for the first platform admin (or --skip-admin)"
      if [ -z "${CENTRAL_ADMIN_PASSWORD:-}" ]; then
        [ -t 0 ] || die "set CENTRAL_ADMIN_PASSWORD (no terminal to prompt on)"
        printf 'Password for %s (min 10 chars): ' "$ADMIN_EMAIL" >&2; read -rs CENTRAL_ADMIN_PASSWORD; echo >&2
        printf 'Again: ' >&2; read -rs p2; echo >&2
        [ "$CENTRAL_ADMIN_PASSWORD" = "$p2" ] || die "passwords differ"
      fi
      [ "${#CENTRAL_ADMIN_PASSWORD}" -ge 10 ] || die "admin password must be at least 10 characters"
      CTRLAPI_DB_URL="$(envfile_get "$CTRLAPI_ENV_PATH" CTRLAPI_DB_URL)" \
        "$CTRLAPI_BIN_PATH" seed-admin --email "$ADMIN_EMAIL" --password "$CENTRAL_ADMIN_PASSWORD" >/dev/null
      unset CENTRAL_ADMIN_PASSWORD
      say "seeded platform admin $ADMIN_EMAIL"
    fi
  fi
else
  say "not seeding (mode $MODE$( [ "$SKIP_ADMIN" = 1 ] && echo ', --skip-admin')); operators came with the database"
fi

# ================================================================= 13. console, Caddy, firewall
step "13/14 console + Caddy + firewall"
unit_restart_noblock stayconnect-cloud-admin
if [ "$DRY_RUN" = 0 ]; then
  ok=0; for i in $(seq 1 45); do
    if curl -fsS -o /dev/null --max-time 2 http://127.0.0.1:3000/login 2>/dev/null; then ok=1; break; fi; sleep 1; done
  [ "$ok" = 1 ] || { journalctl -u stayconnect-cloud-admin -n 40 --no-pager >&2 || true; die "the console did not serve /login on :3000"; }
  say "console serving on 127.0.0.1:3000"
fi

if [ "$TLS_MODE" = internal ]; then
  run env CENTRAL_TLS_SANS="$TLS_SANS" CENTRAL_TLS_DIR="$CENTRAL_TLS_DIR" DEPLOY="$REL/deploy" \
      CENTRAL_ENDPOINT_CONFIG="$ENDPOINT_SRC" bash "$T/central-mint-tls.sh"
fi
render_caddyfile "$REL/deploy/caddy/Caddyfile.central" "$SITE_ADDRESSES" "$TLS_MODE" "$ACME_EMAIL" > "$WORK/Caddyfile"
if [ -f "$CADDYFILE_PATH" ] && cmp -s "$WORK/Caddyfile" "$CADDYFILE_PATH"; then say "Caddyfile current"
else
  [ -f "$CADDYFILE_PATH" ] && run cp -a "$CADDYFILE_PATH" "$CADDYFILE_PATH.bak-$STAMP"
  write_file "$CADDYFILE_PATH" 0644 < "$WORK/Caddyfile"
fi
# Log file first, owned by caddy: `caddy validate` as root would otherwise create it root-owned.
[ -f /var/log/caddy/central.log ] || run install -o caddy -g caddy -m 0640 /dev/null /var/log/caddy/central.log
run caddy validate --config "$CADDYFILE_PATH" --adapter caddyfile
unit_restart_noblock stayconnect-caddy

if [ "$SKIP_FW" = 1 ]; then say "firewall left alone (--skip-firewall). Required: 443/tcp and 9443/tcp public, nothing else."
else run env DEPLOY="$REL/deploy" CENTRAL_ENDPOINT_CONFIG="$ENDPOINT_SRC" bash "$T/central-firewall.sh" --enable; fi
run systemctl start stayconnect-backup-cleanup.timer

# ================================================================= 14. public trust, provenance, smoke
step "14/14 public trust material, provenance, smoke test"
if [ "$DRY_RUN" = 0 ]; then
  cp -f "$KEY_VENDOR.pub" "$CENTRAL_PUBLIC_TRUST_DIR/vendor-license.pub" 2>/dev/null || ed25519_write_pub "$KEY_VENDOR" "$CENTRAL_PUBLIC_TRUST_DIR/vendor-license.pub"
  ed25519_write_pub "$KEY_REG" "$CENTRAL_PUBLIC_TRUST_DIR/assignment-registry-root.pub"
  cp -f "$PKI_ROOT_CRT" "$CENTRAL_PUBLIC_TRUST_DIR/appliance-root-ca.crt"
  [ -f "$CENTRAL_TLS_DIR/ca.crt" ] && cp -f "$CENTRAL_TLS_DIR/ca.crt" "$CENTRAL_PUBLIC_TRUST_DIR/central-tls-ca.crt"
  chmod 0644 "$CENTRAL_PUBLIC_TRUST_DIR"/*
  {
    echo "OneGate Central public trust material — $(hostname) — $STAMP"
    echo "vendor licence key_id:        $(ed25519_keyid "$KEY_VENDOR" private)"
    echo "assignment signing key_id:    $(ed25519_keyid "$KEY_ASSIGN" private)"
    echo "registry root key_id:         $(ed25519_keyid "$KEY_REG" private)"
    echo "appliance Root CA sha256:     $(cert_fpr "$PKI_ROOT_CRT")"
    [ -f "$CENTRAL_TLS_DIR/ca.crt" ] && echo "Central TLS CA sha256:        $(cert_fpr "$CENTRAL_TLS_DIR/ca.crt")"
  } > "$CENTRAL_PUBLIC_TRUST_DIR/FINGERPRINTS.txt"
  python3 - "$CENTRAL_DEPLOYED_JSON" "$REL_COMMIT" "$(sha256_of "$CTRLAPI_BIN_PATH")" "$(binary_revision "$CTRLAPI_BIN_PATH")" \
    "$(cat "$CONSOLE_CURRENT/.next/BUILD_ID")" "$(jget "$CONSOLE_CURRENT/$CONSOLE_MANIFEST" 'd["source_commit"]')" "$MODE" <<'PY'
import json, sys, datetime
out, commit, csum, crev, bid, ccommit, mode = sys.argv[1:8]
json.dump({"release_commit": commit, "ctrlapi_sha256": csum, "ctrlapi_vcs_revision": crev,
           "console_build_id": bid, "console_source_commit": ccommit, "installed_by": "central-install.sh",
           "install_mode": mode,
           "at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")},
          open(out, "w"), indent=2)
PY
  say "provenance written to $CENTRAL_DEPLOYED_JSON (read back from the installed files)"
  run env DEPLOY="$REL/deploy" bash "$T/central-deploy.sh" smoke
  CENTRAL_ENDPOINT_FILE="$CENTRAL_ENDPOINT_PATH" CTRLAPI_ENV_FILE="$CTRLAPI_ENV_PATH" DEPLOY="$REL/deploy" \
    CENTRAL_ENDPOINT_CONFIG="$ENDPOINT_SRC" \
    bash "$T/central-preflight.sh" --pg-exec "docker exec -i $CENTRAL_PG_CONTAINER" --db "$CENTRAL_DB" \
    || die "preflight reports this Central cannot activate appliances (FAIL lines above)"
else
  say "DRY-RUN would publish public trust to $CENTRAL_PUBLIC_TRUST_DIR, write $CENTRAL_DEPLOYED_JSON, run smoke + preflight"
fi

echo
say "================================================================"
say "OneGate Central installed ($MODE) — release $REL_NAME"
say "  console + API:   https://$PRIMARY   (also: $SITE_ADDRESSES)"
say "  appliance mTLS:  $(envfile_get "$ENDPOINT_SRC" CENTRAL_MTLS_BASE 2>/dev/null || echo "https://$PRIMARY:9443")"
say "  public trust for appliance provisioning: $CENTRAL_PUBLIC_TRUST_DIR"
if [ "$MODE" = new ]; then
  say ""
  say "KEY CUSTODY — do these now, before this host can be lost:"
  say "  1. central-export.sh --out /secure/media   (encrypted: every key + CA + the database)"
  say "  2. move $SC_ETC/pki-offline/root-ca.key to cold storage, then delete it here"
  say "  3. publish FINGERPRINTS.txt out of band; put vendor-license.pub and assignment-registry-root.pub in"
  say "     deploy/pki/ for appliance provisioning (not committed — see deploy/pki/README.md)"
else
  say ""
  say "NEXT: point DNS for $PRIMARY at this host. Appliances need no change. Keep the old host stopped."
fi
