# lib-site-db.sh — HOW TO REACH THIS APPLIANCE'S SITE DATABASE, ONCE.
#
# Sourced, not executed. Provides sitedb_resolve, sitedb_psql, sitedb_pg_dump and sitedb_pg_restore.
#
# WHY THIS FILE EXISTS, AND IT IS NOT TIDINESS.
#
# stayconnect-site-backup.sh learned three things the hard way on a containerised appliance, each of which
# had already broken a real backup:
#
#	the CLIENT must be the server's own. PostgreSQL runs in the stayconnect-pg container at 16.3 and the
#	host carried a 14.x client, which aborts with "server version mismatch". On this appliance the host
#	carries NO psql or pg_restore at all -- measured, `command -v` finds neither.
#	the ROLE is not the database name. `stayconnect_site` is the database; there is no such role, and
#	using it produces an authentication error that reads like a wrong password.
#	a svc_* ROLE CANNOT do this. The service roles are deliberately scoped so they cannot read every
#	schema, so refusing them by name beats letting pg_dump discover it after locking every table.
#
# stayconnect-financial-restore.sh -- the OTHER HALF OF THE SAME PROCEDURE -- learned none of them. It
# called host `pg_restore -U stayconnect_site` and host `psql -U stayconnect_site`. On this appliance that
# fails at the first command, AFTER the marker has already been advanced and all five financial writers
# stopped: the safe direction, and still a restore path that cannot restore.
#
# FOUND BY RUNNING IT. The dry run passed -- it only verifies the manifest and the digest -- and the
# preconditions were then checked by hand before the real run: no host client, no such role. A backup that
# can be taken and not restored is not a backup, and the two halves disagreeing about how to reach one
# database is how that shipped.
#
# So the knowledge lives in one file that both source. A future appliance whose container is named or
# initialised differently changes this file, not two.

# sitedb_resolve sets: SITEDB_ROLE, SITEDB_USE_CONTAINER, SITEDB_CONTAINER, SITEDB_DB, SITEDB_SRV_MAJOR.
# $1 = a short label for messages ("backup", "restore").
sitedb_resolve() {
  local tag="${1:-sitedb}"
  SITEDB_DB="${STAYCONNECT_PGDB:-stayconnect_site}"
  SITEDB_CONTAINER="${STAYCONNECT_PG_CONTAINER:-stayconnect-pg}"

  SITEDB_USE_CONTAINER=0
  if command -v docker >/dev/null 2>&1 && docker inspect "$SITEDB_CONTAINER" >/dev/null 2>&1; then
    SITEDB_USE_CONTAINER=1
  fi

  SITEDB_ROLE="${STAYCONNECT_PGUSER:-}"

  # ADMINISTRATIVE ENTRIES ONLY. ctrlapi.env is the administrative DSN where an appliance has one; the
  # service envs are deliberately NOT consulted, because the requirement is a role that can reach
  # everything and no svc_* role satisfies it.
  if [ -z "$SITEDB_ROLE" ]; then
    local envf dsn
    for envf in /etc/stayconnect/ctrlapi.env /etc/stayconnect/backup.env; do
      [ -f "$envf" ] || continue
      dsn="$(grep -oE "postgres://[^ ]*/$SITEDB_DB(\?[^ ]*)?" "$envf" | head -1)" || true
      [ -n "${dsn:-}" ] || continue
      SITEDB_ROLE="$(printf '%s' "$dsn" | sed -E 's#postgres://([^:]+):.*#\1#')"
      [ -n "${PGPASSWORD:-}" ] || PGPASSWORD="$(printf '%s' "$dsn" | sed -E 's#postgres://[^:]+:([^@]*)@.*#\1#')"
      export PGPASSWORD
      break
    done
  fi

  # ASK THE CONTAINER WHO ITS OWNER IS rather than guessing a name. POSTGRES_USER is the role the image
  # initialised the cluster as, so it is the superuser, and inside the container it needs no password.
  # Discovery, not an assumption: a container initialised as something else resolves to that instead.
  if [ -z "$SITEDB_ROLE" ] && [ "$SITEDB_USE_CONTAINER" = 1 ]; then
    SITEDB_ROLE="$(docker exec "$SITEDB_CONTAINER" printenv POSTGRES_USER </dev/null 2>/dev/null | tr -d '\r')"
    [ -n "$SITEDB_ROLE" ] && echo "$tag: using the container's own superuser ($SITEDB_ROLE)"
  fi

  # Off the appliance -- CI, a workstation, a drill fixture -- the standard conventions are the honest
  # fallback. What is NOT a fallback is the database name.
  [ -n "$SITEDB_ROLE" ] || SITEDB_ROLE="${PGUSER:-}"
  [ -n "$SITEDB_ROLE" ] || SITEDB_ROLE="$(id -un 2>/dev/null || true)"
  if [ -z "$SITEDB_ROLE" ]; then
    echo "$tag: FAILED — no database role to work as (set STAYCONNECT_PGUSER)" >&2
    return 1
  fi

  case "$SITEDB_ROLE" in
    svc_*)
      echo "$tag: FAILED — refusing to use the least-privilege service role '$SITEDB_ROLE'." >&2
      echo "$tag: a whole-database operation must reach every schema, and the service roles are" >&2
      echo "$tag: deliberately scoped so that they cannot. Set STAYCONNECT_PGUSER to an administrative" >&2
      echo "$tag: role, or provide /etc/stayconnect/backup.env with a DSN for one." >&2
      return 1 ;;
  esac

  SITEDB_SRV_MAJOR="$(sitedb_psql -qAt -c 'SHOW server_version' 2>/dev/null | cut -d. -f1)"
  if [ -z "$SITEDB_SRV_MAJOR" ]; then
    echo "$tag: FAILED — could not reach $SITEDB_DB as '$SITEDB_ROLE' to establish the server version" >&2
    return 1
  fi
  return 0
}

# sitedb_client_major <program> — the major version of the client that will actually be used.
sitedb_client_major() {
  if [ "$SITEDB_USE_CONTAINER" = 1 ]; then
    docker exec "$SITEDB_CONTAINER" "$1" --version </dev/null 2>/dev/null | grep -oE '[0-9]+' | head -1
  else
    "$1" --version 2>/dev/null | grep -oE '[0-9]+' | head -1
  fi
}

# sitedb_require_client <program> <tag> — refuse a client older than the server, and refuse a missing one.
sitedb_require_client() {
  local prog="$1" tag="${2:-sitedb}" cli
  cli="$(sitedb_client_major "$prog")"
  if [ -z "$cli" ]; then
    echo "$tag: FAILED — no $prog is available to this script." >&2
    echo "$tag: the appliance runs PostgreSQL in the '$SITEDB_CONTAINER' container and carries no host" >&2
    echo "$tag: client; set STAYCONNECT_PG_CONTAINER, or run where the server's own client exists." >&2
    return 1
  fi
  if [ "$cli" -lt "$SITEDB_SRV_MAJOR" ]; then
    echo "$tag: FAILED — $prog $cli cannot work against a PostgreSQL $SITEDB_SRV_MAJOR server." >&2
    return 1
  fi
  echo "$tag: $prog $cli -> server $SITEDB_SRV_MAJOR (container=$SITEDB_USE_CONTAINER)"
  return 0
}

# STDIN IS CLOSED ON EVERY docker exec, deliberately.
#
# `docker exec -i` consumes the CALLER'S stdin. A helper invoked inside a `while read` loop, or in a script
# whose stdin is a heredoc, will silently eat the rest of it -- a failure that looks like truncated input
# rather than like a database call. Only sitedb_pg_restore passes stdin through, because that is its job.
sitedb_psql() {
  if [ "$SITEDB_USE_CONTAINER" = 1 ]; then
    docker exec -e PGPASSWORD="${PGPASSWORD:-}" "$SITEDB_CONTAINER" \
      psql -v ON_ERROR_STOP=1 -U "$SITEDB_ROLE" -d "$SITEDB_DB" "$@" </dev/null
  else
    psql -v ON_ERROR_STOP=1 -U "$SITEDB_ROLE" -d "$SITEDB_DB" "$@"
  fi
}

# sitedb_pg_dump <outfile> — a custom-format dump, streamed to the host so nothing is left in the container.
sitedb_pg_dump() {
  local out="$1"; shift
  if [ "$SITEDB_USE_CONTAINER" = 1 ]; then
    docker exec -e PGPASSWORD="${PGPASSWORD:-}" "$SITEDB_CONTAINER" \
      pg_dump -Fc -U "$SITEDB_ROLE" "$@" "$SITEDB_DB" </dev/null > "$out"
  else
    pg_dump -Fc -U "$SITEDB_ROLE" "$@" "$SITEDB_DB" -f "$out"
  fi
}

# sitedb_pg_restore <dumpfile> [extra args] — streamed IN over stdin for the same reason.
sitedb_pg_restore() {
  local dump="$1"; shift
  if [ "$SITEDB_USE_CONTAINER" = 1 ]; then
    docker exec -i -e PGPASSWORD="${PGPASSWORD:-}" "$SITEDB_CONTAINER" \
      pg_restore -U "$SITEDB_ROLE" -d "$SITEDB_DB" "$@" < "$dump"
  else
    pg_restore -U "$SITEDB_ROLE" -d "$SITEDB_DB" "$@" "$dump"
  fi
}

# sitedb_has_timescaledb — whether this database carries the extension.
#
# IT DECIDES WHETHER A RESTORE CAN WORK AT ALL. TimescaleDB keeps its own catalog in
# _timescaledb_catalog, with circular foreign keys between hypertable, chunk and continuous_agg; pg_dump
# warns about exactly those three on this appliance. Restoring that catalog with triggers live fails or
# produces hypertables whose chunks are not registered. timescaledb_pre_restore() suspends the extension's
# event triggers and background workers for the duration, and timescaledb_post_restore() puts them back.
sitedb_has_timescaledb() {
  local n
  n="$(sitedb_psql -qAt -c "SELECT count(*) FROM pg_extension WHERE extname='timescaledb'" 2>/dev/null)"
  [ "${n:-0}" != "0" ]
}

# sitedb_psql_maint <database> [args...] — a connection to ANOTHER database on the same cluster.
#
# Needed because a database cannot be dropped from inside itself. Everything else here talks to
# $SITEDB_DB; this is the one exception, and it exists so that "drop and recreate" does not have to
# hardcode how to reach the cluster a second time.
sitedb_psql_maint() {
  local db="$1"; shift
  if [ "$SITEDB_USE_CONTAINER" = 1 ]; then
    docker exec -e PGPASSWORD="${PGPASSWORD:-}" "$SITEDB_CONTAINER"       psql -v ON_ERROR_STOP=1 -U "$SITEDB_ROLE" -d "$db" "$@" </dev/null
  else
    psql -v ON_ERROR_STOP=1 -U "$SITEDB_ROLE" -d "$db" "$@"
  fi
}

# sitedb_recreate_database <tag> — terminate every other connection, then DROP and CREATE $SITEDB_DB.
#
# WHY A RESTORE RECREATES THE DATABASE INSTEAD OF CLEANING IT.
#
# The obvious `pg_restore --clean --if-exists` is WRONG on this database and it was measured being wrong on
# PRE-LIVE. --clean drops the objects in the dump, and the TimescaleDB EXTENSION is one of them. Dropping
# the extension takes its schemas with it -- _timescaledb_functions, _timescaledb_internal -- and then
# every object that depends on them fails to restore:
#
#     ERROR: schema "_timescaledb_functions" does not exist
#     Command was: CREATE TRIGGER ts_insert_blocker BEFORE INSERT ON public.audit_log ...
#     pg_restore: warning: errors ignored on restore: 72
#
# The result was a database with 96 relational tables intact, no timescaledb extension, and both
# hypertables empty. pg_restore reports that as "errors ignored" and exits non-zero, so it is not silent --
# but the appliance is left broken either way, with its services stopped.
#
# So the supported restore does what TimescaleDB's own procedure does: start from an empty database, create
# the extension FIRST, suspend it, restore, and put it back.
sitedb_recreate_database() {
  local tag="${1:-sitedb}"
  sitedb_psql_maint postgres -qAt -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity
     WHERE datname = '$SITEDB_DB' AND pid <> pg_backend_pid()" >/dev/null 2>&1 || true
  sitedb_psql_maint postgres -c "DROP DATABASE IF EXISTS $SITEDB_DB" >/dev/null     || { echo "$tag: FAILED — could not drop $SITEDB_DB (something is still connected)" >&2; return 1; }
  sitedb_psql_maint postgres -c "CREATE DATABASE $SITEDB_DB" >/dev/null     || { echo "$tag: FAILED — dropped $SITEDB_DB and could not recreate it" >&2; return 1; }
  return 0
}
