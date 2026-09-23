#!/usr/bin/env bash
# PHASE-6 CONTROLLED LIVE-DARK VALIDATION -- self-restoring, and it proves the restoration.
#
# THIS IS THE ONLY THING IN PHASE 6 THAT TURNS CAPABILITIES ON. The danger was never the enabling; it is being
# interrupted afterwards. A run killed between the flags going on and coming off leaves a DEVELOPMENT appliance
# advertising a capability nobody meant to leave running, synthetic state in its database, and an operator with
# no reason to suspect either.
#
# So restoration is a TRAP -- success, failure, error, INT, TERM -- and it restores a CAPTURED BASELINE rather
# than tidying up towards what it assumes the appliance looked like:
#
#   * the exact DARK Hotel Admin release, by path and content hash, captured before anything changes. Not "the
#     previous release": a run that itself deploys a flagged bundle makes the flagged one previous.
#   * every Phase-6 flag off, verified through the authoritative flag-coherence gate -- not by grepping the
#     files this script wrote, which only ever proves that it can write files.
#   * the per-appliance product setting returned to its captured value, PER APPLIANCE, through the sanctioned
#     audited writer. Never `UPDATE appliance_product_settings SET ... WHERE guest_device_self_service`: that
#     is an owner-level write across every appliance in the database, and it leaves no audit record of who
#     changed what.
#   * every synthetic row identified by SCOPE. The reserved tenant in phase6-validation-scope.sql exists for
#     nothing else, so teardown cannot reach a real guest's row -- and no marker is invented in any constrained
#     business vocabulary to make test rows findable.
#   * and the runtime proven dark afterwards: routes ABSENT, services healthy, accounting owner present.
#
# It refuses to run anywhere but an appliance it was COMPILED to run on, and that refusal is NOT overridable
# by an environment variable -- not the allow-list, and not the path it reads the appliance's identity from.
# A feature-enabling runner that can be pointed at another host by exporting one variable is not protected,
# it is merely inconvenienced.
#
# The check is no longer a hostname. It is a compiled host -> (appliance_id, serial) map, confirmed against
# the appliance's own SIGNED assignment document and then again against its row in the site database, and it
# fails closed on a missing entry, a missing file, an unreadable field or any disagreement. See
# guard_environment.
#
#   usage:  phase6-controlled-validation.sh              full run: capture, enable, validate, restore, verify
#           phase6-controlled-validation.sh run-device-selfservice
#                                                        the same run, stopping at the boundary of what was
#                                                        authorised for PRE-LIVE: Guest Device Self-Service
#                                                        only, without enabling the aggregate capability
#           phase6-controlled-validation.sh restore      restore + verify only (safe at any time, idempotent)
#           phase6-controlled-validation.sh selftest CASE
#                                                        fault injection -- enables a flag and then abandons
#                                                        the run (body-failure | signal | partial |
#                                                        partial-device-selfservice | double-restore)
set -uo pipefail

# ---- identity: compiled in, not configurable ---------------------------------------------------------------
#
# A HOSTNAME WAS THE WHOLE CHECK, AND A HOSTNAME IS NOT AN IDENTITY. This allow-list named `radius`, the
# DEVELOPMENT appliance, which CLAUDE.md 0D has since RETIRED: "not an operational target. Do not contact it".
# So the only thing in Phase 6 that can turn a guest capability on was admitted by a string any host can be
# given, and the one host it named must never be reached again. Both halves of that are now fixed.
#
# THE ALLOW-LIST IS A MAP, NOT A LIST. A host is admitted only when the appliance's OWN identity matches the
# value compiled here for that host, and that identity is read from the SIGNED assignment document -- the
# same file scd verifies against the pinned registry root -- and then confirmed a second time against the
# appliance's own row in the site database. A host with no entry is refused. An entry that does not match is
# refused. An unreadable identity is refused. There is no environment variable that changes any of it: a
# feature-enabling runner that can be redirected by exporting one name is not protected, it is inconvenienced.
#
# PRE-LIVE IS ADMITTED BY EXPLICIT PRODUCT-OWNER AUTHORISATION, for ONE controlled acceptance run of Guest
# Device Self-Service, after which the capability returns to its approved default-OFF state. That is
# controlled acceptance, not Guest activation and not a Guest pilot: D41 remains in force.
readonly AUTHORIZED_HOSTS="sce"
readonly AUTHORIZED_DB="stayconnect_site"

# authorized_appliance_for <hostname> -> "<appliance_id> <serial>", or non-zero when the host has no entry.
#
# Compiled in, one line per authorised appliance, and deliberately verbose: reading this file must tell you
# exactly which physical appliance may be enabled, not merely which name may be.
authorized_appliance_for() {
  case "$1" in
    # PRE-LIVE 172.21.60.25 -- the only appliance (CLAUDE.md 0D). Authorised for the controlled Guest Device
    # Self-Service acceptance run.
    sce) printf '%s %s' 'c6faf4eb-33e4-41b3-9fcb-d902568cc1c9' 'SC-7A8M-WM9R-KMAZ' ;;
    # `radius` is deliberately ABSENT. It was the only entry here and it is retired; leaving a retired host in
    # an enabling allow-list is a standing permission to enable a guest capability on a machine nobody is
    # supposed to touch.
    *) return 1 ;;
  esac
}
# THE PATH IS NOT OVERRIDABLE EITHER, and the first draft of this line made it so
# (PHASE6_ASSIGNMENT_DOC:-...). That would have been the bypass this file exists to refuse: an identity check
# whose SOURCE can be redirected by an environment variable checks whatever the caller points it at.
readonly ASSIGNMENT_DOC="/etc/stayconnect/assignment/assignment.json"

ENV_DIR="/etc/stayconnect"
UNITS="stayconnect-scd stayconnect-acctd stayconnect-edged"
ALL_UNITS="$UNITS stayconnect-portald stayconnect-netd stayconnect-hotel-admin"
PG="stayconnect-pg"
DBUSER="${PHASE6_DB_USER:-stayconnect}"
DB="$AUTHORIZED_DB"
# THE GATE THAT PROVES DARKNESS IS NOT REDIRECTABLE EITHER. This was
# PHASE6_COHERENCE:-/root/phase6-flag-coherence.sh, and $COHERENCE is what the restoration verification runs
# to establish that every Phase-6 flag is off: an environment variable that chooses WHICH script answers that
# question can choose one that always exits 0. Same class of hole as an overridable identity source, in the
# step whose entire purpose is proving the capability went back off.
#
# Two FIXED locations, tried in order, neither caller-chosen: the validation directory this harness is
# installed into, and the path the accepted development-appliance layout used.
coherence_path() {
  local p
  for p in /opt/stayconnect/validation/phase6-flag-coherence.sh /root/phase6-flag-coherence.sh; do
    [ -f "$p" ] && { printf '%s' "$p"; return 0; }
  done
  printf '%s' /opt/stayconnect/validation/phase6-flag-coherence.sh
}
COHERENCE="$(coherence_path)"
VALIDATION_DIR="/opt/stayconnect/validation"
HA_CURRENT="/opt/stayconnect/hotel-admin"
STATE_DIR="/var/lib/stayconnect/phase6-validation"
SCD_SOCK="/run/stayconnect/scd.sock"

# The reserved identities, matching phase6-validation-scope.sql. Fixed rather than generated, and written down
# in both places on purpose: teardown has to work from a cold start, including after a run that died before it
# could record anything anywhere.
# The reserved STAY is the anchor, because the entitlement cannot be: its transitions and its termination
# evidence are append-only, so each run has to grant a new one. Everything the validation creates hangs off
# this stay, and teardown works from that -- an identifier stronger than a marker, since it is a foreign key
# to a row nothing else points at.
readonly SYN_STAY="6d5f0000-0000-4000-8000-000000000102"
readonly SYN_MAC_A="02:00:00:60:00:01"
readonly SYN_MAC_B="02:00:00:60:00:02"
SYN_ENT=""   # resolved after seeding
SYN_SESS=""

pass=0; fail=0
BLACKHOLED=""
HAD_PREREQ=""
TEN=""; SITE=""; APPL=""; GIP=""
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
say(){ printf '\n== %s ==\n' "$1"; }
# </dev/null MATTERS. `docker exec -i` inherits this shell's stdin, so calling q inside a `while read` loop
# lets psql swallow the rest of the file being read. That is not theoretical: the writer-boundary grant looped
# over two roles, granted svc_scd, and never saw the svc_acctd line -- so acctd spent the entire aggregate
# section refusing to start while everything else looked configured.
q(){ docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -tAqc "$1" </dev/null 2>&1; }

# guard_environment refuses to run anywhere it was not compiled to run, on FOUR independent grounds. Every
# one of them fails closed: a missing file, an unreadable field, a mismatch or an absent entry all refuse.
guard_environment() {
  local host want_appl want_serial got_appl got_serial db_appl
  host="$(hostname)"

  # 1. the host must be named.
  case " $AUTHORIZED_HOSTS " in
    *" $host "*) : ;;
    *) echo "REFUSED: host '$host' is not in the compiled allow-list ($AUTHORIZED_HOSTS)" >&2; exit 2 ;;
  esac

  # 2. and it must have a compiled appliance identity. A host in the list with no entry is a mistake, and the
  #    safe reading of a mistake here is "do not enable a guest capability".
  if ! set -- $(authorized_appliance_for "$host"); then
    echo "REFUSED: host '$host' is listed but has no compiled appliance identity" >&2; exit 2
  fi
  want_appl="${1:-}"; want_serial="${2:-}"
  [ -n "$want_appl" ] && [ -n "$want_serial" ] || {
    echo "REFUSED: the compiled entry for '$host' is incomplete" >&2; exit 2; }

  # 3. THE APPLIANCE'S OWN SIGNED ASSIGNMENT must agree. This is the document scd verifies against the pinned
  #    registry root anchor, so it is the appliance's strongest statement of who it is. Read with python3
  #    rather than grep, because a field that happens to appear in a comment or another object is not the
  #    field being asked for.
  [ -r "$ASSIGNMENT_DOC" ] || {
    echo "REFUSED: cannot read the signed assignment at $ASSIGNMENT_DOC, so this appliance cannot be identified" >&2
    exit 2; }
  got_appl="$(python3 -c 'import json,sys
try:
    d=json.load(open(sys.argv[1]))["current"]
    print(d.get("appliance_id") or "")
except Exception:
    print("")' "$ASSIGNMENT_DOC" 2>/dev/null)"
  got_serial="$(python3 -c 'import json,sys
try:
    d=json.load(open(sys.argv[1]))["current"]
    print(d.get("serial") or "")
except Exception:
    print("")' "$ASSIGNMENT_DOC" 2>/dev/null)"
  [ "$got_appl" = "$want_appl" ] || {
    echo "REFUSED: this appliance reports id '${got_appl:-<unreadable>}'; '$host' is compiled as '$want_appl'" >&2
    exit 2; }
  [ "$got_serial" = "$want_serial" ] || {
    echo "REFUSED: this appliance reports serial '${got_serial:-<unreadable>}'; '$host' is compiled as '$want_serial'" >&2
    exit 2; }

  # 4. AND A SECOND, INDEPENDENT SOURCE. The site database's own appliances row must carry the same id. Two
  #    sources that have to agree is what makes this an identity check rather than a file read: a stale or
  #    hand-edited assignment does not by itself admit the run.
  db_appl="$(q "SELECT id FROM public.appliances WHERE id = '$want_appl'")"
  [ "$db_appl" = "$want_appl" ] || {
    echo "REFUSED: the site database does not hold appliance '$want_appl' (got '${db_appl:-<none>}')" >&2
    echo "         The signed assignment and the database disagree about which appliance this is." >&2
    exit 2; }

  case "$DB" in
    *prod*|*production*) echo "REFUSED: database '$DB' looks like Production" >&2; exit 2 ;;
  esac

  printf 'identity: %s / appliance %s / serial %s -- authorised, from the signed assignment and the site DB\n' \
    "$host" "$want_appl" "$want_serial"
}

# ---- baseline capture ---------------------------------------------------------------------------------------
capture_baseline() {
  mkdir -p "$STATE_DIR"
  readlink -f "$HA_CURRENT" > "$STATE_DIR/ha_release"
  ha_hash > "$STATE_DIR/ha_hash"
  # Per appliance, so restoration can put each row back to the value it actually had rather than to a global
  # guess about what "off" meant here.
  docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -tAqc \
    "SELECT tenant_id||' '||site_id||' '||appliance_id||' '||guest_device_self_service
       FROM iam_v2.appliance_product_settings
      ORDER BY tenant_id, site_id, appliance_id" > "$STATE_DIR/settings" 2>/dev/null
  local u
  for u in $UNITS; do
    cp -p "$ENV_DIR/${u#stayconnect-}.env" "$STATE_DIR/${u}.env.baseline" 2>/dev/null || true
  done
  capture_writer_prereq
  ok "baseline captured: dark release $(basename "$(cat "$STATE_DIR/ha_release")"), hash $(cut -c1-12 < "$STATE_DIR/ha_hash"), $(wc -l < "$STATE_DIR/settings" | tr -d ' ') setting row(s), svc_scd writer grant=$HAD_PREREQ"
}

ha_hash() {
  # Content, not just the symlink target. Two releases can share a path after a redeploy; they cannot share
  # this. Restoring by path alone would be satisfied by a directory whose contents had been replaced.
  ( cd "$HA_CURRENT" 2>/dev/null && find . -type f \( -name '*.js' -o -name '*.json' -o -name '*.html' \) \
      -print0 2>/dev/null | sort -z | xargs -0 sha256sum 2>/dev/null | sha256sum | awk '{print $1}' ) \
    || echo "unhashable"
}

# ---- restoration ---------------------------------------------------------------------------------------------
restore_flags() {
  local u f
  for u in $UNITS; do
    f="$ENV_DIR/${u#stayconnect-}.env"
    if [ -f "$STATE_DIR/${u}.env.baseline" ]; then
      cp -p "$STATE_DIR/${u}.env.baseline" "$f"
    else
      # No baseline (restore-only run on a machine that never captured one): strip every Phase-6 flag. Removing
      # a line is safe in a way that writing "=false" is not -- a value nobody can parse must never be read as
      # permission, and the services already fail closed on one.
      sed -i '/^STAYCONNECT_PHASE6_/d' "$f" 2>/dev/null || true
    fi
  done
  restart_units
  settle_units 30 >/dev/null 2>&1 || true
  wait_for_scd || true
}

restore_hotel_admin() {
  local want now
  want="$(cat "$STATE_DIR/ha_release" 2>/dev/null || true)"
  [ -n "$want" ] && [ -d "$want" ] || return 0
  now="$(readlink -f "$HA_CURRENT" 2>/dev/null || true)"
  [ "$now" = "$want" ] && return 0
  ln -sfn "$want" "$HA_CURRENT.tmp" && mv -Tf "$HA_CURRENT.tmp" "$HA_CURRENT"
  # Bounded for the same reason as restart_units: hotel-admin is also Restart=always with the start limiter
  # disabled, so a synchronous restart of a unit that will not come up would block here instead.
  restart_units stayconnect-hotel-admin
}

restore_settings() {
  # THE SANCTIONED AUDITED WRITER, per appliance, back to the captured value. The writer serializes on the
  # appliance and records the change; that is the whole point of it existing.
  local op t s a v
  op="$(q "SELECT id FROM public.operators ORDER BY created_at LIMIT 1")"
  [ -n "$op" ] || return 0
  while read -r t s a v; do
    [ -n "${t:-}" ] || continue
    q "SELECT iam_v2.p6_set_guest_device_self_service('$t','$s','$a', $v, '$op',
         'phase6-controlled-validation', 'restore captured pre-validation baseline')" >/dev/null
  done < "$STATE_DIR/settings" 2>/dev/null
  # A settings row this run created that was NOT in the baseline is removed, so the baseline is restored
  # exactly rather than approximately. The append-only CHANGE record of it stays: that is audit history, and
  # deleting it would be the one thing this script must never teach anyone to do.
  local key
  q "SELECT tenant_id||' '||site_id||' '||appliance_id FROM iam_v2.appliance_product_settings" 2>/dev/null \
  | while read -r key; do
      [ -n "${key:-}" ] || continue
      if ! awk '{print $1" "$2" "$3}' "$STATE_DIR/settings" 2>/dev/null | grep -qxF "$key"; then
        set -- $key
        q "DELETE FROM iam_v2.appliance_product_settings
            WHERE tenant_id='$1' AND site_id='$2' AND appliance_id='$3'" >/dev/null
      fi
    done
}

# A RATCHET, AND WHY RESTORING THE BASELINE IS NOT ENOUGH ON ITS OWN.
#
# Restoring exactly what was captured is the right instinct and it produced the wrong result: an earlier run
# ended with guest_device_self_service = true, so the NEXT run captured true as its baseline and faithfully
# put it back, and so did every run after that. The audit trail shows it plainly -- t -> f, f -> t, then
# "restore captured pre-validation baseline" writing t. Each run was individually correct and the appliance
# drifted anyway.
#
# While Phase 6 is dark the setting cannot legitimately be on: nothing reads it, so a true here is residue
# rather than an operator's decision -- and it is the dangerous kind, because the moment the deployment gate
# opens the capability would be live on this appliance without anyone choosing that. So the final state is
# pinned rather than merely restored, through the same audited writer, with a reason that says why. It is
# reported as a correction, not done quietly: if this fires, something left the appliance wrong.
enforce_dark_setting() {
  local on op t s a
  on="$(q "SELECT count(*) FROM iam_v2.appliance_product_settings WHERE guest_device_self_service")"
  [ "$on" = "0" ] && { ok "no appliance is left with the capability enabled"; return 0; }
  op="$(q "SELECT id FROM public.operators ORDER BY created_at LIMIT 1")"
  q "SELECT iam_v2.p6_set_guest_device_self_service(tenant_id, site_id, appliance_id, false, '$op',
       'phase6-controlled-validation',
       'Phase 6 is dark on this appliance; the per-appliance capability must not be left enabled')
       FROM iam_v2.appliance_product_settings WHERE guest_device_self_service" >/dev/null
  ok "corrected $on appliance setting(s) left enabled, through the audited writer"
}

teardown_scope() {
  # ITS OUTPUT IS READ. The first version discarded it, so when the DO block aborted on a column that does not
  # exist, teardown silently did nothing at all -- and the failure only surfaced a run later, as a unique-index
  # collision on a grant that should have been ended. A cleanup nobody checked is a wish.
  TEARDOWN_OUT=""
  if [ -f "$VALIDATION_DIR/phase6-validation-teardown.sql" ]; then
    TEARDOWN_OUT="$(docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -v ON_ERROR_STOP=1 -q \
      < "$VALIDATION_DIR/phase6-validation-teardown.sql" 2>&1)"
  fi
  # Belt and braces, and the reason is idempotence from a cold start: if the SQL file is not staged -- a run
  # that died before staging it, or a restore invoked on its own -- the reserved identities must still be made
  # inert. Same statements, same reserved ids, no parameters needed.
  q "DO \$\$
     DECLARE r record;
     BEGIN
       FOR r IN SELECT id FROM iam_v2.entitlements WHERE stay_id='$SYN_STAY' AND status <> 'TERMINATED'
       LOOP PERFORM iam_v2.terminate_entitlement_at_boundary(r.id, now(), 'ADMIN'); END LOOP;
       UPDATE iam_v2.sessions SET state='ended', ended=COALESCE(ended,now()),
              end_reason=COALESCE(end_reason,'ADMIN')
        WHERE entitlement_id IN (SELECT id FROM iam_v2.entitlements WHERE stay_id='$SYN_STAY')
          AND state IN ('active','PENDING_ENFORCEMENT');
       UPDATE iam_v2.entitlement_devices SET status='DISCONNECTED',
              disconnected_reason=COALESCE(disconnected_reason,'ADMIN')
        WHERE entitlement_id IN (SELECT id FROM iam_v2.entitlements WHERE stay_id='$SYN_STAY')
          AND status='AUTHORIZED';
     END \$\$;" >/dev/null
}

# ---- the Phase-3 writer-boundary prerequisite ----------------------------------------------------------------
# scd verifies at startup that it can OPEN a controlled operation and refuses to serve the Phase-3 auth surface
# if it cannot: "no EXECUTE on iam_v2.begin_controlled_operation(text), so every authoritative write in that
# family would be refused at the first attempt". On this appliance svc_scd has never held that grant, because
# the Phase-3 auth arm has never been enabled here. Validating the Phase-6 guest surface means the prerequisite
# has to be real, so it is granted for the duration and revoked again -- recorded like everything else, so
# restoration puts back what was actually there rather than what it assumes.
# BOTH RUNTIME ROLES, because both check it at startup. acctd refuses for the same reason scd does -- "acctd:
# refusing to start ... no EXECUTE on iam_v2.begin_controlled_operation(text)" -- and an appliance running the
# aggregate capability with its accounting daemon dead is precisely the state in which a finite budget becomes
# unlimited. Granting one and not the other would have produced exactly that, quietly.
PREREQ_ROLES="svc_scd svc_acctd"

capture_writer_prereq() {
  local r v
  : > "$STATE_DIR/writer_prereq"
  HAD_PREREQ=""
  for r in $PREREQ_ROLES; do
    v="$(q "SELECT has_function_privilege('$r','iam_v2.begin_controlled_operation(text)','EXECUTE')")"
    printf '%s %s
' "$r" "$v" >> "$STATE_DIR/writer_prereq"
    HAD_PREREQ="$HAD_PREREQ $r=$v"
  done
}

grant_writer_prereq() {
  local r v
  while read -r r v; do
    [ -n "${r:-}" ] || continue
    [ "$v" = "f" ] || continue
    q "GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) TO $r" >/dev/null
  done < "$STATE_DIR/writer_prereq"
}

restore_writer_prereq() {
  local r v
  while read -r r v; do
    [ -n "${r:-}" ] || continue
    [ "$v" = "f" ] || continue
    q "REVOKE EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) FROM $r" >/dev/null
  done < "$STATE_DIR/writer_prereq" 2>/dev/null
}

restore_network() {
  # The local-first proof blackholes the Central address. If the run dies while that route is in place the
  # appliance loses its uplink silently, which is a worse outcome than any test failure.
  [ -n "${BLACKHOLED:-}" ] || return 0
  ip route del blackhole "$BLACKHOLED/32" 2>/dev/null
  BLACKHOLED=""
}

restore() {
  say "restoration (runs on success, failure and interruption)"
  restore_network;  ok "no blackhole route left behind by the local-first proof"
  restore_writer_prereq; ok "the Phase-3 writer-boundary grant is back to its captured value"
  restore_flags;    ok "Phase-6 flag files restored to the captured baseline; services restarted"
  restore_hotel_admin; ok "Hotel Admin pointed at the captured DARK release"
  restore_settings; ok "per-appliance product settings restored through the audited writer"
  enforce_dark_setting
  teardown_scope
  case "$TEARDOWN_OUT" in
    *P6_SCOPE_INERT*) ok "the reserved validation scope is inert (terminated through the boundary path)" ;;
    *) no "teardown did not reach an inert scope" "$(printf '%s' "$TEARDOWN_OUT" | head -2 | tr '
' ' ')" ;;
  esac
}

# ---- verification: the RUNTIME, not the files this script wrote ------------------------------------------------
verify_dark() {
  local bad=0
  say "verification of the restored state"

  if [ -f "$COHERENCE" ]; then
    if bash "$COHERENCE" >/dev/null 2>&1; then
      ok "the authoritative flag-coherence gate passes (every Phase-6 flag off and agreeing across services)"
    else
      no "flag coherence" "the authoritative gate reports a problem; run $COHERENCE for detail"; bad=1
    fi
  else
    no "flag coherence" "the authoritative gate is missing at $COHERENCE"; bad=1
  fi

  # THE ROUTE ITSELF. A flag file records an intention; this records what the running process will answer.
  # It goes through route_code rather than a second copy of the same curl -- the duplicate is how one of the
  # two probes kept the bug after the other was fixed.
  wait_for_scd || no "scd is not answering at all" "darkness cannot be established from silence"
  local code; code="$(route_code /v1/phase6/devices/list)"
  case "$code" in
    404) ok "the guest device route is ABSENT in the running scd (404)" ;;
    *)   no "the guest device route answered $code" "while dark it must not exist"; bad=1 ;;
  esac

  # THE ACCOUNTING-OWNER INVARIANT. Accrual is data-driven precisely so a flag cannot stop it, but it still
  # needs a process to run in: an appliance holding aggregate entitlements with no accounting owner is exactly
  # the state in which a finite budget becomes unlimited.
  if systemctl is-active --quiet stayconnect-acctd; then
    if journalctl -u stayconnect-acctd --since '-10min' 2>/dev/null | grep -q 'phase6_fallback_accounting'; then
      ok "the accounting owner is active and reports the Phase-6 fallback owner"
    else
      ok "the accounting owner (acctd) is active"
    fi
  else
    no "acctd is not active" "aggregate budgets would have no owner"; bad=1
  fi

  local u down=""
  for u in $ALL_UNITS; do systemctl is-active --quiet "$u" || down="$down $u"; done
  [ -z "$down" ] && ok "every required service is active" || { no "services not active" "$down"; bad=1; }

  local want now
  want="$(cat "$STATE_DIR/ha_release" 2>/dev/null || true)"
  now="$(readlink -f "$HA_CURRENT" 2>/dev/null || true)"
  if [ -z "$want" ]; then
    ok "no captured Hotel Admin baseline to compare against (restore-only run)"
  elif [ "$want" != "$now" ]; then
    no "Hotel Admin release" "current $now, baseline $want"; bad=1
  elif [ "$(ha_hash)" != "$(cat "$STATE_DIR/ha_hash")" ]; then
    no "Hotel Admin content" "the release path matches but its contents changed"; bad=1
  else
    ok "the exact DARK Hotel Admin release is current, by path and by content hash"
  fi

  local live
  live="$(q "SELECT
      (SELECT count(*) FROM iam_v2.entitlements WHERE stay_id='$SYN_STAY' AND status <> 'TERMINATED')
    + (SELECT count(*) FROM iam_v2.sessions
        WHERE entitlement_id IN (SELECT id FROM iam_v2.entitlements WHERE stay_id='$SYN_STAY')
          AND state IN ('active','PENDING_ENFORCEMENT'))
    + (SELECT count(*) FROM iam_v2.entitlement_devices
        WHERE entitlement_id IN (SELECT id FROM iam_v2.entitlements WHERE stay_id='$SYN_STAY')
          AND status='AUTHORIZED')")"
  [ "$live" = "0" ] && ok "no live synthetic entitlement, session or device binding remains at the reserved ids" \
    || { no "synthetic state is still live" "$live row(s)"; bad=1; }

  # THE SETTINGS TABLE: the same appliances as before the run, and none of them enabled.
  #
  # Row-for-row equality with the capture was the earlier check, and it is what let the ratchet through: it
  # passed happily while the appliance sat enabled, because enabled was what had been captured. The two
  # things actually worth asserting are that this run created no settings row of its own and left none
  # behind, and that nothing is switched on while the phase is dark.
  local nowkeys basekeys onnow
  nowkeys="$(q "SELECT tenant_id||' '||site_id||' '||appliance_id FROM iam_v2.appliance_product_settings
                 ORDER BY tenant_id, site_id, appliance_id")"
  basekeys="$(awk '{print $1" "$2" "$3}' "$STATE_DIR/settings" 2>/dev/null)"
  if [ "$nowkeys" = "$basekeys" ]; then
    ok "the settings table covers exactly the appliances it did before the run"
  else
    no "the settings table gained or lost a row" "$(printf '%s' "$nowkeys" | tr '\n' ';')"; bad=1
  fi
  onnow="$(q "SELECT count(*) FROM iam_v2.appliance_product_settings WHERE guest_device_self_service")"
  [ "$onnow" = "0" ] \
    && ok "no appliance is left with guest device self-service enabled" \
    || { no "an appliance is left with the capability enabled" "$onnow row(s)"; bad=1; }

  return $bad
}

finish() {
  local rc=$?
  restore
  local vrc=0
  verify_dark || vrc=1
  echo "------------------------------------------------------------"
  if [ "$vrc" = "0" ] && [ "$fail" -eq 0 ]; then
    printf 'PHASE6_CONTROLLED_VALIDATION pass=%d fail=%d -- appliance verified DARK and restored (exit %d)\n' \
      "$pass" "$fail" "$rc"
    exit "$rc"
  fi
  printf 'PHASE6_CONTROLLED_VALIDATION pass=%d fail=%d -- APPLIANCE MAY BE PARTIALLY ENABLED, INSPECT IT\n' \
    "$pass" "$fail"
  exit 1
}

# ---- enabling -------------------------------------------------------------------------------------------------
# restart_units -- STOP, THEN START WITHOUT BLOCKING, BOTH BOUNDED.
#
# `systemctl restart` NEVER RETURNS on this appliance when the configuration is one the service refuses.
# MEASURED ON PRE-LIVE: a controlled run sat in do_wait on
#
#     systemctl restart stayconnect-scd stayconnect-acctd stayconnect-edged
#
# for 3 hours 12 minutes during the step that DELIBERATELY configures a refusal, and it left edged and acctd
# down for all of it. The cause is in the unit and it is intentional there:
#
#     Restart=always   RestartSec=2   StartLimitIntervalSec=0
#
# With the start limiter disabled, systemd retries the refused start forever, the restart job never settles
# into active or failed, and a synchronous restart waits for a job that cannot finish. The appliance
# disables the limiter on purpose, so that a transient database outage cannot leave a service permanently
# down -- that policy is right and is not what changes here.
#
# This harness's own comment shows what it assumed instead: "systemd's start limiter then remembers those
# rapid failures: the NEXT restart is refused outright with 'start request repeated too quickly'". That is
# the behaviour of an appliance WITH the limiter, which the retired development appliance had and PRE-LIVE
# does not. Admitting PRE-LIVE is what surfaced it.
#
# So: stop first (which completes even on a looping unit, because it cancels the loop), clear the counter,
# then start WITHOUT blocking and let the callers settle on evidence -- wait_for_scd for the route, and the
# bounded settle below for the units. A step that expects a refusal now observes it in a bounded time
# instead of hanging on it.
restart_units() {   # restart_units [unit ...]  -- defaults to $UNITS
  local set_="${*:-$UNITS}"
  timeout 45 systemctl stop $set_ >/dev/null 2>&1
  systemctl reset-failed $set_ >/dev/null 2>&1
  timeout 45 systemctl start --no-block $set_ >/dev/null 2>&1
}

# settle_units waits, bounded, for every named unit to be active. It REPORTS rather than judges: one step
# deliberately produces a configuration scd must refuse, and a helper that called that a failure would turn
# the fail-closed proof upside down.
settle_units() {    # settle_units <seconds> [unit ...]
  local budget="$1"; shift
  local set_="${*:-$UNITS}" i=0 u all
  while [ "$i" -lt "$budget" ]; do
    all=1
    for u in $set_; do
      [ "$(systemctl is-active "$u" 2>/dev/null)" = "active" ] || all=0
    done
    [ "$all" = "1" ] && return 0
    i=$((i+1)); sleep 1
  done
  return 1
}

set_flags() {   # set_flags FLAG [FLAG ...]  -- exactly this set, on every unit; anything else removed
  # EVERY TOKEN IS CHECKED BEFORE IT IS WRITTEN. Not defensive habit: the first version of the caller wrapped
  # its flag list across two lines with a backslash INSIDE double quotes, where a backslash-newline is not a
  # continuation but two literal characters. The lone "\" was then word-split into its own token and written
  # as the line `\=true`, systemd refused the file, and scd did not come back. A flag file nobody can parse
  # must never be produced by the script whose job is proving the flags are off.
  #
  # STAYCONNECT_PHASE3_MASTER and STAYCONNECT_PHASE3_PMS_AUTH are accepted too, and nothing else is. The
  # Phase-6 guest surface has a prerequisite the appliance itself enforces -- scd refuses to start with the
  # guest child on while the Phase-3 auth arm is off, which is fail-closed and correct -- so validating the
  # Phase-6 surface means turning that accepted, already-merged arm on for the duration. It comes back with
  # everything else, because restoration copies whole env files rather than undoing a list of names somebody
  # remembered to write down.
  local u f n
  for n in "$@"; do
    case "$n" in
      STAYCONNECT_PHASE6_[A-Z_]*|STAYCONNECT_PHASE3_MASTER|STAYCONNECT_PHASE3_PMS_AUTH) : ;;
      *) no "refusing to write an unrecognised flag token" "'$n'"; return 1 ;;
    esac
  done
  for u in $UNITS; do
    f="$ENV_DIR/${u#stayconnect-}.env"
    sed -i '/^STAYCONNECT_PHASE6_/d; /^STAYCONNECT_PHASE3_MASTER=/d; /^STAYCONNECT_PHASE3_PMS_AUTH=/d' "$f"
    for n in "$@"; do printf '%s=true\n' "$n" >> "$f"; done
  done
  # The restart is bounded and non-blocking: see restart_units, which exists because the synchronous form
  # hung for three hours on PRE-LIVE during the very step below that deliberately configures a refusal.
  # Clearing the failure counter is part of it, for the reason this comment used to give on its own -- a
  # remembered burst of rapid failures would otherwise make a LATER restart refuse outright, and every step
  # after it would fail for a reason that has nothing to do with the product.
  restart_units
  # It REPORTS rather than judges. One step deliberately configures a combination the appliance must refuse to
  # start on, and a helper that recorded that refusal as a failure would turn the fail-closed proof upside
  # down. The callers that need scd up notice through the route they then ask for.
  wait_for_scd || return 1
  return 0
}

setting_on() {  # setting_on <true|false> -- the REAL appliance, through the audited writer
  local val="$1" op row
  op="$(q "SELECT id FROM public.operators ORDER BY created_at LIMIT 1")"
  row="$(q "SELECT a.tenant_id||' '||a.site_id||' '||a.id FROM public.appliances a ORDER BY a.id LIMIT 1")"
  set -- $row
  q "SELECT iam_v2.p6_set_guest_device_self_service('$1','$2','$3', $val, '$op',
       'phase6-controlled-validation', 'controlled validation under D25')" >/dev/null
}

# appliance_identity fills TEN/SITE/APPL and GIP from the appliance's own tables. Nothing is typed in: the
# fixture has to live under the identity scd actually resolves against, because scd creates every device row as
# (its own tenant, its own site, its own appliance) and refuses any address that is not on a mapped guest
# network. A fixture in a tenant of its own would be unreachable through the real guest route, and the
# validation would then be proving that the handler works against a fixture.
appliance_identity() {
  local row
  row="$(q "SELECT a.tenant_id||' '||a.site_id||' '||a.id FROM public.appliances a ORDER BY a.id LIMIT 1")"
  set -- $row
  TEN="${1:-}"; SITE="${2:-}"; APPL="${3:-}"
  GIP="$(q "SELECT host(network(subnet_cidr) + 100) FROM guest_networks
             WHERE enabled ORDER BY masklen(subnet_cidr) DESC LIMIT 1")"
  [ -n "$TEN" ] && [ -n "$APPL" ] && [ -n "$GIP" ]
}

seed_scope() {
  # Teardown FIRST, from the host side. The scope file cannot include it: psql executes inside the container,
  # where a host path does not exist.
  teardown_scope
  docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -v ON_ERROR_STOP=1 -q \
    -v ten="$TEN" -v site="$SITE" -v appl="$APPL" -v gip="$GIP" \
    < "$VALIDATION_DIR/phase6-validation-scope.sql" 2>&1
}

route_code() {  # route_code <path> -> ONE HTTP status from the running scd
  # `curl ... || echo 000` prints BOTH curl's own "000" and the fallback, and the caller then compares
  # "000000" against "404" and reports a failure that never happened. One value, whatever occurs.
  local c
  c="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 --unix-socket "$SCD_SOCK" \
        -X POST "http://localhost$1" -H 'Content-Type: application/json' -d '{}' 2>/dev/null)"
  case "$c" in [1-5][0-9][0-9]) printf '%s' "$c" ;; *) printf '000' ;; esac
}

# wait_for_scd blocks until the restarted scd answers at all. A service takes a moment to bind its socket, and
# a probe fired into that gap answers 000 -- which is not a proof of darkness, it is an absence of evidence.
# Reading it as "the route is gone" would let a genuinely-enabled appliance pass the final check.
wait_for_scd() {
  local i=0
  while [ "$i" -lt 60 ]; do
    [ "$(route_code /v1/phase6/devices/list)" != "000" ] && return 0
    i=$((i+1)); sleep 1
  done
  return 1
}

guard_environment

case "${1:-run}" in
  restore)
    say "restore-only run on $(hostname)"
    trap finish EXIT INT TERM
    exit 0
    ;;

  selftest)
    # FAULT INJECTION, RUN BEFORE THE REAL BODY IS EVER TRUSTED. Each case enables a capability and then
    # abandons the run in a different way. The run is judged only by whether the appliance comes back dark --
    # which is the property the real validation depends on and the one that cannot be established by reading
    # the code and hoping.
    say "restoration self-test: '${2:-body-failure}' (a flag IS enabled and then abandoned, on purpose)"
    capture_baseline
    case "${2:-body-failure}" in
      body-failure)
        trap finish EXIT INT TERM
        set_flags STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME
        ok "capability enabled; the body is about to fail"
        false; exit 1 ;;
      signal)
        trap finish EXIT INT TERM
        set_flags STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME
        ok "capability enabled; this run is about to be signalled"
        kill -TERM $$; sleep 10; exit 1 ;;
      partial)
        trap finish EXIT INT TERM
        set_flags STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST \
                  STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_ADMIN STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME
        setting_on true
        appliance_identity && seed_scope >/dev/null 2>&1
        ok "every flag on, the product setting on, and synthetic state seeded; abandoning mid-way"
        exit 3 ;;
      partial-device-selfservice)
        # THE SAME ABANDONMENT AS `partial`, WITHIN THE AUTHORISED CAPABILITY. The existing injection cases
        # enable STAYCONNECT_PHASE6_AGGREGATE_ONLINE_TIME, which the PRE-LIVE authorisation does not cover, so
        # proving restoration on this appliance needed a case that turns on only what was authorised: the
        # master gate, the Guest Device Self-Service children, and the Phase-3 arm the surface fails closed
        # without. It then abandons the run with the capability on, the product setting on and synthetic state
        # seeded -- which is the state restoration actually has to recover from.
        trap finish EXIT INT TERM
        set_flags STAYCONNECT_PHASE3_MASTER STAYCONNECT_PHASE3_PMS_AUTH                   STAYCONNECT_PHASE6_MASTER STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_GUEST                   STAYCONNECT_PHASE6_DEVICE_SELFSERVICE_ADMIN
        grant_writer_prereq
        setting_on true
        appliance_identity && seed_scope >/dev/null 2>&1
        ok "device self-service enabled, the setting on and synthetic state seeded; abandoning mid-way"
        exit 3 ;;

      double-restore)
        # Idempotence: restoring an already-restored appliance must be a no-op that still verifies.
        trap finish EXIT INT TERM
        restore; ok "a first restoration ran; the trap is about to run a second one"
        exit 0 ;;
      *) echo "unknown selftest case '${2:-}'" >&2; exit 2 ;;
    esac
    ;;

  run|run-device-selfservice)
    # SCOPE IS ASSIGNED HERE, from the positional mode, unconditionally -- so an exported P6_SCOPE cannot
    # choose one. `run` is the full validation; `run-device-selfservice` stops at the boundary of what the
    # Product Owner authorised for the PRE-LIVE acceptance run, which is the Guest Device Self-Service
    # capability and nothing else. Sections 0-4 and 7 run either way; section 5 enables a DIFFERENT Phase-6
    # child (aggregate online time) and is skipped in the narrow scope.
    case "${1:-run}" in
      run-device-selfservice) P6_SCOPE=device-selfservice ;;
      *)                      P6_SCOPE=full ;;
    esac
    say "Phase-6 controlled validation on $(hostname) [scope: $P6_SCOPE]"
    trap finish EXIT INT TERM
    capture_baseline
    # SOURCED, not executed: the body must run inside this shell so that its failures, its counters and any
    # signal that reaches it all land in the trap above. A child process would take its own exit status with
    # it and leave the flags on.
    . "$(dirname "$0")/phase6-controlled-validation-body.sh"
    ;;

  *) echo "usage: $0 [run|run-device-selfservice|restore|selftest CASE]" >&2; exit 2 ;;
esac
