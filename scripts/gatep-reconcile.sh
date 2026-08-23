#!/usr/bin/env bash
# THE ONLY SUPPORTED WAY TO RUN A GATE-P PRIVILEGE RECONCILE.
#
# gatep-grants.sql revokes every iam_v2 privilege from the runtime roles and then re-grants the allowlist from
# its per-service includes. Two properties have to hold for that to be safe to run against Production, and
# neither is a property of the SQL alone:
#
#   ATOMICITY — the file now wraps itself in BEGIN/COMMIT, so a failure anywhere rolls the revoke back with
#               everything else. This script's job is to not defeat that: ON_ERROR_STOP is set, and psql is
#               given the file directly rather than a pipeline that could swallow an error.
#
#   SOURCE IDENTITY — the revoke and the re-grants must come from the SAME revision. They are separate files
#               joined by \ir at runtime, so nothing in the SQL can tell that its includes are stale.
#
# ---------------------------------------------------------------------------------------------------------
# WHY THE STAGING CHECK EXISTS. On PRE-LIVE, `docker cp` was used to stage the Gate-P directory into a
# container path that already existed, so the copy landed one level deeper and the reconcile ran against an
# older revision of the per-service files that happened to still be sitting at the target path. The revoke was
# current; the re-grant was not. svc_scd lost EXECUTE on the PMS authentication path, and svc_acctd lost the
# Phase-6 expiry sweep, until the current files were reapplied by hand.
#
# No amount of care at the keyboard prevents that class of mistake, so it is checked mechanically: every file
# the reconcile will read is hashed at the source and re-hashed after staging, and any mismatch, absence,
# surplus or nesting aborts BEFORE the transaction opens. The failure mode this closes is specifically
# "current revoke script combined with stale re-grant files", which cannot be detected once psql has started.
#
# Usage:  gatep-reconcile.sh --container <name> --db <db> [--user <role>] [--expect-commit <sha>] [--dry-run]
#         gatep-reconcile.sh --dsn <dsn> [...]
#
# EXIT CODES: 0 reconciled (or dry run passed) · 1 refused before any database change · 2 the reconcile itself
# failed and was rolled back · 3 usage error.

set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GP="$ROOT/deploy/gatep"

CONTAINER=""; DB=""; PGUSER_ROLE="postgres"; EXPECT_COMMIT=""; DRY_RUN=0; DSN=""
while [ $# -gt 0 ]; do
  case "$1" in
    --container)      CONTAINER="${2:?}"; shift 2;;
    --db)             DB="${2:?}"; shift 2;;
    --user)           PGUSER_ROLE="${2:?}"; shift 2;;
    --dsn)            DSN="${2:?}"; shift 2;;
    --expect-commit)  EXPECT_COMMIT="${2:?}"; shift 2;;
    --dry-run)        DRY_RUN=1; shift;;
    *) echo "usage: $0 --container <name> --db <db> [--user <role>] [--expect-commit <sha>] [--dry-run]"; exit 3;;
  esac
done
if [ -z "$DSN" ] && { [ -z "$CONTAINER" ] || [ -z "$DB" ]; }; then
  echo "usage: $0 --container <name> --db <db> [--user <role>] [--expect-commit <sha>] [--dry-run]"; exit 3
fi

refuse(){ echo "REFUSED (no database change): $*" >&2; exit 1; }

# ---------------------------------------------------------------------------------------------------------
# 1. The exact file set the reconcile will read: the entry point plus every file it \ir-includes.
#
# Parsed from the SQL rather than listed here, so a new include is covered the moment it is added. A list
# maintained by hand would drift and would be trusted precisely when it was wrong.
# ---------------------------------------------------------------------------------------------------------
ENTRY="gatep-grants.sql"
[ -f "$GP/$ENTRY" ] || refuse "$GP/$ENTRY is missing from the source tree"

mapfile -t INCLUDES < <(grep -E '^\\ir[[:space:]]+' "$GP/$ENTRY" | awk '{print $2}')
[ "${#INCLUDES[@]}" -gt 0 ] || refuse "$ENTRY declares no \\ir includes; the parser or the file changed shape"

FILES=("$ENTRY" "${INCLUDES[@]}")
for f in "${FILES[@]}"; do
  [ -f "$GP/$f" ] || refuse "$ENTRY includes '$f', which does not exist in the source tree"
done
echo "== source set: ${#FILES[@]} files (entry + ${#INCLUDES[@]} includes) =="

# ---------------------------------------------------------------------------------------------------------
# 2. Repository identity. An operator running this from a stale checkout is the same failure as a stale copy,
#    one step earlier.
# ---------------------------------------------------------------------------------------------------------
if command -v git >/dev/null 2>&1 && git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1; then
  HEAD_SHA="$(git -C "$ROOT" rev-parse HEAD)"
  echo "   repository HEAD: $HEAD_SHA"
  if [ -n "$EXPECT_COMMIT" ] && [ "$HEAD_SHA" != "$EXPECT_COMMIT" ]; then
    refuse "HEAD is $HEAD_SHA but --expect-commit asked for $EXPECT_COMMIT"
  fi
  if ! git -C "$ROOT" diff --quiet -- deploy/gatep || ! git -C "$ROOT" diff --cached --quiet -- deploy/gatep; then
    refuse "deploy/gatep has uncommitted changes; reconcile only from a committed revision"
  fi
elif [ -n "$EXPECT_COMMIT" ]; then
  refuse "--expect-commit was given but this is not a git checkout"
fi

# ---------------------------------------------------------------------------------------------------------
# 3. Stage into a FRESH EMPTY directory and prove what landed is byte-identical to the source.
# ---------------------------------------------------------------------------------------------------------
STAGE="$(mktemp -d "${TMPDIR:-/tmp}/gatep-stage-XXXXXX")" || refuse "could not create a staging directory"
cleanup(){ rm -rf "$STAGE"; }
trap cleanup EXIT
[ -z "$(ls -A "$STAGE" 2>/dev/null)" ] || refuse "staging directory $STAGE is not empty"

for f in "${FILES[@]}"; do cp "$GP/$f" "$STAGE/$f" || refuse "could not stage $f"; done

# Byte-for-byte, per file. This is the check that would have caught the nested copy.
for f in "${FILES[@]}"; do
  want="$(sha256sum "$GP/$f" | awk '{print $1}')"
  got="$(sha256sum "$STAGE/$f" 2>/dev/null | awk '{print $1}')"
  [ -n "$got" ]          || refuse "$f did not stage"
  [ "$want" = "$got" ]   || refuse "$f differs after staging (source $want, staged $got)"
done

# Nothing extra, and nothing nested. A surplus file is how a stale revision survives a re-stage, and a
# subdirectory is exactly the shape the docker cp mistake produced.
staged_count="$(find "$STAGE" -maxdepth 1 -type f | wc -l | tr -d ' ')"
[ "$staged_count" -eq "${#FILES[@]}" ] || refuse "staging holds $staged_count files, expected ${#FILES[@]}"
nested="$(find "$STAGE" -mindepth 1 -type d | wc -l | tr -d ' ')"
[ "$nested" -eq 0 ] || refuse "staging contains $nested subdirector(y/ies); a nested copy is how stale files get read"
echo "   staged and hash-verified: ${#FILES[@]}/${#FILES[@]} files, no surplus, no nesting"

if [ "$DRY_RUN" = 1 ]; then
  echo "DRY RUN: source verified, no database contacted."
  exit 0
fi

# ---------------------------------------------------------------------------------------------------------
# 4. Apply. One file, one transaction, ON_ERROR_STOP. psql is handed the staged entry point by path so its
#    \ir includes resolve to the staged copies that were just verified — never to whatever else is on disk.
# ---------------------------------------------------------------------------------------------------------
echo "== reconcile =="
if [ -n "$DSN" ]; then
  psql "$DSN" -v ON_ERROR_STOP=1 -q -f "$STAGE/$ENTRY"; rc=$?
else
  REMOTE="/tmp/$(basename "$STAGE")"
  # Every in-container path is addressed with a doubled leading slash in docker exec arguments. Git Bash
  # rewrites a lone leading "/" into a Windows path, so the verification below would look somewhere that does
  # not exist and report the files absent. Linux collapses "//" to "/", so the identical string is correct on
  # the CI runner. The docker cp destination is not affected: it starts with the container name.
  REMOTE_X="/$REMOTE"
  docker exec "$CONTAINER" rm -rf "$REMOTE_X" >/dev/null 2>&1
  docker cp "$STAGE" "$CONTAINER:$REMOTE" >/dev/null 2>&1 || refuse "could not copy the staged set into $CONTAINER"

  # Re-verify INSIDE the container. The copy is the step that went wrong before, so it is checked on the far
  # side rather than assumed: same hashes, same count, no nesting.
  for f in "${FILES[@]}"; do
    want="$(sha256sum "$STAGE/$f" | awk '{print $1}')"
    got="$(docker exec "$CONTAINER" sha256sum "$REMOTE_X/$f" 2>/dev/null | awk '{print $1}')"
    [ "$want" = "$got" ] || { docker exec "$CONTAINER" rm -rf "$REMOTE_X" >/dev/null 2>&1; refuse "$f differs inside the container (expected $want, found ${got:-absent})"; }
  done
  incount="$(docker exec "$CONTAINER" sh -c "find '$REMOTE' -maxdepth 1 -type f | wc -l" 2>/dev/null | tr -d ' ')"
  indirs="$(docker exec "$CONTAINER" sh -c "find '$REMOTE' -mindepth 1 -type d | wc -l" 2>/dev/null | tr -d ' ')"
  if [ "$incount" != "${#FILES[@]}" ] || [ "$indirs" != "0" ]; then
    docker exec "$CONTAINER" rm -rf "$REMOTE_X" >/dev/null 2>&1
    refuse "container staging holds $incount files and $indirs subdirectories, expected ${#FILES[@]} and 0"
  fi
  echo "   container copy hash-verified: ${#FILES[@]}/${#FILES[@]} files, no nesting"

  docker exec "$CONTAINER" psql -v ON_ERROR_STOP=1 -U "$PGUSER_ROLE" -d "$DB" -q -f "$REMOTE_X/$ENTRY"; rc=$?
  docker exec "$CONTAINER" rm -rf "$REMOTE_X" >/dev/null 2>&1
fi

if [ "$rc" -ne 0 ]; then
  echo "RECONCILE FAILED (rc=$rc) — the transaction rolled back; effective privileges are unchanged." >&2
  exit 2
fi
echo "GATEP_RECONCILE = OK"
