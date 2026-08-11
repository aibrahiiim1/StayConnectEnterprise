#!/usr/bin/env bash
# Disposable tests for scripts/binary-rollback.sh.
#
# The defect under test is not "does a rollback work" — it is "can the tool report success when it did not do
# what it claimed". Every case below is a way Live Increment 9's rehearsal reported PASS while the running
# services were untouched.
set -uo pipefail
HERE="$(cd "$(dirname "$0")/../.." && pwd -P)"
TOOL="$HERE/scripts/binary-rollback.sh"
pass=0; fail=0
ok()   { pass=$((pass+1)); printf '  PASS  %s\n' "$1"; }
bad()  { fail=$((fail+1)); printf '  FAIL  %s\n' "$1"; }

# A disposable world: a bin dir, a .bak of each binary, and a "running process" modelled by a file that only
# the restart stub updates.
newworld() {
  W="$(mktemp -d)"
  mkdir -p "$W/bin" "$W/stage" "$W/run"
  printf 'CURRENT-BUILD' > "$W/bin/scd"
  printf 'OLD-BUILD'     > "$W/bin/scd.bak-inc9"
  printf 'CURRENT-BUILD' > "$W/stage/scd"
  cp "$W/bin/scd" "$W/run/scd.exe"   # the running process is backed by the current build
  export SC_ROLLBACK_RESTART_CMD="$W/restart.sh"
  export SC_ROLLBACK_RUNNING_EXE="$W/runexe.sh"
  export SC_ROLLBACK_UNIT_STATE="$W/state.sh"
  unset SC_ROLLBACK_INSTALL_CMD
  # a faithful restart: the process picks up whatever is on disk now
  cat > "$W/restart.sh" <<EOF
#!/usr/bin/env bash
cp "$W/bin/scd" "$W/run/scd.exe"
EOF
  # a restart that does NOT pick up the new file — models a unit that failed to actually swap
  cat > "$W/restart-noop.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  cat > "$W/runexe.sh" <<EOF
#!/usr/bin/env bash
echo "$W/run/scd.exe"
EOF
  cat > "$W/state.sh" <<'EOF'
#!/usr/bin/env bash
echo active
EOF
  cat > "$W/install-busy.sh" <<'EOF'
#!/usr/bin/env bash
echo "cp: cannot create regular file: Text file busy" >&2
exit 1
EOF
  chmod +x "$W"/*.sh
}

run_tool() { bash "$TOOL" --bin-dir "$W/bin" --unit scd=stayconnect-scd "$@" 2>&1; }

echo "== 1. happy path: rollback to the .bak build is verified end to end =="
newworld
out="$(run_tool --source-suffix .bak-inc9)"; rc=$?
if [ $rc -eq 0 ] && echo "$out" | grep -q "BINARY_ROLLBACK = PASS"; then ok "verified rollback exits 0"; else bad "verified rollback did not pass: $out"; fi
if [ "$(cat "$W/bin/scd")" = "OLD-BUILD" ]; then ok "the on-disk binary really was replaced"; else bad "the binary was not replaced"; fi
if [ "$(cat "$W/run/scd.exe")" = "OLD-BUILD" ]; then ok "the running process is backed by the restored build"; else bad "the running process was not restarted onto the restored build"; fi
rm -rf "$W"

echo "== 2. THE INCREMENT-9 DEFECT: a failed replacement must be fatal, immediately =="
newworld
export SC_ROLLBACK_INSTALL_CMD="$W/install-busy.sh"
out="$(run_tool --source-suffix .bak-inc9)"; rc=$?
if [ $rc -ne 0 ]; then ok "a 'Text file busy' replacement failure exits non-zero"; else bad "a failed replacement was reported as success"; fi
if echo "$out" | grep -qi "UNKNOWN state"; then ok "the failure names the resulting state plainly"; else bad "the failure message is not explicit: $out"; fi
if ! echo "$out" | grep -q "== restart =="; then ok "no service was restarted after the failed replacement"; else bad "it restarted services after failing to replace them"; fi
if echo "$out" | grep -q "BINARY_ROLLBACK = PASS"; then bad "it printed a PASS verdict after a failed replacement"; else ok "no PASS verdict was printed"; fi
unset SC_ROLLBACK_INSTALL_CMD
rm -rf "$W"

echo "== 3. THE FALSE-PASS ITSELF: healthy services running the WRONG binary must fail =="
newworld
export SC_ROLLBACK_RESTART_CMD="$W/restart-noop.sh"   # unit stays active, still running the old process image
out="$(run_tool --source-suffix .bak-inc9)"; rc=$?
if [ $rc -ne 0 ]; then ok "a service that is active but running the wrong binary fails verification"; else bad "healthy-but-wrong was reported as a successful rollback"; fi
if echo "$out" | grep -q "state=active"; then ok "the report shows the unit was active — health alone did not carry it"; else bad "unit state was not reported"; fi
if echo "$out" | grep -q "running   "; then ok "the running-process identity is reported explicitly"; else bad "running identity not reported: $out"; fi
rm -rf "$W"

echo "== 4. an unidentifiable running process is a FAILURE, never a pass =="
newworld
cat > "$W/runexe.sh" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "$W/runexe.sh"
out="$(run_tool --source-suffix .bak-inc9)"; rc=$?
if [ $rc -ne 0 ]; then ok "an unreadable running executable fails"; else bad "an unidentifiable process passed"; fi
if echo "$out" | grep -q "this is a FAILURE, not a pass"; then ok "the report says why"; else bad "no explanation given"; fi
rm -rf "$W"

echo "== 5. a missing source binary is refused before anything is touched =="
newworld
rm -f "$W/bin/scd.bak-inc9"
before="$(cat "$W/bin/scd")"
out="$(run_tool --source-suffix .bak-inc9)"; rc=$?
if [ $rc -ne 0 ]; then ok "a missing rollback source is fatal"; else bad "a missing source was tolerated"; fi
if [ "$(cat "$W/bin/scd")" = "$before" ]; then ok "nothing was modified"; else bad "the target was modified despite a missing source"; fi
rm -rf "$W"

echo "== 6. roll-forward from a staging dir is verified the same way =="
newworld
printf 'OLD-BUILD' > "$W/bin/scd"; cp "$W/bin/scd" "$W/run/scd.exe"
out="$(run_tool --source-dir "$W/stage")"; rc=$?
if [ $rc -eq 0 ] && [ "$(cat "$W/run/scd.exe")" = "CURRENT-BUILD" ]; then ok "roll-forward verified in the running process"; else bad "roll-forward not verified: $out"; fi
rm -rf "$W"

echo "== 7. THE PRE-nftconverge COMPATIBILITY BOUNDARY =="
#
# A netd built before ADR-0003 re-asserts the stored bundle, which begins with `delete table` — starting one
# recreates the authorization sets EMPTY. Harmless with nobody authorized; a property-wide outage otherwise.
# The tool must tell those two apart by reading the LIVE set, and must never offer a way past itself.
newnetd() {
  W="$(mktemp -d)"
  mkdir -p "$W/bin" "$W/stage" "$W/run"
  # the "previous release" netd PREDATES convergence: no render marker anywhere in the binary
  printf 'OLD-NETD-no-marker-here' > "$W/bin/netd.bak"
  # the currently deployed netd IS convergence-capable
  printf 'NEW-NETD netd-render-fp= aware' > "$W/bin/netd"
  printf 'NEW-NETD netd-render-fp= aware' > "$W/stage/netd"
  cp "$W/bin/netd" "$W/run/netd.exe"
  cat > "$W/restart.sh" <<EOF
#!/usr/bin/env bash
cp "$W/bin/netd" "$W/run/netd.exe"
EOF
  cat > "$W/runexe.sh" <<EOF
#!/usr/bin/env bash
echo "$W/run/netd.exe"
EOF
  cat > "$W/state.sh" <<'EOF'
#!/usr/bin/env bash
echo active
EOF
  chmod +x "$W"/*.sh
  export SC_ROLLBACK_RESTART_CMD="$W/restart.sh" SC_ROLLBACK_RUNNING_EXE="$W/runexe.sh" SC_ROLLBACK_UNIT_STATE="$W/state.sh"
  unset SC_ROLLBACK_INSTALL_CMD
}
# a stub nft: $1 controls what the live legacy set looks like
stub_nft() {
  cat > "$W/nft.sh" <<EOF
#!/usr/bin/env bash
case "\$*" in
  *"list set inet stayconnect auth_ipv4"*) $1 ;;
  *) exit 0 ;;
esac
EOF
  chmod +x "$W/nft.sh"
  export SC_ROLLBACK_NFT="$W/nft.sh"
}
POP='echo "{\"nftables\":[{\"set\":{\"name\":\"auth_ipv4\",\"elem\":[{\"elem\":{\"val\":{\"concat\":[\"br-g90\",\"10.20.0.5\"]}}},{\"elem\":{\"val\":{\"concat\":[\"br-g90\",\"10.20.0.6\"]}}}]}}]}"'
EMPTY='echo "{\"nftables\":[{\"set\":{\"name\":\"auth_ipv4\"}}]}"'
UNREADABLE='exit 1'

newnetd; stub_nft "$POP"
before="$(cat "$W/bin/netd")"
out="$(bash "$TOOL" --bin-dir "$W/bin" --unit netd=stayconnect-netd --source-suffix .bak 2>&1)"; rc=$?
if [ $rc -ne 0 ]; then ok "a pre-convergence rollback with LIVE authorization is refused"; else bad "it rolled back into a property-wide deauthorization: $out"; fi
if [ "$(cat "$W/bin/netd")" = "$before" ]; then ok "nothing was replaced"; else bad "the binary was replaced despite the refusal"; fi
if ! echo "$out" | grep -q "== restart =="; then ok "no service was restarted"; else bad "it restarted services"; fi
if echo "$out" | grep -q "NOTHING HAS BEEN CHANGED"; then ok "the refusal says plainly that nothing changed"; else bad "refusal is not explicit: $out"; fi
if echo "$out" | grep -qi "Operator action"; then ok "the refusal names the operator action instead of guessing"; else bad "no operator action given"; fi
if echo "$out" | grep -q "2 legacy authorization"; then ok "the refusal counts the guests at risk"; else bad "guest count not reported"; fi
rm -rf "$W"

newnetd; stub_nft "$EMPTY"
out="$(bash "$TOOL" --bin-dir "$W/bin" --unit netd=stayconnect-netd --source-suffix .bak 2>&1)"; rc=$?
if [ $rc -eq 0 ] && [ "$(cat "$W/run/netd.exe")" = "OLD-NETD-no-marker-here" ]; then ok "with an EMPTY legacy set the same rollback is allowed and verified"; else bad "empty-set rollback was blocked: $out"; fi
if echo "$out" | grep -q "EMPTY (0 elements)"; then ok "it states why it was allowed"; else bad "no reason given"; fi
rm -rf "$W"

newnetd; stub_nft "$UNREADABLE"
before="$(cat "$W/bin/netd")"
out="$(bash "$TOOL" --bin-dir "$W/bin" --unit netd=stayconnect-netd --source-suffix .bak 2>&1)"; rc=$?
if [ $rc -ne 0 ]; then ok "an UNREADABLE live set is refused — 'cannot prove empty' is not 'is empty'"; else bad "it proceeded without being able to read the live set"; fi
if [ "$(cat "$W/bin/netd")" = "$before" ]; then ok "nothing was replaced"; else bad "the binary was replaced"; fi
rm -rf "$W"

newnetd; stub_nft "$POP"
out="$(bash "$TOOL" --bin-dir "$W/bin" --unit netd=stayconnect-netd --source-dir "$W/stage" 2>&1)"; rc=$?
if [ $rc -eq 0 ]; then ok "a CONVERGENCE-CAPABLE target is unaffected by the boundary, even with guests online"; else bad "a safe target was blocked: $out"; fi
if echo "$out" | grep -q "no compatibility boundary applies"; then ok "it says why the boundary did not apply"; else bad "no explanation"; fi
rm -rf "$W"

echo "== 7b. there is no generic force path hidden in the ordinary command =="
if grep -qiE -- '--force|SC_ROLLBACK_FORCE|FORCE=1|--yes-i-know' "$TOOL"; then
  bad "the rollback tool exposes an override switch"
else
  ok "no override flag exists on the ordinary rollback command"
fi

echo "== 8. the tool never uses cp to replace a binary =="
if grep -nE '^\s*cp\s' "$TOOL" | grep -v '^\s*#' | grep -q .; then
  bad "binary-rollback.sh contains a cp-based replacement"
else
  ok "replacement is install(1) only"
fi

echo
printf 'BINARY_ROLLBACK_TESTS: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
