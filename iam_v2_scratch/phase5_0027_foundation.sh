#!/usr/bin/env bash
# PHASE-5 MILESTONE 1 — FOUNDATION + SECURITY DB GATE.
#
# 0027 turns three tables that were only SHAPED like Post-Stay identity and typed transfer into tables that
# enforce it. A migration that applies cleanly proves nothing about that: the interesting question is what the
# database now REFUSES. Almost every assertion below is therefore a negative one, run against a real
# PostgreSQL with the authoritative chain built underneath it.
#
# It also re-proves the Phase-3 lifecycle rules, because 0027 replaces p3_stay_lifecycle_guard to add the
# post-stay arm the Phase-3 guard itself names. An added arm that quietly relaxed an existing one would be the
# worst possible outcome of this phase, so every neighbouring transition is asserted again here.
#
# EXIT: 0 all assertions passed, 1 an assertion failed, 2 the environment was not usable.
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C="${PHASE5_CONTAINER:-iamv2-p5}"; DB="${PHASE5_DB:-iam_scratch_p5}"
UP="$ROOT/data-plane/migrations/0027_phase5_poststay_and_transfer.up.sql"
DOWN="$ROOT/data-plane/migrations/0027_phase5_poststay_and_transfer.down.sql"

pass=0; fail=0
ok(){ printf '  [PASS] %s\n' "$1"; pass=$((pass+1)); }
no(){ printf '  [FAIL] %s :: %s\n' "$1" "${2:-}"; fail=$((fail+1)); }
Q(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
AS(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "SET ROLE $1; $2" 2>&1; }

# refused <label> <sql> <expected-substring-of-the-error>
refused(){
  local out; out="$(Q "$2")"
  if [[ "$out" == *ERROR* || "$out" == *denied* ]]; then
    if [[ "$out" == *"$3"* ]]; then ok "$1"
    else no "$1" "refused, but not for the stated reason: $(head -1 <<<"$out")"; fi
  else no "$1" "the statement was ACCEPTED"; fi
}
# accepted <label> <sql>
accepted(){
  local out; out="$(Q "$2")"
  if [[ "$out" == *ERROR* ]]; then no "$1" "$(head -1 <<<"$out")"; else ok "$1"; fi
}
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "expected '$2' got '$3'"; }

docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 \
  || { echo "INFRA: container $C / db $DB is not usable"; exit 2; }
[ "$(Q "SELECT count(*) FROM public.schema_migrations WHERE version='0027_phase5_poststay_and_transfer';")" = "1" ] \
  || { echo "INFRA: 0027 is not applied to $DB"; exit 2; }

echo "===== PHASE-5 MILESTONE 1 — FOUNDATION + SECURITY (db=$DB) ====="

# ---------------------------------------------------------------------------------------------------------
# RESET. This gate MUTATES: it checks a Stay out, reinstates it, converts it to POST_STAY_ACTIVE and records
# transfers. A second run against the leftovers of the first measures a different database, and the failures
# it then reports are its own history rather than the code under test. So it starts by destroying its own
# fixture, and asserts that it actually went.
#
# The profile guard refuses DELETE by design, which is correct for the product and wrong for a disposable
# scratch reset, so the Phase-5 triggers are disabled for the duration of the wipe. That is the ONLY thing
# this block does, it runs as the schema owner on a disposable database, and every assertion afterwards runs
# with the triggers back on.
# ---------------------------------------------------------------------------------------------------------
# Per-table DISABLE TRIGGER cannot do this: deleting an entitlement CASCADES into its append-only history,
# whose guard then refuses the cascade, and ALTER TABLE is itself rejected while those trigger events are
# pending. session_replication_role = replica is the mechanism this project already uses for exactly that
# situation (the tenant hard-purge does the same), and it is scoped to this one statement batch.
Q "SET session_replication_role = replica;
   DELETE FROM iam_v2.entitlement_transfers;
   DELETE FROM iam_v2.stay_links;
   DELETE FROM iam_v2.post_stay_profiles;
   -- replica mode disables FK triggers as well as user ones, so ON DELETE CASCADE does NOT fire and the
   -- entitlement history would be left behind as orphans. The next run's first transition then reports
   -- 'from_state PENDING must equal previous to_state TERMINATED' — the previous run's history talking. It is
   -- deleted explicitly, before its entitlements and again afterwards for anything already orphaned.
   DELETE FROM iam_v2.entitlement_state_transitions
     WHERE entitlement_id IN (SELECT id FROM iam_v2.entitlements WHERE stay_id IS NOT NULL);
   DELETE FROM iam_v2.entitlements WHERE stay_id IS NOT NULL;
   DELETE FROM iam_v2.entitlement_state_transitions est
     WHERE NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e WHERE e.id = est.entitlement_id);
   DELETE FROM iam_v2.purchases    WHERE trigger='ADMIN_GRANT';
   DELETE FROM iam_v2.stays        WHERE external_reservation_id IN ('RESP','RESX','RESY','RESZ','RESR');
   SET session_replication_role = origin;" >/dev/null
eq "the gate starts from a clean fixture" "0|0|0|0"    "$(Q "SELECT (SELECT count(*) FROM iam_v2.post_stay_profiles)||'|'||
                (SELECT count(*) FROM iam_v2.entitlement_transfers)||'|'||
                (SELECT count(*) FROM iam_v2.stay_links)||'|'||
                (SELECT count(*) FROM iam_v2.stays WHERE external_reservation_id IN ('RESP','RESX','RESY','RESZ','RESR'));")"

# ---------------------------------------------------------------------------------------------------------
# FIXTURE. A second PMS interface is the whole point of a cross-PMS transfer, so the fixture has two, with a
# Stay on each. Everything is created here rather than in the shared seed so this gate owns its own state.
# ---------------------------------------------------------------------------------------------------------
T=11111111-1111-1111-1111-111111111111
S=22222222-2222-2222-2222-222222222222
IF_A=aaaa0000-0000-0000-0000-000000000001
IF_B=aaaa0000-0000-0000-0000-0000000000bb
Q "
INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind)
  VALUES ('$IF_B','$T','$S','protel-fias') ON CONFLICT DO NOTHING;
INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,config)
  VALUES ('aaaa0000-0000-0000-0000-0000000000b1','$T','$S','$IF_B',1,'UTC','UNSET','{}') ON CONFLICT DO NOTHING;
-- Stay P: the post-stay subject. Stay X/Y: the transfer pair, one per interface.
INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,status)
  VALUES ('eeee0000-0000-0000-0000-0000000000a1','$T','$S','$IF_A','RESP','SP','IN_HOUSE'),
         ('eeee0000-0000-0000-0000-0000000000a2','$T','$S','$IF_A','RESX','SX','IN_HOUSE'),
         ('eeee0000-0000-0000-0000-0000000000a3','$T','$S','$IF_B','RESY','SY','IN_HOUSE'),
         ('eeee0000-0000-0000-0000-0000000000a4','$T','$S','$IF_A','RESZ','SZ','RESERVED'),
         ('eeee0000-0000-0000-0000-0000000000a5','$T','$S','$IF_A','RESR','SR','RESERVED')
  ON CONFLICT DO NOTHING;" >/dev/null
STAY_P=eeee0000-0000-0000-0000-0000000000a1
STAY_X=eeee0000-0000-0000-0000-0000000000a2
STAY_Y=eeee0000-0000-0000-0000-0000000000a3
STAY_Z=eeee0000-0000-0000-0000-0000000000a4
STAY_R=eeee0000-0000-0000-0000-0000000000a5
HASH='$argon2id$v=19$m=65536,t=1,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNo'

echo "== A. Post-Stay identity: the PIN column accepts exactly one shape =="
refused "a raw PIN cannot be stored where a hash belongs" \
  "INSERT INTO iam_v2.post_stay_profiles(tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
     VALUES ('$T','$S','$STAY_P',1,'4821', now()+interval '1 day','GUEST_AUTHENTICATED_SESSION', now());" \
  "post_stay_profiles_pin_hash_check"
refused "a bcrypt/sha hash is not an argon2id hash" \
  "INSERT INTO iam_v2.post_stay_profiles(tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
     VALUES ('$T','$S','$STAY_P',1,'\$2b\$12\$abcdefghijklmnopqrstuv', now()+interval '1 day','GUEST_AUTHENTICATED_SESSION', now());" \
  "post_stay_profiles_pin_hash_check"

echo "== B. Issuance is bound to the CURRENT episode of a real, occupied Stay =="
refused "a profile naming a stale episode is refused (I-1/I-2)" \
  "INSERT INTO iam_v2.post_stay_profiles(tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
     VALUES ('$T','$S','$STAY_P',7,'$HASH', now()+interval '1 day','GUEST_AUTHENTICATED_SESSION', now());" \
  "CURRENT Stay episode"
Q "UPDATE iam_v2.stays SET status='CANCELLED' WHERE id='$STAY_Z';" >/dev/null
refused "a profile issued from a CANCELLED Stay is refused (F8-c)" \
  "INSERT INTO iam_v2.post_stay_profiles(tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
     VALUES ('$T','$S','$STAY_Z',1,'$HASH', now()+interval '1 day','GUEST_AUTHENTICATED_SESSION', now());" \
  "IN_HOUSE or CHECKED_OUT"
refused "a profile created WITHOUT its reveal is refused -- nobody could ever be shown that PIN (0029)" \
  "INSERT INTO iam_v2.post_stay_profiles(tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via)
     VALUES ('$T','$S','$STAY_P',1,'$HASH', now()+interval '1 day','GUEST_AUTHENTICATED_SESSION');" \
  "records its one-time reveal at mint"
refused "an operator-issued profile with no operator is refused" \
  "INSERT INTO iam_v2.post_stay_profiles(tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
     VALUES ('$T','$S','$STAY_P',1,'$HASH', now()+interval '1 day','OPERATOR_RESET', now());" \
  "psp_issuer_coherent"
accepted "a well-formed guest-authenticated issuance is accepted" \
  "INSERT INTO iam_v2.post_stay_profiles(id,tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
     VALUES ('7777aaaa-0000-0000-0000-000000000001','$T','$S','$STAY_P',1,'$HASH', now()+interval '1 day','GUEST_AUTHENTICATED_SESSION', now());"
PROF=7777aaaa-0000-0000-0000-000000000001
refused "a SECOND profile for the same episode is refused (F8-e)" \
  "INSERT INTO iam_v2.post_stay_profiles(tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
     VALUES ('$T','$S','$STAY_P',1,'$HASH', now()+interval '1 day','GUEST_AUTHENTICATED_SESSION', now());" \
  "post_stay_profiles_origin_stay_id_origin_lifecycle_version_key"

echo "== C. The profile is immutable where it must be =="
refused "origin lineage is read-only" \
  "UPDATE iam_v2.post_stay_profiles SET origin_stay_id='$STAY_X' WHERE id='$PROF';" \
  "read-only"
refused "the episode a profile names is read-only" \
  "UPDATE iam_v2.post_stay_profiles SET origin_lifecycle_version=2 WHERE id='$PROF';" \
  "read-only"
refused "a profile is never deleted" \
  "DELETE FROM iam_v2.post_stay_profiles WHERE id='$PROF';" \
  "never deleted"
refused "a new PIN without a generation bump is refused" \
  "UPDATE iam_v2.post_stay_profiles SET pin_hash='${HASH}Z' WHERE id='$PROF';" \
  "increment pin_generation"
refused "a generation bump with no new PIN is refused" \
  "UPDATE iam_v2.post_stay_profiles SET pin_generation=2 WHERE id='$PROF';" \
  "without a new PIN"

echo "== D. One reveal per generation, recorded at MINT (0029) =="
refused "a SECOND reveal of the same generation is refused (I-5)"   "UPDATE iam_v2.post_stay_profiles SET pin_revealed_at=now()+interval '1 second' WHERE id='$PROF';"   "revealed exactly once"
refused "a new generation that INHERITS the previous reveal is refused"   "UPDATE iam_v2.post_stay_profiles SET pin_hash='${HASH}Z', pin_generation=2, pin_set_at=now() WHERE id='$PROF';"   "records its own reveal"
refused "a new generation with NO reveal is refused"   "UPDATE iam_v2.post_stay_profiles SET pin_hash='${HASH}Z', pin_generation=2, pin_revealed_at=NULL WHERE id='$PROF';"   "records its own reveal"
accepted "re-issuing mints a new generation carrying its OWN reveal"   "UPDATE iam_v2.post_stay_profiles SET pin_hash='${HASH}Z', pin_generation=2, pin_revealed_at=now()+interval '1 second', pin_set_at=now() WHERE id='$PROF';"

echo "== E. Authenticability — invariant I-2, the next-occupant protection =="
eq "an IN_HOUSE origin is NOT yet authenticable (post-stay means after checkout)" "f"    "$(Q "SELECT iam_v2.p5_post_stay_authenticable('$T','$S','$PROF');")"
Q "UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id='$STAY_P';" >/dev/null
eq "after checkout, at the SAME episode, the profile authenticates" "t"    "$(Q "SELECT iam_v2.p5_post_stay_authenticable('$T','$S','$PROF');")"
Q "UPDATE iam_v2.stays SET status='IN_HOUSE', effective_checkout_at=NULL, lifecycle_version=2 WHERE id='$STAY_P';" >/dev/null
eq "the reinstatement really happened (episode 2)" "2|IN_HOUSE"    "$(Q "SELECT lifecycle_version||'|'||status FROM iam_v2.stays WHERE id='$STAY_P';")"
eq "REINSTATEMENT orphans the profile — the next episode cannot use it (F8-a/F8-b)" "f"    "$(Q "SELECT iam_v2.p5_post_stay_authenticable('$T','$S','$PROF');")"
Q "UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now() WHERE id='$STAY_P';" >/dev/null
eq "and it stays dead once the NEW episode checks out — this is the room's next occupant" "f"    "$(Q "SELECT iam_v2.p5_post_stay_authenticable('$T','$S','$PROF');")"

PROF2=7777aaaa-0000-0000-0000-000000000002
accepted "the new episode may mint its OWN profile"   "INSERT INTO iam_v2.post_stay_profiles(id,tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
     VALUES ('$PROF2','$T','$S','$STAY_P',2,'$HASH', now()+interval '1 day','GUEST_AUTHENTICATED_SESSION', now());"
eq "and THAT profile authenticates for THIS episode" "t"    "$(Q "SELECT iam_v2.p5_post_stay_authenticable('$T','$S','$PROF2');")"
eq "while the previous occupant's profile still does not" "f"    "$(Q "SELECT iam_v2.p5_post_stay_authenticable('$T','$S','$PROF');")"
Q "UPDATE iam_v2.post_stay_profiles SET status='REVOKED', revoked_at=now(), revoke_reason='test' WHERE id='$PROF2';" >/dev/null
eq "a REVOKED profile does not authenticate" "f" "$(Q "SELECT iam_v2.p5_post_stay_authenticable('$T','$S','$PROF2');")"
refused "a revoked profile is never reactivated"   "UPDATE iam_v2.post_stay_profiles SET status='ACTIVE', revoked_at=NULL, revoke_reason=NULL WHERE id='$PROF2';"   "never reactivated"
refused "REVOKED without a reason is not a revocation"   "UPDATE iam_v2.post_stay_profiles SET status='REVOKED', revoked_at=now() WHERE id='$PROF';"   "psp_revoked_coherent"
Q "UPDATE iam_v2.post_stay_profiles SET valid_until=now()-interval '1 second' WHERE id='$PROF';" >/dev/null
eq "an EXPIRED profile does not authenticate" "f" "$(Q "SELECT iam_v2.p5_post_stay_authenticable('$T','$S','$PROF');")"

echo "== F. The Stay lifecycle arm this phase adds — and every arm it must NOT have relaxed =="
accepted "CHECKED_OUT -> POST_STAY_ACTIVE is now legal" \
  "UPDATE iam_v2.stays SET status='POST_STAY_ACTIVE' WHERE id='$STAY_P';"
eq "the post-stay conversion stays INSIDE the episode" "2" \
   "$(Q "SELECT lifecycle_version FROM iam_v2.stays WHERE id='$STAY_P';")"
eq "and keeps the immutable checkout boundary" "t" \
   "$(Q "SELECT effective_checkout_at IS NOT NULL FROM iam_v2.stays WHERE id='$STAY_P';")"
eq "a POST_STAY_ACTIVE Stay can never post" "f" \
   "$(Q "SELECT posting_allowed FROM iam_v2.stays WHERE id='$STAY_P';")"
refused "POST_STAY_ACTIVE has no exit (the FINAL contract draws no arrow out)" \
  "UPDATE iam_v2.stays SET status='IN_HOUSE' WHERE id='$STAY_P';" \
  "illegal stays.status transition"
refused "IN_HOUSE -> POST_STAY_ACTIVE is still illegal (post-stay follows a checkout)" \
  "UPDATE iam_v2.stays SET status='POST_STAY_ACTIVE' WHERE id='$STAY_X';" \
  "illegal stays.status transition"
refused "REGRESSION: RESERVED -> CHECKED_OUT is still illegal" \
  "UPDATE iam_v2.stays SET status='CHECKED_OUT' WHERE id='$STAY_Z';" \
  "illegal stays.status transition"
refused "REGRESSION: lifecycle_version still cannot move outside a reinstatement" \
  "UPDATE iam_v2.stays SET lifecycle_version=5 WHERE id='$STAY_X';" \
  "increment by exactly 1 ONLY during"
refused "REGRESSION: a CHECKED_OUT Stay cannot exist without its boundary" \
  "UPDATE iam_v2.stays SET status='CHECKED_OUT' WHERE id='$STAY_X';" \
  "stays_checkedout_needs_boundary"
refused "REGRESSION: the evidence version still cannot decrease" \
  "UPDATE iam_v2.stays SET occupancy_evidence_version=-1 WHERE id='$STAY_X';" \
  "cannot decrease"

echo "== G. stay_links: grounded ends only =="
refused "stay_links(POST_STAY) is refused — post-stay has one real Stay" \
  "INSERT INTO iam_v2.stay_links(tenant_id,site_id,from_stay,to_stay,reason)
     VALUES ('$T','$S','$STAY_X','$STAY_Y','POST_STAY');" \
  "not written"
refused "a same-interface link is a room move, not a transfer" \
  "INSERT INTO iam_v2.stay_links(tenant_id,site_id,from_stay,to_stay,reason)
     VALUES ('$T','$S','$STAY_X','$STAY_Z','CROSS_PMS_TRANSFER');" \
  "DIFFERENT PMS interfaces"
accepted "a genuine cross-interface transfer link is accepted" \
  "INSERT INTO iam_v2.stay_links(tenant_id,site_id,from_stay,to_stay,reason)
     VALUES ('$T','$S','$STAY_X','$STAY_Y','CROSS_PMS_TRANSFER');"
refused "stay_links is append-only" \
  "UPDATE iam_v2.stay_links SET reason='CROSS_PMS_TRANSFER' WHERE from_stay='$STAY_X';" \
  "append-only"

echo "== H. entitlement_transfers: the invariants the comment claimed =="
# entitlements for the transfer pair. Two Phase-3 rules shape this fixture and neither can be worked around:
# entitlements.purchase_id is NOT NULL, and p3_entitlement_status_coherent refuses any status that its
# transition history does not back. So each entitlement is created PENDING — the one state with no history —
# and then moved through the APPROVED operation, exactly as the runtime will have to.
Q "INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
   VALUES ('99990000-0000-0000-0000-0000000000e1','$T','$S','cccc0000-0000-0000-0000-0000000000d1','$IF_A','$STAY_X','ADMIN_GRANT',0,'GRANTED'),
          ('99990000-0000-0000-0000-0000000000e2','$T','$S','cccc0000-0000-0000-0000-0000000000d1','$IF_B','$STAY_Y','ADMIN_GRANT',0,'GRANTED'),
          ('99990000-0000-0000-0000-0000000000e3','$T','$S','cccc0000-0000-0000-0000-0000000000d1','$IF_A','$STAY_Z','ADMIN_GRANT',0,'GRANTED')
   ON CONFLICT DO NOTHING;" >/dev/null
# p3_entitlement_status_coherent is DEFERRED, so the INSERT and its first transition must commit TOGETHER:
# even PENDING is refused at commit when no transition backs it. One Q call is one transaction.
mkent(){ # mkent <id> <stay> <iface> <purchase>
  Q "INSERT INTO iam_v2.entitlements(id,tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,
       service_plan_revision_id,package_revision_id,time_accounting_mode,end_mode,window_ends_at,status)
     VALUES ('$1','$T','$S','$2','$3','$4','{}','bbbb0000-0000-0000-0000-0000000000d1',
       'cccc0000-0000-0000-0000-0000000000d1','VALIDITY_WINDOW','VALIDITY_WINDOW', now()+interval '1 hour','PENDING');
     SELECT iam_v2.apply_entitlement_transition('$1','ACTIVE',now(),NULL);" >/dev/null
}
term(){ Q "SELECT iam_v2.apply_entitlement_transition('$1','TERMINATED',now(),'TRANSFERRED');" >/dev/null; }
E_Y0=12340000-0000-0000-0000-0000000000e4
Q "INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
   VALUES ('99990000-0000-0000-0000-0000000000e4','$T','$S','cccc0000-0000-0000-0000-0000000000d1','$IF_B','$STAY_Y','ADMIN_GRANT',0,'GRANTED')
   ON CONFLICT DO NOTHING;" >/dev/null
mkent "$E_Y0" "$STAY_Y" "$IF_B" 99990000-0000-0000-0000-0000000000e4
Q "SELECT iam_v2.apply_entitlement_transition('$E_Y0','TERMINATED',now(),'SUPERSEDED');" >/dev/null
mkent 12340000-0000-0000-0000-0000000000e1 "$STAY_X" "$IF_A" 99990000-0000-0000-0000-0000000000e1
mkent 12340000-0000-0000-0000-0000000000e2 "$STAY_Y" "$IF_B" 99990000-0000-0000-0000-0000000000e2
mkent 12340000-0000-0000-0000-0000000000e3 "$STAY_Z" "$IF_A" 99990000-0000-0000-0000-0000000000e3
term 12340000-0000-0000-0000-0000000000e1
term 12340000-0000-0000-0000-0000000000e3
eq "the transfer fixture really exists, in the states the cases assume" "TERMINATED|ACTIVE|TERMINATED"    "$(Q "SELECT string_agg(status, '|' ORDER BY id) FROM iam_v2.entitlements
         WHERE id IN ('12340000-0000-0000-0000-0000000000e1','12340000-0000-0000-0000-0000000000e2','12340000-0000-0000-0000-0000000000e3');")"
E_X=12340000-0000-0000-0000-0000000000e1
E_Y=12340000-0000-0000-0000-0000000000e2
E_Z=12340000-0000-0000-0000-0000000000e3
ACTOR=33333333-3333-3333-3333-333333333333

refused "a same-interface transfer is refused (F9-b — this is a room move)" \
  "INSERT INTO iam_v2.entitlement_transfers(tenant_id,site_id,from_entitlement_id,to_entitlement_id,from_stay_id,to_stay_id,actor)
     VALUES ('$T','$S','$E_X','$E_Z','$STAY_X','$STAY_Z','$ACTOR');" \
  "DIFFERENT PMS interfaces"
refused "a transfer whose source is still live is refused (F9 state coupling)" \
  "INSERT INTO iam_v2.entitlement_transfers(tenant_id,site_id,from_entitlement_id,to_entitlement_id,from_stay_id,to_stay_id,actor)
     VALUES ('$T','$S','$E_Y','$E_X','$STAY_Y','$STAY_X','$ACTOR');" \
  "TERMINATED with reason TRANSFERRED"
refused "a transfer with a mismatched entitlement/Stay pair is refused" \
  "INSERT INTO iam_v2.entitlement_transfers(tenant_id,site_id,from_entitlement_id,to_entitlement_id,from_stay_id,to_stay_id,actor)
     VALUES ('$T','$S','$E_X','$E_Y','$STAY_Z','$STAY_Y','$ACTOR');" \
  "does not belong to the source Stay"
refused "a non-CROSS_PMS_TRANSFER reason is refused" \
  "INSERT INTO iam_v2.entitlement_transfers(tenant_id,site_id,from_entitlement_id,to_entitlement_id,from_stay_id,to_stay_id,actor,reason)
     VALUES ('$T','$S','$E_X','$E_Y','$STAY_X','$STAY_Y','$ACTOR','POST_STAY');" \
  "CROSS_PMS_TRANSFER only"
# The supersedes pointer has to be a SAME-SUBJECT one to reach this guard at all: the Phase-1A engine already
# rejects a cross-subject supersession outright, so pointing E_Y at an entitlement on a DIFFERENT Stay never
# set the column and this case silently proved nothing. E_Y0 (created above, terminated as SUPERSEDED on the
# SAME Stay) is a legal supersession target — exactly the shape that must not ALSO be recorded as a transfer.
Q "UPDATE iam_v2.entitlements SET supersedes_entitlement_id='$E_Y0' WHERE id='$E_Y';" >/dev/null
eq "the supersedes pointer really is set (a same-subject supersession, which the engine allows)" "$E_Y0"    "$(Q "SELECT COALESCE(supersedes_entitlement_id::text,'') FROM iam_v2.entitlements WHERE id='$E_Y';")"
refused "an entitlement that is ALREADY a supersession cannot also be recorded as a transfer (F9-d, I-10)" \
  "INSERT INTO iam_v2.entitlement_transfers(tenant_id,site_id,from_entitlement_id,to_entitlement_id,from_stay_id,to_stay_id,actor)
     VALUES ('$T','$S','$E_X','$E_Y','$STAY_X','$STAY_Y','$ACTOR');" \
  "carries no supersedes_entitlement_id"
Q "UPDATE iam_v2.entitlements SET supersedes_entitlement_id=NULL WHERE id='$E_Y';" >/dev/null
accepted "a genuine typed transfer is accepted" \
  "INSERT INTO iam_v2.entitlement_transfers(tenant_id,site_id,from_entitlement_id,to_entitlement_id,from_stay_id,to_stay_id,actor)
     VALUES ('$T','$S','$E_X','$E_Y','$STAY_X','$STAY_Y','$ACTOR');"
refused "the SAME transfer twice is refused (F9-f idempotency, by the from/to uniqueness)" \
  "INSERT INTO iam_v2.entitlement_transfers(tenant_id,site_id,from_entitlement_id,to_entitlement_id,from_stay_id,to_stay_id,actor)
     VALUES ('$T','$S','$E_X','$E_Y','$STAY_X','$STAY_Y','$ACTOR');" \
  "entitlement_transfers_from_entitlement_id_key"
refused "entitlement_transfers is append-only" \
  "UPDATE iam_v2.entitlement_transfers SET actor='$ACTOR' WHERE from_entitlement_id='$E_X';" \
  "append-only"
refused "lineage cannot be erased" \
  "DELETE FROM iam_v2.entitlement_transfers WHERE from_entitlement_id='$E_X';" \
  "append-only"

echo "== I. Cycle-freedom (F9-e) =="
# Y -> X would close the loop X -> Y -> X. Y must first be terminated-as-transferred to get past the state
# coupling, so the ONLY thing left that can refuse it is the cycle walk.
Q "SELECT iam_v2.apply_entitlement_transition('$E_Y','TERMINATED',now(),'TRANSFERRED');" >/dev/null
eq "the cycle case starts from the state it claims (source terminated-as-transferred)" "TERMINATED|TRANSFERRED"    "$(Q "SELECT status||'|'||terminal_reason FROM iam_v2.entitlements WHERE id='$E_Y';")"
refused "a transfer that would close a cycle is refused" \
  "INSERT INTO iam_v2.entitlement_transfers(tenant_id,site_id,from_entitlement_id,to_entitlement_id,from_stay_id,to_stay_id,actor)
     VALUES ('$T','$S','$E_Y','$E_X','$STAY_Y','$STAY_X','$ACTOR');" \
  "cycle"

echo "== J. The controlled-writer boundary, as a NON-owner =="
Q "DROP ROLE IF EXISTS p5_raw_writer;" >/dev/null
Q "CREATE ROLE p5_raw_writer NOLOGIN;
   GRANT USAGE ON SCHEMA iam_v2 TO p5_raw_writer;
   GRANT SELECT, INSERT, UPDATE, DELETE ON iam_v2.post_stay_profiles, iam_v2.entitlement_transfers, iam_v2.stay_links TO p5_raw_writer;" >/dev/null
out="$(AS p5_raw_writer "INSERT INTO iam_v2.post_stay_profiles(tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
       VALUES ('$T','$S','$STAY_X',1,'$HASH', now()+interval '1 day','GUEST_AUTHENTICATED_SESSION', now());")"
[[ "$out" == *"require an open controlled operation"* ]] \
  && ok "a role with full table grants STILL cannot write a profile outside a declared operation" \
  || no "non-owner raw profile write is refused" "$(head -1 <<<"$out")"
out="$(AS p5_raw_writer "INSERT INTO iam_v2.entitlement_transfers(tenant_id,site_id,from_entitlement_id,to_entitlement_id,from_stay_id,to_stay_id,actor)
       VALUES ('$T','$S','$E_Z','$E_X','$STAY_Z','$STAY_X','$ACTOR');")"
[[ "$out" == *"require an open controlled operation"* ]] \
  && ok "nor a transfer" \
  || no "non-owner raw transfer write is refused" "$(head -1 <<<"$out")"
out="$(AS p5_raw_writer "DELETE FROM iam_v2.post_stay_profiles WHERE id='$PROF';")"
[[ "$out" == *"require an open controlled operation"* || "$out" == *"never deleted"* ]] \
  && ok "nor a deletion" || no "non-owner delete is refused" "$(head -1 <<<"$out")"
out="$(AS p5_raw_writer "SELECT iam_v2.p5_begin_controlled_operation('post_stay_identity');")"
[[ "$out" == *"permission denied"* ]] \
  && ok "and it cannot open the operation itself (EXECUTE is revoked from PUBLIC)" \
  || no "non-owner cannot open the scope" "$(head -1 <<<"$out")"
out="$(Q "SELECT iam_v2.p5_begin_controlled_operation('stay');")"
[[ "$out" == *"no approved Phase-5 capability-scoped controlled-writer family"* ]] \
  && ok "the Phase-5 opener refuses families that are not its own" \
  || no "opener family allowlist" "$(head -1 <<<"$out")"

echo "== K. Auth-context coherence for POST_STAY_PIN =="
refused "a POST_STAY_PIN context without a profile is refused" \
  "INSERT INTO iam_v2.auth_contexts(tenant_id,site_id,method,guest_account_id,device_id,guest_network_id,expires_at,pinned_lifecycle_version)
     VALUES ('$T','$S','POST_STAY_PIN',NULL,'44444444-4444-4444-4444-444444444444','55555555-5555-5555-5555-555555555555', now()+interval '10 minutes',1);" \
  "ac_"
refused "a POST_STAY_PIN context without a pinned episode is refused (I-2 at the context layer)" \
  "INSERT INTO iam_v2.auth_contexts(tenant_id,site_id,method,post_stay_profile_id,device_id,guest_network_id,expires_at)
     VALUES ('$T','$S','POST_STAY_PIN','$PROF','44444444-4444-4444-4444-444444444444','55555555-5555-5555-5555-555555555555', now()+interval '10 minutes');" \
  "ac_post_stay_pins"

echo "== L. Privilege posture: DARK =="
G="$(Q "SELECT count(*) FROM information_schema.role_table_grants
        WHERE table_schema='iam_v2' AND table_name IN ('post_stay_profiles','entitlement_transfers','stay_links')
          AND grantee NOT IN (current_user,'PUBLIC','p5_raw_writer');")"
eq "no service role holds any privilege on a Phase-5 table (dark)" "0" "$G"
eq "PUBLIC cannot execute the Phase-5 opener" "f" \
   "$(Q "SELECT has_function_privilege('public','iam_v2.p5_begin_controlled_operation(text)','EXECUTE');")"
eq "PUBLIC cannot execute the profile guard" "f" \
   "$(Q "SELECT has_function_privilege('public','iam_v2.p5_post_stay_profile_guard()','EXECUTE');")"
eq "PUBLIC cannot execute the transfer guard" "f" \
   "$(Q "SELECT has_function_privilege('public','iam_v2.p5_entitlement_transfer_guard()','EXECUTE');")"
eq "PUBLIC cannot execute the authenticability predicate" "f" \
   "$(Q "SELECT has_function_privilege('public','iam_v2.p5_post_stay_authenticable(uuid,uuid,uuid)','EXECUTE');")"
BAD="$(Q "SELECT COALESCE(string_agg(p.proname,','),'') FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname='iam_v2' AND p.proname LIKE 'p5\\_%'
            AND (p.proconfig IS NULL OR array_to_string(p.proconfig,',') NOT LIKE '%search_path=%');")"
eq "every Phase-5 function pins its search_path" "" "$BAD"

echo "===== RESULT PASS=$pass FAIL=$fail ====="
[ "$fail" -eq 0 ] || exit 1
exit 0
