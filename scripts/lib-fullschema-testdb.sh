# shellcheck shell=bash
# THE FULL APPLIANCE SCHEMA, FOR A DISPOSABLE INTEGRATION DATABASE. Sourced, not executed.
#
# The integration harnesses used to build their databases from HISTORICAL partial chains (the iam_v2 scratch
# fixture plus a hand-picked list of migrations, stopping at 0026 or at ~0079). Those chains can never carry
# migration 0100, and the product now queries what 0100 creates, so a suite run on them tests a schema no
# appliance has. This builds exactly what a factory-clean appliance builds, in the order scripts/sitedb-dev.sh
# and scripts/prod-privilege-integration.sh use:
#
#   Gate-P service roles -> the stayconnect platform role -> Gate-P IAM roles (BEFORE the baseline: its own
#   privileges name iam_v2_owner) -> the production baseline, as the platform role -> Gate-P IAM ownership ->
#   every data-plane migration numbered ABOVE the baseline, in order -> Gate-P ownership and grants again
#   (the grant-durability rule: a grant only a migration makes must survive a reconcile).
#
# Disposable test infrastructure only: no appliance, no Central, no PMS, no provider.
#
#   fullschema_build <container> <host-port> <db>
#
# Return codes follow the harness contract: 0 built; 1 the SCHEMA did not apply (deterministic -- a broken
# baseline or migration fails the same way twice, so CI must not retry); 2 the container never became usable
# (transient; the only retryable case).

FULLSCHEMA_IMAGE="${FULLSCHEMA_IMAGE:-timescale/timescaledb:2.16.1-pg16}"
FULLSCHEMA_BASELINE_TOP="${FULLSCHEMA_BASELINE_TOP:-0100}"

fullschema_psql() { # <container> <db> [psql args...]  -- stdin is the script
  local c="$1" db="$2"; shift 2
  docker exec -i "$c" psql -U postgres -d "$db" -v ON_ERROR_STOP=1 "$@"
}

fullschema_post_baseline() { # the migrations published after the baseline snapshot, in order
  local root="$1" f n
  ls "$root/data-plane/migrations" | grep -E '^[0-9]{4}_.*\.up\.sql$' | sort | while read -r f; do
    n="${f%%_*}"
    if [ "$n" \> "$FULLSCHEMA_BASELINE_TOP" ]; then echo "${f%.up.sql}"; fi
  done
}

fullschema_build() {
  local c="$1" port="$2" db="$3"
  local root; root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  local base="$root/data-plane/migrations/baseline/0000_production_baseline.sql"
  local gatep="$root/deploy/gatep"
  local log; log="$(mktemp -d)"
  local ready=0 ok m f

  [ -f "$base" ] || { echo "FULLSCHEMA: the production baseline is missing"; return 1; }

  docker rm -f "$c" >/dev/null 2>&1 || true
  docker run -d --name "$c" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$db" \
    -p "127.0.0.1:$port:5432" "$FULLSCHEMA_IMAGE" >/dev/null 2>&1 \
    || { echo "INFRA: could not start the disposable container $c"; return 2; }
  # The TimescaleDB image runs initdb's transient server first and then restarts: readiness is the SECOND
  # "ready to accept connections" plus three clean queries, not the first successful one.
  for _ in $(seq 1 180); do
    if [ "$(docker logs "$c" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ]; then
      ok=0
      for _ in 1 2 3; do
        docker exec "$c" psql -U postgres -d "$db" -tAqc 'select 1' >/dev/null 2>&1 && ok=$((ok+1))
        sleep 1
      done
      [ "$ok" = 3 ] && { ready=1; break; }
    fi
    sleep 1
  done
  [ "$ready" = 1 ] || { echo "INFRA: $c never became ready"; docker logs "$c" 2>&1 | tail -10; return 2; }

  fullschema_psql "$c" "$db" < "$gatep/gatep-roles.sql" >"$log/roles.log" 2>&1 \
    || { echo "FULLSCHEMA: gatep-roles.sql failed"; tail -5 "$log/roles.log"; return 1; }
  fullschema_psql "$c" "$db" -tAqc \
    "DO \$\$ BEGIN CREATE ROLE stayconnect NOLOGIN SUPERUSER; EXCEPTION WHEN duplicate_object THEN NULL; END \$\$;" >/dev/null \
    || { echo "FULLSCHEMA: platform role"; return 1; }
  # MSYS_NO_PATHCONV: Git Bash would otherwise rewrite /tmp/gatep into a Windows path inside the container.
  docker cp "$gatep" "$c:/tmp/gatep" >/dev/null || { echo "INFRA: docker cp"; return 2; }
  MSYS_NO_PATHCONV=1 docker exec -i "$c" psql -U postgres -d "$db" -v ON_ERROR_STOP=1 -q \
    -f /tmp/gatep/gatep-iam-roles.sql >"$log/iam-roles.log" 2>&1 \
    || { echo "FULLSCHEMA: gatep-iam-roles.sql failed"; tail -5 "$log/iam-roles.log"; return 1; }
  if ! { printf 'SET ROLE stayconnect;\n'; cat "$base"; } | fullschema_psql "$c" "$db" -q >"$log/baseline.log" 2>&1; then
    echo "FULLSCHEMA: the production baseline did not apply"
    grep -iE '^ERROR|^psql:' "$log/baseline.log" | tail -5
    return 1
  fi
  MSYS_NO_PATHCONV=1 docker exec -i "$c" psql -U postgres -d "$db" -v ON_ERROR_STOP=1 -q \
    -f /tmp/gatep/gatep-iam-ownership.sql >"$log/own.log" 2>&1 \
    || { echo "FULLSCHEMA: gatep-iam-ownership.sql failed"; tail -5 "$log/own.log"; return 1; }
  for m in $(fullschema_post_baseline "$root"); do
    if ! fullschema_psql "$c" "$db" -q < "$root/data-plane/migrations/$m.up.sql" >"$log/$m.log" 2>&1; then
      echo "MIGRATION FAILED: $m -- deterministic, not a flake"
      tail -10 "$log/$m.log"
      return 1
    fi
    fullschema_psql "$c" "$db" -tAqc \
      "INSERT INTO public.schema_migrations(version) VALUES ('$m') ON CONFLICT DO NOTHING" >/dev/null 2>&1 || true
    echo "  applied $m"
  done
  for f in gatep-iam-roles.sql gatep-iam-ownership.sql gatep-grants.sql; do
    MSYS_NO_PATHCONV=1 docker exec -i "$c" psql -U postgres -d "$db" -v ON_ERROR_STOP=1 -q \
      -f "/tmp/gatep/$f" >"$log/$f.re.log" 2>&1 \
      || { echo "FULLSCHEMA: $f (reconcile) failed"; tail -5 "$log/$f.re.log"; return 1; }
  done
  # The census that proves 0100 is really there: the posting target replaced the folio model.
  local a1
  a1="$(docker exec "$c" psql -U postgres -d "$db" -tAqc "SELECT
      (SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2'
          AND table_name='pms_interface_revisions' AND column_name='posting_target_model')
    + (SELECT count(*) FROM information_schema.columns WHERE table_schema='iam_v2'
          AND table_name='pms_postings' AND column_name='g_number')
    + (CASE WHEN to_regclass('iam_v2.folios') IS NULL THEN 1 ELSE 0 END)")"
  [ "${a1:-0}" = 3 ] || { echo "FULLSCHEMA: migration 0100 is not in effect (census=$a1)"; return 1; }
  rm -rf "$log"
  return 0
}

# A disposable LOGIN that holds exactly one production group role and nothing else. The group roles are
# NOLOGIN by design, so a restricted end-to-end proof needs a principal that inherits one of them.
fullschema_login() { # <container> <db> <login> <password> <group role>
  local c="$1" db="$2" login="$3" pw="$4" grp="$5"
  docker exec "$c" psql -U postgres -d "$db" -v ON_ERROR_STOP=1 -tAqc "DO \$\$ BEGIN
       IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='$login') THEN
         CREATE ROLE $login LOGIN PASSWORD '$pw' INHERIT;
       END IF;
     END \$\$;
     GRANT $grp TO $login;
     GRANT CONNECT ON DATABASE $db TO $login;
     GRANT USAGE ON SCHEMA public TO $login;" >/dev/null
}

# Give a Gate-P LOGIN service role (svc_posting, svc_pmsd, ...) a password inside the disposable database, so a
# suite can connect as the REAL role with exactly the privileges deploy/gatep gives it.
fullschema_service_password() { # <container> <db> <role> <password>
  docker exec "$1" psql -U postgres -d "$2" -v ON_ERROR_STOP=1 -tAqc "ALTER ROLE $3 PASSWORD '$4';" >/dev/null
}
