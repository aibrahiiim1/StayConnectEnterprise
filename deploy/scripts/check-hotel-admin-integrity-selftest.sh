#!/usr/bin/env bash
# A CHECK THAT CANNOT FAIL IS NOT A CHECK.
#
# check-hotel-admin-integrity.sh exists because of one live state: a Hotel Admin bundle built with no
# capability flags, installed by copying instead of by switching a release, with an obsolete release still
# wired as the rollback — and every conventional check reporting healthy. This proves the checker refuses each
# of those conditions, and for the right reason.
#
# It stages FAKE release trees. Nothing here touches a real appliance, a real service or a real port.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHECK="$HERE/check-hotel-admin-integrity.sh"
CONTRACT="$HERE/../../hotel-admin/capability-contract.json"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0

[ -f "$CONTRACT" ] || { echo "SKIP: no capability contract at $CONTRACT"; echo "HOTEL_ADMIN_INTEGRITY_SELFTEST = SKIP"; exit 0; }

# SYMLINKS ARE THE SUBJECT, NOT AN INCIDENTAL DETAIL. Every case here stages a release pointer, and the whole
# defect being guarded against is about which release a pointer resolves to. On a platform that cannot create
# a symlink (Windows without developer mode) not one case can be staged, so this SKIPS AND SAYS SO rather than
# reporting a PASS it never earned. The appliance and CI are Linux, which is where the result counts.
_probe="$WORK/.probe"; mkdir -p "$_probe/real"; ln -sfn "$_probe/real" "$_probe/link" 2>/dev/null
if [ ! -L "$_probe/link" ]; then
  echo "SKIP: this platform cannot create symlinks, so no release pointer can be staged"
  echo "HOTEL_ADMIN_INTEGRITY_SELFTEST = SKIP"
  exit 0
fi
rm -rf "$_probe"

# stage <name> <flags-inlined:yes|no> [manifest:yes|no] -> echoes the release dir
#
# A release is a directory with a BUILD_ID, a static chunk and a manifest. "flags not inlined" is reproduced
# exactly as Next leaves it: a runtime property access on an env shim, which is the fingerprint the checker
# looks for.
stage() {
  local name="$1" inlined="$2" manifest="${3:-yes}"
  local rel="$WORK/releases/$name"
  mkdir -p "$rel/.next/static/chunks/app"
  echo "BID-$name" > "$rel/.next/BUILD_ID"
  : > "$rel/server.js"
  cp "$CONTRACT" "$rel/capability-contract.json"
  if [ "$inlined" = "no" ]; then
    printf 'let B="1"===L.env.NEXT_PUBLIC_PHASE2_ADMIN,H="1"===L.env.NEXT_PUBLIC_PHASE3_ADMIN;\n' \
      > "$rel/.next/static/chunks/app/layout.js"
  else
    printf 'let B=!0,H=!0;/* flags inlined at build time */\n' > "$rel/.next/static/chunks/app/layout.js"
  fi
  if [ "$manifest" = "yes" ]; then
    cat > "$rel/hotel-admin-release.json" <<JSON
{
  "source_commit": "0123456789abcdef0123456789abcdef01234567",
  "source_state": "clean",
  "build_id": "BID-$name",
  "released_at": "2026-09-05T00:00:00Z",
  "contract_version": "1",
  "build_flags": { "NEXT_PUBLIC_PHASE2_ADMIN": "1", "NEXT_PUBLIC_PHASE3_ADMIN": "1" },
  "required_routes": ["/internet-packages", "/service-plans", "/pms-interfaces"]
}
JSON
  fi
  echo "$rel"
}

# run <description> <want-exit> <expected-message-fragment-or-empty>
#
# The service is not running in a self-test, so the HTTP checks legitimately fail. Those failures are EXPECTED
# and are not what is being asserted: each case asserts that the SPECIFIC structural violation is reported.
run() {
  local what="$1" want="$2" frag="$3" out rc
  out="$(HOTEL_ADMIN_CURRENT="$WORK/current" HOTEL_ADMIN_PREVIOUS="$WORK/previous" \
         HOTEL_ADMIN_RELEASES="$WORK/releases" HOTEL_ADMIN_PORT=1 \
         bash "$CHECK" 2>&1)"; rc=$?
  if [ "$want" = "fail" ] && [ "$rc" = "0" ]; then
    echo "  *** FAIL: $what — the checker reported healthy"; echo "$out" | sed 's/^/      /' | head -12; fail=1; return
  fi
  if [ -n "$frag" ] && ! grep -qF -- "$frag" <<<"$out"; then
    echo "  *** FAIL: $what — refused, but not for the stated reason"; echo "$out" | sed 's/^/      /' | head -12; fail=1; return
  fi
  echo "  ok: $what"
}

# 1. THE LIVE FAILURE, part one: a bundle built with no capability flags.
rm -f "$WORK/current" "$WORK/previous"
ln -sfn "$(stage flagless no)" "$WORK/current"
run "a live release built WITHOUT its capability flags is REFUSED" fail "was built WITHOUT that capability flag"

# 2. THE LIVE FAILURE, part two: the runtime path is a plain directory, so nothing can prove what is live.
rm -f "$WORK/current" "$WORK/previous"
mkdir -p "$WORK/current/.next/static/chunks/app"
echo "BID-unmanaged" > "$WORK/current/.next/BUILD_ID"
run "a plain directory at the runtime path is REFUSED" fail "PLAIN DIRECTORY"

# 3. THE LIVE FAILURE, part three: an obsolete release wired as the executable rollback.
rm -rf "$WORK/current" "$WORK/previous"
ln -sfn "$(stage good yes)" "$WORK/current"
ln -sfn "$(stage obsolete no)" "$WORK/previous"
run "an obsolete rollback target is REFUSED" fail "rolling back to it would remove the operator surfaces"

# 4. A release that cannot prove its provenance.
rm -f "$WORK/current" "$WORK/previous"
ln -sfn "$(stage anonymous yes no)" "$WORK/current"
run "a release with no deployment manifest is REFUSED" fail "cannot prove which commit built it"

# 5. THE OTHER HALF OF THE CONTRACT: a correct release must not be refused for a structural reason. The HTTP
#    checks still fail here because no service is running, so this asserts the STRUCTURAL findings are clean.
rm -f "$WORK/current" "$WORK/previous"
ln -sfn "$(stage current yes)" "$WORK/current"
out="$(HOTEL_ADMIN_CURRENT="$WORK/current" HOTEL_ADMIN_PREVIOUS="$WORK/previous" \
       HOTEL_ADMIN_RELEASES="$WORK/releases" HOTEL_ADMIN_PORT=1 bash "$CHECK" 2>&1)"
for must in "runtime path is a release symlink" "records its source commit" \
            "manifest BUILD_ID matches the release on disk" \
            "every capability flag was inlined at build time" \
            "no previous-release pointer is wired"; do
  if ! grep -qF -- "$must" <<<"$out"; then
    echo "  *** FAIL: a healthy release did not report '$must'"; echo "$out" | sed 's/^/      /' | head -14; fail=1
  fi
done
grep -qF "PLAIN DIRECTORY" <<<"$out" && { echo "  *** FAIL: a healthy release was called unmanaged"; fail=1; }
grep -qF "built WITHOUT" <<<"$out" && { echo "  *** FAIL: a healthy release was called flagless"; fail=1; }
[ "$fail" = "0" ] && echo "  ok: a correct release passes every structural check"

if [ "$fail" = "0" ]; then
  echo "HOTEL_ADMIN_INTEGRITY_SELFTEST = PASS"
  exit 0
fi
echo "HOTEL_ADMIN_INTEGRITY_SELFTEST = FAIL"
exit 1
