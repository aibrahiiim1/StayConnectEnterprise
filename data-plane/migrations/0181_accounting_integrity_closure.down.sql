-- 0181 down: restores the 0061 (ingest_absolute_counters), 0010 (rebind_session_entitlement) and 0180 (entitlement_usage_bytes,
-- p6_advance_data_crossing) definitions and removes what 0181 added.
-- COST OF ROLLING BACK: the sample sequence goes back to max(raw)+1 (the idle-past-retention quota hole returns); the conservative
-- charges 0181 wrote for retroactive bindings are taken back out of the accumulator; the integrity events are dropped (keep a
-- copy first: COPY iam_v2.accounting_integrity_events TO ...). Samples, totals and the 0180 accumulator are untouched.
BEGIN;

-- take back the conservative charges of retroactive bindings (identified by their events)
UPDATE iam_v2.accounting_usage_entitlement t
   SET bytes_down = greatest(t.bytes_down - x.b, 0), records = greatest(t.records - x.n, 0)
  FROM (SELECT e.entitlement_id, (e.detail->>'bucket')::timestamptz AS bucket,
               sum((e.detail->>'charged_bytes_down')::bigint) AS b, sum((e.detail->>'charged_records')::bigint) AS n
          FROM iam_v2.accounting_integrity_events e
         WHERE e.kind = 'RETROACTIVE_BINDING_PAST_RAW_RETENTION' AND e.bytes > 0
         GROUP BY e.entitlement_id, (e.detail->>'bucket')::timestamptz) x
 WHERE t.entitlement_id = x.entitlement_id AND t.bucket = x.bucket AND t.arm = 0;
DELETE FROM iam_v2.accounting_usage_entitlement t
 WHERE t.bytes_up = 0 AND t.bytes_down = 0 AND t.records = 0 AND NOT t.exact;

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

CREATE OR REPLACE FUNCTION iam_v2.rebind_session_entitlement(p_session uuid, p_ent uuid, p_at timestamptz) RETURNS uuid
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
DECLARE v_t uuid; v_s uuid; v_open uuid; v_from timestamptz; v_cur uuid; v_seq bigint; v_id uuid;
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
  RETURN v_id;
END $fn$;

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
       AND (a.bucket + interval '1 hour' <= p_at OR a.latest_sampled_at <= p_at)),
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
         greatest(f.latest, l.latest)
    FROM folded f, live l;
$$;


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
      INSERT INTO iam_v2.data_crossing_state AS s (entitlement_id, tenant_id, site_id, quota_bytes, crossed_at)
      VALUES (e.id, p_tenant, p_site, e.quota, v_exact)
      ON CONFLICT (entitlement_id) DO UPDATE
        SET quota_bytes = EXCLUDED.quota_bytes, crossed_at = EXCLUDED.crossed_at, verified_at = now();
    END IF;
  END LOOP;
  RETURN n;
END $$;

DROP FUNCTION IF EXISTS iam_v2.accounting_integrity_summary(uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.entitlement_usage_uncertainty(uuid, timestamptz);
DROP FUNCTION IF EXISTS iam_v2.p6_reconcile_crossing_checkpoint(uuid);
DROP TABLE IF EXISTS iam_v2.accounting_integrity_events;
ALTER TABLE iam_v2.accounting_checkpoints DROP COLUMN IF EXISTS last_sample_seq;

COMMIT;
