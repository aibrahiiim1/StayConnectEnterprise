-- ---------------------------------------------------------------------------------------------------------
-- 0173: the erasure ledger is authenticated by a key the external copy never holds (audit AD-3, P4 M1 / P3 M4).
-- ---------------------------------------------------------------------------------------------------------
-- Before: the ledger's hash chain proved an entry was not CHANGED, but anyone able to write the external storage could
-- append a well-formed entry, and the next restore would erase the guest records it named. After: every entry carries a
-- MAC made with a per-appliance key that lives only in this database (and in the key holders' sealed escrow, written by
-- scd), so a copy written by anyone else is not an entry at all.
--
--   * iam_v2.ledger_mac_key        the key, 32 random bytes made here, one row, readable by nobody (definer functions only).
--   * erasure_ledger.mac           HMAC-SHA256 over the entry's hash, set by ledger_append in the transaction that makes the entry.
--   * 'seal' entries               the plan for the entries that exist WITHOUT a MAC (every ledger written before this
--                                  migration). scd appends one MACed seal entry that names the last legacy entry and its hash,
--                                  but only when the database, the appliance file and the secondary storage all agree on it. A
--                                  legacy entry is accepted by ledger_apply only when it is covered by a seal whose MAC checks and
--                                  whose named hash matches (the chain makes everything before it part of that proof). Any
--                                  disagreement leaves the entries unsealed, and unsealed entries are NEVER applied: the
--                                  restore is refused and nothing is erased on ambiguity.
--   * ledger_confirm               revoked from svc_edged. edged now presents a durability RECEIPT, a MAC only scd can make
--                                  (it holds the key and writes the two copies), to ledger_confirm_checked.
--   * ledger_apply                 accepts only checked entries: a valid MAC, or coverage by a valid seal.
--
-- Additive and idempotent. No ledger row is rewritten and no guest record is touched.
BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.ledger_mac_key (
  singleton  boolean PRIMARY KEY DEFAULT true CONSTRAINT ledger_mac_key_one CHECK (singleton),
  key        bytea NOT NULL CONSTRAINT ledger_mac_key_len CHECK (length(key) = 32),
  created_at timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE iam_v2.ledger_mac_key IS
  'The per-appliance key that authenticates the erasure ledger (0173). One row; no role can read it except through the ledger definer functions.';
INSERT INTO iam_v2.ledger_mac_key (key) SELECT public.gen_random_bytes(32)
 WHERE NOT EXISTS (SELECT 1 FROM iam_v2.ledger_mac_key);

-- A baseline-built appliance (every new install) carries the schema but not the rows the migrations inserted, so the key is also
-- made on first use, under the ledger's own lock. Nothing is lost by that: a database with no key has no MACed entry (an entry is
-- MACed in the transaction that makes it), and ledger_mac_key_install can still put an escrowed key back while none exists.
CREATE OR REPLACE FUNCTION iam_v2.ledger_mac_key_ensure() RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM iam_v2.ledger_mac_key) THEN
    PERFORM pg_advisory_xact_lock(hashtext('stayconnect-erasure-ledger'));
    INSERT INTO iam_v2.ledger_mac_key (key) SELECT public.gen_random_bytes(32)
     WHERE NOT EXISTS (SELECT 1 FROM iam_v2.ledger_mac_key);
  END IF;
END $$;

ALTER TABLE iam_v2.erasure_ledger ADD COLUMN IF NOT EXISTS mac text;
DO $c$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ledger_mac_shape' AND conrelid = 'iam_v2.erasure_ledger'::regclass) THEN
    ALTER TABLE iam_v2.erasure_ledger ADD CONSTRAINT ledger_mac_shape CHECK (mac IS NULL OR mac ~ '^[0-9a-f]{64}$');
  END IF;
END $c$;
ALTER TABLE iam_v2.erasure_ledger DROP CONSTRAINT IF EXISTS ledger_kind;
ALTER TABLE iam_v2.erasure_ledger ADD CONSTRAINT ledger_kind
  CHECK (kind IN ('genesis', 'erase', 'request_answered', 'assisted_closed', 'checkpoint', 'seal'));

-- The guard now also forbids changing the MAC.
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
  IF TG_OP = 'UPDATE' THEN
    IF OLD.confirmed = false AND NEW.confirmed = true
       AND (NEW.seq, NEW.ledger_id, NEW.tenant_id, NEW.site_id, NEW.kind, NEW.at_us, NEW.body, NEW.prev_hash, NEW.hash, NEW.mac)
           IS NOT DISTINCT FROM (OLD.seq, OLD.ledger_id, OLD.tenant_id, OLD.site_id, OLD.kind, OLD.at_us, OLD.body, OLD.prev_hash, OLD.hash, OLD.mac) THEN
      RETURN NEW;
    END IF;
  ELSIF TG_OP = 'DELETE' THEN
    IF current_setting('iam_v2.ledger_adopt', true) = 'on' THEN
      RETURN OLD;
    END IF;
  END IF;
  RAISE EXCEPTION 'the erasure ledger is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- The MAC, the receipt, and who may ask.
-- ---------------------------------------------------------------------------------------------------------
-- The hash an entry already has covers everything but the hash, so the MAC covers the hash. Domains keep an entry MAC from
-- ever being usable as a receipt. data-plane/internal/ledger/mac.go computes the same values byte for byte.
CREATE OR REPLACE FUNCTION iam_v2.ledger_mac_of(p_hash text) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT encode(public.hmac(convert_to('stayconnect-ledger-mac-v1' || E'\x1f' || p_hash, 'UTF8'), k.key, 'sha256'), 'hex')
    FROM iam_v2.ledger_mac_key k
$$;

CREATE OR REPLACE FUNCTION iam_v2.ledger_receipt_of(p_hash text) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT encode(public.hmac(convert_to('stayconnect-ledger-durable-v1' || E'\x1f' || p_hash, 'UTF8'), k.key, 'sha256'), 'hex')
    FROM iam_v2.ledger_mac_key k
$$;

CREATE OR REPLACE FUNCTION iam_v2.ledger_mac_ok(p_hash text, p_mac text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT p_mac IS NOT NULL AND p_hash IS NOT NULL AND p_mac ~ '^[0-9a-f]{64}$' AND COALESCE(iam_v2.ledger_mac_of(p_hash) = p_mac, false)
$$;

-- scd reads the key to MAC the head note of the secondary storage and to issue receipts; edged cannot.
CREATE OR REPLACE FUNCTION iam_v2.ledger_mac_key_get() RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v text;
BEGIN
  PERFORM iam_v2.ledger_mac_key_ensure();
  SELECT encode(k.key, 'hex') INTO v FROM iam_v2.ledger_mac_key k;
  RETURN v;
END $$;

-- A restored database that predates this migration has a NEW key, which cannot check entries MACed with the old one. scd puts
-- the old key back (from the live database it is replacing, or from the key holders' escrow), but only while no entry here
-- depends on the current key: a database holding any MACed entry keeps its key.
CREATE OR REPLACE FUNCTION iam_v2.ledger_mac_key_install(p_hex text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_cur bytea; v_new bytea;
BEGIN
  IF p_hex IS NULL OR p_hex !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'a ledger key is 32 bytes in hex' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('stayconnect-erasure-ledger'));
  v_new := decode(p_hex, 'hex');
  SELECT key INTO v_cur FROM iam_v2.ledger_mac_key;
  IF v_cur IS NOT DISTINCT FROM v_new THEN
    RETURN true;
  END IF;
  IF EXISTS (SELECT 1 FROM iam_v2.erasure_ledger WHERE mac IS NOT NULL) THEN
    RETURN false;
  END IF;
  DELETE FROM iam_v2.ledger_mac_key;
  INSERT INTO iam_v2.ledger_mac_key (key) VALUES (v_new);
  RETURN true;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- Entries are MACed where they are made.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.ledger_append(p_tenant uuid, p_site uuid, p_kind text, p_body jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE m record; v_seq bigint; v_prev text; v_at bigint; v_body text; v_hash text; v_mac text;
BEGIN
  IF p_kind NOT IN ('erase', 'request_answered', 'assisted_closed', 'checkpoint', 'seal') THEN
    RAISE EXCEPTION 'unknown ledger entry kind %', p_kind USING ERRCODE = 'invalid_parameter_value';
  END IF;
  PERFORM pg_advisory_xact_lock(hashtext('stayconnect-erasure-ledger'));
  PERFORM iam_v2.ledger_mac_key_ensure();
  SELECT * INTO m FROM ledger_meta;
  SELECT l.seq, l.hash INTO v_seq, v_prev FROM erasure_ledger l ORDER BY l.seq DESC LIMIT 1;
  v_seq := COALESCE(v_seq, 0) + 1;
  v_prev := COALESCE(v_prev, repeat('0', 64));
  v_at := (extract(epoch from clock_timestamp()) * 1000000)::bigint;
  v_body := p_body::text;
  v_hash := ledger_hash(m.ledger_id, v_seq, v_prev, p_kind, p_tenant, p_site, v_at, v_body);
  v_mac := ledger_mac_of(v_hash);
  IF v_mac IS NULL THEN
    RAISE EXCEPTION 'the erasure ledger has no key, so nothing can be recorded' USING ERRCODE = 'object_not_in_prerequisite_state';
  END IF;
  INSERT INTO erasure_ledger (seq, ledger_id, tenant_id, site_id, kind, at_us, body, prev_hash, hash, confirmed, mac)
  VALUES (v_seq, m.ledger_id, p_tenant, p_site, p_kind, v_at, v_body, v_prev, v_hash, false, v_mac);
  RETURN jsonb_build_object('ledger', m.ledger_id, 'seq', v_seq, 'prev', v_prev, 'kind', p_kind, 'tenant', p_tenant, 'site', p_site,
                            'at_us', v_at, 'body', v_body, 'hash', v_hash, 'mac', v_mac);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.ledger_row_json(l iam_v2.erasure_ledger) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT jsonb_strip_nulls(jsonb_build_object('ledger', l.ledger_id, 'seq', l.seq, 'prev', l.prev_hash, 'kind', l.kind, 'tenant', l.tenant_id,
                            'site', l.site_id, 'at_us', l.at_us, 'body', l.body, 'hash', l.hash, 'mac', l.mac))
$$;

-- ---------------------------------------------------------------------------------------------------------
-- The plan for entries that exist without a MAC.
-- ---------------------------------------------------------------------------------------------------------
-- Appends ONE seal entry that names the last entry written without a MAC and its hash. scd calls it only after it has checked
-- that the database, the appliance file and the secondary storage all hold exactly that chain; if they do not agree it does not
-- call it, and the unsealed entries stay unusable. Returns NULL when there is nothing to seal or it is already sealed.
CREATE OR REPLACE FUNCTION iam_v2.ledger_seal_legacy(p_tenant uuid, p_site uuid) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_through bigint; v_head text;
BEGIN
  PERFORM pg_advisory_xact_lock(hashtext('stayconnect-erasure-ledger'));
  IF EXISTS (SELECT 1 FROM erasure_ledger WHERE NOT confirmed) THEN
    RAISE EXCEPTION 'an entry is still waiting to be made durable, so the ledger is not sealed now' USING ERRCODE = 'object_in_use';
  END IF;
  SELECT max(l.seq) INTO v_through FROM erasure_ledger l WHERE l.mac IS NULL;
  IF v_through IS NULL THEN
    RETURN NULL;
  END IF;
  IF EXISTS (SELECT 1 FROM erasure_ledger l WHERE l.kind = 'seal' AND ledger_mac_ok(l.hash, l.mac)
                AND (l.body::jsonb ->> 'legacy_through')::bigint >= v_through) THEN
    RETURN NULL;
  END IF;
  SELECT l.hash INTO v_head FROM erasure_ledger l WHERE l.seq = v_through;
  RETURN ledger_append(p_tenant, p_site, 'seal', jsonb_build_object('legacy_through', v_through, 'legacy_head', v_head));
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- Confirming: edged presents a receipt, never a bare hash.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.ledger_confirm_checked(p_hashes text[], p_receipts text[]) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE n integer := 0; k integer; i integer;
BEGIN
  IF p_hashes IS NULL OR p_receipts IS NULL OR cardinality(p_hashes) <> cardinality(p_receipts) OR cardinality(p_hashes) = 0 THEN
    RAISE EXCEPTION 'ledger_receipt_invalid' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  FOR i IN 1 .. cardinality(p_hashes) LOOP
    IF p_receipts[i] IS DISTINCT FROM ledger_receipt_of(p_hashes[i]) THEN
      RAISE EXCEPTION 'ledger_receipt_invalid' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    UPDATE erasure_ledger SET confirmed = true WHERE NOT confirmed AND hash = p_hashes[i];
    GET DIAGNOSTICS k = ROW_COUNT;
    n := n + k;
  END LOOP;
  IF n <> cardinality(p_hashes) THEN
    RAISE EXCEPTION 'ledger_receipt_invalid' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  RETURN n;
END $$;

-- ledger_head also says how much of the ledger is still unauthenticated.
CREATE OR REPLACE FUNCTION iam_v2.ledger_head() RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT jsonb_build_object('ledger', m.ledger_id, 'seq', COALESCE(h.seq, 0), 'hash', COALESCE(h.hash, ''), 'at_us', COALESCE(h.at_us, 0),
                            'unconfirmed', (SELECT count(*) FROM erasure_ledger u WHERE NOT u.confirmed),
                            'unsealed', (SELECT count(*) FROM erasure_ledger u
                                          WHERE u.mac IS NULL
                                            AND u.seq > COALESCE((SELECT max((s.body::jsonb ->> 'legacy_through')::bigint) FROM erasure_ledger s
                                                                   WHERE s.kind = 'seal' AND ledger_mac_ok(s.hash, s.mac)), 0)))
    FROM ledger_meta m LEFT JOIN LATERAL (SELECT l.seq, l.hash, l.at_us FROM erasure_ledger l WHERE l.confirmed ORDER BY l.seq DESC LIMIT 1) h ON true
$$;

-- ---------------------------------------------------------------------------------------------------------
-- Replay accepts only checked entries.
-- ---------------------------------------------------------------------------------------------------------
-- As in 0148, plus: an entry is applied only when its MAC checks, or when it is a legacy entry (no MAC) covered by a seal whose MAC
-- checks and whose named hash is the hash of the entry at the sealed position. Anything else stops the replay at that entry and is
-- reported as a conflict; nothing is applied past it and nothing is skipped silently.
CREATE OR REPLACE FUNCTION public.ledger_apply(p_entries jsonb, p_adopt boolean DEFAULT false) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $function$
DECLARE e record; m record; v_head_seq bigint; v_head_hash text; v_applied integer := 0; v_known integer := 0;
        v_failed jsonb := NULL; v_conflict jsonb := NULL; v_hash text; v_first record; v_stored text;
        v_leg_through bigint := NULL; v_leg_head text := NULL; v_seal record;
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

  -- The strongest valid seal, from the entries given or the ones already held.
  FOR v_seal IN
    SELECT s.hash, s.body FROM (
        SELECT x.hash, x.mac, x.body FROM jsonb_to_recordset(p_entries) AS x(kind text, hash text, mac text, body text) WHERE x.kind = 'seal'
        UNION ALL
        SELECT l.hash, l.mac, l.body FROM iam_v2.erasure_ledger l WHERE l.kind = 'seal') s
     WHERE iam_v2.ledger_mac_ok(s.hash, s.mac)
  LOOP
    BEGIN
      IF v_leg_through IS NULL OR (v_seal.body::jsonb ->> 'legacy_through')::bigint > v_leg_through THEN
        v_leg_through := (v_seal.body::jsonb ->> 'legacy_through')::bigint;
        v_leg_head := v_seal.body::jsonb ->> 'legacy_head';
      END IF;
    EXCEPTION WHEN OTHERS THEN
      NULL; -- a seal whose body cannot be read seals nothing
    END;
  END LOOP;

  FOR e IN SELECT x.* FROM jsonb_to_recordset(p_entries)
             AS x(ledger uuid, seq bigint, prev text, kind text, tenant uuid, site uuid, at_us bigint, body text, hash text, mac text)
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
    -- Checked: its own MAC, or coverage by a valid seal. A MAC that is present and wrong is a forgery, not a legacy entry.
    IF e.mac IS NOT NULL THEN
      IF NOT iam_v2.ledger_mac_ok(e.hash, e.mac) THEN
        v_conflict := jsonb_build_object('seq', e.seq, 'why', 'the entry was not written by this appliance (its authentication code does not check)');
        EXIT;
      END IF;
    ELSIF v_leg_through IS NOT NULL AND e.seq <= v_leg_through AND (e.seq < v_leg_through OR e.hash = v_leg_head) THEN
      NULL; -- a legacy entry covered by a valid seal; the chain ties it to the sealed hash
    ELSE
      v_conflict := jsonb_build_object('seq', e.seq, 'why', 'the entry carries no authentication code and is not covered by a seal, so it is not applied');
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
      INSERT INTO iam_v2.erasure_ledger (seq, ledger_id, tenant_id, site_id, kind, at_us, body, prev_hash, hash, confirmed, mac)
      VALUES (e.seq, e.ledger, e.tenant, e.site, e.kind, e.at_us, e.body, e.prev, e.hash, true, e.mac);
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
  'Applies the erasure ledger entries the database does not have (0148); since 0173 only entries whose authentication code checks, or that a valid seal covers. Granted to scd only.';

-- ---------------------------------------------------------------------------------------------------------
-- Ownership and grants (mirrored in deploy/gatep/svc-guest-data-rights-grants.sql).
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON iam_v2.ledger_mac_key FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.ledger_mac_of(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.ledger_receipt_of(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.ledger_mac_ok(text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.ledger_mac_key_ensure() FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.ledger_mac_key_get() FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.ledger_mac_key_install(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.ledger_seal_legacy(uuid,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.ledger_confirm_checked(text[],text[]) FROM PUBLIC;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.ledger_mac_key OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_mac_of(text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_receipt_of(text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_mac_ok(text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_mac_key_ensure() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_mac_key_get() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_mac_key_install(text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_seal_legacy(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.ledger_confirm_checked(text[],text[]) OWNER TO iam_v2_owner';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    -- edged no longer confirms by hash alone: it presents scd's receipt.
    EXECUTE 'REVOKE EXECUTE ON FUNCTION iam_v2.ledger_confirm(text[]) FROM svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_confirm_checked(text[],text[]) TO svc_edged';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_mac_ok(text,text) TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_mac_key_get() TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_mac_key_install(text) TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.ledger_seal_legacy(uuid,uuid) TO svc_scd';
  END IF;
END $g$;

COMMIT;
