#!/usr/bin/env bash
# RESTORE AN APPLIANCE'S /etc/stayconnect FROM ITS ENCRYPTED CONFIGURATION ARCHIVE — so a rebuilt machine is the
# SAME appliance (identity, certificate, assignment, licence, data-encryption keys), not a new one.
#
# Every scd backup writes etc-<stamp>.tgz.age beside the database dump (data-plane/cmd/scd/backups.go,
# writeEtcArchive), but nothing could restore it: no code reads that archive back. Two mistakes were easy to make
# by hand, and both were found in review of the first written procedure:
#
#   * the archive's paths are relative to /etc ("stayconnect/scd.env"), so extracting it with -C /etc/stayconnect
#     produced /etc/stayconnect/stayconnect/... and left the freshly generated identity and keys in use;
#   * restoring the old *.env and .dsn files replaced the database passwords with the OLD appliance's, while the
#     rebuilt machine's PostgreSQL holds the NEW ones (pg_dump does not carry role passwords). scd could then not
#     reach its database, and the database restore that must follow could never start.
#
# So this script restores everything in the archive EXCEPT the database credentials, which it reconciles: every
# *_DB_URL / *_DSN line in a restored env file takes the value this machine's provisioning generated, and .dsn and
# secrets/pg-superuser.env are kept as they are. The previous /etc/stayconnect is saved first.
#
# Order of a rebuild (docs/runbooks/ONEGATE_FRESH_VM_INSTALLATION_MANUAL.md, E.4 case 2):
#   1. install the same (or a newer) release on the new machine WITHOUT activating it in Central;
#   2. this script (services are stopped by it);
#   3. start scd, then restore the database: scd backup-restore --name <db backup> --identity-file <holder key>;
#   4. start the rest and verify.
#
# Usage (root): restore-appliance-config.sh --archive /path/etc-<stamp>.tgz.age --identity /path/holder.key [--yes]
#                                            [--list]   (decrypt and list the archive; change nothing)
set -euo pipefail
ARCHIVE=""; IDENTITY=""; YES=0; LIST=0
while [ $# -gt 0 ]; do
  case "$1" in
    --archive) ARCHIVE="${2:?}"; shift 2;;
    --identity) IDENTITY="${2:?}"; shift 2;;
    --yes) YES=1; shift;;
    --list) LIST=1; shift;;
    -h|--help) sed -n '2,30p' "$0"; exit 0;;
    *) echo "restore-appliance-config: unknown argument $1" >&2; exit 1;;
  esac
done
ETC="${SC_ETC_DIR:-/etc/stayconnect}"
say() { echo "[restore-config] $*"; }
die() { echo "[restore-config] REFUSED (nothing changed): $*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || [ -n "${SC_ETC_DIR:-}" ] || die "run as root"
[ -f "$ARCHIVE" ] || die "--archive is required and must exist"
[ -f "$IDENTITY" ] || die "--identity (a backup key holder's PRIVATE key file) is required"
command -v age >/dev/null || die "age is not installed (apt-get install age)"

T="$(mktemp -d)"; chmod 0700 "$T"; trap 'rm -rf "$T"' EXIT
age -d -i "$IDENTITY" "$ARCHIVE" | tar -xzf - -C "$T" || die "could not decrypt/extract $ARCHIVE with that key"
[ -d "$T/stayconnect" ] || die "the archive does not contain stayconnect/ (paths are relative to /etc)"
if [ "$LIST" = 1 ]; then ( cd "$T" && find stayconnect -type f | sort ); exit 0; fi
[ -s "$T/stayconnect/identity/ed25519.key" ] || say "WARNING: the archive carries no identity key — the appliance will register as a NEW one"
[ -e "$T/stayconnect/removed-from-central.json" ] && say "WARNING: the archived appliance had been REMOVED from Central; restored, it will admit no new Clients"
[ -d "$ETC" ] || die "$ETC does not exist — install the release on this machine first"

if [ "$YES" != 1 ] && [ -t 0 ]; then
  read -r -p "Replace this machine's identity and keys in $ETC with the archived appliance's? [y/N] " a
  [ "$a" = y ] || [ "$a" = Y ] || die "not confirmed"
fi

if [ -z "${SC_ETC_DIR:-}" ]; then
  say "stopping the appliance services"
  systemctl stop stayconnect-hotel-admin stayconnect-edged stayconnect-portald stayconnect-acctd stayconnect-pmsd \
                 stayconnect-netd stayconnect-scd >/dev/null 2>&1 || true
fi
SAVED="${ETC}.before-restore-$(date -u +%Y%m%dT%H%M%SZ)"
cp -a "$ETC" "$SAVED"; chmod 0700 "$SAVED"
say "saved the current configuration to $SAVED"

# Database credentials stay this machine's: reconcile them in the extracted copy before it is installed.
for f in "$T"/stayconnect/*.env; do
  [ -f "$f" ] || continue
  cur="$ETC/$(basename "$f")"
  [ -f "$cur" ] || continue
  while IFS= read -r line; do
    key="${line%%=*}"
    case "$key" in *_DB_URL|*_DSN)
      if grep -qE "^$key=" "$f"; then
        python3 - "$f" "$key" "$line" <<'PY'
import sys
path, key, newline = sys.argv[1:4]
out = [newline if l.split("=", 1)[0] == key else l for l in open(path).read().split("\n")]
open(path, "w").write("\n".join(out))
PY
      else
        printf '%s\n' "$line" >> "$f"
      fi;;
    esac
  done < <(grep -E '^[A-Z0-9_]+=' "$cur")
done
rm -f "$T/stayconnect/.dsn" "$T/stayconnect/secrets/pg-superuser.env"

( cd "$T" && tar -cf - stayconnect ) | ( cd "$(dirname "$ETC")" && tar -xpf - --no-same-owner )
# ownership the provisioner sets: env files root:stayconnect 0640, secrets root 0600
if getent group stayconnect >/dev/null 2>&1; then chown root:stayconnect "$ETC"/*.env 2>/dev/null || true; fi
chmod 0640 "$ETC"/*.env 2>/dev/null || true
chmod 0700 "$ETC/secrets" 2>/dev/null || true; chmod 0600 "$ETC"/secrets/* 2>/dev/null || true
say "restored $(cd "$T" && find stayconnect -type f | wc -l) file(s) into $ETC, database credentials kept from this machine"
say ""
say "NEXT:"
say "  systemctl start stayconnect-scd"
say "  /opt/stayconnect/bin/scd backup-restore --name <db-backup-name> --identity-file $IDENTITY"
say "  then start the other services (or reboot) and run the verification checklist."
say "  If this machine is different hardware, Central must Replace hardware / Rebind before the licence is valid."
