#!/usr/bin/env bash
# stayconnect-financial-restore.sh — the SUPPORTED site-database restore.
#
# The repository's existing procedure (docs/BACKUP_AND_RESTORE.md §1) is:
#
#     pg_restore -U stayconnect_site -d stayconnect_site /var/backups/.../site-<stamp>.dump
#
# That restores into the EXISTING database of the EXISTING cluster, so pg_control's system_identifier does
# not change and nothing inside the restored data can tell that it is older than the appliance. This script
# is that procedure with the one missing piece: a marker on the management partition that pg_dump cannot
# read and pg_restore cannot write, advanced BEFORE the restore, so the database comes back knowing it has
# been rolled back.
#
# WHAT IT DOES, in order, and why the order matters:
#
#   1. verify the manifest signature and the dump's digest      nothing is restored on an unverified artefact
#   2. read and ADVANCE the management marker                   before the restore, so a crash mid-restore
#                                                               still leaves the marker ahead and the next
#                                                               startup still detects the rollback
#   3. stop the services that write financial state             a restore under a live writer is not a restore
#   4. pg_restore                                               the existing supported command, unchanged
#   5. stamp the database and enter FINANCIAL_RECOVERY_MODE     the work in flight at backup time is still
#                                                               unaccounted for, so money movement is held
#   6. start the services                                       guest access resumes; money does not
#
# WHAT PROTECTS THE MARKER. Ownership and file permissions, and nothing else. There is no TPM, no secure
# element and no monotonic counter in this appliance profile, and this script claims none: root can rewrite
# the marker, and so can a restore performed without this script. It defends against the ordinary
# operational restore and against accident. It is not an anti-tamper control, and the UNSUPPORTED_RAW_SNAPSHOT
# path exists precisely because someone will eventually restore without it.
#
# DARK: this script is delivered but not wired into any timer or service. It performs no financial traffic.
set -euo pipefail

MARKER_DIR="${STAYCONNECT_MARKER_DIR:-/etc/stayconnect}"
MARKER="$MARKER_DIR/financial-restore-generation.json"
PGUSER_SITE="${STAYCONNECT_PGUSER:-stayconnect_site}"
PGDB_SITE="${STAYCONNECT_PGDB:-stayconnect_site}"
PSQL=(psql -v ON_ERROR_STOP=1 -U "$PGUSER_SITE" -d "$PGDB_SITE" -tAq)
SERVICES=(stayconnect-edged stayconnect-acctd stayconnect-portald stayconnect-scd)

die() { echo "restore: $*" >&2; exit 1; }
note() { echo "restore: $*"; }

usage() {
  cat >&2 <<'USAGE'
usage: stayconnect-financial-restore.sh --dump <file> --manifest <file> --pubkey <file>
                                        --tenant <uuid> --site <uuid> [--dry-run]

  --dump       the pg_dump -Fc artefact to restore
  --manifest   the signed restore manifest describing it
  --pubkey     the public key the manifest is verified against
  --dry-run    verify and report; touch neither the marker nor the database
USAGE
  exit 2
}

DUMP=""; MANIFEST=""; PUBKEY=""; TENANT=""; SITE=""; DRYRUN=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dump) DUMP="$2"; shift 2;;
    --manifest) MANIFEST="$2"; shift 2;;
    --pubkey) PUBKEY="$2"; shift 2;;
    --tenant) TENANT="$2"; shift 2;;
    --site) SITE="$2"; shift 2;;
    --dry-run) DRYRUN=1; shift;;
    *) usage;;
  esac
done
[ -n "$DUMP" ] && [ -n "$MANIFEST" ] && [ -n "$PUBKEY" ] && [ -n "$TENANT" ] && [ -n "$SITE" ] || usage
[ -f "$DUMP" ]     || die "no such dump: $DUMP"
[ -f "$MANIFEST" ] || die "no such manifest: $MANIFEST"
[ -f "$PUBKEY" ]   || die "no such public key: $PUBKEY"

# ---------------------------------------------------------------- 1. verify
# The manifest is verified BEFORE anything is read out of it. Parsing first and verifying afterwards means
# every field has already been through the parser as attacker-controlled input.
SIG="$MANIFEST.sig"
[ -f "$SIG" ] || die "the manifest carries no signature ($SIG)"
if ! openssl dgst -sha256 -verify "$PUBKEY" -signature "$SIG" "$MANIFEST" >/dev/null 2>&1; then
  die "the restore manifest did not verify against $PUBKEY. Nothing has been touched."
fi
note "manifest signature verified"

MANIFEST_SHA="$(sha256sum "$MANIFEST" | awk '{print $1}')"
WANT_SHA="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["dump_sha256"])' "$MANIFEST")"
TAKEN_AT="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("backup_taken_at",""))' "$MANIFEST")"
GOT_SHA="$(sha256sum "$DUMP" | awk '{print $1}')"
[ "$WANT_SHA" = "$GOT_SHA" ] || die "the dump does not match the manifest (want $WANT_SHA, got $GOT_SHA)"
note "dump digest matches the manifest"

# ---------------------------------------------------------------- 2. advance the marker
CUR_GEN=0
if [ -f "$MARKER" ]; then
  CUR_GEN="$(python3 -c 'import json,sys;print(int(json.load(open(sys.argv[1]))["restore_generation"]))' "$MARKER")"
fi
NEXT_GEN=$((CUR_GEN + 1))
note "restore generation $CUR_GEN -> $NEXT_GEN"

if [ "$DRYRUN" = 1 ]; then
  note "dry run: verified only. The marker and the database are untouched."
  exit 0
fi

install -d -m 0700 "$MARKER_DIR"
TMP="$MARKER.tmp.$$"
cat > "$TMP" <<JSON
{
  "restore_generation": $NEXT_GEN,
  "advanced_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "manifest_sha256": "$MANIFEST_SHA",
  "note": "Written by stayconnect-financial-restore.sh. This file lives on the management partition so a pg_restore cannot roll it back. It is protected by file permissions only; there is no TPM or monotonic counter in this profile."
}
JSON
chmod 0600 "$TMP"
# Advance BEFORE the restore, and fsync the directory: a crash between here and the restore leaves the
# marker ahead of the database, which reads as "restored" and holds money movement. Failing safe means
# failing towards the hold.
mv -f "$TMP" "$MARKER"
sync
note "management marker advanced"

# ---------------------------------------------------------------- 3. stop the writers
for s in "${SERVICES[@]}"; do systemctl stop "$s" 2>/dev/null || true; done
note "financial writers stopped"

# ---------------------------------------------------------------- 4. the restore itself
if ! pg_restore -U "$PGUSER_SITE" -d "$PGDB_SITE" --clean --if-exists "$DUMP"; then
  note "pg_restore FAILED. The marker is already advanced, so the next startup will hold money movement"
  note "and route everything to reconciliation. That is deliberate: a half-restored financial database is"
  note "exactly the situation recovery mode exists for."
  exit 1
fi
note "database restored"

# ---------------------------------------------------------------- 5. stamp and hold
"${PSQL[@]}" -c "SELECT iam_v2.p4_record_supported_restore('$TENANT'::uuid,'$SITE'::uuid,$NEXT_GEN,
  '$MANIFEST_SHA', $( [ -n "$TAKEN_AT" ] && echo "'$TAKEN_AT'::timestamptz" || echo NULL ),
  '$(id -un)');" >/dev/null \
  || die "the restore completed but could not be recorded. Do NOT start the services: run the reconcile
step manually before anything writes financial state."
note "restore recorded; the site is in FINANCIAL_RECOVERY_MODE"

# ---------------------------------------------------------------- 6. resume
for s in "${SERVICES[@]}"; do systemctl start "$s" 2>/dev/null || true; done
cat <<'DONE'

restore: complete.

Guest internet access is running. MONEY MOVEMENT IS HELD: no posting will be transmitted and no payment
will be executed until an operator has reconciled every item that was in flight when the backup was taken.
Open Hotel Admin -> Financial recovery to do that. Nothing will be replayed automatically, and recovery
cannot be released while any held item is unreconciled or any underlying record is still sendable.
DONE
