#!/usr/bin/env bash
# MG-0 — additive UNIQUE(tenant_id,site_id,id) anchor on public.guest_networks.
# Non-transactional (CREATE UNIQUE INDEX CONCURRENTLY cannot run in a txn block).
# Implements: duplicate pre-check, validity guard (no bare IF NOT EXISTS), CONCURRENTLY build,
# indisvalid verification + exact definition check, invalid-index detection + DROP INDEX CONCURRENTLY,
# and a proven interruption-recovery cycle.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; source "$HERE/lib.sh"
IDX="guest_networks_tsi_anchor"

step() { echo "  MG0: $*"; }

# 1) duplicate pre-check (must be zero)
dups="$(psqlx "SELECT count(*) FROM (SELECT tenant_id,site_id,id FROM public.guest_networks GROUP BY 1,2,3 HAVING count(*)>1) d;")"
step "duplicate pre-check rows=$dups"
[ "$dups" = "0" ] || { echo "MG0 ABORT: duplicate (tenant,site,id) rows exist"; exit 40; }

# 2) validity guard: if the index exists but is INVALID, drop it first (never accept an invalid index; no bare IF NOT EXISTS skip)
build_anchor() {
  local exists valid
  exists="$(psqlx "SELECT count(*) FROM pg_class WHERE relname='$IDX' AND relkind='i';")"
  if [ "$exists" != "0" ]; then
    valid="$(psqlx "SELECT indisvalid FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE c.relname='$IDX';")"
    if [ "$valid" = "t" ]; then step "anchor already exists and is VALID — nothing to do"; return 0; fi
    step "anchor exists but INVALID ($valid) — dropping before rebuild"
    psql_ac "DROP INDEX CONCURRENTLY public.$IDX;"
  fi
  # 3) build concurrently, outside any transaction (NO bare IF NOT EXISTS)
  step "CREATE UNIQUE INDEX CONCURRENTLY (autocommit)"
  psql_ac "CREATE UNIQUE INDEX CONCURRENTLY $IDX ON public.guest_networks (tenant_id, site_id, id);"
}
build_anchor

# 4) verify indisvalid + indisready + exact definition
valid="$(psqlx "SELECT indisvalid::text FROM pg_index WHERE indexrelid='public.$IDX'::regclass;")"
ready="$(psqlx "SELECT indisready::text FROM pg_index WHERE indexrelid='public.$IDX'::regclass;")"
defok="$(psqlx "SELECT (indexdef = 'CREATE UNIQUE INDEX $IDX ON public.guest_networks USING btree (tenant_id, site_id, id)')::text FROM pg_indexes WHERE indexname='$IDX';")"
step "verify indisvalid=$valid indisready=$ready exact_def=$defok"
case "$valid" in t|true) ;; *) echo "MG0 FAIL: indisvalid=$valid"; exit 41;; esac
case "$ready" in t|true) ;; *) echo "MG0 FAIL: indisready=$ready"; exit 41;; esac
case "$defok" in t|true) ;; *) echo "MG0 FAIL: index definition mismatch"; exit 41;; esac
echo "MG0_OK"
