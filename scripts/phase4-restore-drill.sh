#!/usr/bin/env bash
# PHASE-4 SUPPORTED RESTORE DRILL.
#
# This is the drill T0038 did not have. That round "proved restore" by UPDATEing the stored system identity
# to a fake value -- which exercises the comparison and nothing else. It could not have discovered the
# defect this drill exists to cover: the repository's supported restore is
#
#     pg_restore -U stayconnect_site -d stayconnect_site <dump>
#
# into the SAME cluster, so system_identifier never changes and the T0038 detector never fires.
#
# So this drill performs a REAL pg_dump and a REAL pg_restore against a disposable PostgreSQL 16, with the
# management marker held in a temporary directory, and asserts what an operator would actually experience:
#
#   * financial work that existed at backup time and was completed afterwards comes BACK as pending;
#   * the raw restore is NOT detected by system identity, because nothing about the cluster changed;
#   * the marker-based detector DOES fire, holds every rail, and records the restore;
#   * an unsupported restore -- no marker, or a marker that vanished -- is detected and recorded as such;
#   * nothing is replayed, and release is refused until the records themselves are safe.
#
# Disposable infrastructure only. No Production, no appliance, no PMS, no provider.
# EXIT: 0 pass, 1 assertion failure (never retry), 2 infrastructure (retryable).
set -uo pipefail
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
C="${PHASE4_RESTORE_CONTAINER:-iamv2-p4restore}"; DB=iam_scratch; PORT="${PHASE4_RESTORE_PORT:-55443}"
pass=0; fail=0
ok(){ echo "  [PASS] $1"; pass=$((pass+1)); }
no(){ echo "  [FAIL] $1 :: ${2:-}"; fail=$((fail+1)); }
Q(){ docker exec "$C" psql -U postgres -d "$DB" -tAqc "$1" 2>&1; }
eq(){ if [ "$2" = "$3" ]; then ok "$1"; else no "$1" "expected '$2', got '$3'"; fi; }

cleanup(){ docker rm -f "$C" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

echo "===== PHASE-4 SUPPORTED RESTORE DRILL (real pg_dump / pg_restore) ====="
docker run -d --name "$C" -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB="$DB" \
  -p "127.0.0.1:$PORT:5432" postgres:16-alpine >/dev/null 2>&1 || { echo "INFRA: container"; exit 2; }
for i in $(seq 1 60); do docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 && break; sleep 1; done
docker exec "$C" psql -U postgres -d "$DB" -tAqc 'select 1' >/dev/null 2>&1 || { echo "INFRA: not ready"; exit 2; }
SCRATCH_CONTAINER="$C" SCRATCH_DB="$DB" SCRATCH_PORT_ALLOW="$PORT" SCRATCH_ACK=I_UNDERSTAND_DISPOSABLE \
  bash "$ROOT/iam_v2_scratch/run.sh" fresh >/dev/null 2>&1 || { echo "INFRA: schema"; exit 2; }
docker exec "$C" psql -U postgres -d "$DB" -tAqc "CREATE TABLE IF NOT EXISTS public.schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());" >/dev/null
for m in 0009_phase2_commerce 0010_phase3_stay_resolution 0011_phase4_financial_execution \
         0012_phase4_financial_hardening 0013_phase4_reversal_ledger 0014_phase4_payment_settlement \
         0015_phase4_payment_hardening 0016_phase4_payment_coherence 0017_phase4_least_privilege \
         0018_phase4_financial_identity_and_privilege 0019_phase4_financial_recovery \
         0020_phase4_financial_observability 0021_phase4_trust_boundary 0022_phase4_recovery_closure \
         0023_phase4_restore_generation 0024_phase4_outcome_authority_and_grant_kernel 0025_phase4_recovery_completion_and_compliance; do
  docker exec -i "$C" psql -U postgres -d "$DB" -v ON_ERROR_STOP=1 \
    < "$ROOT/data-plane/migrations/$m.up.sql" >/dev/null 2>&1 \
    || { echo "$m did not apply - a defect, not a flake"; exit 1; }
done
docker exec "$C" psql -U postgres -d "$DB" -tAqc \
  "CREATE TABLE IF NOT EXISTS public.operators (id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
     tenant_id uuid, email text NOT NULL, display_name text, password_hash text,
     status text NOT NULL DEFAULT 'active', created_at timestamptz NOT NULL DEFAULT now(),
     updated_at timestamptz NOT NULL DEFAULT now(), auth_method text NOT NULL DEFAULT 'local');" >/dev/null

T=11111111-1111-1111-1111-111111111111
S=22222222-2222-2222-2222-222222222222
Q "INSERT INTO public.tenants(id) VALUES ('$T') ON CONFLICT DO NOTHING;
   INSERT INTO public.sites(id,tenant_id) VALUES ('$S','$T') ON CONFLICT DO NOTHING;
   INSERT INTO public.operators(id,tenant_id,email,status)
     VALUES ('33333333-3333-3333-3333-333333333333','$T','ops@test.local','active') ON CONFLICT DO NOTHING;" >/dev/null
ACTOR=33333333-3333-3333-3333-333333333333
IDENT="$(Q "SELECT system_identifier::text FROM pg_control_system();")"

# ---------------------------------------------------------------- baseline: a healthy site, generation 0
eq "the site starts outside recovery" "INITIALIZED" \
  "$(Q "SELECT iam_v2.p4_reconcile_financial_epoch_v2('$T','$S','$IDENT',0,true);")"
eq "the restore generation starts at zero" "0" \
  "$(Q "SELECT iam_v2.p4_current_restore_generation('$T','$S');")"

# A commercial chain to hang the purchase on. The scratch schema ships no packages, so the drill builds
# its own rather than depending on a fixture whose contents it does not control.
Q "INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code) VALUES ('77770000-0000-0000-0000-000000000001','$T','$S','drill');
   INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,name,max_concurrent_devices,time_accounting_mode,data_quota_bytes)
     VALUES ('77770000-0000-0000-0000-0000000000a1','$T','$S','77770000-0000-0000-0000-000000000001',1,'drill',2,'VALIDITY_WINDOW',1000000);
   INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code) VALUES ('77770000-0000-0000-0000-000000000002','$T','$S','drillpkg');
   INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent)
     VALUES ('77770000-0000-0000-0000-0000000000b1','$T','$S','77770000-0000-0000-0000-000000000002',1,'77770000-0000-0000-0000-0000000000a1','GENERAL',100,'USD',2);" >/dev/null
eq "the drill built its own commercial chain" "1"   "$(Q "SELECT count(*) FROM iam_v2.internet_package_revisions WHERE id='77770000-0000-0000-0000-0000000000b1';")"

# A payment intent that exists at BACKUP time. After the restore it will come back as CREATED, which is the
# whole hazard: the restored database believes it still has to be executed.
Q "INSERT INTO iam_v2.purchases(id,tenant_id,site_id,package_revision_id,trigger,amount_minor,currency,currency_exponent,state)
   VALUES ('44440000-0000-0000-0000-000000000001','$T','$S','77770000-0000-0000-0000-0000000000b1','ADMIN_GRANT',100,'USD',2,'AWAITING_SETTLEMENT');" >/dev/null
eq "the backup-time purchase exists" "1"   "$(Q "SELECT count(*) FROM iam_v2.purchases WHERE id='44440000-0000-0000-0000-000000000001';")"
Q "INSERT INTO iam_v2.settlements(id,tenant_id,site_id,purchase_id,method,status)
   VALUES ('44440000-0000-0000-0000-0000000000d1','$T','$S','44440000-0000-0000-0000-000000000001','ONLINE_PAYMENT','REQUIRED');" >/dev/null
Q "INSERT INTO iam_v2.payment_provider_accounts(id,tenant_id,site_id,provider,merchant_account_ref,status,is_default)
   VALUES ('55550000-0000-0000-0000-000000000011','$T','$S','test-double','acct_drill','ACTIVE',true);" >/dev/null
Q "INSERT INTO iam_v2.payment_transactions(id,tenant_id,site_id,settlement_id,merchant_account_id,transaction_type,provider,provider_ref,idempotency_key,amount_minor,currency,currency_exponent,status)
   VALUES ('66660000-0000-0000-0000-000000000001','$T','$S','44440000-0000-0000-0000-0000000000d1','55550000-0000-0000-0000-000000000011','CHARGE','test-double','sc_drill_ref','idem-drill',100,'USD',2,'CREATED');" >/dev/null
eq "an in-flight payment exists at backup time" "CREATED" \
  "$(Q "SELECT status FROM iam_v2.payment_transactions WHERE id='66660000-0000-0000-0000-000000000001';")"

# ---------------------------------------------------------------- the REAL backup
MSYS_NO_PATHCONV=1 docker exec "$C" pg_dump -U postgres -Fc "$DB" -f /tmp/drill.dump 2>/dev/null \
  || { echo "INFRA: pg_dump failed"; exit 2; }
ok "a real pg_dump was taken"

# work continues AFTER the backup: the payment executes and captures
Q "SELECT iam_v2.begin_payment_execution('66660000-0000-0000-0000-000000000001');" >/dev/null
Q "SELECT iam_v2.p4_apply_provider_outcome('sc_drill_ref','evt-drill','x','CAPTURED',NULL,'{}'::jsonb);" >/dev/null
eq "after the backup the payment captured" "CAPTURED" \
  "$(Q "SELECT status FROM iam_v2.payment_transactions WHERE id='66660000-0000-0000-0000-000000000001';")"
eq "and its settlement settled" "SETTLED" \
  "$(Q "SELECT status FROM iam_v2.settlements WHERE id='44440000-0000-0000-0000-0000000000d1';")"

# ---------------------------------------------------------------- the REAL restore
MSYS_NO_PATHCONV=1 docker exec "$C" pg_restore -U postgres -d "$DB" --clean --if-exists /tmp/drill.dump >/dev/null 2>&1
eq "the restore brought the captured payment back as CREATED" "CREATED" \
  "$(Q "SELECT status FROM iam_v2.payment_transactions WHERE id='66660000-0000-0000-0000-000000000001';")"
eq "and its settlement back to REQUIRED" "REQUIRED" \
  "$(Q "SELECT status FROM iam_v2.settlements WHERE id='44440000-0000-0000-0000-0000000000d1';")"

# THE FINDING THIS DRILL EXISTS FOR. The cluster is unchanged, so the T0038 detector sees nothing.
IDENT2="$(Q "SELECT system_identifier::text FROM pg_control_system();")"
if [ "$IDENT" = "$IDENT2" ]; then
  ok "a supported pg_restore does NOT change system_identifier (which is why it cannot be the only signal)"
else
  no "a supported pg_restore does NOT change system_identifier" "identity changed: $IDENT -> $IDENT2"
fi
eq "the system-identity signal alone reports nothing" "UNCHANGED" \
  "$(Q "SELECT iam_v2.p4_reconcile_financial_epoch_v2('$T','$S','$IDENT2',0,true);")"

# The MARKER is what carries the truth across the restore. The tool advanced it before restoring, so it is
# now ahead of the database.
eq "the marker-based detector fires" "RECOVERY_ENTERED" \
  "$(Q "SELECT iam_v2.p4_reconcile_financial_epoch_v2('$T','$S','$IDENT2',1,true);")"
eq "money movement is held" "t" "$(Q "SELECT iam_v2.p4_financial_recovery_active('$T','$S');")"
eq "the restored payment is held for reconciliation" "1" \
  "$(Q "SELECT count(*) FROM iam_v2.financial_recovery_holds WHERE tenant_id='$T' AND work_kind='PAYMENT_TRANSACTION' AND resolution IS NULL;")"
eq "the restore was recorded as UNSUPPORTED (no manifest explains it)" "1" \
  "$(Q "SELECT count(*) FROM iam_v2.financial_restore_events WHERE tenant_id='$T' AND restore_kind='UNSUPPORTED_RAW_SNAPSHOT';")"

# nothing is replayed: execution is refused while held
out="$(Q "SELECT iam_v2.begin_payment_execution('66660000-0000-0000-0000-000000000001');")"
if printf '%s' "$out" | grep -q 'FINANCIAL_RECOVERY_MODE'; then
  ok "the restored payment cannot be re-executed"
else
  no "the restored payment cannot be re-executed" "$(printf '%s' "$out" | head -1)"
fi

# release is refused while the record itself is still live
rel="$(Q "SELECT iam_v2.p4_resolve_recovery_hold(
   (SELECT id FROM iam_v2.financial_recovery_holds WHERE tenant_id='$T' AND resolution IS NULL LIMIT 1),
   'CONFIRMED_COMPLETED','$ACTOR','the provider dashboard shows this charge as captured');")"
rel2="$(Q "SELECT iam_v2.p4_release_financial_recovery('$T','$S','$ACTOR','reconciled against the provider dashboard');")"
if printf '%s' "$rel2" | grep -qE 'RECOVERY_HOLDS_UNRESOLVED|RECOVERY_STATE_UNSAFE'; then
  ok "release is refused while anything is still unreconciled or still live"
else
  no "release is refused while anything is still unreconciled or still live" "$(printf '%s' "$rel2" | head -1)"
fi

# ---------------------------------------------------------------- the SUPPORTED path, recorded properly
Q "UPDATE iam_v2.financial_epochs SET released_at=now(), released_by='$ACTOR', release_note='drill reset'
    WHERE tenant_id='$T' AND released_at IS NULL;" >/dev/null
Q "UPDATE iam_v2.financial_recovery_holds SET resolution='ABANDONED', resolved_at=now(), resolved_by='$ACTOR',
    resolution_note='drill reset' WHERE tenant_id='$T' AND resolution IS NULL;" >/dev/null
SHA="$(printf 'drill-manifest' | sha256sum | awk '{print $1}')"
sup="$(Q "SELECT iam_v2.p4_record_supported_restore('$T','$S',5,'$SHA',now(),'drill');")"
if printf '%s' "$sup" | grep -qE '^[0-9]+$'; then
  ok "a supported restore records its verified manifest digest and enters recovery"
else
  no "a supported restore records its manifest" "$(printf '%s' "$sup" | head -1)"
fi
eq "the supported restore is distinguishable from a raw snapshot" "1" \
  "$(Q "SELECT count(*) FROM iam_v2.financial_restore_events WHERE tenant_id='$T' AND restore_kind='SUPPORTED' AND manifest_sha256='$SHA';")"
eq "the generation advanced" "5" "$(Q "SELECT iam_v2.p4_current_restore_generation('$T','$S');")"
bad="$(Q "SELECT iam_v2.p4_record_supported_restore('$T','$S',5,'$SHA',now(),'drill');")"
if printf '%s' "$bad" | grep -q 'RESTORE_GENERATION_NOT_ADVANCED'; then
  ok "a restore that does not advance the generation is refused"
else
  no "a restore that does not advance the generation is refused" "$(printf '%s' "$bad" | head -1)"
fi
nosha="$(Q "SELECT iam_v2.p4_record_supported_restore('$T','$S',6,'not-a-digest',now(),'drill');")"
if printf '%s' "$nosha" | grep -q 'RESTORE_MANIFEST_REQUIRED'; then
  ok "a supported restore without a verified manifest digest is refused"
else
  no "a supported restore without a manifest digest is refused" "$(printf '%s' "$nosha" | head -1)"
fi

# ---------------------------------------------------------------- the unsupported path: marker gone
Q "UPDATE iam_v2.financial_epochs SET released_at=now(), released_by='$ACTOR', release_note='drill reset'
    WHERE tenant_id='$T' AND released_at IS NULL;" >/dev/null
eq "a vanished management marker is treated as an unsupported restore" "RECOVERY_ENTERED" \
  "$(Q "SELECT iam_v2.p4_reconcile_financial_epoch_v2('$T','$S','$IDENT2',0,false);")"

# ---------------------------------------------------------------- the tool itself
if bash -n "$ROOT/deploy/scripts/stayconnect-financial-restore.sh"    && bash -n "$ROOT/deploy/scripts/stayconnect-site-backup.sh"; then
  ok "the supported backup and restore tools parse"
else
  no "the supported backup and restore tools parse" "syntax error"
fi
if grep -q 'no TPM' "$ROOT/deploy/scripts/stayconnect-financial-restore.sh"; then
  ok "the tool records the absence of hardware anti-rollback rather than implying it"
else
  no "the tool records its own limitation" "the TPM/monotonic-counter limitation is not stated"
fi


# ============================================================================
# THE SUPPORTED FLOW, END TO END, WITH A REAL SIGNED MANIFEST
# ============================================================================
# Everything above proved the DATABASE half. This proves the TOOL half: a manifest signed with the
# appliance's pinned registry root, a caller-chosen key being refused, a quiesce that cannot be faked, and
# the /etc backup leaving the marker alone.
echo
echo "== the supported restore flow (pinned anchor, proven quiesce, marker vs /etc) =="
TOOL="$ROOT/deploy/scripts/stayconnect-financial-restore.sh"
BACKUP="$ROOT/deploy/scripts/stayconnect-site-backup.sh"
W="$(mktemp -d)"; trap 'rm -rf "$W"' RETURN 2>/dev/null || true

# The REAL trust anchor shape: a raw 32-byte Ed25519 public key, exactly as the assignment registry pins it.
openssl genpkey -algorithm ed25519 -out "$W/root.pem" 2>/dev/null
openssl pkey -in "$W/root.pem" -pubout -outform DER 2>/dev/null | tail -c 32 > "$W/anchor.pub"
# ...and an unrelated key, which is what an attacker or a careless operator would bring.
openssl genpkey -algorithm ed25519 -out "$W/other.pem" 2>/dev/null

if [ "$(stat -c%s "$W/anchor.pub" 2>/dev/null || stat -f%z "$W/anchor.pub")" = "32" ]; then
  ok "the pinned anchor is a raw 32-byte Ed25519 key, the shape the product already uses"
else
  no "the pinned anchor is a raw 32-byte Ed25519 key" "wrong size"
fi

# a dump and a manifest that describes it
printf 'not-a-real-dump-but-a-real-digest' > "$W/site.dump"
DSHA="$(sha256sum "$W/site.dump" | awk '{print $1}')"
cat > "$W/m.json" <<JSON
{"dump_sha256":"$DSHA","backup_taken_at":"2026-08-13T00:00:00Z","database":"stayconnect_site"}
JSON
openssl pkeyutl -sign -inkey "$W/root.pem" -rawin -in "$W/m.json" -out "$W/m.json.sig" 2>/dev/null
openssl pkeyutl -sign -inkey "$W/other.pem" -rawin -in "$W/m.json" -out "$W/other.sig" 2>/dev/null

# Host shims. The restore tool targets a Linux appliance where python3 and pg_dump are present; this
# drill also runs on a developer workstation whose Git Bash PATH has neither. The shims make the drill
# deterministic on both WITHOUT changing what the tool does: python3 forwards to whatever python exists,
# and pg_dump writes a stand-in artefact so the /etc-exclusion check -- the thing actually under test --
# still runs. Neither shim touches the verification, the quiesce or the marker logic.
mkdir -p "$W/bin"
# `command -v` is not enough on Windows: the Microsoft Store ships a zero-byte python3 alias that
# resolves but does not run. Ask it for its version instead.
if ! python3 --version >/dev/null 2>&1; then
  printf '#!/usr/bin/env bash\nexec python "$@"\n' > "$W/bin/python3"
  chmod +x "$W/bin/python3"
fi
if ! pg_dump --version >/dev/null 2>&1; then
  cat > "$W/bin/pg_dump" <<'PGSTUB'
#!/usr/bin/env bash
prev=""
for a in "$@"; do
  if [ "$prev" = "-f" ]; then printf 'stand-in dump' > "$a"; fi
  prev="$a"
done
exit 0
PGSTUB
  chmod +x "$W/bin/pg_dump"
fi
export PATH="$W/bin:$PATH"

run_tool() {  # run_tool <anchor> <manifest> <extra args...>
  SCD_ASSIGNMENT_REGISTRY_ROOT="$1" STAYCONNECT_MARKER_DIR="$W/etc" \
    bash "$TOOL" --dump "$W/site.dump" --manifest "$2" \
    --tenant 11111111-1111-1111-1111-111111111111 \
    --site 22222222-2222-2222-2222-222222222222 "${@:3}" 2>&1
}

out="$(run_tool "$W/anchor.pub" "$W/m.json" --dry-run)"
if printf '%s' "$out" | grep -q "verified against the pinned registry root anchor"; then
  ok "a manifest signed by the pinned root verifies"
else
  no "a manifest signed by the pinned root verifies" "$(printf '%s' "$out" | tail -2 | tr '\n' ' ')"
fi

# THE FINDING THIS BLOCK EXISTS FOR: a manifest signed by any other key must not be accepted, and the tool
# must not offer a way to nominate the key it is checked against.
cp "$W/other.sig" "$W/m.json.sig"
out="$(run_tool "$W/anchor.pub" "$W/m.json" --dry-run || true)"
if printf '%s' "$out" | grep -q "did not verify against this appliance's pinned registry root"; then
  ok "a manifest signed by an unrelated key is refused"
else
  no "a manifest signed by an unrelated key is refused" "$(printf '%s' "$out" | tail -2 | tr '\n' ' ')"
fi
openssl pkeyutl -sign -inkey "$W/root.pem" -rawin -in "$W/m.json" -out "$W/m.json.sig" 2>/dev/null

out="$(run_tool "$W/anchor.pub" "$W/m.json" --dry-run --pubkey "$W/other.pem" || true)"
if printf '%s' "$out" | grep -q -- "--pubkey is not accepted"; then
  ok "the tool refuses a caller-chosen verification key outright"
else
  no "the tool refuses a caller-chosen verification key" "$(printf '%s' "$out" | tail -2 | tr '\n' ' ')"
fi

# a tampered dump is caught by the digest the VERIFIED manifest carries
printf 'tampered' >> "$W/site.dump"
out="$(run_tool "$W/anchor.pub" "$W/m.json" --dry-run || true)"
if printf '%s' "$out" | grep -q "does not match the manifest"; then
  ok "a dump that does not match the verified manifest is refused"
else
  no "a dump that does not match the verified manifest is refused" "$(printf '%s' "$out" | tail -2 | tr '\n' ' ')"
fi
printf 'not-a-real-dump-but-a-real-digest' > "$W/site.dump"

# no anchor at all: fail closed rather than fall back to something
out="$(run_tool "$W/absent.pub" "$W/m.json" --dry-run || true)"
if printf '%s' "$out" | grep -q "no pinned registry root anchor"; then
  ok "an appliance with no pinned anchor cannot restore at all"
else
  no "an appliance with no pinned anchor cannot restore" "$(printf '%s' "$out" | tail -2 | tr '\n' ' ')"
fi

# ---------------------------------------------------------------- the proven quiesce
# systemctl does not exist in this container, so the tool's own "not installed on this appliance" branch is
# what runs. The assertion that matters is the SHAPE of the check: it stops, then verifies with is-active,
# and treats an un-provable stop as fatal rather than logging and continuing.
if grep -q 'systemctl is-active' "$TOOL" && grep -q 'could not be proven stopped' "$TOOL"; then
  ok "the restore proves each writer stopped rather than assuming the stop worked"
else
  no "the restore proves each writer stopped" "no is-active verification found"
fi
if grep -q 'stayconnect-pmsd' "$TOOL"; then
  ok "the quiesce list includes the PMS/financial runtime that exists after phase 4"
else
  no "the quiesce list includes pmsd" "pmsd is not in SERVICES"
fi
if grep -qE 'systemctl stop "\$s" \|\| true' "$TOOL"; then
  no "a failed stop is never swallowed" "the tool still discards a stop failure"
else
  ok "a failed stop is never swallowed"
fi

# A quiesce that cannot be proven must abort. Simulated with a systemctl stub that reports the service is
# still running -- which is exactly what a hung writer looks like.
mkdir -p "$W/stub"
cat > "$W/stub/systemctl" <<'STUB'
#!/usr/bin/env bash
case "$1" in
  list-unit-files) exit 0 ;;
  stop)            exit 0 ;;
  is-active)       echo active; exit 0 ;;
  start)           exit 0 ;;
esac
exit 0
STUB
chmod +x "$W/stub/systemctl"
# install -d -m 0700 cannot set POSIX permissions on a Windows filesystem, so the directory is created
# up front. The tool still runs its own permission call; it simply finds the directory already there.
mkdir -p "$W/etc2"
out="$(PATH="$W/stub:$W/bin:$PATH" SCD_ASSIGNMENT_REGISTRY_ROOT="$W/anchor.pub" STAYCONNECT_MARKER_DIR="$W/etc2" \
  bash "$TOOL" --dump "$W/site.dump" --manifest "$W/m.json" \
  --tenant 11111111-1111-1111-1111-111111111111 \
  --site 22222222-2222-2222-2222-222222222222 2>&1 || true)"
if printf '%s' "$out" | grep -q "could not be proven stopped"; then
  ok "a writer that will not stop aborts the restore"
else
  no "a writer that will not stop aborts the restore" "$(printf '%s' "$out" | tail -2 | tr '\n' ' ')"
fi
if [ -f "$W/etc2/financial-restore-generation.json" ]; then
  ok "the marker was already advanced before the abort, so the next startup still holds money movement"
else
  no "the marker is advanced before the quiesce" "no marker was written"
fi

# ---------------------------------------------------------------- the marker vs the /etc backup domain
mkdir -p "$W/etcroot/stayconnect"
echo '{"restore_generation": 7}' > "$W/etcroot/stayconnect/financial-restore-generation.json"
echo 'identity' > "$W/etcroot/stayconnect/identity.key"
out="$(STAYCONNECT_BACKUP_DIR="$W/backups" STAYCONNECT_ETC_DIR="$W/etcroot/stayconnect" \
  bash "$BACKUP" 2>&1 || true)"
TGZ="$(ls "$W/backups"/*-etc.tgz 2>/dev/null | head -1)"
if [ -n "$TGZ" ] && ! tar tzf "$TGZ" | grep -q 'financial-restore-generation.json'; then
  ok "the /etc backup excludes the financial restore marker"
else
  no "the /etc backup excludes the marker" "$(printf '%s' "$out" | tail -2 | tr '\n' ' ')"
fi
if [ -n "$TGZ" ] && tar tzf "$TGZ" | grep -q 'identity.key'; then
  ok "the /etc backup still contains everything else it always did"
else
  no "the /etc backup still contains identity material" "identity.key missing from the archive"
fi

echo
echo "===== RESTORE DRILL: PASS=$pass FAIL=$fail ====="
[ "$fail" -eq 0 ] || exit 1
exit 0
