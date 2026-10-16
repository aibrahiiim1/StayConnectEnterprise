#!/bin/bash
# 2. ENFORCEMENT WITH REAL TRAFFIC (audit T20, B-1, D-C). Run as root on the appliance. Isolated fixture plans and
# free packages, guest-network namespace clients, fixture Personal accounts; nothing a real guest holds is touched.
#   a. publish refusals: ADMIN_APPROVAL plan, package speed steps
#   b. real-session flow teardown: a >=60 s TCP download + ping, operator Disconnect -> bytes stop within 2 s
#   c. data quota 3 MB with a real download (T20): terminated, off the network, the running transfer stops
#   d. duration window 90 s -> TERMINATED, offline
#   e. time allowance 90 s of online time on VALIDITY_WINDOW (window 1 h) -> TERMINATED (TIME), offline
#   f. idle timeout 60 s / maximum session 120 s -> the SESSION ends, the device signs in again
#   g. DISCONNECT_OLDEST with 2 devices: a third is admitted, the oldest is disconnected
. "$(dirname "$0")/lib.sh"
HUGE=${HUGE:-https://proof.ovh.net/files/1Gb.dat}; BIG=http://212.183.159.230/5MB.zip
plan(){ # plan CODE NAME EXTRAJSON
  mkplan "{\"code\":\"$1-$R4\",\"name\":\"$2\",\"down_kbps\":20000,\"up_kbps\":10000,\"max_concurrent_devices\":${MAXD:-1},\"device_limit_policy\":\"${DLP:-REJECT_NEW_DEVICE}\",\"time_accounting_mode\":\"VALIDITY_WINDOW\",\"speed_allocation\":\"PER_DEVICE\",\"create_only\":true${3:+,$3}}"; }
DEFTIERS='[{"order":1,"grant":{}}]'
pkg(){ # pkg CODE PLANREV NAME DURATION [TIERS]
  mkpkg "{\"code\":\"$1-$R4\",\"service_plan_revision_id\":\"$2\",\"display\":{\"name\":\"$3\"},\"duration_policy\":{\"end_mode\":\"VALIDITY_WINDOW\",\"duration_seconds\":$4},\"eligibility_rules\":[],\"grant_tiers\":${5:-$DEFTIERS},\"create_only\":true}"; }
newacc(){ local a; a=$(mkacc "$1"); ACCS+=("$a"); echo "$a"; }
# bytes_after FILE T : bytes the file grew between T+2 s and T+5 s (T = epoch seconds of the stop)
growth(){ local f=$1 s2 s5; sleep 2; s2=$(stat -c %s "$f" 2>/dev/null || echo 0); sleep 3; s5=$(stat -c %s "$f" 2>/dev/null || echo 0); echo $((s5-s2)); }
want(){ [ -z "${ONLY:-}" ] || [[ "$ONLY" == *"$1"* ]]; }
late_pings(){ awk -v t="$2" -F'[][]' '/bytes from/ {if ($2+0 > t+2) n++} END{print n+0}' "$1"; }

if want a; then
echo "== a. publish refusals"
r=$(DLP=ADMIN_APPROVAL MAXD=1 plan acc-aa "Acc admin approval")
case "$r" in ERR*) ok "ADMIN_APPROVAL plan refused at publish: ${r:0:160}";; *) bad "ADMIN_APPROVAL plan was published (rev ${r:0:8})";; esac
BASE=$(MAXD=3 plan acc-base "Acc base")
case "$BASE" in ERR*) bad "base plan: $BASE"; exit 1;; esac
r=$(pkg acc-steps "$BASE" "Acc speed steps" 3600 '[{"order":10,"grant":{}},{"order":20,"grant":{"down_kbps":2000,"up_kbps":500}}]')
case "$r" in ERR*speed_steps_not_supported*) ok "package with speed steps refused: ${r:0:140}";; ERR*) info "speed steps refused differently: $r"; ok "speed steps refused";; *) bad "package with speed steps was saved (${r:0:8})";; esac
r=$(pkg acc-steps1 "$BASE" "Acc speed step" 3600 '[{"order":10,"grant":{"down_kbps":2000}}]')
case "$r" in ERR*) ok "a single step that sets speeds is refused too";; *) bad "single speed step saved (${r:0:8})";; esac

fi
if want b; then
echo "== b. real session: long download, operator Disconnect, bytes stop within 2 s"
PB=$(pkg acc-free-b "$BASE" "Acc base free" 3600); case "$PB" in ERR*) bad "base package: $PB"; exit 1;; esac
UB="acc-b-$R4"; AB=$(newacc "$UB"); CB=b$R4; client $CB
r=$(signin $CB "$UB"); r=$(acquire $CB "$PB"); waiton $CB && ok "client online on the free package" || bad "not online: $r"
IPB=$(nsip $CB)
ip netns exec $CB ping -D -i 0.5 1.1.1.1 > /tmp/$CB.ping 2>&1 & PP=$!
ip netns exec $CB curl -s -m 600 -o /tmp/$CB.dl "$HUGE" & DP=$!
sleep 62; s=$(stat -c %s /tmp/$CB.dl 2>/dev/null || echo 0)
kill -0 $DP 2>/dev/null && [ "$s" -gt 10000000 ] && ok "after 62 s the download is still running ($((s/1048576)) MiB received)" || bad "download not running at 62 s ($s bytes)"
SID=$($PSQL "SELECT id FROM iam_v2.sessions WHERE host(ip)='$IPB' AND ended IS NULL ORDER BY started DESC LIMIT 1")
T=$(date +%s.%N); c=$(code POST "/sessions/$SID/disconnect" '{"reason":"acceptance: flow teardown"}'); info "disconnect session ${SID:0:8} -> $c $(last 100)"
g=$(growth /tmp/$CB.dl); [ "$g" = 0 ] && ok "0 bytes received between 2 s and 5 s after Disconnect" || bad "$g bytes still arriving 2-5 s after Disconnect (B-1 not fixed on a real session)"
sleep 1; lp=$(late_pings /tmp/$CB.ping "$T"); [ "$lp" = 0 ] && ok "no ping reply later than 2 s after Disconnect" || bad "$lp ping replies after Disconnect+2s"
[ "$(online $CB)" != 204 ] && ok "the device is off the network" || bad "device still online after Disconnect"
kill $DP $PP 2>/dev/null; rm -f /tmp/$CB.dl

fi
if want c; then
echo "== c. data quota 3 MB with a real download (T20)"
QREV=$(MAXD=2 plan acc-q "Acc 3MB" '"data_quota_bytes":3000000'); QP=$(pkg acc-free-q "$QREV" "Acc 3MB free" 3600)
UQ="acc-q-$R4"; AQ=$(newacc "$UQ"); CQ=q$R4; client $CQ
r=$(signin $CQ "$UQ"); r=$(acquire $CQ "$QP"); waiton $CQ && ok "client online on the 3 MB package" || bad "not online: $r"
ip netns exec $CQ ping -D -i 0.5 1.1.1.1 > /tmp/$CQ.ping 2>&1 & PP=$!
ip netns exec $CQ curl -s -m 300 -o /tmp/$CQ.dl "$HUGE" & DP=$!
for _ in $(seq 1 40); do E=$(ENT $AQ); case "$E" in *TERMINATED*) break;; esac; sleep 1; done
T=$(date +%s.%N); case "$E" in *TERMINATED*) ok "entitlement TERMINATED by the quota (${E#*|})";; *) bad "not terminated: $E";; esac
g=$(growth /tmp/$CQ.dl); [ "$g" = 0 ] && ok "the running transfer stopped: 0 bytes 2-5 s after termination (F-1 fixed)" || bad "$g bytes still arriving after termination"
sleep 1; lp=$(late_pings /tmp/$CQ.ping "$T"); [ "$lp" = 0 ] && ok "ping stopped after termination" || bad "$lp ping replies after termination"
waitoff $CQ 5 && ok "client is off the network" || bad "still online after quota"
cons=$(echo "$E" | cut -d'|' -f4); info "consumed_data_bytes=$cons (quota 3000000)"
kill $DP $PP 2>/dev/null; rm -f /tmp/$CQ.dl
r=$(acquire $CQ "$QP"); [ "$(online $CQ)" != 204 ] && info "re-acquire after exhaustion -> not online ($r)" || info "re-acquire granted a fresh package (no prior-purchase rule on the fixture)"

fi
if want e; then
echo "== d+e. duration window 90 s and online-time allowance 90 s (run together)"
DREV=$(plan acc-d "Acc duration"); DP90=$(pkg acc-free-d "$DREV" "Acc 90s window" 90)
TREV=$(plan acc-tq "Acc time allowance" '"time_quota_seconds":90'); case "$TREV" in ERR*) bad "time allowance plan refused: $TREV";; esac
TPK=$(pkg acc-free-tq "$TREV" "Acc 90s online" 3600)
UD="acc-d-$R4"; AD=$(newacc "$UD"); CD=d$R4; client $CD
UT="acc-t-$R4"; AT=$(newacc "$UT"); CT=t$R4; client $CT
signin $CD "$UD" >/dev/null; acquire $CD "$DP90" >/dev/null; signin $CT "$UT" >/dev/null; acquire $CT "$TPK" >/dev/null; T0=$(date +%s)
waiton $CD && waiton $CT && ok "both clients online" || bad "not both online (d=$(online $CD) t=$(online $CT))"
ip netns exec $CT ping -i 2 1.1.1.1 >/dev/null 2>&1 & TPP=$!
sleep 55; [ "$(online $CD)" = 204 ] && [ "$(online $CT)" = 204 ] && ok "both still online at ~60 s" || bad "offline early (d=$(online $CD) t=$(online $CT))"
offd=""; offt=""
for _ in $(seq 1 40); do now=$(($(date +%s)-T0)); [ -z "$offd" ] && [ "$(online $CD)" != 204 ] && offd=$now; [ -z "$offt" ] && [ "$(online $CT)" != 204 ] && offt=$now; [ -n "$offd" ] && [ -n "$offt" ] && break; sleep 3; done
kill $TPP 2>/dev/null
[ -n "$offd" ] && ok "window expiry: offline at ~${offd}s (window 90 s)" || bad "duration client still online at $(($(date +%s)-T0)) s"
E=$(ENT $AD); case "$E" in *TERMINATED*) ok "duration entitlement TERMINATED (${E#*|})";; *) bad "duration entitlement: $E";; esac
if [ -z "$offt" ]; then for _ in $(seq 1 50); do [ "$(online $CT)" != 204 ] && { offt=$(($(date +%s)-T0)); break; }; sleep 3; done; fi
E=$(ENT $AT); case "$E" in *TERMINATED*) ok "time-allowance entitlement TERMINATED (${E#*|})";; *) bad "time-allowance entitlement: $E";; esac
TT=$($PSQL "SELECT round(extract(epoch from terminated_at-activated_at))||'s after activation, exhausted_at-activated_at='||coalesce(round(extract(epoch from online_time_exhausted_at-activated_at))::text,'-')||'s' FROM iam_v2.entitlements WHERE id='${E%%|*}'"); info "time-allowance entitlement terminated $TT"
SS=$($PSQL "SELECT string_agg(coalesce(end_reason,'open')||' '||round(extract(epoch from coalesce(ended,now())-started))||'s',', ') FROM iam_v2.sessions WHERE entitlement_id='${E%%|*}'"); info "its sessions: $SS"
[ -n "$offt" ] && [ "$offt" -le 150 ] && ok "time allowance enforced: offline at ~${offt}s of a 1 h window (90 s online allowance)" || { [ -n "$offt" ] && bad "time allowance enforced late: offline only at ~${offt}s for a 90 s online allowance" || bad "time-allowance client still online at $(($(date +%s)-T0)) s"; }
ev=$($PSQL "SELECT coalesce(string_agg(to_state||':'||coalesce(reason,'-'),', ' ORDER BY seq),'') FROM iam_v2.entitlement_state_transitions WHERE entitlement_id='${E%%|*}'" 2>/dev/null); info "its transitions: ${ev:-n/a}"
wex=$($PSQL "SELECT round(extract(epoch from window_ends_at-now())) FROM iam_v2.entitlements WHERE id='${E%%|*}'"); info "its validity window still had ${wex}s left when the allowance ran out"

fi
if want f; then
echo "== f. idle timeout 60 s / maximum session 120 s"
IREV=$(plan acc-im "Acc idle+max" '"idle_timeout_seconds":60,"max_continuous_session_seconds":120'); case "$IREV" in ERR*) bad "idle/max plan: $IREV";; esac
IPK=$(pkg acc-free-im "$IREV" "Acc idle/max" 3600)
UI="acc-i-$R4"; AI=$(newacc "$UI"); CI=i$R4; client $CI
signin $CI "$UI" >/dev/null; acquire $CI "$IPK" >/dev/null; waiton $CI && ok "client online (idle 60 s / max 120 s)" || bad "not online"
S1=$($PSQL "SELECT se.id FROM iam_v2.sessions se JOIN iam_v2.entitlements e ON e.id=se.entitlement_id WHERE e.guest_account_id='$AI' AND se.ended IS NULL LIMIT 1")
info "idle: no traffic from the client now"; endr=""
for _ in $(seq 1 50); do endr=$($PSQL "SELECT coalesce(end_reason,'') FROM iam_v2.sessions WHERE id='$S1' AND ended IS NOT NULL"); [ -n "$endr" ] && break; sleep 3; done
[ "$endr" = IDLE_TIMEOUT ] && ok "idle session ended: end_reason=IDLE_TIMEOUT after $($PSQL "SELECT round(extract(epoch from ended-started)) FROM iam_v2.sessions WHERE id='$S1'")s" || bad "idle session end_reason='$endr'"
[ "$(online $CI)" != 204 ] && ok "device offline after idle timeout" || bad "device still online after IDLE_TIMEOUT"
E=$(ENT $AI); case "$E" in *ACTIVE*) ok "entitlement still ACTIVE (only the session ended)";; *) bad "entitlement after idle: $E";; esac
r=$(signin $CI "$UI"); waiton $CI 6 || { acquire $CI "$IPK" >/dev/null; waiton $CI 6; }
[ "$(online $CI)" = 204 ] && ok "device signs in again and is online" || bad "device could not sign in again ($r)"
S2=$($PSQL "SELECT se.id FROM iam_v2.sessions se JOIN iam_v2.entitlements e ON e.id=se.entitlement_id WHERE e.guest_account_id='$AI' AND se.ended IS NULL LIMIT 1")
ip netns exec $CI ping -i 1 1.1.1.1 >/dev/null 2>&1 & IPP=$!
endr=""; for _ in $(seq 1 60); do endr=$($PSQL "SELECT coalesce(end_reason,'') FROM iam_v2.sessions WHERE id='$S2' AND ended IS NOT NULL"); [ -n "$endr" ] && break; sleep 3; done
kill $IPP 2>/dev/null
[ "$endr" = MAX_SESSION_DURATION ] && ok "busy session ended at max duration: end_reason=MAX_SESSION_DURATION after $($PSQL "SELECT round(extract(epoch from ended-started)) FROM iam_v2.sessions WHERE id='$S2'")s" || bad "max-session end_reason='$endr'"
r=$(signin $CI "$UI"); waiton $CI 6 || { acquire $CI "$IPK" >/dev/null; waiton $CI 6; }
[ "$(online $CI)" = 204 ] && ok "device signs in again after the max-session end" || bad "no re-sign-in after max session"

fi
if want g; then
echo "== g. DISCONNECT_OLDEST (2 devices)"
OREV=$(MAXD=2 DLP=DISCONNECT_OLDEST plan acc-do "Acc disconnect oldest"); case "$OREV" in ERR*) bad "DISCONNECT_OLDEST plan: $OREV";; esac
OPK=$(pkg acc-free-do "$OREV" "Acc 2 devices" 3600)
UO="acc-o-$R4"; AO=$(newacc "$UO")
for k in 1 2 3; do client o$k$R4; done
signin o1$R4 "$UO" >/dev/null; acquire o1$R4 "$OPK" >/dev/null; waiton o1$R4 && ok "device 1 online" || bad "device 1 not online"
sleep 2; signin o2$R4 "$UO" >/dev/null; r=$(acquire o2$R4 "$OPK"); waiton o2$R4 && ok "device 2 online" || bad "device 2 not online ($r)"
sleep 2; signin o3$R4 "$UO" >/dev/null; r=$(acquire o3$R4 "$OPK"); waiton o3$R4 && ok "device 3 admitted over the limit" || bad "device 3 not admitted: $r $(pagetext /tmp/o3$R4.html 160)"
waitoff o1$R4 6 && ok "the oldest device (1) was disconnected" || bad "device 1 still online"
[ "$(online o2$R4)" = 204 ] && ok "device 2 still online" || bad "device 2 went offline"
n=$($PSQL "SELECT count(*) FROM iam_v2.device_limit_disconnections d JOIN iam_v2.entitlements e ON e.id=d.entitlement_id WHERE e.guest_account_id='$AO'" 2>/dev/null); [ "${n:-0}" -ge 1 ] && ok "recorded in device_limit_disconnections ($n)" || bad "no device_limit_disconnections row ($n)"
fi
