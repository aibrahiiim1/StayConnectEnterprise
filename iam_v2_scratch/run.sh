#!/usr/bin/env bash
# iam_v2 scratch orchestrator (disposable DB only). Commands: fixture | up | down | reup | fresh
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"
MIG="$HERE/migrations"; IDX="guest_networks_tsi_anchor"

up() { for f in mg1_pms_interface_core mg2_plans_packages mg3_identities_credentials mg4_stay_domain \
                 mg5_auth_commerce mg6_entitlements_devices_sessions mg7_postings_payments \
                 mg8_resolution_aux mg9_engine; do
  echo "  apply $f"; psqlf "$MIG/$f.sql" >/dev/null; done; echo "UP_OK"; }

down() { psqlx "DROP SCHEMA IF EXISTS iam_v2 CASCADE;" >/dev/null
  # MG-0 anchor drop is non-transactional (mirrors CONCURRENTLY build)
  psql_ac "DROP INDEX CONCURRENTLY IF EXISTS public.$IDX;" >/dev/null || true
  echo "DOWN_OK"; }

case "${1:-}" in
  fixture) safety_guard; psqlf "$HERE/00_platform_fixture.sql" >/dev/null; echo "FIXTURE_OK";;
  up)   up;;
  down) down;;
  reup) up;;
  fresh)
    safety_guard
    psqlx "DROP SCHEMA IF EXISTS iam_v2 CASCADE;" >/dev/null
    psql_ac "DROP INDEX CONCURRENTLY IF EXISTS public.$IDX;" >/dev/null || true
    psqlx "DROP TABLE IF EXISTS public.guest_networks, public.sites, public.tenants CASCADE;" >/dev/null
    psqlf "$HERE/00_platform_fixture.sql" >/dev/null; echo "  fixture ok"
    bash "$HERE/mg0.sh" | sed 's/^/  /'
    up;;
  *) echo "usage: run.sh fixture|up|down|reup|fresh"; exit 2;;
esac
