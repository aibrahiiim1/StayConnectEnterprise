-- ---------------------------------------------------------------------------------------------------------
-- 0148: the erasure ledger is kept in two places, and a restore reapplies it.
-- ---------------------------------------------------------------------------------------------------------
-- 0147 kept a journal of erasures that scd copied to a file. This replaces it with a LEDGER: an append-only, hash-chained
-- log of every erasure and of how every data-protection request was closed, written in the same transaction as the
-- erasure, and committed to two copies outside the database (a file on the appliance and an independent secondary
-- storage) BEFORE the erasure is reported as done (data-plane/internal/ledger). After a restore from an older backup the
-- ledger is replayed, so the guests the hotel has already told were erased are erased again and the requests it has
-- already answered are closed again, before the restored data is served.
--
-- The ledger holds no guest data and no staff-typed text: the kind and id of a record that was anonymised, the reference
-- given to the guest, when, the counts, and (for a request) its dates, status and the coded ground for a refusal.
BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.ledger_meta (
  singleton  boolean PRIMARY KEY DEFAULT true CONSTRAINT ledger_meta_one CHECK (singleton),
  ledger_id  uuid NOT NULL DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO iam_v2.ledger_meta DEFAULT VALUES ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS iam_v2.erasure_ledger (
  seq        bigint PRIMARY KEY CONSTRAINT ledger_seq CHECK (seq >= 1),
  ledger_id  uuid NOT NULL,
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  kind       text NOT NULL CONSTRAINT ledger_kind CHECK (kind IN ('genesis', 'erase', 'request_answered', 'assisted_closed', 'checkpoint')),
  at_us      bigint NOT NULL,
  body       text NOT NULL,
  prev_hash  text NOT NULL CONSTRAINT ledger_prev CHECK (prev_hash ~ '^[0-9a-f]{64}$'),
  hash       text NOT NULL CONSTRAINT ledger_hash_shape CHECK (hash ~ '^[0-9a-f]{64}$'),
  confirmed  boolean NOT NULL DEFAULT false,
  CONSTRAINT ledger_hash_unique UNIQUE (hash)
);
COMMENT ON TABLE iam_v2.erasure_ledger IS
  'The erasure ledger (0148): append-only and hash-chained. A row is confirmed once it is durable in both external copies; an '
  'unconfirmed row exists only inside the transaction that made it. Holds no guest data and no staff-typed text.';

CREATE OR REPLACE FUNCTION iam_v2.ledger_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND OLD.confirmed = false AND NEW.confirmed = true
     AND (NEW.seq, NEW.ledger_id, NEW.tenant_id, NEW.site_id, NEW.kind, NEW.at_us, NEW.body, NEW.prev_hash, NEW.hash)
         IS NOT DISTINCT FROM (OLD.seq, OLD.ledger_id, OLD.tenant_id, OLD.site_id, OLD.kind, OLD.at_us, OLD.body, OLD.prev_hash, OLD.hash) THEN
    RETURN NEW;
  END IF;
  IF TG_TABLE_NAME = 'ledger_meta' AND TG_OP = 'UPDATE' AND current_setting('iam_v2.ledger_adopt', true) = 'on' THEN
    RETURN NEW;
  END IF;
  IF TG_TABLE_NAME = 'erasure_ledger' AND TG_OP = 'DELETE' AND current_setting('iam_v2.ledger_adopt', true) = 'on' THEN
    RETURN OLD;
  END IF;
  RAISE EXCEPTION 'the erasure ledger is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $$;
DROP TRIGGER IF EXISTS erasure_ledger_guard ON iam_v2.erasure_ledger;
CREATE TRIGGER erasure_ledger_guard BEFORE UPDATE OR DELETE ON iam_v2.erasure_ledger FOR EACH ROW EXECUTE FUNCTION iam_v2.ledger_guard();
DROP TRIGGER IF EXISTS erasure_ledger_no_truncate ON iam_v2.erasure_ledger;
CREATE TRIGGER erasure_ledger_no_truncate BEFORE TRUNCATE ON iam_v2.erasure_ledger FOR EACH STATEMENT EXECUTE FUNCTION iam_v2.ledger_guard();
DROP TRIGGER IF EXISTS ledger_meta_guard ON iam_v2.ledger_meta;
CREATE TRIGGER ledger_meta_guard BEFORE UPDATE OR DELETE ON iam_v2.ledger_meta FOR EACH ROW EXECUTE FUNCTION iam_v2.ledger_guard();

-- The hash of an entry: everything but the hash itself. data-plane/internal/ledger computes the same value byte for byte.
CREATE OR REPLACE FUNCTION iam_v2.ledger_hash(p_ledger uuid, p_seq bigint, p_prev text, p_kind text, p_tenant uuid, p_site uuid,
                                              p_at_us bigint, p_body text) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'pg_temp'
    AS $$
  SELECT encode(sha256(convert_to(concat_ws(E'\x1f', 'stayconnect-ledger-v1', p_ledger::text, p_seq::text, p_prev, p_kind,
                                            p_tenant::text, p_site::text, p_at_us::text, p_body), 'UTF8')), 'hex')
$$;

-- The first entry. Written once; the file copies are made from it by scd.
INSERT INTO iam_v2.erasure_ledger (seq, ledger_id, tenant_id, site_id, kind, at_us, body, prev_hash, hash, confirmed)
SELECT 1, m.ledger_id, '00000000-0000-0000-0000-000000000000', '00000000-0000-0000-0000-000000000000', 'genesis',
       t.us, '{"note": "ledger created"}', repeat('0', 64),
       iam_v2.ledger_hash(m.ledger_id, 1, repeat('0', 64), 'genesis', '00000000-0000-0000-0000-000000000000',
                          '00000000-0000-0000-0000-000000000000', t.us, '{"note": "ledger created"}'),
       true
  FROM iam_v2.ledger_meta m, (SELECT (extract(epoch from clock_timestamp()) * 1000000)::bigint AS us) t
 WHERE NOT EXISTS (SELECT 1 FROM iam_v2.erasure_ledger);


-- ---------------------------------------------------------------------------------------------------------
-- Appending, confirming, reading.
-- ---------------------------------------------------------------------------------------------------------
-- One entry. The advisory lock is held to the end of the transaction, so two erasures cannot interleave their entries and
-- the sequence has no gap: the next request waits until this one is durable in both copies and committed (or rolled back).
CREATE OR REPLACE FUNCTION iam_v2.ledger_append(p_tenant uuid, p_site uuid, p_kind text, p_body jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE m record; v_seq bigint; v_prev text; v_at bigint; v_body text; v_hash text;
BEGIN
  IF p_kind NOT IN ('erase', 'request_answered', 'assisted_closed', 'checkpoint') THEN
    RAISE EXCEPTION 'unknown ledger entry kind %', p_kind USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('stayconnect-erasure-ledger'));
  SELECT * INTO m FROM ledger_meta;
  SELECT l.seq, l.hash INTO v_seq, v_prev FROM erasure_ledger l ORDER BY l.seq DESC LIMIT 1;
  v_seq := COALESCE(v_seq, 0) + 1;
  v_prev := COALESCE(v_prev, repeat('0', 64));
  v_at := (extract(epoch from clock_timestamp()) * 1000000)::bigint;
  v_body := p_body::text;
  v_hash := ledger_hash(m.ledger_id, v_seq, v_prev, p_kind, p_tenant, p_site, v_at, v_body);
  INSERT INTO erasure_ledger (seq, ledger_id, tenant_id, site_id, kind, at_us, body, prev_hash, hash, confirmed)
  VALUES (v_seq, m.ledger_id, p_tenant, p_site, p_kind, v_at, v_body, v_prev, v_hash, false);
  RETURN jsonb_build_object('ledger', m.ledger_id, 'seq', v_seq, 'prev', v_prev, 'kind', p_kind, 'tenant', p_tenant, 'site', p_site,
                            'at_us', v_at, 'body', v_body, 'hash', v_hash);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.ledger_row_json(l iam_v2.erasure_ledger) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT jsonb_build_object('ledger', l.ledger_id, 'seq', l.seq, 'prev', l.prev_hash, 'kind', l.kind, 'tenant', l.tenant_id,
                            'site', l.site_id, 'at_us', l.at_us, 'body', l.body, 'hash', l.hash)
$$;

-- What this transaction has appended and not yet confirmed (the caller ships it to both copies, then confirms it).
CREATE OR REPLACE FUNCTION iam_v2.ledger_pending() RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(jsonb_agg(ledger_row_json(l) ORDER BY l.seq), '[]'::jsonb) FROM erasure_ledger l WHERE NOT l.confirmed
$$;

CREATE OR REPLACE FUNCTION iam_v2.ledger_confirm(p_hashes text[]) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE n integer;
BEGIN
  UPDATE erasure_ledger SET confirmed = true WHERE NOT confirmed AND hash = ANY (p_hashes);
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END $$;

-- How far the ledger has reached, and whether anything is unconfirmed (committed but not durable in both copies: a fault).
CREATE OR REPLACE FUNCTION iam_v2.ledger_head() RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT jsonb_build_object('ledger', m.ledger_id, 'seq', COALESCE(h.seq, 0), 'hash', COALESCE(h.hash, ''),
                            'unconfirmed', (SELECT count(*) FROM erasure_ledger u WHERE NOT u.confirmed))
    FROM ledger_meta m LEFT JOIN LATERAL (SELECT l.seq, l.hash FROM erasure_ledger l WHERE l.confirmed ORDER BY l.seq DESC LIMIT 1) h ON true
$$;

CREATE OR REPLACE FUNCTION iam_v2.ledger_export(p_from bigint) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(jsonb_agg(ledger_row_json(l) ORDER BY l.seq), '[]'::jsonb) FROM erasure_ledger l WHERE l.confirmed AND l.seq > COALESCE(p_from, 0)
$$;

-- Is the database's own copy a valid chain? (The same check the files get.)
CREATE OR REPLACE FUNCTION iam_v2.ledger_verify() RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  WITH r AS (
    SELECT l.seq, l.hash, l.prev_hash, l.ledger_id,
           lag(l.hash) OVER (ORDER BY l.seq) AS before_hash, lag(l.seq) OVER (ORDER BY l.seq) AS before_seq,
           ledger_hash(l.ledger_id, l.seq, l.prev_hash, l.kind, l.tenant_id, l.site_id, l.at_us, l.body) AS calc
      FROM erasure_ledger l WHERE l.confirmed)
  SELECT jsonb_build_object('ok', count(*) FILTER (WHERE calc <> hash OR (before_seq IS NULL AND (seq <> 1 OR prev_hash <> repeat('0', 64)))
                                                      OR (before_seq IS NOT NULL AND (seq <> before_seq + 1 OR prev_hash <> before_hash))) = 0,
                            'entries', count(*),
                            'first_bad', min(seq) FILTER (WHERE calc <> hash OR (before_seq IS NULL AND (seq <> 1 OR prev_hash <> repeat('0', 64)))
                                                              OR (before_seq IS NOT NULL AND (seq <> before_seq + 1 OR prev_hash <> before_hash))))
    FROM r
$$;

-- The entry for an erasure that has just run: what was anonymised (kind and id), the reference, when, and the counts.
CREATE OR REPLACE FUNCTION iam_v2.ledger_append_erasure(p_tenant uuid, p_site uuid, p_ref text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE e record; v_records jsonb;
BEGIN
  SELECT * INTO e FROM guest_data_erasures x WHERE x.erasure_ref = p_ref AND x.tenant_id = p_tenant AND x.site_id = p_site;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'the erasure % is not recorded', p_ref USING ERRCODE = 'no_data_found';
  END IF;
  SELECT COALESCE(jsonb_agg(jsonb_build_object('kind', j.kind, 'id', j.subject_id, 'detail', j.detail) ORDER BY j.kind, j.subject_id, COALESCE(j.detail, '')), '[]'::jsonb)
    INTO v_records FROM erasure_journal j WHERE j.erasure_ref = p_ref AND j.tenant_id = p_tenant AND j.site_id = p_site;
  RETURN ledger_append(p_tenant, p_site, 'erase', jsonb_build_object(
    'erasure_ref', e.erasure_ref, 'subject_kind', e.subject_kind, 'erased_at', e.erased_at, 'counts', e.counts, 'records', v_records));
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- Every erasure and every closed request writes its ledger entry in the SAME transaction.
-- ---------------------------------------------------------------------------------------------------------
-- The caller (edged) ships what iam_v2.ledger_pending() returns to both copies BEFORE it commits: if either copy fails, the
-- transaction is rolled back and nothing was erased. The functions below only append; they do not decide.
CREATE OR REPLACE FUNCTION public.guest_data_erase(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text)
 RETURNS jsonb
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v jsonb;
BEGIN
  v := public.guest_data_erase_core(p_tenant, p_site, p_kind, p_id, p_operator, p_reason, false, NULL);
  PERFORM iam_v2.ledger_append_erasure(p_tenant, p_site, v ->> 'erasure_ref');
  RETURN v;
END $function$;

-- The assisted erasure writes its ledger entry
CREATE OR REPLACE FUNCTION public.assisted_request_erase(p_tenant uuid, p_site uuid, p_request uuid, p_actor text, p_reason text,
                                                         p_confirm text, p_signature text, p_selected jsonb)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE v jsonb; v_macs text[]; v_pats text[]; v_ref text; v_since timestamptz; n_audit integer := 0;
BEGIN
  PERFORM public.assisted_reason_check(p_tenant, p_site, iam_v2.assisted_request_anchors_json(p_tenant, p_site, p_request), p_reason);
  v := iam_v2.assisted_request_erase_run(p_tenant, p_site, p_request, p_actor, p_reason, p_confirm, p_signature, p_selected);
  v_ref := v ->> 'erasure_ref';
  PERFORM iam_v2.ledger_append_erasure(p_tenant, p_site, v_ref);
  v_macs := ARRAY(SELECT jsonb_array_elements_text(v -> 'macs_cleared'));
  n_audit := public.audit_clean_macs(p_tenant, v_macs, v_ref, COALESCE((v ->> 'since')::timestamptz, '-infinity'));
  RETURN (v - 'macs_cleared' - 'since') || jsonb_build_object('activity_log_entries_cleaned', n_audit);
END $$;

-- Closing an assisted request writes its ledger entry
CREATE OR REPLACE FUNCTION iam_v2.assisted_request_close(p_tenant uuid, p_site uuid, p_request uuid, p_actor text, p_outcome text, p_reason text)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE q record; v_exports integer; v_disclosed integer;
BEGIN
  IF p_actor IS NULL OR length(btrim(p_actor)) NOT BETWEEN 1 AND 200 THEN
    RAISE EXCEPTION 'an operator is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_outcome IS NULL OR p_outcome NOT IN ('COMPLETED', 'CANCELLED') THEN
    RAISE EXCEPTION 'the outcome must be COMPLETED or CANCELLED' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) NOT BETWEEN 4 AND 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO q FROM assisted_guest_requests x WHERE x.id = p_request AND x.tenant_id = p_tenant AND x.site_id = p_site FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'assisted request not found' USING ERRCODE = 'no_data_found';
  END IF;
  IF q.status <> 'OPEN' THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'closed';
  END IF;
  SELECT count(*) INTO v_exports FROM assisted_guest_request_events e WHERE e.request_id = p_request AND e.action = 'EXPORTED';
  SELECT count(*) INTO v_disclosed FROM assisted_guest_request_records r WHERE r.request_id = p_request AND r.action = 'DISCLOSED';
  -- An erasure completes only by running; an access request completes only after something was handed over.
  IF p_outcome = 'COMPLETED' AND (q.purpose <> 'ACCESS' OR v_exports = 0) THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'nothing_done';
  END IF;
  PERFORM assisted_close(p_tenant, p_site, p_request, p_actor, p_outcome, p_reason,
                         jsonb_build_object('exports', v_exports, 'records_disclosed', v_disclosed),
                         CASE p_outcome WHEN 'COMPLETED' THEN 'COMPLETED' ELSE 'CANCELLED' END);
  PERFORM ledger_append(p_tenant, p_site, 'assisted_closed', jsonb_build_object(
    'id', p_request, 'ref', q.ref, 'purpose', q.purpose, 'status', p_outcome, 'closed_at', now()));
  -- The hotel's file of this request, handed back once: closing it removes the addresses and the free text.
  RETURN jsonb_build_object('ref', q.ref, 'status', p_outcome,
    'receipt', jsonb_build_object('reference', q.ref, 'purpose', q.purpose, 'requester', q.requester_ref,
                                  'verification_method', q.verification_method, 'evidence_note', q.evidence_note,
                                  'range_from', q.range_from, 'range_to', q.range_to,
                                  'statements', jsonb_build_object('identity_verified', q.attest_identity, 'device_shown', q.attest_device,
                                                                   'sole_use', q.attest_sole_use, 'private_address_confirmed', q.attest_private_address),
                                  'prepared_by', q.created_by, 'opened_at', q.created_at,
                                  'exports', v_exports, 'records_disclosed', v_disclosed));
END $function$;

-- Answering a request writes its ledger entry
CREATE OR REPLACE FUNCTION iam_v2.request_register_answer(p_tenant uuid, p_site uuid, p_id uuid, p_actor text, p_status text,
        p_outcome text, p_note text, p_erasure_ref text, p_assisted_ref text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE q record;
BEGIN
  IF p_actor IS NULL OR length(btrim(p_actor)) NOT BETWEEN 1 AND 200 THEN
    RAISE EXCEPTION 'an operator is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_status IS NULL OR p_status NOT IN ('ANSWERED', 'PARTLY_REFUSED', 'REFUSED', 'WITHDRAWN') THEN
    RAISE EXCEPTION 'the outcome is answered, partly refused, refused or withdrawn' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  -- A refusal, or a part-refusal, names its ground: the person is told it, and told they may complain (Art. 12(4)).
  IF p_status IN ('PARTLY_REFUSED', 'REFUSED') AND COALESCE(p_outcome, 'NONE') = 'NONE' THEN
    RAISE EXCEPTION 'a refusal names its ground' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_note IS NOT NULL AND length(p_note) > 500 THEN
    RAISE EXCEPTION 'the note is at most 500 characters' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO q FROM guest_data_request_register x WHERE x.id = p_id AND x.tenant_id = p_tenant AND x.site_id = p_site FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'request not found' USING ERRCODE = 'no_data_found'; END IF;
  IF q.status <> 'OPEN' THEN
    RAISE EXCEPTION 'register_refused' USING ERRCODE = 'object_in_use', DETAIL = 'closed';
  END IF;
  UPDATE guest_data_request_register
     SET status = p_status, outcome_code = COALESCE(NULLIF(p_outcome, ''), 'NONE'), answered_by = btrim(p_actor), answered_at = now(),
         erasure_ref = NULLIF(p_erasure_ref, ''), assisted_ref = NULLIF(p_assisted_ref, ''),
         requester_ref = '[removed]', outcome_note = NULL, extension_reason = NULL
   WHERE id = p_id;
  PERFORM ledger_append(p_tenant, p_site, 'request_answered', jsonb_build_object(
    'id', p_id, 'ref', q.ref, 'kind', q.kind, 'received_at', q.received_at, 'due_at', q.due_at, 'extended_until', q.extended_until,
    'status', p_status, 'outcome', COALESCE(NULLIF(p_outcome, ''), 'NONE'), 'erasure_ref', NULLIF(p_erasure_ref, ''),
    'assisted_ref', NULLIF(p_assisted_ref, ''), 'verification_method', q.verification_method, 'answered_at', now()));
  RETURN jsonb_build_object('reference', q.ref, 'kind', q.kind, 'requester', q.requester_ref, 'received_at', q.received_at,
                            'due_at', COALESCE(q.extended_until, q.due_at), 'answered_at', now(), 'status', p_status,
                            'outcome', COALESCE(NULLIF(p_outcome, ''), 'NONE'), 'note', p_note,
                            'verification_method', q.verification_method, 'erasure_ref', NULLIF(p_erasure_ref, ''),
                            'assisted_ref', NULLIF(p_assisted_ref, ''), 'answered_in_time', iam_v2.site_today(p_site) <= COALESCE(q.extended_until, q.due_at));
END $$;


-- ---------------------------------------------------------------------------------------------------------
-- Replay: what a restored database is told, in order, from the ledger.
-- ---------------------------------------------------------------------------------------------------------
-- An erasure: every record it anonymised is anonymised again from what the restored data says now, and the proof the guest
-- was given (the reference, when, the counts) is put back. A record that no longer exists (created after the backup) is
-- skipped: there is nothing to erase.
CREATE OR REPLACE FUNCTION iam_v2.ledger_replay_erase(p_tenant uuid, p_site uuid, p_body jsonb) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'public', 'pg_temp'
    AS $$
DECLARE r record; v_ref text := p_body ->> 'erasure_ref'; v_res jsonb; v_kinds text[] := '{}'; v_ids uuid[] := '{}';
BEGIN
  FOR r IN SELECT x.kind, x.id, x.detail FROM jsonb_to_recordset(p_body -> 'records') AS x(kind text, id uuid, detail text) LOOP
    IF r.kind = 'assisted_record' THEN
      v_kinds := v_kinds || r.detail;
      v_ids := v_ids || r.id;
    ELSE
      BEGIN
        PERFORM public.guest_data_erase_core(p_tenant, p_site, r.kind, r.id, 'restore', 'erasure replayed from the ledger', true, v_ref);
      EXCEPTION WHEN no_data_found THEN
        NULL;   -- the record did not exist in the restored data
      END;
    END IF;
  END LOOP;
  IF cardinality(v_ids) > 0 THEN
    v_res := assisted_apply_records(p_tenant, p_site, v_kinds, v_ids);
    PERFORM public.audit_clean_macs(p_tenant, ARRAY(SELECT jsonb_array_elements_text(v_res -> 'macs_cleared')), v_ref, '-infinity');
  END IF;
  INSERT INTO guest_data_erasures (erasure_ref, tenant_id, site_id, subject_kind, erased_by, erase_reason, counts, erased_at)
  VALUES (v_ref, p_tenant, p_site, p_body ->> 'subject_kind', 'restored from the erasure ledger',
          'erasure replayed from the ledger; the reason is not kept there', COALESCE(p_body -> 'counts', '{}'::jsonb),
          COALESCE((p_body ->> 'erased_at')::timestamptz, now()))
  ON CONFLICT (erasure_ref) DO NOTHING;
  INSERT INTO erasure_journal (tenant_id, site_id, erasure_ref, kind, subject_id, detail, erased_at)
    SELECT p_tenant, p_site, v_ref, x.kind, x.id, x.detail, COALESCE((p_body ->> 'erased_at')::timestamptz, now())
      FROM jsonb_to_recordset(p_body -> 'records') AS x(kind text, id uuid, detail text)
  ON CONFLICT DO NOTHING;
END $$;

-- A closed request: put back as closed, with the requester and the note gone. One created after the backup is recreated
-- as closed.
CREATE OR REPLACE FUNCTION iam_v2.ledger_replay_answer(p_tenant uuid, p_site uuid, p_body jsonb) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'public', 'pg_temp'
    AS $$
DECLARE v_id uuid := (p_body ->> 'id')::uuid; v_at timestamptz := COALESCE((p_body ->> 'answered_at')::timestamptz, now());
BEGIN
  UPDATE guest_data_request_register q
     SET status = p_body ->> 'status', outcome_code = p_body ->> 'outcome', answered_by = 'restored from the erasure ledger',
         answered_at = v_at, erasure_ref = NULLIF(p_body ->> 'erasure_ref', ''), assisted_ref = NULLIF(p_body ->> 'assisted_ref', ''),
         requester_ref = '[removed]', outcome_note = NULL, extension_reason = NULL,
         extended_until = NULLIF(p_body ->> 'extended_until', '')::date
   WHERE q.id = v_id AND q.tenant_id = p_tenant AND q.site_id = p_site AND q.status = 'OPEN';
  IF NOT FOUND AND NOT EXISTS (SELECT 1 FROM guest_data_request_register q WHERE q.id = v_id) THEN
    INSERT INTO guest_data_request_register (id, ref, tenant_id, site_id, kind, received_at, due_at, extended_until, requester_ref,
                                             verification_method, status, outcome_code, erasure_ref, assisted_ref, created_by,
                                             created_at, answered_by, answered_at)
    VALUES (v_id, p_body ->> 'ref', p_tenant, p_site, p_body ->> 'kind', (p_body ->> 'received_at')::date, (p_body ->> 'due_at')::date,
            NULLIF(p_body ->> 'extended_until', '')::date, '[removed]', NULLIF(p_body ->> 'verification_method', ''),
            p_body ->> 'status', p_body ->> 'outcome', NULLIF(p_body ->> 'erasure_ref', ''), NULLIF(p_body ->> 'assisted_ref', ''),
            'restored from the erasure ledger', (p_body ->> 'received_at')::timestamptz, 'restored from the erasure ledger', v_at)
    ON CONFLICT DO NOTHING;
  END IF;
END $$;

-- A closed assisted request that the backup still has open: closed again, which removes its addresses and free text.
CREATE OR REPLACE FUNCTION iam_v2.ledger_replay_assisted_closed(p_tenant uuid, p_site uuid, p_body jsonb) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'public', 'pg_temp'
    AS $$
BEGIN
  IF EXISTS (SELECT 1 FROM assisted_guest_requests q WHERE q.id = (p_body ->> 'id')::uuid AND q.tenant_id = p_tenant
                AND q.site_id = p_site AND q.status = 'OPEN') THEN
    PERFORM assisted_close(p_tenant, p_site, (p_body ->> 'id')::uuid, 'restored from the erasure ledger', p_body ->> 'status',
                           'closed before the restore', jsonb_build_object('restored', true), COALESCE(p_body ->> 'status', 'CANCELLED'));
  END IF;
END $$;

-- Applies the entries the database does not have, in order, each one verified against the chain first. Run by scd on the
-- staging database BEFORE a restore goes live, and hourly. An entry that cannot be applied stops the replay there (the chain
-- stays contiguous) and is reported and retried; nothing is skipped silently.
--
-- p_adopt: a database that has only its own first entry (a restore of a backup older than the ledger, then the migration)
-- takes over the ledger it is given instead of keeping the fresh one.
CREATE OR REPLACE FUNCTION public.ledger_apply(p_entries jsonb, p_adopt boolean DEFAULT false) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $function$
DECLARE e record; m record; v_head_seq bigint; v_head_hash text; v_applied integer := 0; v_known integer := 0;
        v_failed jsonb := NULL; v_conflict jsonb := NULL; v_hash text; v_first record; v_stored text;
BEGIN
  IF jsonb_typeof(p_entries) IS DISTINCT FROM 'array' THEN
    RAISE EXCEPTION 'a ledger is a list of entries' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('stayconnect-erasure-ledger'));
  SELECT * INTO m FROM iam_v2.ledger_meta;
  SELECT x.* INTO v_first FROM jsonb_to_recordset(p_entries) AS x(ledger uuid, seq bigint) ORDER BY x.seq LIMIT 1;
  IF p_adopt AND v_first.seq = 1 AND v_first.ledger IS DISTINCT FROM m.ledger_id
     AND (SELECT count(*) FROM iam_v2.erasure_ledger) = 1 THEN
    PERFORM set_config('iam_v2.ledger_adopt', 'on', true);
    DELETE FROM iam_v2.erasure_ledger;
    UPDATE iam_v2.ledger_meta SET ledger_id = v_first.ledger;
    PERFORM set_config('iam_v2.ledger_adopt', '', true);
    SELECT * INTO m FROM iam_v2.ledger_meta;
  END IF;
  SELECT l.seq, l.hash INTO v_head_seq, v_head_hash FROM iam_v2.erasure_ledger l ORDER BY l.seq DESC LIMIT 1;
  v_head_seq := COALESCE(v_head_seq, 0);
  v_head_hash := COALESCE(v_head_hash, repeat('0', 64));
  FOR e IN SELECT x.* FROM jsonb_to_recordset(p_entries)
             AS x(ledger uuid, seq bigint, prev text, kind text, tenant uuid, site uuid, at_us bigint, body text, hash text)
            ORDER BY x.seq LOOP
    IF e.ledger IS DISTINCT FROM m.ledger_id THEN
      v_conflict := jsonb_build_object('seq', e.seq, 'why', 'another ledger');
      EXIT;
    END IF;
    IF e.seq <= v_head_seq THEN
      SELECT l.hash INTO v_stored FROM iam_v2.erasure_ledger l WHERE l.seq = e.seq;
      IF v_stored IS DISTINCT FROM e.hash THEN
        v_conflict := jsonb_build_object('seq', e.seq, 'why', 'the entry differs from the one in the database');
        EXIT;
      END IF;
      v_known := v_known + 1;
      CONTINUE;
    END IF;
    v_hash := iam_v2.ledger_hash(e.ledger, e.seq, e.prev, e.kind, e.tenant, e.site, e.at_us, e.body);
    IF e.seq <> v_head_seq + 1 OR e.prev IS DISTINCT FROM v_head_hash OR v_hash IS DISTINCT FROM e.hash THEN
      v_conflict := jsonb_build_object('seq', e.seq, 'why', 'the entry does not continue the ledger');
      EXIT;
    END IF;
    BEGIN
      IF e.kind = 'erase' THEN
        PERFORM iam_v2.ledger_replay_erase(e.tenant, e.site, e.body::jsonb);
      ELSIF e.kind = 'request_answered' THEN
        PERFORM iam_v2.ledger_replay_answer(e.tenant, e.site, e.body::jsonb);
      ELSIF e.kind = 'assisted_closed' THEN
        PERFORM iam_v2.ledger_replay_assisted_closed(e.tenant, e.site, e.body::jsonb);
      END IF;
      INSERT INTO iam_v2.erasure_ledger (seq, ledger_id, tenant_id, site_id, kind, at_us, body, prev_hash, hash, confirmed)
      VALUES (e.seq, e.ledger, e.tenant, e.site, e.kind, e.at_us, e.body, e.prev, e.hash, true);
    EXCEPTION WHEN OTHERS THEN
      v_failed := jsonb_build_object('seq', e.seq, 'kind', e.kind, 'error', SQLERRM);
    END;
    IF v_failed IS NOT NULL THEN
      EXIT;
    END IF;
    v_head_seq := e.seq;
    v_head_hash := e.hash;
    v_applied := v_applied + 1;
  END LOOP;
  RETURN jsonb_build_object('applied', v_applied, 'already_known', v_known, 'failed', v_failed, 'conflict', v_conflict,
                            'head', jsonb_build_object('ledger', m.ledger_id, 'seq', v_head_seq, 'hash', v_head_hash));
END $function$;
COMMENT ON FUNCTION public.ledger_apply(jsonb, boolean) IS
  'Applies the erasure ledger entries the database does not have (0148). Run by scd on a restored database before it goes live, and hourly; granted to nobody else.';

-- ---------------------------------------------------------------------------------------------------------
-- The secondary storage: one row, the secret sealed by scd, readable only through these functions.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.ledger_destination (
  singleton       boolean PRIMARY KEY DEFAULT true CONSTRAINT ledger_dest_one CHECK (singleton),
  kind            text NOT NULL CONSTRAINT ledger_dest_kind CHECK (kind IN ('dir', 'sftp', 's3')),
  config          jsonb NOT NULL,
  secret_ct       bytea,
  secret_nonce    bytea,
  secret_key_id   text,
  secret_ver      integer,
  configured_by   text NOT NULL,
  configured_at   timestamptz NOT NULL DEFAULT now(),
  verified_at     timestamptz,
  last_ok_at      timestamptz,
  last_error      text,
  last_error_at   timestamptz
);
COMMENT ON TABLE public.ledger_destination IS
  'Where the second copy of the erasure ledger is kept (0148). Mandatory: erasure is refused until it is set up and verified. The secret is sealed by scd.';

CREATE OR REPLACE FUNCTION public.ledger_destination_get() RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
  SELECT COALESCE((SELECT to_jsonb(d) - 'singleton' - 'secret_ct' - 'secret_nonce' FROM public.ledger_destination d), 'null'::jsonb)
$$;

-- scd reads the sealed secret to open it (never returned to a browser).
CREATE OR REPLACE FUNCTION public.ledger_destination_secret() RETURNS TABLE (secret_ct bytea, secret_nonce bytea, secret_key_id text, secret_ver integer)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
  SELECT d.secret_ct, d.secret_nonce, d.secret_key_id, d.secret_ver FROM public.ledger_destination d
$$;

CREATE OR REPLACE FUNCTION public.ledger_destination_set(p_kind text, p_config jsonb, p_ct bytea, p_nonce bytea, p_key_id text, p_ver integer, p_by text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
  IF p_kind NOT IN ('dir', 'sftp', 's3') THEN
    RAISE EXCEPTION 'unknown storage kind' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  INSERT INTO public.ledger_destination (singleton, kind, config, secret_ct, secret_nonce, secret_key_id, secret_ver, configured_by, configured_at, verified_at, last_ok_at, last_error, last_error_at)
  VALUES (true, p_kind, p_config, p_ct, p_nonce, p_key_id, p_ver, p_by, now(), now(), NULL, NULL, NULL)
  ON CONFLICT (singleton) DO UPDATE
     SET kind = EXCLUDED.kind, config = EXCLUDED.config, secret_ct = EXCLUDED.secret_ct, secret_nonce = EXCLUDED.secret_nonce,
         secret_key_id = EXCLUDED.secret_key_id, secret_ver = EXCLUDED.secret_ver, configured_by = EXCLUDED.configured_by,
         configured_at = now(), verified_at = now(), last_ok_at = NULL, last_error = NULL, last_error_at = NULL;
END $$;

CREATE OR REPLACE FUNCTION public.ledger_destination_note(p_ok boolean, p_error text) RETURNS void
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
  UPDATE public.ledger_destination
     SET last_ok_at = CASE WHEN p_ok THEN now() ELSE last_ok_at END,
         last_error = CASE WHEN p_ok THEN NULL ELSE left(p_error, 300) END,
         last_error_at = CASE WHEN p_ok THEN NULL ELSE now() END
$$;


-- ---------------------------------------------------------------------------------------------------------
-- Ownership and grants. The ledger tables are readable by nobody but the owner; the runtime roles reach them only through
-- the functions above.
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON iam_v2.ledger_meta, iam_v2.erasure_ledger, public.ledger_destination FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.ledger_guard(), iam_v2.ledger_hash(uuid,bigint,text,text,uuid,uuid,bigint,text),
  iam_v2.ledger_append(uuid,uuid,text,jsonb), iam_v2.ledger_row_json(iam_v2.erasure_ledger), iam_v2.ledger_pending(),
  iam_v2.ledger_confirm(text[]), iam_v2.ledger_head(), iam_v2.ledger_export(bigint), iam_v2.ledger_verify(),
  iam_v2.ledger_append_erasure(uuid,uuid,text), iam_v2.ledger_replay_erase(uuid,uuid,jsonb),
  iam_v2.ledger_replay_answer(uuid,uuid,jsonb), iam_v2.ledger_replay_assisted_closed(uuid,uuid,jsonb),
  public.ledger_apply(jsonb,boolean), public.ledger_destination_get(), public.ledger_destination_secret(),
  public.ledger_destination_set(text,jsonb,bytea,bytea,text,integer,text), public.ledger_destination_note(boolean,text) FROM PUBLIC;

-- The three replay helpers are NOT given to iam_v2_owner: they call the public erasure functions, which only the migration
-- owner (a superuser, like the replay of 0147) may execute. They are reachable only through public.ledger_apply.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.ledger_meta OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.erasure_ledger OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_guard() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_hash(uuid,bigint,text,text,uuid,uuid,bigint,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_append(uuid,uuid,text,jsonb) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_row_json(iam_v2.erasure_ledger) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_pending() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_confirm(text[]) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_head() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_export(bigint) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_verify() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_append_erasure(uuid,uuid,text) OWNER TO iam_v2_owner';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_pending() TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_confirm(text[]) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_head() TO svc_edged';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_append(uuid,uuid,text,jsonb) TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_pending() TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_confirm(text[]) TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_head() TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_export(bigint) TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_verify() TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.ledger_apply(jsonb,boolean) TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.ledger_destination_get() TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.ledger_destination_secret() TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.ledger_destination_set(text,jsonb,bytea,bytea,text,integer,text) TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.ledger_destination_note(boolean,text) TO svc_scd';
  END IF;
END $g$;

COMMIT;
