#!/usr/bin/env bash
# APPLY THE MIGRATIONS A RELEASE SHIPS THAT THIS APPLIANCE'S DATABASE DOES NOT HAVE YET — one at a time, through
# the authoritative runner (scripts/edge-migrate.sh), never around it.
#
# Used by appliance-install.sh --upgrade. It decides only WHICH migrations are pending; every safety property of
# applying one stays with edge-migrate.sh: positive target identity, the sha256 of the exact file, the mandatory
# verified pre-change backup (exit 4 and no change without one), the advisory lock, one ledger row per migration,
# a least-privilege executor (iam_v2_owner via --apply-role, never the superuser).
#
# WHERE THE DATABASE STANDS is read from the database itself where it can be:
#   * the highest numbered version in public.schema_migrations, or
#   * for a database built from the factory-clean baseline and never migrated since — its ledger is empty by
#     design — the number the provisioner recorded when it applied that baseline (/etc/stayconnect/schema-baseline).
#   Neither available = refuse: guessing which migrations a database already contains is how a migration is applied
#   twice or skipped.
#
# After the last migration the Gate-P grants are reconciled (scripts/gatep-reconcile.sh, hash-verified, one
# transaction), because a new migration can add an object a runtime role needs.
#
# Usage: apply-pending-migrations.sh [--root /opt/stayconnect] [--container stayconnect-pg] [--user stayconnect]
#                                    [--list]          (print the pending set and exit; changes nothing)
# Exit: 0 done (or nothing pending) · 1 refused · 2 a migration or the reconcile failed · 4 no pre-change backup
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
CONTAINER=stayconnect-pg; PGUSER_ROLE=stayconnect; DB=stayconnect_site; LIST=0
MARKER="${SC_SCHEMA_BASELINE_FILE:-/etc/stayconnect/schema-baseline}"
while [ $# -gt 0 ]; do
  case "$1" in
    --root) ROOT="${2:?}"; shift 2;;
    --container) CONTAINER="${2:?}"; shift 2;;
    --user) PGUSER_ROLE="${2:?}"; shift 2;;
    --list) LIST=1; shift;;
    *) echo "apply-pending-migrations: unknown argument $1" >&2; exit 1;;
  esac
done
say() { echo "[migrate] $*"; }
refuse() { echo "[migrate] REFUSED (nothing changed): $*" >&2; exit 1; }
# shellcheck source=lib-baseline.sh
. "$ROOT/deploy/scripts/lib-baseline.sh"
MIG="$ROOT/data-plane/migrations"
RUNNER="$ROOT/scripts/edge-migrate.sh"
[ -d "$MIG" ] || refuse "no migrations at $MIG"
[ -f "$RUNNER" ] || refuse "no migration runner at $RUNNER"

q() { docker exec -i "$CONTAINER" psql -X -tAq -U "$PGUSER_ROLE" -d "$DB" -c "$1" </dev/null; }
top="$(q "SELECT coalesce(max(substring(version from 1 for 4)),'') FROM public.schema_migrations WHERE version ~ '^[0-9]{4}_'")" \
  || refuse "cannot read the migration ledger of $DB"
src="the migration ledger"
if [ -z "$top" ]; then
  if [ -s "$MARKER" ]; then
    top="$(tr -dc '0-9' < "$MARKER" | head -c 4)"; src="$MARKER (factory-clean baseline, empty ledger)"
  fi
fi
[ -n "$top" ] || refuse "the ledger is empty and $MARKER does not say which baseline built this database"
mapfile -t PENDING < <(migrations_after "$MIG" "$top")
say "database is at $top (from $src); this release ships up to $(latest_migration "$MIG"); pending: ${#PENDING[@]}"
for v in "${PENDING[@]}"; do say "  $v"; done
[ "$LIST" = 1 ] && exit 0

if [ "${#PENDING[@]}" -gt 0 ]; then
  export EDGE_PSQL="docker exec -i $CONTAINER psql -v ON_ERROR_STOP=1 -X -U $PGUSER_ROLE -d $DB"
  for v in "${PENDING[@]}"; do
    sha="$(sha256sum "$MIG/$v.up.sql" | cut -d' ' -f1)"
    say "applying $v"
    # A migration that must change the platform schema (public) declares it; it then runs as the platform role under the
    # runner's --platform-migration conditions (see scripts/edge-migrate.sh). Everything else keeps the least-privilege role.
    if head -n 20 "$MIG/$v.up.sql" | grep -qE '^--[[:space:]]*edge-migrate:[[:space:]]*platform[[:space:]]*$'; then
      # The marker is a request to run as the platform role, so the file must be the one this release was built and
      # verified with: its hash has to equal the entry in the installed SHA256SUMS (written by the release the installer
      # just verified). A tree edited after installation, or a marker added to a file the release did not ship, is refused.
      relsum="$(awk -v f="./data-plane/migrations/$v.up.sql" '{n=$2; sub(/^\*/,"",n)} n==f {print $1}' "$ROOT/SHA256SUMS" 2>/dev/null | head -1)"
      [ -n "$relsum" ] && [ "$relsum" = "$sha" ]         || refuse "platform migration $v does not match the installed release record ($ROOT/SHA256SUMS); refusing to run it as the platform role"
      say "  $v is a platform migration: applied as $PGUSER_ROLE"
      bash "$RUNNER" --only "$v" --expect-db "$DB" --target-kind live-site \
           --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION --apply-role "$PGUSER_ROLE" --platform-migration --expect-sha256 "$sha"
    else
      bash "$RUNNER" --only "$v" --expect-db "$DB" --target-kind live-site \
           --ack-target I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION --apply-role iam_v2_owner --expect-sha256 "$sha"
    fi
    rc=$?
    case "$rc" in
      0) ;;
      4) echo "[migrate] REFUSED: no verified pre-change backup could be taken before $v, so nothing was changed." >&2
         echo "          Add a backup key holder (Admin Console -> Backups -> Backup encryption) and re-run." >&2
         exit 4;;
      *) echo "[migrate] FAILED applying $v (runner exit $rc). Migrations before it are applied and recorded." >&2; exit 2;;
    esac
  done
fi

# The reconcile is worth running even with nothing pending: an upgrade can change grants without a migration.
args=(--container "$CONTAINER" --db "$DB" --user "$PGUSER_ROLE")
if [ -f "$ROOT/RELEASE.json" ]; then
  commit="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["source_commit"])' "$ROOT/RELEASE.json" 2>/dev/null || true)"
  [ -n "$commit" ] && args+=(--expect-commit "$commit")
fi
bash "$ROOT/scripts/gatep-reconcile.sh" "${args[@]}" || { echo "[migrate] FAILED: the Gate-P reconcile rolled back" >&2; exit 2; }
say "done"
