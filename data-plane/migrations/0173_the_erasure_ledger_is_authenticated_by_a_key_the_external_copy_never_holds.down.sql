-- 0173 down: back to the 0148 ledger (no MAC, hashes confirmed by edged). The key and the MAC column are dropped; a
-- seal entry already written stays in the ledger as an ordinary entry the old code ignores.
BEGIN;
ALTER TABLE iam_v2.erasure_ledger DROP CONSTRAINT IF EXISTS ledger_kind;
ALTER TABLE iam_v2.erasure_ledger ADD CONSTRAINT ledger_kind CHECK (kind IN ('genesis', 'erase', 'request_answered', 'assisted_closed', 'checkpoint', 'seal'));

CREATE OR REPLACE FUNCTION iam_v2.ledger_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF TG_TABLE_NAME = 'ledger_meta' THEN
    IF TG_OP = 'UPDATE' AND current_setting('iam_v2.ledger_adopt', true) = 'on' THEN
      RETURN NEW;
    END IF;
    RAISE EXCEPTION 'the erasure ledger is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
  END IF;
  -- NESTED, not AND-ed: OLD and NEW do not exist for a TRUNCATE, and SQL does not promise to stop at the first false operand.
  IF TG_OP = 'UPDATE' THEN
    IF OLD.confirmed = false AND NEW.confirmed = true
       AND (NEW.seq, NEW.ledger_id, NEW.tenant_id, NEW.site_id, NEW.kind, NEW.at_us, NEW.body, NEW.prev_hash, NEW.hash)
           IS NOT DISTINCT FROM (OLD.seq, OLD.ledger_id, OLD.tenant_id, OLD.site_id, OLD.kind, OLD.at_us, OLD.body, OLD.prev_hash, OLD.hash) THEN
      RETURN NEW;
    END IF;
  ELSIF TG_OP = 'DELETE' THEN
    IF current_setting('iam_v2.ledger_adopt', true) = 'on' THEN
      RETURN OLD;
    END IF;
  END IF;
  RAISE EXCEPTION 'the erasure ledger is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $$;

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

CREATE OR REPLACE FUNCTION iam_v2.ledger_head() RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT jsonb_build_object('ledger', m.ledger_id, 'seq', COALESCE(h.seq, 0), 'hash', COALESCE(h.hash, ''), 'at_us', COALESCE(h.at_us, 0),
                            'unconfirmed', (SELECT count(*) FROM erasure_ledger u WHERE NOT u.confirmed))
    FROM ledger_meta m LEFT JOIN LATERAL (SELECT l.seq, l.hash, l.at_us FROM erasure_ledger l WHERE l.confirmed ORDER BY l.seq DESC LIMIT 1) h ON true
$$;

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
        PERFORM public.ledger_replay_erase(e.tenant, e.site, e.body::jsonb);
      ELSIF e.kind = 'request_answered' THEN
        PERFORM public.ledger_replay_answer(e.tenant, e.site, e.body::jsonb);
      ELSIF e.kind = 'assisted_closed' THEN
        PERFORM public.ledger_replay_assisted_closed(e.tenant, e.site, e.body::jsonb);
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

DROP FUNCTION IF EXISTS iam_v2.ledger_confirm_checked(text[], text[]);
DROP FUNCTION IF EXISTS iam_v2.ledger_seal_legacy(uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.ledger_mac_key_install(text);
DROP FUNCTION IF EXISTS iam_v2.ledger_mac_key_get();
DROP FUNCTION IF EXISTS iam_v2.ledger_mac_ok(text, text);
DROP FUNCTION IF EXISTS iam_v2.ledger_receipt_of(text);
DROP FUNCTION IF EXISTS iam_v2.ledger_mac_of(text);
ALTER TABLE iam_v2.erasure_ledger DROP CONSTRAINT IF EXISTS ledger_mac_shape;
ALTER TABLE iam_v2.erasure_ledger DROP COLUMN IF EXISTS mac;
DROP TABLE IF EXISTS iam_v2.ledger_mac_key;
DO $g$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
  EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_confirm(text[]) TO svc_edged'; END IF; END $g$;
COMMIT;
