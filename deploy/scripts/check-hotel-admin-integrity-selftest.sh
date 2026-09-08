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
  # A rollback target is now held to the FULL contract, so a fixture needs the route manifest a real release
  # has. Without it every case would fail on a missing manifest rather than on the condition it is staging.
  SC_REL="$rel" SC_CONTRACT="$CONTRACT" python3 -c 'import json, os
c = json.load(open(os.environ["SC_CONTRACT"], encoding="utf-8"))
routes = c["required_routes"]["routes"] + [e["route"] for e in c["required_navigation"]["entries"]]
json.dump({r.rstrip("/") + "/page": r for r in routes},
          open(os.environ["SC_REL"] + "/.next/app-path-routes-manifest.json", "w", encoding="utf-8"))' 
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
run "an obsolete rollback target is REFUSED" fail "is NOT a legitimate rollback"

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

echo "== build identity is LOSSLESS: - and _ are different builds =="
# THE COLLISION THIS REPLACED. The identity check used to compare the BUILD_ID on disk against the one Next
# stamps into an HTML comment, where a hyphen is written as an underscore because a comment cannot contain
# "--". Normalising the disk id to match made hyphenated builds pass -- and made "ABC-DEF" and "ABC_DEF",
# both ids Next can generate, indistinguishable. That is a false POSITIVE: a deployment verified against a
# different build.
#
# The identity now comes from the running server's own asset path, which carries the id exactly. These drive
# it with a stubbed fetcher so the properties are asserted without a server.
# shellcheck source=/dev/null
. "$HERE/lib-hotel-admin-contract.sh"

# A stub server that is running exactly one build: it answers 200 for that build's path and 404 for anything
# else. Written to a file because the helper invokes it as a command.
STUB="$WORK/stub-status"; mkdir -p "$WORK"
cat > "$STUB" <<'STUBEOF'
#!/usr/bin/env bash
# $1 is the URL; $SERVING is the one build this stub is running.
case "$1" in
  */_next/static/"$SERVING"/_buildManifest.js) echo 200 ;;
  *) echo 404 ;;
esac
STUBEOF
chmod +x "$STUB"

run_probe() { SERVING="$1" HA_FETCH_STATUS="$STUB" ha_serves_build_id "https://x" "$2"; }

# 1. a correct HYPHENATED build passes -- the case the old check rejected
if run_probe "CiLetqcFQ-UcO0nBzrxeZ" "CiLetqcFQ-UcO0nBzrxeZ"; then
  echo "  ok: a hyphenated BUILD_ID verifies against the build actually running"
else
  echo "  *** FAIL: a correct hyphenated BUILD_ID was rejected"; fail=1
fi

# 2. "-" and "_" are DIFFERENT builds -- the collision the old check introduced
if run_probe "ABC_DEF-aaaaaaaaaaaaaa" "ABC-DEF-aaaaaaaaaaaaaa"; then
  echo "  *** FAIL: an underscore id verified against a hyphen id — the lossy collision is back"; fail=1
else
  echo "  ok: '-' and '_' ids are distinct builds"
fi

# 3. a genuinely different build fails
if run_probe "aap1R2iCuFf2dubGXuDC5" "X05a_KJQAOuyeVoNK1VnA"; then
  echo "  *** FAIL: a different build was accepted"; fail=1
else
  echo "  ok: a different build is refused"
fi

# 4. NO prefix or substring matching: neither direction may pass
if run_probe "X05a_KJQAOuyeVoNK1VnA" "X05a"; then
  echo "  *** FAIL: a PREFIX of the running build id was accepted"; fail=1
elif run_probe "X05a" "X05a_KJQAOuyeVoNK1VnA"; then
  echo "  *** FAIL: a build id containing the running one as a prefix was accepted"; fail=1
else
  echo "  ok: no prefix or substring comparison is used"
fi

# 5. an empty id can never verify
if run_probe "X05a_KJQAOuyeVoNK1VnA" ""; then
  echo "  *** FAIL: an empty BUILD_ID verified"; fail=1
else
  echo "  ok: an empty BUILD_ID is refused"
fi

if [ "$fail" = "0" ]; then
  echo "HOTEL_ADMIN_INTEGRITY_SELFTEST = PASS"
  exit 0
fi
echo "HOTEL_ADMIN_INTEGRITY_SELFTEST = FAIL"
exit 1
