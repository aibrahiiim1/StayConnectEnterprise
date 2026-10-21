-- 0141 down: export, erasure and search as 0137/0139 left them (a stay is one record). Guests already erased
-- one at a time stay erased.
BEGIN;
CREATE OR REPLACE FUNCTION iam_v2.guest_data_scope(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS TABLE(o_kind text, o_id uuid, o_value text)
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[] := '{}'; p uuid[] := '{}'; s uuid[] := '{}'; own uuid[]; e uuid[]; d uuid[]; shared uuid[];
        resv text[];
BEGIN
  IF p_kind = 'account' THEN
    SELECT COALESCE(array_agg(x.id), '{}') INTO a FROM guest_access_accounts x
     WHERE x.id = p_id AND x.tenant_id = p_tenant AND x.site_id = p_site;
  ELSIF p_kind = 'principal' THEN
    SELECT COALESCE(array_agg(x.id), '{}') INTO p FROM guest_principals x WHERE x.id = p_id AND x.tenant_id = p_tenant;
  ELSIF p_kind = 'stay' THEN
    SELECT COALESCE(array_agg(x.id), '{}') INTO s FROM stays x
     WHERE x.id = p_id AND x.tenant_id = p_tenant AND x.site_id = p_site;
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
  -- The guest's own access, then ONE step out through it -- never further, so a lookup cannot walk from one
  -- guest to the whole hotel.
  SELECT COALESCE(array_agg(x.id), '{}') INTO own FROM entitlements x
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site
     AND (x.guest_account_id = ANY (a) OR x.guest_principal_id = ANY (p) OR x.stay_id = ANY (s));
  SELECT a || COALESCE(array_agg(DISTINCT x.guest_account_id) FILTER (WHERE x.guest_account_id IS NOT NULL AND NOT x.guest_account_id = ANY (a)), '{}'),
         p || COALESCE(array_agg(DISTINCT x.guest_principal_id) FILTER (WHERE x.guest_principal_id IS NOT NULL AND NOT x.guest_principal_id = ANY (p)), '{}'),
         s || COALESCE(array_agg(DISTINCT x.stay_id) FILTER (WHERE x.stay_id IS NOT NULL AND NOT x.stay_id = ANY (s)), '{}')
    INTO a, p, s FROM entitlements x WHERE x.id = ANY (own);
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
END $function$;
CREATE OR REPLACE FUNCTION iam_v2.guest_data_export_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS jsonb
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; ev uuid[]; macs macaddr[]; se_ids uuid[]; pu_ids uuid[];
BEGIN
  SELECT COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'account'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'principal'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'entitlement'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'device'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay_event'), '{}')
    INTO a, p, s, e, d, ev FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
  SELECT COALESCE(array_agg(dv.mac), '{}') INTO macs FROM devices dv WHERE dv.id = ANY (d);
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
        'rate_plan', x.rate_plan, 'vip', x.vip, 'travel_agent', x.travel_agent,
        'arrival', x.arrival, 'departure', x.departure, 'checked_out_at', x.effective_checkout_at,
        'status', x.status,
        'guests', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'name', sg.display_name, 'first_name', sg.first_name_norm, 'last_name', sg.last_name_norm,
            'pms_guest_id', sg.external_guest_id, 'primary', sg.is_primary) ORDER BY sg.is_primary DESC), '[]'::jsonb)
          FROM stay_guests sg WHERE sg.stay_id = x.id),
        'linked_to', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'reason', k.reason, 'other_reservation', o.external_reservation_id,
            'direction', CASE WHEN k.from_stay = x.id THEN 'continued_as' ELSE 'continues' END)), '[]'::jsonb)
          FROM stay_links k JOIN stays o ON o.id = CASE WHEN k.from_stay = x.id THEN k.to_stay ELSE k.from_stay END
         WHERE k.from_stay = x.id OR k.to_stay = x.id)) ORDER BY x.arrival), '[]'::jsonb)
      FROM stays x WHERE x.id = ANY (s)),
    'pms_events', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'type', x.event_type, 'received_at', x.received_at, 'pms_time', x.pms_timestamp_utc,
        'status', x.processing_status, 'details', x.payload) ORDER BY x.received_at), '[]'::jsonb)
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
      FROM pms_postings x WHERE x.tenant_id = p_tenant AND (x.purchase_id = ANY (pu_ids) OR x.stay_id = ANY (s))),
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
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs)));
END $function$;
CREATE OR REPLACE FUNCTION iam_v2.erase_guest_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text, p_public_counts jsonb)
 RETURNS jsonb
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; ev uuid[]; macs macaddr[]; live text[] := '{}';
        v_id uuid; v_shared integer; v_attempts integer; v_sessions integer; v_k integer; v_verif integer := 0;
        v_events integer := 0; v_given integer := 0; v_ref text; v_counts jsonb;
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
         count(*) FILTER (WHERE o_kind = 'device_shared')
    INTO a, p, s, e, d, ev, v_shared FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
  PERFORM 1 FROM devices dv WHERE dv.id = ANY (d) FOR UPDATE;
  SELECT COALESCE(array_agg(dv.mac), '{}') INTO macs FROM devices dv WHERE dv.id = ANY (d) AND dv.mac::text NOT LIKE '03:%';

  -- REFUSED WHILE LIVE (D-C3). The PMS would copy an open stay straight back, and anonymising access or a
  -- device in use would cut a guest off mid-session. A stay the PMS left open after its departure is not live.
  IF EXISTS (SELECT 1 FROM stays x WHERE x.id = ANY (s) AND x.status IN ('RESERVED', 'IN_HOUSE')
                                     AND (x.departure IS NULL OR x.departure >= current_date - 1)) THEN
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
  UPDATE stay_events x
     SET payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE x.id = ANY (ev) AND x.stay_id IS NULL AND x.processing_status <> 'PENDING'
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
  SELECT v_events + count(*) INTO v_events FROM stay_events x WHERE x.id = ANY (ev) AND x.stay_id IS NOT NULL;
  PERFORM set_config('iam_v2.anonymise', '', true);

  v_ref := 'ER-' || upper(encode(public.gen_random_bytes(6), 'hex'));
  v_counts := jsonb_build_object(
    'client_accounts', cardinality(a), 'guest_identities', cardinality(p), 'hotel_stays', cardinality(s),
    'pms_events', v_events, 'devices', cardinality(d), 'devices_kept_shared', v_shared, 'sessions', v_sessions,
    'sign_in_attempts', v_attempts, 'verification_records', v_verif) || COALESCE(p_public_counts, '{}'::jsonb);
  INSERT INTO guest_data_erasures (erasure_ref, tenant_id, site_id, subject_kind, erased_by, erase_reason, counts)
  VALUES (v_ref, p_tenant, p_site, p_kind, btrim(p_operator), btrim(p_reason), v_counts);
  RETURN jsonb_build_object('erasure_ref', v_ref, 'counts', v_counts);
END $function$;
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
    SELECT 'stay'::text, x.id,
           'Room ' || COALESCE(x.normalized_room_number, '?') || ' · reservation ' || x.external_reservation_id,
           (SELECT string_agg(sg.display_name, ', ' ORDER BY sg.is_primary DESC) FROM stay_guests sg WHERE sg.stay_id = x.id),
           x.status,
           COALESCE(x.effective_checkout_at, x.departure::timestamptz, x.arrival::timestamptz)
      FROM stays x
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.external_reservation_id NOT LIKE 'anon-%'
       AND (lower(COALESCE(x.normalized_room_number, '')) = q OR lower(x.external_reservation_id) = q
            OR EXISTS (SELECT 1 FROM stay_guests sg
                        WHERE sg.stay_id = x.id
                          AND (lower(COALESCE(sg.display_name, '')) LIKE pat OR lower(COALESCE(sg.last_name_norm, '')) LIKE pat
                               OR lower(COALESCE(sg.first_name_norm, '')) LIKE pat))
            OR EXISTS (SELECT 1 FROM entitlements en WHERE en.id = ANY (v_ents) AND en.stay_id = x.id))
  ) r ORDER BY 6 DESC NULLS LAST LIMIT 50;
END $function$;
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
  SELECT COALESCE(array_agg(DISTINCT o_id::text), '{}'),
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
CREATE OR REPLACE FUNCTION public.guest_data_erase(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text)
 RETURNS jsonb
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v_ids text[]; v_vals text[]; v_pats text[]; n_codes integer; n_oauth integer; n_audit integer;
BEGIN
  SELECT COALESCE(array_agg(DISTINCT o_id::text), '{}'),
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

-- gde_kind keeps 'stay_guest': the erasure log is append-only, and its rows stay as they were written.
DROP FUNCTION IF EXISTS iam_v2.event_payload_for_guests(jsonb, jsonb, boolean);
DROP FUNCTION IF EXISTS iam_v2.stay_guests_as_json(uuid[]);
DROP FUNCTION IF EXISTS iam_v2.stay_named_guests(uuid);
COMMIT;
