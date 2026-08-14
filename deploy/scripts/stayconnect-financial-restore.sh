#!/usr/bin/env bash
# stayconnect-financial-restore.sh — the SUPPORTED site-database restore.
#
# docs/BACKUP_AND_RESTORE.md §1 describes the restore as `pg_restore -d stayconnect_site <dump>` into the
# EXISTING database of the EXISTING cluster. Nothing about the cluster changes, so nothing inside the
# restored data can tell that it is older than the appliance. This script is that procedure with the three
# things that make it trustworthy: a manifest verified against the appliance's OWN pinned trust anchor, a
# proven quiesce of every financial writer, and a management marker that pg_restore cannot roll back.
#
# THE TRUST ANCHOR. The manifest is verified with the registry root public key already baked into this
# appliance at manufacture (/etc/stayconnect/assignment-registry-root.pub, the same anchor the assignment
# registry uses). There is deliberately NO --pubkey option: a verification key a caller supplies verifies
# nothing except that the caller has a key. No new root of trust is introduced and no certificate trust
# anchor is redesigned -- this reuses the one the product already pins.
#
# THE QUIESCE. Every service that writes financial state is stopped and then PROVEN stopped. A failure to
# stop is fatal: restoring underneath a live writer produces a database that is neither the backup nor the
# present.
#
# THE MARKER. Advanced BEFORE the restore, so a crash mid-restore still leaves it ahead and the next startup
# still holds money movement. It is excluded from the /etc backup (see stayconnect-site-backup.sh), because
# an /etc archive containing it would roll it back and silence the very detector it exists to be.
#
# WHAT PROTECTS THE MARKER: ownership and file permissions, and nothing else. There is no TPM, no secure
# element and no monotonic counter in this appliance profile, and this script claims none. Root can rewrite
# it, and so can a restore performed without this script -- which is why the UNSUPPORTED_RAW_SNAPSHOT path
# exists in migration 0023.
#
# DARK: delivered, not wired into any timer or service. It performs no financial traffic.
set -euo pipefail

MARKER_DIR="${STAYCONNECT_MARKER_DIR:-/etc/stayconnect}"
MARKER="$MARKER_DIR/financial-restore-generation.json"
TRUST_ANCHOR="${SCD_ASSIGNMENT_REGISTRY_ROOT:-/etc/stayconnect/assignment-registry-root.pub}"
PGUSER_SITE="${STAYCONNECT_PGUSER:-stayconnect_site}"
PGDB_SITE="${STAYCONNECT_PGDB:-stayconnect_site}"
PSQL=(psql -v ON_ERROR_STOP=1 -U "$PGUSER_SITE" -d "$PGDB_SITE" -tAq)

# Every service that can write financial state. pmsd is included because it is the PMS/financial runtime
# that exists after Phase 4: leaving it out would mean the quiesce was complete for today's services and
# quietly incomplete for the ones this milestone exists to prepare for.
SERVICES=(stayconnect-edged stayconnect-pmsd stayconnect-acctd stayconnect-portald stayconnect-scd)

die() { echo "restore: $*" >&2; exit 1; }
note() { echo "restore: $*"; }

usage() {
  cat >&2 <<'USAGE'
usage: stayconnect-financial-restore.sh --dump <file> --manifest <file>
                                        --tenant <uuid> --site <uuid> [--dry-run]

  --dump       the pg_dump -Fc artefact to restore
  --manifest   the restore manifest, signed with the registry root key
  --dry-run    verify and report; touch neither the marker nor the database

There is no --pubkey. The manifest is verified against this appliance's pinned registry root anchor
(SCD_ASSIGNMENT_REGISTRY_ROOT), because a caller-supplied verification key proves nothing.
USAGE
  exit 2
}

DUMP=""; MANIFEST=""; TENANT=""; SITE=""; DRYRUN=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dump) DUMP="$2"; shift 2;;
    --manifest) MANIFEST="$2"; shift 2;;
    --tenant) TENANT="$2"; shift 2;;
    --site) SITE="$2"; shift 2;;
    --dry-run) DRYRUN=1; shift;;
    --pubkey)
      die "--pubkey is not accepted. The manifest is verified against this appliance's pinned registry root
anchor; a key supplied on the command line would let whoever runs the restore choose who signed it.";;
    *) usage;;
  esac
done
[ -n "$DUMP" ] && [ -n "$MANIFEST" ] && [ -n "$TENANT" ] && [ -n "$SITE" ] || usage
[ -f "$DUMP" ]     || die "no such dump: $DUMP"
[ -f "$MANIFEST" ] || die "no such manifest: $MANIFEST"
[ -f "$TRUST_ANCHOR" ] || die "this appliance has no pinned registry root anchor at $TRUST_ANCHOR;
a restore cannot be verified and will not proceed"

# ---------------------------------------------------------------- 1. verify against the PINNED anchor
# The anchor is a raw 32-byte Ed25519 public key, the format the assignment registry already uses. OpenSSL
# needs it wrapped in the fixed Ed25519 SubjectPublicKeyInfo prefix; that prefix is a constant, so this is a
# format conversion and not a second trust decision.
[ "$(stat -c%s "$TRUST_ANCHOR" 2>/dev/null || stat -f%z "$TRUST_ANCHOR")" = "32" ] \
  || die "the pinned anchor at $TRUST_ANCHOR is not a raw 32-byte Ed25519 key"
SIG="$MANIFEST.sig"
[ -f "$SIG" ] || die "the manifest carries no signature ($SIG)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
printf '\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00' > "$WORK/anchor.der"
cat "$TRUST_ANCHOR" >> "$WORK/anchor.der"
openssl pkey -pubin -inform DER -in "$WORK/anchor.der" -out "$WORK/anchor.pem" 2>/dev/null \
  || die "the pinned anchor could not be read as an Ed25519 public key"

if ! openssl pkeyutl -verify -pubin -inkey "$WORK/anchor.pem" \
       -rawin -in "$MANIFEST" -sigfile "$SIG" >/dev/null 2>&1; then
  die "the restore manifest did not verify against this appliance's pinned registry root.
Nothing has been touched. A manifest signed by any other key is not a supported restore."
fi
note "manifest verified against the pinned registry root anchor"

MANIFEST_SHA="$(sha256sum "$MANIFEST" | awk '{print $1}')"
WANT_SHA="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["dump_sha256"])' "$MANIFEST")"
TAKEN_AT="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1])).get("backup_taken_at",""))' "$MANIFEST")"
GOT_SHA="$(sha256sum "$DUMP" | awk '{print $1}')"
[ "$WANT_SHA" = "$GOT_SHA" ] || die "the dump does not match the manifest (want $WANT_SHA, got $GOT_SHA)"
note "dump digest matches the verified manifest"

# ---------------------------------------------------------------- 2. advance the marker
CUR_GEN=0
if [ -f "$MARKER" ]; then
  CUR_GEN="$(python3 -c 'import json,sys;print(int(json.load(open(sys.argv[1]))["restore_generation"]))' "$MARKER")"
fi
NEXT_GEN=$((CUR_GEN + 1))
note "restore generation $CUR_GEN -> $NEXT_GEN"

if [ "$DRYRUN" = 1 ]; then
  note "dry run: verified only. The marker, the services and the database are untouched."
  exit 0
fi

# Create it securely if it is absent. If it already exists it was provisioned by the installer, and
# re-chmod'ing somebody else's directory is not this tool's job -- nor is it possible on every filesystem a
# drill might run on.
[ -d "$MARKER_DIR" ] || install -d -m 0700 "$MARKER_DIR" || die "cannot create $MARKER_DIR"
TMP="$MARKER.tmp.$$"
cat > "$TMP" <<JSON
{
  "restore_generation": $NEXT_GEN,
  "advanced_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "manifest_sha256": "$MANIFEST_SHA",
  "note": "Written by stayconnect-financial-restore.sh. It lives on the management partition and is EXCLUDED from the /etc backup archive, so neither pg_restore nor an /etc restore can roll it back. Protected by file permissions only; there is no TPM or monotonic counter in this profile."
}
JSON
chmod 0600 "$TMP"
mv -f "$TMP" "$MARKER"
sync
note "management marker advanced"

# ---------------------------------------------------------------- 3. PROVEN quiesce
# Stopping is not the same as being stopped. Each service is stopped and then checked, and a service that
# is still active -- or whose state cannot be determined at all -- aborts the restore. Nothing here is
# swallowed: restoring underneath a live financial writer produces a database that is neither the backup
# nor the present, and that is worse than not restoring.
STOP_FAILED=""
for s in "${SERVICES[@]}"; do
  if ! systemctl list-unit-files "$s.service" >/dev/null 2>&1; then
    note "  $s: not installed on this appliance — nothing to stop"
    continue
  fi
  systemctl stop "$s" || STOP_FAILED="$STOP_FAILED $s(stop-failed)"
  state="$(systemctl is-active "$s" 2>/dev/null || true)"
  case "$state" in
    inactive|failed|"") note "  $s: stopped ($state)" ;;
    *)                  STOP_FAILED="$STOP_FAILED $s($state)" ;;
  esac
done
if [ -n "$STOP_FAILED" ]; then
  die "these financial writers could not be proven stopped:$STOP_FAILED
The database has NOT been restored. The marker is already advanced, so the next startup will hold money
movement and route everything to reconciliation -- which is the safe direction. Stop the services by hand,
establish why they would not stop, and run this again."
fi
note "every financial writer is proven stopped"

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
START_FAILED=""
for s in "${SERVICES[@]}"; do
  systemctl list-unit-files "$s.service" >/dev/null 2>&1 || continue
  systemctl start "$s" || START_FAILED="$START_FAILED $s"
done
if [ -n "$START_FAILED" ]; then
  die "the restore completed and is recorded, but these services did not start:$START_FAILED
Money movement is HELD, so nothing unsafe can happen while you investigate -- but guest access may be
degraded until they are running."
fi

cat <<'DONE'

restore: complete.

Guest internet access is running. MONEY MOVEMENT IS HELD: no posting will be transmitted and no payment
will be executed until an operator has reconciled every item that was in flight when the backup was taken.
Open Hotel Admin -> Financial recovery to do that. Nothing will be replayed automatically, and recovery
cannot be released while any held item is unreconciled or any underlying record is still sendable.
DONE
