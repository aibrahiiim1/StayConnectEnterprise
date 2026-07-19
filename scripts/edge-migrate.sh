#!/usr/bin/env bash
# Authoritative edge-DB (site-local) migration runner for data-plane/migrations/NNNN_name.up.sql.
#
# POSITIVE-IDENTITY, FAIL-CLOSED, ATOMIC:
#   * Normal apply REQUIRES the complete expected target identity — none of these is optional:
#       --only <exact-version>            (single migration; --all is a disposable-test convenience only)
#       --expect-db <exact-db-name>       (current_database() must equal it)
#       --target-kind <disposable|live-site>
#       --ack-target <exact-acknowledgement>   disposable => I_UNDERSTAND_DISPOSABLE_DATABASE
#                                              live-site  => I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION (+ expect-db=stayconnect_site)
#       --expect-sha256 <hex>             (SHA-256 of the selected file; the binding pre-apply control)
#     A target is permitted ONLY because its complete expected identity is positively verified — there is
#     NO database-name blacklist.
#   * Migration directory is the canonical repo dir data-plane/migrations, resolved with realpath and
#     compared to the checked-out repository path. Arbitrary --dir is refused in normal/live mode (only a
#     separately-acknowledged disposable test mode may override it). Symlink/traversal/outside-repo/missing/
#     duplicate/symlinked-file are rejected.
#   * The ledger is verified STRUCTURALLY (read-only) BEFORE the lock: schema_migrations exists; version is
#     text NOT NULL PRIMARY KEY; applied_at is timestamptz NOT NULL; no duplicate versions; owner in the
#     approved allowlist; execution role holds exactly the required privileges; the accepted 0009 baseline is
#     present before 0010 is allowed. The apply/skip DECISION for the migration still happens only UNDER the
#     bounded advisory lock (lock -> re-read ledger -> apply-or-SKIP_AFTER_LOCK -> one ledger row -> unlock).
#   * The ONLY public-schema write normal execution performs is the expected schema_migrations metadata row.
#     No public business/IAM table or public-schema business structure is changed. Ledger bootstrap, if ever
#     required, is a SEPARATE standalone administrative operation (--bootstrap-ledger) — never part of a
#     normal migration run, and it applies no migration.
set -euo pipefail
HERE="$(cd "$(dirname "$0")/.." && pwd -P)"
CANON_MIG_DIR="$HERE/data-plane/migrations"
ONLY=""; ALL=0; EXPECT_DB=""; TARGET_KIND=""; ACK=""; EXPECT_SHA=""; DIR_OVERRIDE=""; ACK_DIR=""
BOOTSTRAP=0; BOOTSTRAP_OWNER=""
LEDGER_OWNER_ALLOWLIST="${LEDGER_OWNER_ALLOWLIST:-iam_v2_owner postgres}"
while [ $# -gt 0 ]; do
  case "$1" in
    --only) ONLY="$2"; shift 2;;
    --all) ALL=1; shift;;
    --expect-db) EXPECT_DB="$2"; shift 2;;
    --target-kind) TARGET_KIND="$2"; shift 2;;
    --ack-target) ACK="$2"; shift 2;;
    --expect-sha256) EXPECT_SHA="$2"; shift 2;;
    --dir) DIR_OVERRIDE="$2"; shift 2;;
    --ack-noncanonical-dir) ACK_DIR="$2"; shift 2;;
    --bootstrap-ledger) BOOTSTRAP=1; shift;;
    --bootstrap-owner) BOOTSTRAP_OWNER="$2"; shift 2;;
    *) echo "REFUSED: unknown arg: $1" >&2; exit 2;;
  esac
done
[ -n "${EDGE_PSQL:-}" ] || { echo "REFUSED: EDGE_PSQL not set" >&2; exit 2; }
q(){ $EDGE_PSQL -tAqc "$1"; }
NAME_RE='^[0-9]{4}_[a-z0-9_]+$'

ack_for_kind(){ case "$1" in disposable) echo "I_UNDERSTAND_DISPOSABLE_DATABASE";; live-site) echo "I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION";; *) echo "";; esac; }

verify_target_identity(){ # $1=mode-label
  [ -n "$EXPECT_DB" ]     || { echo "REFUSED: --expect-db is mandatory" >&2; exit 3; }
  [ -n "$TARGET_KIND" ]   || { echo "REFUSED: --target-kind <disposable|live-site> is mandatory" >&2; exit 3; }
  [ -n "$ACK" ]           || { echo "REFUSED: --ack-target is mandatory" >&2; exit 3; }
  local want; want="$(ack_for_kind "$TARGET_KIND")"
  [ -n "$want" ] || { echo "REFUSED: --target-kind must be 'disposable' or 'live-site'" >&2; exit 3; }
  [ "$ACK" = "$want" ] || { echo "REFUSED: --ack-target '$ACK' does not match target-kind=$TARGET_KIND (expected $want)" >&2; exit 3; }
  if [ "$TARGET_KIND" = "live-site" ] && [ "$EXPECT_DB" != "stayconnect_site" ]; then
    echo "REFUSED: live-site target requires --expect-db stayconnect_site" >&2; exit 3
  fi
  local curdb; curdb="$(q 'SELECT current_database()')"
  [ "$curdb" = "$EXPECT_DB" ] || { echo "REFUSED: connected to '$curdb' but --expect-db '$EXPECT_DB'" >&2; exit 3; }
  # positive baseline: the iam_v2 schema must be present (baseline built)
  [ "$(q "SELECT count(*) FROM information_schema.schemata WHERE schema_name='iam_v2'")" = 1 ] \
    || { echo "REFUSED: iam_v2 schema not present in '$curdb' (baseline not built)" >&2; exit 3; }
  if [ "$TARGET_KIND" = "live-site" ]; then
    [ "$(q "SELECT rolsuper FROM pg_roles WHERE rolname=current_user")" = f ] \
      || { echo "REFUSED: live-site execution role must be a least-privilege NON-superuser" >&2; exit 3; }
  fi
}

resolve_mig_dir(){
  local d="$CANON_MIG_DIR"
  if [ -n "$DIR_OVERRIDE" ]; then
    if [ "$TARGET_KIND" != "disposable" ] || [ "$ACK_DIR" != "I_UNDERSTAND_NONCANONICAL_TEST_DIR" ]; then
      echo "REFUSED: --dir override requires target-kind=disposable AND --ack-noncanonical-dir I_UNDERSTAND_NONCANONICAL_TEST_DIR" >&2; exit 3
    fi
    d="$DIR_OVERRIDE"
  fi
  [ -d "$d" ] || { echo "REFUSED: migration directory missing: $d" >&2; exit 3; }
  local rd; rd="$(realpath "$d" 2>/dev/null || true)"
  [ -n "$rd" ] || { echo "REFUSED: cannot resolve migration directory: $d" >&2; exit 3; }
  if [ -z "$DIR_OVERRIDE" ]; then
    local rc; rc="$(realpath "$CANON_MIG_DIR")"
    [ "$rd" = "$rc" ] || { echo "REFUSED: resolved migration dir '$rd' != canonical '$rc' (symlink/traversal escape)" >&2; exit 3; }
    case "$rd" in "$(realpath "$HERE")"/*) : ;; *) echo "REFUSED: migration dir outside repository" >&2; exit 3;; esac
  fi
  echo "$rd"
}

select_file(){ # $1=dir  -> echoes exactly one file path for $ONLY, guarded
  local d="$1" n=0 hit=""
  for g in "$d/$ONLY".up.sql; do [ -e "$g" ] && { n=$((n+1)); hit="$g"; }; done
  [ "$n" -eq 1 ] || { echo "REFUSED: --only '$ONLY' resolves to $n files (need exactly 1)" >&2; exit 2; }
  [ -L "$hit" ] && { echo "REFUSED: migration file is a symlink (rejected): $hit" >&2; exit 3; }
  [ -f "$hit" ] || { echo "REFUSED: migration file not a regular file: $hit" >&2; exit 3; }
  # duplicate-version guard (case-insensitive filesystems / stray copies)
  local dup; dup="$(ls "$d" 2>/dev/null | grep -iE "^${ONLY}\.up\.sql$" | wc -l | tr -d ' ')"
  [ "$dup" = "1" ] || { echo "REFUSED: duplicate migration filenames for '$ONLY' ($dup)" >&2; exit 3; }
  echo "$hit"
}

verify_ledger_structural(){ # read-only, BEFORE lock; fail closed
  [ "$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='schema_migrations'")" = 1 ] \
    || { echo "REFUSED: public.schema_migrations ledger absent; run a separate --bootstrap-ledger first" >&2; exit 3; }
  local vtype vnull anull acount
  vtype="$(q "SELECT data_type FROM information_schema.columns WHERE table_schema='public' AND table_name='schema_migrations' AND column_name='version'")"
  vnull="$(q "SELECT is_nullable FROM information_schema.columns WHERE table_schema='public' AND table_name='schema_migrations' AND column_name='version'")"
  [ "$vtype" = "text" ] && [ "$vnull" = "NO" ] || { echo "REFUSED: ledger 'version' must be text NOT NULL (got $vtype/$vnull)" >&2; exit 3; }
  anull="$(q "SELECT is_nullable FROM information_schema.columns WHERE table_schema='public' AND table_name='schema_migrations' AND column_name='applied_at'")"
  local atype; atype="$(q "SELECT data_type FROM information_schema.columns WHERE table_schema='public' AND table_name='schema_migrations' AND column_name='applied_at'")"
  [ "$atype" = "timestamp with time zone" ] && [ "$anull" = "NO" ] || { echo "REFUSED: ledger 'applied_at' must be timestamptz NOT NULL (got $atype/$anull)" >&2; exit 3; }
  # version must be the primary key
  [ "$(q "SELECT count(*) FROM information_schema.table_constraints tc JOIN information_schema.key_column_usage k ON k.constraint_name=tc.constraint_name WHERE tc.table_schema='public' AND tc.table_name='schema_migrations' AND tc.constraint_type='PRIMARY KEY' AND k.column_name='version'")" = 1 ] \
    || { echo "REFUSED: ledger 'version' is not the PRIMARY KEY" >&2; exit 3; }
  # no duplicate versions
  [ "$(q "SELECT count(*) FROM (SELECT version FROM public.schema_migrations GROUP BY version HAVING count(*)>1) d")" = 0 ] \
    || { echo "REFUSED: duplicate versions present in ledger" >&2; exit 3; }
  # owner allowlist
  local owner; owner="$(q "SELECT tableowner FROM pg_tables WHERE schemaname='public' AND tablename='schema_migrations'")"
  case " $LEDGER_OWNER_ALLOWLIST " in *" $owner "*) : ;; *) echo "REFUSED: ledger owner '$owner' not in allowlist ($LEDGER_OWNER_ALLOWLIST)" >&2; exit 3;; esac
  # APPLY needs exactly SELECT (read the ledger) + INSERT (record the applied version). It must NOT need or
  # hold DELETE/UPDATE/TRUNCATE — those belong to the separate rollback/admin operation, not a forward apply.
  for p in SELECT INSERT; do
    [ "$(q "SELECT has_table_privilege(current_user,'public.schema_migrations','$p')")" = t ] \
      || { echo "REFUSED: apply role lacks required $p on schema_migrations" >&2; exit 3; }
  done
  if [ "$TARGET_KIND" = "live-site" ]; then
    # live-site apply role must be minimal: no destructive ledger rights, no public DDL.
    for p in UPDATE DELETE TRUNCATE REFERENCES TRIGGER; do
      [ "$(q "SELECT has_table_privilege(current_user,'public.schema_migrations','$p')")" = f ] \
        || { echo "REFUSED: live-site apply role must NOT hold $p on schema_migrations (rollback/admin only)" >&2; exit 3; }
    done
    [ "$(q "SELECT has_schema_privilege(current_user,'public','CREATE')")" = f ] \
      || { echo "REFUSED: live-site apply role must NOT hold public CREATE (least privilege)" >&2; exit 3; }
  fi
}

verify_baseline_for(){ # $1=version ; require 0009 present before 0010+
  local ver="$1"
  local num="${ver%%_*}"
  if [ "$((10#$num))" -ge 10 ]; then
    [ "$(q "SELECT count(*) FROM public.schema_migrations WHERE version='0009_phase2_commerce'")" = 1 ] \
      || { echo "REFUSED: accepted baseline 0009_phase2_commerce must be applied before $ver" >&2; exit 3; }
  fi
}

apply_one(){ # $1=file  atomic lock-then-ledger
  local f="$1" base ver sha key out
  base="$(basename "$f")"; ver="${base%.up.sql}"
  echo "$ver" | grep -Eq "$NAME_RE" || { echo "REFUSED: version '$ver' does not match $NAME_RE" >&2; exit 2; }
  sha="$(sha256sum "$f" | awk '{print $1}')"
  if [ -n "$EXPECT_SHA" ]; then
    if [ "$sha" != "$EXPECT_SHA" ]; then
      echo "REFUSED: checksum mismatch for $ver" >&2
      echo "  expected(--expect-sha256): $EXPECT_SHA" >&2
      echo "  actual(sha256 of file):    $sha" >&2
      exit 3
    fi
  elif [ "$ALL" -ne 1 ]; then
    echo "REFUSED: --expect-sha256 is mandatory for a single-migration apply" >&2; exit 3
  fi
  verify_baseline_for "$ver"
  key="$(q "SELECT hashtextextended('stayconnect_edge_migrate:'||'$ver', 0)")"
  echo "  select $ver  file=$base  sha256=$sha  lock_key=$key  db=$EXPECT_DB  kind=$TARGET_KIND"
  out="$(
    { printf "SET statement_timeout='60s';\nSELECT pg_advisory_lock(%s);\nSET statement_timeout=0;\n" "$key"
      printf "SELECT (NOT EXISTS(SELECT 1 FROM public.schema_migrations WHERE version='%s')) AS need \\\\gset\n" "$ver"
      printf "\\\\if :need\n\\\\echo APPLYING_UNDER_LOCK\n"
      cat "$f"
      printf "\nINSERT INTO public.schema_migrations(version) VALUES ('%s') ON CONFLICT DO NOTHING;\n" "$ver"
      printf "\\\\else\n\\\\echo SKIP_AFTER_LOCK\n\\\\endif\n"
      printf "SELECT pg_advisory_unlock(%s);\n" "$key"
    } | $EDGE_PSQL 2>&1
  )"
  if echo "$out" | grep -q "APPLYING_UNDER_LOCK"; then echo "  apply $ver (under lock)"; return 10
  elif echo "$out" | grep -q "SKIP_AFTER_LOCK"; then echo "  skip-after-lock $ver (already applied)"; return 11
  else echo "RUNNER ERROR for $ver:"; echo "$out" | tail -5 >&2; exit 4; fi
}

# ---- BOOTSTRAP MODE (standalone; applies no migration) -------------------------------------------
if [ "$BOOTSTRAP" -eq 1 ]; then
  { [ -z "$ONLY" ] && [ "$ALL" -ne 1 ]; } || { echo "REFUSED: --bootstrap-ledger cannot be combined with --only/--all" >&2; exit 2; }
  [ -n "$EXPECT_DB" ] || { echo "REFUSED: bootstrap requires --expect-db" >&2; exit 3; }
  case "$TARGET_KIND" in
    disposable|live-site) : ;;
    "") echo "REFUSED: bootstrap requires --target-kind" >&2; exit 3;;
    *) echo "REFUSED: bootstrap --target-kind must be 'disposable' or 'live-site' (got '$TARGET_KIND')" >&2; exit 3;;
  esac
  [ "$ACK" = "I_UNDERSTAND_LEDGER_BOOTSTRAP" ] || { echo "REFUSED: bootstrap requires --ack-target I_UNDERSTAND_LEDGER_BOOTSTRAP" >&2; exit 3; }
  [ -n "$BOOTSTRAP_OWNER" ] || { echo "REFUSED: bootstrap requires --bootstrap-owner <role>" >&2; exit 3; }
  if [ "$TARGET_KIND" = "live-site" ] && [ "$EXPECT_DB" != "stayconnect_site" ]; then
    echo "REFUSED: live-site bootstrap requires --expect-db stayconnect_site" >&2; exit 3
  fi
  curdb="$(q 'SELECT current_database()')"
  [ "$curdb" = "$EXPECT_DB" ] || { echo "REFUSED: connected to '$curdb' but --expect-db '$EXPECT_DB'" >&2; exit 3; }
  case " $LEDGER_OWNER_ALLOWLIST " in *" $BOOTSTRAP_OWNER "*) : ;; *) echo "REFUSED: bootstrap owner '$BOOTSTRAP_OWNER' not in allowlist" >&2; exit 3;; esac
  if [ "$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='schema_migrations'")" = 1 ]; then
    echo "REFUSED: ledger already exists; bootstrap is not needed and will not run" >&2; exit 3
  fi
  q "CREATE TABLE public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
  q "ALTER TABLE public.schema_migrations OWNER TO $BOOTSTRAP_OWNER;" >/dev/null
  own="$(q "SELECT tableowner FROM pg_tables WHERE schemaname='public' AND tablename='schema_migrations'")"
  [ "$own" = "$BOOTSTRAP_OWNER" ] || { echo "REFUSED: bootstrap owner verification failed (got $own)" >&2; exit 3; }
  echo "EDGE_LEDGER_BOOTSTRAP_OK db=$EXPECT_DB owner=$own (no migration applied)"
  exit 0
fi

# ---- NORMAL MIGRATION MODE ----------------------------------------------------------------------
if [ -z "$ONLY" ] && [ "$ALL" -ne 1 ]; then echo "REFUSED: specify --only <exact-version> (or --all in disposable mode)" >&2; exit 2; fi
[ -z "$ONLY" ] || echo "$ONLY" | grep -Eq "$NAME_RE" || { echo "REFUSED: --only '$ONLY' does not match $NAME_RE" >&2; exit 2; }
verify_target_identity "normal"
if [ "$ALL" -eq 1 ] && [ "$TARGET_KIND" != "disposable" ]; then
  echo "REFUSED: --all is a disposable-test convenience; live/single apply must use --only" >&2; exit 3
fi
MIG_DIR="$(resolve_mig_dir)"
verify_ledger_structural

applied=0; skipped=0
if [ -n "$ONLY" ]; then
  f="$(select_file "$MIG_DIR")"
  set +e; apply_one "$f"; rc=$?; set -e
  [ "$rc" = 10 ] && applied=$((applied+1)); [ "$rc" = 11 ] && skipped=$((skipped+1))
  { [ "$rc" = 10 ] || [ "$rc" = 11 ]; } || exit "$rc"
else
  for f in "$MIG_DIR"/*.up.sql; do
    [ -f "$f" ] || continue
    set +e; apply_one "$f"; rc=$?; set -e
    [ "$rc" = 10 ] && applied=$((applied+1)); [ "$rc" = 11 ] && skipped=$((skipped+1))
    { [ "$rc" = 10 ] || [ "$rc" = 11 ]; } || exit "$rc"
  done
fi
echo "EDGE_MIGRATE_OK applied=$applied skipped=$skipped"
