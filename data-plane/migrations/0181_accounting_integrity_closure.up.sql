-- 0181 -- ACCOUNTING INTEGRITY CLOSURE (follow-up to 0175 and 0180; PO mission "accounting integrity, permanent solutions")
--
-- 0180 made an Entitlement's usage survive the raw purge. Reviewing it end to end against interrupted writes, replays,
-- retroactive bindings, reboots, long sessions, transfers and termination found one real quota hole and several places where an
-- uncertain number could be read in the permissive direction. This migration closes them. Nothing here fabricates history: where
-- history is missing the result is the CONSERVATIVE direction for the quota (never lower than the truth), and every such case
-- is recorded in iam_v2.accounting_integrity_events or visible through iam_v2.accounting_integrity_summary().
--
--  1. THE QUOTA HOLE: sample_seq was max(raw sample_seq)+1. The raw purge deletes samples, so a session whose samples were all
--     purged (idle for longer than raw_retention_days, then active again) restarted at sample_seq 1, BELOW the crossing
--     checkpoint (data_crossing_progress.examined_seq). p6_advance_data_crossing and p6_data_crossing read only
--     "sample_seq > examined_seq", so every new sample up to the old high-water mark was never counted toward the crossing and the
--     data quota could be passed. FIX: accounting_checkpoints.last_sample_seq, advanced by ingest_absolute_counters, makes the
--     sequence monotonic for the life of the session (the unique (session_id, sample_seq) index still holds). Existing
--     checkpoints are backfilled from max(raw sample_seq, examined_seq). Sessions already hit by the defect are repaired by (3).
--  2. entitlement_usage_bytes(p_ent, p_at): a folded hour that straddles p_at (it holds samples after p_at that can no longer be
--     split out) was LEFT OUT, i.e. a lower bound: the checkout quota test could read less usage than the guest had. It is now
--     COUNTED, i.e. an upper bound, over by at most the usage of that one hour; the result is clamped so latest_sampled_at never
--     exceeds p_at. iam_v2.entitlement_usage_uncertainty() says how much of a figure is uncertain.
--  3. p6_reconcile_crossing_checkpoint(ent): lifts the entitlement's crossing checkpoint to what the folded accumulator plus the
--     raw samples already examined say it must be (never lowers it). Run once here for every live entitlement (repairs sessions
--     hit by (1), a binding with no checkpoint row, any understatement) and by (4).
--  4. rebind_session_entitlement(session, ent, p_at): a binding written for an instant older than raw_retention_days moves samples
--     the purge has ALREADY folded into the previous entitlement's accumulator, and the fold cannot be redone. The new
--     entitlement is now charged, conservatively, with the session's folded hour-tier bytes from the hour of p_at onwards
--     (exact = false), the previous entitlement keeps what it was charged (an overcount in total, never an undercount), and an
--     event is recorded whenever p_at is older than the raw retention (even when nothing is found folded: the purge may run at
--     the same moment).
--  5. p6_advance_data_crossing: records an event when the crossing instant is approximate (the quota was reached inside the
--     folded part, so the instant is the latest sample time of the crossing hour, not the sample).
--  6. The 0180 backfill's inexact accumulator rows (attribution by hour, not by sample) are recorded per entitlement.
--
-- Non-destructive: a new table, a new column, new functions and CREATE OR REPLACE of four functions (signatures, owners and ACLs
-- unchanged). The .down restores the 0061/0010/0180 definitions and drops what this adds.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 0. The audit trail operators can see.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.accounting_integrity_events (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL,
  site_id        uuid NOT NULL,
  entitlement_id uuid,
  session_id     uuid,
  kind           text NOT NULL CONSTRAINT aie_kind CHECK (kind IN (
                   'CHECKPOINT_BELOW_EXACT', 'RETROACTIVE_BINDING_PAST_RAW_RETENTION', 'CROSSING_INSTANT_APPROXIMATE',
                   'BACKFILL_INEXACT_ATTRIBUTION')),
  bytes          bigint CONSTRAINT aie_bytes CHECK (bytes IS NULL OR bytes >= 0),
  detail         jsonb NOT NULL DEFAULT '{}'::jsonb,
  recorded_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS aie_site_kind ON iam_v2.accounting_integrity_events (tenant_id, site_id, kind, recorded_at DESC);
COMMENT ON TABLE iam_v2.accounting_integrity_events IS
  'Every place the accounting could not be exact and chose the conservative (never lower) quota direction instead (0181): a '
  'checkpoint lifted to the exact figure, a binding written into history the raw purge had already folded, a crossing instant '
  'that is only known to the hour, an inexact backfilled attribution. No guest data: ids, byte counts and instants only. Read '
  'through iam_v2.accounting_integrity_summary().';

-- ---------------------------------------------------------------------------------------------------------
-- 1. A sample sequence that survives the purge.
-- ---------------------------------------------------------------------------------------------------------
LOCK TABLE iam_v2.accounting_checkpoints IN SHARE ROW EXCLUSIVE MODE;
ALTER TABLE iam_v2.accounting_checkpoints ADD COLUMN IF NOT EXISTS last_sample_seq bigint NOT NULL DEFAULT 0;
COMMENT ON COLUMN iam_v2.accounting_checkpoints.last_sample_seq IS
  'The highest accounting_records.sample_seq ever allocated for the session through this checkpoint (0181). Survives the raw purge, '
  'so the per-session sequence never restarts below the crossing checkpoint.';
UPDATE iam_v2.accounting_checkpoints c
   SET last_sample_seq = greatest(c.last_sample_seq,
         COALESCE((SELECT max(r.sample_seq) FROM iam_v2.accounting_records r WHERE r.session_id = c.session_id), 0),
         COALESCE((SELECT max(p.examined_seq) FROM iam_v2.data_crossing_progress p WHERE p.session_id = c.session_id), 0));

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
  -- 0181: monotonic for the life of the session, NOT max(raw)+1. The raw purge deletes samples, and a session whose samples
  -- were all purged (idle past raw_retention_days) restarted at 1, below the crossing checkpoint's examined_seq, so
  -- p6_advance_data_crossing / p6_data_crossing never read the new samples and the data quota could be passed.
  SELECT greatest(COALESCE((SELECT max(r0.sample_seq) FROM iam_v2.accounting_records r0 WHERE r0.session_id = p_session), 0),
                  COALESCE((SELECT max(c2.last_sample_seq) FROM iam_v2.accounting_checkpoints c2 WHERE c2.session_id = p_session), 0)) + 1
    INTO v_seq;
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
         last_record_id = v_rec, last_classification = v_class, last_sample_seq = v_seq, updated_at = now()
   WHERE id = cp.id;
  RETURN v_class;
END $fn$;


-- ---------------------------------------------------------------------------------------------------------
-- 2. Readers: a straddling folded hour is counted (upper bound), and the uncertainty is queryable.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.entitlement_usage_bytes(p_ent uuid, p_at timestamptz)
RETURNS TABLE(bytes_up bigint, bytes_down bigint, records bigint, latest_sampled_at timestamptz)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
  WITH folded AS (
    SELECT sum(a.bytes_up) AS up, sum(a.bytes_down) AS down, sum(a.records) AS n, max(a.latest_sampled_at) AS latest
      FROM iam_v2.accounting_usage_entitlement a
     WHERE a.entitlement_id = p_ent AND a.arm IN (0, 2)
       AND a.bucket <= p_at),
  live AS (
    SELECT sum(r.bytes_up) AS up, sum(r.bytes_down) AS down, count(*) AS n, max(r.sampled_at) AS latest
      FROM (
        SELECT ar.bytes_up, ar.bytes_down, ar.sampled_at
          FROM iam_v2.session_entitlement_bindings b
          JOIN iam_v2.accounting_records ar ON ar.session_id = b.session_id
           AND ar.sampled_at >= b.bound_from AND (b.bound_until IS NULL OR ar.sampled_at < b.bound_until)
          JOIN iam_v2.sessions s ON s.id = ar.session_id
         WHERE b.entitlement_id = p_ent AND ar.sampled_at <= p_at
        UNION ALL
        SELECT ar.bytes_up, ar.bytes_down, ar.sampled_at
          FROM iam_v2.sessions s
          JOIN iam_v2.accounting_records ar ON ar.session_id = s.id
         WHERE s.entitlement_id = p_ent AND ar.sampled_at <= p_at
           AND NOT EXISTS (SELECT 1 FROM iam_v2.session_entitlement_bindings b2 WHERE b2.session_id = s.id)
      ) r)
  SELECT (COALESCE(f.up, 0) + COALESCE(l.up, 0))::bigint,
         (COALESCE(f.down, 0) + COALESCE(l.down, 0))::bigint,
         (COALESCE(f.n, 0) + COALESCE(l.n, 0))::bigint,
         CASE WHEN greatest(f.latest, l.latest) IS NULL THEN NULL ELSE least(greatest(f.latest, l.latest), p_at) END
    FROM folded f, live l;
$$;

COMMENT ON FUNCTION iam_v2.entitlement_usage_bytes(uuid, timestamptz) IS
  'Bytes attributed to ONE Entitlement up to ONE instant, by binding interval, with the no-binding fallback '
  'for sessions that predate the binding table. SECURITY DEFINER so a caller can obtain the aggregate without '
  'holding SELECT on iam_v2.accounting_records -- svc_pmsd needs the number at a checkout boundary and Gate-P '
  'forbids it the table. Adds the samples the purge has folded into iam_v2.accounting_usage_entitlement (0180). Since 0181 a '
  'folded hour that straddles p_at is COUNTED whole: the answer is exact, or an UPPER bound over by at most that hour (never '
  'a lower bound; see entitlement_usage_uncertainty). It returns four aggregate numbers about an entitlement the caller must '
  'already be able to name; it exposes no guest, Stay, reservation, folio, payment or PMS data, and it writes nothing.';

CREATE OR REPLACE FUNCTION iam_v2.entitlement_usage_uncertainty(p_ent uuid, p_at timestamptz)
RETURNS TABLE(straddling_bytes bigint, inexact_bytes bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp
AS $$
  SELECT COALESCE(sum(a.bytes_up + a.bytes_down) FILTER (WHERE a.arm IN (0, 2) AND a.bucket <= p_at
                                                          AND a.bucket + interval '1 hour' > p_at AND a.latest_sampled_at > p_at), 0)::bigint,
         COALESCE(sum(a.bytes_up + a.bytes_down) FILTER (WHERE a.arm IN (0, 2) AND a.bucket <= p_at AND NOT a.exact), 0)::bigint
    FROM iam_v2.accounting_usage_entitlement a
   WHERE a.entitlement_id = p_ent;
$$;
COMMENT ON FUNCTION iam_v2.entitlement_usage_uncertainty(uuid, timestamptz) IS
  'How much of entitlement_usage_bytes(p_ent, p_at) is not a sample-exact figure (0181): straddling_bytes = the folded hour that '
  'holds samples after p_at and was counted whole (the result may exceed the truth by at most this); inexact_bytes = folded rows '
  'whose attribution was estimated (0180 backfill, 0181 retroactive-binding charge). Both zero = the figure is exact.';

CREATE OR REPLACE FUNCTION iam_v2.accounting_integrity_summary(p_tenant uuid, p_site uuid)
RETURNS TABLE(kind text, events bigint, entitlements bigint, bytes bigint, last_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp
AS $$
  SELECT e.kind, count(*)::bigint, count(DISTINCT e.entitlement_id)::bigint, COALESCE(sum(e.bytes), 0)::bigint, max(e.recorded_at)
    FROM iam_v2.accounting_integrity_events e
   WHERE e.tenant_id = p_tenant AND e.site_id = p_site
   GROUP BY e.kind
  UNION ALL
  SELECT 'FOLDED_BUCKET_INEXACT'::text, count(*)::bigint, count(DISTINCT a.entitlement_id)::bigint,
         COALESCE(sum(a.bytes_up + a.bytes_down), 0)::bigint, max(a.latest_sampled_at)
    FROM iam_v2.accounting_usage_entitlement a
   WHERE a.tenant_id = p_tenant AND a.site_id = p_site AND NOT a.exact
  HAVING count(*) > 0;
$$;
COMMENT ON FUNCTION iam_v2.accounting_integrity_summary(uuid, uuid) IS
  'Per kind: how many times the accounting chose the conservative direction instead of an exact figure at this site, for how many '
  'entitlements and bytes, and when last (0181). Empty = every figure is sample-exact.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. The crossing checkpoint can only be lifted to the exact figure, never lowered.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p6_reconcile_crossing_checkpoint(p_entitlement uuid) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$
DECLARE v_t uuid; v_s uuid; v_folded bigint; v_raw bigint; v_counted bigint; v_def bigint; v_first uuid; v_sess uuid;
BEGIN
  SELECT tenant_id, site_id INTO v_t, v_s FROM iam_v2.entitlements WHERE id = p_entitlement;
  IF NOT FOUND THEN RETURN 0; END IF;
  SELECT COALESCE(sum(a.bytes_up + a.bytes_down), 0) INTO v_folded
    FROM iam_v2.accounting_usage_entitlement a WHERE a.entitlement_id = p_entitlement AND a.arm = 0;
  -- the raw samples a checkpoint must already include: those at or before the binding's examined_seq
  SELECT COALESCE(sum(ar.bytes_up + ar.bytes_down), 0) INTO v_raw
    FROM iam_v2.session_entitlement_bindings sb
    JOIN iam_v2.data_crossing_progress pr ON pr.binding_id = sb.id
    JOIN iam_v2.accounting_records ar ON ar.session_id = sb.session_id AND ar.sample_seq <= pr.examined_seq
     AND sb.bound_from <= ar.sampled_at AND (sb.bound_until IS NULL OR sb.bound_until > ar.sampled_at)
   WHERE sb.entitlement_id = p_entitlement;
  SELECT COALESCE(sum(pr.counted_bytes), 0) INTO v_counted FROM iam_v2.data_crossing_progress pr WHERE pr.entitlement_id = p_entitlement;
  v_def := v_folded + v_raw - v_counted;
  IF v_def <= 0 THEN RETURN 0; END IF;
  SELECT pr.binding_id INTO v_first FROM iam_v2.data_crossing_progress pr
   WHERE pr.entitlement_id = p_entitlement ORDER BY pr.binding_id LIMIT 1;
  IF v_first IS NULL THEN
    SELECT sb.id INTO v_first FROM iam_v2.session_entitlement_bindings sb
     WHERE sb.entitlement_id = p_entitlement ORDER BY sb.bound_from, sb.id LIMIT 1;
    IF v_first IS NULL THEN RETURN 0; END IF;
  END IF;
  SELECT sb.session_id INTO v_sess FROM iam_v2.session_entitlement_bindings sb WHERE sb.id = v_first;
  INSERT INTO iam_v2.data_crossing_progress AS pr (binding_id, tenant_id, site_id, entitlement_id, session_id, examined_seq, counted_bytes, final)
  VALUES (v_first, v_t, v_s, p_entitlement, v_sess, 0, v_def, false)
  ON CONFLICT (binding_id) DO UPDATE SET counted_bytes = pr.counted_bytes + EXCLUDED.counted_bytes, updated_at = now();
  INSERT INTO iam_v2.accounting_integrity_events (tenant_id, site_id, entitlement_id, session_id, kind, bytes, detail)
  VALUES (v_t, v_s, p_entitlement, v_sess, 'CHECKPOINT_BELOW_EXACT', v_def,
          jsonb_build_object('folded', v_folded, 'raw_examined', v_raw, 'checkpoint_before', v_counted));
  RETURN v_def;
END $$;
COMMENT ON FUNCTION iam_v2.p6_reconcile_crossing_checkpoint(uuid) IS
  'Lifts an entitlement''s data-crossing checkpoint to folded accumulator + the raw samples already examined when it is lower '
  '(0181); never lowers it, never terminates anything. Returns the bytes added and records an integrity event.';
REVOKE ALL ON FUNCTION iam_v2.p6_reconcile_crossing_checkpoint(uuid) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 4. Rebinding into history the purge has already folded.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.rebind_session_entitlement(p_session uuid, p_ent uuid, p_at timestamptz) RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_t uuid; v_s uuid; v_open uuid; v_from timestamptz; v_cur uuid; v_seq bigint; v_id uuid;
        v_days integer; v_cut timestamptz; v_wm timestamptz; v_hb bigint; v_hn bigint; v_rb bigint; v_rn bigint; v_gap bigint;
BEGIN
  -- This operation writes a capability-scoped family, so it declares its own scope. Doing it here
  -- rather than relying on ownership is what lets Gate-P give every function its own owner without
  -- any of them losing the right to perform its own writes.
  PERFORM iam_v2.begin_controlled_operation('session_binding');
  SELECT tenant_id, site_id, entitlement_id INTO v_t, v_s, v_cur FROM iam_v2.sessions WHERE id = p_session FOR UPDATE;
  IF v_t IS NULL THEN RAISE EXCEPTION 'session % not found', p_session; END IF;
  PERFORM 1 FROM iam_v2.entitlements WHERE id = p_ent AND tenant_id = v_t AND site_id = v_s;
  IF NOT FOUND THEN RAISE EXCEPTION 'entitlement % is not in the session scope', p_ent; END IF;
  SELECT id, bound_from INTO v_open, v_from FROM iam_v2.session_entitlement_bindings
    WHERE session_id = p_session AND bound_until IS NULL;
  IF v_open IS NOT NULL THEN
    IF p_at < v_from THEN RAISE EXCEPTION 'rebinding at % precedes the open interval start %', p_at, v_from; END IF;
    UPDATE iam_v2.session_entitlement_bindings SET bound_until = p_at WHERE id = v_open;
  END IF;
  SELECT COALESCE(max(seq),0)+1 INTO v_seq FROM iam_v2.session_entitlement_bindings WHERE session_id = p_session;
  INSERT INTO iam_v2.session_entitlement_bindings(tenant_id,site_id,session_id,entitlement_id,seq,bound_from)
    VALUES (v_t,v_s,p_session,p_ent,v_seq,p_at) RETURNING id INTO v_id;
  UPDATE iam_v2.sessions SET entitlement_id = p_ent WHERE id = p_session;

  -- 0181. A binding for an instant older than the raw retention may take over samples the purge has ALREADY folded into the
  -- previous entitlement's accumulator; that fold cannot be redone (the samples are gone). Missing history is never invented:
  -- the new entitlement is charged the session's folded hour-tier bytes from the hour of p_at onwards (an upper bound -- it
  -- includes the part of the first hour before p_at and anything the previous binding legitimately kept), marked exact = false,
  -- the previous entitlement keeps what it was charged, and the event says so. An event is written whenever p_at is older than
  -- the retention, even if nothing is found folded yet (a purge may be running).
  IF p_at < now() - interval '1 day' THEN
    SELECT g.raw_retention_days INTO v_days FROM iam_v2.usage_rollup_settings_get(v_t, v_s) g;
    v_cut := now() - make_interval(days => COALESCE(v_days, 2));
    IF p_at < v_cut THEN
      SELECT watermark INTO v_wm FROM iam_v2.site_usage_rollup_state WHERE tenant_id = v_t AND site_id = v_s;
      SELECT COALESCE(sum(h.bytes_up + h.bytes_down), 0), COALESCE(sum(h.samples), 0) INTO v_hb, v_hn
        FROM iam_v2.accounting_usage_hour h WHERE h.session_id = p_session AND h.bucket >= date_trunc('hour', p_at);
      SELECT COALESCE(sum(ar.bytes_up + ar.bytes_down), 0), count(*) INTO v_rb, v_rn
        FROM iam_v2.accounting_records ar
       WHERE ar.session_id = p_session AND ar.sampled_at >= date_trunc('hour', p_at)
         AND v_wm IS NOT NULL AND ar.ingested_at < v_wm;
      v_gap := greatest(v_hb - v_rb, 0);
      IF v_gap > 0 THEN
        INSERT INTO iam_v2.accounting_usage_entitlement AS t
               (tenant_id, site_id, entitlement_id, arm, bucket, bytes_up, bytes_down, records, latest_sampled_at, exact)
        VALUES (v_t, v_s, p_ent, 0, date_trunc('hour', p_at), 0, v_gap, greatest(v_hn - v_rn, 0), p_at, false)
        ON CONFLICT (entitlement_id, bucket, arm) DO UPDATE
           SET bytes_down = t.bytes_down + EXCLUDED.bytes_down, records = t.records + EXCLUDED.records,
               latest_sampled_at = greatest(t.latest_sampled_at, EXCLUDED.latest_sampled_at), exact = false;
      END IF;
      INSERT INTO iam_v2.accounting_integrity_events (tenant_id, site_id, entitlement_id, session_id, kind, bytes, detail)
      VALUES (v_t, v_s, p_ent, p_session, 'RETROACTIVE_BINDING_PAST_RAW_RETENTION', v_gap,
              jsonb_build_object('bound_from', p_at, 'raw_retention_days', COALESCE(v_days, 2), 'bucket', date_trunc('hour', p_at),
                                 'charged_bytes_down', v_gap, 'charged_records', greatest(v_hn - v_rn, 0)));
      IF v_gap > 0 THEN
        PERFORM iam_v2.p6_reconcile_crossing_checkpoint(p_ent);
      END IF;
    END IF;
  END IF;
  RETURN v_id;
END $fn$;


-- ---------------------------------------------------------------------------------------------------------
-- 5. p6_advance_data_crossing: identical to 0180 plus the event for an approximate crossing instant.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.p6_advance_data_crossing(p_tenant uuid, p_site uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE e record; b record; v_new bigint; v_max bigint; v_est numeric; v_exact timestamptz; n int := 0;
        v_folded bigint; v_first uuid;
BEGIN
  FOR e IN
    SELECT en.id, COALESCE(en.data_quota_bytes, spr.data_quota_bytes) AS quota
      FROM iam_v2.entitlements en
      LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = en.service_plan_revision_id
     WHERE en.tenant_id = p_tenant AND en.site_id = p_site
       AND en.status IN ('ACTIVE', 'PENDING', 'SUSPENDED')
       AND COALESCE(en.data_quota_bytes, spr.data_quota_bytes) IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM iam_v2.data_crossing_state s
                        WHERE s.entitlement_id = en.id AND s.crossed_at IS NOT NULL
                          AND s.quota_bytes = COALESCE(en.data_quota_bytes, spr.data_quota_bytes))
     ORDER BY en.id
  LOOP
    n := n + 1;
    FOR b IN
      SELECT sb.id, sb.session_id, sb.bound_from, sb.bound_until, se.ended,
             COALESCE(pr.examined_seq, 0) AS seq, COALESCE(pr.final, false) AS final
        FROM iam_v2.session_entitlement_bindings sb
        JOIN iam_v2.sessions se ON se.id = sb.session_id
        LEFT JOIN iam_v2.data_crossing_progress pr ON pr.binding_id = sb.id
       WHERE sb.entitlement_id = e.id
    LOOP
      IF b.final THEN
        CONTINUE;   -- an ended session's records are complete; there is nothing new to read
      END IF;
      -- ONLY the records after the checkpoint: the unique (session_id, sample_seq) index answers this directly.
      SELECT COALESCE(sum(CASE WHEN b.bound_from <= ar.sampled_at
                                    AND (b.bound_until IS NULL OR b.bound_until > ar.sampled_at)
                               THEN ar.bytes_up + ar.bytes_down ELSE 0 END), 0),
             max(ar.sample_seq)
        INTO v_new, v_max
        FROM iam_v2.accounting_records ar
       WHERE ar.session_id = b.session_id AND ar.sample_seq > b.seq;
      INSERT INTO iam_v2.data_crossing_progress AS pr
        (binding_id, tenant_id, site_id, entitlement_id, session_id, examined_seq, counted_bytes, final)
      VALUES (b.id, p_tenant, p_site, e.id, b.session_id, COALESCE(v_max, b.seq), v_new,
              -- ingestion refuses an ended session outright, so once it has been ended for a while (longer than
              -- any in-flight observation could take) its record set is closed for good
              b.ended IS NOT NULL AND b.ended < now() - interval '5 minutes')
      ON CONFLICT (binding_id) DO UPDATE
        SET examined_seq = GREATEST(pr.examined_seq, EXCLUDED.examined_seq),
            counted_bytes = pr.counted_bytes + EXCLUDED.counted_bytes,
            final = EXCLUDED.final, updated_at = now();
    END LOOP;

    SELECT COALESCE(sum(pr.counted_bytes), 0) INTO v_est
      FROM iam_v2.data_crossing_progress pr WHERE pr.entitlement_id = e.id;
    IF v_est >= e.quota THEN
      -- The running total says the quota may have been reached: establish the exact instant ONCE.
      v_exact := iam_v2.p6_data_crossing_full(e.id);
      IF v_exact IS NULL THEN
        -- The checkpoint overstated (an attribution interval shrank after it counted). Recount every binding of
        -- this entitlement exactly, so the estimate is honest again and this branch is not taken every tick.
        -- The samples the purge has already folded are no longer raw: they are carried by the first checkpoint row.
        SELECT COALESCE(sum(a.bytes_up + a.bytes_down), 0) INTO v_folded
          FROM iam_v2.accounting_usage_entitlement a WHERE a.entitlement_id = e.id AND a.arm = 0;
        SELECT p2.binding_id INTO v_first FROM iam_v2.data_crossing_progress p2
         WHERE p2.entitlement_id = e.id ORDER BY p2.binding_id LIMIT 1;
        UPDATE iam_v2.data_crossing_progress pr
           SET counted_bytes = COALESCE((
                 SELECT sum(ar.bytes_up + ar.bytes_down)
                   FROM iam_v2.accounting_records ar
                   JOIN iam_v2.session_entitlement_bindings sb ON sb.id = pr.binding_id
                  WHERE ar.session_id = pr.session_id AND ar.sample_seq <= pr.examined_seq
                    AND sb.bound_from <= ar.sampled_at
                    AND (sb.bound_until IS NULL OR sb.bound_until > ar.sampled_at)), 0)
                 + CASE WHEN pr.binding_id = v_first THEN v_folded ELSE 0 END,
               updated_at = now()
         WHERE pr.entitlement_id = e.id;
      END IF;
      IF v_exact IS NOT NULL AND COALESCE((SELECT sum(a.bytes_up + a.bytes_down) FROM iam_v2.accounting_usage_entitlement a
                                            WHERE a.entitlement_id = e.id AND a.arm = 0), 0) >= e.quota THEN
        -- the quota was reached inside the folded part: the instant is the latest sample time of the crossing hour (0181 event)
        INSERT INTO iam_v2.accounting_integrity_events (tenant_id, site_id, entitlement_id, kind, bytes, detail)
        VALUES (p_tenant, p_site, e.id, 'CROSSING_INSTANT_APPROXIMATE', e.quota, jsonb_build_object('crossed_at', v_exact));
      END IF;
      INSERT INTO iam_v2.data_crossing_state AS s (entitlement_id, tenant_id, site_id, quota_bytes, crossed_at)
      VALUES (e.id, p_tenant, p_site, e.quota, v_exact)
      ON CONFLICT (entitlement_id) DO UPDATE
        SET quota_bytes = EXCLUDED.quota_bytes, crossed_at = EXCLUDED.crossed_at, verified_at = now();
    END IF;
  END LOOP;
  RETURN n;
END $$;


-- ---------------------------------------------------------------------------------------------------------
-- 6. Ownership, grants.
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.accounting_integrity_events OWNER TO iam_v2_owner';
  END IF;
END $own$;
REVOKE ALL ON TABLE iam_v2.accounting_integrity_events FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.entitlement_usage_uncertainty(uuid, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.accounting_integrity_summary(uuid, uuid) FROM PUBLIC;
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.accounting_integrity_summary(uuid, uuid) TO svc_edged;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.entitlement_usage_uncertainty(uuid, timestamptz) TO svc_pmsd;
  END IF;
END $g$;

-- ---------------------------------------------------------------------------------------------------------
-- 7. One-off: record the 0180 backfill's inexact rows, and lift every live entitlement's checkpoint to the exact figure.
-- ---------------------------------------------------------------------------------------------------------
INSERT INTO iam_v2.accounting_integrity_events (tenant_id, site_id, entitlement_id, kind, bytes, detail)
SELECT a.tenant_id, a.site_id, a.entitlement_id, 'BACKFILL_INEXACT_ATTRIBUTION',
       sum(a.bytes_up + a.bytes_down)::bigint, jsonb_build_object('buckets', count(*))
  FROM iam_v2.accounting_usage_entitlement a WHERE NOT a.exact
 GROUP BY a.tenant_id, a.site_id, a.entitlement_id;

DO $recon$
DECLARE e record; n integer := 0;
BEGIN
  FOR e IN
    SELECT en.id FROM iam_v2.entitlements en
      LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = en.service_plan_revision_id
     WHERE en.status IN ('ACTIVE', 'PENDING', 'SUSPENDED')
       AND COALESCE(en.data_quota_bytes, spr.data_quota_bytes) IS NOT NULL
       AND (EXISTS (SELECT 1 FROM iam_v2.accounting_usage_entitlement a WHERE a.entitlement_id = en.id)
            OR EXISTS (SELECT 1 FROM iam_v2.data_crossing_progress p WHERE p.entitlement_id = en.id))
  LOOP
    IF iam_v2.p6_reconcile_crossing_checkpoint(e.id) > 0 THEN n := n + 1; END IF;
  END LOOP;
  RAISE NOTICE '0181: % entitlement checkpoint(s) lifted to the exact figure', n;
END $recon$;

-- ---------------------------------------------------------------------------------------------------------
-- PROVE THE PROPERTIES on the database this just changed.
-- ---------------------------------------------------------------------------------------------------------
DO $verify$
DECLARE p pg_proc%ROWTYPE; f text;
BEGIN
  FOREACH f IN ARRAY ARRAY['iam_v2.entitlement_usage_bytes(uuid, timestamptz)', 'iam_v2.p6_advance_data_crossing(uuid, uuid)',
                           'iam_v2.rebind_session_entitlement(uuid, uuid, timestamptz)',
                           'iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)',
                           'iam_v2.p6_reconcile_crossing_checkpoint(uuid)', 'iam_v2.entitlement_usage_uncertainty(uuid, timestamptz)',
                           'iam_v2.accounting_integrity_summary(uuid, uuid)'] LOOP
    SELECT pr.* INTO p FROM pg_proc pr WHERE pr.oid = to_regprocedure(f);
    IF NOT FOUND THEN RAISE EXCEPTION 'migration 0181 lost %', f; END IF;
    IF NOT p.prosecdef THEN RAISE EXCEPTION '% is not SECURITY DEFINER', f; END IF;
    IF p.proconfig IS NULL OR NOT EXISTS (SELECT 1 FROM unnest(p.proconfig) c WHERE c LIKE 'search_path=%') THEN
      RAISE EXCEPTION '% has no pinned search_path', f;
    END IF;
    IF has_function_privilege('public', p.oid, 'EXECUTE') THEN RAISE EXCEPTION 'PUBLIC can execute %', f; END IF;
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_pmsd')
     AND NOT has_function_privilege('svc_pmsd', 'iam_v2.entitlement_usage_bytes(uuid, timestamptz)', 'EXECUTE') THEN
    RAISE EXCEPTION 'svc_pmsd lost EXECUTE on entitlement_usage_bytes';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd')
     AND NOT has_function_privilege('svc_acctd', 'iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)', 'EXECUTE') THEN
    RAISE EXCEPTION 'svc_acctd lost EXECUTE on ingest_absolute_counters';
  END IF;
END $verify$;

COMMIT;
