BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. An anonymised value is recognised by its exact shape, not by a prefix.
-- ---------------------------------------------------------------------------------------------------------
-- Every anonymised reservation number, identity value and event identity is 'anon-' plus 32-64 random hex digits
-- (iam_v2.anonymised_token, give_up_waiting_stay_events). The earlier tests were `LIKE 'anon-%'`, so a guest whose
-- email began with "anon-" was taken for already anonymised and never was.
CREATE OR REPLACE FUNCTION iam_v2.is_anon_token(p text) RETURNS boolean
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    AS $$ SELECT p ~ '^anon-[0-9a-f]{32,64}$' $$;

CREATE OR REPLACE FUNCTION iam_v2.anonymise_guest_account(p_account uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  UPDATE iam_v2.account_email_tokens
     SET last_send_error = NULL, token_hmac = CASE WHEN token_hmac IS NOT NULL THEN public.gen_random_bytes(32) END,
         superseded_at = CASE WHEN used_at IS NULL AND superseded_at IS NULL THEN now() ELSE superseded_at END
   WHERE account_id = p_account;
  UPDATE iam_v2.guest_access_accounts
     SET username = iam_v2.anonymised_token() || '@anonymised.removed-' || left(replace(id::text, '-', ''), 8) || '.invalid',
         display_name = NULL, notes = NULL, created_by_device_mac = NULL, enabled = false,
         -- The link to the reservation, the password hash, the sign-in history and the dates of the account go with the name:
         -- the reservation the account was made for would otherwise name the person a join away.
         stay_id = NULL, password_hash = '$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', failed_attempts = 0, locked_until = NULL, last_login_at = NULL,
         login_count = 0, created_at = date_trunc('month', created_at), email_verified_at = NULL, verification_deadline = NULL,
         verification_reminder_sent_at = NULL, grace_until = NULL, valid_from = NULL, valid_until = NULL
   WHERE id = p_account AND username NOT LIKE 'anon-%@anonymised.removed-%';
  -- What staff typed about this account's access.
  PERFORM set_config('iam_v2.scrub', 'on', true);
  UPDATE iam_v2.entitlement_adjustments SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_account_id = p_account) AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.device_slot_actions SET reason = 'Removed after the staff-notes period', detail = CASE WHEN detail IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_account_id = p_account)
     AND (reason <> 'Removed after the staff-notes period' OR (detail IS NOT NULL AND detail <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.guest_device_actions SET detail = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_account_id = p_account)
     AND detail IS NOT NULL AND detail <> 'Removed after the staff-notes period';
  PERFORM set_config('iam_v2.scrub', '', true);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.anonymise_guest_principal(p_principal uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  UPDATE iam_v2.guest_principals SET display_name = NULL WHERE id = p_principal AND display_name IS NOT NULL;
  UPDATE iam_v2.guest_principal_identities SET factor_value_norm = iam_v2.anonymised_token(), attrs = '{}'::jsonb
   WHERE guest_principal_id = p_principal AND NOT iam_v2.is_anon_token(factor_value_norm);
  UPDATE iam_v2.guest_principal_identities SET verified_at = date_trunc('month', verified_at)
   WHERE guest_principal_id = p_principal AND verified_at IS NOT NULL AND verified_at <> date_trunc('month', verified_at);
  -- A remembered-device secret is revoked AND its value replaced: a revoked row must not keep a usable shape of it.
  UPDATE iam_v2.principal_device_credentials
     SET revoked_at = COALESCE(revoked_at, now()), secret_hmac = public.gen_random_bytes(32), last_used_at = NULL
   WHERE guest_principal_id = p_principal AND (revoked_at IS NULL OR last_used_at IS NOT NULL);
  PERFORM set_config('iam_v2.scrub', 'on', true);
  UPDATE iam_v2.entitlement_adjustments SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_principal_id = p_principal) AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.device_slot_actions SET reason = 'Removed after the staff-notes period', detail = CASE WHEN detail IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_principal_id = p_principal)
     AND (reason <> 'Removed after the staff-notes period' OR (detail IS NOT NULL AND detail <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.guest_device_actions SET detail = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.guest_principal_id = p_principal)
     AND detail IS NOT NULL AND detail <> 'Removed after the staff-notes period';
  PERFORM set_config('iam_v2.scrub', '', true);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.anonymise_stay(p_stay uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  PERFORM iam_v2.begin_controlled_operation('stay');
  UPDATE iam_v2.stays
     SET external_reservation_id = iam_v2.anonymised_token(), external_stay_identity = iam_v2.anonymised_token(),
         normalized_room_number = NULL, vip = NULL, travel_agent = NULL, rate_plan = NULL, room_type = NULL,
         -- The dates stay as months and the times as days: enough for a report, not enough to find the booking in the PMS.
         arrival = date_trunc('month', arrival)::date, departure = date_trunc('month', departure)::date,
         -- (only on a finished stay: an open one is still governed by the occupancy-evidence rules)
         effective_checkout_at = CASE WHEN status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
                                      THEN date_trunc('day', effective_checkout_at) ELSE effective_checkout_at END,
         occupancy_evidence_at = CASE WHEN status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
                                      THEN date_trunc('day', occupancy_evidence_at) ELSE occupancy_evidence_at END,
         occupancy_ingested_at = CASE WHEN status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
                                      THEN date_trunc('day', occupancy_ingested_at) ELSE occupancy_ingested_at END
   WHERE id = p_stay AND NOT iam_v2.is_anon_token(external_reservation_id);
  -- What staff typed about the stay, and the post-stay secret.
  PERFORM set_config('iam_v2.scrub', 'on', true);
  UPDATE iam_v2.stay_posting_blocks SET note = CASE WHEN note IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END,
         cleared_reason = CASE WHEN cleared_reason IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE stay_id = p_stay AND ((note IS NOT NULL AND note <> 'Removed after the staff-notes period') OR (cleared_reason IS NOT NULL AND cleared_reason <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.pms_case_resolutions SET note = 'Removed after the staff-notes period', evidence_ref = CASE WHEN evidence_ref IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE stay_id = p_stay AND ((note IS NOT NULL AND note <> 'Removed after the staff-notes period') OR (evidence_ref IS NOT NULL AND evidence_ref <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.entitlement_transfers SET reason = 'Removed after the staff-notes period'
   WHERE (from_stay_id = p_stay OR to_stay_id = p_stay) AND reason IS NOT NULL AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.entitlement_adjustments SET reason = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.stay_id = p_stay) AND reason <> 'Removed after the staff-notes period';
  UPDATE iam_v2.device_slot_actions SET reason = 'Removed after the staff-notes period', detail = CASE WHEN detail IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.stay_id = p_stay)
     AND (reason <> 'Removed after the staff-notes period' OR (detail IS NOT NULL AND detail <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.guest_device_actions SET detail = 'Removed after the staff-notes period'
   WHERE entitlement_id IN (SELECT e.id FROM iam_v2.entitlements e WHERE e.stay_id = p_stay)
     AND detail IS NOT NULL AND detail <> 'Removed after the staff-notes period';
  UPDATE iam_v2.post_stay_profiles
     SET pin_hash = '$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', revoke_reason = CASE WHEN revoke_reason IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE origin_stay_id = p_stay AND (pin_hash <> '$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' OR (revoke_reason IS NOT NULL AND revoke_reason <> 'Removed after the staff-notes period'));
  PERFORM set_config('iam_v2.scrub', '', true);
  UPDATE iam_v2.stay_guests
     SET external_guest_id = NULL, first_name_norm = NULL, last_name_norm = NULL, display_name = NULL,
         pin_hash = NULL
   WHERE stay_id = p_stay
     AND num_nonnulls(external_guest_id, first_name_norm, last_name_norm, display_name, pin_hash) > 0;
  UPDATE iam_v2.stay_events
     SET payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE stay_id = p_stay AND processing_status <> 'PENDING'
     AND payload IS DISTINCT FROM '{"anonymised": true}'::jsonb;
  -- The copy of the names and room migration 0066 kept as rollback evidence goes with the stay
  -- (GDPR audit 2026-10-19, A6). Not every appliance has the table, so it is looked up first.
  IF to_regclass('iam_v2.backfill_0066_identity_text') IS NOT NULL THEN
    EXECUTE $q$
      UPDATE iam_v2.backfill_0066_identity_text x
         SET prior_first_name = NULL, prior_last_name = NULL, prior_room = NULL
       WHERE (x.row_id = $1 OR x.row_id IN (SELECT sg.id FROM iam_v2.stay_guests sg WHERE sg.stay_id = $1))
         AND num_nonnulls(x.prior_first_name, x.prior_last_name, x.prior_room) > 0
    $q$ USING p_stay;
  END IF;
END $$;

CREATE OR REPLACE FUNCTION iam_v2.give_up_waiting_stay_events(p_tenant uuid, p_site uuid, p_before timestamptz,
                                                             p_only uuid[], p_limit integer)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE n integer;
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  PERFORM iam_v2.begin_controlled_operation('stay');
  UPDATE iam_v2.stay_events e
     SET processing_status = 'FAILED', review_code = 'RETENTION_GIVEN_UP', processed_at = now(),
         payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE e.id IN (SELECT x.id FROM iam_v2.stay_events x
                   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.processing_status = 'PENDING'
                     AND x.received_at < p_before AND (p_only IS NULL OR x.id = ANY (p_only))
                   ORDER BY x.received_at LIMIT p_limit
                   FOR UPDATE SKIP LOCKED);
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END $$;

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
    -- A device identifies nobody (0145): only a person's own access (account or identity) is found by it,
    -- never a room's, a voucher's or an anonymous one.
    v_ents := ARRAY(SELECT en.id FROM entitlements en WHERE en.id = ANY (v_ents)
                     AND (en.guest_account_id IS NOT NULL OR en.guest_principal_id IS NOT NULL));
  END IF;

  RETURN QUERY
  SELECT r.* FROM (
    SELECT 'account'::text, x.id, x.username, x.display_name,
           CASE WHEN NOT x.enabled AND x.username LIKE '%.removed-%.invalid' THEN 'removed'
                WHEN x.enabled THEN 'enabled' ELSE 'disabled' END,
           GREATEST(x.created_at, x.last_login_at)
      FROM guest_access_accounts x
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND NOT iam_v2.is_anon_token(x.username)
       AND (lower(x.username) LIKE pat OR lower(COALESCE(x.display_name, '')) LIKE pat
            OR x.created_by_device_mac = v_mac
            OR EXISTS (SELECT 1 FROM entitlements en WHERE en.id = ANY (v_ents) AND en.guest_account_id = x.id))
    UNION ALL
    SELECT 'principal'::text, g.id,
           (SELECT string_agg(i.factor_value_norm, ', ' ORDER BY i.verified_at DESC NULLS LAST)
              FROM guest_principal_identities i
             WHERE i.guest_principal_id = g.id AND NOT iam_v2.is_anon_token(i.factor_value_norm)),
           g.display_name,
           (SELECT string_agg(DISTINCT i.factor_type, ', ') FROM guest_principal_identities i
             WHERE i.guest_principal_id = g.id AND NOT iam_v2.is_anon_token(i.factor_value_norm)),
           (SELECT max(i.verified_at) FROM guest_principal_identities i WHERE i.guest_principal_id = g.id)
      FROM guest_principals g
     WHERE g.tenant_id = p_tenant
       -- Not another site's guest (0144): an identity also used at another site is not offered here, as the
       -- scope refuses it.
       AND NOT EXISTS (SELECT 1 FROM auth_contexts ac WHERE ac.guest_principal_id = g.id AND ac.site_id <> p_site)
       AND NOT EXISTS (SELECT 1 FROM entitlements en WHERE en.guest_principal_id = g.id AND en.site_id <> p_site)
       AND (lower(COALESCE(g.display_name, '')) LIKE pat
            OR EXISTS (SELECT 1 FROM guest_principal_identities i
                        WHERE i.guest_principal_id = g.id AND NOT iam_v2.is_anon_token(i.factor_value_norm)
                          AND lower(i.factor_value_norm) LIKE pat)
            OR EXISTS (SELECT 1 FROM principal_device_credentials c JOIN devices dv ON dv.id = c.device_id
                        WHERE c.guest_principal_id = g.id AND dv.mac = v_mac AND dv.site_id = p_site)
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
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND NOT iam_v2.is_anon_token(x.external_reservation_id)
       AND (lower(COALESCE(x.normalized_room_number, '')) = q OR lower(x.external_reservation_id) = q
            OR EXISTS (SELECT 1 FROM stay_guests sg
                        WHERE sg.stay_id = x.id
                          AND (lower(COALESCE(sg.display_name, '')) LIKE pat OR lower(COALESCE(sg.last_name_norm, '')) LIKE pat
                               OR lower(COALESCE(sg.first_name_norm, '')) LIKE pat)))
  ) r ORDER BY 6 DESC NULLS LAST LIMIT 51;
END $function$;

CREATE OR REPLACE FUNCTION iam_v2.guest_data_identifiers(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
    RETURNS TABLE(o_kind text, o_value text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE ks text[]; ids uuid[]; vs text[]; s uuid[]; gr uuid[]; a uuid[];
BEGIN
  SELECT COALESCE(array_agg(sc.o_kind), '{}'), COALESCE(array_agg(sc.o_id), '{}'), COALESCE(array_agg(sc.o_value), '{}')
    INTO ks, ids, vs FROM guest_data_scope(p_tenant, p_site, p_kind, p_id) sc;
  SELECT COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'stay'), '{}'), COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'stay_guest'), '{}'),
         COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'account'), '{}')
    INTO s, gr, a FROM unnest(ks, ids) z(k, i);
  RETURN QUERY
    SELECT DISTINCT t.k, lower(btrim(t.v)) FROM (
      SELECT 'strong'::text AS k, g.v FROM unnest(ks, vs) g(k, v) WHERE g.k IN ('account', 'identity', 'device', 'stay')
      UNION ALL SELECT 'name', g.v FROM unnest(ks, vs) g(k, v) WHERE g.k IN ('principal', 'stay_guest')
      UNION ALL SELECT 'name', sg.display_name FROM stay_guests sg WHERE sg.stay_id = ANY (s) OR sg.id = ANY (gr)
      UNION ALL SELECT 'name', concat_ws(' ', sg.first_name_norm, sg.last_name_norm)
                  FROM stay_guests sg WHERE (sg.stay_id = ANY (s) OR sg.id = ANY (gr))
                   AND sg.first_name_norm IS NOT NULL AND sg.last_name_norm IS NOT NULL
      UNION ALL SELECT 'name', x.display_name FROM guest_access_accounts x WHERE x.id = ANY (a)
      UNION ALL SELECT 'weak', x.normalized_room_number FROM stays x WHERE x.id = ANY (s)
      UNION ALL SELECT 'weak', sg.first_name_norm FROM stay_guests sg WHERE sg.stay_id = ANY (s) OR sg.id = ANY (gr)
      UNION ALL SELECT 'weak', sg.last_name_norm FROM stay_guests sg WHERE sg.stay_id = ANY (s) OR sg.id = ANY (gr)
    ) t
    WHERE t.v IS NOT NULL AND length(btrim(t.v)) >= 2 AND NOT iam_v2.is_anon_token(t.v);
END $$;

CREATE OR REPLACE FUNCTION iam_v2.guest_data_scope(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS TABLE(o_kind text, o_id uuid, o_value text)
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[] := '{}'; p uuid[] := '{}'; s uuid[] := '{}'; ss uuid[] := '{}'; sso uuid[] := '{}'; gr uuid[] := '{}';
        anchor uuid[] := '{}'; own uuid[]; own_ind uuid[]; ind_a uuid[]; room uuid[]; cand uuid[]; d uuid[]; dshared uuid[];
        ch uuid[]; v_origin uuid; v_seen uuid[]; v_front uuid[]; v_next uuid[]; v_depth integer := 0; v_st uuid;
        v_rows uuid[]; v_walked boolean := false; v_anchor0 uuid[];
BEGIN
  IF p_kind = 'stay_guest' THEN
    SELECT ARRAY[sg.id], ARRAY[sg.stay_id] INTO anchor, v_front
      FROM stay_guests sg WHERE sg.id = p_id AND sg.tenant_id = p_tenant AND sg.site_id = p_site;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'guest record not found' USING ERRCODE = 'no_data_found';
    END IF;
    -- THE GUEST'S OWN STAYS: from their row, through stay links, continuing only from a stay they are on.
    v_seen := v_front;
    WHILE cardinality(v_front) > 0 AND v_depth < 20 LOOP
      SELECT COALESCE(array_agg(DISTINCT CASE WHEN k.from_stay = ANY (v_front) THEN k.to_stay ELSE k.from_stay END), '{}')
        INTO v_next
        FROM stay_links k
       WHERE k.tenant_id = p_tenant AND k.site_id = p_site AND (k.from_stay = ANY (v_front) OR k.to_stay = ANY (v_front));
      v_next := ARRAY(SELECT z.x FROM unnest(v_next) z(x) WHERE NOT z.x = ANY (v_seen));
      v_seen := v_seen || v_next;
      v_front := '{}';
      FOREACH v_st IN ARRAY v_next LOOP
        IF cardinality(same_guest_rows(anchor, ARRAY[v_st])) > 0 THEN
          v_front := v_front || v_st;
        END IF;
      END LOOP;
      v_depth := v_depth + 1;
    END LOOP;
    -- Stays the guest is on: one with a single named guest is the reservation's own, whole (its PMS data -- never
    -- its room access, which more people than the named guest use); on a shared one, only their rows.
    FOREACH v_st IN ARRAY v_seen LOOP
      v_rows := same_guest_rows(anchor, ARRAY[v_st]);
      IF cardinality(v_rows) = 0 THEN
        CONTINUE;
      ELSIF stay_named_guests(v_st) <= 1 THEN
        s := s || v_st;
      ELSE
        ss := ss || v_st; gr := gr || v_rows;
      END IF;
    END LOOP;
    v_walked := true;
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
  IF cardinality(a) + cardinality(p) + cardinality(s) + cardinality(ss) = 0 THEN
    RAISE EXCEPTION 'guest record not found' USING ERRCODE = 'no_data_found';
  END IF;

  -- A client account's link to a stay is a reference, not ownership of the reservation (0145): the stay is
  -- reached only by a request about the stay.
  IF NOT v_walked AND cardinality(s) > 0 THEN
    -- A ROOM MOVE OR A TRANSFER IS THE SAME STAY CONTINUED: followed both ways, at most 20 links, and only
    -- onward from a stay with one guest named. A shared stay is reached, never passed through.
    v_front := ARRAY(SELECT z.x FROM unnest(s) z(x) WHERE stay_named_guests(z.x) <= 1);
    v_seen := s;
    -- A LINK IS FOLLOWED ONLY TO THE SAME GUEST. A stay linked to another person's (a transfer, a post-stay chain) is that
    -- person's reservation: it is not exported and not anonymised for this guest.
    v_anchor0 := ARRAY(SELECT sg.id FROM stay_guests sg WHERE sg.stay_id = ANY (s)
                        AND num_nonnulls(sg.first_name_norm, sg.last_name_norm, sg.display_name, sg.external_guest_id) > 0);
    WHILE cardinality(v_front) > 0 AND v_depth < 20 LOOP
      SELECT COALESCE(array_agg(DISTINCT CASE WHEN k.from_stay = ANY (v_front) THEN k.to_stay ELSE k.from_stay END), '{}')
        INTO v_next
        FROM stay_links k
       WHERE k.tenant_id = p_tenant AND k.site_id = p_site AND (k.from_stay = ANY (v_front) OR k.to_stay = ANY (v_front));
      v_next := ARRAY(SELECT z.x FROM unnest(v_next) z(x)
                       WHERE NOT z.x = ANY (v_seen)
                         AND (stay_named_guests(z.x) > 1 OR cardinality(same_guest_rows(v_anchor0, ARRAY[z.x])) > 0));
      v_seen := v_seen || v_next;
      v_front := ARRAY(SELECT z.x FROM unnest(v_next) z(x) WHERE stay_named_guests(z.x) <= 1);
      v_depth := v_depth + 1;
    END LOOP;
    s := v_seen;
    -- A shared stay other than the one asked for is kept whole: neither it nor its shared access is in scope.
    ss := ARRAY(SELECT z.x FROM unnest(s) z(x) WHERE stay_named_guests(z.x) > 1 AND z.x IS DISTINCT FROM v_origin);
    s := ARRAY(SELECT z.x FROM unnest(s) z(x) WHERE NOT z.x = ANY (ss));
  END IF;

  -- NO OTHER SITE'S GUEST. An identity is the tenant's; if it was used at another site, this site's staff cannot
  -- export or erase it.
  IF EXISTS (SELECT 1 FROM entitlements x WHERE x.guest_principal_id = ANY (p) AND x.site_id <> p_site)
     OR EXISTS (SELECT 1 FROM auth_contexts x WHERE x.guest_principal_id = ANY (p) AND x.site_id <> p_site) THEN
    RAISE EXCEPTION 'guest_data_live' USING ERRCODE = 'object_in_use', DETAIL = 'other_site';
  END IF;

  -- Asked for by stay: the guest of that stay is the guest who asked, so on a shared stay it continues as, their
  -- own rows are in scope (D-R4: PMS guest number on the same interface, or a unique full name).
  IF p_kind = 'stay' AND cardinality(ss) > 0 THEN
    anchor := ARRAY(SELECT sg.id FROM stay_guests sg WHERE sg.stay_id = ANY (s)
                     AND num_nonnulls(sg.first_name_norm, sg.last_name_norm, sg.display_name, sg.external_guest_id) > 0);
    gr := ARRAY(SELECT z.x FROM unnest(same_guest_rows(anchor, ss)) z(x) WHERE NOT z.x = ANY (anchor));
  END IF;
  sso := ARRAY(SELECT z.x FROM unnest(ss) z(x) WHERE NOT EXISTS (SELECT 1 FROM stay_guests sg WHERE sg.stay_id = z.x AND sg.id = ANY (gr)));
  ss := ARRAY(SELECT z.x FROM unnest(ss) z(x) WHERE NOT z.x = ANY (sso));

  -- THE PERSON'S OWN ACCESS: the entitlements whose subject is the account or the identity. The room's access
  -- (subject: a stay) is shared by everyone in the room and is never the person's.
  SELECT COALESCE(array_agg(x.id), '{}') INTO own FROM entitlements x
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND (x.guest_account_id = ANY (a) OR x.guest_principal_id = ANY (p));
  SELECT COALESCE(array_agg(x.id), '{}') INTO ind_a FROM guest_access_accounts x WHERE x.id = ANY (a) AND x.self_registered;
  SELECT COALESCE(array_agg(x.id), '{}') INTO own_ind FROM entitlements x
   WHERE x.id = ANY (own) AND (x.guest_account_id = ANY (ind_a) OR x.guest_principal_id = ANY (p));
  SELECT COALESCE(array_agg(x.id), '{}') INTO room FROM entitlements x
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.stay_id = ANY (s);
  -- THE PERSON'S OWN ONE-TIME CODES: sent to an address they proved (an identity's email or phone, a
  -- self-registered account's email). What those codes led to -- a verification access, a code binding -- is
  -- theirs by the address, whatever device it happened on.
  SELECT COALESCE(array_agg(o.id), '{}') INTO ch FROM public.auth_otps o
   WHERE o.tenant_id = p_tenant AND lower(o.destination) IN (
           SELECT lower(i.factor_value_norm) FROM guest_principal_identities i
            WHERE i.guest_principal_id = ANY (p) AND NOT iam_v2.is_anon_token(i.factor_value_norm)
           UNION SELECT lower(x.username) FROM guest_access_accounts x
            WHERE x.id = ANY (ind_a) AND NOT iam_v2.is_anon_token(x.username));

  -- DEVICES: reached through the person's own access, identity or registration, and theirs only when nothing else
  -- touches them. A staff-issued account is a credential that may be shared, so its devices are never attributed.
  SELECT COALESCE(array_agg(DISTINCT z.dev), '{}') INTO cand FROM (
    SELECT ed.device_id AS dev FROM entitlement_devices ed WHERE ed.entitlement_id = ANY (own)
    UNION SELECT se.device_id FROM sessions se WHERE se.entitlement_id = ANY (own)
    UNION SELECT c.device_id FROM principal_device_credentials c WHERE c.guest_principal_id = ANY (p)
    UNION SELECT ac.device_id FROM auth_contexts ac
           WHERE ac.tenant_id = p_tenant AND ac.site_id = p_site AND (ac.guest_account_id = ANY (a) OR ac.guest_principal_id = ANY (p))
    UNION SELECT dv.id FROM devices dv JOIN guest_access_accounts x ON x.id = ANY (a) AND dv.mac = x.created_by_device_mac
           WHERE dv.tenant_id = p_tenant AND dv.site_id = p_site
  ) z WHERE z.dev IS NOT NULL;
  SELECT COALESCE(array_agg(z.dev), '{}') INTO d FROM unnest(cand) z(dev)
   WHERE device_exclusive_to(z.dev, own_ind, p, ind_a, ch);
  dshared := ARRAY(SELECT z.dev FROM unnest(cand) z(dev) WHERE NOT z.dev = ANY (d));

  RETURN QUERY SELECT 'account'::text, x.id, CASE WHEN NOT iam_v2.is_anon_token(x.username) THEN x.username END
                 FROM guest_access_accounts x WHERE x.id = ANY (a);
  -- A removed account's username still carries the address it was created with.
  RETURN QUERY SELECT 'account'::text, x.id, substring(x.username FROM '^(.+)\.removed-[^.]+\.invalid$')
                 FROM guest_access_accounts x
                WHERE x.id = ANY (a) AND NOT iam_v2.is_anon_token(x.username) AND x.username ~ '\.removed-[^.]+\.invalid$';
  RETURN QUERY SELECT 'principal'::text, x.id, x.display_name FROM guest_principals x WHERE x.id = ANY (p);
  RETURN QUERY SELECT 'identity'::text, i.guest_principal_id, i.factor_value_norm FROM guest_principal_identities i
                WHERE i.guest_principal_id = ANY (p) AND NOT iam_v2.is_anon_token(i.factor_value_norm);
  RETURN QUERY SELECT 'stay'::text, x.id, CASE WHEN NOT iam_v2.is_anon_token(x.external_reservation_id) THEN x.external_reservation_id END
                 FROM stays x WHERE x.id = ANY (s);
  -- The stay's PMS messages, and those that never resolved to a stay but name its reservation ON THE SAME PMS
  -- INTERFACE: a reservation number means something only inside its own interface (0145).
  RETURN QUERY SELECT 'stay_event'::text, ev.id, ev.processing_status FROM stay_events ev
                WHERE ev.tenant_id = p_tenant AND ev.site_id = p_site
                  AND (ev.stay_id = ANY (s)
                       OR (ev.stay_id IS NULL AND EXISTS (
                             SELECT 1 FROM stays st
                              WHERE st.id = ANY (s) AND NOT iam_v2.is_anon_token(st.external_reservation_id)
                                AND st.pms_interface_id = ev.pms_interface_id
                                AND st.external_reservation_id = ev.payload->>'reservation')));
  -- The person's own access. An individual identity's or self-registered account's entitlements are theirs; a
  -- staff-issued account's belong to a credential that may be shared (its usage is counted, its payers and
  -- sign-ins are not attributed).
  RETURN QUERY SELECT 'entitlement'::text, z.x, NULL::text FROM unnest(own_ind) z(x);
  RETURN QUERY SELECT 'entitlement_credential'::text, z.x, NULL::text FROM unnest(own) z(x) WHERE NOT z.x = ANY (own_ind);
  RETURN QUERY SELECT 'challenge'::text, z.x, NULL::text FROM unnest(ch) z(x);
  RETURN QUERY SELECT 'access_shared'::text, z.x, NULL::text FROM unnest(room) z(x);
  RETURN QUERY SELECT 'device'::text, dv.id, CASE WHEN dv.mac::text NOT LIKE '03:%' THEN dv.mac::text END
                 FROM devices dv WHERE dv.id = ANY (d);
  RETURN QUERY SELECT 'device_shared'::text, z.x, NULL::text FROM unnest(dshared) z(x);
  RETURN QUERY SELECT 'stay_shared'::text, z.x, NULL::text FROM unnest(ss) z(x);
  RETURN QUERY SELECT 'stay_shared_other'::text, z.x, NULL::text FROM unnest(sso) z(x);
  -- The guest's own rows on those shared stays, and the shared stays' PMS messages, from which only they are removed.
  IF cardinality(gr) > 0 THEN
    RETURN QUERY SELECT 'stay_guest'::text, sg.id, sg.display_name FROM stay_guests sg WHERE sg.id = ANY (gr);
    RETURN QUERY SELECT 'stay_event'::text, ev.id, ev.processing_status FROM stay_events ev
                  WHERE ev.tenant_id = p_tenant AND ev.site_id = p_site AND ev.stay_id = ANY (ss);
  END IF;
END $function$;

CREATE OR REPLACE FUNCTION iam_v2.run_data_retention(p_tenant uuid, p_site uuid, p_batch integer)
 RETURNS TABLE(o_category text, o_cutoff timestamp with time zone, o_anonymised integer)
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE r record; v_cut timestamptz; v_n integer; v_k integer; v_id uuid; v_floor timestamptz;
BEGIN
  IF p_batch IS NULL OR p_batch NOT BETWEEN 1 AND 10000 THEN
    RAISE EXCEPTION 'a batch of 1-10000 is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO r FROM iam_v2.data_retention_get(p_tenant, p_site);
  SELECT f.since INTO v_floor FROM iam_v2.retention_floor f;
  v_floor := COALESCE(v_floor, now());
  PERFORM set_config('iam_v2.anonymise', 'retention', true);

  -- PMS STAYS: the stay is over (checked out, cancelled or no-show) for longer than the retention, nothing it
  -- granted is still live, and no post-stay access is valid inside the retention. A date is a day in the
  -- hotel's time zone (0144).
  v_cut := now() - make_interval(days => r.pms_stay_days); v_n := 0;
  FOR v_id IN
    SELECT s.id FROM iam_v2.stays s
      LEFT JOIN LATERAL (SELECT max(e.received_at) AS last_event FROM iam_v2.stay_events e WHERE e.stay_id = s.id) le ON true
     WHERE s.tenant_id = p_tenant AND s.site_id = p_site
       AND (s.status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
            -- A stay the PMS never closed (0137, audit B2): its departure is older than the retention.
            OR (s.status IN ('RESERVED', 'IN_HOUSE') AND s.departure IS NOT NULL
                AND iam_v2.site_day_start(p_site, s.departure + 1) < v_cut)
            -- ... or with no departure at all (0143): the PMS's latest full room list no longer has it, and it has
            -- had no PMS message for longer than the retention.
            OR (s.status IN ('RESERVED', 'IN_HOUSE') AND s.departure IS NULL
                AND COALESCE(le.last_event, iam_v2.site_day_start(p_site, s.arrival + 1)) < v_cut
                AND iam_v2.stay_absent_from_published_roster(s.id)))
       AND NOT iam_v2.is_anon_token(s.external_reservation_id)
       AND COALESCE(s.effective_checkout_at, iam_v2.site_day_start(p_site, s.departure + 1),
                    iam_v2.site_day_start(p_site, s.arrival + 1), le.last_event) < v_cut
       AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e WHERE e.stay_id = s.id AND e.status <> 'TERMINATED')
       AND NOT EXISTS (SELECT 1 FROM iam_v2.post_stay_profiles p
                        WHERE p.origin_stay_id = s.id AND p.status = 'ACTIVE' AND p.valid_until >= v_cut)
     ORDER BY s.id LIMIT p_batch
  LOOP
    PERFORM iam_v2.anonymise_stay(v_id); v_n := v_n + 1;
  END LOOP;
  -- Processed PMS events that never resolved to a stay carry the same guest data. One still waiting for a staff
  -- decision is first closed as undecided (0144), as 0123's expiry closes one, so it leaves the review queue
  -- instead of staying in it blank. A message whose reservation is still open in the mirror is left alone.
  INSERT INTO iam_v2.pms_case_resolutions
         (tenant_id, site_id, pms_interface_id, stay_event_id, stay_id, disposition,
          evidence_kind, evidence_ref, resolved_by, note)
  SELECT x.tenant_id, x.site_id, x.pms_interface_id, x.id, NULL, 'EXPIRED_UNDECIDED',
         'DATA_RETENTION', 'stay retention ' || r.pms_stay_days::text || ' days', 'data retention', NULL
    FROM iam_v2.stay_events x
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.stay_id IS NULL
     AND x.processing_status = 'MANUAL_REVIEW' AND x.received_at < v_cut
     AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions c WHERE c.stay_event_id = x.id)
     AND NOT EXISTS (SELECT 1 FROM iam_v2.stays o
                      WHERE o.tenant_id = x.tenant_id AND o.site_id = x.site_id AND o.pms_interface_id = x.pms_interface_id
                        AND o.status IN ('RESERVED', 'IN_HOUSE')
                        AND o.external_reservation_id = btrim(COALESCE(x.payload->>'reservation', '')))
   ORDER BY x.received_at LIMIT p_batch
  ON CONFLICT (stay_event_id) DO NOTHING;
  PERFORM iam_v2.begin_controlled_operation('stay');
  UPDATE iam_v2.stay_events e
     SET payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE e.id IN (SELECT x.id FROM iam_v2.stay_events x
                   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.stay_id IS NULL
                     AND x.processing_status <> 'PENDING' AND x.received_at < v_cut
                     AND x.payload IS DISTINCT FROM '{"anonymised": true}'::jsonb
                     AND (x.processing_status <> 'MANUAL_REVIEW'
                          OR EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions c WHERE c.stay_event_id = x.id))
                     AND NOT EXISTS (SELECT 1 FROM iam_v2.stays o
                                      WHERE o.tenant_id = x.tenant_id AND o.site_id = x.site_id
                                        AND o.pms_interface_id = x.pms_interface_id
                                        AND o.status IN ('RESERVED', 'IN_HOUSE')
                                        AND o.external_reservation_id = btrim(COALESCE(x.payload->>'reservation', '')))
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
  -- columns of every session that ended before the cutoff. A device never seen is as old as the floor (0144).
  v_cut := now() - make_interval(days => r.client_activity_days); v_n := 0;
  FOR v_id IN
    SELECT d.id FROM iam_v2.devices d
     WHERE d.tenant_id = p_tenant AND d.site_id = p_site
       AND d.mac::text NOT LIKE '03:%'
       AND COALESCE(d.last_seen, d.first_seen, v_floor) < v_cut
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
                        WHERE i.guest_principal_id = gp.id AND NOT iam_v2.is_anon_token(i.factor_value_norm)))
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
  -- A client account: nothing about it is newer than the cutoff -- not its creation (the floor when 0104 could
  -- not record it), not its start or end of validity, not a verification, sign-in or lock (0144).
  FOR v_id IN
    SELECT a.id FROM iam_v2.guest_access_accounts a
     WHERE a.tenant_id = p_tenant AND a.site_id = p_site
       AND a.username NOT LIKE 'anon-%@anonymised.removed-%'
       AND GREATEST(COALESCE(a.created_at, v_floor), COALESCE(a.valid_from, '-infinity'),
                    COALESCE(a.valid_until, '-infinity'),
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

-- A device's address also leaves the account registered on it
CREATE OR REPLACE FUNCTION iam_v2.anonymise_device(p_device uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_mac macaddr;
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  -- An account the person registered on this device holds its address too: it goes with it, whichever of the
  -- retention periods came first.
  SELECT dv.mac INTO v_mac FROM iam_v2.devices dv WHERE dv.id = p_device AND dv.mac::text NOT LIKE '03:%';
  IF v_mac IS NOT NULL THEN
    UPDATE iam_v2.guest_access_accounts SET created_by_device_mac = NULL
     WHERE tenant_id = (SELECT tenant_id FROM iam_v2.devices WHERE id = p_device) AND created_by_device_mac = v_mac;
  END IF;
  UPDATE iam_v2.devices SET mac = iam_v2.anonymised_mac(), last_ip = NULL,
         first_seen = date_trunc('day', first_seen), last_seen = date_trunc('day', last_seen)
   WHERE id = p_device AND mac::text NOT LIKE '03:%';
  -- What staff typed or the guest did about this device, and the records keyed by its address that are over.
  PERFORM set_config('iam_v2.scrub', 'on', true);
  UPDATE iam_v2.device_slot_actions SET reason = 'Removed after the staff-notes period', detail = CASE WHEN detail IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
   WHERE (device_id = p_device OR replacement_device_id = p_device) AND (reason <> 'Removed after the staff-notes period' OR (detail IS NOT NULL AND detail <> 'Removed after the staff-notes period'));
  UPDATE iam_v2.guest_device_actions SET detail = 'Removed after the staff-notes period'
   WHERE device_id = p_device AND detail IS NOT NULL AND detail <> 'Removed after the staff-notes period';
  IF v_mac IS NOT NULL THEN
    UPDATE iam_v2.guest_signin_restrictions SET device_mac = iam_v2.anonymised_mac(), last_submitted_room = NULL,
           release_reason = CASE WHEN release_reason IS NULL THEN NULL ELSE 'Removed after the staff-notes period' END
     WHERE device_mac = v_mac AND (released_at IS NOT NULL OR expires_at IS NULL OR expires_at < now());
    UPDATE iam_v2.guest_signin_device_passes x SET device_mac = iam_v2.anonymised_mac(), reason = 'Removed after the staff-notes period'
     WHERE x.device_mac = v_mac AND EXISTS (SELECT 1 FROM iam_v2.guest_signin_network_pauses p WHERE p.id = x.pause_id AND (p.ended_at IS NOT NULL OR p.expires_at < now()));
    UPDATE iam_v2.verification_access_grants SET device_mac = iam_v2.anonymised_mac(), ip = '0.0.0.0'::inet,
           destination_digest = encode(public.gen_random_bytes(32), 'hex')
     WHERE device_mac = v_mac AND ended_at IS NOT NULL;
    UPDATE iam_v2.otp_device_bindings SET device_mac = iam_v2.anonymised_mac() WHERE device_mac = v_mac;
  END IF;
  PERFORM set_config('iam_v2.scrub', '', true);
  UPDATE iam_v2.sessions SET ip = NULL, mac = NULL
   WHERE device_id = p_device AND ended IS NOT NULL AND (ip IS NOT NULL OR mac IS NOT NULL);
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 2. The erasure journal: what was erased, so that a restore can erase it again.
-- ---------------------------------------------------------------------------------------------------------
-- A restore brings back the database as it was. Everything erased or anonymised since is back, with its names.
-- The journal records WHAT was erased -- the kind and the id of the record, or of each record an assisted request
-- anonymised -- and nothing about WHO it was: a random uuid of a record that is anonymised identifies nobody. scd
-- copies it to a file next to the backups, outside the database, and replays it after any restore
-- (data-plane/cmd/scd/erasure_journal.go).
CREATE TABLE IF NOT EXISTS iam_v2.erasure_journal (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL,
  site_id     uuid NOT NULL,
  erasure_ref text NOT NULL CONSTRAINT ej_ref CHECK (erasure_ref ~ '^ER-[0-9A-F]{12}$'),
  kind        text NOT NULL CONSTRAINT ej_kind CHECK (kind IN ('account', 'principal', 'stay', 'stay_guest', 'assisted_record')),
  subject_id  uuid NOT NULL,
  detail      text CONSTRAINT ej_detail CHECK (detail IS NULL OR detail ~ '^[a-z_]{3,40}$'),
  erased_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT ej_assisted_detail CHECK ((kind = 'assisted_record') = (detail IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS ej_unique ON iam_v2.erasure_journal (erasure_ref, kind, subject_id, COALESCE(detail, ''));
CREATE INDEX IF NOT EXISTS ej_site ON iam_v2.erasure_journal (tenant_id, site_id, erased_at);
COMMENT ON TABLE iam_v2.erasure_journal IS
  'Which records an erasure anonymised (0147): kind and record id only. Replayed after a restore so an older backup does not '
  'bring an erased guest back. Holds no personal data. Append-only.';
DROP TRIGGER IF EXISTS erasure_journal_append_only ON iam_v2.erasure_journal;
CREATE TRIGGER erasure_journal_append_only BEFORE UPDATE OR DELETE ON iam_v2.erasure_journal
  FOR EACH ROW EXECUTE FUNCTION iam_v2.data_retention_append_only();

-- The erasure can be replayed after a restore, and is journaled
CREATE OR REPLACE FUNCTION iam_v2.erase_guest_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text, p_public_counts jsonb)
 RETURNS jsonb
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; ev uuid[]; room uuid[]; macs macaddr[]; live text[] := '{}';
        ec uuid[]; ch uuid[]; ev_open uuid[];
        v_id uuid; v_shared integer; v_attempts integer; v_sessions integer; v_k integer; v_verif integer := 0;
        v_events integer := 0; v_given integer := 0; v_ref text; v_counts jsonb;
        ss uuid[]; gr uuid[]; v_who jsonb; v_guests integer := 0; v_other integer; v_try integer := 0;
        v_sig text; v_sig2 text; v_today date;
        v_replay boolean := COALESCE((p_public_counts->>'_replay')::boolean, false);
BEGIN
  IF p_operator IS NULL OR length(btrim(p_operator)) NOT BETWEEN 1 AND 200 THEN
    RAISE EXCEPTION 'an operator is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) NOT BETWEEN 4 AND 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  -- NOTHING JOINS THE SCOPE WHILE IT IS ERASED (0144). Lock every record in scope, then compute the scope again
  -- under the locks; if it moved in between, lock the new records too, a few times at most.
  LOOP
    SELECT COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'account'), '{}'),
           COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'principal'), '{}'),
           COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay'), '{}'),
           COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'entitlement'), '{}'),
           COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'device'), '{}'),
           COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_event'), '{}'),
           count(*) FILTER (WHERE o_kind = 'device_shared'),
           COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_shared'), '{}'),
           COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_guest'), '{}'),
           count(*) FILTER (WHERE o_kind = 'stay_shared_other'),
           COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'access_shared'), '{}'),
           COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'entitlement_credential'), '{}'),
           COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'challenge'), '{}'),
           md5(string_agg(o_kind || ':' || o_id::text, ',' ORDER BY o_kind, o_id))
      INTO a, p, s, e, d, ev, v_shared, ss, gr, v_other, room, ec, ch, v_sig FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
    PERFORM 1 FROM stays x WHERE x.id = ANY (s) OR x.id = ANY (ss) ORDER BY x.id FOR UPDATE;
    PERFORM 1 FROM guest_access_accounts x WHERE x.id = ANY (a) ORDER BY x.id FOR UPDATE;
    PERFORM 1 FROM guest_principals x WHERE x.id = ANY (p) ORDER BY x.id FOR UPDATE;
    PERFORM 1 FROM entitlements x WHERE x.id = ANY (e) OR x.id = ANY (ec) OR x.id = ANY (room) ORDER BY x.id FOR UPDATE;
    PERFORM 1 FROM devices dv WHERE dv.id = ANY (d) ORDER BY dv.id FOR UPDATE;
    SELECT md5(string_agg(o_kind || ':' || o_id::text, ',' ORDER BY o_kind, o_id)) INTO v_sig2
      FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
    EXIT WHEN v_sig2 IS NOT DISTINCT FROM v_sig;
    v_try := v_try + 1;
    IF v_try >= 3 THEN
      RAISE EXCEPTION 'guest_data_live' USING ERRCODE = 'object_in_use', DETAIL = 'busy';
    END IF;
  END LOOP;
  SELECT COALESCE(array_agg(dv.mac), '{}') INTO macs FROM devices dv WHERE dv.id = ANY (d) AND dv.mac::text NOT LIKE '03:%';
  v_today := site_today(p_site);

  -- REFUSED WHILE LIVE (D-C3). The PMS would copy an open stay straight back, and anonymising a stay, access or a
  -- device in use would cut people off mid-session. The room's access is checked here although it is not
  -- changed: the people using it are not the person asking, but they are online on this reservation.
  -- A STAY WITH MORE THAN ONE GUEST IS ERASED ONE GUEST AT A TIME (0141).
  IF p_kind = 'stay' AND stay_named_guests(p_id) > 1 THEN
    live := live || 'co_guests'::text;
  END IF;
  IF EXISTS (SELECT 1 FROM stays x WHERE (x.id = ANY (s) OR x.id = ANY (ss))
                                     AND x.status IN ('RESERVED', 'IN_HOUSE')
                                     AND ((x.departure IS NULL AND NOT stay_absent_from_published_roster(x.id))
                                          OR x.departure >= v_today - 1)) THEN
    live := live || 'pms_open'::text;
  END IF;
  -- A PMS event received in the last 24 hours may still be processed: refused. An older one that is still
  -- waiting will not be, and is given up below (0139, D-B2).
  IF EXISTS (SELECT 1 FROM stay_events x WHERE x.id = ANY (ev) AND x.processing_status = 'PENDING'
                                          AND x.received_at >= now() - interval '24 hours') THEN
    live := live || 'pms_pending'::text;
  END IF;
  IF EXISTS (SELECT 1 FROM entitlements x WHERE (x.id = ANY (e) OR x.id = ANY (ec) OR x.id = ANY (room)) AND x.status <> 'TERMINATED')
     OR EXISTS (SELECT 1 FROM post_stay_profiles x
                 WHERE x.origin_stay_id = ANY (s) AND x.status = 'ACTIVE' AND x.valid_until > now()) THEN
    live := live || 'access_live'::text;
  END IF;
  IF EXISTS (SELECT 1 FROM sessions x WHERE (x.device_id = ANY (d) OR x.entitlement_id = ANY (e) OR x.entitlement_id = ANY (ec)
                                              OR x.entitlement_id = ANY (room)) AND x.ended IS NULL)
     OR EXISTS (SELECT 1 FROM verification_access_grants x
                 WHERE x.tenant_id = p_tenant AND x.challenge_id = ANY (ch) AND x.ended_at IS NULL AND x.expires_at > now()) THEN
    live := live || 'online'::text;
  END IF;
  IF EXISTS (SELECT 1 FROM guest_signin_restrictions x
              WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs)
                AND x.released_at IS NULL AND (x.expires_at IS NULL OR x.expires_at >= now())) THEN
    live := live || 'signin_restricted'::text;
  END IF;
  -- A replay after a restore erases what was erased before: the checks were passed then, and the restored state is older.
  IF cardinality(live) > 0 AND NOT v_replay THEN
    RAISE EXCEPTION 'guest_data_live' USING ERRCODE = 'object_in_use', DETAIL = array_to_string(live, ',');
  END IF;

  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  -- What the person typed and where from: kept as a count, nothing else. Only their own access and exclusive
  -- devices; attempts to enter the reservation's room credentials are the typist's.
  UPDATE sign_in_attempts x
     SET submitted_room = NULL, device_ip = NULL, device_mac = NULL,
         sensitive_ciphertext = NULL, sensitive_nonce = NULL, encryption_key_id = NULL, cipher_version = NULL
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site
     AND (x.entitlement_id = ANY (e) OR x.device_mac = ANY (macs))
     AND num_nonnulls(x.submitted_room, x.device_ip, x.device_mac, x.sensitive_ciphertext) > 0;
  GET DIAGNOSTICS v_attempts = ROW_COUNT;
  -- What was typed to enter THIS reservation (the holder's name, the room) is the reservation's; who typed it
  -- (the address and device) is not, and stays.
  UPDATE sign_in_attempts x
     SET submitted_room = NULL, sensitive_ciphertext = NULL, sensitive_nonce = NULL, encryption_key_id = NULL, cipher_version = NULL
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.matched_stay_id = ANY (s)
     AND num_nonnulls(x.submitted_room, x.sensitive_ciphertext) > 0;
  GET DIAGNOSTICS v_k = ROW_COUNT; v_attempts := v_attempts + v_k;
  -- The MAC address is the key of these records, so it is replaced before the device itself is anonymised.
  UPDATE verification_access_grants x
     SET device_mac = anonymised_mac(), ip = '0.0.0.0'::inet, destination_digest = encode(public.gen_random_bytes(32), 'hex')
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.challenge_id = ANY (ch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_verif := v_verif + v_k;
  UPDATE guest_signin_restrictions x SET device_mac = anonymised_mac(), last_submitted_room = NULL
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_verif := v_verif + v_k;
  UPDATE otp_device_bindings x SET device_mac = anonymised_mac()
   WHERE x.tenant_id = p_tenant AND x.challenge_id = ANY (ch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_verif := v_verif + v_k;
  UPDATE guest_signin_device_passes x SET device_mac = anonymised_mac()
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_verif := v_verif + v_k;
  -- Session addresses only on the person's exclusive devices (any session there is theirs by definition).
  UPDATE sessions x SET ip = NULL, mac = NULL
   WHERE x.device_id = ANY (d) AND x.ended IS NOT NULL AND (x.ip IS NOT NULL OR x.mac IS NOT NULL);
  GET DIAGNOSTICS v_sessions = ROW_COUNT;

  -- PMS events that never resolved to a stay but name the guest's reservation (stay events are anonymised with
  -- their stay below). A waiting one older than 24 hours is given up first; a newer one was refused above.
  PERFORM begin_controlled_operation('stay');
  -- Only a message that never resolved to a stay may be given up here. A waiting message cannot carry a stay
  -- (the inbox guard forbids it), but a shared stay's messages are other guests' too and are never given up.
  ev_open := ARRAY(SELECT x.id FROM stay_events x WHERE x.id = ANY (ev) AND x.stay_id IS NULL);
  v_given := give_up_waiting_stay_events(p_tenant, p_site, now() - interval '24 hours', ev_open, 10000);
  IF NOT v_replay AND EXISTS (SELECT 1 FROM stay_events x WHERE x.id = ANY (ev) AND x.processing_status = 'PENDING') THEN
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
    UPDATE stay_events x SET payload = event_payload_for_guests(x.payload, v_who, false, x.pms_interface_id)
     WHERE x.id = ANY (ev) AND x.processing_status <> 'PENDING'
       AND x.payload IS DISTINCT FROM event_payload_for_guests(x.payload, v_who, false, x.pms_interface_id);
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

  -- The public half names the reference when it already used it in the activity log (0144).
  v_ref := COALESCE(p_public_counts->>'_erasure_ref', 'ER-' || upper(encode(public.gen_random_bytes(6), 'hex')));
  v_counts := jsonb_build_object(
    'client_accounts', cardinality(a), 'guest_identities', cardinality(p), 'hotel_stays', cardinality(s),
    'stay_guests', v_guests, 'hotel_stays_kept_shared', cardinality(ss) + v_other,
    'pms_events', v_events, 'devices', cardinality(d), 'devices_kept_shared', v_shared,
    'room_access_kept_shared', cardinality(room), 'sessions', v_sessions,
    'sign_in_attempts', v_attempts, 'verification_records', v_verif)
    || (COALESCE(p_public_counts, '{}'::jsonb) - '_erasure_ref' - '_replay');
  IF NOT v_replay THEN
    INSERT INTO guest_data_erasures (erasure_ref, tenant_id, site_id, subject_kind, erased_by, erase_reason, counts)
    VALUES (v_ref, p_tenant, p_site, p_kind, btrim(p_operator), btrim(p_reason), v_counts);
    INSERT INTO erasure_journal (tenant_id, site_id, erasure_ref, kind, subject_id) VALUES (p_tenant, p_site, v_ref, p_kind, p_id);
  END IF;
  RETURN jsonb_build_object('erasure_ref', v_ref, 'counts', v_counts);
END $function$;

-- One erasure, for a request (journaled) or for a replay after a restore (not)
CREATE OR REPLACE FUNCTION public.guest_data_erase_core(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text,
                                                         p_replay boolean, p_ref text)
 RETURNS jsonb
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_ids text[]; v_vals text[]; v_strong text[]; v_names text[]; v_weak text[]; v_macs text[]; v_pats text[];
        v_since timestamptz; n_codes integer; n_oauth integer; n_audit integer; v_ref text; v_res jsonb;
        a uuid[]; p uuid[]; s uuid[]; d uuid[]; gr uuid[]; n_dhcp integer;
BEGIN
  -- What identifies the guest, read BEFORE anything is anonymised.
  SELECT COALESCE(array_agg(o_value) FILTER (WHERE o_kind = 'strong'), '{}'),
         COALESCE(array_agg(o_value) FILTER (WHERE o_kind = 'name'), '{}'),
         COALESCE(array_agg(o_value) FILTER (WHERE o_kind = 'weak'), '{}')
    INTO v_strong, v_names, v_weak FROM iam_v2.guest_data_identifiers(p_tenant, p_site, p_kind, p_id);
  IF NOT p_replay THEN
    PERFORM public.guest_data_reason_check(p_tenant, p_site, p_kind, p_id, p_reason);
  END IF;
  SELECT COALESCE(array_agg(DISTINCT o_id::text) FILTER (
             WHERE o_kind NOT IN ('stay_shared', 'stay_shared_other', 'access_shared', 'device_shared', 'challenge')
               AND NOT (o_kind = 'stay_event' AND EXISTS (SELECT 1 FROM iam_v2.stay_events e
                                                         WHERE e.id = o_id AND iam_v2.stay_named_guests(e.stay_id) > 1))), '{}'),
         COALESCE(array_agg(DISTINCT lower(o_value))
                    FILTER (WHERE o_kind IN ('account', 'identity', 'device') AND o_value IS NOT NULL), '{}'),
         COALESCE(array_agg(DISTINCT lower(o_value)) FILTER (WHERE o_kind = 'device' AND o_value IS NOT NULL), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'account'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind IN ('principal', 'identity')), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'device'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_guest'), '{}')
    INTO v_ids, v_vals, v_macs, a, p, s, d, gr FROM iam_v2.guest_data_scope(p_tenant, p_site, p_kind, p_id);
  -- Nothing about the guest can be older than their first record: the activity log is read from there.
  SELECT least((SELECT min(x.created_at) FROM iam_v2.guest_access_accounts x WHERE x.id = ANY (a)),
               (SELECT min(x.verified_at) FROM iam_v2.guest_principal_identities x WHERE x.guest_principal_id = ANY (p)),
               (SELECT min(x.expires_at) - interval '1 day' FROM iam_v2.auth_contexts x
                 WHERE x.guest_principal_id = ANY (p) OR x.guest_account_id = ANY (a) OR x.stay_id = ANY (s)),
               (SELECT min(x.received_at) FROM iam_v2.stay_events x
                 WHERE x.stay_id = ANY (s) OR x.stay_id IN (SELECT sg.stay_id FROM iam_v2.stay_guests sg WHERE sg.id = ANY (gr))),
               (SELECT min(x.first_seen) FROM iam_v2.devices x WHERE x.id = ANY (d))) - interval '1 day'
    INTO v_since;
  IF EXISTS (SELECT 1 FROM iam_v2.guest_access_accounts x WHERE x.id = ANY (a) AND x.created_at IS NULL)
     OR EXISTS (SELECT 1 FROM iam_v2.devices x WHERE x.id = ANY (d) AND x.first_seen IS NULL) THEN
    v_since := NULL;
  END IF;
  v_since := COALESCE(v_since, '-infinity');

  -- The one-time codes are anonymised LAST: the person's verification records and code bindings are tied to them by
  -- the address the code went to, and the erasure below reads that address (0145). Counted now, changed after.
  SELECT count(*) INTO n_codes FROM auth_otps o WHERE o.tenant_id = p_tenant AND lower(o.destination) = ANY (v_vals);
  UPDATE social_oauth_states x SET client_ip = NULL, client_mac = NULL, user_agent = NULL, code_verifier = NULL, nonce = NULL
   WHERE x.tenant_id = p_tenant AND x.client_mac::text = ANY (v_macs);
  GET DIAGNOSTICS n_oauth = ROW_COUNT;

  -- THE ACTIVITY LOG KEEPS WHO AND WHEN (D-R2), AND TOUCHES NOTHING THAT IS NOT ABOUT THIS GUEST (0145). An entry
  -- about the guest's own records keeps its action, staff member and time; the record id gives way to the erasure
  -- reference and the guest's identifiers and name become "[erased]". Any other entry loses only a STRONG
  -- identifier (an address, a username, a reservation number, a device address): a room number, a first or last
  -- name alone, or a display name never rewrites an entry about something else (a VLAN 101, a Smith family
  -- request). What staff typed into the guest-data screens is read more widely, since staff typed it about
  -- someone: the full name there, and in a search any part of a name or room. A refusal below undoes this.
  v_ref := COALESCE(p_ref, 'ER-' || upper(encode(public.gen_random_bytes(6), 'hex')));
  v_pats := ARRAY(SELECT '%' || replace(replace(replace(x, '\', '\\'), '%', '\%'), '_', '\_') || '%'
                    FROM unnest(v_strong || v_names || v_weak) x WHERE length(x) >= 3);
  WITH cand AS MATERIALIZED (
    SELECT l.tableoid AS toid, l.ctid AS rid, l.payload AS old_payload, l.actor_type,
           (l.target_id = ANY (v_ids) OR lower(l.target_id) = ANY (v_strong) OR lower(l.actor_id) = ANY (v_strong)) AS about_it,
           CASE
             WHEN l.action = 'guest_data.searched' THEN iam_v2.redact_guest_text(l.payload, v_strong || v_names || v_weak, true)
             -- An entry ABOUT this guest loses every way of naming them (a surname and a room number included); one a staff
             -- member typed about someone loses a full name and the strong identifiers.
             WHEN l.target_id = ANY (v_ids) OR lower(l.target_id) = ANY (v_strong) OR lower(l.actor_id) = ANY (v_strong)
               THEN iam_v2.redact_guest_text(l.payload, v_strong || v_names || v_weak, false)
             WHEN l.action LIKE 'guest_data.%'
               THEN iam_v2.redact_guest_text(l.payload, v_strong || v_names, false)
             ELSE iam_v2.redact_guest_text(l.payload, v_strong, false)
           END AS new_payload
      FROM audit_log l
     WHERE l.tenant_id = p_tenant AND l.ts >= v_since
       AND (l.target_id = ANY (v_ids) OR lower(l.target_id) = ANY (v_strong) OR lower(l.actor_id) = ANY (v_strong)
            OR l.action = 'guest_data.searched' OR lower(l.payload::text) LIKE ANY (v_pats)))
  UPDATE audit_log l
     SET payload = cand.new_payload || jsonb_build_object('erasure_ref', v_ref),
         target_id = CASE WHEN cand.about_it THEN NULL ELSE l.target_id END,
         ip = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.ip END,
         user_agent = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.user_agent END,
         actor_id = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.actor_id END
    FROM cand
   WHERE l.tableoid = cand.toid AND l.ctid = cand.rid AND (cand.about_it OR cand.new_payload IS DISTINCT FROM cand.old_payload);
  GET DIAGNOSTICS n_audit = ROW_COUNT;

  -- Refuses (and so undoes the above) while anything is live.
  v_res := iam_v2.erase_guest_records(p_tenant, p_site, p_kind, p_id, p_operator, p_reason,
             jsonb_build_object('verification_codes', n_codes, 'oauth_states', n_oauth, 'audit_entries', n_audit,
                                '_erasure_ref', v_ref, '_replay', p_replay));
  UPDATE auth_otps o SET destination = 'anonymised', ip = NULL, user_agent = NULL
   WHERE o.tenant_id = p_tenant AND lower(o.destination) = ANY (v_vals);
  -- A static address reservation (Network -> DHCP) is staff-managed infrastructure and is not touched here; it is reported,
  -- so that staff remove it where it lives.
  SELECT count(*) INTO n_dhcp FROM public.dhcp_reservations r WHERE lower(r.mac::text) = ANY (v_macs);
  IF n_dhcp > 0 THEN
    v_res := jsonb_set(v_res, '{counts,dhcp_reservations_kept}', to_jsonb(n_dhcp));
  END IF;
  RETURN v_res;
END $function$;

CREATE OR REPLACE FUNCTION public.guest_data_erase(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text)
 RETURNS jsonb
 LANGUAGE sql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
  SELECT public.guest_data_erase_core(p_tenant, p_site, p_kind, p_id, p_operator, p_reason, false, NULL)
$function$;

-- An assisted erasure is journaled too
CREATE OR REPLACE FUNCTION iam_v2.assisted_request_erase_run(p_tenant uuid, p_site uuid, p_request uuid, p_actor text,
        p_reason text, p_confirm text, p_signature text, p_selected jsonb)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE q record; v jsonb; v_ref text; v_id uuid; v_macs text[] := '{}'; v_counts jsonb; v_receipt jsonb;
        n_sessions integer := 0; n_attempts integer := 0; n_verif integer := 0; n_bind integer := 0; n_restr integer := 0;
        n_pass integer := 0; n_redirect integer := 0; n_dev integer := 0; v_k integer; v_total integer;
        s_ids uuid[]; a_ids uuid[]; g_ids uuid[]; b_ids uuid[]; r_ids uuid[]; p_ids uuid[]; o_ids uuid[]; d_ids uuid[];
        v_since timestamptz;
BEGIN
  IF p_actor IS NULL OR length(btrim(p_actor)) NOT BETWEEN 1 AND 200 THEN
    RAISE EXCEPTION 'an operator is required' USING ERRCODE = 'invalid_parameter_value';
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
  IF q.purpose <> 'ERASURE' THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'wrong_purpose';
  END IF;
  IF lower(btrim(COALESCE(p_confirm, ''))) <> lower(q.ref) THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'confirmation';
  END IF;
  -- NOTHING MOVES UNDER THE REVIEW: the request, the address (among assisted requests) and the devices are locked, then
  -- the review is computed again and must be what staff saw. A session that begins meanwhile waits for the device;
  -- other records keyed by the address are not locked out, and a change in them shows in the signature.
  PERFORM pg_advisory_xact_lock(hashtextextended(p_site::text || ':' || m::text, 146)) FROM unnest(q.macs) m ORDER BY m;
  PERFORM 1 FROM devices dv WHERE dv.tenant_id = p_tenant AND dv.site_id = p_site AND dv.mac = ANY (q.macs) ORDER BY dv.id FOR UPDATE;
  v := assisted_validate_selection(p_tenant, p_site, p_request, p_signature, p_selected, true);
  v_total := jsonb_array_length(v -> 'selected');

  SELECT COALESCE(array_agg(s.id) FILTER (WHERE s.kind = 'session'), '{}'),
         COALESCE(array_agg(s.id) FILTER (WHERE s.kind = 'sign_in_attempt'), '{}'),
         COALESCE(array_agg(s.id) FILTER (WHERE s.kind = 'verification_access'), '{}'),
         COALESCE(array_agg(s.id) FILTER (WHERE s.kind = 'code_device_binding'), '{}'),
         COALESCE(array_agg(s.id) FILTER (WHERE s.kind = 'sign_in_restriction'), '{}'),
         COALESCE(array_agg(s.id) FILTER (WHERE s.kind = 'sign_in_pass'), '{}'),
         COALESCE(array_agg(s.id) FILTER (WHERE s.kind = 'sign_in_redirect'), '{}'),
         COALESCE(array_agg(s.id) FILTER (WHERE s.kind = 'device'), '{}')
    INTO s_ids, a_ids, g_ids, b_ids, r_ids, p_ids, o_ids, d_ids
    FROM jsonb_to_recordset(v -> 'selected') AS s(kind text, id uuid, class text, device uuid);
  -- The addresses of the devices that are anonymised, read before they are: the activity log is cleaned of them.
  SELECT COALESCE(array_agg(lower(dv.mac::text)), '{}'),
         CASE WHEN bool_or(dv.first_seen IS NULL) THEN NULL ELSE min(dv.first_seen) - interval '1 day' END
    INTO v_macs, v_since FROM devices dv WHERE dv.id = ANY (d_ids);

  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  -- Only ended sessions (the review never offers another), and only their addresses: usage totals, entitlements and
  -- the accounting samples are untouched.
  UPDATE sessions x SET ip = NULL, mac = NULL
   WHERE x.id = ANY (s_ids) AND x.tenant_id = p_tenant AND x.site_id = p_site AND x.ended IS NOT NULL AND (x.ip IS NOT NULL OR x.mac IS NOT NULL);
  GET DIAGNOSTICS n_sessions = ROW_COUNT;
  UPDATE sign_in_attempts x
     SET submitted_room = NULL, device_ip = NULL, device_mac = NULL,
         sensitive_ciphertext = NULL, sensitive_nonce = NULL, encryption_key_id = NULL, cipher_version = NULL
   WHERE x.id = ANY (a_ids) AND x.tenant_id = p_tenant AND x.site_id = p_site;
  GET DIAGNOSTICS n_attempts = ROW_COUNT;
  UPDATE verification_access_grants x
     SET device_mac = anonymised_mac(), ip = '0.0.0.0'::inet, destination_digest = encode(public.gen_random_bytes(32), 'hex')
   WHERE x.id = ANY (g_ids) AND x.tenant_id = p_tenant AND x.site_id = p_site AND x.ended_at IS NOT NULL;
  GET DIAGNOSTICS n_verif = ROW_COUNT;
  UPDATE otp_device_bindings x SET device_mac = anonymised_mac()
   WHERE x.challenge_id = ANY (b_ids) AND x.tenant_id = p_tenant AND x.device_mac = ANY (q.macs);
  GET DIAGNOSTICS n_bind = ROW_COUNT;
  UPDATE guest_signin_restrictions x SET device_mac = anonymised_mac(), last_submitted_room = NULL
   WHERE x.id = ANY (r_ids) AND x.tenant_id = p_tenant AND x.site_id = p_site;
  GET DIAGNOSTICS n_restr = ROW_COUNT;
  UPDATE guest_signin_device_passes x SET device_mac = anonymised_mac()
   WHERE x.id = ANY (p_ids) AND x.tenant_id = p_tenant AND x.site_id = p_site;
  GET DIAGNOSTICS n_pass = ROW_COUNT;
  UPDATE public.social_oauth_states x SET client_ip = NULL, client_mac = NULL, user_agent = NULL, code_verifier = NULL, nonce = NULL
   WHERE md5(x.state)::uuid = ANY (o_ids) AND x.tenant_id = p_tenant AND x.client_mac = ANY (q.macs);
  GET DIAGNOSTICS n_redirect = ROW_COUNT;
  -- A device whose every record is inside the request and selected is anonymised like an exclusive device (its
  -- remaining ended sessions lose their addresses with it: they are all selected, the review required it).
  FOREACH v_id IN ARRAY d_ids LOOP
    PERFORM anonymise_device(v_id);
    n_dev := n_dev + 1;
  END LOOP;
  -- An account the person registered on that device holds its address too (only a stated account can be here: any other
  -- account created on the device made it someone else's).
  UPDATE guest_access_accounts g SET created_by_device_mac = NULL
   WHERE g.tenant_id = p_tenant AND g.site_id = p_site AND g.created_by_device_mac = ANY (q.macs)
     AND g.created_by_device_mac::text = ANY (v_macs);
  PERFORM set_config('iam_v2.anonymise', '', true);

  v_ref := 'ER-' || upper(encode(public.gen_random_bytes(6), 'hex'));
  v_counts := jsonb_build_object('assisted_request', q.ref, 'sessions', n_sessions, 'sign_in_attempts', n_attempts,
                                 'verification_records', n_verif + n_bind, 'sign_in_restrictions', n_restr,
                                 'sign_in_passes', n_pass, 'sign_in_redirects', n_redirect, 'devices', n_dev,
                                 'records_selected', v_total);
  INSERT INTO guest_data_erasures (erasure_ref, tenant_id, site_id, subject_kind, erased_by, erase_reason, counts)
  VALUES (v_ref, p_tenant, p_site, 'assisted', btrim(p_actor), btrim(p_reason), v_counts);
  INSERT INTO erasure_journal (tenant_id, site_id, erasure_ref, kind, subject_id, detail)
    SELECT p_tenant, p_site, v_ref, 'assisted_record', s.id, s.kind FROM jsonb_to_recordset(v -> 'selected') AS s(kind text, id uuid, class text, device uuid)
    ON CONFLICT DO NOTHING;
  INSERT INTO assisted_guest_request_records (request_id, rec_kind, rec_id, rec_class, action)
    SELECT p_request, s.kind, s.id, s.class, 'ANONYMISED' FROM jsonb_to_recordset(v -> 'selected') AS s(kind text, id uuid, class text, device uuid);
  -- The file the hotel keeps of this request, handed back once: the closing scrub removes it from the database.
  v_receipt := jsonb_build_object('reference', q.ref, 'requester', q.requester_ref, 'verification_method', q.verification_method,
                                  'evidence_note', q.evidence_note, 'range_from', q.range_from, 'range_to', q.range_to,
                                  'statements', jsonb_build_object('identity_verified', q.attest_identity, 'device_shown', q.attest_device,
                                                                   'sole_use', q.attest_sole_use, 'private_address_confirmed', q.attest_private_address),
                                  'prepared_by', q.created_by, 'opened_at', q.created_at);
  PERFORM assisted_close(p_tenant, p_site, p_request, p_actor, 'COMPLETED', p_reason, v_counts || jsonb_build_object('erasure_ref', v_ref), 'COMPLETED');
  RETURN jsonb_build_object('erasure_ref', v_ref, 'counts', v_counts, 'receipt', v_receipt, 'macs_cleared', to_jsonb(v_macs),
                            'since', COALESCE(v_since, '-infinity'::timestamptz));
END $function$;

-- The anonymising writes of an assisted erasure, by record id: what a replay after a restore applies.
CREATE OR REPLACE FUNCTION iam_v2.assisted_apply_records(p_tenant uuid, p_site uuid, p_kinds text[], p_ids uuid[])
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE s_ids uuid[]; a_ids uuid[]; g_ids uuid[]; b_ids uuid[]; r_ids uuid[]; p_pass uuid[]; o_ids uuid[]; d_ids uuid[];
        v_macs text[]; v_id uuid; n integer := 0; k integer;
BEGIN
  SELECT COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'session'), '{}'),
         COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'sign_in_attempt'), '{}'),
         COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'verification_access'), '{}'),
         COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'code_device_binding'), '{}'),
         COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'sign_in_restriction'), '{}'),
         COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'sign_in_pass'), '{}'),
         COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'sign_in_redirect'), '{}'),
         COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'device'), '{}')
    INTO s_ids, a_ids, g_ids, b_ids, r_ids, p_pass, o_ids, d_ids FROM unnest(p_kinds, p_ids) z(k, i);
  SELECT COALESCE(array_agg(lower(dv.mac::text)), '{}') INTO v_macs FROM devices dv WHERE dv.id = ANY (d_ids) AND dv.mac::text NOT LIKE '03:%';
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  UPDATE sessions x SET ip = NULL, mac = NULL
   WHERE x.id = ANY (s_ids) AND x.tenant_id = p_tenant AND x.ended IS NOT NULL AND (x.ip IS NOT NULL OR x.mac IS NOT NULL);
  GET DIAGNOSTICS k = ROW_COUNT; n := n + k;
  UPDATE sign_in_attempts x
     SET submitted_room = NULL, device_ip = NULL, device_mac = NULL,
         sensitive_ciphertext = NULL, sensitive_nonce = NULL, encryption_key_id = NULL, cipher_version = NULL
   WHERE x.id = ANY (a_ids) AND x.tenant_id = p_tenant AND num_nonnulls(x.submitted_room, x.device_ip, x.device_mac, x.sensitive_ciphertext) > 0;
  GET DIAGNOSTICS k = ROW_COUNT; n := n + k;
  UPDATE verification_access_grants x
     SET device_mac = anonymised_mac(), ip = '0.0.0.0'::inet, destination_digest = encode(public.gen_random_bytes(32), 'hex')
   WHERE x.id = ANY (g_ids) AND x.tenant_id = p_tenant AND x.ended_at IS NOT NULL AND x.device_mac::text NOT LIKE '03:%';
  GET DIAGNOSTICS k = ROW_COUNT; n := n + k;
  UPDATE otp_device_bindings x SET device_mac = anonymised_mac()
   WHERE x.challenge_id = ANY (b_ids) AND x.tenant_id = p_tenant AND x.device_mac::text NOT LIKE '03:%';
  GET DIAGNOSTICS k = ROW_COUNT; n := n + k;
  UPDATE guest_signin_restrictions x SET device_mac = anonymised_mac(), last_submitted_room = NULL
   WHERE x.id = ANY (r_ids) AND x.tenant_id = p_tenant AND x.device_mac::text NOT LIKE '03:%';
  GET DIAGNOSTICS k = ROW_COUNT; n := n + k;
  UPDATE guest_signin_device_passes x SET device_mac = anonymised_mac()
   WHERE x.id = ANY (p_pass) AND x.tenant_id = p_tenant AND x.device_mac::text NOT LIKE '03:%';
  GET DIAGNOSTICS k = ROW_COUNT; n := n + k;
  UPDATE public.social_oauth_states x SET client_ip = NULL, client_mac = NULL, user_agent = NULL, code_verifier = NULL, nonce = NULL
   WHERE md5(x.state)::uuid = ANY (o_ids) AND x.tenant_id = p_tenant AND num_nonnulls(x.client_ip, x.client_mac, x.user_agent) > 0;
  GET DIAGNOSTICS k = ROW_COUNT; n := n + k;
  FOREACH v_id IN ARRAY d_ids LOOP
    PERFORM anonymise_device(v_id);
  END LOOP;
  PERFORM set_config('iam_v2.anonymise', '', true);
  RETURN jsonb_build_object('rows_changed', n, 'macs_cleared', to_jsonb(v_macs));
END $function$;
COMMENT ON FUNCTION iam_v2.assisted_apply_records(uuid, uuid, text[], uuid[]) IS
  'Applies the anonymising writes of an assisted erasure to records by id, without a request (0147): the replay after a restore.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. The export and the replay (scd only).
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.erasure_journal_export(p_tenant uuid, p_site uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
  SELECT COALESCE(jsonb_agg(to_jsonb(j) - 'seq' ORDER BY j.erased_at, j.id), '[]'::jsonb)
    FROM iam_v2.erasure_journal j WHERE j.tenant_id = p_tenant AND j.site_id = p_site
$$;

-- Replays a journal carried from outside the database (the file scd keeps next to the backups) onto the database as it is
-- now. An entry the database does not have is exactly an erasure it does not know of -- one made after the backup this
-- database was restored from -- so only those are applied, and each is recorded only once it has been applied: an entry
-- that cannot be applied is reported and tried again at the next call, never silently dropped. An entry whose record no
-- longer exists (created after the backup) is recorded and skipped. Idempotent and cheap: a database that has every entry
-- does nothing.
CREATE OR REPLACE FUNCTION public.erasure_journal_replay(p_journal jsonb) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $function$
DECLARE e record; v_reapplied integer := 0; v_gone integer := 0; v_known integer := 0; v_failed jsonb := '[]'::jsonb;
        v_res jsonb; v_changed integer := 0;
BEGIN
  IF jsonb_typeof(p_journal) IS DISTINCT FROM 'array' THEN
    RAISE EXCEPTION 'a journal is a list of entries' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT count(*) INTO v_known FROM jsonb_to_recordset(p_journal) AS x(id uuid)
   WHERE EXISTS (SELECT 1 FROM iam_v2.erasure_journal j WHERE j.id = x.id);
  FOR e IN SELECT x.* FROM jsonb_to_recordset(p_journal)
             AS x(id uuid, tenant_id uuid, site_id uuid, erasure_ref text, kind text, subject_id uuid, detail text, erased_at timestamptz)
            WHERE NOT EXISTS (SELECT 1 FROM iam_v2.erasure_journal j WHERE j.id = x.id)
            ORDER BY x.erased_at, x.id LOOP
    BEGIN
      IF e.kind = 'assisted_record' THEN
        v_res := iam_v2.assisted_apply_records(e.tenant_id, e.site_id, ARRAY[e.detail], ARRAY[e.subject_id]);
        v_changed := v_changed + COALESCE((v_res ->> 'rows_changed')::integer, 0);
        PERFORM public.audit_clean_macs(e.tenant_id, ARRAY(SELECT jsonb_array_elements_text(v_res -> 'macs_cleared')), e.erasure_ref, '-infinity');
      ELSE
        v_res := public.guest_data_erase_core(e.tenant_id, e.site_id, e.kind, e.subject_id, 'restore', 'erasure replayed after a restore', true, e.erasure_ref);
      END IF;
      INSERT INTO iam_v2.erasure_journal (id, tenant_id, site_id, erasure_ref, kind, subject_id, detail, erased_at)
      VALUES (e.id, e.tenant_id, e.site_id, e.erasure_ref, e.kind, e.subject_id, e.detail, COALESCE(e.erased_at, now()))
      ON CONFLICT DO NOTHING;
      v_reapplied := v_reapplied + 1;
    EXCEPTION
      WHEN no_data_found THEN
        INSERT INTO iam_v2.erasure_journal (id, tenant_id, site_id, erasure_ref, kind, subject_id, detail, erased_at)
        VALUES (e.id, e.tenant_id, e.site_id, e.erasure_ref, e.kind, e.subject_id, e.detail, COALESCE(e.erased_at, now()))
        ON CONFLICT DO NOTHING;
        v_gone := v_gone + 1;
      WHEN OTHERS THEN
        v_failed := v_failed || jsonb_build_array(jsonb_build_object('erasure_ref', e.erasure_ref, 'kind', e.kind, 'error', SQLERRM));
    END;
  END LOOP;
  RETURN jsonb_build_object('entries', jsonb_array_length(p_journal), 'already_known', v_known, 'reapplied', v_reapplied,
                            'records_gone', v_gone, 'assisted_rows_changed', v_changed, 'failed', v_failed);
END $function$;
COMMENT ON FUNCTION public.erasure_journal_replay(jsonb) IS
  'Erases again what the journal says was erased (0147). Run by scd after a restore and hourly; granted to nobody else.';

-- ---------------------------------------------------------------------------------------------------------
-- 4. Who is already anonymised no longer makes a device shared, and a stay link is followed only to the same guest.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.anonymised_entitlements(p_tenant uuid) RETURNS uuid[]
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(array_agg(en.id), '{}') FROM entitlements en
   WHERE en.tenant_id = p_tenant
     AND (en.guest_account_id IN (SELECT g.id FROM guest_access_accounts g WHERE g.tenant_id = p_tenant
                                    AND g.username ~ '^anon-[0-9a-f]{32,64}@anonymised[.]removed-')
          OR en.guest_principal_id IN (SELECT gp.id FROM guest_principals gp WHERE gp.tenant_id = p_tenant
                                         AND EXISTS (SELECT 1 FROM guest_principal_identities i WHERE i.guest_principal_id = gp.id)
                                         AND NOT EXISTS (SELECT 1 FROM guest_principal_identities i
                                                          WHERE i.guest_principal_id = gp.id AND NOT iam_v2.is_anon_token(i.factor_value_norm))))
$$;
CREATE OR REPLACE FUNCTION iam_v2.anonymised_accounts(p_tenant uuid) RETURNS uuid[]
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$ SELECT COALESCE(array_agg(g.id), '{}') FROM guest_access_accounts g
           WHERE g.tenant_id = p_tenant AND g.username ~ '^anon-[0-9a-f]{32,64}@anonymised[.]removed-' $$;
CREATE OR REPLACE FUNCTION iam_v2.anonymised_principals(p_tenant uuid) RETURNS uuid[]
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$ SELECT COALESCE(array_agg(gp.id), '{}') FROM guest_principals gp
           WHERE gp.tenant_id = p_tenant
             AND EXISTS (SELECT 1 FROM guest_principal_identities i WHERE i.guest_principal_id = gp.id)
             AND NOT EXISTS (SELECT 1 FROM guest_principal_identities i WHERE i.guest_principal_id = gp.id AND NOT iam_v2.is_anon_token(i.factor_value_norm)) $$;
COMMENT ON FUNCTION iam_v2.anonymised_entitlements(uuid) IS
  'Entitlements whose account or identity is already anonymised (0147): a link to someone who no longer identifies anybody does not make a device shared.';

-- A person's phone is theirs when the only other links are to records already anonymised
CREATE OR REPLACE FUNCTION iam_v2.device_exclusive_to(p_device uuid, p_own_ind uuid[], p_principals uuid[], p_accounts uuid[], p_challenges uuid[])
    RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT (
      EXISTS (SELECT 1 FROM entitlement_devices ed WHERE ed.device_id = p_device AND ed.entitlement_id = ANY (p_own_ind || iam_v2.anonymised_entitlements((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))))
      OR EXISTS (SELECT 1 FROM sessions se WHERE se.device_id = p_device AND se.entitlement_id = ANY (p_own_ind || iam_v2.anonymised_entitlements((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))))
      OR EXISTS (SELECT 1 FROM principal_device_credentials c WHERE c.device_id = p_device AND c.guest_principal_id = ANY (p_principals || iam_v2.anonymised_principals((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))))
      OR EXISTS (SELECT 1 FROM auth_contexts ac WHERE ac.device_id = p_device
                    AND (ac.guest_account_id = ANY (p_accounts || iam_v2.anonymised_accounts((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))) OR ac.guest_principal_id = ANY (p_principals || iam_v2.anonymised_principals((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device)))))
      OR EXISTS (SELECT 1 FROM devices dv JOIN guest_access_accounts g
                    ON g.created_by_device_mac = dv.mac AND g.tenant_id = dv.tenant_id AND g.site_id = dv.site_id
                  WHERE dv.id = p_device AND g.id = ANY (p_accounts || iam_v2.anonymised_accounts((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))))
    )
    AND NOT EXISTS (SELECT 1 FROM entitlement_devices ed WHERE ed.device_id = p_device AND NOT ed.entitlement_id = ANY (p_own_ind || iam_v2.anonymised_entitlements((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))))
    AND NOT EXISTS (SELECT 1 FROM sessions se WHERE se.device_id = p_device AND NOT se.entitlement_id = ANY (p_own_ind || iam_v2.anonymised_entitlements((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))))
    AND NOT EXISTS (SELECT 1 FROM principal_device_credentials c WHERE c.device_id = p_device AND NOT c.guest_principal_id = ANY (p_principals || iam_v2.anonymised_principals((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))))
    AND NOT EXISTS (SELECT 1 FROM auth_contexts ac WHERE ac.device_id = p_device
                       AND NOT (COALESCE(ac.guest_account_id = ANY (p_accounts || iam_v2.anonymised_accounts((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))), false)
                                OR COALESCE(ac.guest_principal_id = ANY (p_principals || iam_v2.anonymised_principals((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))), false)))
    AND NOT EXISTS (SELECT 1 FROM devices dv JOIN guest_access_accounts g
                       ON g.created_by_device_mac = dv.mac AND g.tenant_id = dv.tenant_id AND g.site_id = dv.site_id
                     WHERE dv.id = p_device AND NOT g.id = ANY (p_accounts || iam_v2.anonymised_accounts((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))))
    -- Someone else proved an address on it, or signed in with an access that is not theirs: records keyed by the
    -- device's MAC address that this person's own one-time codes do not explain.
    AND NOT EXISTS (SELECT 1 FROM devices dv JOIN verification_access_grants g
                       ON g.device_mac = dv.mac AND g.tenant_id = dv.tenant_id AND g.site_id = dv.site_id
                     WHERE dv.id = p_device AND (g.challenge_id IS NULL OR NOT g.challenge_id = ANY (p_challenges)))
    AND NOT EXISTS (SELECT 1 FROM devices dv JOIN otp_device_bindings b
                       ON b.device_mac = dv.mac AND b.tenant_id = dv.tenant_id
                     WHERE dv.id = p_device AND NOT b.challenge_id = ANY (p_challenges))
    AND NOT EXISTS (SELECT 1 FROM devices dv JOIN sign_in_attempts sa
                       ON sa.device_mac = dv.mac AND sa.tenant_id = dv.tenant_id AND sa.site_id = dv.site_id
                     WHERE dv.id = p_device AND sa.entitlement_id IS NOT NULL AND NOT sa.entitlement_id = ANY (p_own_ind || iam_v2.anonymised_entitlements((SELECT dv0.tenant_id FROM iam_v2.devices dv0 WHERE dv0.id = p_device))));
$$;

-- ---------------------------------------------------------------------------------------------------------
-- 5. What staff typed or are called in the records, and the one gate that may change it.
-- ---------------------------------------------------------------------------------------------------------
-- Many records are append-only (a settings change, a slot action, a revoked voucher) and carry free text a member of
-- staff typed about a guest, or the staff member's email as the person who acted. Neither belongs there for ever.
-- This registry names every such column. A guard on each of those tables lets ONE thing through: an UPDATE that
-- changes only registered columns, made by the table's owner inside the transaction-local scrub that the definer
-- functions below switch on. Nothing else changes: every other guard on the table is as it was.
CREATE TABLE IF NOT EXISTS iam_v2.scrub_registry (
  table_name  text NOT NULL,
  column_name text NOT NULL,
  kind        text NOT NULL CONSTRAINT sr_kind CHECK (kind IN ('label', 'note', 'secret')),
  ts_column   text,
  handled     boolean NOT NULL DEFAULT false,
  jsonb_key   text,
  PRIMARY KEY (table_name, column_name),
  CONSTRAINT sr_note_ts CHECK ((kind = 'note') = (ts_column IS NOT NULL))
);
COMMENT ON TABLE iam_v2.scrub_registry IS
  'Columns that hold what staff typed about a guest (note), who acted (label) or a secret (0147), and may be scrubbed by the retention runs and by erasure.';
INSERT INTO iam_v2.scrub_registry (table_name, column_name, kind, ts_column, handled, jsonb_key) VALUES
  ('iam_v2.pms_case_resolutions', 'note', 'note', 'resolved_at', true, NULL),
  ('iam_v2.device_slot_actions', 'reason', 'note', 'acted_at', true, NULL),
  ('iam_v2.post_stay_profiles', 'revoke_reason', 'note', 'revoked_at', true, NULL),
  ('iam_v2.stay_posting_blocks', 'note', 'note', 'created_at', true, NULL),
  ('iam_v2.stay_posting_blocks', 'cleared_reason', 'note', 'cleared_at', true, NULL),
  ('iam_v2.vouchers', 'notes', 'note', 'created_at', true, NULL),
  ('iam_v2.guest_data_erasures', 'erase_reason', 'note', 'erased_at', true, NULL),
  ('iam_v2.device_slot_actions', 'detail', 'note', 'acted_at', false, NULL),
  ('iam_v2.guest_device_actions', 'detail', 'note', 'acted_at', false, NULL),
  ('iam_v2.entitlement_adjustments', 'reason', 'note', 'created_at', false, NULL),
  ('iam_v2.guest_signin_restrictions', 'release_reason', 'note', 'released_at', false, NULL),
  ('iam_v2.guest_signin_device_passes', 'reason', 'note', 'granted_at', false, NULL),
  ('iam_v2.voucher_revocations', 'reason', 'note', 'revoked_at', false, NULL),
  ('iam_v2.voucher_code_reveals', 'reason', 'note', 'revealed_at', false, NULL),
  ('iam_v2.posting_review_actions', 'reason', 'note', 'created_at', false, NULL),
  ('iam_v2.pms_answer_confirmations', 'reason', 'note', 'recorded_at', false, NULL),
  ('iam_v2.pms_answer_confirmations', 'evidence', 'note', 'recorded_at', false, NULL),
  ('iam_v2.pms_case_resolutions', 'evidence_ref', 'note', 'resolved_at', false, NULL),
  ('iam_v2.financial_recovery_holds', 'resolution_note', 'note', 'resolved_at', false, NULL),
  ('iam_v2.card_reversal_reviews', 'resolution_reason', 'note', 'resolved_at', false, NULL),
  ('iam_v2.entitlement_transfers', 'reason', 'note', 'created_at', false, NULL),
  ('iam_v2.pms_roster_reconciliation_runs', 'reason', 'note', 'run_at', false, NULL),
  ('iam_v2.payment_transaction_events', 'detail', 'note', 'received_at', false, 'provider_message'),
  ('iam_v2.account_password_settings_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.accounting_retention_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.appliance_product_setting_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.assisted_guest_request_events', 'actor', 'label', NULL, false, NULL),
  ('iam_v2.assisted_guest_requests', 'created_by', 'label', NULL, false, NULL),
  ('iam_v2.assisted_guest_requests', 'closed_by', 'label', NULL, false, NULL),
  ('iam_v2.card_reversal_reviews', 'resolved_by', 'label', NULL, false, NULL),
  ('iam_v2.client_group_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.data_retention_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.device_settings_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.device_slot_actions', 'operator_label', 'label', NULL, false, NULL),
  ('iam_v2.guest_data_erasures', 'erased_by', 'label', NULL, false, NULL),
  ('iam_v2.guest_network_replacements', 'created_by', 'label', NULL, false, NULL),
  ('iam_v2.guest_signin_device_passes', 'granted_by', 'label', NULL, false, NULL),
  ('iam_v2.guest_signin_network_pauses', 'ended_by', 'label', NULL, false, NULL),
  ('iam_v2.guest_signin_network_policy_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.guest_signin_protection_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.guest_signin_restrictions', 'released_by', 'label', NULL, false, NULL),
  ('iam_v2.otp_code_reveals', 'revealed_by', 'label', NULL, false, NULL),
  ('iam_v2.payment_account_conversion', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.payment_account_conversion_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.payment_provider_account_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.payment_provider_secret_generations', 'created_by', 'label', NULL, false, NULL),
  ('iam_v2.pms_case_resolutions', 'resolved_by', 'label', NULL, false, NULL),
  ('iam_v2.pms_connection_settings_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.pms_reconciliation_settings_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.pms_review_expiry_settings_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.pms_room_inventory_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.pms_roster_reconciliation_runs', 'run_by', 'label', NULL, false, NULL),
  ('iam_v2.self_service_settings_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.site_account_password_settings', 'updated_by', 'label', NULL, false, NULL),
  ('iam_v2.site_accounting_retention', 'updated_by', 'label', NULL, false, NULL),
  ('iam_v2.site_card_payment_setting_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.site_data_retention', 'updated_by', 'label', NULL, false, NULL),
  ('iam_v2.site_device_settings', 'updated_by', 'label', NULL, false, NULL),
  ('iam_v2.site_module_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.site_payment_domain_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.site_self_service_settings', 'updated_by', 'label', NULL, false, NULL),
  ('iam_v2.site_walled_garden_settings', 'updated_by', 'label', NULL, false, NULL),
  ('iam_v2.verification_access_grants', 'cleared_by', 'label', NULL, false, NULL),
  ('iam_v2.voucher_code_reveals', 'operator_label', 'label', NULL, false, NULL),
  ('iam_v2.voucher_code_settings_changes', 'changed_by', 'label', NULL, false, NULL),
  ('iam_v2.walled_garden_settings_changes', 'changed_by', 'label', NULL, false, NULL),
  ('public.appliance_recovery_events', 'actor', 'label', NULL, false, NULL),
  ('public.backup_key_holder_events', 'actor', 'label', NULL, false, NULL),
  ('public.backup_key_holders', 'created_by', 'label', NULL, false, NULL),
  ('public.backup_key_holders', 'removed_by', 'label', NULL, false, NULL),
  ('public.operator_account_retention', 'updated_by', 'label', NULL, false, NULL),
  ('public.operator_account_retention_changes', 'changed_by', 'label', NULL, false, NULL),
  ('public.operator_session_policy', 'updated_by', 'label', NULL, false, NULL),
  ('public.operator_session_policy_changes', 'changed_by', 'label', NULL, false, NULL),
  ('public.operator_signin_protection', 'updated_by', 'label', NULL, false, NULL),
  ('public.operator_signin_protection_changes', 'changed_by', 'label', NULL, false, NULL),
  ('public.portal_https_settings', 'updated_by', 'label', NULL, false, NULL),
  ('public.portal_https_settings_changes', 'changed_by', 'label', NULL, false, NULL),
  ('public.system_network_audit', 'actor', 'label', NULL, false, NULL),
  ('iam_v2.post_stay_profiles', 'pin_hash', 'secret', NULL, false, NULL)
ON CONFLICT (table_name, column_name) DO NOTHING;

CREATE OR REPLACE FUNCTION iam_v2.scrub_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE r record; v_o jsonb := to_jsonb(OLD); v_n jsonb := to_jsonb(NEW);
BEGIN
  IF TG_OP <> 'UPDATE'
     OR current_setting('iam_v2.scrub', true) IS DISTINCT FROM 'on'
     OR (current_user <> (SELECT pg_get_userbyid(c.relowner) FROM pg_class c WHERE c.oid = TG_RELID)
         AND NOT COALESCE((SELECT pr.rolsuper FROM pg_roles pr WHERE pr.rolname = current_user), false)) THEN
    RAISE EXCEPTION '% is append-only: % refused', TG_TABLE_NAME, TG_OP USING ERRCODE = 'restrict_violation';
  END IF;
  FOR r IN SELECT x.column_name, x.jsonb_key FROM iam_v2.scrub_registry x WHERE x.table_name = TG_TABLE_SCHEMA || '.' || TG_TABLE_NAME LOOP
    IF r.jsonb_key IS NULL THEN
      v_o := v_o - r.column_name; v_n := v_n - r.column_name;
    ELSE
      IF v_o -> r.column_name IS NOT NULL AND jsonb_typeof(v_o -> r.column_name) = 'object' THEN
        v_o := jsonb_set(v_o, ARRAY[r.column_name], (v_o -> r.column_name) - r.jsonb_key);
      END IF;
      IF v_n -> r.column_name IS NOT NULL AND jsonb_typeof(v_n -> r.column_name) = 'object' THEN
        v_n := jsonb_set(v_n, ARRAY[r.column_name], (v_n -> r.column_name) - r.jsonb_key);
      END IF;
    END IF;
  END LOOP;
  IF v_o IS DISTINCT FROM v_n THEN
    RAISE EXCEPTION '% may be scrubbed only in its registered columns', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END $$;

-- Every other guard on a registered table stands aside while the scrub is on; our own guard then admits only the
-- scrub. Idempotent: a trigger already carrying the condition is left alone.
DO $inst$
DECLARE t record; trg record; def text;
BEGIN
  FOR t IN SELECT DISTINCT x.table_name FROM iam_v2.scrub_registry x LOOP
    IF to_regclass(t.table_name) IS NULL THEN CONTINUE; END IF;
    FOR trg IN SELECT g.oid, g.tgname FROM pg_trigger g
                WHERE g.tgrelid = to_regclass(t.table_name) AND NOT g.tgisinternal AND g.tgconstraint = 0
                  AND (g.tgtype & 2) = 2 AND (g.tgtype & 24) <> 0 AND g.tgname <> 'zz_scrub_guard' LOOP
      def := pg_get_triggerdef(trg.oid);
      IF def LIKE '%iam_v2.scrub%' THEN CONTINUE; END IF;
      IF def LIKE '% WHEN (%' THEN
        RAISE EXCEPTION 'trigger % on % already has a WHEN condition: wire it into the scrub by hand', trg.tgname, t.table_name;
      END IF;
      EXECUTE format('DROP TRIGGER %I ON %s', trg.tgname, t.table_name);
      EXECUTE replace(def, ' EXECUTE FUNCTION', ' WHEN (current_setting(''iam_v2.scrub'', true) IS DISTINCT FROM ''on'') EXECUTE FUNCTION');
    END LOOP;
    EXECUTE format('DROP TRIGGER IF EXISTS zz_scrub_guard ON %s', t.table_name);
    EXECUTE format('CREATE TRIGGER zz_scrub_guard BEFORE UPDATE OR DELETE ON %s FOR EACH ROW WHEN (current_setting(''iam_v2.scrub'', true) = ''on'') EXECUTE FUNCTION iam_v2.scrub_guard()', t.table_name);
  END LOOP;
END $inst$;

-- A staff member's email, replaced wherever it names them as the person who acted (iam_v2 tables).
CREATE OR REPLACE FUNCTION iam_v2.scrub_staff_label(p_old text, p_new text) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE r record; n integer := 0; k integer;
BEGIN
  IF p_old IS NULL OR btrim(p_old) = '' OR p_new !~ '^removed-[0-9a-f]{12}@staff[.]invalid$' THEN
    RETURN 0;
  END IF;
  PERFORM set_config('iam_v2.scrub', 'on', true);
  FOR r IN SELECT x.table_name, x.column_name FROM iam_v2.scrub_registry x WHERE x.kind = 'label' AND x.table_name LIKE 'iam_v2.%' LOOP
    IF to_regclass(r.table_name) IS NULL THEN CONTINUE; END IF;
    EXECUTE format('UPDATE %s SET %I = $2 WHERE lower(%I) = lower($1)', r.table_name, r.column_name, r.column_name) USING p_old, p_new;
    GET DIAGNOSTICS k = ROW_COUNT; n := n + k;
  END LOOP;
  PERFORM set_config('iam_v2.scrub', '', true);
  RETURN n;
END $$;

-- ... and the public tables (the staff-retention run, which anonymises the account, calls both).
CREATE OR REPLACE FUNCTION public.scrub_staff_label_public(p_old text, p_new text) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE r record; n integer := 0; k integer;
BEGIN
  IF p_old IS NULL OR btrim(p_old) = '' OR p_new !~ '^removed-[0-9a-f]{12}@staff[.]invalid$' THEN
    RETURN 0;
  END IF;
  PERFORM set_config('iam_v2.scrub', 'on', true);
  FOR r IN SELECT x.table_name, x.column_name FROM iam_v2.scrub_registry x WHERE x.kind = 'label' AND x.table_name LIKE 'public.%' LOOP
    IF to_regclass(r.table_name) IS NULL THEN CONTINUE; END IF;
    EXECUTE format('UPDATE %s SET %I = $2 WHERE lower(%I) = lower($1)', r.table_name, r.column_name, r.column_name) USING p_old, p_new;
    GET DIAGNOSTICS k = ROW_COUNT; n := n + k;
  END LOOP;
  PERFORM set_config('iam_v2.scrub', '', true);
  RETURN n;
END $$;

-- Staff free text that follows the staff-notes period (the run of 0143 handled seven of these columns; the rest go
-- the same way), a batch per column per call.
CREATE OR REPLACE FUNCTION iam_v2.scrub_registry_notes(p_tenant uuid, p_site uuid, p_cut timestamptz, p_batch integer, p_placeholder text)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE r record; n integer := 0; k integer;
BEGIN
  PERFORM set_config('iam_v2.scrub', 'on', true);
  FOR r IN SELECT x.* FROM iam_v2.scrub_registry x WHERE x.kind = 'note' AND NOT x.handled LOOP
    IF to_regclass(r.table_name) IS NULL THEN CONTINUE; END IF;
    IF r.jsonb_key IS NULL THEN
      EXECUTE format('UPDATE %1$s x SET %2$I = $1 WHERE x.ctid IN (SELECT y.ctid FROM %1$s y WHERE y.tenant_id = $2 AND y.site_id = $3
                       AND y.%3$I < $4 AND y.%2$I IS NOT NULL AND y.%2$I <> $1 LIMIT $5)', r.table_name, r.column_name, r.ts_column)
        USING p_placeholder, p_tenant, p_site, p_cut, p_batch;
    ELSE
      EXECUTE format('UPDATE %1$s x SET %2$I = jsonb_set(x.%2$I, ARRAY[%4$L], to_jsonb($1::text)) WHERE x.ctid IN (SELECT y.ctid FROM %1$s y
                       WHERE y.tenant_id = $2 AND y.site_id = $3 AND y.%3$I < $4 AND y.%2$I ? %4$L AND y.%2$I ->> %4$L <> $1 LIMIT $5)',
                     r.table_name, r.column_name, r.ts_column, r.jsonb_key)
        USING p_placeholder, p_tenant, p_site, p_cut, p_batch;
    END IF;
    GET DIAGNOSTICS k = ROW_COUNT; n := n + k;
  END LOOP;
  PERFORM set_config('iam_v2.scrub', '', true);
  RETURN n;
END $$;

-- The staff-notes period reaches every registered column
CREATE OR REPLACE FUNCTION iam_v2.run_staff_note_retention(p_tenant uuid, p_site uuid, p_batch integer)
    RETURNS TABLE(o_category text, o_cutoff timestamptz, o_anonymised integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_days integer; v_cut timestamptz; v_n integer := 0; v_k integer;
        c_removed constant text := 'Removed after the staff-notes period';
BEGIN
  SELECT r.staff_note_days INTO v_days FROM iam_v2.data_retention_get(p_tenant, p_site) r;
  v_cut := now() - make_interval(days => v_days);
  PERFORM set_config('iam_v2.anonymise', 'retention', true);

  UPDATE iam_v2.pms_case_resolutions x SET note = c_removed
   WHERE x.id IN (SELECT y.id FROM iam_v2.pms_case_resolutions y
                   WHERE y.tenant_id = p_tenant AND y.site_id = p_site AND y.resolved_at < v_cut
                     AND y.note IS NOT NULL AND y.note <> c_removed
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;

  UPDATE iam_v2.device_slot_actions x SET reason = c_removed
   WHERE x.id IN (SELECT y.id FROM iam_v2.device_slot_actions y
                   WHERE y.tenant_id = p_tenant AND y.site_id = p_site AND y.acted_at < v_cut
                     AND y.reason IS NOT NULL AND y.reason <> c_removed
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;

  UPDATE iam_v2.post_stay_profiles x SET revoke_reason = c_removed
   WHERE x.id IN (SELECT y.id FROM iam_v2.post_stay_profiles y
                   WHERE y.tenant_id = p_tenant AND y.site_id = p_site AND y.status = 'REVOKED'
                     AND y.revoked_at < v_cut AND y.revoke_reason <> c_removed
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;

  UPDATE iam_v2.stay_posting_blocks x SET note = c_removed
   WHERE x.id IN (SELECT y.id FROM iam_v2.stay_posting_blocks y
                   WHERE y.tenant_id = p_tenant AND y.site_id = p_site AND y.created_at < v_cut
                     AND y.note IS NOT NULL AND y.note <> c_removed
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;

  UPDATE iam_v2.stay_posting_blocks x SET cleared_reason = c_removed
   WHERE x.id IN (SELECT y.id FROM iam_v2.stay_posting_blocks y
                   WHERE y.tenant_id = p_tenant AND y.site_id = p_site AND y.cleared_at < v_cut
                     AND y.cleared_reason IS NOT NULL AND y.cleared_reason <> c_removed
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;

  UPDATE iam_v2.vouchers x SET notes = c_removed
   WHERE x.id IN (SELECT y.id FROM iam_v2.vouchers y
                   WHERE y.tenant_id = p_tenant AND y.site_id = p_site AND y.created_at < v_cut
                     AND y.notes IS NOT NULL AND btrim(y.notes) <> '' AND y.notes <> c_removed
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;

  UPDATE iam_v2.guest_data_erasures x SET erase_reason = c_removed
   WHERE x.erasure_ref IN (SELECT y.erasure_ref FROM iam_v2.guest_data_erasures y
                            WHERE y.tenant_id = p_tenant AND y.site_id = p_site AND y.erased_at < v_cut
                              AND y.erase_reason <> c_removed
                            LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;

  v_k := iam_v2.scrub_registry_notes(p_tenant, p_site, v_cut, p_batch, c_removed); v_n := v_n + v_k;

  PERFORM set_config('iam_v2.anonymise', '', true);
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'staff_notes', v_days, v_cut, v_n);
    o_category := 'staff_notes'; o_cutoff := v_cut; o_anonymised := v_n;
    RETURN NEXT;
  END IF;
END $$;

-- A departed staff member's email leaves the records they acted in
CREATE OR REPLACE FUNCTION public.run_staff_retention(p_tenant uuid, p_site uuid)
  RETURNS jsonb
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_days integer; v_audit_days integer; ids uuid[]; n_ops integer; n_audit integer; n_net integer; n_https integer; n_assisted integer;
        rec record; n_labels integer := 0;
BEGIN
  SELECT r.disabled_account_days INTO v_days FROM public.operator_account_retention_get() r;
  SELECT r.audit_log_days INTO v_audit_days FROM iam_v2.data_retention_get(p_tenant, p_site) r;

  SELECT COALESCE(array_agg(o.id), '{}') INTO ids FROM (
    SELECT o.id FROM public.operators o
     WHERE o.status = 'disabled' AND o.anonymised_at IS NULL
       AND o.disabled_at < now() - make_interval(days => v_days)
     ORDER BY o.disabled_at LIMIT 500 FOR UPDATE) o;
  -- Before the email goes, it is taken out of every record that names the person as the one who acted.
  FOR rec IN SELECT o.id, o.email FROM public.operators o WHERE o.id = ANY (ids) LOOP
    n_labels := n_labels + iam_v2.scrub_staff_label(rec.email, 'removed-' || left(replace(rec.id::text, '-', ''), 12) || '@staff.invalid')
                         + public.scrub_staff_label_public(rec.email, 'removed-' || left(replace(rec.id::text, '-', ''), 12) || '@staff.invalid');
  END LOOP;
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
   WHERE x.id IN (SELECT y.id FROM public.system_network_audit y
                   WHERE y.source_ip IS NOT NULL AND y.created_at < now() - make_interval(days => v_audit_days)
                   LIMIT 5000);
  GET DIAGNOSTICS n_net = ROW_COUNT;
  PERFORM set_config('public.staff_retention', 'on', true);
  UPDATE public.portal_https_settings_changes x SET source_ip = NULL
   WHERE x.id IN (SELECT y.id FROM public.portal_https_settings_changes y
                   WHERE y.source_ip IS NOT NULL AND y.changed_at < now() - make_interval(days => v_audit_days)
                   LIMIT 5000);
  GET DIAGNOSTICS n_https = ROW_COUNT;
  PERFORM set_config('public.staff_retention', '', true);

  -- An assisted guest data request left open for 30 days is cancelled and scrubbed (0146).
  BEGIN
    n_assisted := iam_v2.expire_assisted_requests(p_tenant, p_site);
  EXCEPTION WHEN OTHERS THEN
    n_assisted := 0;   -- reported by the next run; the staff accounts above are already anonymised
  END;

  RETURN jsonb_build_object('staff_accounts_anonymised', n_ops, 'audit_entries', n_audit,
                            'staff_addresses_cleared', n_net + n_https, 'assisted_requests_expired', n_assisted,
                            'staff_labels_scrubbed', n_labels);
END $fn$;

-- Audit entries about a guest follow the guest's periods
CREATE OR REPLACE FUNCTION public.run_data_retention_public(p_tenant uuid, p_site uuid, p_batch integer)
    RETURNS TABLE(o_category text, o_cutoff timestamptz, o_anonymised integer)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE r record; v_cut timestamptz; v_n integer; v_k integer; v_from timestamptz; v_to timestamptz;
BEGIN
  IF p_batch IS NULL OR p_batch NOT BETWEEN 1 AND 10000 THEN
    RAISE EXCEPTION 'a batch of 1-10000 is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO r FROM iam_v2.data_retention_get(p_tenant, p_site);

  v_cut := now() - make_interval(days => r.expired_record_days); v_n := 0;
  UPDATE public.auth_otps o SET destination = 'anonymised', ip = NULL, user_agent = NULL
   WHERE o.id IN (SELECT x.id FROM public.auth_otps x
                   WHERE x.tenant_id = p_tenant AND x.expires_at < v_cut
                     AND (x.destination <> 'anonymised' OR x.ip IS NOT NULL OR x.user_agent IS NOT NULL)
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  UPDATE public.social_oauth_states s
     SET client_ip = NULL, client_mac = NULL, user_agent = NULL, code_verifier = NULL, nonce = NULL
   WHERE s.state IN (SELECT x.state FROM public.social_oauth_states x
                      WHERE x.tenant_id = p_tenant AND x.expires_at < v_cut
                        AND num_nonnulls(x.client_ip, x.client_mac, x.user_agent, x.code_verifier, x.nonce) > 0
                      LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  UPDATE public.pms_attempts a SET room_number = 'anonymised', ip = NULL
   WHERE a.id IN (SELECT x.id FROM public.pms_attempts x
                   WHERE x.tenant_id = p_tenant AND x.attempted_at < v_cut
                     AND (x.room_number <> 'anonymised' OR x.ip IS NOT NULL)
                   LIMIT p_batch);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_n := v_n + v_k;
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'expired_records', r.expired_record_days, v_cut, v_n);
    o_category := 'expired_records'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;

  -- AUDIT LOG: oldest first, at most seven days of rows per call. What happened, when, and which operator did
  -- it stay; addresses, user agents, targets, payloads and guest actor ids go.
  v_cut := now() - make_interval(days => r.audit_log_days);
  SELECT k.done_until INTO v_from FROM iam_v2.data_retention_checkpoints k
   WHERE k.tenant_id = p_tenant AND k.site_id = p_site AND k.category = 'audit_log';
  IF v_from IS NULL THEN
    SELECT min(l.ts) INTO v_from FROM public.audit_log l;
  END IF;
  IF v_from IS NOT NULL AND v_from < v_cut THEN
    v_to := LEAST(v_from + interval '7 days', v_cut);
    UPDATE public.audit_log l
       SET ip = NULL, user_agent = NULL, target_id = NULL, payload = '{"anonymised": true}'::jsonb,
           actor_id = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.actor_id END
     WHERE l.ts >= v_from AND l.ts < v_to
       AND (l.tenant_id = p_tenant OR l.tenant_id IS NULL)
       AND l.payload IS DISTINCT FROM '{"anonymised": true}'::jsonb;
    GET DIAGNOSTICS v_n = ROW_COUNT;
    INSERT INTO iam_v2.data_retention_checkpoints (tenant_id, site_id, category, done_until)
    VALUES (p_tenant, p_site, 'audit_log', v_to)
    ON CONFLICT (tenant_id, site_id, category) DO UPDATE SET done_until = EXCLUDED.done_until;
    IF v_n > 0 THEN
      INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
      VALUES (p_tenant, p_site, 'audit_log', r.audit_log_days, v_cut, v_n);
      o_category := 'audit_log'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
    END IF;
  END IF;
  -- ENTRIES ABOUT A GUEST go with the guest, not with the audit log's own (longer) period: an entry that names a client
  -- account, a device address, a session address or a stay, and what a staff member searched for, loses its target and
  -- its text once the longest of the guest-data periods has passed. What happened, when, and by which operator stay.
  v_cut := now() - make_interval(days => GREATEST(r.pms_stay_days, r.client_activity_days, r.inactive_identity_days));
  WITH cand AS (
    SELECT l.tableoid AS toid, l.ctid AS rid FROM public.audit_log l
     WHERE (l.tenant_id = p_tenant OR l.tenant_id IS NULL) AND l.ts < v_cut
       AND l.target_type IN ('guest_account', 'client_account', 'client_accounts', 'guest_device', 'device', 'devices', 'device_mac',
                              'session', 'sessions', 'stay', 'stays', 'stay_events', 'post_stay_profile', 'sign_in_attempt',
                              'sign_in_attempts', 'otp_challenge', 'guest_data', 'usage_stays', 'usage_sources', 'usage_source',
                              'dhcp_leases', 'pms_stay')
       AND (l.payload IS DISTINCT FROM '{"anonymised": true}'::jsonb OR l.target_id IS NOT NULL)
     LIMIT p_batch)
  UPDATE public.audit_log l
     SET ip = NULL, user_agent = NULL, target_id = NULL, payload = '{"anonymised": true}'::jsonb,
         actor_id = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.actor_id END
    FROM cand WHERE l.tableoid = cand.toid AND l.ctid = cand.rid;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  IF v_n > 0 THEN
    INSERT INTO iam_v2.data_retention_runs (tenant_id, site_id, category, retention_days, cutoff, rows_anonymised)
    VALUES (p_tenant, p_site, 'audit_log', GREATEST(r.pms_stay_days, r.client_activity_days, r.inactive_identity_days), v_cut, v_n);
    o_category := 'audit_log'; o_cutoff := v_cut; o_anonymised := v_n; RETURN NEXT;
  END IF;
END $$;

-- The lifecycle guard admits the anonymising write that coarsens times to the day
CREATE OR REPLACE FUNCTION iam_v2.p3_stay_lifecycle_guard() RETURNS trigger
  LANGUAGE plpgsql
  SET search_path = iam_v2, pg_temp
  AS $fn$
DECLARE allowed boolean; is_reinstate boolean; evidence_changed boolean;
BEGIN
  -- ANONYMISING A FINISHED STAY (0147) may coarsen the checkout boundary and the occupancy evidence time to the day, and
  -- nothing else about the lifecycle: the boundary stays inside its day, the status, versions and evidence identity do not
  -- move. Only the table's owner, inside the anonymising definer functions (the flag is transaction-local).
  IF current_setting('iam_v2.anonymise', true) = 'retention'
     AND current_user = (SELECT pg_get_userbyid(c.relowner) FROM pg_class c WHERE c.oid = TG_RELID)
     AND OLD.status IN ('CHECKED_OUT', 'POST_STAY_ACTIVE', 'CANCELLED', 'NO_SHOW')
     AND NEW.status = OLD.status AND NEW.lifecycle_version = OLD.lifecycle_version
     AND NEW.last_applied_event_version = OLD.last_applied_event_version
     AND NEW.occupancy_evidence_version = OLD.occupancy_evidence_version
     AND NEW.occupancy_revision_id IS NOT DISTINCT FROM OLD.occupancy_revision_id
     AND NEW.occupancy_normalization_version IS NOT DISTINCT FROM OLD.occupancy_normalization_version
     AND NEW.occupancy_clock_suspect IS NOT DISTINCT FROM OLD.occupancy_clock_suspect
     AND (NEW.effective_checkout_at IS NOT DISTINCT FROM OLD.effective_checkout_at
          OR NEW.effective_checkout_at = date_trunc('day', OLD.effective_checkout_at))
     AND (NEW.occupancy_evidence_at IS NOT DISTINCT FROM OLD.occupancy_evidence_at
          OR NEW.occupancy_evidence_at = date_trunc('day', OLD.occupancy_evidence_at)) THEN
    RETURN NEW;
  END IF;
  IF NEW.last_applied_event_version < OLD.last_applied_event_version THEN
    RAISE EXCEPTION 'stays.last_applied_event_version cannot decrease (% -> %)',
      OLD.last_applied_event_version, NEW.last_applied_event_version;
  END IF;

  IF NEW.occupancy_evidence_version < OLD.occupancy_evidence_version THEN
    RAISE EXCEPTION 'stays.occupancy_evidence_version cannot decrease (% -> %)',
      OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
  END IF;
  evidence_changed := (
       NEW.occupancy_evidence_at          IS DISTINCT FROM OLD.occupancy_evidence_at
    OR NEW.occupancy_revision_id          IS DISTINCT FROM OLD.occupancy_revision_id
    OR NEW.occupancy_normalization_version IS DISTINCT FROM OLD.occupancy_normalization_version
    OR NEW.occupancy_clock_suspect        IS DISTINCT FROM OLD.occupancy_clock_suspect);
  IF evidence_changed THEN
    IF NEW.occupancy_evidence_version <> OLD.occupancy_evidence_version + 1 THEN
      RAISE EXCEPTION 'a material occupancy-evidence change must increment occupancy_evidence_version by exactly 1 (% -> %)',
        OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
    END IF;
  ELSE
    IF NEW.occupancy_evidence_version <> OLD.occupancy_evidence_version THEN
      RAISE EXCEPTION 'stays.occupancy_evidence_version may not change without a material occupancy-evidence change (% -> %)',
        OLD.occupancy_evidence_version, NEW.occupancy_evidence_version;
    END IF;
  END IF;

  is_reinstate := (OLD.status = 'CHECKED_OUT' AND NEW.status = 'IN_HOUSE');

  IF NEW.effective_checkout_at IS DISTINCT FROM OLD.effective_checkout_at THEN
    IF is_reinstate THEN
      IF NEW.effective_checkout_at IS NOT NULL THEN
        RAISE EXCEPTION 'reinstatement must CLEAR effective_checkout_at (starts a new episode)';
      END IF;
    ELSIF OLD.status = 'IN_HOUSE' AND NEW.status = 'CHECKED_OUT' THEN
      IF NEW.effective_checkout_at IS NULL THEN
        RAISE EXCEPTION 'checkout must SET effective_checkout_at';
      END IF;
    ELSE
      RAISE EXCEPTION 'effective_checkout_at is immutable within an episode (% -> %, status % -> %)',
        OLD.effective_checkout_at, NEW.effective_checkout_at, OLD.status, NEW.status;
    END IF;
  END IF;

  IF NEW.lifecycle_version <> OLD.lifecycle_version THEN
    IF NOT (NEW.lifecycle_version = OLD.lifecycle_version + 1 AND is_reinstate) THEN
      RAISE EXCEPTION 'stays.lifecycle_version may increment by exactly 1 ONLY during a CHECKED_OUT->IN_HOUSE reinstatement (% -> %, % -> %)',
        OLD.lifecycle_version, NEW.lifecycle_version, OLD.status, NEW.status;
    END IF;
  END IF;

  IF NEW.status <> OLD.status THEN
    allowed := CASE
      WHEN OLD.status='RESERVED'    AND NEW.status IN ('IN_HOUSE','CANCELLED','NO_SHOW') THEN true
      WHEN OLD.status='IN_HOUSE'    AND NEW.status IN ('CHECKED_OUT')                    THEN true
      WHEN OLD.status='CHECKED_OUT' AND NEW.status IN ('IN_HOUSE')                       THEN true  -- reinstatement
      -- PHASE 5: the post-stay conversion, and the only arm this migration adds. It stays within the episode
      -- (lifecycle_version unchanged, enforced above) and keeps the immutable checkout boundary (unchanged,
      -- enforced above), so a post-stay Stay is still the same episode that checked out.
      WHEN OLD.status='CHECKED_OUT' AND NEW.status IN ('POST_STAY_ACTIVE')               THEN true
      ELSE false END;
    IF NOT allowed THEN
      RAISE EXCEPTION 'illegal stays.status transition % -> %', OLD.status, NEW.status;
    END IF;
    IF is_reinstate AND NEW.lifecycle_version <> OLD.lifecycle_version + 1 THEN
      RAISE EXCEPTION 'reinstatement (CHECKED_OUT->IN_HOUSE) must increment lifecycle_version exactly once';
    END IF;
  END IF;
  RETURN NEW;
END $fn$;

-- The export gives the person everything attributable to them, and says what is kept by law
CREATE OR REPLACE FUNCTION iam_v2.guest_data_export_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS jsonb
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; ev uuid[]; macs macaddr[]; se_ids uuid[]; pu_ids uuid[];
        ss uuid[]; gr uuid[]; v_who jsonb; v_ids text[]; n_other integer; n_dshared integer; room uuid[]; n_room_dev integer;
        ec uuid[]; ch uuid[]; v_holder jsonb;
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
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_guest'), '{}'),
         count(*) FILTER (WHERE o_kind = 'stay_shared_other'),
         count(*) FILTER (WHERE o_kind = 'device_shared'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'access_shared'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'entitlement_credential'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'challenge'), '{}')
    INTO a, p, s, e, d, ev, ss, gr, n_other, n_dshared, room, ec, ch FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
  SELECT COALESCE(array_agg(dv.mac), '{}') INTO macs FROM devices dv WHERE dv.id = ANY (d);
  -- The reservation's own guests: used to keep other people's names out of the PMS messages.
  v_holder := stay_guests_as_json(ARRAY(SELECT sg.id FROM stay_guests sg WHERE sg.stay_id = ANY (s)));
  -- How many devices the room's access has seen: a number, never a device.
  SELECT count(DISTINCT z.dev) INTO n_room_dev FROM (
    SELECT ed.device_id AS dev FROM entitlement_devices ed WHERE ed.entitlement_id = ANY (room)
    UNION SELECT se.device_id FROM sessions se WHERE se.entitlement_id = ANY (room)) z;
  -- The guest, as named on their own stays and on the shared stays: their names are kept in the events, nobody
  -- else's.
  v_who := stay_guests_as_json(gr || ARRAY(SELECT sg.id FROM stay_guests sg WHERE sg.stay_id = ANY (s)));
  SELECT COALESCE(array_agg(sg.external_guest_id) FILTER (WHERE sg.external_guest_id IS NOT NULL), '{}') INTO v_ids
    FROM stay_guests sg WHERE sg.id = ANY (gr);
  -- Sessions on the person's exclusive devices only; a session on a shared device could be anyone's.
  SELECT COALESCE(array_agg(se.id), '{}') INTO se_ids FROM sessions se
   WHERE se.tenant_id = p_tenant AND se.device_id = ANY (d);
  SELECT COALESCE(array_agg(DISTINCT pu.id), '{}') INTO pu_ids FROM purchases pu
   WHERE pu.tenant_id = p_tenant AND pu.site_id = p_site
     AND pu.id IN (SELECT x.purchase_id FROM entitlements x WHERE x.id = ANY (e));

  RETURN jsonb_build_object(
    'client_accounts', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'username', x.username, 'display_name', x.display_name, 'notes', x.notes, 'enabled', x.enabled,
        'self_registered', x.self_registered, 'linked_to_a_reservation', x.stay_id IS NOT NULL,
        'created_at', x.created_at, 'valid_from', x.valid_from,
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
         WHERE (k.from_stay = x.id OR k.to_stay = x.id) AND (o.id = ANY (s) OR o.id = ANY (ss)))) ORDER BY x.arrival), '[]'::jsonb)
      FROM stays x WHERE x.id = ANY (s) OR x.id = ANY (ss)),
    'pms_events', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'type', x.event_type, 'received_at', x.received_at, 'pms_time', x.pms_timestamp_utc,
        'status', x.processing_status,
        'details', CASE
            WHEN cardinality(gr) > 0 THEN event_payload_for_guests(x.payload, v_who, true, x.pms_interface_id)
            -- Even a reservation with one named guest may list sharers in a PMS message; only the guest's own entry
            -- (by number, or a unique full name) goes to them.
            WHEN jsonb_typeof(x.payload->'sharers') = 'array' THEN
              (x.payload - 'sharers') || jsonb_build_object('sharers', COALESCE(
                 event_payload_for_guests(jsonb_build_object('sharers', x.payload->'sharers'), v_holder, true, x.pms_interface_id)->'sharers', '[]'::jsonb))
            ELSE x.payload END)
        ORDER BY x.received_at), '[]'::jsonb)
      FROM stay_events x WHERE x.id = ANY (ev)),
    'post_stay_access', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'status', x.status, 'issued_at', x.issued_at, 'issued_via', x.issued_via, 'valid_until', x.valid_until,
        'pin_set_at', x.pin_set_at, 'revoked_at', x.revoked_at, 'revoke_reason', x.revoke_reason) ORDER BY x.created_at), '[]'::jsonb)
      FROM post_stay_profiles x WHERE x.origin_stay_id = ANY (s)),
    'internet_access', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'status', x.status, 'activated_at', x.activated_at, 'ends_at', x.window_ends_at,
        'ended_at', x.terminated_at, 'ended_because', x.terminal_reason,
        'data_used_bytes', x.consumed_data_bytes, 'online_seconds', x.consumed_online_seconds)
        ORDER BY x.activated_at NULLS LAST), '[]'::jsonb)
      FROM entitlements x WHERE x.id = ANY (e) OR x.id = ANY (ec)),
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
    -- The folio of the reservation: the reservation holder's, so its PMS guest number is shown only to a request
    -- about the reservation. A posting made for the person's own purchase carries no guest number.
    'room_charges', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'type', x.posting_type, 'amount_minor', x.amount_minor, 'currency', x.currency,
        'currency_exponent', x.currency_exponent,
        'pms_guest_number', CASE WHEN x.stay_id = ANY (s) OR (x.stay_id = ANY (ss) AND x.g_number = ANY (v_ids)) THEN x.g_number END,
        'created_at', x.created_at)
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
    -- What was tried with the person's own access or from their exclusive device. Attempts to enter a room's
    -- credentials are the typist's, not the reservation holder's, and are never exported with the reservation.
    'sign_in_attempts', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'at', sa.occurred_at, 'room_entered', sa.submitted_room, 'checked_against', sa.verifier_kind,
        'result', sa.result, 'guest_network', sa.guest_network_name, 'device_ip', sa.device_ip,
        'device_mac_address', sa.device_mac) ORDER BY sa.occurred_at), '[]'::jsonb)
      FROM sign_in_attempts sa
     WHERE sa.tenant_id = p_tenant AND sa.site_id = p_site
       AND (sa.entitlement_id = ANY (e) OR sa.device_mac = ANY (macs))),
    'sign_in_restrictions', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'device_mac_address', x.device_mac, 'guest_network', x.guest_network_name, 'room_entered', x.last_submitted_room,
        'restricted_at', x.restricted_at, 'until', x.expires_at, 'failures', x.failure_count, 'reason', x.reason,
        'released_at', x.released_at, 'release_reason', x.release_reason) ORDER BY x.restricted_at), '[]'::jsonb)
      FROM guest_signin_restrictions x
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs)),
    -- A verification access and a code binding are tied to a person by the address their one-time code went to,
    -- not by the device (0145).
    'verification_access', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'device_mac_address', x.device_mac, 'ip', x.ip, 'guest_network', x.guest_network_id,
        'started_at', x.started_at, 'expires_at', x.expires_at, 'ended_at', x.ended_at, 'end_reason', x.end_reason,
        'used_bytes', x.used_bytes) ORDER BY x.started_at), '[]'::jsonb)
      FROM verification_access_grants x
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.challenge_id = ANY (ch)),
    'code_device_bindings', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'device_mac_address', x.device_mac, 'issued_at', x.issued_at) ORDER BY x.issued_at), '[]'::jsonb)
      FROM otp_device_bindings x WHERE x.tenant_id = p_tenant AND x.challenge_id = ANY (ch)),
    -- A staff "let this device try again" pass: keyed by the device, so only an exclusive device's.
    'sign_in_passes', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'device_mac_address', x.device_mac, 'granted_at', x.granted_at) ORDER BY x.granted_at), '[]'::jsonb)
      FROM guest_signin_device_passes x WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs)),
    -- What staff did with the person's access, and what the person did with their devices (the staff member's name is not given).
    'access_adjustments', (SELECT COALESCE(jsonb_agg(jsonb_build_object('field', x.field, 'from', x.old_value, 'to', x.new_value,
        'reason', x.reason, 'at', x.created_at) ORDER BY x.created_at), '[]'::jsonb)
      FROM entitlement_adjustments x WHERE x.entitlement_id = ANY (e)),
    'access_history', (SELECT COALESCE(jsonb_agg(jsonb_build_object('from', x.from_state, 'to', x.to_state, 'effective_at', x.effective_at,
        'reason', x.reason) ORDER BY x.seq), '[]'::jsonb)
      FROM entitlement_state_transitions x WHERE x.entitlement_id = ANY (e)),
    'device_actions', (SELECT COALESCE(jsonb_agg(z.o ORDER BY z.at), '[]'::jsonb) FROM (
        SELECT x.acted_at AS at, jsonb_build_object('by', x.actor, 'action', x.action, 'outcome', x.outcome, 'reason', x.reason,
                 'detail', x.detail, 'at', x.acted_at) AS o
          FROM device_slot_actions x WHERE x.entitlement_id = ANY (e)
        UNION ALL
        SELECT x.acted_at, jsonb_build_object('by', 'the guest', 'action', x.action, 'outcome', x.outcome, 'detail', x.detail, 'at', x.acted_at)
          FROM guest_device_actions x WHERE x.entitlement_id = ANY (e)) z),
    'device_authorisations', (SELECT COALESCE(jsonb_agg(jsonb_build_object('authorised_at', x.authorized_at, 'ended_at', x.deauthorized_at,
        'device_mac_address', dv.mac) ORDER BY x.authorized_at), '[]'::jsonb)
      FROM entitlement_device_authorizations x LEFT JOIN devices dv ON dv.id = x.device_id AND dv.id = ANY (d)
     WHERE x.entitlement_id = ANY (e)),
    'sign_in_events', (SELECT COALESCE(jsonb_agg(jsonb_build_object('method', ac.method, 'guest_network', ac.guest_network_id,
        'valid_until', ac.expires_at, 'used_at', ac.consumed_at, 'client_group', ac.client_group_id) ORDER BY ac.expires_at), '[]'::jsonb)
      FROM auth_contexts ac
     WHERE ac.tenant_id = p_tenant AND ac.site_id = p_site
       AND (ac.guest_principal_id = ANY (p)
            OR ac.guest_account_id IN (SELECT g.id FROM guest_access_accounts g WHERE g.id = ANY (a) AND g.self_registered))),
    'account_messages', (SELECT COALESCE(jsonb_agg(jsonb_build_object('purpose', x.purpose, 'created_at', x.created_at, 'sent_at', x.sent_at,
        'used_at', x.used_at, 'send_attempts', x.send_attempts) ORDER BY x.created_at), '[]'::jsonb)
      FROM account_email_tokens x WHERE x.account_id = ANY (a)),
    'account_security', (SELECT COALESCE(jsonb_agg(jsonb_build_object('failed_sign_ins', x.failed_attempts, 'locked_until', x.locked_until,
        'grace_until', x.grace_until, 'grace_extensions_used', x.grace_extensions_used,
        'created_on_device_mac_address', x.created_by_device_mac) ORDER BY x.created_at), '[]'::jsonb)
      FROM guest_access_accounts x WHERE x.id = ANY (a)),
    -- Staff notes about the reservation, and the postings' PMS numbers (kept by law, see kept_by_law).
    'staff_notes_about_the_reservation', (SELECT COALESCE(jsonb_agg(z.o ORDER BY z.at), '[]'::jsonb) FROM (
        SELECT x.created_at AS at, jsonb_build_object('kind', 'posting block', 'reason', x.reason, 'note', x.note, 'cleared_reason', x.cleared_reason,
                 'at', x.created_at) AS o FROM stay_posting_blocks x WHERE x.stay_id = ANY (s)
        UNION ALL
        SELECT x.resolved_at, jsonb_build_object('kind', 'PMS case', 'disposition', x.disposition, 'note', x.note, 'at', x.resolved_at)
          FROM pms_case_resolutions x WHERE x.stay_id = ANY (s)) z),
    'posting_attempts', (SELECT COALESCE(jsonb_agg(jsonb_build_object('posting_number', x.rn, 'pms_guest_number', x.g_number, 'sent_at', x.sent_at,
        'outcome', x.outcome) ORDER BY x.sent_at), '[]'::jsonb)
      FROM posting_attempts x JOIN pms_postings pp ON pp.id = x.internal_posting_id
     WHERE pp.tenant_id = p_tenant AND (pp.purchase_id = ANY (pu_ids) OR pp.stay_id = ANY (s))),
    'other_reservations_with_your_pms_guest_number', (SELECT COALESCE(jsonb_agg(jsonb_build_object('reservation', st2.external_reservation_id,
        'arrival', st2.arrival, 'departure', st2.departure, 'status', st2.status) ORDER BY st2.arrival), '[]'::jsonb)
      FROM stays st2
     WHERE st2.tenant_id = p_tenant AND st2.site_id = p_site AND NOT st2.id = ANY (s) AND NOT st2.id = ANY (ss)
       AND NOT is_anon_token(st2.external_reservation_id)
       AND EXISTS (SELECT 1 FROM stay_guests g2 JOIN stay_guests g1 ON g1.external_guest_id = g2.external_guest_id AND g1.pms_interface_id = g2.pms_interface_id
                    WHERE g2.stay_id = st2.id AND g2.external_guest_id IS NOT NULL
                      AND (g1.stay_id = ANY (s) OR g1.id = ANY (gr)))),
    'static_address_reservations_kept', (SELECT count(*) FROM public.dhcp_reservations r WHERE r.mac = ANY (macs)),
    'kept_by_law', jsonb_build_object(
        'what', 'Room charges, payments and the PMS posting records of this guest are kept for the legal period for financial records, and are given above.',
        'what_they_still_carry', 'The PMS guest number on each posting, the posting numbers, the payment provider references, the amounts and the dates.',
        'why', 'The hotel must keep its financial records (accounting and tax law). Erasing a guest anonymises their account, reservation, devices and usage; it does not remove these.'),
    'names_before_normalisation', CASE WHEN to_regclass('iam_v2.backfill_0066_identity_text') IS NULL THEN '[]'::jsonb
      ELSE (SELECT COALESCE(jsonb_agg(jsonb_build_object(
              'first_name', b.prior_first_name, 'last_name', b.prior_last_name, 'room', b.prior_room,
              'recorded_at', b.captured_at) ORDER BY b.captured_at), '[]'::jsonb)
            FROM backfill_0066_identity_text b
           WHERE b.tenant_id = p_tenant AND b.site_id = p_site
             AND num_nonnulls(b.prior_first_name, b.prior_last_name, b.prior_room) > 0
             AND (b.row_id = ANY (gr) OR b.row_id = ANY (s)
                  OR b.row_id IN (SELECT sg.id FROM stay_guests sg WHERE sg.stay_id = ANY (s)))) END)
  || CASE WHEN cardinality(ss) > 0 OR n_other > 0 THEN jsonb_build_object('shared_stays_note',
       'Other guests are named on a stay linked to this guest. Only this guest''s own details are included: the '
       || 'room''s internet access, devices and sessions are shared by everyone on the stay and are not included.')
     ELSE '{}'::jsonb END
  || CASE WHEN cardinality(room) > 0 OR n_dshared > 0 THEN jsonb_build_object('not_included', jsonb_build_object(
       'why', 'Internet access used through a room, voucher or staff-issued credential, and any device also used '
              || 'with someone else, is shared by several people. It cannot be attributed to one person, so it is '
              || 'not included here and this system never discloses or changes it for one person. The hotel '
              || 'answers a request about it separately, by its own manual and legal process; the end of a '
              || 'retention period is not an answer to such a request.',
       'room_access_records', cardinality(room), 'devices_seen_by_that_access', n_room_dev, 'shared_devices', n_dshared))
     ELSE '{}'::jsonb END;
END $function$;

-- The activity log is exported by the rule the erasure rewrites it by
CREATE OR REPLACE FUNCTION public.guest_data_export(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS jsonb
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v jsonb; v_ids text[]; v_vals text[]; v_macs text[];
        a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; v_ctx uuid[]; v_strong text[]; v_pats text[];
BEGIN
  v := iam_v2.guest_data_export_records(p_tenant, p_site, p_kind, p_id);
  SELECT COALESCE(array_agg(o_value) FILTER (WHERE o_kind = 'strong'), '{}') INTO v_strong FROM iam_v2.guest_data_identifiers(p_tenant, p_site, p_kind, p_id);
  v_pats := ARRAY(SELECT '%' || replace(replace(replace(x, '\', '\\'), '%', '\%'), '_', '\_') || '%' FROM unnest(v_strong) x WHERE length(x) >= 4);
  SELECT COALESCE(array_agg(DISTINCT o_id::text) FILTER (
             WHERE o_kind NOT IN ('stay_shared', 'stay_shared_other', 'access_shared', 'device_shared', 'challenge')
               AND NOT (o_kind = 'stay_event' AND EXISTS (SELECT 1 FROM iam_v2.stay_events e
                                                         WHERE e.id = o_id AND iam_v2.stay_named_guests(e.stay_id) > 1))), '{}'),
         COALESCE(array_agg(DISTINCT lower(o_value)) FILTER (WHERE o_kind IN ('account', 'identity') AND o_value IS NOT NULL), '{}'),
         COALESCE(array_agg(DISTINCT lower(o_value)) FILTER (WHERE o_kind = 'device' AND o_value IS NOT NULL), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'account'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind IN ('principal', 'identity')), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'entitlement'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'device'), '{}')
    INTO v_ids, v_vals, v_macs, a, p, s, e, d FROM iam_v2.guest_data_scope(p_tenant, p_site, p_kind, p_id);
  -- The person's sign-ins: by their own account or identity, from a device only they used, or the one that paid
  -- for access they held. A sign-in with a room's credentials is not attributable to the reservation holder
  -- (0145), and a shared device is not in d, so nobody else's sign-in is exported.
  SELECT COALESCE(array_agg(ac.id), '{}') INTO v_ctx FROM iam_v2.auth_contexts ac
   WHERE ac.tenant_id = p_tenant AND ac.site_id = p_site
     AND (ac.guest_account_id IN (SELECT x.id FROM iam_v2.guest_access_accounts x WHERE x.id = ANY (a) AND x.self_registered)
          OR ac.guest_principal_id = ANY (p)
          OR ac.device_id = ANY (d)
          OR ac.id IN (SELECT pu.auth_context_id FROM iam_v2.purchases pu
                         JOIN iam_v2.entitlements en ON en.purchase_id = pu.id WHERE en.id = ANY (e)));
  RETURN jsonb_build_object('format', 'stayconnect.guest-data-export/2', 'exported_at', now()) || v || jsonb_build_object(
    'verification_codes', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'channel', o.channel, 'sent_to', o.destination, 'sent_at', o.issued_at, 'used_at', o.consumed_at,
        'requested_from_ip', o.ip, 'requested_from_browser', o.user_agent)
        ORDER BY o.issued_at), '[]'::jsonb)
      FROM auth_otps o WHERE o.tenant_id = p_tenant AND lower(o.destination) = ANY (v_vals)),
    'sign_in_redirects', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'provider', x.provider, 'from_ip', x.client_ip, 'device_mac_address', x.client_mac, 'browser', x.user_agent,
        'started_at', x.created_at, 'completed_at', x.consumed_at) ORDER BY x.created_at), '[]'::jsonb)
      FROM social_oauth_states x WHERE x.tenant_id = p_tenant AND x.client_mac::text = ANY (v_macs)),
    'activity_log', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'at', l.ts, 'action', l.action, 'by', l.actor_type, 'details', l.payload) ORDER BY l.ts), '[]'::jsonb)
      FROM audit_log l WHERE l.tenant_id = p_tenant
       -- The same entries the erasure rewrites: by the record's id, by an address, a username or a number of theirs, as the
       -- target or the actor, or named in the entry.
       AND (l.target_id = ANY (v_ids) OR lower(l.target_id) = ANY (v_strong) OR lower(l.actor_id) = ANY (v_strong)
            OR lower(l.payload::text) LIKE ANY (v_pats))),
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

-- The record of the notice shown says the built-in notice when the hotel has none
CREATE OR REPLACE FUNCTION public.privacy_notice_in_force(p_tenant uuid) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE b jsonb; d jsonb; v_text text; v_url text; v_terms text; v_fp text; v_id uuid;
        c_builtin constant text := '[built-in notice] This Wi-Fi is provided by the hotel, which is responsible for your personal data on it. To give you internet access and keep the network secure, it records your device''s network addresses, when you connect and how much data you use, and what you sign in with. These records are deleted or made anonymous after a set time. Ask at reception for the hotel''s full privacy notice, or to see, correct or delete your data. (Shown in the guest''s language.)';
BEGIN
  SELECT t.branding INTO b FROM public.tenants t WHERE t.id = p_tenant;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;
  d := CASE WHEN jsonb_typeof(b) = 'object' AND b ? 'design' THEN b -> 'design' ELSE b END;
  IF jsonb_typeof(d) = 'object' THEN
    v_text  := NULLIF(CASE WHEN jsonb_typeof(d -> 'privacy_text') = 'string' THEN d ->> 'privacy_text' END, '');
    v_url   := NULLIF(CASE WHEN jsonb_typeof(d -> 'privacy_url') = 'string' THEN d ->> 'privacy_url' END, '');
    v_terms := NULLIF(CASE WHEN jsonb_typeof(d -> 'terms_url') = 'string' THEN d ->> 'terms_url' END, '');
  END IF;
  -- With no notice of its own the portal shows its short built-in one (privacy.default): that is what the guest was shown,
  -- and what is recorded -- not "nothing".
  IF v_text IS NULL THEN
    v_text := c_builtin;
  END IF;
  v_fp := encode(sha256(convert_to(jsonb_build_array(v_text, v_url, v_terms)::text, 'UTF8')), 'hex');

  INSERT INTO public.privacy_notice_versions (tenant_id, fingerprint, privacy_text, privacy_url, terms_url)
  VALUES (p_tenant, v_fp, v_text, v_url, v_terms)
  ON CONFLICT ON CONSTRAINT pnv_one_per_content DO NOTHING
  RETURNING id INTO v_id;
  IF v_id IS NULL THEN
    SELECT x.id INTO v_id FROM public.privacy_notice_versions x WHERE x.tenant_id = p_tenant AND x.fingerprint = v_fp;
  END IF;
  RETURN v_id;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 6. The request register: every request a guest or visitor made, when it is due, and how it was answered.
-- ---------------------------------------------------------------------------------------------------------
-- The law gives the hotel one month to answer (Art. 12(3)), extendable by two more for a complex or numerous request, and
-- requires a refusal to say why and that the person may complain. The product did the work of a request (find, export,
-- erase) but kept no record of the request itself: when it was received, when it is due, how the person was verified,
-- whether it was answered or refused and on what ground. This is that record. It holds a reference to the requester only
-- while the request is open; answering it removes that and the free text, and keeps the dates, the ground and the references
-- of the export, erasure or assisted request that carried it out. It is a record the staff keep; it decides nothing.
CREATE TABLE IF NOT EXISTS iam_v2.guest_data_request_register (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  ref                 text NOT NULL CONSTRAINT gdrr_ref CHECK (ref ~ '^RQ-[0-9A-F]{8}$'),
  tenant_id           uuid NOT NULL,
  site_id             uuid NOT NULL,
  kind                text NOT NULL CONSTRAINT gdrr_kind CHECK (kind IN ('ACCESS', 'ERASURE', 'OTHER')),
  received_at         date NOT NULL,
  due_at              date NOT NULL,
  extended_until      date,
  extension_reason    text,
  requester_ref       text NOT NULL CONSTRAINT gdrr_requester CHECK (length(btrim(requester_ref)) BETWEEN 2 AND 120),
  verification_method text CONSTRAINT gdrr_method CHECK (verification_method IS NULL OR verification_method IN
                        ('ID_CHECKED_IN_PERSON', 'ACCOUNT_CONTROL_SHOWN', 'CALLBACK_TO_KNOWN_CONTACT', 'OTHER_DOCUMENTED')),
  status              text NOT NULL DEFAULT 'OPEN' CONSTRAINT gdrr_status
                        CHECK (status IN ('OPEN', 'ANSWERED', 'PARTLY_REFUSED', 'REFUSED', 'WITHDRAWN')),
  outcome_code        text CONSTRAINT gdrr_outcome CHECK (outcome_code IS NULL OR outcome_code IN
                        ('NONE', 'LEGAL_RETENTION', 'NOT_IDENTIFIED', 'SHARED_ACCESS', 'THIRD_PARTY_RIGHTS', 'MANIFESTLY_UNFOUNDED', 'OTHER')),
  outcome_note        text CONSTRAINT gdrr_note CHECK (outcome_note IS NULL OR length(outcome_note) <= 500),
  erasure_ref         text CONSTRAINT gdrr_er CHECK (erasure_ref IS NULL OR erasure_ref ~ '^ER-[0-9A-F]{12}$'),
  assisted_ref        text CONSTRAINT gdrr_ar CHECK (assisted_ref IS NULL OR assisted_ref ~ '^AR-[0-9A-F]{10}$'),
  created_by          text NOT NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  answered_by         text,
  answered_at         timestamptz,
  CONSTRAINT gdrr_unique UNIQUE (ref),
  CONSTRAINT gdrr_due CHECK (due_at >= received_at AND (extended_until IS NULL OR extended_until >= due_at)),
  CONSTRAINT gdrr_closed CHECK ((status = 'OPEN') = (answered_at IS NULL) AND (answered_at IS NULL) = (answered_by IS NULL)
                                AND ((status = 'OPEN') OR outcome_code IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS gdrr_site ON iam_v2.guest_data_request_register (tenant_id, site_id, status, due_at);
COMMENT ON TABLE iam_v2.guest_data_request_register IS
  'A request made under the data-protection rules, its deadline and how it was answered (0147). The requester reference and '
  'the note are removed when it is answered; nothing here decides anything.';

CREATE OR REPLACE FUNCTION iam_v2.request_register_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'a request in the register is never deleted' USING ERRCODE = 'restrict_violation';
  END IF;
  IF OLD.status <> 'OPEN' THEN
    RAISE EXCEPTION 'an answered request does not change' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.id, NEW.ref, NEW.tenant_id, NEW.site_id, NEW.kind, NEW.received_at, NEW.due_at, NEW.created_by, NEW.created_at)
     IS DISTINCT FROM (OLD.id, OLD.ref, OLD.tenant_id, OLD.site_id, OLD.kind, OLD.received_at, OLD.due_at, OLD.created_by, OLD.created_at) THEN
    RAISE EXCEPTION 'what was received, and when, cannot be edited' USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS request_register_guard ON iam_v2.guest_data_request_register;
CREATE TRIGGER request_register_guard BEFORE UPDATE OR DELETE ON iam_v2.guest_data_request_register
  FOR EACH ROW EXECUTE FUNCTION iam_v2.request_register_guard();

CREATE OR REPLACE FUNCTION iam_v2.request_register_open(p_tenant uuid, p_site uuid, p_actor text, p_kind text, p_received date,
                                                          p_requester text, p_method text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_ref text := 'RQ-' || upper(encode(public.gen_random_bytes(4), 'hex')); v_id uuid; v_due date;
BEGIN
  IF p_actor IS NULL OR length(btrim(p_actor)) NOT BETWEEN 1 AND 200 THEN
    RAISE EXCEPTION 'an operator is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_received IS NULL OR p_received > iam_v2.site_today(p_site) OR p_received < iam_v2.site_today(p_site) - 366 THEN
    RAISE EXCEPTION 'the request was received on a day up to a year ago, and not in the future' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_requester IS NULL OR length(btrim(p_requester)) NOT BETWEEN 2 AND 120 THEN
    RAISE EXCEPTION 'say who is asking (2-120 characters)' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  v_due := (p_received + interval '1 month')::date;
  INSERT INTO guest_data_request_register (ref, tenant_id, site_id, kind, received_at, due_at, requester_ref, verification_method, created_by)
  VALUES (v_ref, p_tenant, p_site, p_kind, p_received, v_due, btrim(p_requester), NULLIF(p_method, ''), btrim(p_actor))
  RETURNING id INTO v_id;
  RETURN jsonb_build_object('id', v_id, 'ref', v_ref, 'due_at', v_due);
END $$;

-- A complex or numerous request may take two more months, once, with the person told within the first month (Art. 12(3)).
CREATE OR REPLACE FUNCTION iam_v2.request_register_extend(p_tenant uuid, p_site uuid, p_id uuid, p_actor text, p_reason text) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE q record;
BEGIN
  IF p_reason IS NULL OR length(btrim(p_reason)) NOT BETWEEN 4 AND 500 THEN
    RAISE EXCEPTION 'say why the request needs longer (4-500 characters)' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  -- The reason is kept in the register: it names no one (no address, no number).
  IF p_reason ~ '@' OR p_reason ~ '[0-9][^0-9]*[0-9][^0-9]*[0-9][^0-9]*[0-9][^0-9]*[0-9][^0-9]*[0-9]' THEN
    RAISE EXCEPTION 'the reason must not contain an email address or a number that could identify someone' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT * INTO q FROM guest_data_request_register x WHERE x.id = p_id AND x.tenant_id = p_tenant AND x.site_id = p_site FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'request not found' USING ERRCODE = 'no_data_found'; END IF;
  IF q.status <> 'OPEN' OR q.extended_until IS NOT NULL OR iam_v2.site_today(p_site) > q.due_at THEN
    RAISE EXCEPTION 'register_refused' USING ERRCODE = 'object_in_use', DETAIL = 'not_extendable';
  END IF;
  UPDATE guest_data_request_register SET extended_until = (received_at + interval '3 months')::date,
         extension_reason = btrim(p_reason) WHERE id = p_id;
  RETURN jsonb_build_object('id', p_id, 'due_at', (q.received_at + interval '3 months')::date);
END $$;

-- Answering closes the request and removes the requester reference and the note; it returns them once, as the hotel's
-- own file of the request.
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
  RETURN jsonb_build_object('reference', q.ref, 'kind', q.kind, 'requester', q.requester_ref, 'received_at', q.received_at,
                            'due_at', COALESCE(q.extended_until, q.due_at), 'answered_at', now(), 'status', p_status,
                            'outcome', COALESCE(NULLIF(p_outcome, ''), 'NONE'), 'note', p_note,
                            'verification_method', q.verification_method, 'erasure_ref', NULLIF(p_erasure_ref, ''),
                            'assisted_ref', NULLIF(p_assisted_ref, ''), 'answered_in_time', iam_v2.site_today(p_site) <= COALESCE(q.extended_until, q.due_at));
END $$;

CREATE OR REPLACE FUNCTION iam_v2.request_register_list(p_tenant uuid, p_site uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(jsonb_agg(jsonb_build_object(
      'id', x.id, 'ref', x.ref, 'kind', x.kind, 'received_at', x.received_at, 'due_at', COALESCE(x.extended_until, x.due_at),
      'extended', x.extended_until IS NOT NULL, 'extension_reason', CASE WHEN x.status = 'OPEN' THEN x.extension_reason END,
      'requester', x.requester_ref, 'verification_method', x.verification_method, 'status', x.status, 'outcome', x.outcome_code,
      'erasure_ref', x.erasure_ref, 'assisted_ref', x.assisted_ref, 'created_by', x.created_by, 'answered_by', x.answered_by,
      'answered_at', x.answered_at,
      'days_left', CASE WHEN x.status = 'OPEN' THEN COALESCE(x.extended_until, x.due_at) - iam_v2.site_today(p_site) END,
      'overdue', x.status = 'OPEN' AND iam_v2.site_today(p_site) > COALESCE(x.extended_until, x.due_at))
      ORDER BY (x.status = 'OPEN') DESC, COALESCE(x.extended_until, x.due_at), x.received_at DESC), '[]'::jsonb)
    FROM (SELECT * FROM guest_data_request_register r WHERE r.tenant_id = p_tenant AND r.site_id = p_site
           ORDER BY (r.status = 'OPEN') DESC, r.received_at DESC LIMIT 300) x
$$;

INSERT INTO iam_v2.scrub_registry (table_name, column_name, kind, ts_column, handled, jsonb_key) VALUES
  ('iam_v2.guest_data_request_register', 'created_by', 'label', NULL, false, NULL),
  ('iam_v2.guest_data_request_register', 'answered_by', 'label', NULL, false, NULL)
ON CONFLICT (table_name, column_name) DO NOTHING;
DO $inst$
DECLARE t record; trg record; def text;
BEGIN
  FOR t IN SELECT DISTINCT x.table_name FROM iam_v2.scrub_registry x WHERE x.table_name = 'iam_v2.guest_data_request_register' LOOP
    FOR trg IN SELECT g.oid, g.tgname FROM pg_trigger g
                WHERE g.tgrelid = to_regclass(t.table_name) AND NOT g.tgisinternal AND g.tgconstraint = 0
                  AND (g.tgtype & 2) = 2 AND (g.tgtype & 24) <> 0 AND g.tgname <> 'zz_scrub_guard' LOOP
      def := pg_get_triggerdef(trg.oid);
      IF def LIKE '%iam_v2.scrub%' THEN CONTINUE; END IF;
      EXECUTE format('DROP TRIGGER %I ON %s', trg.tgname, t.table_name);
      EXECUTE replace(def, ' EXECUTE FUNCTION', ' WHEN (current_setting(''iam_v2.scrub'', true) IS DISTINCT FROM ''on'') EXECUTE FUNCTION');
    END LOOP;
    EXECUTE format('DROP TRIGGER IF EXISTS zz_scrub_guard ON %s', t.table_name);
    EXECUTE format('CREATE TRIGGER zz_scrub_guard BEFORE UPDATE OR DELETE ON %s FOR EACH ROW WHEN (current_setting(''iam_v2.scrub'', true) = ''on'') EXECUTE FUNCTION iam_v2.scrub_guard()', t.table_name);
  END LOOP;
END $inst$;

-- ---------------------------------------------------------------------------------------------------------
-- 7. Second review: the device-address cleaning is one function; a nameless co-occupant is a guest; times are the site's.
-- ---------------------------------------------------------------------------------------------------------
-- The device addresses an assisted erasure anonymised are strong identifiers: wherever the activity log carries one it is
-- replaced. One function does it for the request AND for the replay after a restore (which had left them behind), and names
-- the rows by table and position because audit_log is a hypertable (a position alone is not unique across chunks).
CREATE OR REPLACE FUNCTION public.audit_clean_macs(p_tenant uuid, p_macs text[], p_ref text, p_since timestamptz) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE v_macs text[]; v_pats text[]; n integer := 0;
BEGIN
  -- Staff type an address in any spelling: colons, dashes, none, or three groups of four.
  v_macs := ARRAY(SELECT DISTINCT lower(x) FROM unnest(p_macs) m
                    CROSS JOIN LATERAL unnest(ARRAY[m, replace(m, ':', '-'), replace(m, ':', ''),
                      substr(replace(m, ':', ''), 1, 4) || '.' || substr(replace(m, ':', ''), 5, 4) || '.' || substr(replace(m, ':', ''), 9, 4)]) x);
  IF cardinality(v_macs) = 0 THEN
    RETURN 0;
  END IF;
  v_pats := ARRAY(SELECT '%' || replace(replace(replace(x, '\', '\\'), '%', '\%'), '_', '\_') || '%' FROM unnest(v_macs) x);
  WITH cand AS MATERIALIZED (
    SELECT l.tableoid AS toid, l.ctid AS rid, l.payload AS old_payload, (lower(l.target_id) = ANY (v_macs)) AS about_it,
           iam_v2.redact_guest_text(l.payload, v_macs, l.action = 'guest_data.searched') AS new_payload
      FROM audit_log l
     WHERE l.tenant_id = p_tenant AND l.ts >= COALESCE(p_since, '-infinity')
       AND (lower(l.target_id) = ANY (v_macs) OR l.action = 'guest_data.searched' OR lower(l.payload::text) LIKE ANY (v_pats)))
  UPDATE audit_log l
     SET payload = cand.new_payload || jsonb_build_object('erasure_ref', p_ref),
         target_id = CASE WHEN cand.about_it THEN NULL ELSE l.target_id END
    FROM cand
   WHERE l.tableoid = cand.toid AND l.ctid = cand.rid AND (cand.about_it OR cand.new_payload IS DISTINCT FROM cand.old_payload);
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END $$;

-- The assisted erasure cleans the activity log through the shared function
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
  v_macs := ARRAY(SELECT jsonb_array_elements_text(v -> 'macs_cleared'));
  n_audit := public.audit_clean_macs(p_tenant, v_macs, v_ref, COALESCE((v ->> 'since')::timestamptz, '-infinity'));
  RETURN (v - 'macs_cleared' - 'since') || jsonb_build_object('activity_log_entries_cleaned', n_audit);
END $$;

-- A reservation row that carries only a PMS guest number is still a guest: it is not the requester's to export or erase.
CREATE OR REPLACE FUNCTION iam_v2.stay_named_guests(p_stay uuid) RETURNS integer
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT count(*)::integer FROM iam_v2.stay_guests sg
   WHERE sg.stay_id = p_stay AND num_nonnulls(sg.first_name_norm, sg.last_name_norm, sg.display_name, sg.external_guest_id) > 0;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- N. Ownership and least privilege (mirrored in deploy/gatep/svc-guest-data-rights-grants.sql).
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON FUNCTION iam_v2.is_anon_token(text), iam_v2.assisted_apply_records(uuid,uuid,text[],uuid[]),
  iam_v2.anonymised_entitlements(uuid), iam_v2.anonymised_accounts(uuid), iam_v2.anonymised_principals(uuid),
  iam_v2.scrub_staff_label(text,text), iam_v2.scrub_registry_notes(uuid,uuid,timestamptz,integer,text), public.scrub_staff_label_public(text,text),
  public.guest_data_erase_core(uuid,uuid,text,uuid,text,text,boolean,text),
  public.erasure_journal_export(uuid,uuid), public.erasure_journal_replay(jsonb) FROM PUBLIC;
REVOKE ALL ON iam_v2.erasure_journal, iam_v2.scrub_registry, iam_v2.guest_data_request_register FROM PUBLIC;
REVOKE ALL ON FUNCTION public.audit_clean_macs(uuid,text[],text,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.request_register_guard(), iam_v2.request_register_open(uuid,uuid,text,text,date,text,text),
  iam_v2.request_register_extend(uuid,uuid,uuid,text,text),
  iam_v2.request_register_answer(uuid,uuid,uuid,text,text,text,text,text,text), iam_v2.request_register_list(uuid,uuid) FROM PUBLIC;
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.erasure_journal OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.is_anon_token(text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_apply_records(uuid,uuid,text[],uuid[]) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.anonymised_entitlements(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.anonymised_accounts(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.anonymised_principals(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.scrub_registry OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.scrub_guard() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.scrub_staff_label(text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.scrub_registry_notes(uuid,uuid,timestamptz,integer,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.guest_data_request_register OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.request_register_guard() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.request_register_open(uuid,uuid,text,text,date,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.request_register_extend(uuid,uuid,uuid,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.request_register_answer(uuid,uuid,uuid,text,text,text,text,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.request_register_list(uuid,uuid) OWNER TO iam_v2_owner';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'GRANT SELECT (code_verifier, nonce) ON public.social_oauth_states TO iam_v2_owner';
    EXECUTE 'GRANT UPDATE (code_verifier, nonce) ON public.social_oauth_states TO iam_v2_owner';
    EXECUTE 'GRANT SELECT ON public.dhcp_reservations TO iam_v2_owner';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.request_register_open(uuid,uuid,text,text,date,text,text) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.request_register_extend(uuid,uuid,uuid,text,text) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.request_register_answer(uuid,uuid,uuid,text,text,text,text,text,text) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.request_register_list(uuid,uuid) TO svc_edged';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.erasure_journal_export(uuid,uuid) TO svc_scd';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.erasure_journal_replay(jsonb) TO svc_scd';
  END IF;
END $own$;

COMMIT;
