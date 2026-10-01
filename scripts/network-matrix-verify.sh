#!/usr/bin/env bash
# CLIENT NETWORKING VERIFICATION MATRIX — run on PRE-LIVE, as root.
#
# It exercises the real appliance: edged, netd, the Linux bridges and VLAN sub-interfaces, Kea and nftables,
# through the operator API. Everything it creates is ADDITIVE and on its own VLANs and subnets; the production
# client network is never the subject of a destructive case, and the run ends by removing what it created.
#
# It is deliberately full of NEGATIVE cases: a matrix whose every row passes proves only that nothing was
# checked. Each refusal below is a refusal the product must make.
set -uo pipefail
R="--resolve hotel.stayconnect.local:443:127.0.0.1"
B=https://hotel.stayconnect.local/api/edge/v1
CK=/tmp/net-matrix-ck
PARENT="${PARENT:-ens192}"
V1=${V1:-3101}; V2=${V2:-3102}; V3=${V3:-3103}
S1=10.231.0; S2=10.232.0; S3=10.233.0
pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
api(){ curl -sk $R -b $CK -H "Content-Type: application/json" "$@"; }
code(){ curl -sk $R -b $CK -o /tmp/nm.out -w '%{http_code}' -H "Content-Type: application/json" "$@"; }
jq1(){ python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }
pg(){ docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site -tAc "$1"; }
authcount(){ nft -j list table inet stayconnect 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)
# auth_ipv4 and phase3_auth_ipv4 are the sets that hold a GUEST AUTHORIZATION. guest_interfaces,
# guest_subnets and walled_garden_ip are structure: they grow with the number of client networks, and
# counting them as "authorizations" would make every added network look like a loss.
print(sum(len(o["set"].get("elem",[])) for o in d["nftables"]
          if "set" in o and o["set"]["name"] in ("auth_ipv4","phase3_auth_ipv4")))'; }
structcount(){ nft -j list table inet stayconnect 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)
print(sum(len(o["set"].get("elem",[])) for o in d["nftables"]
          if "set" in o and o["set"]["name"] in ("guest_interfaces","guest_subnets")))'; }

curl -sk $R -c $CK -H 'Content-Type: application/json' -d '{"email":"admin","password":"admin"}' $B/auth/login -o /dev/null
echo "== 0. baseline =="
BASE_NETS=$(api $B/network/guest-networks | jq1 'len(d["data"])')
BASE_BRIDGES=$(ip -br link show | grep -c '^br-g' || true)
BASE_NFT=$(authcount); BASE_STRUCT=$(structcount)
PROD_ID=$(api $B/network/guest-networks | jq1 'd["data"][0]["id"]')
PROD_BR=$(api $B/network/guest-networks | jq1 'd["data"][0]["bridge_name"]')
echo "  networks=$BASE_NETS bridges=$BASE_BRIDGES guest_authorizations=$BASE_NFT structure=$BASE_STRUCT prod=$PROD_BR"

net(){ # net <name> <vlan> <s> -> json body
  printf '{"name":"%s","network_type":"vlan","parent_interface":"%s","vlan_id":%s,"subnet_cidr":"%s.0/24","gateway_ip":"%s.1","pools":[{"start_ip":"%s.100","end_ip":"%s.200"}]}' "$1" "$PARENT" "$2" "$3" "$3" "$3" "$3"
}

echo "== 1. negative: refusals before anything is written =="
c=$(code -X POST -d "$(net dup $V1 $S1)" $B/network/guest-networks)   # create one first
[ "$c" = 201 ] && N1=$(python3 -c 'import json;print(json.load(open("/tmp/nm.out"))["id"])') || { no "seed VLAN $V1" "$c"; }
c=$(code -X POST -d "$(net dup2 $V1 $S2)" $B/network/guest-networks)
[ "$c" = 400 ] || [ "$c" = 409 ] && ok "a repeated VLAN id on the same port is refused ($c)" || no "duplicate VLAN refused" "$c"
c=$(code -X POST -d "$(net overlap $V2 $S1)" $B/network/guest-networks)
[ "$c" = 409 ] && ok "an overlapping subnet is refused (409 subnet_overlap)" || no "overlapping subnet refused" "$c"
c=$(code -X POST -d "{\"networks\":[$(net b1 $V2 $S2),$(net b2 $V2 $S3)]}" $B/network/guest-networks/batch)
[ "$c" = 400 ] && ok "a batch repeating one VLAN is refused whole" || no "batch duplicate VLAN refused" "$c"
c=$(code -X POST -d "{\"networks\":[$(net b1 $V2 $S2),$(net b2 $V3 $S2)]}" $B/network/guest-networks/batch)
[ "$c" = 409 ] && ok "a batch with two identical subnets is refused whole" || no "batch overlapping subnet refused" "$c"
[ "$(api $B/network/guest-networks | jq1 'len(d["data"])')" = "$((BASE_NETS+1))" ] \
  && ok "every refusal left the catalogue exactly as it was" || no "a refusal created a network"

echo "== 2. a trunk of two more VLANs, in one transaction =="
c=$(code -X POST -d "{\"networks\":[$(net Ext2 $V2 $S2),$(net Ext3 $V3 $S3)]}" $B/network/guest-networks/batch)
[ "$c" = 201 ] && ok "batch created both VLANs" || no "batch create" "$c"
[ "$(api $B/network/guest-networks | jq1 'len(d["data"])')" = "$((BASE_NETS+3))" ] \
  && ok "three test networks beside the production one" || no "network count after batch"

echo "== 3. apply and confirm =="
c=$(code -X POST -d '{}' $B/network/validate); V_OK=$(python3 -c 'import json;print(json.load(open("/tmp/nm.out"))["validation"]["ok"])' 2>/dev/null)
[ "$V_OK" = "True" ] && ok "validation passes for one untagged network plus three tagged VLANs on one trunk" || no "validate" "$c $V_OK"
c=$(code -X POST -d '{"summary":"networking matrix"}' $B/network/apply)
REV=$(python3 -c 'import json;print(json.load(open("/tmp/nm.out")).get("revision_id",""))' 2>/dev/null)
ST=$(python3 -c 'import json;print(json.load(open("/tmp/nm.out")).get("state",""))' 2>/dev/null)
[ "$ST" = "pending_confirmation" ] && ok "apply reached pending_confirmation" || no "apply" "$c $ST"
for v in $V1 $V2 $V3; do
  ip -br link show "br-g$v" >/dev/null 2>&1 && ok "bridge br-g$v exists" || no "bridge br-g$v" "missing"
  ip -br link show "$PARENT.$v" >/dev/null 2>&1 && ok "VLAN sub-interface $PARENT.$v exists" || no "subif $PARENT.$v" "missing"
done
A1=$(ip -o -4 addr show dev br-g$V1 | awk '{print $4}')
[ "$A1" = "$S1.1/24" ] && ok "br-g$V1 carries exactly its gateway $S1.1/24" || no "br-g$V1 addressing" "$A1"
ip -br link show "$PROD_BR" >/dev/null 2>&1 && ok "the production bridge $PROD_BR is untouched" || no "production bridge" "gone"
for v in $V1 $V2 $V3; do
  grep -q "$(echo $S1 | cut -d. -f1-2)" /dev/null 2>&1 # no-op, keeps shellcheck quiet
done
KEA=$(docker exec stayconnect-pg true 2>/dev/null; ss -lunp 2>/dev/null | grep -c ':67 ' || true)
[ "${KEA:-0}" -ge 1 ] && ok "Kea is listening on DHCP (:67)" || no "kea listening" "$KEA"
SCOPES=$(curl -sk $R -b $CK "$B/network/dhcp/leases?page=1&page_size=1" -o /dev/null -w '%{http_code}')
[ "$SCOPES" = 200 ] && ok "the DHCP surface answers" || no "dhcp surface" "$SCOPES"
NFT_NOW=$(authcount); STRUCT_NOW=$(structcount)
[ "$NFT_NOW" = "$BASE_NFT" ] && ok "no guest authorization was lost ($NFT_NOW in auth_ipv4+phase3_auth_ipv4)" || no "nft authorizations" "$BASE_NFT -> $NFT_NOW"
[ "$STRUCT_NOW" -gt "$BASE_STRUCT" ] && ok "the new networks are in the nft structure ($BASE_STRUCT -> $STRUCT_NOW interface/subnet elements)" || no "nft structure did not grow" "$BASE_STRUCT -> $STRUCT_NOW"
c=$(code -X POST -d '{}' $B/network/revisions/$REV/confirm)
[ "$c" = 200 ] && ok "confirm accepted" || no "confirm" "$c"

echo "== 4. a staged topology change =="
# Stage: VLAN V1 -> V1+100, with a new subnet.
STAGE=$(printf '{"network_type":"vlan","parent_interface":"%s","vlan_id":%s,"reason":"matrix: move to another VLAN","subnet_cidr":"%s.0/24","gateway_ip":"%s.1","pools":[{"start_ip":"%s.100","end_ip":"%s.200"}]}' "$PARENT" "$((V1+100))" "10.234.0" "10.234.0" "10.234.0" "10.234.0")
c=$(code -X POST -d "$STAGE" $B/network/guest-networks/$N1/replace)
REPL=$(python3 -c 'import json;print(json.load(open("/tmp/nm.out")).get("replacement_id",""))' 2>/dev/null)
[ "$c" = 201 ] && ok "the change was staged" || no "stage" "$c"
EN=$(pg "SELECT enabled FROM guest_networks WHERE id='$N1'")
TY=$(pg "SELECT network_type||'/'||COALESCE(vlan_id::text,'-') FROM guest_networks WHERE id='$N1'")
[ "$EN" = "t" ] && [ "$TY" = "vlan/$V1" ] && ok "staging changed nothing: the network is still enabled on VLAN $V1" || no "staging changed the network" "$EN $TY"
[ "$(pg "SELECT count(*) FROM guest_networks WHERE tenant_id IS NOT NULL")" = "$((BASE_NETS+3))" ] \
  && ok "staging created no network row" || no "staging created a row"
ip -br link show "br-g$V1" >/dev/null 2>&1 && ok "staging left the live bridge alone" || no "staging touched the wire"

echo "== 5. apply the replacement, then roll it back =="
c=$(code -X POST -d '{"summary":"matrix: apply replacement"}' $B/network/apply)
REV2=$(python3 -c 'import json;print(json.load(open("/tmp/nm.out")).get("revision_id",""))' 2>/dev/null)
ST2=$(python3 -c 'import json;print(json.load(open("/tmp/nm.out")).get("state",""))' 2>/dev/null)
[ "$ST2" = "pending_confirmation" ] && ok "the replacement applied" || no "apply replacement" "$c $ST2"
ip -br link show "br-g$((V1+100))" >/dev/null 2>&1 && ok "the successor bridge br-g$((V1+100)) is live" || no "successor bridge" "missing"
! ip -br link show "br-g$V1" >/dev/null 2>&1 && ok "the retired bridge br-g$V1 is gone" || no "old bridge still live"
A2=$(ip -o -4 addr show dev br-g$((V1+100)) | awk '{print $4}')
[ "$A2" = "10.234.0.1/24" ] && ok "the successor carries its new gateway" || no "successor addressing" "$A2"
NEWID=$(pg "SELECT successor_network_id FROM iam_v2.guest_network_replacements WHERE id='$REPL'")
RPOOL=$(pg "SELECT count(*) FROM dhcp_pools WHERE guest_network_id='$NEWID'")
[ "$RPOOL" -ge 1 ] && ok "the successor carried a DHCP pool" || no "carried pool" "$RPOOL"
# ...and roll it back.
c=$(code -X POST -d '{}' $B/network/revisions/$REV2/rollback)
[ "$c" = 200 ] && ok "rollback accepted" || no "rollback" "$c"
sleep 3
ip -br link show "br-g$V1" >/dev/null 2>&1 && ok "ROLLBACK REBUILT the bridge the failed apply removed" || no "bridge not rebuilt after rollback"
A3=$(ip -o -4 addr show dev br-g$V1 | awk '{print $4}')
[ "$A3" = "$S1.1/24" ] && ok "the rebuilt bridge carries its original gateway again" || no "rebuilt addressing" "$A3"
EN2=$(pg "SELECT enabled FROM guest_networks WHERE id='$N1'")
SU=$(pg "SELECT COALESCE(successor_network_id::text,'-') FROM iam_v2.guest_network_replacements WHERE id='$REPL'")
RS=$(pg "SELECT state FROM iam_v2.guest_network_replacements WHERE id='$REPL'")
[ "$EN2" = "t" ] && [ "$SU" = "-" ] && [ "$RS" = "PENDING" ] \
  && ok "the database agrees with the wire again: original enabled, successor gone, request retryable" \
  || no "database/wire disagreement after rollback" "enabled=$EN2 successor=$SU state=$RS"
[ "$(pg "SELECT count(*) FROM guest_networks")" = "$((BASE_NETS+3))" ] && ok "no orphan network row survived the rollback" || no "orphan row after rollback"
NFT_END=$(authcount)
[ "$NFT_END" = "$BASE_NFT" ] && ok "guest authorizations survived apply+rollback" || no "authorizations changed" "$BASE_NFT -> $NFT_END"

echo "== 6. cancel, then clean up =="
c=$(code -X DELETE $B/network/guest-network-replacements/$REPL)
[ "$c" = 200 ] && ok "a staged request can be cancelled" || no "cancel" "$c"
for id in $(pg "SELECT id FROM guest_networks WHERE name LIKE 'Ext%' OR name='dup'"); do
  code -X POST -d '{}' $B/network/guest-networks/$id/disable >/dev/null
done
c=$(code -X POST -d '{"summary":"matrix: remove test networks"}' $B/network/apply)
REV3=$(python3 -c 'import json;print(json.load(open("/tmp/nm.out")).get("revision_id",""))' 2>/dev/null)
code -X POST -d '{}' $B/network/revisions/$REV3/confirm >/dev/null
for id in $(pg "SELECT id FROM guest_networks WHERE name LIKE 'Ext%' OR name='dup'"); do
  code -X DELETE $B/network/guest-networks/$id >/dev/null
done
LEFT=$(pg "SELECT count(*) FROM guest_networks")
[ "$LEFT" = "$BASE_NETS" ] && ok "the appliance is back to its $BASE_NETS production network(s)" || no "cleanup left rows" "$LEFT"
ip -br link show "$PROD_BR" >/dev/null 2>&1 && ok "the production bridge is still up at the end" || no "production bridge missing"
NFT_FIN=$(authcount); STRUCT_FIN=$(structcount)
[ "$NFT_FIN" = "$BASE_NFT" ] && ok "guest authorizations unchanged across the whole run" || no "authorizations changed overall" "$BASE_NFT -> $NFT_FIN"
[ "$STRUCT_FIN" = "$BASE_STRUCT" ] && ok "the nft structure is back to the production network alone" || no "nft structure not restored" "$BASE_STRUCT -> $STRUCT_FIN"
rm -f $CK /tmp/nm.out
echo "============================================================"
echo "NETWORK_MATRIX pass=$pass fail=$fail"
[ "$fail" -eq 0 ]
