#!/usr/bin/env bash
# stayconnect-financial-restore.sh — the SUPPORTED site-database restore.
#
# docs/BACKUP_AND_RESTORE.md §1 described the restore as `pg_restore -d stayconnect_site <dump>` into the
# EXISTING database of the EXISTING cluster. THE DATABASE IS NOW RECREATED and the CLUSTER is not, which is
# the distinction the whole detector rests on: system_identifier comes from pg_control and belongs to the
# CLUSTER, so it is unchanged by any of this and cannot tell that the data is older than the appliance.
# That is why the management marker exists.
#
# The database is recreated because `pg_restore --clean --if-exists` does not work on this database and was
# measured not working on PRE-LIVE: --clean drops the TimescaleDB extension along with everything else,
# which takes _timescaledb_functions and _timescaledb_internal with it, and 72 dependent objects then fail
# to restore. See sitedb_recreate_database in lib-site-db.sh.
#
# This script is that procedure with the four things that make it trustworthy: a manifest verified against
# the appliance's OWN pinned trust anchor, a proven quiesce of every financial writer, a management marker
# that pg_restore cannot roll back, and Gate-P re-applied afterwards because a database recreate silently
# drops the one privilege pg_dump never carries.
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
# HOW THIS SCRIPT REACHES THE DATABASE IS NOT THIS SCRIPT'S DECISION ANY MORE.
#
# It used to be, and it was wrong in three ways at once, all three of which stayconnect-site-backup.sh had
# already been corrected for: it defaulted the ROLE to `stayconnect_site`, which is the DATABASE name and
# not a role that exists here; it called the HOST psql and pg_restore, which on this appliance do not exist
# at all -- PostgreSQL runs in a container and the host carries no client; and it would have accepted a
# svc_* role that cannot read every schema.
#
# FOUND BY RUNNING THIS SCRIPT. The --dry-run passes on a machine where the real run cannot work, because
# the dry run verifies the manifest and the digest and then stops -- before any database call. Meanwhile the
# marker is advanced and all five financial writers are stopped in the steps BETWEEN the dry-run exit and
# the first pg_restore. So the failure mode was: an appliance with its services down, its marker ahead, and
# its database untouched.
#
# deploy/scripts/lib-site-db.sh now owns the answer for both halves of the procedure, which is the point: a
# backup that can be taken and not restored is not a backup, and the two halves disagreeing about how to
# reach one database is exactly how that shipped.
# WHERE THE LIBRARY IS, WHICHEVER WAY THIS SCRIPT WAS INVOKED.
#
# NOT just "next to me". install-service-units.sh installs this script as /opt/stayconnect/bin/<name>
# WITHOUT its extension, and until now it installed no libraries at all -- which is why
# lib-hotel-admin-contract.sh is sitting in /opt/stayconnect/bin on the appliance with nothing in the
# repository putting it there. Someone copied it by hand, exactly like the service accounts that no
# install path created. The installer now carries lib-*.sh too, and this resolver means the script works
# from the repository, from /opt/stayconnect/deploy/scripts, and from $BIN, rather than only from wherever
# it happened to be tested.
_sc_lib() {
  local name="$1" here d
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  for d in "${SC_LIB_DIR:-}" "$here" /opt/stayconnect/bin /opt/stayconnect/deploy/scripts; do
    [ -n "$d" ] && [ -f "$d/$name" ] && { echo "$d/$name"; return 0; }
  done
  echo "FATAL: $name was not found (looked in \$SC_LIB_DIR, $here, /opt/stayconnect/bin," >&2
  echo "       /opt/stayconnect/deploy/scripts). This script cannot reach the database without it." >&2
  return 1
}
# shellcheck source=lib-site-db.sh
. "$(_sc_lib lib-site-db.sh)" || exit 1

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

# ---------------------------------------------------------------- 1b. CAN THIS MACHINE RESTORE AT ALL?
#
# BEFORE THE MARKER IS ADVANCED AND BEFORE ANYTHING IS STOPPED. The original ordering verified the
# manifest, advanced the marker, stopped five services and only then discovered there was no pg_restore --
# leaving the appliance down with its marker ahead and its database untouched. Establishing the database
# access is a read-only question and it belongs here, where the answer costs nothing.
sitedb_resolve restore || die "cannot reach the site database; nothing has been touched"
sitedb_require_client pg_restore restore || die "cannot restore on this machine; nothing has been touched"
sitedb_require_client psql restore || die "cannot record the restore on this machine; nothing has been touched"

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
# 0644, NOT 0600, AND THE DIFFERENCE IS THE WHOLE DETECTOR.
#
# This was 0600 and root-owned. edged -- the ONLY process that reads it -- runs as the unprivileged
# stayconnect account, so it got EACCES, payment.ReadRestoreMarker reported "absent", and the database
# concluded nothing had been restored. Measured on PRE-LIVE during the drill that found this: a real
# supported restore of a real appliance produced outcome=UNCHANGED, no hold and no restore event.
#
# THE MARKER IS NOT A SECRET. It holds a count, a timestamp and the digest of a manifest that is itself
# distributed to whoever performs a restore. Its security property is that a pg_restore cannot roll it
# BACK, which comes from its location outside the database and from write permission -- root-owned, so only
# root can advance it. Nothing about that requires hiding the count from the process whose job is to read
# it, and hiding it is what disarmed the detector.
#
# World-readable rather than a group, deliberately: a group grant would have to name the reader's account,
# and the next service that reads this file under a different account would fail the same silent way. There
# is nothing here to protect by narrowing.
chmod 0644 "$TMP"
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
# TIMESCALEDB MUST BE SUSPENDED ACROSS THE RESTORE, and this is not optional on this appliance.
#
# The site database carries TimescaleDB (accounting_records is a hypertable). Its own catalog lives in
# _timescaledb_catalog with CIRCULAR foreign keys between hypertable, chunk and continuous_agg -- pg_dump
# names exactly those three in its warnings on every backup this appliance takes:
#
#     pg_dump: warning: there are circular foreign-key constraints on this table:
#     pg_dump: detail: hypertable
#     pg_dump: hint: You might not be able to restore the dump without using --disable-triggers
#
# Restoring that catalog with the extension's event triggers and background workers live either fails or
# leaves hypertables whose chunks are not registered -- a database that restores "successfully" and has
# lost the shape of its time-series tables. timescaledb_pre_restore() suspends them for the duration;
# timescaledb_post_restore() puts them back and revalidates.
#
# The post_restore is run by a trap, so it happens even when pg_restore fails or the script is interrupted.
# Leaving the extension suspended would be worse than a failed restore: the database would come back up
# with its background workers off and nothing saying so.
# START FROM AN EMPTY DATABASE. See sitedb_recreate_database for the measurement behind this: the obvious
# `pg_restore --clean --if-exists` drops the TimescaleDB extension along with the rest, takes its schemas
# with it, and then fails to restore 72 dependent objects -- leaving 96 relational tables intact, no
# extension, and both hypertables empty.
note "recreating $SITEDB_DB from empty (the dump is the whole content)"
sitedb_recreate_database restore || die "the database was not restored"

# THE EXTENSION FIRST, because pre_restore is a function the extension provides: it cannot suspend an
# extension that is not there, and the dump's own CREATE EXTENSION comes too late for the catalog data that
# depends on it.
sitedb_psql -qAt -c 'CREATE EXTENSION IF NOT EXISTS timescaledb' >/dev/null 2>&1 || true

TS_SUSPENDED=0
ts_post_restore() {
  [ "$TS_SUSPENDED" = 1 ] || return 0
  TS_SUSPENDED=0
  if sitedb_psql -qAt -c 'SELECT timescaledb_post_restore()' >/dev/null 2>&1; then
    note "timescaledb_post_restore() done"
  else
    note "WARNING: timescaledb_post_restore() FAILED. The extension may still be suspended: its"
    note "background workers are off until it is run by hand. Money movement is held regardless."
  fi
}
if sitedb_has_timescaledb; then
  note "timescaledb present: suspending it for the restore"
  sitedb_psql -qAt -c 'SELECT timescaledb_pre_restore()' >/dev/null     || die "timescaledb_pre_restore() failed; the database has NOT been restored"
  TS_SUSPENDED=1
  trap ts_post_restore EXIT
fi

if ! sitedb_pg_restore "$DUMP"; then
  ts_post_restore
  note "pg_restore FAILED. The marker is already advanced, so the next startup will hold money movement"
  note "and route everything to reconciliation. That is deliberate: a half-restored financial database is"
  note "exactly the situation recovery mode exists for."
  exit 1
fi
note "database restored"

# ---------------------------------------------------------------- 5. stamp and hold
ts_post_restore

# ---------------------------------------------------------------- 4b. PRIVILEGES THE DUMP DOES NOT CARRY
#
# pg_dump captures privileges ON OBJECTS. It does not capture privileges on the DATABASE, because those are
# cluster-level and belong to pg_dumpall. gatep-iam-roles.sql grants exactly one:
#
#     GRANT CREATE ON DATABASE <db> TO iam_v2_owner
#
# Recreating the database therefore silently revokes it, and the symptom arrives much later -- the next
# migration cannot create anything, on an appliance whose restore reported success. Gate-P is the
# authoritative source for privileges, so it is re-applied here rather than hoped for.
GATEP=""
for d in "${STAYCONNECT_GATEP_DIR:-}" /opt/stayconnect/deploy/gatep "$(dirname "${BASH_SOURCE[0]}")/../gatep"; do
  [ -n "$d" ] && [ -f "$d/gatep-grants.sql" ] && { GATEP="$d"; break; }
done
if [ -n "$GATEP" ]; then
  note "re-applying Gate-P from $GATEP (database-level grants are not in the dump)"
  if [ "$SITEDB_USE_CONTAINER" = 1 ]; then
    docker cp "$GATEP" "$SITEDB_CONTAINER:/tmp/gatep-restore" >/dev/null
    for f in gatep-roles.sql gatep-iam-roles.sql gatep-iam-ownership.sql gatep-grants.sql; do
      [ -f "$GATEP/$f" ] || continue
      sitedb_psql -f "/tmp/gatep-restore/$f" >/dev/null || die "Gate-P file $f failed after the restore.
The data is restored and money movement is HELD. Do not enable transmission until privileges are correct."
      note "  applied $f"
    done
  else
    for f in gatep-roles.sql gatep-iam-roles.sql gatep-iam-ownership.sql gatep-grants.sql; do
      [ -f "$GATEP/$f" ] || continue
      sitedb_psql -f "$GATEP/$f" >/dev/null || die "Gate-P file $f failed after the restore."
      note "  applied $f"
    done
  fi
elif [ "${STAYCONNECT_SKIP_GATEP:-0}" = "1" ]; then
  note "STAYCONNECT_SKIP_GATEP=1: not re-applying Gate-P (privileges are the caller's responsibility)"
else
  die "the database is restored but the Gate-P files could not be found, so GRANT CREATE ON DATABASE
was not restored and the next migration will fail. Set STAYCONNECT_GATEP_DIR, or STAYCONNECT_SKIP_GATEP=1
if privileges are being managed another way. Money movement is HELD either way."
fi

# HYPERTABLES MUST BE REGISTERED, NOT MERELY SHAPED -- the same assertion provision-fresh-appliance.sh
# makes, for the same reason: a database whose hypertables are not registered refuses every write to
# audit_log and accounting_records while looking perfect in the catalog.
if sitedb_has_timescaledb; then
  HYPER="$(sitedb_psql -qAt -c 'SELECT count(*) FROM timescaledb_information.hypertables' 2>/dev/null)"
  [ "${HYPER:-0}" -ge 2 ] || die "only ${HYPER:-0} hypertable(s) are registered after the restore;
audit_log and accounting_records would reject every write. Money movement is HELD."
  note "$HYPER hypertables registered"
fi

sitedb_psql -tAq -c "SELECT iam_v2.p4_record_supported_restore('$TENANT'::uuid,'$SITE'::uuid,$NEXT_GEN,
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
