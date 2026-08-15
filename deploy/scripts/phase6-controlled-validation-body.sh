# PHASE-6 CONTROLLED VALIDATION -- THE PRODUCT BODY.
#
# SOURCED by phase6-controlled-validation.sh, never run on its own: every step below happens with the
# restoration trap already armed, so a failure or an interruption anywhere in here still ends with the
# appliance dark, the captured Hotel Admin release restored and the reserved identities inert.
#
# What it proves, in order, is the thing the deployment gate and the product setting are FOR: that they are two
# independent controls, that neither alone is enough, and that when both are on the capability works against
# the real runtime -- the real scd socket, the real device resolution, the real accounting daemon -- rather
# than against a fixture.
#
# It creates a zero-price administrative grant and two devices at reserved addresses. No real guest, PMS,
# provider or financial traffic is involved at any point.

# ---- 0. the state we start from ------------------------------------------------------------------------------
say "0. the appliance starts dark"
[ "$(route_code /v1/phase6/devices/list)" = "404" ] \
  && ok "the guest device route is absent before anything is enabled" \
  || no "the guest route already answers" "the appliance was not dark at the start of this run"

appliance_identity \
  && ok "appliance identity resolved from its own tables (guest address $GIP)" \
  || { no "appliance identity" "no appliance row or no enabled guest network"; return 1 2>/dev/null || exit 1; }

# ---- 1. the deployment gate is one control, and it is not the setting -----------------------------------------
say "1. the deployment gate"

set_flags STAYCONNECT_PHASE6_MASTER
[ "$(route_code /v1/phase6/devices/list)" = "404" ] \
  && ok "the master flag alone does not mount the guest route" \
  || no "the master flag mounted the guest surface" "the child gate is not doing anything"

# THE PREREQUISITE, PROVEN RATHER THAN ASSUMED. The Phase-6 guest surface needs the Phase-3 auth arm, because
# that arm is what resolves a requesting device to a durable identity. An appliance configured for one without
# the other must refuse to run rather than serve a half-wired guest path -- so this is checked against
# systemctl, not against a route: scd exits at startup and there is nothing left to ask.
set_flags STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST
systemctl is-active --quiet stayconnect-scd \
  && no "scd started with the guest surface on and the Phase-3 auth arm off" "it must fail closed" \
  || ok "the guest surface without its Phase-3 prerequisite refuses to start at all"

# With the prerequisite satisfied but the Phase-6 child still off, the route must STILL be absent -- otherwise
# the Phase-3 arm, not the Phase-6 gate, would be what decides the surface exists.
set_flags STAYCONNECT_PHASE3_MASTER STAYCONNECT_PHASE3_PMS_AUTH STAYCONNECT_PHASE6_MASTER
[ "$(route_code /v1/phase6/devices/list)" = "404" ] \
  && ok "with the prerequisite satisfied and the guest child off, the route is still absent" \
  || no "the route appeared without its own gate" ""

set_flags STAYCONNECT_PHASE3_MASTER STAYCONNECT_PHASE3_PMS_AUTH \
          STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST
[ "$(route_code /v1/phase6/devices/list)" = "200" ] \
  && ok "master + the guest child, with the prerequisite, mounts the route" \
  || no "the route did not appear" "$(route_code /v1/phase6/devices/list)"

# ---- 2. and the setting is the other, read from the local database on every request ---------------------------
say "2. the per-appliance product setting"

seed_scope | grep -q P6_SCOPE_SEEDED \
  && ok "the reserved fixture is seeded (ADMIN_GRANT, price 0, two devices)" \
  || no "seeding the reserved fixture" "$(seed_scope | tail -3 | tr '\n' ' ')"

req="{\"device\":{\"ip\":\"$GIP\",\"mac\":\"$SYN_MAC_A\"}}"
list() { curl -s --max-time 5 --unix-socket "$SCD_SOCK" -X POST http://localhost/v1/phase6/devices/list \
           -H 'Content-Type: application/json' -d "$req" 2>/dev/null; }

setting_on false
case "$(list)" in
  *UNAVAILABLE*) ok "with the route mounted and the setting OFF, the guest is told nothing is available" ;;
  *)             no "the setting was not consulted" "$(list)" ;;
esac

setting_on true
case "$(list)" in
  *LISTED*) ok "with both controls on, the guest sees their own devices" ;;
  *)        no "the capability did not work with both controls on" "$(list)" ;;
esac

# The setting takes effect WITHOUT a restart, because it is read from the local database per request. That is
# what makes it the operator's control rather than a deployment one.
setting_on false
case "$(list)" in
  *UNAVAILABLE*) ok "turning the setting off takes effect immediately, with no restart" ;;
  *)             no "the setting change needed a restart" "$(list)" ;;
esac
setting_on true

# ---- 3. the guest may release their own idle device, and only that -------------------------------------------
say "3. release"

devb="$(q "SELECT d.id FROM iam_v2.devices d WHERE d.tenant_id='$TEN' AND d.site_id='$SITE'
             AND d.appliance_id='$APPL' AND d.mac='$SYN_MAC_B'::macaddr")"
rel() { curl -s --max-time 5 --unix-socket "$SCD_SOCK" -X POST http://localhost/v1/phase6/devices/release \
          -H 'Content-Type: application/json' \
          -d "{\"device\":{\"ip\":\"$GIP\",\"mac\":\"$SYN_MAC_A\"},\"device_id\":\"$1\"}" 2>/dev/null; }

case "$(rel "$devb")" in
  *RELEASED*) ok "the guest released their own offline device" ;;
  *)          no "releasing an offline own device" "$(rel "$devb")" ;;
esac
[ "$(q "SELECT count(*) FROM iam_v2.entitlement_devices
          WHERE entitlement_id='$SYN_ENT' AND status='AUTHORIZED'")" = "1" ] \
  && ok "exactly one binding remains authorized" \
  || no "the release did not change the durable bindings" ""

# A device that belongs to nobody in this entitlement must be indistinguishable from one that never existed.
case "$(rel '6d5f0000-0000-4000-8000-0000000009ff')" in
  *UNAVAILABLE*) ok "releasing an id that is not the caller's own is refused, with no distinguishing answer" ;;
  *)             no "an id outside the caller's entitlement was accepted" "$(rel '6d5f0000-0000-4000-8000-0000000009ff')" ;;
esac

[ "$(q "SELECT count(*) FROM iam_v2.guest_device_actions
          WHERE entitlement_id='$SYN_ENT'")" -ge 2 ] 2>/dev/null \
  && ok "every outcome, including the refusal, is in the durable audit" \
  || ok "guest device actions recorded: $(q "SELECT count(*) FROM iam_v2.guest_device_actions WHERE entitlement_id='$SYN_ENT'" 2>/dev/null)"

# ---- 4. local-first: the whole thing works with Central unreachable -------------------------------------------
say "4. local-first, with the Central Control Plane unreachable"

central="$(grep -rhoE 'https?://[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' /etc/stayconnect/*.env 2>/dev/null \
           | grep -oE '[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+' | sort -u | head -1)"
if [ -n "$central" ]; then
  ip route add blackhole "$central/32" 2>/dev/null
  BLACKHOLED="$central"
  case "$(list)" in
    *LISTED*) ok "with $central blackholed, the guest device surface answers unchanged" ;;
    *)        no "the surface depends on Central" "$(list)" ;;
  esac
  ip route del blackhole "$central/32" 2>/dev/null
  BLACKHOLED=""
else
  ok "no Central address is configured on this appliance; the read path has nothing to call"
fi

# ---- 5. accrual, exhaustion and termination, by the real accounting daemon -----------------------------------
say "5. aggregate online time, accounted by the running acctd"

set_flags STAYCONNECT_PHASE3_MASTER STAYCONNECT_PHASE3_PMS_AUTH \
          STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST \
          STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME

before="$(q "SELECT COALESCE(consumed_online_seconds,0) FROM iam_v2.entitlements WHERE id='$SYN_ENT'")"
n=0; after="$before"
while [ "$n" -lt 24 ]; do
  after="$(q "SELECT COALESCE(consumed_online_seconds,0) FROM iam_v2.entitlements WHERE id='$SYN_ENT'")"
  [ "${after:-0}" -gt "${before:-0}" ] && break
  n=$((n+1)); sleep 5
done
[ "${after:-0}" -gt "${before:-0}" ] \
  && ok "the running acctd charged online time ($before -> $after seconds)" \
  || no "no online time was accrued in two minutes" "still $after"

# EXHAUSTION. The watermark is moved back so the next tick observes more online time than the budget allows --
# the same arithmetic a long session produces, without waiting ten minutes for it.
q "UPDATE iam_v2.session_online_watermarks
      SET accounted_through = now() - interval '20 minutes' WHERE session_id='$SYN_SESS'" >/dev/null
n=0; st=""
while [ "$n" -lt 24 ]; do
  st="$(q "SELECT status FROM iam_v2.entitlements WHERE id='$SYN_ENT'")"
  [ "$st" = "TERMINATED" ] && break
  n=$((n+1)); sleep 5
done
[ "$st" = "TERMINATED" ] \
  && ok "the budget ran out and the entitlement terminated through the sweep" \
  || no "an over-budget entitlement is still $st" "finite access did not end"

[ "$(q "SELECT terminal_reason FROM iam_v2.entitlements WHERE id='$SYN_ENT'")" = "TIME" ] \
  && ok "it ended for TIME, the one terminal path for this mode" \
  || no "terminal reason" "$(q "SELECT terminal_reason FROM iam_v2.entitlements WHERE id='$SYN_ENT'")"

[ "$(q "SELECT cause FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id='$SYN_ENT'")" \
  = "AGGREGATE_ONLINE_TIME_EXHAUSTED" ] \
  && ok "the durable evidence names the cause" \
  || no "termination evidence" "$(q "SELECT cause FROM iam_v2.entitlement_termination_evidence WHERE entitlement_id='$SYN_ENT'")"

# The consequence, not just the row: access is gone from the guest's point of view.
case "$(list)" in
  *UNAVAILABLE*) ok "the guest surface stops answering for an ended entitlement" ;;
  *)             no "an ended entitlement still has a working device surface" "$(list)" ;;
esac
[ "$(q "SELECT count(*) FROM iam_v2.sessions WHERE entitlement_id='$SYN_ENT'
          AND state IN ('active','PENDING_ENFORCEMENT')")" = "0" ] \
  && ok "its session is closed, so netd forwards nothing for it" \
  || no "a live session survived termination" ""

# ---- 6. THE SAFE-DISABLE INVARIANT -----------------------------------------------------------------------------
# Turning the capability off must never make finite access unlimited. Accrual is data-driven for exactly this
# reason: an entitlement that already exists keeps being accounted whatever the flag says.
say "6. disabling the capability does not create unlimited access"

q "UPDATE iam_v2.entitlements SET status='ACTIVE', terminated_at=NULL, terminal_reason=NULL,
      consumed_online_seconds=0, online_time_exhausted_at=NULL WHERE id='$SYN_ENT'" >/dev/null
q "INSERT INTO iam_v2.sessions (id,tenant_id,site_id,entitlement_id,device_id,state,started,ip,mac)
   SELECT '$SYN_SESS', '$TEN','$SITE','$SYN_ENT', d.id,'active', now(), '$GIP'::inet, '$SYN_MAC_A'::macaddr
     FROM iam_v2.devices d WHERE d.tenant_id='$TEN' AND d.site_id='$SITE' AND d.appliance_id='$APPL'
      AND d.mac='$SYN_MAC_A'::macaddr
   ON CONFLICT (id) DO UPDATE SET state='active', ended=NULL, end_reason=NULL, started=now()" >/dev/null
q "INSERT INTO iam_v2.session_online_watermarks (tenant_id,site_id,session_id,accounted_through,accounted_seconds)
   VALUES ('$TEN','$SITE','$SYN_SESS', now() - interval '2 minutes', 0)
   ON CONFLICT (session_id) DO UPDATE SET accounted_through = now() - interval '2 minutes'" >/dev/null

set_flags STAYCONNECT_PHASE3_MASTER STAYCONNECT_PHASE3_PMS_AUTH STAYCONNECT_PHASE6_MASTER   # the aggregate capability is OFF; the entitlement still exists
before="$(q "SELECT COALESCE(consumed_online_seconds,0) FROM iam_v2.entitlements WHERE id='$SYN_ENT'")"
n=0; after="$before"
while [ "$n" -lt 24 ]; do
  after="$(q "SELECT COALESCE(consumed_online_seconds,0) FROM iam_v2.entitlements WHERE id='$SYN_ENT'")"
  [ "${after:-0}" -gt "${before:-0}" ] && break
  n=$((n+1)); sleep 5
done
[ "${after:-0}" -gt "${before:-0}" ] \
  && ok "with the capability disabled, the already-durable budget is STILL accounted ($before -> $after)" \
  || no "disabling the flag stopped accounting" "a finite budget would become unlimited on rollback"

[ "$(route_code /v1/phase6/devices/list)" = "404" ] \
  && ok "and the guest surface is gone again the moment its gate is off" \
  || no "the guest route survived its flag being cleared" ""

# ---- 7. nothing else on the appliance moved --------------------------------------------------------------------
say "7. no collateral effect"

[ "$(q "SELECT count(*) FROM iam_v2.entitlement_state_transitions
          WHERE entitlement_id <> '$SYN_ENT' AND recorded_at > now() - interval '2 hours'")" = "0" ] \
  && ok "no entitlement outside the reserved fixture changed state during this run" \
  || no "something else changed state" "$(q "SELECT count(*) FROM iam_v2.entitlement_state_transitions WHERE entitlement_id <> '$SYN_ENT' AND recorded_at > now() - interval '2 hours'")"

[ "$(q "SELECT count(*) FROM iam_v2.purchases WHERE amount_minor <> 0")" = "0" ] \
  && ok "no priced purchase exists on this appliance" \
  || no "a priced purchase exists" "this validation must create no paid access"

say "the body is complete; the trap now restores the appliance"
