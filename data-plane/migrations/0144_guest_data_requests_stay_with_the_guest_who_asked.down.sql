-- 0144 down -- guest-data requests and retention go back to 0143's behaviour. Every function 0144 replaced is
-- restored exactly as its previous migration left it (0129, 0138, 0139, 0141, 0142, 0143); the helpers and the
-- retention floor 0144 added are dropped. Data already anonymised, and activity-log entries already redacted,
-- stay as they are, as with any retention. Review messages closed by retention keep their decision: the
-- evidence-kind list is restored NOT VALID so those rows are not refused.

BEGIN;

-- restored from 0143_the_low_items_of_the_final_check.up.sql
CREATE OR REPLACE FUNCTION iam_v2.event_payload_for_guests(p_payload jsonb, p_guests jsonb, p_keep boolean)
    RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  WITH g AS (SELECT NULLIF(x->>'id', '') AS id, x->>'first' AS f, x->>'last' AS l
               FROM jsonb_array_elements(COALESCE(p_guests, '[]'::jsonb)) x),
  top AS (SELECT EXISTS (SELECT 1 FROM g WHERE g.l IS NOT NULL
                            AND upper(btrim(p_payload->>'last_name')) = g.l
                            AND (g.f IS NULL OR btrim(COALESCE(p_payload->>'first_name', '')) = ''
                                 OR upper(btrim(p_payload->>'first_name')) = g.f)) AS mine)
  SELECT CASE WHEN p_payload IS NULL OR jsonb_typeof(p_payload) <> 'object' THEN p_payload ELSE
    (CASE WHEN (SELECT mine FROM top) = p_keep THEN p_payload ELSE p_payload - 'last_name' - 'first_name' - 'vip' - 'travel_agent' END)
    || CASE WHEN jsonb_typeof(p_payload->'sharers') = 'array' THEN jsonb_build_object('sharers', COALESCE((
         SELECT jsonb_agg(sh) FROM jsonb_array_elements(p_payload->'sharers') sh
          WHERE EXISTS (SELECT 1 FROM g
                         WHERE (g.id IS NOT NULL AND sh->>'external_guest_id' = g.id)
                            OR (g.l IS NOT NULL AND upper(btrim(sh->>'last_name')) = g.l
                                AND (g.f IS NULL OR btrim(COALESCE(sh->>'first_name', '')) = ''
                                     OR upper(btrim(sh->>'first_name')) = g.f))) = p_keep),
         '[]'::jsonb))
       ELSE '{}'::jsonb END
  END;
$$;

-- restored from 0143_the_low_items_of_the_final_check.up.sql
CREATE OR REPLACE FUNCTION iam_v2.stay_absent_from_published_roster(p_stay uuid) RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT EXISTS (
    SELECT 1 FROM iam_v2.stays s
      JOIN iam_v2.pms_interface_runtime r
        ON r.tenant_id = s.tenant_id AND r.site_id = s.site_id AND r.pms_interface_id = s.pms_interface_id
       AND r.published_resync_generation > 0
     WHERE s.id = p_stay
       AND NOT EXISTS (
         SELECT 1 FROM iam_v2.stay_events se
          WHERE se.tenant_id = s.tenant_id AND se.site_id = s.site_id AND se.pms_interface_id = s.pms_interface_id
            AND se.admission_kind = 'RESYNC' AND se.resync_generation = r.published_resync_generation
            AND se.event_type IN ('GI', 'GC')
            AND (btrim(COALESCE(se.payload->>'reservation', '')) = s.external_reservation_id
                 OR (btrim(COALESCE(se.payload->>'reservation', '')) = ''
                     AND upper(btrim(COALESCE(se.payload->>'room', ''))) = s.normalized_room_number))));
$$;

-- restored from 0139_a_pms_event_that_waits_too_long_is_given_up.up.sql
CREATE OR REPLACE FUNCTION iam_v2.p3_stay_event_appendonly()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE ok_stay int;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'stay_events is append-only (DELETE rejected)';
  END IF;

  -- RETENTION ANONYMISATION (0127). The table owner, inside a retention run, may replace the payload and the
  -- event fingerprint of a processed event -- and nothing else.
  --
  -- GIVING UP A WAITING EVENT (0139; GDPR audit B2, D-B2). An event still PENDING long after any real PMS would
  -- have been processed may, in the same run, be closed as FAILED with review code RETENTION_GIVEN_UP, its
  -- payload blanked and its fingerprint replaced, in ONE move. No other column may change, so the processor can
  -- never act on a blanked payload: the event is terminal before anyone sees it empty.
  IF TG_OP = 'UPDATE'
     AND current_setting('iam_v2.anonymise', true) = 'retention'
     AND current_user = (SELECT pg_get_userbyid(c.relowner) FROM pg_class c WHERE c.oid = TG_RELID) THEN
    IF OLD.processing_status = 'PENDING' THEN
      IF NEW.processing_status = 'FAILED' AND NEW.review_code = 'RETENTION_GIVEN_UP' AND NEW.processed_at IS NOT NULL
         AND NEW.payload = '{"anonymised": true}'::jsonb AND NEW.stay_id IS NOT DISTINCT FROM OLD.stay_id
         AND (to_jsonb(NEW) - 'payload' - 'external_event_identity' - 'processing_status' - 'review_code' - 'processed_at')
             = (to_jsonb(OLD) - 'payload' - 'external_event_identity' - 'processing_status' - 'review_code' - 'processed_at') THEN
        RETURN NEW;
      END IF;
      RAISE EXCEPTION 'a waiting stay_events row may only be given up whole (FAILED, RETENTION_GIVEN_UP, payload blanked)';
    END IF;
    IF (to_jsonb(NEW) - 'payload' - 'external_event_identity')
       IS DISTINCT FROM (to_jsonb(OLD) - 'payload' - 'external_event_identity') THEN
      RAISE EXCEPTION 'stay_events anonymisation may replace only payload and external_event_identity of a processed event';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP = 'INSERT' THEN
    IF NEW.processing_status <> 'PENDING' THEN
      RAISE EXCEPTION 'stay_events must be inserted as PENDING (no terminal event inserted directly)';
    END IF;
    IF NEW.stay_id IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events cannot be inserted with a pre-resolved stay_id';
    END IF;
    IF NEW.processed_at IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.processed_at must be NULL on insert';
    END IF;
    IF NEW.review_code IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.review_code must be NULL on insert';
    END IF;
    RETURN NEW;
  END IF;

  -- UPDATE: immutable identity / normalization / admission columns
  IF   NEW.id                    IS DISTINCT FROM OLD.id
    OR NEW.tenant_id             IS DISTINCT FROM OLD.tenant_id
    OR NEW.site_id               IS DISTINCT FROM OLD.site_id
    OR NEW.pms_interface_id      IS DISTINCT FROM OLD.pms_interface_id
    OR NEW.external_event_identity IS DISTINCT FROM OLD.external_event_identity
    OR NEW.event_type            IS DISTINCT FROM OLD.event_type
    OR NEW.pms_timestamp_raw     IS DISTINCT FROM OLD.pms_timestamp_raw
    OR NEW.pms_timestamp_utc     IS DISTINCT FROM OLD.pms_timestamp_utc
    OR NEW.source_timezone       IS DISTINCT FROM OLD.source_timezone
    OR NEW.received_at           IS DISTINCT FROM OLD.received_at
    OR NEW.sequence_version      IS DISTINCT FROM OLD.sequence_version
    OR NEW.normalization_version IS DISTINCT FROM OLD.normalization_version
    OR NEW.clock_suspect         IS DISTINCT FROM OLD.clock_suspect
    OR NEW.payload               IS DISTINCT FROM OLD.payload
    OR NEW.admission_kind              IS DISTINCT FROM OLD.admission_kind
    OR NEW.admission_runtime_generation IS DISTINCT FROM OLD.admission_runtime_generation
    OR NEW.resync_generation           IS DISTINCT FROM OLD.resync_generation
    OR NEW.fingerprint_key_version     IS DISTINCT FROM OLD.fingerprint_key_version
  THEN
    RAISE EXCEPTION 'stay_events identity/normalization/admission columns are immutable (append-only)';
  END IF;

  -- Once terminal, the only permitted update is a no-op (no result/lineage field may change).
  IF OLD.processing_status <> 'PENDING' THEN
    IF NEW.processing_status IS DISTINCT FROM OLD.processing_status
       OR NEW.stay_id      IS DISTINCT FROM OLD.stay_id
       OR NEW.processed_at IS DISTINCT FROM OLD.processed_at
       OR NEW.review_code  IS DISTINCT FROM OLD.review_code THEN
      RAISE EXCEPTION 'terminal stay_events row is immutable (status/stay_id/processed_at/review_code frozen)';
    END IF;
    RETURN NEW;
  END IF;

  -- OLD is PENDING and staying PENDING: no result/lineage field may be set yet.
  IF NEW.processing_status = 'PENDING' THEN
    IF NEW.stay_id IS NOT NULL OR NEW.processed_at IS NOT NULL OR NEW.review_code IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events result fields (stay_id/processed_at/review_code) may only be set on PENDING->terminal';
    END IF;
    RETURN NEW;
  END IF;

  -- PENDING -> terminal (one move). A RESYNC row's PUBLICATION is enforced by the consumer against the
  -- interface's published_resync_generation boundary; the row itself stays immutable append-first evidence.
  IF NEW.processing_status NOT IN ('APPLIED','SKIPPED_DUPLICATE','MANUAL_REVIEW','FAILED') THEN
    RAISE EXCEPTION 'invalid stay_events terminal processing_status %', NEW.processing_status;
  END IF;
  IF NEW.processed_at IS NULL THEN
    RAISE EXCEPTION 'stay_events.processed_at is required on PENDING->%', NEW.processing_status;
  END IF;
  -- stay_id lineage: NULL -> a same-interface Stay only
  IF NEW.stay_id IS NOT NULL THEN
    IF OLD.stay_id IS NOT NULL THEN
      RAISE EXCEPTION 'stay_events.stay_id may only go from NULL to a resolved Stay';
    END IF;
    SELECT count(*) INTO ok_stay FROM iam_v2.stays s
      WHERE s.id = NEW.stay_id AND s.tenant_id = NEW.tenant_id
        AND s.site_id = NEW.site_id AND s.pms_interface_id = NEW.pms_interface_id;
    IF ok_stay <> 1 THEN
      RAISE EXCEPTION 'stay_events.stay_id must reference a Stay in the same tenant/site/pms_interface';
    END IF;
  END IF;
  -- review_code vocabulary: bounded machine code only (no PII / payload / stack traces)
  IF NEW.review_code IS NOT NULL AND NEW.review_code !~ '^[A-Z][A-Z0-9_]{0,63}$' THEN
    RAISE EXCEPTION 'stay_events.review_code must match ^[A-Z][A-Z0-9_]{0,63}$ (bounded machine code)';
  END IF;
  -- result-specific invariants
  IF NEW.processing_status = 'APPLIED' THEN
    IF NEW.stay_id IS NULL THEN RAISE EXCEPTION 'APPLIED requires a resolved same-interface stay_id'; END IF;
    IF NEW.review_code IS NOT NULL THEN RAISE EXCEPTION 'APPLIED must not carry a review_code'; END IF;
  ELSIF NEW.processing_status = 'MANUAL_REVIEW' THEN
    IF NEW.review_code IS NULL THEN RAISE EXCEPTION 'MANUAL_REVIEW requires a bounded review_code'; END IF;
  ELSIF NEW.processing_status = 'FAILED' THEN
    IF NEW.review_code IS NULL THEN RAISE EXCEPTION 'FAILED requires a bounded review_code'; END IF;
  END IF;
  -- SKIPPED_DUPLICATE: processed_at required (checked); stay_id/review_code optional (validated above).
  RETURN NEW;
END $function$;

-- restored from 0142_a_stay_link_does_not_lead_into_a_shared_stay.up.sql
CREATE OR REPLACE FUNCTION iam_v2.guest_data_scope(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS TABLE(o_kind text, o_id uuid, o_value text)
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[] := '{}'; p uuid[] := '{}'; s uuid[] := '{}'; own uuid[]; e uuid[]; d uuid[]; shared uuid[];
        resv text[]; ss uuid[] := '{}'; g record; gr uuid[] := '{}'; v_origin uuid;
BEGIN
  -- ONE GUEST OF A SHARED STAY (0141, GDPR audit C3). The scope is that guest's own rows on the stay (and on the
  -- stays it continues as), the stay itself as 'stay_shared' -- never anonymised, never exported whole -- and the
  -- stay's PMS events, from which only that guest is removed. Nothing about access or devices: a stay's access is
  -- shared by everyone in the room and cannot be told apart per guest. A guest who is the only one named on the
  -- stay is the whole stay, as before.
  IF p_kind = 'stay_guest' THEN
    SELECT sg.stay_id, sg.external_guest_id, sg.first_name_norm, sg.last_name_norm INTO g
      FROM stay_guests sg WHERE sg.id = p_id AND sg.tenant_id = p_tenant AND sg.site_id = p_site;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'guest record not found' USING ERRCODE = 'no_data_found';
    END IF;
    IF stay_named_guests(g.stay_id) > 1 THEN
      WITH RECURSIVE l(id, depth) AS (
        SELECT g.stay_id, 0
        UNION
        SELECT CASE WHEN k.from_stay = l.id THEN k.to_stay ELSE k.from_stay END, l.depth + 1
          FROM l JOIN stay_links k ON k.tenant_id = p_tenant AND k.site_id = p_site
                                  AND (k.from_stay = l.id OR k.to_stay = l.id)
         WHERE l.depth < 20)
      SELECT array_agg(DISTINCT l.id) INTO ss FROM l;
      SELECT COALESCE(array_agg(sg.id), '{}') INTO gr FROM stay_guests sg
       WHERE sg.stay_id = ANY (ss)
         AND (sg.id = p_id
              OR (g.external_guest_id IS NOT NULL AND sg.external_guest_id = g.external_guest_id)
              OR (g.last_name_norm IS NOT NULL AND sg.last_name_norm = g.last_name_norm
                  AND sg.first_name_norm IS NOT DISTINCT FROM g.first_name_norm));
      SELECT COALESCE(array_agg(x.external_reservation_id) FILTER (WHERE x.external_reservation_id NOT LIKE 'anon-%'), '{}')
        INTO resv FROM stays x WHERE x.id = ANY (ss);
      RETURN QUERY SELECT 'stay_guest'::text, sg.id, sg.display_name FROM stay_guests sg WHERE sg.id = ANY (gr);
      RETURN QUERY SELECT 'stay_shared'::text, z.x, NULL::text FROM unnest(ss) z(x);
      RETURN QUERY SELECT 'stay_event'::text, ev.id, ev.processing_status FROM stay_events ev
                    WHERE ev.tenant_id = p_tenant AND ev.site_id = p_site
                      AND (ev.stay_id = ANY (ss)
                           OR (ev.stay_id IS NULL AND cardinality(resv) > 0 AND ev.payload->>'reservation' = ANY (resv)));
      RETURN;
    END IF;
    s := ARRAY[g.stay_id];
    v_origin := g.stay_id;
  ELSIF p_kind = 'account' THEN
    SELECT COALESCE(array_agg(x.id), '{}') INTO a FROM guest_access_accounts x
     WHERE x.id = p_id AND x.tenant_id = p_tenant AND x.site_id = p_site;
  ELSIF p_kind = 'principal' THEN
    SELECT COALESCE(array_agg(x.id), '{}') INTO p FROM guest_principals x WHERE x.id = p_id AND x.tenant_id = p_tenant;
  ELSIF p_kind = 'stay' THEN
    SELECT COALESCE(array_agg(x.id), '{}') INTO s FROM stays x
     WHERE x.id = p_id AND x.tenant_id = p_tenant AND x.site_id = p_site;
    v_origin := p_id;
  ELSE
    RAISE EXCEPTION 'unknown guest record kind %', p_kind USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF cardinality(a) + cardinality(p) + cardinality(s) = 0 THEN
    RAISE EXCEPTION 'guest record not found' USING ERRCODE = 'no_data_found';
  END IF;

  s := s || COALESCE((SELECT array_agg(DISTINCT x.stay_id) FROM guest_access_accounts x
                       WHERE x.id = ANY (a) AND x.stay_id IS NOT NULL AND NOT x.stay_id = ANY (s)), '{}');
  -- A ROOM MOVE OR A TRANSFER IS THE SAME STAY CONTINUED (stay_links): followed both ways, at most 20 links.
  s := s || COALESCE((
    WITH RECURSIVE l(id, depth) AS (
      SELECT z.id, 0 FROM unnest(s) z(id)
      UNION
      SELECT CASE WHEN k.from_stay = l.id THEN k.to_stay ELSE k.from_stay END, l.depth + 1
        FROM l JOIN stay_links k ON k.tenant_id = p_tenant AND k.site_id = p_site
                                AND (k.from_stay = l.id OR k.to_stay = l.id)
       WHERE l.depth < 20)
    SELECT array_agg(DISTINCT l.id) FROM l WHERE NOT l.id = ANY (s)), '{}');
  -- A stay reached through an account, an identity or a stay link, with more than one guest named on it, is kept
  -- whole: neither it nor its shared access is in scope (0141; 0142, final check D1). The stay asked for is not
  -- filtered here: a shared one is refused by export and erasure ('co_guests').
  ss := ARRAY(SELECT z.x FROM unnest(s) z(x) WHERE stay_named_guests(z.x) > 1 AND z.x IS DISTINCT FROM v_origin);
  s := ARRAY(SELECT z.x FROM unnest(s) z(x) WHERE NOT z.x = ANY (ss));
  -- The guest's own access, then ONE step out through it -- never further, so a lookup cannot walk from one
  -- guest to the whole hotel.
  SELECT COALESCE(array_agg(x.id), '{}') INTO own FROM entitlements x
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site
     AND (x.guest_account_id = ANY (a) OR x.guest_principal_id = ANY (p) OR x.stay_id = ANY (s));
  SELECT a || COALESCE(array_agg(DISTINCT x.guest_account_id) FILTER (WHERE x.guest_account_id IS NOT NULL AND NOT x.guest_account_id = ANY (a)), '{}'),
         p || COALESCE(array_agg(DISTINCT x.guest_principal_id) FILTER (WHERE x.guest_principal_id IS NOT NULL AND NOT x.guest_principal_id = ANY (p)), '{}'),
         s || COALESCE(array_agg(DISTINCT x.stay_id) FILTER (WHERE x.stay_id IS NOT NULL AND NOT x.stay_id = ANY (s)), '{}')
    INTO a, p, s FROM entitlements x WHERE x.id = ANY (own);
  ss := ss || ARRAY(SELECT z.x FROM unnest(s) z(x)
                     WHERE stay_named_guests(z.x) > 1 AND NOT z.x = ANY (ss) AND z.x IS DISTINCT FROM v_origin);
  s := ARRAY(SELECT z.x FROM unnest(s) z(x) WHERE NOT z.x = ANY (ss));
  -- Asked for by stay: the guest of that stay is the guest who asked, so on a shared stay it continues as, their
  -- own rows are in scope -- by PMS guest number or by name -- and the other guests are not (0142, D1). Reached
  -- through an account or an identity, which guest it is cannot be known, so no rows are.
  IF p_kind IN ('stay', 'stay_guest') AND cardinality(ss) > 0 THEN
    SELECT COALESCE(array_agg(DISTINCT sg.id), '{}') INTO gr FROM stay_guests sg
     WHERE sg.stay_id = ANY (ss)
       AND EXISTS (SELECT 1 FROM stay_guests o
                    WHERE o.stay_id = ANY (s)
                      AND ((o.external_guest_id IS NOT NULL AND sg.external_guest_id = o.external_guest_id)
                           OR (o.last_name_norm IS NOT NULL AND sg.last_name_norm = o.last_name_norm
                               AND sg.first_name_norm IS NOT DISTINCT FROM o.first_name_norm)));
  END IF;
  -- Every access touching those records: what erasure must find ended before it may run.
  SELECT COALESCE(array_agg(x.id), '{}') INTO e FROM entitlements x
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site
     AND (x.guest_account_id = ANY (a) OR x.guest_principal_id = ANY (p) OR x.stay_id = ANY (s));

  SELECT COALESCE(array_agg(DISTINCT z.dev), '{}') INTO d FROM (
    SELECT ed.device_id FROM entitlement_devices ed WHERE ed.entitlement_id = ANY (own)
    UNION SELECT se.device_id FROM sessions se WHERE se.entitlement_id = ANY (own) AND se.device_id IS NOT NULL
    UNION SELECT c.device_id FROM principal_device_credentials c WHERE c.guest_principal_id = ANY (p)
    UNION SELECT dv.id FROM devices dv JOIN guest_access_accounts x ON x.id = ANY (a) AND dv.mac = x.created_by_device_mac
           WHERE dv.tenant_id = p_tenant AND dv.site_id = p_site
  ) z(dev);
  SELECT COALESCE(array_agg(z.dev), '{}') INTO shared FROM unnest(d) z(dev)
   WHERE EXISTS (SELECT 1 FROM entitlement_devices ed WHERE ed.device_id = z.dev AND NOT ed.entitlement_id = ANY (own))
      OR EXISTS (SELECT 1 FROM sessions se WHERE se.device_id = z.dev AND se.entitlement_id IS NOT NULL
                                              AND NOT se.entitlement_id = ANY (own))
      OR EXISTS (SELECT 1 FROM principal_device_credentials c WHERE c.device_id = z.dev AND NOT c.guest_principal_id = ANY (p));

  SELECT COALESCE(array_agg(x.external_reservation_id) FILTER (WHERE x.external_reservation_id NOT LIKE 'anon-%'), '{}')
    INTO resv FROM stays x WHERE x.id = ANY (s);

  RETURN QUERY SELECT 'account'::text, x.id, CASE WHEN x.username NOT LIKE 'anon-%' THEN x.username END
                 FROM guest_access_accounts x WHERE x.id = ANY (a);
  -- A removed account's username still carries the address it was created with.
  RETURN QUERY SELECT 'account'::text, x.id, substring(x.username FROM '^(.+)\.removed-[^.]+\.invalid$')
                 FROM guest_access_accounts x
                WHERE x.id = ANY (a) AND x.username NOT LIKE 'anon-%' AND x.username ~ '\.removed-[^.]+\.invalid$';
  RETURN QUERY SELECT 'principal'::text, x.id, x.display_name FROM guest_principals x WHERE x.id = ANY (p);
  RETURN QUERY SELECT 'identity'::text, i.guest_principal_id, i.factor_value_norm FROM guest_principal_identities i
                WHERE i.guest_principal_id = ANY (p) AND i.factor_value_norm NOT LIKE 'anon-%';
  RETURN QUERY SELECT 'stay'::text, x.id, CASE WHEN x.external_reservation_id NOT LIKE 'anon-%' THEN x.external_reservation_id END
                 FROM stays x WHERE x.id = ANY (s);
  -- The stay's PMS events, and those that never resolved to a stay but name its reservation.
  RETURN QUERY SELECT 'stay_event'::text, ev.id, ev.processing_status FROM stay_events ev
                WHERE ev.tenant_id = p_tenant AND ev.site_id = p_site
                  AND (ev.stay_id = ANY (s)
                       OR (ev.stay_id IS NULL AND cardinality(resv) > 0 AND ev.payload->>'reservation' = ANY (resv)));
  RETURN QUERY SELECT 'entitlement'::text, z.x, NULL::text FROM unnest(e) z(x);
  RETURN QUERY SELECT 'device'::text, dv.id, CASE WHEN dv.mac::text NOT LIKE '03:%' THEN dv.mac::text END
                 FROM devices dv WHERE dv.id = ANY (d) AND NOT dv.id = ANY (shared);
  RETURN QUERY SELECT 'device_shared'::text, z.x, NULL::text FROM unnest(shared) z(x);
  RETURN QUERY SELECT 'stay_shared'::text, z.x, NULL::text FROM unnest(ss) z(x);
  -- The guest's own rows on those shared stays, and the shared stays' PMS events, from which only they are removed.
  IF cardinality(gr) > 0 THEN
    RETURN QUERY SELECT 'stay_guest'::text, sg.id, sg.display_name FROM stay_guests sg WHERE sg.id = ANY (gr);
    RETURN QUERY SELECT 'stay_event'::text, ev.id, ev.processing_status FROM stay_events ev
                  WHERE ev.tenant_id = p_tenant AND ev.site_id = p_site AND ev.stay_id = ANY (ss);
  END IF;
END $function$;

-- restored from 0141_erasing_one_guest_leaves_the_other_guests_on_the_stay.up.sql
CREATE OR REPLACE FUNCTION iam_v2.guest_data_find(p_tenant uuid, p_site uuid, p_query text)
 RETURNS TABLE(o_kind text, o_id uuid, o_label text, o_detail text, o_status text, o_last_activity timestamp with time zone)
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE q text := lower(btrim(COALESCE(p_query, ''))); pat text; v_mac macaddr; v_ents uuid[] := '{}';
BEGIN
  IF length(q) NOT BETWEEN 2 AND 200 THEN
    RAISE EXCEPTION 'a search of 2-200 characters is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  pat := '%' || replace(replace(replace(q, '\', '\\'), '%', '\%'), '_', '\_') || '%';
  IF q ~ '^[0-9a-f]{2}([:.-]?[0-9a-f]{2}){5}$' THEN
    BEGIN v_mac := q::macaddr; EXCEPTION WHEN others THEN v_mac := NULL; END;
  END IF;
  -- A MAC address finds whoever held access on that device: authorised on it, or with a session from it.
  IF v_mac IS NOT NULL THEN
    SELECT COALESCE(array_agg(DISTINCT z.ent), '{}') INTO v_ents FROM (
      SELECT ed.entitlement_id FROM entitlement_devices ed JOIN devices dv ON dv.id = ed.device_id
       WHERE dv.tenant_id = p_tenant AND dv.site_id = p_site AND dv.mac = v_mac
      UNION SELECT se.entitlement_id FROM sessions se JOIN devices dv ON dv.id = se.device_id
       WHERE dv.tenant_id = p_tenant AND dv.site_id = p_site AND dv.mac = v_mac AND se.entitlement_id IS NOT NULL
    ) z(ent);
  END IF;

  RETURN QUERY
  SELECT r.* FROM (
    SELECT 'account'::text, x.id, x.username, x.display_name,
           CASE WHEN NOT x.enabled AND x.username LIKE '%.removed-%.invalid' THEN 'removed'
                WHEN x.enabled THEN 'enabled' ELSE 'disabled' END,
           GREATEST(x.created_at, x.last_login_at)
      FROM guest_access_accounts x
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.username NOT LIKE 'anon-%'
       AND (lower(x.username) LIKE pat OR lower(COALESCE(x.display_name, '')) LIKE pat
            OR x.created_by_device_mac = v_mac
            OR EXISTS (SELECT 1 FROM entitlements en WHERE en.id = ANY (v_ents) AND en.guest_account_id = x.id))
    UNION ALL
    SELECT 'principal'::text, g.id,
           (SELECT string_agg(i.factor_value_norm, ', ' ORDER BY i.verified_at DESC NULLS LAST)
              FROM guest_principal_identities i
             WHERE i.guest_principal_id = g.id AND i.factor_value_norm NOT LIKE 'anon-%'),
           g.display_name,
           (SELECT string_agg(DISTINCT i.factor_type, ', ') FROM guest_principal_identities i
             WHERE i.guest_principal_id = g.id AND i.factor_value_norm NOT LIKE 'anon-%'),
           (SELECT max(i.verified_at) FROM guest_principal_identities i WHERE i.guest_principal_id = g.id)
      FROM guest_principals g
     WHERE g.tenant_id = p_tenant
       AND (lower(COALESCE(g.display_name, '')) LIKE pat
            OR EXISTS (SELECT 1 FROM guest_principal_identities i
                        WHERE i.guest_principal_id = g.id AND i.factor_value_norm NOT LIKE 'anon-%'
                          AND lower(i.factor_value_norm) LIKE pat)
            OR EXISTS (SELECT 1 FROM principal_device_credentials c JOIN devices dv ON dv.id = c.device_id
                        WHERE c.guest_principal_id = g.id AND dv.mac = v_mac)
            OR EXISTS (SELECT 1 FROM entitlements en WHERE en.id = ANY (v_ents) AND en.guest_principal_id = g.id))
    UNION ALL
    -- A stay with more than one guest named on it is offered one guest at a time (0141, GDPR audit C3).
    SELECT CASE WHEN gs.id IS NULL THEN 'stay' ELSE 'stay_guest' END, COALESCE(gs.id, x.id),
           'Room ' || COALESCE(x.normalized_room_number, '?') || ' · reservation ' || x.external_reservation_id,
           COALESCE(gs.display_name,
             (SELECT string_agg(sg.display_name, ', ' ORDER BY sg.is_primary DESC) FROM stay_guests sg WHERE sg.stay_id = x.id)),
           x.status,
           COALESCE(x.effective_checkout_at, x.departure::timestamptz, x.arrival::timestamptz)
      FROM stays x
      LEFT JOIN LATERAL (SELECT sg.id, COALESCE(sg.display_name, initcap(concat_ws(' ', sg.first_name_norm, sg.last_name_norm))) AS display_name
                           FROM stay_guests sg
                          WHERE sg.stay_id = x.id AND stay_named_guests(x.id) > 1
                            AND num_nonnulls(sg.first_name_norm, sg.last_name_norm, sg.display_name) > 0) gs ON true
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.external_reservation_id NOT LIKE 'anon-%'
       AND (lower(COALESCE(x.normalized_room_number, '')) = q OR lower(x.external_reservation_id) = q
            OR EXISTS (SELECT 1 FROM stay_guests sg
                        WHERE sg.stay_id = x.id
                          AND (lower(COALESCE(sg.display_name, '')) LIKE pat OR lower(COALESCE(sg.last_name_norm, '')) LIKE pat
                               OR lower(COALESCE(sg.first_name_norm, '')) LIKE pat))
            OR EXISTS (SELECT 1 FROM entitlements en WHERE en.id = ANY (v_ents) AND en.stay_id = x.id))
  ) r ORDER BY 6 DESC NULLS LAST LIMIT 50;
END $function$;

-- restored from 0143_the_low_items_of_the_final_check.up.sql
CREATE OR REPLACE FUNCTION iam_v2.guest_data_export_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS jsonb
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; ev uuid[]; macs macaddr[]; se_ids uuid[]; pu_ids uuid[];
        ss uuid[]; gr uuid[]; v_who jsonb; v_ids text[];
BEGIN
  -- A stay with more than one guest is exported one guest at a time (0141, GDPR audit C3): the whole stay would
  -- hand one guest the others' data (Art. 15(4)).
  IF p_kind = 'stay' AND stay_named_guests(p_id) > 1 THEN
    RAISE EXCEPTION 'this stay has more than one guest: export each guest from the search'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'account'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'principal'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'entitlement'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'device'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_event'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_shared'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_guest'), '{}')
    INTO a, p, s, e, d, ev, ss, gr FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
  SELECT COALESCE(array_agg(dv.mac), '{}') INTO macs FROM devices dv WHERE dv.id = ANY (d);
  -- The guest, as named on their own stays and on the shared stays: their names are kept in the events, nobody
  -- else's.
  v_who := stay_guests_as_json(gr || ARRAY(SELECT sg.id FROM stay_guests sg WHERE sg.stay_id = ANY (s)));
  SELECT COALESCE(array_agg(sg.external_guest_id) FILTER (WHERE sg.external_guest_id IS NOT NULL), '{}') INTO v_ids
    FROM stay_guests sg WHERE sg.id = ANY (gr);
  SELECT COALESCE(array_agg(se.id), '{}') INTO se_ids FROM sessions se
   WHERE se.tenant_id = p_tenant AND (se.entitlement_id = ANY (e) OR se.device_id = ANY (d));
  SELECT COALESCE(array_agg(DISTINCT pu.id), '{}') INTO pu_ids FROM purchases pu
   WHERE pu.tenant_id = p_tenant AND pu.site_id = p_site
     AND (pu.id IN (SELECT x.purchase_id FROM entitlements x WHERE x.id = ANY (e)) OR pu.stay_id = ANY (s));

  RETURN jsonb_build_object(
    'client_accounts', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'username', x.username, 'display_name', x.display_name, 'notes', x.notes, 'enabled', x.enabled,
        'self_registered', x.self_registered, 'created_at', x.created_at, 'valid_from', x.valid_from,
        'valid_until', x.valid_until, 'email_verified_at', x.email_verified_at,
        'last_sign_in_at', x.last_login_at, 'sign_in_count', x.login_count) ORDER BY x.created_at), '[]'::jsonb)
      FROM guest_access_accounts x WHERE x.id = ANY (a)),
    'guest_identities', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'display_name', g.display_name,
        'sign_in_methods', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'type', i.factor_type, 'issuer', i.factor_issuer, 'value', i.factor_value_norm,
            'verified_at', i.verified_at, 'details', i.attrs) ORDER BY i.verified_at), '[]'::jsonb)
          FROM guest_principal_identities i WHERE i.guest_principal_id = g.id))), '[]'::jsonb)
      FROM guest_principals g WHERE g.id = ANY (p)),
    'remembered_devices', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'method', c.method, 'device_mac_address', dv.mac, 'created_at', c.created_at, 'expires_at', c.expires_at,
        'last_used_at', c.last_used_at, 'revoked_at', c.revoked_at) ORDER BY c.created_at), '[]'::jsonb)
      FROM principal_device_credentials c LEFT JOIN devices dv ON dv.id = c.device_id
     WHERE c.guest_principal_id = ANY (p)),
    'hotel_stays', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'reservation', x.external_reservation_id, 'room', x.normalized_room_number, 'room_type', x.room_type,
        'rate_plan', x.rate_plan,
        'vip', CASE WHEN x.id = ANY (s) THEN x.vip END, 'travel_agent', CASE WHEN x.id = ANY (s) THEN x.travel_agent END,
        'arrival', x.arrival, 'departure', x.departure, 'checked_out_at', x.effective_checkout_at,
        'status', x.status,
        'guests', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'name', sg.display_name, 'first_name', sg.first_name_norm, 'last_name', sg.last_name_norm,
            'pms_guest_id', sg.external_guest_id, 'primary', sg.is_primary) ORDER BY sg.is_primary DESC), '[]'::jsonb)
          FROM stay_guests sg WHERE sg.stay_id = x.id AND (x.id = ANY (s) OR sg.id = ANY (gr))),
        'linked_to', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'reason', k.reason, 'other_reservation', o.external_reservation_id,
            'direction', CASE WHEN k.from_stay = x.id THEN 'continued_as' ELSE 'continues' END)), '[]'::jsonb)
          FROM stay_links k JOIN stays o ON o.id = CASE WHEN k.from_stay = x.id THEN k.to_stay ELSE k.from_stay END
         WHERE k.from_stay = x.id OR k.to_stay = x.id)) ORDER BY x.arrival), '[]'::jsonb)
      FROM stays x WHERE x.id = ANY (s) OR (x.id = ANY (ss) AND cardinality(gr) > 0)),
    'pms_events', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'type', x.event_type, 'received_at', x.received_at, 'pms_time', x.pms_timestamp_utc,
        'status', x.processing_status,
        'details', CASE WHEN cardinality(gr) > 0 THEN event_payload_for_guests(x.payload, v_who, true) ELSE x.payload END)
        ORDER BY x.received_at), '[]'::jsonb)
      FROM stay_events x WHERE x.id = ANY (ev)),
    'post_stay_access', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'status', x.status, 'issued_at', x.issued_at, 'issued_via', x.issued_via, 'valid_until', x.valid_until,
        'pin_set_at', x.pin_set_at, 'revoked_at', x.revoked_at, 'revoke_reason', x.revoke_reason) ORDER BY x.created_at), '[]'::jsonb)
      FROM post_stay_profiles x WHERE x.origin_stay_id = ANY (s) OR (x.origin_stay_id = ANY (ss) AND cardinality(gr) > 0)),
    'internet_access', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'status', x.status, 'activated_at', x.activated_at, 'ends_at', x.window_ends_at,
        'ended_at', x.terminated_at, 'ended_because', x.terminal_reason,
        'data_used_bytes', x.consumed_data_bytes, 'online_seconds', x.consumed_online_seconds)
        ORDER BY x.activated_at NULLS LAST), '[]'::jsonb)
      FROM entitlements x WHERE x.id = ANY (e)),
    'purchases', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'amount_minor', pu.amount_minor, 'currency', pu.currency, 'currency_exponent', pu.currency_exponent,
        'tax_amount_minor', pu.tax_amount_minor, 'state', pu.state, 'paid_by', pu.trigger)), '[]'::jsonb)
      FROM purchases pu WHERE pu.id = ANY (pu_ids)),
    'payments', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'type', t.transaction_type, 'provider', t.provider, 'amount_minor', t.amount_minor, 'currency', t.currency,
        'currency_exponent', t.currency_exponent, 'status', t.status, 'provider_reference', t.provider_txn_ref,
        'created_at', t.intent_created_at) ORDER BY t.intent_created_at), '[]'::jsonb)
      FROM payment_transactions t JOIN settlements st ON st.id = t.settlement_id
     WHERE st.purchase_id = ANY (pu_ids)),
    'refunds_that_ended_access', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'captured_minor', x.captured_minor, 'refunded_minor', x.refunded_minor, 'ended_at', x.terminated_at)
        ORDER BY x.terminated_at), '[]'::jsonb)
      FROM refund_access_terminations x WHERE x.entitlement_id = ANY (e)),
    'room_charges', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'type', x.posting_type, 'amount_minor', x.amount_minor, 'currency', x.currency,
        'currency_exponent', x.currency_exponent, 'pms_guest_number', x.g_number, 'created_at', x.created_at)
        ORDER BY x.created_at), '[]'::jsonb)
      FROM pms_postings x WHERE x.tenant_id = p_tenant
       AND (x.purchase_id = ANY (pu_ids) OR x.stay_id = ANY (s) OR (x.stay_id = ANY (ss) AND x.g_number = ANY (v_ids)))),
    'devices', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'mac_address', dv.mac, 'first_seen', dv.first_seen, 'last_seen', dv.last_seen, 'last_ip', dv.last_ip,
        'networks_seen_on', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'guest_network', na.guest_network_id, 'first_seen', na.first_seen, 'last_seen', na.last_seen)
            ORDER BY na.first_seen), '[]'::jsonb)
          FROM device_network_appearances na WHERE na.device_id = dv.id),
        'staff_actions', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'action', sa.action, 'outcome', sa.outcome, 'by', sa.actor, 'reason', sa.reason, 'at', sa.acted_at)
            ORDER BY sa.acted_at), '[]'::jsonb)
          FROM device_slot_actions sa WHERE sa.device_id = dv.id OR sa.replacement_device_id = dv.id))
        ORDER BY dv.first_seen), '[]'::jsonb)
      FROM devices dv WHERE dv.id = ANY (d)),
    'sessions', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'started', se.started, 'ended', se.ended, 'end_reason', se.end_reason, 'ip', se.ip, 'mac_address', se.mac,
        'bytes_up', se.bytes_up, 'bytes_down', se.bytes_down,
        'usage_samples', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'at', ar.sampled_at, 'bytes_up', ar.bytes_up, 'bytes_down', ar.bytes_down) ORDER BY ar.sampled_at), '[]'::jsonb)
          FROM accounting_records ar WHERE ar.session_id = se.id)) ORDER BY se.started), '[]'::jsonb)
      FROM sessions se WHERE se.id = ANY (se_ids)),
    'sign_in_attempts', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'at', sa.occurred_at, 'room_entered', sa.submitted_room, 'checked_against', sa.verifier_kind,
        'result', sa.result, 'guest_network', sa.guest_network_name, 'device_ip', sa.device_ip,
        'device_mac_address', sa.device_mac) ORDER BY sa.occurred_at), '[]'::jsonb)
      FROM sign_in_attempts sa
     WHERE sa.tenant_id = p_tenant AND sa.site_id = p_site
       AND (sa.matched_stay_id = ANY (s) OR sa.entitlement_id = ANY (e) OR sa.device_mac = ANY (macs))),
    'sign_in_restrictions', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'device_mac_address', x.device_mac, 'guest_network', x.guest_network_name, 'room_entered', x.last_submitted_room,
        'restricted_at', x.restricted_at, 'until', x.expires_at, 'failures', x.failure_count, 'reason', x.reason,
        'released_at', x.released_at, 'release_reason', x.release_reason) ORDER BY x.restricted_at), '[]'::jsonb)
      FROM guest_signin_restrictions x
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs)),
    'verification_access', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'device_mac_address', x.device_mac, 'ip', x.ip, 'guest_network', x.guest_network_id,
        'started_at', x.started_at, 'expires_at', x.expires_at, 'ended_at', x.ended_at, 'end_reason', x.end_reason,
        'used_bytes', x.used_bytes) ORDER BY x.started_at), '[]'::jsonb)
      FROM verification_access_grants x
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs)))
  || CASE WHEN cardinality(ss) > 0 THEN jsonb_build_object('shared_stays_note',
       'Other guests are named on a stay in this export. Only this guest''s own details are included: the room''s '
       || 'internet access, devices and sessions are shared by everyone on the stay and are not included.')
     ELSE '{}'::jsonb END;
END $function$;

-- restored from 0143_the_low_items_of_the_final_check.up.sql
CREATE OR REPLACE FUNCTION iam_v2.erase_guest_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text, p_public_counts jsonb)
 RETURNS jsonb
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; ev uuid[]; macs macaddr[]; live text[] := '{}';
        v_id uuid; v_shared integer; v_attempts integer; v_sessions integer; v_k integer; v_verif integer := 0;
        v_events integer := 0; v_given integer := 0; v_ref text; v_counts jsonb;
        ss uuid[]; gr uuid[]; v_who jsonb; v_guests integer := 0;
BEGIN
  IF p_operator IS NULL OR length(btrim(p_operator)) NOT BETWEEN 1 AND 200 THEN
    RAISE EXCEPTION 'an operator is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) NOT BETWEEN 4 AND 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'account'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'principal'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'entitlement'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'device'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_event'), '{}'),
         count(*) FILTER (WHERE o_kind = 'device_shared'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_shared'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_guest'), '{}')
    INTO a, p, s, e, d, ev, v_shared, ss, gr FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
  PERFORM 1 FROM devices dv WHERE dv.id = ANY (d) FOR UPDATE;
  SELECT COALESCE(array_agg(dv.mac), '{}') INTO macs FROM devices dv WHERE dv.id = ANY (d) AND dv.mac::text NOT LIKE '03:%';

  -- REFUSED WHILE LIVE (D-C3). The PMS would copy an open stay straight back, and anonymising access or a
  -- device in use would cut a guest off mid-session. A stay the PMS left open after its departure is not live.
  -- A STAY WITH MORE THAN ONE GUEST IS ERASED ONE GUEST AT A TIME (0141, GDPR audit C3): erasing it whole would
  -- erase the other guests too, who did not ask.
  IF p_kind = 'stay' AND stay_named_guests(p_id) > 1 THEN
    live := live || 'co_guests'::text;
  END IF;
  IF EXISTS (SELECT 1 FROM stays x WHERE (x.id = ANY (s) OR (x.id = ANY (ss) AND cardinality(gr) > 0))
                                     AND x.status IN ('RESERVED', 'IN_HOUSE')
                                     AND ((x.departure IS NULL AND NOT stay_absent_from_published_roster(x.id))
                                          OR x.departure >= current_date - 1)) THEN
    live := live || 'pms_open'::text;
  END IF;
  -- A PMS event received in the last 24 hours may still be processed: refused. An older one that is still
  -- waiting will not be, and is given up below (0139, D-B2).
  IF EXISTS (SELECT 1 FROM stay_events x WHERE x.id = ANY (ev) AND x.processing_status = 'PENDING'
                                          AND x.received_at >= now() - interval '24 hours') THEN
    live := live || 'pms_pending'::text;
  END IF;
  IF EXISTS (SELECT 1 FROM entitlements x WHERE x.id = ANY (e) AND x.status <> 'TERMINATED')
     OR EXISTS (SELECT 1 FROM post_stay_profiles x
                 WHERE x.origin_stay_id = ANY (s) AND x.status = 'ACTIVE' AND x.valid_until > now()) THEN
    live := live || 'access_live'::text;
  END IF;
  IF EXISTS (SELECT 1 FROM sessions x WHERE (x.device_id = ANY (d) OR x.entitlement_id = ANY (e)) AND x.ended IS NULL)
     OR EXISTS (SELECT 1 FROM verification_access_grants x
                 WHERE x.tenant_id = p_tenant AND x.device_mac = ANY (macs) AND x.ended_at IS NULL AND x.expires_at > now()) THEN
    live := live || 'online'::text;
  END IF;
  IF EXISTS (SELECT 1 FROM guest_signin_restrictions x
              WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs)
                AND x.released_at IS NULL AND (x.expires_at IS NULL OR x.expires_at >= now())) THEN
    live := live || 'signin_restricted'::text;
  END IF;
  IF cardinality(live) > 0 THEN
    RAISE EXCEPTION 'guest_data_live' USING ERRCODE = 'object_in_use', DETAIL = array_to_string(live, ',');
  END IF;

  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  -- What the guest typed and where from: kept as a count, nothing else.
  UPDATE sign_in_attempts x
     SET submitted_room = NULL, device_ip = NULL, device_mac = NULL,
         sensitive_ciphertext = NULL, sensitive_nonce = NULL, encryption_key_id = NULL, cipher_version = NULL
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site
     AND (x.matched_stay_id = ANY (s) OR x.entitlement_id = ANY (e) OR x.device_mac = ANY (macs))
     AND num_nonnulls(x.submitted_room, x.device_ip, x.device_mac, x.sensitive_ciphertext) > 0;
  GET DIAGNOSTICS v_attempts = ROW_COUNT;
  -- The MAC address is the key of these records, so it is replaced before the device itself is anonymised.
  UPDATE verification_access_grants x
     SET device_mac = anonymised_mac(), ip = '0.0.0.0'::inet, destination_digest = encode(public.gen_random_bytes(32), 'hex')
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_verif := v_verif + v_k;
  UPDATE guest_signin_restrictions x SET device_mac = anonymised_mac(), last_submitted_room = NULL
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_verif := v_verif + v_k;
  UPDATE otp_device_bindings x SET device_mac = anonymised_mac()
   WHERE x.tenant_id = p_tenant AND x.device_mac = ANY (macs);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_verif := v_verif + v_k;
  UPDATE sessions x SET ip = NULL, mac = NULL
   WHERE x.entitlement_id = ANY (e) AND x.ended IS NOT NULL AND (x.ip IS NOT NULL OR x.mac IS NOT NULL);
  GET DIAGNOSTICS v_sessions = ROW_COUNT;

  -- PMS events that never resolved to a stay but name the guest's reservation (stay events are anonymised with
  -- their stay below). A waiting one older than 24 hours is given up first; a newer one was refused above.
  PERFORM begin_controlled_operation('stay');
  v_given := give_up_waiting_stay_events(p_tenant, p_site, now() - interval '24 hours', ev, 10000);
  IF EXISTS (SELECT 1 FROM stay_events x WHERE x.id = ANY (ev) AND x.processing_status = 'PENDING') THEN
    RAISE EXCEPTION 'guest_data_live' USING ERRCODE = 'object_in_use', DETAIL = 'pms_pending';
  END IF;
  UPDATE stay_events x
     SET payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE x.id = ANY (ev) AND x.stay_id IS NULL AND x.processing_status <> 'PENDING'
     AND (cardinality(s) > 0 OR cardinality(gr) = 0)
     AND x.payload IS DISTINCT FROM '{"anonymised": true}'::jsonb;
  GET DIAGNOSTICS v_events = ROW_COUNT;
  IF v_given > 0 THEN
    v_events := v_events + (SELECT count(*) FROM stay_events x
                             WHERE x.id = ANY (ev) AND x.stay_id IS NULL AND x.review_code = 'RETENTION_GIVEN_UP');
  END IF;

  FOREACH v_id IN ARRAY d LOOP PERFORM anonymise_device(v_id); END LOOP;
  FOREACH v_id IN ARRAY a LOOP PERFORM anonymise_guest_account(v_id); END LOOP;
  FOREACH v_id IN ARRAY p LOOP PERFORM anonymise_guest_principal(v_id); END LOOP;
  FOREACH v_id IN ARRAY s LOOP PERFORM anonymise_stay(v_id); END LOOP;
  SELECT v_events + count(*) INTO v_events FROM stay_events x WHERE x.id = ANY (ev) AND x.stay_id IS NOT NULL
                                                                 AND x.stay_id = ANY (s);
  -- One guest of a shared stay: their own rows, their copy in migration 0066's rollback table, and their name in
  -- the stay's PMS events. The stay, its other guests and its shared access stay as they are.
  IF cardinality(gr) > 0 THEN
    v_who := stay_guests_as_json(gr);
    UPDATE stay_events x SET payload = event_payload_for_guests(x.payload, v_who, false)
     WHERE x.id = ANY (ev) AND x.processing_status <> 'PENDING'
       AND x.payload IS DISTINCT FROM event_payload_for_guests(x.payload, v_who, false);
    GET DIAGNOSTICS v_k = ROW_COUNT; v_events := v_events + v_k;
    UPDATE stay_guests x
       SET external_guest_id = NULL, first_name_norm = NULL, last_name_norm = NULL, display_name = NULL, pin_hash = NULL
     WHERE x.id = ANY (gr);
    GET DIAGNOSTICS v_guests = ROW_COUNT;
    IF to_regclass('iam_v2.backfill_0066_identity_text') IS NOT NULL THEN
      EXECUTE $q$
        UPDATE iam_v2.backfill_0066_identity_text x
           SET prior_first_name = NULL, prior_last_name = NULL, prior_room = NULL
         WHERE x.row_id = ANY ($1) AND num_nonnulls(x.prior_first_name, x.prior_last_name, x.prior_room) > 0
      $q$ USING gr;
    END IF;
  END IF;
  PERFORM set_config('iam_v2.anonymise', '', true);

  v_ref := 'ER-' || upper(encode(public.gen_random_bytes(6), 'hex'));
  v_counts := jsonb_build_object(
    'client_accounts', cardinality(a), 'guest_identities', cardinality(p), 'hotel_stays', cardinality(s),
    'stay_guests', v_guests, 'hotel_stays_kept_shared', cardinality(ss),
    'pms_events', v_events, 'devices', cardinality(d), 'devices_kept_shared', v_shared, 'sessions', v_sessions,
    'sign_in_attempts', v_attempts, 'verification_records', v_verif) || COALESCE(p_public_counts, '{}'::jsonb);
  INSERT INTO guest_data_erasures (erasure_ref, tenant_id, site_id, subject_kind, erased_by, erase_reason, counts)
  VALUES (v_ref, p_tenant, p_site, p_kind, btrim(p_operator), btrim(p_reason), v_counts);
  RETURN jsonb_build_object('erasure_ref', v_ref, 'counts', v_counts);
END $function$;

-- restored from 0143_the_low_items_of_the_final_check.up.sql
CREATE OR REPLACE FUNCTION public.guest_data_export(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS jsonb
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v jsonb; v_ids text[]; v_vals text[];
        a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; v_ctx uuid[];
BEGIN
  v := iam_v2.guest_data_export_records(p_tenant, p_site, p_kind, p_id);
  SELECT COALESCE(array_agg(DISTINCT o_id::text) FILTER (
             WHERE o_kind <> 'stay_shared'
               AND NOT (o_kind = 'stay_event' AND EXISTS (SELECT 1 FROM iam_v2.stay_events e
                                                         WHERE e.id = o_id AND iam_v2.stay_named_guests(e.stay_id) > 1))), '{}'),
         COALESCE(array_agg(DISTINCT lower(o_value)) FILTER (WHERE o_kind IN ('account', 'identity') AND o_value IS NOT NULL), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'account'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind IN ('principal', 'identity')), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'entitlement'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'device'), '{}')
    INTO v_ids, v_vals, a, p, s, e, d FROM iam_v2.guest_data_scope(p_tenant, p_site, p_kind, p_id);
  -- The guest's sign-ins: by their own subject, from a device only they used, or the one that paid for access
  -- they held. A shared device is not in d, so another guest's sign-in on it is not exported.
  SELECT COALESCE(array_agg(ac.id), '{}') INTO v_ctx FROM iam_v2.auth_contexts ac
   WHERE ac.tenant_id = p_tenant AND ac.site_id = p_site
     AND (ac.guest_account_id = ANY (a) OR ac.guest_principal_id = ANY (p) OR ac.stay_id = ANY (s)
          OR ac.device_id = ANY (d)
          OR ac.id IN (SELECT pu.auth_context_id FROM iam_v2.purchases pu
                         JOIN iam_v2.entitlements en ON en.purchase_id = pu.id WHERE en.id = ANY (e)));
  RETURN jsonb_build_object('format', 'stayconnect.guest-data-export/2', 'exported_at', now()) || v || jsonb_build_object(
    'verification_codes', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'channel', o.channel, 'sent_to', o.destination, 'sent_at', o.issued_at, 'used_at', o.consumed_at,
        'requested_from_ip', o.ip, 'requested_from_browser', o.user_agent)
        ORDER BY o.issued_at), '[]'::jsonb)
      FROM auth_otps o WHERE o.tenant_id = p_tenant AND lower(o.destination) = ANY (v_vals)),
    'activity_log', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'at', l.ts, 'action', l.action, 'by', l.actor_type, 'details', l.payload) ORDER BY l.ts), '[]'::jsonb)
      FROM audit_log l WHERE l.tenant_id = p_tenant AND l.target_id = ANY (v_ids)),
    'privacy_notices_shown', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'signed_in_at', ac.privacy_notice_shown_at, 'sign_in_method', ac.method, 'notice', left(pv.fingerprint, 12))
        ORDER BY ac.privacy_notice_shown_at), '[]'::jsonb)
      FROM iam_v2.auth_contexts ac JOIN privacy_notice_versions pv ON pv.id = ac.privacy_notice_version_id
     WHERE ac.id = ANY (v_ctx)),
    'privacy_notices', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'notice', left(pv.fingerprint, 12), 'first_shown_at', pv.first_shown_at, 'text', pv.privacy_text,
        'link', pv.privacy_url, 'terms_link', pv.terms_url) ORDER BY pv.first_shown_at), '[]'::jsonb)
      FROM privacy_notice_versions pv
     WHERE pv.id IN (SELECT ac.privacy_notice_version_id FROM iam_v2.auth_contexts ac WHERE ac.id = ANY (v_ctx))));
END $function$;

-- restored from 0143_the_low_items_of_the_final_check.up.sql
CREATE OR REPLACE FUNCTION public.guest_data_erase(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text)
 RETURNS jsonb
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_ids text[]; v_vals text[]; v_pats text[]; n_codes integer; n_oauth integer; n_audit integer;
BEGIN
  SELECT COALESCE(array_agg(DISTINCT o_id::text) FILTER (
             WHERE o_kind <> 'stay_shared'
               AND NOT (o_kind = 'stay_event' AND EXISTS (SELECT 1 FROM iam_v2.stay_events e
                                                         WHERE e.id = o_id AND iam_v2.stay_named_guests(e.stay_id) > 1))), '{}'),
         COALESCE(array_agg(DISTINCT lower(o_value))
                    FILTER (WHERE o_kind IN ('account', 'identity', 'device') AND length(o_value) >= 6), '{}')
    INTO v_ids, v_vals FROM iam_v2.guest_data_scope(p_tenant, p_site, p_kind, p_id);

  UPDATE auth_otps o SET destination = 'anonymised', ip = NULL, user_agent = NULL
   WHERE o.tenant_id = p_tenant AND lower(o.destination) = ANY (v_vals);
  GET DIAGNOSTICS n_codes = ROW_COUNT;
  UPDATE social_oauth_states x SET client_ip = NULL, client_mac = NULL, user_agent = NULL
   WHERE x.tenant_id = p_tenant AND x.client_mac::text = ANY (v_vals);
  GET DIAGNOSTICS n_oauth = ROW_COUNT;
  -- Audit entries about the guest: by the ids of their records, or naming their address, username or MAC. What
  -- happened, when and which staff member did it stay, as in the retention run.
  v_pats := ARRAY(SELECT '%' || replace(replace(replace(x, '\', '\\'), '%', '\%'), '_', '\_') || '%' FROM unnest(v_vals) x);
  UPDATE audit_log l
     SET ip = NULL, user_agent = NULL, target_id = NULL, payload = '{"anonymised": true}'::jsonb,
         actor_id = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.actor_id END
   WHERE l.tenant_id = p_tenant
     AND l.payload IS DISTINCT FROM '{"anonymised": true}'::jsonb
     AND (l.target_id = ANY (v_ids) OR lower(l.target_id) = ANY (v_vals) OR lower(l.payload::text) LIKE ANY (v_pats));
  GET DIAGNOSTICS n_audit = ROW_COUNT;

  -- Refuses (and so undoes the above) while anything is live.
  RETURN iam_v2.erase_guest_records(p_tenant, p_site, p_kind, p_id, p_operator, p_reason,
           jsonb_build_object('verification_codes', n_codes, 'oauth_states', n_oauth, 'audit_entries', n_audit));
END $function$;

-- restored from 0143_the_low_items_of_the_final_check.up.sql
CREATE OR REPLACE FUNCTION iam_v2.run_data_retention(p_tenant uuid, p_site uuid, p_batch integer)
 RETURNS TABLE(o_category text, o_cutoff timestamp with time zone, o_anonymised integer)
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE r record; v_cut timestamptz; v_n integer; v_k integer; v_id uuid;
BEGIN
  IF p_batch IS NULL OR p_batch NOT BETWEEN 1 AND 10000 THEN
    RAISE EXCEPTION 'a batch of 1-10000 is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO r FROM iam_v2.data_retention_get(p_tenant, p_site);
  PERFORM set_config('iam_v2.anonymise', 'retention', true);

  -- PMS STAYS: the stay is over (checked out, cancelled or no-show) for longer than the retention, nothing it
  -- granted is still live, and no post-stay access is valid inside the retention.
  v_cut := now() - make_interval(days => r.pms_stay_days); v_n := 0;
  FOR v_id IN
    SELECT s.id FROM iam_v2.stays s
      LEFT JOIN LATERAL (SELECT max(e.received_at) AS last_event FROM iam_v2.stay_events e WHERE e.stay_id = s.id) le ON true
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site
       AND (s.status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
            -- A stay the PMS never closed (0137, audit B2): its departure is older than the retention.
            OR (s.status IN ('RESERVED', 'IN_HOUSE') AND s.departure IS NOT NULL
                AND (s.departure + 1)::timestamptz < v_cut)
            -- ... or with no departure at all (0143): the PMS's latest full room list no longer has it, and it has
            -- had no PMS message for longer than the retention.
            OR (s.status IN ('RESERVED', 'IN_HOUSE') AND s.departure IS NULL
                AND COALESCE(le.last_event, (s.arrival + 1)::timestamptz) < v_cut
                AND iam_v2.stay_absent_from_published_roster(s.id)))
       AND s.external_reservation_id NOT LIKE 'anon-%'
       AND COALESCE(s.effective_checkout_at, (s.departure + 1)::timestamptz, (s.arrival + 1)::timestamptz, le.last_event) < v_cut
       AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e WHERE e.stay_id = s.id AND e.status <> 'TERMINATED')
       AND NOT EXISTS (SELECT 1 FROM iam_v2.post_stay_profiles p
                        WHERE p.origin_stay_id = s.id AND p.status = 'ACTIVE' AND p.valid_until >= v_cut)
     ORDER BY s.id LIMIT p_batch
  LOOP
    PERFORM iam_v2.anonymise_stay(v_id); v_n := v_n + 1;
  END LOOP;
  -- Processed PMS events that never resolved to a stay carry the same guest data.
  PERFORM iam_v2.begin_controlled_operation('stay');
  UPDATE iam_v2.stay_events e
     SET payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE e.id IN (SELECT x.id FROM iam_v2.stay_events x
                   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.stay_id IS NULL
                     AND x.processing_status <> 'PENDING' AND x.received_at < v_cut
                     AND x.payload IS DISTINCT FROM '{"anonymised": true}'::jsonb
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  -- Events still waiting after the stay retention are given up (0139, D-B2). Rows the stay engine holds are
  -- skipped, never waited for; it finishes them or a later run gives them up.
  v_n := v_n + iam_v2.give_up_waiting_stay_events(p_tenant, p_site, v_cut, NULL, p_batch);
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'pms_stays', r.pms_stay_days, v_cut, v_n);
    o_category := 'pms_stays'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;

  -- CLIENT ACTIVITY: a device unseen for longer than the retention with nothing live on it, and the address
  -- columns of every session that ended before the cutoff.
  v_cut := now() - make_interval(days => r.client_activity_days); v_n := 0;
  FOR v_id IN
    SELECT d.id FROM iam_v2.devices d
     WHERE d.tenant_id = p_tenant AND d.site_id = p_site
       AND d.mac::text NOT LIKE '03:%'
       AND COALESCE(d.last_seen, d.first_seen) < v_cut
       AND NOT EXISTS (SELECT 1 FROM iam_v2.sessions se WHERE se.device_id = d.id AND (se.ended IS NULL OR se.ended >= v_cut))
       AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlement_devices ed WHERE ed.device_id = d.id AND ed.status = 'AUTHORIZED')
       AND NOT EXISTS (SELECT 1 FROM iam_v2.principal_device_credentials c
                        WHERE c.device_id = d.id AND c.revoked_at IS NULL AND c.expires_at > now())
     ORDER BY d.id LIMIT p_batch
  LOOP
    PERFORM iam_v2.anonymise_device(v_id); v_n := v_n + 1;
  END LOOP;
  UPDATE iam_v2.sessions se SET ip = NULL, mac = NULL
   WHERE se.id IN (SELECT x.id FROM iam_v2.sessions x
                    WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.ended < v_cut
                      AND (x.ip IS NOT NULL OR x.mac IS NOT NULL)
                    LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'client_activity', r.client_activity_days, v_cut, v_n);
    o_category := 'client_activity'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;

  -- INACTIVE IDENTITIES: no sign-in, verification, access or remembered-device use inside the retention, and
  -- nothing granted to the identity still live.
  v_cut := now() - make_interval(days => r.inactive_identity_days); v_n := 0;
  FOR v_id IN
    SELECT gp.id FROM iam_v2.guest_principals gp
     WHERE gp.tenant_id = p_tenant
       AND (gp.display_name IS NOT NULL
            OR EXISTS (SELECT 1 FROM iam_v2.guest_principal_identities i
                        WHERE i.guest_principal_id = gp.id AND i.factor_value_norm NOT LIKE 'anon-%'))
       AND NOT EXISTS (SELECT 1 FROM iam_v2.guest_principal_identities i
                        WHERE i.guest_principal_id = gp.id AND i.verified_at >= v_cut)
       AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e
                        WHERE e.guest_principal_id = gp.id
                          AND (e.status <> 'TERMINATED' OR COALESCE(e.terminated_at, 'infinity') >= v_cut))
       AND NOT EXISTS (SELECT 1 FROM iam_v2.auth_contexts ac
                        WHERE ac.guest_principal_id = gp.id AND COALESCE(ac.consumed_at, ac.expires_at) >= v_cut)
       AND NOT EXISTS (SELECT 1 FROM iam_v2.principal_device_credentials c
                        WHERE c.guest_principal_id = gp.id
                          AND (GREATEST(c.created_at, c.last_used_at) >= v_cut
                               OR (c.revoked_at IS NULL AND c.expires_at > now())))
     ORDER BY gp.id LIMIT p_batch
  LOOP
    PERFORM iam_v2.anonymise_guest_principal(v_id); v_n := v_n + 1;
  END LOOP;
  FOR v_id IN
    SELECT a.id FROM iam_v2.guest_access_accounts a
     WHERE a.tenant_id = p_tenant AND a.site_id = p_site
       AND a.username NOT LIKE 'anon-%@anonymised.removed-%'
       AND GREATEST(COALESCE(a.created_at, '-infinity'), COALESCE(a.valid_until, '-infinity'),
                    COALESCE(a.grace_until, '-infinity'), COALESCE(a.email_verified_at, '-infinity'),
                    COALESCE(a.last_login_at, '-infinity'), COALESCE(a.locked_until, '-infinity')) < v_cut
       AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e
                        WHERE e.guest_account_id = a.id
                          AND (e.status <> 'TERMINATED' OR COALESCE(e.terminated_at, 'infinity') >= v_cut))
       AND NOT EXISTS (SELECT 1 FROM iam_v2.auth_contexts ac
                        WHERE ac.guest_account_id = a.id AND COALESCE(ac.consumed_at, ac.expires_at) >= v_cut)
     ORDER BY a.id LIMIT p_batch
  LOOP
    PERFORM iam_v2.anonymise_guest_account(v_id); v_n := v_n + 1;
  END LOOP;
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'inactive_identities', r.inactive_identity_days, v_cut, v_n);
    o_category := 'inactive_identities'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;

  -- EXPIRED SINGLE-USE RECORDS (iam_v2 part; public tables are in run_data_retention_public).
  v_cut := now() - make_interval(days => r.expired_record_days); v_n := 0;
  UPDATE iam_v2.verification_access_grants g
     SET device_mac = iam_v2.anonymised_mac(), ip = '0.0.0.0'::inet,
         destination_digest = encode(public.gen_random_bytes(32), 'hex')
   WHERE g.id IN (SELECT x.id FROM iam_v2.verification_access_grants x
                   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.expires_at < v_cut
                     AND x.device_mac::text NOT LIKE '03:%'
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  UPDATE iam_v2.guest_signin_restrictions g
     SET device_mac = iam_v2.anonymised_mac(), last_submitted_room = NULL
   WHERE g.id IN (SELECT x.id FROM iam_v2.guest_signin_restrictions x
                   WHERE x.tenant_id = p_tenant AND x.site_id = p_site
                     AND GREATEST(x.updated_at, COALESCE(x.expires_at, '-infinity'), COALESCE(x.released_at, '-infinity')) < v_cut
                     AND x.device_mac::text NOT LIKE '03:%'
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  UPDATE iam_v2.account_email_tokens t SET last_send_error = NULL
   WHERE t.id IN (SELECT x.id FROM iam_v2.account_email_tokens x
                   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.expires_at < v_cut
                     AND x.last_send_error IS NOT NULL
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'expired_records', r.expired_record_days, v_cut, v_n);
    o_category := 'expired_records'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;

  PERFORM set_config('iam_v2.anonymise', '', true);
END $function$;

-- restored from 0138_disabled_staff_accounts_are_anonymised_after_a_set_period.up.sql
CREATE OR REPLACE FUNCTION public.operators_track_disabled() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.anonymised_at IS NOT NULL AND NEW.status <> 'disabled' THEN
    RAISE EXCEPTION 'an anonymised staff account cannot be enabled again' USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.status = 'disabled' AND OLD.status IS DISTINCT FROM 'disabled' THEN
    NEW.disabled_at := COALESCE(NEW.disabled_at, now());
  ELSIF NEW.status <> 'disabled' THEN
    NEW.disabled_at := NULL;
  END IF;
  RETURN NEW;
END $$;

-- restored from 0138_disabled_staff_accounts_are_anonymised_after_a_set_period.up.sql
CREATE OR REPLACE FUNCTION public.run_staff_retention(p_tenant uuid, p_site uuid)
  RETURNS jsonb
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_days integer; v_audit_days integer; ids uuid[]; n_ops integer; n_audit integer; n_net integer; n_https integer;
BEGIN
  SELECT r.disabled_account_days INTO v_days FROM public.operator_account_retention_get() r;
  SELECT r.audit_log_days INTO v_audit_days FROM iam_v2.data_retention_get(p_tenant, p_site) r;

  SELECT COALESCE(array_agg(o.id), '{}') INTO ids FROM (
    SELECT o.id FROM public.operators o
     WHERE o.status = 'disabled' AND o.anonymised_at IS NULL
       AND o.disabled_at < now() - make_interval(days => v_days)
     ORDER BY o.disabled_at LIMIT 500 FOR UPDATE) o;
  UPDATE public.operators o
     SET email = 'removed-' || left(replace(o.id::text, '-', ''), 12) || '@staff.invalid',
         display_name = NULL, password_hash = NULL, oidc_sub = NULL, anonymised_at = now(), updated_at = now()
   WHERE o.id = ANY (ids);
  GET DIAGNOSTICS n_ops = ROW_COUNT;
  DELETE FROM public.operator_mfa_recovery_codes WHERE operator_id = ANY (ids);
  DELETE FROM public.operator_mfa WHERE operator_id = ANY (ids);
  UPDATE public.audit_log l SET payload = l.payload - 'email' - 'display_name' - 'username'
   WHERE l.target_type = 'operator' AND l.target_id = ANY (ids::text[])
     AND (l.payload ? 'email' OR l.payload ? 'display_name' OR l.payload ? 'username');
  GET DIAGNOSTICS n_audit = ROW_COUNT;

  UPDATE public.system_network_audit x SET source_ip = NULL
   WHERE x.source_ip IS NOT NULL AND x.created_at < now() - make_interval(days => v_audit_days);
  GET DIAGNOSTICS n_net = ROW_COUNT;
  PERFORM set_config('public.staff_retention', 'on', true);
  UPDATE public.portal_https_settings_changes x SET source_ip = NULL
   WHERE x.source_ip IS NOT NULL AND x.changed_at < now() - make_interval(days => v_audit_days);
  GET DIAGNOSTICS n_https = ROW_COUNT;
  PERFORM set_config('public.staff_retention', '', true);

  RETURN jsonb_build_object('staff_accounts_anonymised', n_ops, 'audit_entries', n_audit,
                            'staff_addresses_cleared', n_net + n_https);
END $fn$;

-- restored from 0129_backups_are_encrypted_to_the_hotels_own_keys.up.sql
CREATE OR REPLACE FUNCTION public.backup_key_recipients(p_tenant uuid DEFAULT NULL, p_site uuid DEFAULT NULL)
    RETURNS SETOF text
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
  IF p_tenant IS NULL OR p_site IS NULL THEN
    IF (SELECT count(DISTINCT (tenant_id, site_id)) FROM backup_key_holders WHERE status = 'ACTIVE') > 1 THEN
      RAISE EXCEPTION 'backup key holders of more than one site are active; name the site'
        USING ERRCODE = 'cardinality_violation';
    END IF;
    RETURN QUERY SELECT public_key FROM backup_key_holders WHERE status = 'ACTIVE' ORDER BY confirmed_at;
  ELSE
    RETURN QUERY SELECT public_key FROM backup_key_holders
                  WHERE status = 'ACTIVE' AND tenant_id = p_tenant AND site_id = p_site ORDER BY confirmed_at;
  END IF;
END $$;

DROP TRIGGER IF EXISTS operators_track_disabled ON public.operators;
CREATE TRIGGER operators_track_disabled BEFORE UPDATE OF status ON public.operators
  FOR EACH ROW EXECUTE FUNCTION public.operators_track_disabled();

ALTER TABLE iam_v2.pms_case_resolutions DROP CONSTRAINT IF EXISTS pms_case_resolutions_evidence_kind_check;
ALTER TABLE iam_v2.pms_case_resolutions ADD CONSTRAINT pms_case_resolutions_evidence_kind_check CHECK (evidence_kind = ANY (ARRAY[
    'PUBLISHED_ROSTER_GENERATION','ADMISSION_KIND','STAY_STATE','NONE','OPERATOR_DECISION','EXPIRY_SETTING'])) NOT VALID;

DROP FUNCTION IF EXISTS public.guest_data_reason_check(uuid, uuid, text, uuid, text);
DROP FUNCTION IF EXISTS iam_v2.redact_guest_text(jsonb, text[], boolean);
DROP FUNCTION IF EXISTS iam_v2.text_names_any(text, text[]);
DROP FUNCTION IF EXISTS iam_v2.guest_data_identifiers(uuid, uuid, text, uuid);
DROP FUNCTION IF EXISTS iam_v2.same_guest_rows(uuid[], uuid[]);
DROP FUNCTION IF EXISTS iam_v2.same_guest(text, text, text, text, text, text);
DROP FUNCTION IF EXISTS iam_v2.site_today(uuid);
DROP FUNCTION IF EXISTS iam_v2.site_day_start(uuid, date);
DROP TABLE IF EXISTS iam_v2.retention_floor;
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'REVOKE SELECT (id, tenant_id, timezone) ON public.sites FROM iam_v2_owner';
  END IF;
END $g$;

COMMIT;
