#!/usr/bin/env bash
# EXPORT AN EXISTING ONEGATE CENTRAL — everything a new host needs to BECOME it (central-install.sh --mode restore).
#
#   !!! THE BUNDLE THIS WRITES IS THE MOST SENSITIVE FILE THE PRODUCT HAS. !!!
#   It contains the vendor licence signing key, the assignment signing key, the assignment registry root key,
#   the appliance intermediate CA key (and the Root CA key if it is still on this host), the Central TLS CA key,
#   and a full copy of the licence database. Anyone holding it can mint licences and certificates that every
#   appliance in the field will accept. It is encrypted by default (AES-256, PBKDF2 600k); keep the passphrase
#   apart from the file, move it over an encrypted channel, and destroy every copy once the move is verified.
#
# Usage (as root on the Central being exported):
#   central-export.sh [--out <dir>]            snapshot: ctrlapi is stopped for the few seconds of the dump, then
#                                              restarted. For rehearsals and for the key-custody escrow copy.
#   central-export.sh --final [--out <dir>]    THE MOVE: ctrlapi and the console are stopped AND DISABLED and stay
#                                              down. After the new host takes over, this one must never issue
#                                              another licence (two Centrals with one identity = conflicting
#                                              licence versions that appliances refuse).
#   --no-encrypt   write a plaintext .tar.gz (0600). Only onto media that is itself encrypted.
#   --dry-run      list what would be exported; stop nothing, write nothing.
# Passphrase: $CENTRAL_BUNDLE_PASSPHRASE, or prompted twice.
#
# Nothing here modifies trust material or the database. It reads them, and (only) stops ctrlapi while dumping
# so that the database and its recorded row counts are one consistent snapshot.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROG=central-export
# shellcheck source=central-lib.sh
. "$HERE/central-lib.sh"

OUT="/root/central-export" FINAL=0 ENCRYPT=1
while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --final) FINAL=1; shift ;;
    --no-encrypt) ENCRYPT=0; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
export DRY_RUN
need_root
command -v docker >/dev/null || die "docker not found"
docker inspect "$CENTRAL_PG_CONTAINER" >/dev/null 2>&1 || die "no $CENTRAL_PG_CONTAINER container on this host"

STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
HOST="$(hostname)"
KIND=snapshot; [ "$FINAL" = 1 ] && KIND=final
NAME="central-export-$HOST-$STAMP"

step "what will be exported from $HOST ($KIND)"
say "trust material:"
present=0
for item in "${CENTRAL_TRUST_ITEMS[@]}"; do
  IFS='|' read -r kind path _mode what <<< "$item"
  if [ -f "/$path" ]; then printf '  +  /%-52s %s\n' "$path" "$what"; present=$((present+1))
  elif [ "$kind" = req ]; then printf '  !! /%-52s MISSING (required)\n' "$path"
  fi
done
for g in "${CENTRAL_TRUST_GLOBS[@]}"; do for f in /$g; do [ -f "$f" ] && printf '  +  %s\n' "$f"; done; done
say "database: $CENTRAL_DB in $CENTRAL_PG_CONTAINER (pg_dump -Fc) + per-table row counts"
say "configuration (reference only; the new host renders its own): ctrlapi.env, central-endpoint.env, Caddyfile, compose file"

# A Central whose trust set does not hold together must not be propagated to a second host.
echo
say "checking the trust material on this host belongs together"
verify_trust_set / || die "this host's trust material is inconsistent (FAIL above). Fix that before moving it anywhere."

if [ "$DRY_RUN" = 1 ]; then say "DRY RUN — nothing stopped, nothing written"; exit 0; fi

if [ "$ENCRYPT" = 1 ] && [ -z "${CENTRAL_BUNDLE_PASSPHRASE:-}" ]; then
  [ -t 0 ] || die "set CENTRAL_BUNDLE_PASSPHRASE (no terminal to prompt on), or --no-encrypt onto encrypted media"
  printf 'Passphrase for the export bundle: ' >&2; read -rs CENTRAL_BUNDLE_PASSPHRASE; echo >&2
  printf 'Again: ' >&2; read -rs p2; echo >&2
  [ "$CENTRAL_BUNDLE_PASSPHRASE" = "$p2" ] || die "passphrases differ"
  [ "${#CENTRAL_BUNDLE_PASSPHRASE}" -ge 12 ] || die "use a passphrase of at least 12 characters"
fi

umask 077
install -d -m 0700 "$OUT"
STAGE="$(mktemp -d "$OUT/.stage.XXXXXX")"
B="$STAGE/$NAME"
mkdir -p "$B/files" "$B/config" "$B/db"
cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT

step "trust material"
for item in "${CENTRAL_TRUST_ITEMS[@]}"; do
  IFS='|' read -r _kind path mode _what <<< "$item"
  [ -f "/$path" ] || continue
  install -D -m "$mode" "/$path" "$B/files/$path"
done
for g in "${CENTRAL_TRUST_GLOBS[@]}"; do
  for f in /$g; do [ -f "$f" ] && install -D -m 0600 "$f" "$B/files/${f#/}"; done
done
say "$(find "$B/files" -type f | wc -l) files"
for f in "$CTRLAPI_ENV_PATH" "$CENTRAL_ENDPOINT_PATH" "$CADDYFILE_PATH" "$CENTRAL_INSTALL_ENV" \
         "$CENTRAL_ROOT/compose/infra.yml" "$CENTRAL_COMPOSE_FILE" "$CENTRAL_DEPLOYED_JSON"; do
  [ -f "$f" ] && install -m 0600 "$f" "$B/config/$(basename "$f")"
done

step "database (consistent snapshot)"
was_active=0
systemctl is-active --quiet stayconnect-ctrlapi 2>/dev/null && was_active=1
restart_ctrlapi() {
  if [ "$FINAL" = 0 ] && [ "$was_active" = 1 ]; then
    unit_restart_noblock stayconnect-ctrlapi
    ctrlapi_ready 60 || warn "ctrlapi did not report ready after the export — check journalctl -u stayconnect-ctrlapi"
  fi
}
trap 'restart_ctrlapi; cleanup' EXIT
if [ "$was_active" = 1 ]; then
  say "stopping ctrlapi so no write lands between the dump and its row counts (appliances retry; guests unaffected)"
  systemctl stop stayconnect-ctrlapi
fi
if [ "$FINAL" = 1 ]; then
  systemctl stop stayconnect-cloud-admin 2>/dev/null || true
  systemctl disable stayconnect-ctrlapi stayconnect-cloud-admin >/dev/null 2>&1 || true
  say "FINAL: ctrlapi and the console are stopped and disabled on $HOST"
fi
# pg_dump always warns about TimescaleDB's circular catalog FKs (harmless for a full dump); shown only on failure.
docker exec "$CENTRAL_PG_CONTAINER" pg_dump -U "$CENTRAL_DB_USER" -d "$CENTRAL_DB" -Fc > "$B/db/$CENTRAL_DB.dump" 2> "$STAGE/pg_dump.err" \
  || { cat "$STAGE/pg_dump.err" >&2; die "pg_dump failed"; }
docker exec -i "$CENTRAL_PG_CONTAINER" pg_restore -l < "$B/db/$CENTRAL_DB.dump" >/dev/null || die "the dump is not a readable archive"
pg_rowcounts > "$B/db/rowcounts.tsv"
HYPER="$(pg_hypertable_count)"
TSV="$(pg_q -c "SELECT extversion FROM pg_extension WHERE extname='timescaledb'")"
PGV="$(pg_q -c "SHOW server_version")"
LASTMIG="$(pg_q -c "SELECT max(version) FROM schema_migrations" 2>/dev/null || true)"
say "dump $(du -h "$B/db/$CENTRAL_DB.dump" | cut -f1), $(wc -l < "$B/db/rowcounts.tsv") tables, $HYPER hypertables, last migration $LASTMIG"
restart_ctrlapi
trap cleanup EXIT

step "manifest + checksums"
key_id() { [ -f "$B/files/$1" ] && ed25519_keyid "$B/files/$1" private || true; }
python3 - "$B/MANIFEST.json" "$NAME" "$HOST" "$KIND" "$STAMP" "$TSV" "$PGV" "$HYPER" "$LASTMIG" \
  "$(key_id etc/stayconnect/vendor-license.key)" "$(key_id etc/stayconnect/assignment-signing.key)" \
  "$(key_id etc/stayconnect/assignment-registry-root.key)" "$(cert_fpr /etc/stayconnect/pki/root-ca.crt)" <<'PY'
import json, sys
(out, bid, host, kind, at, tsv, pgv, hyper, lastmig, vk, ak, rk, rootfpr) = sys.argv[1:14]
json.dump({
  "_warning": "CONTAINS PRIVATE SIGNING KEYS, CA KEYS AND THE LICENCE DATABASE. Treat as the vendor's root of trust.",
  "bundle_id": bid, "source_host": host, "kind": kind, "created_at": at,
  "db": {"file": "db/stayconnect.dump", "rowcounts": "db/rowcounts.tsv", "timescaledb_version": tsv,
         "server_version": pgv, "hypertables": int(hyper or 0), "last_migration": lastmig},
  "identity": {"vendor_key_id": vk, "assignment_key_id": ak, "registry_root_key_id": rk,
               "appliance_root_ca_sha256": rootfpr},
}, open(out, "w", encoding="utf-8"), indent=2)
PY
cat > "$B/README.txt" <<EOF
OneGate Central export — $HOST — $STAMP ($KIND)

THIS DIRECTORY HOLDS THE VENDOR'S PRIVATE SIGNING KEYS, CA KEYS AND THE LICENCE DATABASE.

Restore on a new host:  central-install.sh --mode restore --bundle <this bundle>
See docs/DEPLOYMENT_CLOUD.md ("Move Central to a new host").
EOF
( cd "$B" && find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS )

if [ "$ENCRYPT" = 1 ]; then
  F="$OUT/$NAME.tar.gz.enc"
  tar -C "$STAGE" -cz "$NAME" | CENTRAL_BUNDLE_PASSPHRASE="$CENTRAL_BUNDLE_PASSPHRASE" \
    openssl enc -aes-256-cbc -pbkdf2 -iter 600000 -salt -pass env:CENTRAL_BUNDLE_PASSPHRASE -out "$F"
  # Prove it decrypts and is complete before anyone relies on it.
  CENTRAL_BUNDLE_PASSPHRASE="$CENTRAL_BUNDLE_PASSPHRASE" openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 \
    -pass env:CENTRAL_BUNDLE_PASSPHRASE -in "$F" | tar -tz >/dev/null || die "the encrypted bundle does not read back"
else
  F="$OUT/$NAME.tar.gz"
  tar -C "$STAGE" -czf "$F" "$NAME"
  warn "the bundle is NOT encrypted. It must only ever sit on encrypted media."
fi
chmod 0600 "$F"
sha256sum "$F" > "$F.sha256"

echo
say "EXPORT WRITTEN: $F"
say "  sha256 $(cut -d' ' -f1 < "$F.sha256")"
say "  identity: vendor $(jget "$B/MANIFEST.json" 'd["identity"]["vendor_key_id"]'), assignment $(jget "$B/MANIFEST.json" 'd["identity"]["assignment_key_id"]'), registry $(jget "$B/MANIFEST.json" 'd["identity"]["registry_root_key_id"]')"
say ""
say "IT CONTAINS EVERY PRIVATE KEY OF THIS CENTRAL. Move it encrypted, keep the passphrase separate, delete"
say "every copy once the new host is verified."
if [ "$FINAL" = 1 ]; then
  say ""
  say "THIS HOST IS NOW STOPPED AS CENTRAL (ctrlapi + console disabled). Do not re-enable it once the new host"
  say "has issued anything. Next: central-install.sh --mode restore on the new host, then switch DNS."
fi
