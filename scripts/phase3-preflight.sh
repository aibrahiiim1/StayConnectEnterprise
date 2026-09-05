#!/usr/bin/env bash
# Phase-3 OFFLINE PREFLIGHT.
#
# Answers one question before anyone touches an appliance: is this build safe to deploy DARK?
#
# Everything here is local and offline. It contacts no appliance, no production database, no PMS and no
# network service. It refuses to run against anything but the repository it lives in, and it fails on the
# FIRST condition that would make a dark deployment unsafe rather than reporting a tidy list at the end.
#
# Usage: bash scripts/phase3-preflight.sh [--json]
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
JSON=0
[ "${1:-}" = "--json" ] && JSON=1

pass=0; fail=0
declare -a RESULTS
ok(){ RESULTS+=("PASS|$1"); pass=$((pass+1)); [ $JSON -eq 1 ] || echo "  [PASS] $1"; }
no(){ RESULTS+=("FAIL|$1"); fail=$((fail+1)); [ $JSON -eq 1 ] || echo "  [FAIL] $1"; }

[ $JSON -eq 1 ] || echo "== Phase-3 offline preflight (no appliance, no production DB, no PMS) =="

# ---------------------------------------------------------------- 1. the build itself
if (cd "$ROOT/data-plane" && go build ./... >/dev/null 2>&1); then
  ok "data-plane builds"
else
  no "data-plane does not build"
fi
# gofmt is checked over the PHASE-3 package set (the same list the Phase-3 CI enforces). Pre-existing
# formatting elsewhere in the repository is out of this preflight's scope and is deliberately not asserted
# here, so a Phase-3 go/no-go is never blocked or falsely reassured by unrelated code.
P3_PKGS="internal/pmsd internal/pms internal/stayengine internal/pmsresolve internal/authctx internal/grace internal/checkout internal/staygrant internal/enforce internal/shapeplan internal/writerguard cmd/pmsd cmd/edged cmd/portald cmd/acctd cmd/netd cmd/scd"
if (cd "$ROOT/data-plane" && [ -z "$(gofmt -l $P3_PKGS 2>/dev/null)" ]); then
  ok "Phase-3 Go sources are gofmt-clean"
else
  no "Phase-3 Go sources are not gofmt-clean: $(cd "$ROOT/data-plane" && gofmt -l $P3_PKGS | tr '
' ' ')"
fi
if (cd "$ROOT/data-plane" && go vet ./... >/dev/null 2>&1); then
  ok "go vet is clean"
else
  no "go vet reports problems"
fi

# ---------------------------------------------------------------- 2. the flags that keep it dark
# A deployed unit must ship with every Phase-3 flag OFF. The authoritative default lives in the Go config;
# this asserts the DEFAULT is off, not merely that someone remembered to unset the env.
if grep -q "func DefaultPMSConfig() PMSConfig { return PMSConfig{} }" "$ROOT/data-plane/internal/iamv2/pms_config.go"; then
  ok "Phase-3 flag defaults are OFF in code"
else
  no "Phase-3 flag defaults are not provably OFF"
fi
if grep -q "phase3 surface flag enabled while STAYCONNECT_PHASE3_MASTER is OFF" "$ROOT/data-plane/internal/iamv2/pms_config.go"; then
  ok "a surface flag without the master flag is a startup failure (loud, not silently off)"
else
  no "an incoherent flag set would not fail closed"
fi
# WHICH ADMIN SURFACES A DEPLOYMENT MAY ENABLE — decided by the contract, not by a blanket ban.
#
# This rule used to refuse ANY mention of NEXT_PUBLIC_PHASE3_ADMIN under deploy/, which was exactly right
# while Phase 3 was DARK and the PMS admin surface was not authorized to ship. Phase 3 is now
# ACCEPTED_AND_CLOSED and those surfaces are authorized and in operational use, so a blanket ban would now
# forbid the appliance from serving the UI it is required to serve — and, worse, the version of this rule that
# simply passed on "no mention anywhere" is what let a bundle ship with the flags MISSING.
#
# The question is therefore not "is the flag mentioned" but "is every admin flag a deployment sets one the
# capability contract authorizes". An unlisted flag is still refused; a listed one is the point.
_unauthorized=""
for _f in $(grep -RIoh "NEXT_PUBLIC_PHASE[0-9]_ADMIN" "$ROOT/deploy" 2>/dev/null | sort -u); do
  grep -qF -- "$_f" "$ROOT/hotel-admin/capability-contract.json" 2>/dev/null || _unauthorized="$_unauthorized $_f"
done
if [ -n "$_unauthorized" ]; then
  no "a deployment file enables an admin surface the capability contract does not authorize:$_unauthorized"
else
  ok "every admin surface referenced under deploy/ is one the capability contract authorizes"
fi

# ---------------------------------------------------------------- 3. the migration is reversible
UP="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql"
DOWN="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.down.sql"
if [ -f "$UP" ] && [ -f "$DOWN" ]; then
  ok "migration 0010 has both an up and a down script"
else
  no "migration 0010 is missing its up or down script"
fi
# every controlled function the up script creates must be dropped by the down script: a rollback that leaves
# executable functions behind is not a rollback.
missing=""
while read -r fn; do
  grep -q "DROP FUNCTION IF EXISTS iam_v2.$fn" "$DOWN" || missing="$missing $fn"
done < <(grep -oE "CREATE OR REPLACE FUNCTION iam_v2\.[a-z0-9_]+" "$UP" | sed 's/.*iam_v2\.//' | sort -u)
if [ -z "$missing" ]; then
  ok "every function created by 0010 is dropped by its down script"
else
  no "down script does not drop:$missing"
fi
# every table the up script creates must be dropped too
tmissing=""
while read -r tb; do
  grep -q "DROP TABLE IF EXISTS iam_v2.$tb" "$DOWN" || tmissing="$tmissing $tb"
done < <(grep -oE "CREATE TABLE iam_v2\.[a-z0-9_]+" "$UP" | sed 's/.*iam_v2\.//' | sort -u)
if [ -z "$tmissing" ]; then
  ok "every table created by 0010 is dropped by its down script"
else
  no "down script does not drop tables:$tmissing"
fi

# ---------------------------------------------------------------- 4. zero runtime privilege while dark
if grep -q "REVOKE EXECUTE ON FUNCTION iam_v2.apply_entitlement_transition" "$UP" &&
   ! grep -qE "GRANT (EXECUTE|SELECT|INSERT|UPDATE|DELETE).*TO (svc_|PUBLIC)" "$UP"; then
  ok "0010 grants no runtime role any iam_v2 privilege"
else
  no "0010 grants a runtime privilege (Gate-P is a separate, authorized step)"
fi

# ---------------------------------------------------------------- 4b. exactly one Phase-3 shaping writer
# ADR-0002: netd is the ONLY process that mutates Phase-3 tc state. This is checked structurally rather than
# trusted: if acctd ever regains a tc mutation call, two daemons can race the same kernel classes on their own
# schedules, and nothing at runtime would report it.
if grep -qE '(AddSession|DeleteSession|EnsureBridgeInfra)\(' "$ROOT/data-plane/cmd/netd/phase3_shaping.go" 2>/dev/null; then
  ok "netd is a Phase-3 shaping writer (ADR-0002)"
else
  no "netd does not perform Phase-3 shaping"
fi
if grep -qE '(AddSession|DeleteSession)\(' "$ROOT/data-plane/cmd/acctd/phase3.go" 2>/dev/null; then
  no "acctd mutates tc directly — ADR-0002 requires netd to be the single shaping writer"
else
  ok "acctd derives the plan and performs NO Phase-3 tc mutation (single writer holds)"
fi

# ---------------------------------------------------------------- 5. no live-evidence fabrication
# The evidence bundle must never contain claims about an appliance this tooling has not actually touched.
if grep -RIn "LIVE VERIFIED" "$ROOT/docs/manifests" 2>/dev/null | grep -q .; then
  no "a manifest claims live verification that this offline tooling cannot have performed"
else
  ok "no offline artifact claims live verification"
fi

# ---------------------------------------------------------------- 8. the Phase-3 control-plane invariants
# These four are the properties that make the single-writer decision (ADR-0002) and the controlled-writer
# boundary real rather than documentary. Each is checked structurally, because each can be silently undone by
# an ordinary-looking change.

# (a) netd refuses to mutate tc while Phase 3 is dark, on its OWN authority. If this check ever fails, the
#     kill switch depends on acctd staying correct instead of on netd enforcing it.
if grep -q 'phase3_dark' "$ROOT/data-plane/cmd/netd/phase3_shaping.go" \
   && grep -q 'if !p.mode.Active' "$ROOT/data-plane/cmd/netd/phase3_shaping.go"; then
  ok "netd refuses shaping submissions while Phase 3 is dark (its own check, not the producer's)"
else
  no "netd does not independently refuse shaping while dark"
fi

# (b) the shaping producer is authenticated by peer credentials, never by a request header. A header is a
#     claim any local process can write; SO_PEERCRED is the kernel's statement.
if grep -q 'SO_PEERCRED' "$ROOT/data-plane/cmd/netd/phase3_peer_linux.go" \
   && ! grep -qE 'r\.Header\.Get\("X-[^"]*(Producer|Service|Caller)' "$ROOT/data-plane/cmd/netd/phase3_shaping.go"; then
  ok "the Phase-3 shaping producer is authenticated by peer credentials, not a header"
else
  no "the shaping producer is not authenticated by peer credentials"
fi

# (c) both ends of the shaping contract use the ONE shared definition. Two hand-written copies of a canonical
#     hash drift silently, and the drift only shows up as a refused plan in production.
if grep -q 'internal/shapeplan' "$ROOT/data-plane/cmd/netd/phase3_shaping.go" \
   && grep -q 'internal/shapeplan' "$ROOT/data-plane/cmd/acctd/phase3.go"; then
  ok "producer and applier share one shaping contract definition (internal/shapeplan)"
else
  no "the shaping contract is defined separately on each side"
fi

# (d) every Phase-3 composition root verifies the controlled-writer boundary before it can write. A service
#     that skipped this could run against a schema whose guards were never applied and never notice.
# netd is included because although it writes no Phase-3 TABLE directly, it performs two authoritative
# operations (allocating a class generation, registering a class origin) and those mean nothing on a schema
# whose guards were never applied. portald is deliberately absent: it writes no iam_v2 state at all, it
# proxies to scd, so requiring the check there would be requiring a promise it has no way to keep.
missing=""
for root in acctd edged scd pmsd netd; do
  grep -q 'writerguard.Verify' "$ROOT/data-plane/cmd/$root/main.go" || missing="$missing $root"
done
if [ -z "$missing" ]; then
  ok "every Phase-3 writing service verifies the controlled-writer boundary at startup"
else
  no "these Phase-3 services do not verify the writer boundary:$missing"
fi

# (e) ACCOUNTABLE BEFORE FORWARDING. netd must register a class's accounting origin BEFORE it activates the
# guest forwarding filters, or a class could carry unaccounted traffic. This is checked structurally, by line
# order in the one function that provisions a class: the registerOrigin call must precede the ActivateSession
# call, and the guest filters must never be installed by anything but ActivateSession. A reordering that broke
# the invariant would pass every functional test that only checks the happy path — this catches it at the door.
PROV="$ROOT/data-plane/cmd/netd/phase3_provision.go"
reg_line="$(grep -n 'registerOrigin(' "$PROV" | head -1 | cut -d: -f1)"
act_line="$(grep -n 'ActivateSession(' "$PROV" | head -1 | cut -d: -f1)"
# PrepareSession OR PrepareSessionIn: the staged preparation gained a SHARED-group variant, and a pattern that
# only knew the original name reported the invariant BROKEN when the call was renamed — a false alarm on a
# check whose whole value is that it is trusted.
prep_line="$(grep -nE 'PrepareSession(In)?\(' "$PROV" | head -1 | cut -d: -f1)"
if [ -n "$reg_line" ] && [ -n "$act_line" ] && [ -n "$prep_line" ] \
   && [ "$prep_line" -lt "$reg_line" ] && [ "$reg_line" -lt "$act_line" ]; then
  ok "netd prepares, then registers the accounting origin, then activates forwarding (accountable before forwarding)"
else
  no "netd's provisioning order is not prepare -> register origin -> activate (accountable-before-forwarding invariant)"
fi
# The forwarding filter is installed ONLY by the staged ActivateSession — never by AddSession, which installs a
# class and its filter together and is the legacy (non-Phase-3) path scd uses.
if grep -q 'AddSession(' "$ROOT/data-plane/cmd/netd/phase3_provision.go" 2>/dev/null \
   || grep -q 'shp.AddSession(' "$ROOT/data-plane/cmd/netd/phase3_shaping.go" 2>/dev/null; then
  no "netd's Phase-3 shaping path calls AddSession (class+filter in one step); it must stage Prepare/Activate"
else
  ok "netd's Phase-3 shaping never installs a class-and-filter in one step (no AddSession on the managed path)"
fi

# ---------------------------------------------------------------- 9. rollback ordering
# Every trigger the up migration attaches to the controlled-writer guard has to be dropped BY NAME in the down
# migration, and BEFORE the guard function itself is dropped. PostgreSQL refuses to drop a function while a
# trigger still depends on it, so one missing line aborts the entire rollback -- and nothing that merely
# APPLIES the migration can see it. That is why this has now slipped through twice: the failure only appears
# on the rollback path, and only in CI. The check is static so it costs nothing and names the missing line.
UP_SQL="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.up.sql"
DOWN_SQL="$ROOT/data-plane/migrations/0010_phase3_stay_resolution.down.sql"
guard_line="$(grep -n 'DROP FUNCTION IF EXISTS iam_v2.p3_controlled_writer_only' "$DOWN_SQL" | head -1 | cut -d: -f1)"
order_defect=""
if [ -z "$guard_line" ]; then
  order_defect=" the down migration never drops the controlled-writer guard function"
else
  # Each "CREATE TRIGGER <name> ... EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();" in the up migration.
  # The whole statement is accumulated up to its terminating semicolon before matching, so a trigger that
  # merely happens to sit above a guarded one is not mistaken for a guarded trigger itself.
  guarded="$(awk '
    /CREATE TRIGGER/            { stmt=$0; name=$3; open=1 }
    open && !/CREATE TRIGGER/   { stmt=stmt " " $0 }
    open && /;[[:space:]]*$/    { if (stmt ~ /p3_controlled_writer_only\(\)/) print name; open=0 }
  ' "$UP_SQL" | sort -u)"
  [ -n "$guarded" ] || order_defect=" no guarded triggers found in the up migration (the check would be vacuous)"
  for trg in $guarded; do
    dl="$(grep -n "DROP TRIGGER IF EXISTS $trg " "$DOWN_SQL" | head -1 | cut -d: -f1)"
    if [ -z "$dl" ]; then
      order_defect="$order_defect $trg(never dropped)"
    elif [ "$dl" -gt "$guard_line" ]; then
      order_defect="$order_defect $trg(dropped after the guard function)"
    fi
  done
fi
if [ -z "$order_defect" ]; then
  ok "every controlled-writer trigger is dropped by name before its guard function (rollback ordering)"
else
  no "rollback ordering defect:$order_defect"
fi


# ---------------------------------------------------------------- 10. bounded kernel authorization
# The one property that survives every process dying. A Phase-3 authorization must never be installed without a
# timeout: a permanent nft element keeps forwarding a guest past their entitlement window, past checkout and
# past a revocation, for as long as the appliance stays up, and nothing that is merely NOT RUNNING can undo it.
#
# Checked in two places, because either alone can be defeated. The gate must refuse a non-positive lease, and
# the provisioning path must never call Authorize with a literal zero.
GATE="$ROOT/data-plane/cmd/netd/phase3_gate.go"
LEASE="$ROOT/data-plane/cmd/netd/phase3_lease.go"
NFTGO="$ROOT/data-plane/internal/nft/nft.go"
if grep -q 'ErrUnboundedLease' "$NFTGO" && grep -q 'ttl <= 0' "$NFTGO" && grep -q 'LeaseIn' "$GATE"; then
  ok "Phase-3 packet authorization refuses an unbounded lease (ttl must be > 0)"
else
  no "the Phase-3 gate can install a PERMANENT nft element; a dead daemon would leave guests online forever"
fi
if grep -nE 'gate\.Authorize\(ctx, bridge, ip, 0\)' "$ROOT/data-plane/cmd/netd/"*.go >/dev/null 2>&1; then
  no "netd asks for a zero-timeout (permanent) Phase-3 authorization"
else
  ok "no Phase-3 code path requests a permanent authorization"
fi
# The lease must be clamped by the session's own hard boundary, or renewal would push a guest's deadline
# further away every pass and a long-lived session would never reach it.
if grep -q 'AccessEndsAt' "$LEASE" && grep -q 'AccessEndsAt' "$ROOT/data-plane/internal/shapeplan/plan.go"; then
  ok "the kernel lease is clamped by the session's hard access boundary, carried in the shaping contract"
else
  no "the shaping contract carries no hard access boundary, so a lease cannot be clamped by one"
fi
# Renewal must not be a repeated `add element`: nftables does not restart an existing element's timer on a
# second add, so a renewal built on it looks healthy and drops every guest at the first lease boundary.
if grep -q 'delete element inet stayconnect %s { %s } ; add element' "$NFTGO"; then
  ok "lease renewal is an atomic delete+add in one nft transaction, not a repeated add"
else
  no "lease renewal does not use the atomic refresh; a repeated add does not restart an element's timer"
fi

# ---------------------------------------------------------------- 11. accountability is DB-enforced
# "ACTIVE means authorized AND accountable" must be a database invariant, not an ordering convention in Go.
if grep -q 'ENFORCE_NOT_ACCOUNTABLE' "$UP_SQL" && grep -q 'ENFORCE_ORIGIN_EPOCH_MISMATCH' "$UP_SQL"; then
  ok "activate_session_enforcement verifies the accounting origin itself before promoting a Session"
else
  no "the controlled activation trusts the caller's claim that an epoch is accountable"
fi
# An unproven durable activation must not become permanent access.
if grep -q 'phase3ProvisionalLease' "$LEASE" && grep -q 'quarantine' "$ROOT/data-plane/cmd/netd/phase3_provision.go"; then
  ok "an activation that cannot be proven holds only a provisional lease and is then quarantined"
else
  no "an unconfirmed durable activation can persist as ordinary internet access"
fi

# ---------------------------------------------------------------- 12. the surgical live-dark foundation
# Installing the Phase-3 nft foundation on a live appliance must never flush or recreate the StayConnect table:
# the authorization set is part of it, and recreating it means recreating it EMPTY, taking every live legacy
# guest offline at once.
FOUND="$ROOT/data-plane/internal/nftfoundation/foundation.go"
if [ -f "$FOUND" ]; then
  if grep -nE '"(flush|delete table|add table)' "$FOUND" >/dev/null 2>&1; then
    no "the surgical foundation issues a flush/table command; it would disconnect every live legacy guest"
  else
    ok "the surgical foundation never flushes or recreates the StayConnect table"
  fi
  if grep -q 'sameElements' "$FOUND" && grep -q 'LegacyBefore' "$FOUND"; then
    ok "the foundation proves legacy authorization parity before and after, and rolls back if it cannot"
  else
    no "the foundation does not prove legacy authorization parity"
  fi
else
  no "the surgical Phase-3 nft foundation is missing; a flag-only cutover cannot be prepared safely"
fi

# ---------------------------------------------------------------- 13. hard-boundary + durable bound
# A lease clamped to a guest's hard access boundary must never be rounded UP: nft's timeout granularity is
# whole seconds, so rounding up expires the authorization PAST the deadline the business stated, by up to
# 999ms, on almost every boundary that does not land on a whole second.
if grep -q 'remaining.Truncate(time.Second)' "$LEASE" && grep -q 'ErrLeaseTooShort' "$NFTGO"; then
  ok "a boundary-clamped lease is truncated, never rounded up, and a sub-second lease is refused"
else
  no "a boundary-clamped lease can be rounded up past the hard access boundary"
fi
# The activation-uncertainty bound must be WRITE-AHEAD DURABLE: fsynced BEFORE the guest is provisionally
# authorized. Recorded at the end of the pass instead, a crash in between loses the bound entirely and the next
# process awards a brand-new grace — so a crash loop renews provisional access forever.
JOURNAL="$ROOT/data-plane/cmd/netd/phase3_journal.go"
if [ -f "$JOURNAL" ] && grep -q 'beginAttempt' "$JOURNAL" && grep -q 'd.Sync()' "$JOURNAL"; then
  ok "the activation bound is journalled and fsynced (file + directory) through an explicit durability boundary"
else
  no "there is no write-ahead durability boundary for the activation bound"
fi
# ORDERING: the write-ahead record must come BEFORE the first provisional authorization, in that one function.
begin_line="$(grep -n 'beginAttempt(' "$PROV" | head -1 | cut -d: -f1)"
prov_auth_line="$(grep -n 'gate.Authorize(ctx, bridge, ip, provisionalOrLess' "$PROV" | head -1 | cut -d: -f1)"
if [ -n "$begin_line" ] && [ -n "$prov_auth_line" ] && [ "$begin_line" -lt "$prov_auth_line" ]; then
  ok "the activation bound is made durable BEFORE the first provisional packet authorization"
else
  no "a guest can be provisionally authorized before the bound on that attempt is durable"
fi
if grep -q 'unprovenUnknown' "$ROOT/data-plane/cmd/netd/phase3_shaping.go" && grep -q 'unprovenUnknown' "$JOURNAL"; then
  ok "an unreadable activation journal fails closed instead of granting a fresh grace"
else
  no "losing the durable activation journal would award a fresh grace period"
fi
# SECURITY TIME: the bound must be measured against a monotonic clock, never the wall clock.
SECTIME="$ROOT/data-plane/cmd/netd/phase3_securitytime.go"
if [ -f "$SECTIME" ] && grep -q 'BootMillis' "$SECTIME" && grep -q 'proc/uptime' "$SECTIME"; then
  ok "the activation bound is measured against boot-relative monotonic time, not the wall clock"
else
  no "the activation bound is measured against a clock that NTP or a wrong RTC can move backwards"
fi
if grep -q 'crossBoot' "$JOURNAL" && grep -q 'crossBoot' "$PROV"; then
  ok "an unproven activation that survives a reboot is not granted a fresh grace"
else
  no "a reboot can manufacture a fresh unproven-activation grace"
fi
# BOOT IDENTITY IS PART OF THE CLOCK. An unreadable boot id represented as "" made crossBoot() compare "" with
# "" and answer "same boot", so a prior boot's deadline could be measured against a new boot's uptime.
if grep -q 'BootID() (string, error)' "$SECTIME" && grep -q 'plausibleBootID' "$SECTIME"; then
  ok "boot identity is an error-returning, validated part of the security clock"
else
  no "an unreadable or empty boot identity is indistinguishable from a trustworthy one"
fi
# The boot identity must be validated against the ACTUAL kernel contract (a canonical lowercase 8-4-4-4-12
# UUID), not merely "some opaque token": a truncated read would otherwise pass and could hide a real reboot.
if grep -q 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx' "$SECTIME"; then
  ok "boot identity is validated against the canonical Linux boot-id form"
else
  no "a truncated or malformed boot identity would be accepted as trustworthy"
fi
if grep -q 'q.BootID == "" || currentBootID == ""' "$JOURNAL"; then
  ok "an untrusted boot identity on either side is treated as a boot CHANGE, not as the same boot"
else
  no "reboot detection fails open when a boot identity cannot be read"
fi
# THE SECURITY JOURNAL IS VALIDATED SEMANTICALLY. It is authority now, and valid JSON is not the same as
# coherent security state: a deadline far in the future parses perfectly and silently widens the grace.
if grep -q 'func validateAttempts' "$JOURNAL" && grep -q 'longer than the' "$JOURNAL"; then
  ok "every persisted security record is validated for coherent, bounded state before it is trusted"
else
  no "a syntactically valid but incoherent security record would be trusted"
fi
# THE JOURNAL IS BOUND TO THIS APPLIANCE. A security history from another Tenant, Site or Appliance -- a
# restored image, a cloned VM, a copied /var/lib -- must never be read as "no attempts outstanding".
if grep -q 'func validateScope' "$JOURNAL" && grep -q 'load(tenant, site, appliance string)' "$JOURNAL"; then
  ok "the activation journal is bound to the assigned tenant/site/appliance scope and cannot be read without it"
else
  no "a journal from another appliance scope could be read as this appliance's security history"
fi
# AND THE TWO IDENTITIES IN A RECORD MUST AGREE. A key naming one session beside a SessionID naming another is
# a record that can be enforced against the wrong guest.
if grep -q 'func parseClassKey' "$JOURNAL" && grep -q 'the key names session' "$JOURNAL"; then
  ok "a record's activation key and session identity are parsed canonically and proven to agree"
else
  no "a security record's two identities can disagree"
fi

# ---------------------------------------------------------------- 12. the surgical live-dark foundation
# Installing the Phase-3 nft foundation on a live appliance must never flush or recreate the StayConnect table:
# the authorization set is part of it, and recreating it means recreating it EMPTY, taking every live legacy
# guest offline at once.
FOUND="$ROOT/data-plane/internal/nftfoundation/foundation.go"
if [ -f "$FOUND" ]; then
  if grep -nE '"(flush|delete table|add table)' "$FOUND" >/dev/null 2>&1; then
    no "the surgical foundation issues a flush/table command; it would disconnect every live legacy guest"
  else
    ok "the surgical foundation never flushes or recreates the StayConnect table"
  fi
  if grep -q 'sameElements' "$FOUND" && grep -q 'LegacyBefore' "$FOUND"; then
    ok "the foundation proves legacy authorization parity before and after, and rolls back if it cannot"
  else
    no "the foundation does not prove legacy authorization parity"
  fi
else
  no "the surgical Phase-3 nft foundation is missing; a flag-only cutover cannot be prepared safely"
fi

# ---------------------------------------------------------------- 13. hard-boundary + durable bound
# A lease clamped to a guest's hard access boundary must never be rounded UP: nft's timeout granularity is
# whole seconds, so rounding up expires the authorization PAST the deadline the business stated, by up to
# 999ms, on almost every boundary that does not land on a whole second.
if grep -q 'remaining.Truncate(time.Second)' "$LEASE" && grep -q 'ErrLeaseTooShort' "$NFTGO"; then
  ok "a boundary-clamped lease is truncated, never rounded up, and a sub-second lease is refused"
else
  no "a boundary-clamped lease can be rounded up past the hard access boundary"
fi
# ================================================== 9. ruleset durability across restart and reboot
#
# The Live Increment-9 blocker. netd re-asserted the ruleset on boot by replaying the stored bundle — a file
# rendered by an OLDER binary — so every start deleted a set the current software requires. These checks hold
# the corrected shape in place: reconciliation renders from the current binary, a matching fingerprint means
# NOTHING is executed, and the authorization that was live is carried across the one converge that does happen.
RECON="$ROOT/data-plane/internal/nftconverge/converge.go"
BOOT="$ROOT/data-plane/cmd/netd/apply_ops.go"
RENDER="$ROOT/data-plane/internal/netcfg/render_nft.go"

# (a) boot reconciliation must not replay a stored bundle's nft file.
if [ -f "$BOOT" ]; then
  boot_fn="$(awk '/^func \(a \*applier\) ReconcileActiveOnBoot/,/^}/' "$BOOT")"
  if printf '%s' "$boot_fn" | grep -q 'stayconnect.nft'; then
    # writing the artifact back for the record is fine; EXECUTING it is the defect.
    if printf '%s' "$boot_fn" | grep -qE '"nft", *"-f"'; then
      no "boot reconciliation still executes a stored bundle ruleset file"
    else
      ok "boot reconciliation never executes a stored bundle ruleset file"
    fi
  else
    ok "boot reconciliation never executes a stored bundle ruleset file"
  fi
  if printf '%s' "$boot_fn" | grep -q 'ensureNftStructure'; then
    ok "boot reconciliation converges the ruleset from a fresh render of the current binary"
  else
    no "boot reconciliation does not render the ruleset from the current binary"
  fi
else
  no "netd apply_ops.go not found; ruleset-durability checks cannot run"
fi

# (b) a matching fingerprint must return BEFORE anything is executed.
if [ -f "$RECON" ]; then
  ensure_fn="$(awk '/^func \(e \*Engine\) Ensure/,/^}/' "$RECON")"
  skip_line="$(printf '%s' "$ensure_fn" | grep -n 'live.TableExists && live.Fingerprint == out.DesiredFP' | cut -d: -f1 | head -1)"
  run_line="$(printf '%s' "$ensure_fn" | grep -n 'e.R.Run(' | cut -d: -f1 | head -1)"
  if [ -n "$skip_line" ] && [ -n "$run_line" ] && [ "$skip_line" -lt "$run_line" ]; then
    ok "a live ruleset that already matches the current render short-circuits before any command is executed"
  else
    no "the steady-state short-circuit does not precede execution (a routine restart could rewrite the ruleset)"
  fi
  if printf '%s' "$ensure_fn" | grep -q 'CarryOverCommands'; then
    ok "the upgrade converge carries live authorization across the atomic replace"
  else
    no "the converge does not carry live authorization across the replace"
  fi
  if printf '%s' "$ensure_fn" | grep -q 'nft applied but live fingerprint'; then
    ok "the converge verifies the result against the kernel instead of trusting the exit code"
  else
    no "the converge does not verify its result against the kernel"
  fi
  # the remaining lease, never the original
  if awk '/^func CarryOverCommands/,/^}/' "$RECON" | grep -q 'e.Expires > 0'; then
    ok "carried authorizations use the REMAINING lease, not the original"
  else
    no "carried authorizations do not use the remaining lease"
  fi
else
  no "internal/nftconverge/converge.go not found; ruleset-durability checks cannot run"
fi

# (c) the Phase-3 set is part of the rendered structure and carries a fingerprint.
if [ -f "$RENDER" ]; then
  if grep -q 'RenderMarkerSet' "$RENDER" && grep -q 'func RenderFingerprint' "$RENDER"; then
    ok "the generated ruleset carries a structure fingerprint the kernel can be asked for"
  else
    no "the generated ruleset carries no structure fingerprint"
  fi
  if grep -q 'set phase3_auth_ipv4' "$RENDER"; then
    ok "phase3_auth_ipv4 is emitted by the renderer itself, so its presence survives every restart"
  else
    no "phase3_auth_ipv4 is not part of the rendered structure"
  fi
else
  no "render_nft.go not found"
fi

# (d) the verified binary rollback tool cannot report success it did not earn.
ROLLBACK="$ROOT/scripts/binary-rollback.sh"
if [ -f "$ROLLBACK" ]; then
  if grep -nE '^\s*cp\s' "$ROLLBACK" | grep -qv '^\s*#'; then
    no "binary-rollback.sh replaces binaries with cp (cannot overwrite a running executable)"
  else
    ok "binary rollback replaces binaries with install(1), never cp"
  fi
  if grep -q '/proc/\$pid/exe' "$ROLLBACK"; then
    ok "binary rollback verifies the identity of the RUNNING process image, not just service health"
  else
    no "binary rollback does not verify the running process image"
  fi
else
  no "scripts/binary-rollback.sh not found"
fi

# ============================================== 10. network lifecycle: draft vs confirmed, and safe rollback
#
# (a) BOOT MUST RECONSTRUCT THE CONFIRMED REVISION, NOT AN UNAPPLIED DRAFT. Reconciling from the mutable
#     guest_networks rows would let an edit nobody applied take effect at the next reboot, with no apply
#     record, no health check, no confirmation and no watchdog — and would resurrect a change that was rolled
#     back precisely because it broke connectivity.
if [ -f "$BOOT" ]; then
  boot_fn2="$(awk '/^func \(a \*applier\) ReconcileActiveOnBoot/,/^}/' "$BOOT")"
  if printf '%s' "$boot_fn2" | grep -q 'LoadIntent'; then
    no "boot reconciliation still reads the mutable guest_networks intent (an unapplied draft could become the runtime)"
  else
    ok "boot reconciliation never reads the mutable guest_networks intent"
  fi
  if printf '%s' "$boot_fn2" | grep -qE 'currentActiveIntent|CurrentActiveIntent'; then
    ok "boot reconciliation reconstructs the CONFIRMED active revision's own intent snapshot"
  else
    no "boot reconciliation does not use the confirmed active revision's intent snapshot"
  fi
else
  no "netd apply_ops.go not found; network-lifecycle checks cannot run"
fi

# (b) ROLLBACK MUST NOT DEGRADE INTO EXECUTING A STORED RULESET FILE. That file begins with `delete table` and
#     is rendered by whatever binary was current when the revision was applied; running it at the worst moment
#     is how a rollback turns into a silent property-wide deauthorization.
if [ -f "$BOOT" ]; then
  rb_fn="$(awk '/^func \(a \*applier\) rollback\(/,/^}/' "$BOOT")"
  if printf '%s' "$rb_fn" | grep -E '"nft", *"-f"' | grep -qv '\-c'; then
    no "rollback still executes a stored ruleset file"
  else
    ok "rollback never executes a stored ruleset file"
  fi
  if printf '%s' "$rb_fn" | grep -q 'ensureNftStructure'; then
    ok "rollback restores structure by rendering the previous confirmed intent"
  else
    no "rollback does not render the previous confirmed intent"
  fi
  if printf '%s' "$rb_fn" | grep -q 'blocker'; then
    ok "a rollback that cannot be completed safely reports a blocker instead of degrading"
  else
    no "a failed nft rollback does not report a blocker"
  fi
fi

# (c) AN UNREADABLE LIVE STATE IS NOT AN EMPTY ONE. Absence must be decided by enumeration; any read or parse
#     failure must abort before nft -f.
if [ -f "$RECON" ]; then
  if grep -q 'ErrLiveStateUntrusted' "$RECON"; then
    ok "an unreadable live nft state is a distinct, named refusal rather than an assumed-empty one"
  else
    no "an unreadable live nft state is not distinguished from an empty one"
  fi
  if awk '/^func \(e \*Engine\) ReadLive/,/^}/' "$RECON" | grep -q 'list tables'; then
    ok "set absence is decided by enumerating the table, not by a failed read"
  else
    no "set absence is still inferred from a failed read"
  fi
  if awk '/^func \(e \*Engine\) readSet/,/^}/' "$RECON" | grep -q 'return nil, nil'; then
    no "a failed set read still degrades to an empty set"
  else
    ok "a failed set read never degrades to an empty set"
  fi
fi

# (d) THE RENDER MARKER MUST NOT SURVIVE A STRUCTURAL CHANGE MADE BY THE OPERATOR TOOL. netd skips
#     reconciliation whenever the marker matches, so a stale marker after a foundation rollback would leave the
#     appliance without the Phase-3 structure indefinitely.
FOUND="$ROOT/data-plane/internal/nftfoundation/foundation.go"
if [ -f "$FOUND" ]; then
  if grep -q 'func invalidateMarker' "$FOUND" && [ "$(grep -c 'invalidateMarker(st)' "$FOUND")" -ge 2 ]; then
    ok "the operator foundation tool invalidates the render marker on both install and rollback"
  else
    no "the operator foundation tool can change structure while leaving the render marker claiming it is current"
  fi
else
  no "nftfoundation/foundation.go not found"
fi

# (e) NO OTHER PACKAGE MAY EMIT STRUCTURAL nft COMMANDS. The renderer and the operator tool are the only two
#     writers of structure; anything else would be a third path that can desynchronise the marker.
# COMMENTS ARE NOT COMMANDS. The rollback path documents the destructive artifact it refuses to execute, and
# an earlier version of this check read that prose as a violation — a check that cannot tell an explanation
# from an instruction trains people to ignore it.
stray=""
while IFS= read -r f; do
  case "$f" in *_test.go) continue;; esac
  case "$f" in */internal/netcfg/*|*/internal/nftconverge/*|*/internal/nftfoundation/*) continue;; esac
  if sed -E 's#//.*##' "$f" | grep -qE '"(add|delete|replace|create)", *"(set|rule|chain|table)"|(add|delete|replace) (set|rule|chain) inet stayconnect|delete table inet stayconnect'; then
    stray="$stray $f"
  fi
done < <(find "$ROOT/data-plane" -name '*.go' 2>/dev/null)
if [ -z "$stray" ]; then
  ok "only the renderer and the operator foundation tool emit structural nft commands"
else
  no "a package outside the renderer/foundation emits structural nft commands: $stray"
fi

# ============================================== 11. the DARK pmsd deployment contract
#
# Live Increment 9 found the pmsd BINARY on the appliance and no unit. A daemon that is "deployed" as a file
# and never installed as a service is not deployed, and nothing noticed. These checks hold the reviewed
# contract in place: the unit exists, runs as a dedicated non-root account, and the dark env carries no flag.
PMSD_UNIT="$ROOT/deploy/systemd/stayconnect-pmsd.service"
PMSD_ENV="$ROOT/deploy/env/pmsd.env.dark"
PMSD_INSTALL="$ROOT/scripts/install-pmsd-dark.sh"
if [ -f "$PMSD_UNIT" ]; then
  if grep -qE '^User=stayconnect-pmsd' "$PMSD_UNIT" && ! grep -qE '^User=root' "$PMSD_UNIT"; then
    ok "the pmsd unit runs as a dedicated non-root account"
  else
    no "the pmsd unit does not run as the dedicated stayconnect-pmsd account"
  fi
  if grep -qE '^Restart=on-failure' "$PMSD_UNIT"; then
    ok "pmsd restarts only on FAILURE, so a clean flags-OFF exit cannot restart-loop"
  else
    no "the pmsd unit would restart a clean dark exit (restart storm while dark)"
  fi
  for h in NoNewPrivileges=yes 'CapabilityBoundingSet=$' ProtectSystem=strict PrivateTmp=yes; do
    grep -qE "^$h" "$PMSD_UNIT" || no "the pmsd unit is missing hardening: $h"
  done
  grep -qE '^NoNewPrivileges=yes' "$PMSD_UNIT" && ok "pmsd unit hardening is present (no new privileges, no capabilities, strict system protection)"
else
  no "deploy/systemd/stayconnect-pmsd.service is missing"
fi
if [ -f "$PMSD_ENV" ]; then
  if grep -qE '^[[:space:]]*STAYCONNECT_PHASE3_' "$PMSD_ENV"; then
    no "the reviewed dark pmsd env sets a Phase-3 flag"
  else
    ok "the reviewed dark pmsd env sets no Phase-3 flag (every flag resolves OFF)"
  fi
  # AND IT MUST NOT MENTION ONE EITHER, even in a comment. The darkness proof used on the appliance is a plain
  # name search across /etc/stayconnect; a flag name in a comment turns that clean signal into a false alarm on
  # a security-critical surface, and a check that cries wolf is a check people learn to skip.
  if grep -q 'STAYCONNECT_PHASE3' "$PMSD_ENV"; then
    no "the dark pmsd env mentions a Phase-3 flag name (it would trip the appliance darkness grep)"
  else
    ok "the dark pmsd env mentions no Phase-3 flag name at all, so the appliance darkness grep stays clean"
  fi
else
  no "deploy/env/pmsd.env.dark is missing; the unit EnvironmentFile has no reviewed source"
fi
if [ -f "$PMSD_INSTALL" ]; then
  if grep -q 'useradd --system' "$PMSD_INSTALL" && ! grep -qE 'User=root|--uid 0' "$PMSD_INSTALL"; then
    ok "the pmsd installer establishes the least-privilege account rather than weakening the unit"
  else
    no "the pmsd installer does not establish the reviewed least-privilege account"
  fi
  grep -q 'PMSD_DARK_CONTRACT' "$PMSD_INSTALL"     && ok "the pmsd installer verifies the dark contract it just installed"     || no "the pmsd installer does not verify the dark contract"
else
  no "scripts/install-pmsd-dark.sh is missing"
fi

# ============================================== 12. the pre-nftconverge rollback boundary
#
# A netd built before ADR-0003 re-asserts the stored bundle, which begins with `delete table` — starting one
# recreates the authorization sets EMPTY. Harmless with nobody authorized; a property-wide outage otherwise.
ROLLBACK="$ROOT/scripts/binary-rollback.sh"
if [ -f "$ROLLBACK" ]; then
  grep -q 'check_compat_boundary' "$ROLLBACK"     && ok "rollback evaluates the pre-convergence compatibility boundary before replacing anything"     || no "rollback has no pre-convergence compatibility boundary"
  grep -q "netd-render-fp=" "$ROLLBACK"     && ok "convergence capability is read from the target ARTIFACT, not a version string"     || no "rollback does not determine convergence capability from the target binary"
  if grep -qiE -- '--force|SC_ROLLBACK_FORCE|--yes-i-know' "$ROLLBACK"; then
    no "the ordinary rollback command exposes a force/override path past the boundary"
  else
    ok "no force/override path exists on the ordinary rollback command"
  fi
  awk '/^check_compat_boundary/,/^}/' "$ROLLBACK" | grep -q 'legacy_auth_count'     && ok "the boundary decides from the LIVE authorization set, not from assumptions"     || no "the boundary does not read the live authorization set"
else
  no "scripts/binary-rollback.sh is missing"
fi

# ---------------------------------------------------------------- 12. the DHCP ownership authority
#
# Address ownership decides whether an authorized address still belongs to its guest, and it decides it from
# Kea's leases. That makes DHCP a SAFETY authority, and its silence a safety condition. On 2026-08-31 the
# PRE-LIVE appliance ran for three days with a Kea that answered status-get while refusing every lease command,
# and every surface reported it healthy. These assert the code cannot do that again.
EV="$ROOT/data-plane/cmd/netd/phase3_ownership_evidence.go"
if [ -f "$EV" ]; then
  ok "netd carries a DHCP ownership-evidence health probe"
  grep -q 'no current lease manager' "$EV"     && ok "the exact live condition (no current lease manager) is classified as its own fault"     || no "the lease-manager-unavailable condition is not distinguished from any other query failure"
  grep -q 'evidenceMemfileUnusable' "$EV"     && ok "an unusable memfile backing state is reported with the artifact that caused it"     || no "memfile state is not inspected, so the cause of an evidence outage cannot be named"
  # The probe must not repair what it judges. os.Remove/Rename/Truncate or a service restart here would destroy
  # the evidence and hide a recurring fault behind a self-healing loop.
  if grep -qE 'os\.(Remove|Rename|Truncate|WriteFile|Create)|systemctl|exec\.Command' "$EV"; then
    no "the evidence probe mutates DHCP state instead of reporting it"
  else
    ok "the evidence probe repairs nothing: it reads, reports and preserves"
  fi
else
  no "netd has no DHCP ownership-evidence probe"
fi
grep -q 'statusWithEvidence' "$ROOT/data-plane/cmd/netd/main.go"   && ok "netd health reports the plane against a FRESH evidence probe, not a cached one"   || no "netd health does not consult the ownership authority"
awk '/func \(p \*phase3Shaping\) statusWithEvidence/,/^}/' "$ROOT/data-plane/cmd/netd/phase3_shaping.go"   | grep -q 'shapingDegradedState'   && ok "an evidence outage degrades the reported enforcement plane"   || no "the plane can report itself healthy while its ownership authority is unavailable"
CHK="$ROOT/deploy/scripts/check-dhcp-ownership-evidence.sh"
if [ -f "$CHK" ]; then
  ok "an operational DHCP ownership-evidence check ships with the appliance"
  if grep -qE '^[^#]*(rm|systemctl restart|truncate|mv)' "$CHK"; then
    no "the operational evidence check repairs or deletes DHCP state"
  else
    ok "the operational check deletes nothing and restarts nothing"
  fi
  grep -q 'check-dhcp-ownership-evidence.sh' "$ROOT/deploy/scripts/check-phase3-enforcement-plane.sh"     && ok "the enforcement-plane gate refuses a session-minting surface with no ownership evidence"     || no "the enforcement-plane gate ignores the ownership authority"
else
  no "no operational DHCP ownership-evidence check ships with the appliance"
fi

# ---------------------------------------------------------------- 13. the Hotel Admin capability contract
#
# Next.js substitutes NEXT_PUBLIC_* at BUILD time. A flag that is absent when `next build` runs is never
# substituted, so the compiled comparison is evaluated in the browser against an empty env shim and is
# permanently false: the routes still compile and answer by URL, and the operator simply loses the navigation
# to them. That is how the PRE-LIVE appliance served a Hotel Admin with no Internet Packages, no Service Plans
# and no PMS Connection for five days while every structural check passed.
CONTRACT="$ROOT/hotel-admin/capability-contract.json"
if [ -f "$CONTRACT" ]; then
  ok "the Hotel Admin capability contract is present"
  # Read with grep rather than python: this preflight also runs on a Windows workstation, where a python3
  # invoked from MSYS cannot open an MSYS-style path, and a check that errors is a check that does not run.
  missing_contract=""
  for token in NEXT_PUBLIC_PHASE2_ADMIN NEXT_PUBLIC_PHASE3_ADMIN /internet-packages /service-plans /pms-interfaces; do
    grep -qF -- "$token" "$CONTRACT" || missing_contract="$missing_contract $token"
  done
  [ -z "$missing_contract" ]     && ok "the contract requires the Phase-2/Phase-3 admin flags and the Internet Packages, Service Plans and PMS routes"     || no "the capability contract omits:$missing_contract"
else
  no "hotel-admin/capability-contract.json is missing — a build has nothing to be held to"
fi
DHA="$ROOT/deploy/scripts/deploy-hotel-admin.sh"
grep -q 'contract_flags' "$DHA"   && ok "packaging supplies the capability flags from the contract instead of a bare build"   || no "packaging still runs an unconstrained build"
grep -q 'assert_contract_satisfied' "$DHA"   && ok "packaging and installation both prove the contract against the BUILT tree"   || no "nothing proves the capability contract against a built bundle"
grep -q 'source_commit' "$DHA"   && ok "every bundle carries a machine-readable manifest naming its source commit"   || no "bundles ship without provenance"
grep -q 'smoke_live' "$DHA"   && ok "installation smoke-tests the live endpoint for the served BUILD_ID and the required surfaces"   || no "installation trusts systemctl and HTTP 200 alone"
grep -q 'rollback_eligible' "$DHA"   && ok "a rollback target must satisfy the current capability contract"   || no "an obsolete UI can still be wired as an executable rollback"
[ -f "$ROOT/deploy/scripts/check-hotel-admin-integrity.sh" ]   && ok "a standing Hotel Admin integrity guard ships with the appliance"   || no "no standing Hotel Admin integrity guard exists"
# THE GUARDS MUST BE FAIL-CLOSED, and one shared implementation must decide what satisfies the contract.
[ -f "$ROOT/deploy/scripts/lib-hotel-admin-contract.sh" ]   && ok "one shared implementation decides whether a release satisfies the contract"   || no "the deployment path and the standing checker define contract satisfaction separately"
grep -q 'ha_served_build_id' "$ROOT/deploy/scripts/check-hotel-admin-integrity.sh"   && grep -q 'ha_served_build_id' "$DHA"   && ok "both guards read the served BUILD_ID through the same fail-closed extractor"   || no "a guard still compares a served BUILD_ID it may not have been able to extract"
awk '/^smoke_live\(\)/,/^}/' "$DHA" | grep -q 'checked" -ne "$want_n'   && ok "the live smoke test proves it exercised every required route, by count"   || no "the live smoke test cannot tell a full route sweep from an empty one"
grep -q 'HOTEL_ADMIN_PUBLIC_URL' "$ROOT/deploy/scripts/check-hotel-admin-integrity.sh"   && ok "a configured operator endpoint is verified to serve the managed release"   || no "localhost health alone is allowed to prove the operator endpoint is current"
[ -f "$ROOT/deploy/scripts/check-hotel-admin-integrity-adversarial.sh" ]   && ok "the integrity guards carry an adversarial suite that drives them against controlled endpoints"   || no "nothing proves the integrity guards can fail"
# THE CERTIFICATE CHAIN, not just the leaf. A leaf with two years of life above an intermediate that expired
# last week is an endpoint no client can use, and it is what "healthy, days_remaining 729" meant here.
CM="$ROOT/deploy/scripts/hotel-admin-cert-manager.sh"
if [ -f "$CM" ]; then
  grep -q 'chain_expiring' "$CM"     && ok "certificate health is decided from every certificate in the served chain"     || no "certificate health still reads only the leaf, so an expired intermediate is invisible"
  grep -q 'chain_verifies' "$CM"     && ok "the served chain must verify against the local root before it is called healthy"     || no "nothing verifies that the served chain still chains to its root"
  grep -q 'CHAIN_RENEW_DAYS' "$CM"     && ok "the renewal window tracks the chain's own clock, not the leaf's"     || no "the renewal window is measured against the leaf, which outlives the intermediate by years"
else
  no "the Hotel Admin certificate manager is missing"
fi
[ -f "$ROOT/deploy/scripts/check-hotel-admin-cert-selftest.sh" ]   && ok "the certificate chain rules carry a regression suite built from real certificates"   || no "nothing proves the certificate chain rules can fail"
# THE 203/EXEC GUARD MUST NOT BE PINNED TO ONE DIRECTORY. It scanned only /opt/stayconnect/bin, so a unit
# naming /usr/local/sbin/... was invisible to it -- which is how a renewal timer failed nightly for two weeks.
grep -q 'unit_exec_paths' "$ROOT/deploy/scripts/install-service-units.sh"   && ok "the installer verifies every absolute Exec path a unit declares, in any directory"   || no "the installer only checks helpers under one directory"

# ============================================================================== report
# Emitting comes last on purpose: an earlier version printed the JSON before section 8 had run, so --json
# silently reported a smaller, all-passing suite.
if [ $JSON -eq 1 ]; then
  printf '{"pass":%d,"fail":%d,"checks":[' "$pass" "$fail"
  first=1
  for r in "${RESULTS[@]}"; do
    st="${r%%|*}"; msg="${r#*|}"
    [ $first -eq 1 ] || printf ','
    printf '{"status":"%s","check":"%s"}' "$st" "$(printf '%s' "$msg" | sed 's/"/\\"/g')"
    first=0
  done
  printf ']}\n'
else
  echo "============================================================"
  echo "PHASE3_PREFLIGHT: pass=$pass fail=$fail -> $([ $fail -eq 0 ] && echo PASS || echo FAIL)"
fi
[ $fail -eq 0 ]
