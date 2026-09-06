-- THE ENTITLEMENT NEVER RECORDED WHAT IT HAD SPENT.
--
-- The approved contract is that a data allowance belongs to the ENTITLEMENT: aggregate across every Device,
-- Session and address, reaching it ends access exactly once, and a new IP or a second device never resets it.
--
-- iam_v2.entitlements.consumed_data_bytes exists to hold that number. On this appliance it held 0 -- for all
-- four entitlements, through a real guest session that moved 35,692,721 bytes, with usage_version still 0.
-- Nothing had ever written it.
--
-- WHY. There are TWO ingestion implementations in this schema. iam_v2.ingest_sample (iam_base mg9) advances
-- the entitlement aggregate. iam_v2.ingest_absolute_counters (0010) is the one acctd actually calls, and it
-- advanced the accounting record, the session totals and the checkpoint -- but not the entitlement. The
-- daemon used the implementation that forgot.
--
-- WHY NO TEST CAUGHT IT. Every quota test inserts into iam_v2.accounting_records directly. That exercises the
-- DERIVED crossing (which is correct, and is why enforcement was latently sound) while never once running the
-- ingestion operation the appliance runs. The tested path and the live path were different paths.
--
-- WHAT THIS DOES NOT CHANGE. p6_data_crossing remains the SINGLE authority for WHEN a quota was crossed --
-- the exact sample, from the authoritative series. This migration adds no second opinion about that. It makes
-- the Entitlement's own recorded usage true, so that what the record says a guest has spent matches what they
-- have spent.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. One place to ask what an Entitlement has consumed
-- ---------------------------------------------------------------------------------------------------------
-- The same attribution p6_data_crossing uses, exposed on its own so that surfaces, the reconciliation below
-- and the tests all read ONE definition instead of three hand-written window functions.
CREATE OR REPLACE FUNCTION iam_v2.p3_entitlement_data_usage(p_entitlement uuid)
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $usage$
  SELECT COALESCE(sum(ar.bytes_up + ar.bytes_down), 0)::bigint
    FROM iam_v2.accounting_records ar
    JOIN iam_v2.session_entitlement_bindings b ON b.session_id = ar.session_id
     AND b.entitlement_id = p_entitlement AND b.bound_from <= ar.sampled_at
     AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
$usage$;

COMMENT ON FUNCTION iam_v2.p3_entitlement_data_usage(uuid) IS
  'Authoritative bytes attributed to an Entitlement across every Session and Device bound to it, by the same '
  'binding intervals p6_data_crossing uses. The maintained counter entitlements.consumed_data_bytes must '
  'equal this.';

REVOKE ALL ON FUNCTION iam_v2.p3_entitlement_data_usage(uuid) FROM PUBLIC;

DO $grants$
DECLARE r text;
BEGIN
  FOREACH r IN ARRAY ARRAY['svc_acctd', 'svc_scd', 'svc_edged'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
      EXECUTE format('GRANT EXECUTE ON FUNCTION iam_v2.p3_entitlement_data_usage(uuid) TO %I', r);
    END IF;
  END LOOP;
END $grants$;

-- ---------------------------------------------------------------------------------------------------------
-- 2. The ingestion operation, now advancing the Entitlement it already attributes to
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.ingest_absolute_counters(
    p_tenant uuid, p_site uuid, p_session uuid, p_source_device uuid,
    p_bridge text, p_class_minor int, p_epoch bigint,
    p_abs_up bigint, p_abs_down bigint, p_sampled_at timestamptz) RETURNS text
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE
  v_started timestamptz; v_device uuid; v_ended timestamptz; v_iface text; v_ip inet; v_other int;
  cp record; v_ent uuid; v_up bigint; v_down bigint; v_seq bigint; v_rec uuid; v_class text; v_delayed boolean;
BEGIN
  IF p_abs_up IS NULL OR p_abs_down IS NULL OR p_abs_up < 0 OR p_abs_down < 0 THEN
    RAISE EXCEPTION 'ACCT_INVALID: absolute counters must be non-negative (up=%, down=%)', p_abs_up, p_abs_down;
  END IF;
  IF p_sampled_at IS NULL OR p_bridge IS NULL OR p_bridge = '' OR p_class_minor IS NULL OR p_epoch IS NULL THEN
    RAISE EXCEPTION 'ACCT_INVALID: sampled_at, bridge, class minor and source epoch are all required';
  END IF;

  -- SCOPE AND SOURCE COHERENCE. Every field describing WHERE these counters came from is re-derived here from
  -- the Session's own row and compared. The daemon computing them correctly is not evidence: the operation
  -- has to be able to refuse a caller that computed them wrongly, was fed the wrong session, or is replaying
  -- one session's counters under another's identity.
  SELECT started, device_id, ended, ingress_interface, ip
    INTO v_started, v_device, v_ended, v_iface, v_ip
    FROM iam_v2.sessions
   WHERE id = p_session AND tenant_id = p_tenant AND site_id = p_site;
  IF v_started IS NULL THEN
    RAISE EXCEPTION 'ACCT_SESSION_OUT_OF_SCOPE: session % is not in this tenant/site', p_session;
  END IF;
  -- DEVICE. Without this a class minor reused by another guest would quietly bill its traffic to whoever
  -- held the minor before.
  IF v_device IS DISTINCT FROM p_source_device THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % is not bound to device %', p_session, p_source_device;
  END IF;
  -- BRIDGE. The Session records the interface it is actually on. A Session with none cannot be measured at
  -- all: there is no server-pinned answer to compare against, and accepting the caller's bridge would let it
  -- open a second counter series for the same guest.
  IF v_iface IS NULL OR v_iface = '' THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % records no ingress interface', p_session;
  END IF;
  IF v_iface IS DISTINCT FROM p_bridge THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % is on %, not %', p_session, v_iface, p_bridge;
  END IF;
  -- ADDRESS + CLASS. The minor is a pure function of the Session's own address, so a caller cannot name a
  -- different guest's class, and a Session whose address changed cannot keep accruing against the old one.
  IF v_ip IS NULL THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % has no address to measure', p_session;
  END IF;
  IF iam_v2.p3_expected_class_minor(v_ip) IS DISTINCT FROM p_class_minor THEN
    RAISE EXCEPTION 'ACCT_SOURCE_MISMATCH: session % belongs to class %, not %',
      p_session, iam_v2.p3_expected_class_minor(v_ip), p_class_minor;
  END IF;
  IF p_sampled_at < v_started THEN
    RAISE EXCEPTION 'ACCT_INVALID: sample time % precedes the session start %', p_sampled_at, v_started;
  END IF;
  -- ONE AUTHORITATIVE SERIES. Every component of the checkpoint key is now pinned to the Session's own row,
  -- so a second source tuple for the same Session cannot be invented. This assertion states that explicitly
  -- rather than leaving it as a property somebody has to re-derive: if a stale checkpoint from a previous
  -- address or bridge still exists, it is not the one this observation may advance.
  SELECT count(*) INTO v_other FROM iam_v2.accounting_checkpoints cp2
   WHERE cp2.session_id = p_session
     AND (cp2.bridge, cp2.class_minor, cp2.source_device_id) IS DISTINCT FROM (p_bridge, p_class_minor, p_source_device);
  -- An ENDED session owns no further traffic. Refusing here (rather than only on the delta path) means an
  -- ended session cannot even establish a baseline, so nothing can later be billed against it.
  IF v_ended IS NOT NULL THEN
    RAISE EXCEPTION 'ACCT_SESSION_OUT_OF_SCOPE: session % ended at %', p_session, v_ended;
  END IF;

  -- CHECKPOINT: locked (or created) before anything is decided, so two runtimes observing the same counter
  -- cannot both compute a delta from the same previous value.
  SELECT * INTO cp FROM iam_v2.accounting_checkpoints
    WHERE session_id = p_session AND source_device_id = p_source_device
      AND bridge = p_bridge AND class_minor = p_class_minor
    FOR UPDATE;

  IF cp.id IS NULL THEN
    -- FIRST OBSERVATION. There is nothing to subtract from, so nothing is billed: the absolute counter becomes
    -- the baseline. Storing it as usage would charge the guest for everything since the class was created.
    INSERT INTO iam_v2.accounting_checkpoints
      (tenant_id, site_id, session_id, source_device_id, bridge, class_minor, source_epoch,
       prev_bytes_up, prev_bytes_down, prev_sampled_at, last_classification)
      VALUES (p_tenant, p_site, p_session, p_source_device, p_bridge, p_class_minor, p_epoch,
              p_abs_up, p_abs_down, p_sampled_at, 'BASELINED');
    RETURN 'BASELINED';
  END IF;

  IF p_epoch < cp.source_epoch THEN
    -- An older generation than the one already accepted is a stale or misrouted reading, never new usage.
    RAISE EXCEPTION 'ACCT_STALE_EPOCH: epoch % is older than the accepted epoch %', p_epoch, cp.source_epoch;
  END IF;

  IF p_epoch > cp.source_epoch THEN
    -- TRUSTED RESET. The TC owner says this managed class was replaced, so its counters legitimately restart.
    -- The new absolutes become the baseline; the bytes since the replacement are counted from here on.
    UPDATE iam_v2.accounting_checkpoints
       SET source_epoch = p_epoch, prev_bytes_up = p_abs_up, prev_bytes_down = p_abs_down,
           prev_sampled_at = p_sampled_at, last_classification = 'RESET_BASELINED', updated_at = now()
     WHERE id = cp.id;
    RETURN 'RESET_BASELINED';
  END IF;

  -- SAME EPOCH from here on.
  --
  -- TEMPORAL ORDER. Within one counter series, time only moves forward. An observation dated before the last
  -- accepted one is a delayed or misrouted delivery, and treating it as new usage would date a CURRENT delta
  -- into a HISTORICAL window — potentially one already frozen by a boundary watermark, where it would be
  -- recorded as delayed usage that never happened then.
  IF p_sampled_at < cp.prev_sampled_at THEN
    RAISE EXCEPTION 'ACCT_STALE_SAMPLE: sample at % precedes the last accepted sample at %',
      p_sampled_at, cp.prev_sampled_at;
  END IF;
  -- EQUAL sample times are explicitly ALLOWED when the counters advanced. The absolute counters are the
  -- authoritative evidence of how much was used; the timestamp only says when it was read. Two readings
  -- sharing an instant means the caller's clock is coarser than its sampling rate — a real and ordinary
  -- condition — and refusing the pair would throw away measured bytes to defend a precision assumption.
  -- A DECREASE at the same instant is still a regression and is refused below, and an identical pair is the
  -- replay case; neither can slip through here.

  IF p_abs_up = cp.prev_bytes_up AND p_abs_down = cp.prev_bytes_down THEN
    -- EXACT REPLAY. The counters have not moved since the last accepted observation, so whatever the caller
    -- believes about its own delivery, there is nothing new to store. This is what makes an uncertain commit
    -- safe: the retry sees the persisted state and is told so.
    --
    -- The persisted classification is reported so a caller retrying after an uncertain commit learns what
    -- actually happened to its observation — in particular whether it was ACCEPTED or landed in a frozen
    -- window as DELAYED. It is prefixed REPLAY: because the caller must be able to tell "your observation was
    -- accepted, just now" from "your observation was accepted, earlier": counting the second as fresh usage
    -- would make every retry look like new traffic in the daemon's own tallies and health.
    RETURN CASE WHEN cp.last_classification IN ('ACCEPTED','DELAYED')
                THEN 'REPLAY:' || cp.last_classification ELSE 'DUPLICATE' END;
  END IF;

  IF p_abs_up < cp.prev_bytes_up OR p_abs_down < cp.prev_bytes_down THEN
    -- A DECREASE WITHOUT A NEW EPOCH is ambiguous: it could be a silently recreated class, a misread, or a
    -- reused minor. Guessing "count from zero" would invent usage; guessing "ignore" would lose it. Fail closed
    -- and keep the checkpoint, so the next trustworthy observation can be judged against a known-good value.
    RAISE EXCEPTION 'ACCT_COUNTER_REGRESSION: counters went backwards without a new source epoch (up %->%, down %->%)',
      cp.prev_bytes_up, p_abs_up, cp.prev_bytes_down, p_abs_down;
  END IF;

  v_up := p_abs_up - cp.prev_bytes_up;
  v_down := p_abs_down - cp.prev_bytes_down;

  -- ATTRIBUTION at SAMPLE time, through the ONE shared resolver (iam_v2.p3_entitlement_at). There is no
  -- fallback to the session's current entitlement anywhere in this chain.
  v_ent := iam_v2.p3_entitlement_at(p_session, p_sampled_at);
  IF v_ent IS NULL THEN
    RAISE EXCEPTION 'ACCT_NO_BINDING: no entitlement was bound to session % at %', p_session, p_sampled_at;
  END IF;

  -- the per-session record sequence is allocated under the checkpoint lock, so it cannot collide
  SELECT COALESCE(max(sample_seq),0)+1 INTO v_seq FROM iam_v2.accounting_records WHERE session_id = p_session;
  INSERT INTO iam_v2.accounting_records
    (tenant_id, site_id, session_id, sample_seq, bytes_up, bytes_down, sampled_at, ingested_at)
    VALUES (p_tenant, p_site, p_session, v_seq, v_up, v_down, p_sampled_at, now())
    RETURNING id INTO v_rec;

  UPDATE iam_v2.sessions SET bytes_up = bytes_up + v_up, bytes_down = bytes_down + v_down
   WHERE id = p_session;

  -- THE ENTITLEMENT'S OWN AGGREGATE, ADVANCED IN THE SAME TRANSACTION AS THE RECORD THAT JUSTIFIES IT.
  --
  -- THIS IS THE LINE THAT WAS MISSING. Every other total was maintained here -- the accounting record, the
  -- session's bytes, the checkpoint -- and the ENTITLEMENT's was not, so consumed_data_bytes sat at 0 and
  -- usage_version at 0 for the entire life of every entitlement this appliance has ever granted. The column
  -- is not decorative: the approved contract says the allowance belongs to the Entitlement, and this is where
  -- the Entitlement records what it has spent. A guest who had used 35 MB of a 100 MB package was recorded,
  -- authoritatively, as having used nothing.
  --
  -- (The OTHER ingestion function, iam_v2.ingest_sample from the iam_base engine, always did this. It is not
  -- the one acctd calls. Two implementations of one operation, and the running daemon used the one that
  -- forgot -- which is also why no test caught it: every quota test writes accounting_records directly.)
  --
  -- ATTRIBUTED TO v_ent, NOT TO THE SESSION'S CURRENT ENTITLEMENT. v_ent came from p3_entitlement_at at THIS
  -- sample's own time, so a Phase-5 transfer charges each sample to whoever actually owned the session then.
  -- It is the same attribution p6_data_crossing uses, so the counter and the crossing cannot disagree.
  --
  -- EXACTLY ONCE, and every guard that makes that true has already run above: a replay returned at the
  -- DUPLICATE branch, an out-of-order sample at ACCT_STALE_SAMPLE, a regression at ACCT_COUNTER_REGRESSION,
  -- and a re-baselined epoch returned before reaching here. Only a genuinely new delta arrives at this line.
  --
  -- A NEW ADDRESS, DEVICE OR SESSION CANNOT RESET IT: the row updated is the entitlement's, not the
  -- session's, and the value only ever increases. The ent_guard trigger refuses any decrease that is not an
  -- audited adjustment, so this cannot silently walk backwards either.
  UPDATE iam_v2.entitlements
     SET consumed_data_bytes = consumed_data_bytes + (v_up + v_down),
         usage_version = usage_version + 1
   WHERE id = v_ent;

  SELECT EXISTS (SELECT 1 FROM iam_v2.delayed_accounting_records WHERE accounting_record_id = v_rec)
    INTO v_delayed;
  v_class := CASE WHEN v_delayed THEN 'DELAYED' ELSE 'ACCEPTED' END;

  UPDATE iam_v2.accounting_checkpoints
     SET prev_bytes_up = p_abs_up, prev_bytes_down = p_abs_down, prev_sampled_at = p_sampled_at,
         last_record_id = v_rec, last_classification = v_class, updated_at = now()
   WHERE id = cp.id;
  RETURN v_class;
END $fn$;
REVOKE EXECUTE ON FUNCTION iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz) FROM PUBLIC;

-- CREATE OR REPLACE keeps existing grants, but the daemon losing EXECUTE here would stop all accounting, so
-- it is re-asserted rather than assumed. Dynamic, because a scratch database may not have the service roles.
DO $ingrant$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,'
         || 'bigint,bigint,bigint,timestamptz) TO svc_acctd';
  END IF;
END $ingrant$;

-- ---------------------------------------------------------------------------------------------------------
-- 3. One-time reconciliation of what the defect left behind
-- ---------------------------------------------------------------------------------------------------------
-- DETERMINISTIC, AUDITABLE, NON-DESTRUCTIVE, AND DERIVED FROM NOTHING BUT AUTHORITATIVE ACCOUNTING. Every
-- counter is set to the sum of the accounting records already attributed to it. No accounting record, no
-- Purchase and no accepted evidence is rewritten; only the counter that was never written at all is written.
--
-- INCREASE-ONLY. If a counter somehow already exceeds the derived sum it is LEFT ALONE and reported, because
-- lowering it would be an unaudited decrease -- the exact thing entitlement_adjustments exists to prevent.
--
-- IT DOES NOT TERMINATE ANYTHING. Reconciling a counter is not an enforcement decision. If reconciliation
-- reveals an entitlement whose usage has already crossed its quota, the expiry sweep finds it on its very
-- next pass through the ONE sanctioned path (p6_expire_entitlement), which re-derives the crossing instant
-- from the accounting series and ends access AT THAT instant. Terminating here would date the ending to the
-- migration instead of to the sample that crossed it.
DO $recon$
DECLARE
  r          record;
  n_fixed    int := 0;
  n_crossed  int := 0;
BEGIN
  FOR r IN
    SELECT e.id, e.consumed_data_bytes AS was, iam_v2.p3_entitlement_data_usage(e.id) AS derived,
           e.tenant_id, e.site_id, spr.data_quota_bytes AS quota
      FROM iam_v2.entitlements e
      LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = e.service_plan_revision_id
     ORDER BY e.id
  LOOP
    IF r.derived > r.was THEN
      UPDATE iam_v2.entitlements
         SET consumed_data_bytes = r.derived, usage_version = usage_version + 1
       WHERE id = r.id;
      INSERT INTO iam_v2.entitlement_adjustments
        (tenant_id, site_id, entitlement_id, field, old_value, new_value, actor, reason)
        VALUES (r.tenant_id, r.site_id, r.id, 'consumed_data_bytes', r.was::text, r.derived::text, NULL,
                'MIGRATION 0061 one-time reconciliation: the ingestion operation never advanced this counter. '
                'Set to the sum of the accounting records already attributed to this Entitlement. No '
                'accounting record was altered.');
      n_fixed := n_fixed + 1;
    ELSIF r.derived < r.was THEN
      RAISE WARNING 'entitlement % records % bytes but only % are attributable; left unchanged for an audited adjustment',
        r.id, r.was, r.derived;
    END IF;
    IF r.quota IS NOT NULL AND r.derived >= r.quota THEN
      n_crossed := n_crossed + 1;
      RAISE WARNING 'entitlement % has already crossed its quota (% of %); the expiry sweep will end it at the crossing sample',
        r.id, r.derived, r.quota;
    END IF;
  END LOOP;
  RAISE NOTICE 'migration 0061: % entitlement counter(s) reconciled, % already over quota', n_fixed, n_crossed;
END $recon$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. The invariant, asserted here rather than hoped for
-- ---------------------------------------------------------------------------------------------------------
DO $verify$
DECLARE bad int;
BEGIN
  SELECT count(*) INTO bad FROM iam_v2.entitlements e
   WHERE e.consumed_data_bytes <> iam_v2.p3_entitlement_data_usage(e.id);
  IF bad > 0 THEN
    RAISE EXCEPTION 'migration 0061: % entitlement(s) still disagree with their attributed accounting', bad;
  END IF;
END $verify$;

COMMIT;
