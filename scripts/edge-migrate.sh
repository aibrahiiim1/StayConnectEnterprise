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
#
# ---------------------------------------------------------------------------------------------------------
# DOWN MODE (--down): the same gate, inverted, plus three guards a forward apply does not need.
# ---------------------------------------------------------------------------------------------------------
# There was no --down. docs/PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md printed
#
#     bash scripts/edge-migrate.sh --down --only 0010_phase3_stay_resolution --expect-db <db> ...
#
# and this runner answered `REFUSED: unknown arg: --down` and exit 2 — the string "down" did not appear in
# the file. So the documented live rollback was unexecutable, and the real one was a hand-run psql outside
# every control here. That is the gap this mode closes.
#
#   usage: --down --only <version> --expect-db <db> --target-kind <kind> --ack-target <ack>
#          --ack-down <ack> --expect-sha256 <sha of the .down.sql>
#
# WHAT IS THE SAME: exact single version, canonical directory, symlink/duplicate rejection, positive target
# identity, structural ledger verification, the bounded advisory lock on the same key, forced
# ON_ERROR_STOP, and two independent proofs of the outcome.
#
# WHAT IS DIFFERENT, and why each is necessary:
#
#   1. A SECOND, DOWN-SPECIFIC ACKNOWLEDGEMENT (--ack-down). --ack-target says which database you mean;
#      --ack-down says you mean to REMOVE schema from it. disposable => I_UNDERSTAND_DISPOSABLE_DOWN_MIGRATION,
#      live-site => I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION. One flag cannot carry both meanings: an operator
#      who has typed the live-site apply acknowledgement a hundred times must not be one --down away from
#      dropping a schema.
#
#   2. HEAD-OF-LEDGER. The version being rolled back must be the HIGHEST applied version. Rolling back 0060
#      while 0085 is applied would leave every migration in between standing on objects that no longer
#      exist, and nothing in a down script checks that — each one only knows how to undo itself. Refusing is
#      the only safe answer, and the message names the head so the operator knows what to do first.
#
#   3. THE LEDGER PRIVILEGE IS THE OPPOSITE ONE. A forward apply needs SELECT + INSERT and is REFUSED on a
#      live site if it holds DELETE (line ~186). A down needs SELECT + DELETE, because removing the version
#      row is what makes the rollback re-appliable. So a down CANNOT be run by the forward apply role, by
#      construction, and this mode demands the privilege the forward mode forbids. That asymmetry is the
#      point: the role that routinely migrates forward cannot roll back.
#
# THE ON_ERROR_STOP REASONING INVERTS TOO, and the inverted form is worse. Without it a FAILED down
# migration still reaches the appended ledger DELETE: the down body's COMMIT degrades to ROLLBACK so the
# objects survive, and then the DELETE commits in its own implicit transaction. The result is a database
# whose objects are present and whose ledger says they are not — so the next forward apply would try to
# create what already exists, and fail. Both proofs below are therefore required: psql's exit status, and
# the ledger row actually being GONE.
#
# WHAT THIS MODE DOES NOT DO. It does not take a backup, and it does not pretend to have verified one. A
# backup before a live down-migration is policy, it belongs in the runbook, and an acknowledgement flag this
# script could not check would be theatre rather than a guard.
set -euo pipefail
HERE="$(cd "$(dirname "$0")/.." && pwd -P)"
CANON_MIG_DIR="$HERE/data-plane/migrations"
ONLY=""; ALL=0; EXPECT_DB=""; TARGET_KIND=""; ACK=""; EXPECT_SHA=""; DIR_OVERRIDE=""; ACK_DIR=""
BOOTSTRAP=0; BOOTSTRAP_OWNER=""; APPLY_ROLE=""
DOWN=0; ACK_DOWN=""
LEDGER_OWNER_ALLOWLIST="${LEDGER_OWNER_ALLOWLIST:-iam_v2_owner postgres}"
while [ $# -gt 0 ]; do
  case "$1" in
    --apply-role) APPLY_ROLE="$2"; shift 2;;
    --only) ONLY="$2"; shift 2;;
    --all) ALL=1; shift;;
    --expect-db) EXPECT_DB="$2"; shift 2;;
    --target-kind) TARGET_KIND="$2"; shift 2;;
    --ack-target) ACK="$2"; shift 2;;
    --expect-sha256) EXPECT_SHA="$2"; shift 2;;
    --dir) DIR_OVERRIDE="$2"; shift 2;;
    --ack-noncanonical-dir) ACK_DIR="$2"; shift 2;;
    --down) DOWN=1; shift;;
    --ack-down) ACK_DOWN="$2"; shift 2;;
    --bootstrap-ledger) BOOTSTRAP=1; shift;;
    --bootstrap-owner) BOOTSTRAP_OWNER="$2"; shift 2;;
    *) echo "REFUSED: unknown arg: $1" >&2; exit 2;;
  esac
done
[ -n "${EDGE_PSQL:-}" ] || { echo "REFUSED: EDGE_PSQL not set" >&2; exit 2; }

# --apply-role runs the whole migration as a named role via SET ROLE, so that the connection may be made by
# whatever account can reach the database while the migration is EXECUTED by the least-privileged role that owns
# the schema.
#
# It exists because discovering this took live time during Increment 9. The site appliance connects as the
# cluster superuser `stayconnect`, which the live-site gate below correctly refuses (it holds public CREATE).
# The correct executor is `iam_v2_owner`, which owns all 49 pre-existing iam_v2 tables — so applying as anyone
# else also silently changes object ownership. Operators worked that out by hand and expressed it as a
# PGOPTIONS=-crole=… string wrapped around EDGE_PSQL, which is easy to get wrong and invisible in the run log.
# Naming the role as an argument makes it explicit, checked, and recorded.
#
# It grants nothing. SET ROLE can only reach a role the connecting account is already a member of, and every
# least-privilege check below evaluates current_user AFTER the switch, so the role is held to exactly the same
# standard it would be if it had connected directly.
ROLE_PREFIX=""
if [ -n "$APPLY_ROLE" ]; then
  echo "$APPLY_ROLE" | grep -Eq '^[a-z_][a-z0-9_]*$' || { echo "REFUSED: --apply-role '$APPLY_ROLE' is not a plain role name" >&2; exit 2; }
  ROLE_PREFIX="SET ROLE $APPLY_ROLE; "
fi
q(){ $EDGE_PSQL -tAqc "${ROLE_PREFIX}$1"; }
NAME_RE='^[0-9]{4}_[a-z0-9_]+$'

ack_for_kind(){ case "$1" in disposable) echo "I_UNDERSTAND_DISPOSABLE_DATABASE";; live-site) echo "I_UNDERSTAND_LIVE_DARK_SITE_MIGRATION";; *) echo "";; esac; }
# A SEPARATE VOCABULARY FOR REMOVING SCHEMA. Deliberately not derived from the apply acknowledgement, so
# muscle memory for one cannot satisfy the other.
ack_down_for_kind(){ case "$1" in disposable) echo "I_UNDERSTAND_DISPOSABLE_DOWN_MIGRATION";; live-site) echo "I_UNDERSTAND_LIVE_SITE_DOWN_MIGRATION";; *) echo "";; esac; }

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
  # disposable mode must carry a HARNESS-generated marker, not merely a caller assertion. Use pg_class
  # (not privilege-filtered information_schema) so the check is independent of the execution role's grants.
  if [ "$TARGET_KIND" = "disposable" ]; then
    [ "$(q "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='_scratch_marker'")" = 1 ] \
      || { echo "REFUSED: disposable target requires a harness-generated marker (public._scratch_marker)" >&2; exit 3; }
  fi
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

select_down_file(){ # $1=dir -> echoes exactly one .down.sql path for $ONLY, guarded like the up half
  local d="$1" n=0 hit=""
  for g in "$d/$ONLY".down.sql; do [ -e "$g" ] && { n=$((n+1)); hit="$g"; }; done
  [ "$n" -eq 1 ] || { echo "REFUSED: --only '$ONLY' resolves to $n down-migration files (need exactly 1)" >&2; exit 2; }
  [ -L "$hit" ] && { echo "REFUSED: down-migration file is a symlink (rejected): $hit" >&2; exit 3; }
  [ -f "$hit" ] || { echo "REFUSED: down-migration file not a regular file: $hit" >&2; exit 3; }
  local dup; dup="$(ls "$d" 2>/dev/null | grep -iE "^${ONLY}\.down\.sql$" | wc -l | tr -d ' ')"
  [ "$dup" = "1" ] || { echo "REFUSED: duplicate down-migration filenames for '$ONLY' ($dup)" >&2; exit 3; }
  echo "$hit"
}

verify_ledger_structural_down(){ # read-only, BEFORE lock; fail closed. The privilege mirror of the up half.
  [ "$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='schema_migrations'")" = 1 ]     || { echo "REFUSED: public.schema_migrations ledger absent; there is nothing to roll back" >&2; exit 3; }
  local who; who="$(q "SELECT current_user")"
  # A DOWN NEEDS THE PRIVILEGE A FORWARD APPLY IS FORBIDDEN. Removing the version row is what makes the
  # rollback re-appliable, so SELECT + DELETE, and the forward path refuses DELETE on a live site -- which
  # means the routine migrating role cannot roll back, by construction.
  for p in SELECT DELETE; do
    if [ "$(q "SELECT has_table_privilege(current_user,'public.schema_migrations','$p')")" != t ]; then
      echo "REFUSED: down role '$who' lacks required $p on public.schema_migrations." >&2
      echo "         A down-migration removes its own ledger row, so it needs exactly:" >&2
      echo "           GRANT SELECT, DELETE ON public.schema_migrations TO $who;" >&2
      echo "         This is NOT the forward apply role: that one is refused DELETE on a live site by" >&2
      echo "         design, and this mode demands it. Use the rollback/admin role." >&2
      exit 3
    fi
  done
}

# THE LEDGER LOCK. Every mutation of public.schema_migrations takes this ONE key before it takes its own
# per-version key, and the reason is a race a per-version lock cannot close.
#
# The down path refuses to roll back anything but the head of the ledger. That check ran before the lock was
# taken, and the lock key was derived from the version being reverted -- so a forward apply of a HIGHER
# version took a DIFFERENT key, was not blocked by it, and could commit in the window between the head
# check and the revert. The revert then removed a migration that was no longer the head, leaving the newer
# one standing on objects that had just been dropped. The under-lock recheck could not catch it either: it
# asked only whether the row still existed, which it did.
#
# Serialising every ledger mutation on one key is the honest fix. Migrations are inherently sequential, so
# there is nothing to lose: two runners racing to change the schema of one database is not a case worth
# preserving concurrency for. The per-version key is KEPT as well, because it is what makes a repeated apply
# of the SAME migration a clean SKIP_AFTER_LOCK rather than a wait-then-redo.
ledger_lock_key(){ q "SELECT hashtextextended('stayconnect_edge_migrate:ledger', 0)"; }

verify_head_of_ledger(){ # $1=version ; refuse to roll back anything but the highest applied version
  local ver="$1" head applied
  applied="$(q "SELECT count(*) FROM public.schema_migrations WHERE version='$ver'")"
  [ "$applied" = "1" ] || {
    echo "REFUSED: $ver is not applied to this database, so there is nothing to roll back." >&2
    echo "         A down-migration whose up never ran would drop objects another migration owns." >&2
    exit 3; }
  # The numbered head only. iam_base/* rows are applied by name rather than in sequence and are not part of
  # the numbered ordering a rollback walks backwards through.
  head="$(q "SELECT coalesce(max(version),'') FROM public.schema_migrations WHERE version ~ '^[0-9]{4}_'")"
  [ "$head" = "$ver" ] || {
    echo "REFUSED: $ver is not the head of the ledger -- the highest applied numbered migration is $head." >&2
    echo "         Rolling back out of order would leave every migration between $ver and $head standing" >&2
    echo "         on objects that no longer exist, and a down script only knows how to undo itself." >&2
    echo "         Roll back from the head downwards, one migration at a time." >&2
    exit 3; }
  echo "  head-of-ledger confirmed: $ver is the highest applied numbered migration"
}

revert_one(){ # $1=down-file  atomic lock-then-ledger, mirrored from apply_one
  local f="$1" base ver sha key out rc
  base="$(basename "$f")"; ver="${base%.down.sql}"
  echo "$ver" | grep -Eq "$NAME_RE" || { echo "REFUSED: version '$ver' does not match $NAME_RE" >&2; exit 2; }
  sha="$(sha256sum "$f" | awk '{print $1}')"
  [ -n "$EXPECT_SHA" ] || { echo "REFUSED: --expect-sha256 is mandatory for a down-migration" >&2; exit 3; }
  if [ "$sha" != "$EXPECT_SHA" ]; then
    echo "REFUSED: checksum mismatch for $ver (down)" >&2
    echo "  expected(--expect-sha256): $EXPECT_SHA" >&2
    echo "  actual(sha256 of file):    $sha" >&2
    exit 3
  fi
  verify_head_of_ledger "$ver"
  key="$(q "SELECT hashtextextended('stayconnect_edge_migrate:'||'$ver', 0)")"
  lkey="$(ledger_lock_key)"
  echo "  revert $ver  file=$base  sha256=$sha  lock_key=$key  ledger_lock=$lkey  db=$EXPECT_DB  kind=$TARGET_KIND"
  out="$(
    { [ -n "$APPLY_ROLE" ] && printf "SET ROLE %s;\n" "$APPLY_ROLE"
      printf "SET statement_timeout='60s';\nSELECT pg_advisory_lock(%s);\n" "$lkey"
      printf "SELECT pg_advisory_lock(%s);\nSET statement_timeout=0;\n" "$key"
      # HEAD IS RE-ESTABLISHED UNDER THE LOCK, not merely existence. The check before the lock is what
      # gives the operator a readable refusal; this one is what makes it true at the moment it matters.
      printf "SELECT EXISTS(SELECT 1 FROM public.schema_migrations WHERE version='%s') AS present,\n" "$ver"
      printf "       (coalesce((SELECT max(version) FROM public.schema_migrations WHERE version ~ '^[0-9]{4}_'),'') = '%s') AS is_head \\\\gset\n" "$ver"
      printf "\\\\if :present\n\\\\if :is_head\n\\\\echo REVERTING_UNDER_LOCK\n"
      cat "$f"
      printf "\nDELETE FROM public.schema_migrations WHERE version='%s';\n" "$ver"
      printf "\\\\else\n\\\\echo NOT_HEAD_AFTER_LOCK\n\\\\endif\n"
      printf "\\\\else\n\\\\echo SKIP_AFTER_LOCK\n\\\\endif\n"
      printf "SELECT pg_advisory_unlock(%s);\nSELECT pg_advisory_unlock(%s);\n" "$key" "$lkey"
    } | $EDGE_PSQL -v ON_ERROR_STOP=1 2>&1
  )"
  rc=$?
  if echo "$out" | grep -q "NOT_HEAD_AFTER_LOCK"; then
    echo "REFUSED: $ver was the head when this runner checked and is NOT the head now -- another runner" >&2
    echo "         applied a higher migration while this one was starting. Nothing was reverted." >&2
    echo "         Re-read the ledger and roll back from the new head downwards." >&2
    exit 3
  fi
  if echo "$out" | grep -q "REVERTING_UNDER_LOCK"; then
    # TWO INDEPENDENT PROOFS, and for a down the second one matters more than for an apply. Without
    # ON_ERROR_STOP a failed down still reaches the appended DELETE: the body's COMMIT degrades to ROLLBACK
    # so the objects SURVIVE, and the DELETE then commits on its own. The database would hold objects the
    # ledger denies, and the next forward apply would try to create what is already there.
    if [ "$rc" != "0" ]; then
      echo "RUNNER ERROR: $ver down FAILED (psql exit $rc) -- nothing was reverted" >&2
      echo "$out" | grep -iE "^(ERROR|FATAL|psql:)" | head -5 >&2
      exit 4
    fi
    if [ "$(q "SELECT count(*) FROM public.schema_migrations WHERE version='$ver'")" != "0" ]; then
      echo "RUNNER ERROR: $ver was reverted but its ledger row REMAINS -- the down FAILED and rolled back" >&2
      echo "$out" | grep -iE "^(ERROR|FATAL|psql:)" | head -5 >&2
      exit 4
    fi
    echo "  revert $ver (under lock)"; return 10
  elif echo "$out" | grep -q "SKIP_AFTER_LOCK"; then
    echo "  skip-after-lock $ver (not applied)"; return 11
  else echo "RUNNER ERROR for $ver (down):"; echo "$out" | tail -5 >&2; exit 4; fi
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
  #
  # A CLUSTER SUPERUSER IS ALWAYS AN ACCEPTABLE LEDGER OWNER, whatever it is called. The allowlist defaults to
  # "iam_v2_owner postgres" because those are the names in the reference deployment, and on the site appliance
  # the check therefore refused a perfectly correct setup: the ledger is owned by `stayconnect`, which IS that
  # cluster's superuser — there is no role named `postgres` at all. The rule the check is really trying to
  # express is "the ledger is not owned by some ordinary role that could tamper with it", and superuser
  # ownership satisfies that more strongly than the hard-coded names do. Operators had to override the
  # allowlist by hand to proceed, which is exactly the sort of ad-hoc weakening this formalisation removes.
  local owner; owner="$(q "SELECT tableowner FROM pg_tables WHERE schemaname='public' AND tablename='schema_migrations'")"
  local owner_super; owner_super="$(q "SELECT rolsuper FROM pg_roles WHERE rolname='$owner'")"
  case " $LEDGER_OWNER_ALLOWLIST " in
    *" $owner "*) : ;;
    *)
      if [ "$owner_super" = "t" ]; then
        echo "  ledger owner '$owner' not in allowlist ($LEDGER_OWNER_ALLOWLIST) but IS a cluster superuser; accepted"
      else
        echo "REFUSED: ledger owner '$owner' not in allowlist ($LEDGER_OWNER_ALLOWLIST) and is not a cluster superuser" >&2; exit 3
      fi
      ;;
  esac
  # APPLY needs exactly SELECT (read the ledger) + INSERT (record the applied version). It must NOT need or
  # hold DELETE/UPDATE/TRUNCATE — those belong to the separate rollback/admin operation, not a forward apply.
  local who; who="$(q "SELECT current_user")"
  for p in SELECT INSERT; do
    if [ "$(q "SELECT has_table_privilege(current_user,'public.schema_migrations','$p')")" != t ]; then
      # Name the remedy. This is the one precondition an otherwise correctly-provisioned appliance is likely
      # to be missing, and "lacks required INSERT" alone sent an operator hunting for which role and which
      # grant during a live window.
      echo "REFUSED: apply role '$who' lacks required $p on public.schema_migrations." >&2
      echo "         Grant exactly the two privileges a forward apply needs, and nothing more:" >&2
      echo "           GRANT SELECT, INSERT ON public.schema_migrations TO $who;" >&2
      exit 3
    fi
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

verify_baseline_for(){ # $1=version ; the Phase-2 commerce baseline must be present before 0010+
  local ver="$1"
  local num="${ver%%_*}"
  [ "$((10#$num))" -ge 10 ] || return 0

  # THE UPGRADE PATH: the ledger says 0009 was applied. Unchanged, and still the first thing tried.
  if [ "$(q "SELECT count(*) FROM public.schema_migrations WHERE version='0009_phase2_commerce'")" = 1 ]; then
    return 0
  fi

  # A FACTORY-CLEAN APPLIANCE HAS NO 0009 ROW AND NEVER WILL.
  #
  # It is built from data-plane/migrations/baseline/0000_production_baseline.sql — a dump of the upgrade
  # path's END STATE — precisely so a new appliance never constructs the superseded guest-IAM tables even
  # momentarily. Migrations 0001..0049 are therefore not applied individually and are not in its ledger; the
  # first row such an appliance ever records is the first migration published AFTER its baseline was cut.
  #
  # This check refused every one of those. It read the ledger for evidence of an event that, on that install
  # path, correctly never happened — so the authoritative runner could not apply a migration to the very
  # appliances the baseline exists to produce, and operators went around it. A guard that cannot be satisfied
  # by a supported installation is not enforcing a precondition; it is training people to bypass it.
  #
  # WHAT THE CHECK IS ACTUALLY FOR is that the Phase-2 commerce baseline is PRESENT before anything from 0010
  # onward touches the database. A ledger row is one proof of that. The structures themselves are a stronger
  # and more direct one, so on a database with no 0009 row we ask the schema instead of the ledger: 0009's own
  # artefacts — the purchase/quote pin-equality writer and the offer-quote immutability writer — must exist.
  #
  # AND ONLY ON A DATABASE THAT WAS NEVER WALKED UP THE MIGRATION PATH. If the ledger holds ANY migration
  # below 0010, this is an upgrade-path installation that is missing 0009, which is exactly the broken state
  # the original check was written to catch, and it is still refused. A baseline install has no such row.
  local early
  early="$(q "SELECT count(*) FROM public.schema_migrations WHERE substring(version from 1 for 4) < '0010'")"
  if [ "${early:-1}" != "0" ]; then
    echo "REFUSED: accepted baseline 0009_phase2_commerce must be applied before $ver" >&2
    echo "         The ledger records $early migration(s) below 0010, so this is an upgrade-path" >&2
    echo "         installation with a gap — not a factory-clean baseline install." >&2
    exit 3
  fi

  local commerce
  commerce="$(q "SELECT (to_regprocedure('iam_v2.trg_purchase_quote_pin_equal()') IS NOT NULL
                     AND to_regprocedure('iam_v2.trg_offer_quote_immutable()') IS NOT NULL)")"
  if [ "$commerce" != "t" ]; then
    echo "REFUSED: neither the 0009 ledger row nor the Phase-2 commerce structures it creates are present" >&2
    echo "         (iam_v2.trg_purchase_quote_pin_equal / iam_v2.trg_offer_quote_immutable). This database" >&2
    echo "         is neither an upgrade-path install that has reached 0009 nor a factory-clean baseline." >&2
    exit 3
  fi
  echo "  no 0009 ledger row, no migration below 0010, and the Phase-2 commerce structures are present:"
  echo "  factory-clean baseline install; the commerce baseline came from the baseline dump"
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
  lkey="$(ledger_lock_key)"
  echo "  select $ver  file=$base  sha256=$sha  lock_key=$key  ledger_lock=$lkey  db=$EXPECT_DB  kind=$TARGET_KIND"
  out="$(
    { [ -n "$APPLY_ROLE" ] && printf "SET ROLE %s;\n" "$APPLY_ROLE"
      # The ledger lock FIRST, then the per-version lock -- the same order in both directions, because two
      # locks taken in two orders is a deadlock waiting for the first concurrent run.
      printf "SET statement_timeout='60s';\nSELECT pg_advisory_lock(%s);\n" "$lkey"
      printf "SELECT pg_advisory_lock(%s);\nSET statement_timeout=0;\n" "$key"
      printf "SELECT (NOT EXISTS(SELECT 1 FROM public.schema_migrations WHERE version='%s')) AS need \\\\gset\n" "$ver"
      printf "\\\\if :need\n\\\\echo APPLYING_UNDER_LOCK\n"
      cat "$f"
      printf "\nINSERT INTO public.schema_migrations(version) VALUES ('%s') ON CONFLICT DO NOTHING;\n" "$ver"
      printf "\\\\else\n\\\\echo SKIP_AFTER_LOCK\n\\\\endif\n"
      printf "SELECT pg_advisory_unlock(%s);\nSELECT pg_advisory_unlock(%s);\n" "$key" "$lkey"
    } | $EDGE_PSQL -v ON_ERROR_STOP=1 2>&1
  )"
  rc=$?
  # ON_ERROR_STOP IS FORCED HERE AND NOT LEFT TO THE CALLER, because without it a FAILED migration is
  # recorded as APPLIED and can never be retried.
  #
  # The mechanism: the migration file carries its own BEGIN/COMMIT, so the ledger INSERT this runner appends
  # is necessarily OUTSIDE it. When a statement fails and psql keeps going, the transaction is aborted, the
  # migration's COMMIT degrades to ROLLBACK -- and then the appended INSERT runs in its own implicit
  # transaction and succeeds. The result is a ledger row asserting a migration applied, with none of its
  # objects present and no path that will ever try it again.
  #
  # With ON_ERROR_STOP psql exits at the first error, so the INSERT is never reached and $rc is non-zero.
  # A caller that passes its own ON_ERROR_STOP is unaffected: this flag comes last and psql takes the last.
  if echo "$out" | grep -q "APPLYING_UNDER_LOCK"; then
    # THE ECHO IS NOT THE PROOF, AND TREATING IT AS ONE REPORTED A FAILED MIGRATION AS APPLIED.
    #
    # APPLYING_UNDER_LOCK is printed BEFORE the migration body runs. Under ON_ERROR_STOP a failing
    # migration aborts and its own BEGIN/COMMIT rolls the whole thing back -- including the ledger INSERT --
    # but the echo has already been emitted, so this branch was taken and the runner printed
    # EDGE_MIGRATE_OK applied=1 over a database it had not changed.
    #
    # Observed, not theorised: 0069 reported applied=1 against a live appliance while the ledger row and
    # every object it creates were absent, because the applying role could not create in the target schema.
    # A deployment that trusted the runner would have carried on to restart daemons against a schema that
    # was never migrated.
    #
    # Two independent proofs, because they fail in different directions: psql's exit status catches the
    # migration that errored, and the ledger row catches the apply that produced no record of itself.
    if [ "$rc" != "0" ]; then
      echo "RUNNER ERROR: $ver FAILED (psql exit $rc) -- nothing was applied" >&2
      echo "$out" | grep -iE "^(ERROR|FATAL|psql:)" | head -5 >&2
      exit 4
    fi
    if [ "$(q "SELECT count(*) FROM public.schema_migrations WHERE version='$ver'")" != "1" ]; then
      echo "RUNNER ERROR: $ver was attempted but left NO ledger row -- the apply FAILED and rolled back" >&2
      echo "$out" | grep -iE "^(ERROR|FATAL|psql:)" | head -5 >&2
      exit 4
    fi
    echo "  apply $ver (under lock)"; return 10
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
  # strict PostgreSQL role-name policy: reject anything but a plain identifier (no whitespace/quotes/;/SQL)
  echo "$BOOTSTRAP_OWNER" | grep -Eq '^[a-z_][a-z0-9_]{0,62}$' \
    || { echo "REFUSED: bootstrap owner '$BOOTSTRAP_OWNER' is not a valid role identifier" >&2; exit 3; }
  if [ "$TARGET_KIND" = "live-site" ]; then
    [ "$EXPECT_DB" = "stayconnect_site" ] || { echo "REFUSED: live-site bootstrap requires --expect-db stayconnect_site" >&2; exit 3; }
    # live owner comes from a FIXED approved set baked into the runner, never an env-expandable allowlist
    case "$BOOTSTRAP_OWNER" in iam_v2_owner) : ;; *) echo "REFUSED: live-site bootstrap owner must be a fixed approved role (iam_v2_owner)" >&2; exit 3;; esac
  else
    case " $LEDGER_OWNER_ALLOWLIST " in *" $BOOTSTRAP_OWNER "*) : ;; *) echo "REFUSED: bootstrap owner '$BOOTSTRAP_OWNER' not in allowlist" >&2; exit 3;; esac
  fi
  curdb="$(q 'SELECT current_database()')"
  [ "$curdb" = "$EXPECT_DB" ] || { echo "REFUSED: connected to '$curdb' but --expect-db '$EXPECT_DB'" >&2; exit 3; }
  # the role must actually exist (validated identifier → safe to interpolate)
  [ "$(q "SELECT count(*) FROM pg_roles WHERE rolname='$BOOTSTRAP_OWNER'")" = 1 ] \
    || { echo "REFUSED: bootstrap owner role '$BOOTSTRAP_OWNER' does not exist" >&2; exit 3; }
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

# ---- DOWN MODE -------------------------------------------------------------------------------------------
if [ "$DOWN" -eq 1 ]; then
  # ONE MIGRATION, NEVER A SWEEP. A down sweep is a schema deletion with a loop around it.
  [ "$ALL" -ne 1 ] || { echo "REFUSED: --all cannot be combined with --down; roll back one migration at a time" >&2; exit 3; }
  [ -n "$ONLY" ] || { echo "REFUSED: --down requires --only <version>" >&2; exit 3; }
  want_down="$(ack_down_for_kind "$TARGET_KIND")"
  [ -n "$want_down" ] || { echo "REFUSED: --target-kind must be disposable or live-site" >&2; exit 3; }
  if [ "$ACK_DOWN" != "$want_down" ]; then
    echo "REFUSED: --down on a $TARGET_KIND target requires --ack-down $want_down" >&2
    echo "         --ack-target says WHICH database you mean. --ack-down says you mean to REMOVE schema" >&2
    echo "         from it. They are separate on purpose: an operator who has typed the apply" >&2
    echo "         acknowledgement a hundred times must not be one flag away from dropping a schema." >&2
    exit 3
  fi
  MIG_DIR="$(resolve_mig_dir)"
  verify_ledger_structural_down
  f="$(select_down_file "$MIG_DIR")"
  echo "== DOWN MIGRATION: this REMOVES schema. What follows is the exact file that will run =="
  sed -n '1,200p' "$f" | grep -vE '^\s*--' | grep -vE '^\s*$' | sed 's/^/    /'
  echo "== end of down file =="
  reverted=0; skipped=0
  set +e; revert_one "$f"; rc=$?; set -e
  [ "$rc" = 10 ] && reverted=$((reverted+1)); [ "$rc" = 11 ] && skipped=$((skipped+1))
  { [ "$rc" = 10 ] || [ "$rc" = 11 ]; } || exit "$rc"
  echo "EDGE_MIGRATE_DOWN_OK reverted=$reverted skipped=$skipped"
  exit 0
fi

[ -z "$ACK_DOWN" ] || { echo "REFUSED: --ack-down is only meaningful with --down" >&2; exit 2; }
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
