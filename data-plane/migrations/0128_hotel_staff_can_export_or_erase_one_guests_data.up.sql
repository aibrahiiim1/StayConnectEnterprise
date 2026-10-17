-- 0128 -- HOTEL STAFF CAN EXPORT OR ERASE ONE GUEST'S DATA
-- (GDPR remediation plan C3 and C4; Product-Owner decisions D-C3 and D-C4, 2026-10-17;
--  docs/privacy/GDPR_REMEDIATION_PLAN.md section 4).
--
-- A guest who asks "what do you hold about me" (GDPR Art. 15 and 20) or "erase me" (Art. 17) is answered from
-- Hotel Admin. This migration adds:
--
--   1. iam_v2.guest_data_find -- look a guest up by email, phone, username, name, room, reservation or MAC
--      address. It returns the three kinds of record a guest is anchored by: a client account, a guest identity
--      (the person behind email, phone and social sign-ins) or a hotel stay.
--   2. iam_v2.guest_data_scope -- ONE definition of "everything held about this guest", shared by export and
--      erasure so the two can never disagree: the anchor, the records linked to it by one step through the
--      internet access it held (the stay a client account belongs to, the identity that signed in on a stay),
--      the access itself, and the devices and sessions it used. A device that another guest's access also used
--      is left alone and counted, never anonymised on someone else's behalf.
--   3. public.guest_data_export -- that scope as one JSON document, for staff to hand to the guest (D-C4).
--   4. public.guest_data_erase -- that scope anonymised with the 0127 routine (D-C3: no row is deleted, every
--      identifier becomes a random token or is cleared, free text is blanked), plus the guest's sign-in
--      attempts, verification records, sign-in codes, OAuth states and audit-log mentions. REFUSED while
--      anything is live: a stay the PMS still has open (it would copy the guest straight back), internet access
--      that has not ended, or a device that is online. Each erasure leaves one record with a reference (ER-...)
--      to give the guest, and no personal data.
--
-- Who may call them is edged's decision (the guest-data-rights key: site_admin and the IT manager, D-C4), behind
-- the operator's password and a reason, audited. Financial rows (purchases, postings, settlements) keep their
-- amounts: they reference the anonymised stay, not the person.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The erasure record.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.guest_data_erasures (
  erasure_ref  text PRIMARY KEY CONSTRAINT gde_ref CHECK (erasure_ref ~ '^ER-[0-9A-F]{12}$'),
  tenant_id    uuid NOT NULL,
  site_id      uuid NOT NULL,
  subject_kind text NOT NULL CONSTRAINT gde_kind CHECK (subject_kind IN ('account', 'principal', 'stay')),
  erased_by    text NOT NULL CONSTRAINT gde_actor CHECK (length(btrim(erased_by)) BETWEEN 1 AND 200),
  erase_reason text NOT NULL CONSTRAINT gde_reason CHECK (length(btrim(erase_reason)) BETWEEN 4 AND 500),
  counts       jsonb NOT NULL,
  erased_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS gde_site ON iam_v2.guest_data_erasures (tenant_id, site_id, erased_at DESC);
COMMENT ON TABLE iam_v2.guest_data_erasures IS
  'Every "erase this guest" that ran (0128, GDPR plan C3): a reference to give the guest, who did it and why, and '
  'how many records were anonymised. Holds no personal data and no link back to the erased records. Append-only.';
DROP TRIGGER IF EXISTS guest_data_erasures_append_only ON iam_v2.guest_data_erasures;
CREATE TRIGGER guest_data_erasures_append_only BEFORE UPDATE OR DELETE ON iam_v2.guest_data_erasures
  FOR EACH ROW EXECUTE FUNCTION iam_v2.data_retention_append_only();

-- ---------------------------------------------------------------------------------------------------------
-- 2. Lookup.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.guest_data_find(p_tenant uuid, p_site uuid, p_query text)
    RETURNS TABLE(o_kind text, o_id uuid, o_label text, o_detail text, o_status text, o_last_activity timestamptz)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
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
END $$;
COMMENT ON FUNCTION iam_v2.guest_data_find(uuid, uuid, text) IS
  'Finds the client accounts, guest identities and hotel stays matching a search (0128, GDPR plan C3/C4): email, '
  'phone, username or name (substring), room or reservation (exact), or a device MAC address. At most 50, most '
  'recent first. Already-anonymised records are never returned.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. Scope: everything held about one guest. Granted to nobody; export and erasure run it.
--    Kinds returned: account, principal, identity (o_id = its principal), stay, entitlement, device,
--    device_shared. o_value is the identifying text of the row (username, identity value, reservation, MAC),
--    NULL once it is anonymised.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.guest_data_scope(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
    RETURNS TABLE(o_kind text, o_id uuid, o_value text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE a uuid[] := '{}'; p uuid[] := '{}'; s uuid[] := '{}'; own uuid[]; e uuid[]; d uuid[]; shared uuid[];
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
  RETURN QUERY SELECT 'entitlement'::text, z.x, NULL::text FROM unnest(e) z(x);
  RETURN QUERY SELECT 'device'::text, dv.id, CASE WHEN dv.mac::text NOT LIKE '03:%' THEN dv.mac::text END
                 FROM devices dv WHERE dv.id = ANY (d) AND NOT dv.id = ANY (shared);
  RETURN QUERY SELECT 'device_shared'::text, z.x, NULL::text FROM unnest(shared) z(x);
END $$;
COMMENT ON FUNCTION iam_v2.guest_data_scope(uuid, uuid, text, uuid) IS
  'Everything held about one guest (0128): the anchor record, the accounts, identities and stays one step away '
  'through the access it held, that access, and the devices it used alone. Shared by export and erasure.';

-- ---------------------------------------------------------------------------------------------------------
-- 4. Export (iam_v2 part). Granted to nobody; public.guest_data_export runs it.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.guest_data_export_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
    RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; macs macaddr[];
BEGIN
  SELECT COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'account'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'principal'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'entitlement'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'device'), '{}')
    INTO a, p, s, e, d FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
  SELECT COALESCE(array_agg(dv.mac), '{}') INTO macs FROM devices dv WHERE dv.id = ANY (d);

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
    'hotel_stays', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'reservation', x.external_reservation_id, 'room', x.normalized_room_number, 'room_type', x.room_type,
        'arrival', x.arrival, 'departure', x.departure, 'checked_out_at', x.effective_checkout_at,
        'status', x.status,
        'guests', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'name', sg.display_name, 'first_name', sg.first_name_norm, 'last_name', sg.last_name_norm,
            'date_of_birth', sg.date_of_birth, 'primary', sg.is_primary) ORDER BY sg.is_primary DESC), '[]'::jsonb)
          FROM stay_guests sg WHERE sg.stay_id = x.id)) ORDER BY x.arrival), '[]'::jsonb)
      FROM stays x WHERE x.id = ANY (s)),
    'internet_access', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'status', x.status, 'activated_at', x.activated_at, 'ends_at', x.window_ends_at,
        'ended_at', x.terminated_at, 'ended_because', x.terminal_reason,
        'data_used_bytes', x.consumed_data_bytes, 'online_seconds', x.consumed_online_seconds)
        ORDER BY x.activated_at NULLS LAST), '[]'::jsonb)
      FROM entitlements x WHERE x.id = ANY (e)),
    'purchases', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'amount_minor', pu.amount_minor, 'currency', pu.currency, 'currency_exponent', pu.currency_exponent,
        'tax_amount_minor', pu.tax_amount_minor, 'state', pu.state, 'paid_by', pu.trigger)), '[]'::jsonb)
      FROM purchases pu WHERE pu.id IN (SELECT x.purchase_id FROM entitlements x WHERE x.id = ANY (e))),
    'devices', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'mac_address', dv.mac, 'first_seen', dv.first_seen, 'last_seen', dv.last_seen, 'last_ip', dv.last_ip)
        ORDER BY dv.first_seen), '[]'::jsonb)
      FROM devices dv WHERE dv.id = ANY (d)),
    'sessions', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'started', se.started, 'ended', se.ended, 'end_reason', se.end_reason, 'ip', se.ip, 'mac_address', se.mac,
        'bytes_up', se.bytes_up, 'bytes_down', se.bytes_down) ORDER BY se.started), '[]'::jsonb)
      FROM sessions se WHERE se.tenant_id = p_tenant AND (se.entitlement_id = ANY (e) OR se.device_id = ANY (d))),
    'sign_in_attempts', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'at', sa.occurred_at, 'room_entered', sa.submitted_room, 'result', sa.result) ORDER BY sa.occurred_at), '[]'::jsonb)
      FROM sign_in_attempts sa
     WHERE sa.tenant_id = p_tenant AND sa.site_id = p_site
       AND (sa.matched_stay_id = ANY (s) OR sa.entitlement_id = ANY (e) OR sa.device_mac = ANY (macs))));
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 5. Erasure (iam_v2 part). Granted to nobody; public.guest_data_erase runs it after its own part, in the same
--    transaction, so a refusal here undoes everything.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.erase_guest_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid,
                                                      p_operator text, p_reason text, p_public_counts jsonb)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; macs macaddr[]; live text[] := '{}';
        v_id uuid; v_shared integer; v_attempts integer; v_sessions integer; v_k integer; v_verif integer := 0;
        v_ref text; v_counts jsonb;
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
         count(*) FILTER (WHERE o_kind = 'device_shared')
    INTO a, p, s, e, d, v_shared FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
  PERFORM 1 FROM devices dv WHERE dv.id = ANY (d) FOR UPDATE;
  SELECT COALESCE(array_agg(dv.mac), '{}') INTO macs FROM devices dv WHERE dv.id = ANY (d) AND dv.mac::text NOT LIKE '03:%';

  -- REFUSED WHILE LIVE (D-C3). The PMS would copy an open stay straight back, and anonymising access or a
  -- device in use would cut a guest off mid-session.
  IF EXISTS (SELECT 1 FROM stays x WHERE x.id = ANY (s) AND x.status IN ('RESERVED', 'IN_HOUSE')) THEN
    live := live || 'pms_open'::text;
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
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs)
     AND (x.expires_at IS NULL OR x.expires_at < now() OR x.released_at IS NOT NULL);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_verif := v_verif + v_k;
  UPDATE otp_device_bindings x SET device_mac = anonymised_mac()
   WHERE x.tenant_id = p_tenant AND x.device_mac = ANY (macs);
  GET DIAGNOSTICS v_k = ROW_COUNT; v_verif := v_verif + v_k;
  UPDATE sessions x SET ip = NULL, mac = NULL
   WHERE x.entitlement_id = ANY (e) AND x.ended IS NOT NULL AND (x.ip IS NOT NULL OR x.mac IS NOT NULL);
  GET DIAGNOSTICS v_sessions = ROW_COUNT;

  FOREACH v_id IN ARRAY d LOOP PERFORM anonymise_device(v_id); END LOOP;
  FOREACH v_id IN ARRAY a LOOP PERFORM anonymise_guest_account(v_id); END LOOP;
  FOREACH v_id IN ARRAY p LOOP PERFORM anonymise_guest_principal(v_id); END LOOP;
  FOREACH v_id IN ARRAY s LOOP PERFORM anonymise_stay(v_id); END LOOP;
  -- The names and room 0066 captured before it normalised them.
  UPDATE backfill_0066_identity_text x SET prior_first_name = NULL, prior_last_name = NULL, prior_room = NULL
   WHERE x.tenant_id = p_tenant
     AND (x.row_id = ANY (s) OR x.row_id IN (SELECT sg.id FROM stay_guests sg WHERE sg.stay_id = ANY (s)))
     AND num_nonnulls(x.prior_first_name, x.prior_last_name, x.prior_room) > 0;
  PERFORM set_config('iam_v2.anonymise', '', true);

  v_ref := 'ER-' || upper(encode(public.gen_random_bytes(6), 'hex'));
  v_counts := jsonb_build_object(
    'client_accounts', cardinality(a), 'guest_identities', cardinality(p), 'hotel_stays', cardinality(s),
    'devices', cardinality(d), 'devices_kept_shared', v_shared, 'sessions', v_sessions,
    'sign_in_attempts', v_attempts, 'verification_records', v_verif) || COALESCE(p_public_counts, '{}'::jsonb);
  INSERT INTO guest_data_erasures (erasure_ref, tenant_id, site_id, subject_kind, erased_by, erase_reason, counts)
  VALUES (v_ref, p_tenant, p_site, p_kind, btrim(p_operator), btrim(p_reason), v_counts);
  RETURN jsonb_build_object('erasure_ref', v_ref, 'counts', v_counts);
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 6. The two entry points edged calls (public: they also cover the platform tables).
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.guest_data_export(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
    RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE v jsonb; v_ids text[]; v_vals text[];
BEGIN
  v := iam_v2.guest_data_export_records(p_tenant, p_site, p_kind, p_id);
  SELECT COALESCE(array_agg(DISTINCT o_id::text), '{}'),
         COALESCE(array_agg(DISTINCT lower(o_value)) FILTER (WHERE o_kind IN ('account', 'identity') AND o_value IS NOT NULL), '{}')
    INTO v_ids, v_vals FROM iam_v2.guest_data_scope(p_tenant, p_site, p_kind, p_id);
  RETURN jsonb_build_object('format', 'stayconnect.guest-data-export/1', 'exported_at', now()) || v || jsonb_build_object(
    'verification_codes', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'channel', o.channel, 'sent_to', o.destination, 'sent_at', o.issued_at, 'used_at', o.consumed_at)
        ORDER BY o.issued_at), '[]'::jsonb)
      FROM auth_otps o WHERE o.tenant_id = p_tenant AND lower(o.destination) = ANY (v_vals)),
    'activity_log', (SELECT COALESCE(jsonb_agg(jsonb_build_object('at', l.ts, 'action', l.action) ORDER BY l.ts), '[]'::jsonb)
      FROM audit_log l WHERE l.tenant_id = p_tenant AND l.target_id = ANY (v_ids)));
END $$;
COMMENT ON FUNCTION public.guest_data_export(uuid, uuid, text, uuid) IS
  'One guest''s data as a JSON document (0128, GDPR plan C4, Art. 15/20): client accounts, identities and sign-in '
  'methods, hotel stays, internet access and purchases, devices, sessions, sign-in attempts, verification codes '
  'sent and the activity log entries about them. Changes nothing.';

CREATE OR REPLACE FUNCTION public.guest_data_erase(p_tenant uuid, p_site uuid, p_kind text, p_id uuid,
                                                   p_operator text, p_reason text)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
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
END $$;
COMMENT ON FUNCTION public.guest_data_erase(uuid, uuid, text, uuid, text, text) IS
  'Erases one guest (0128, GDPR plan C3, Art. 17; D-C3): anonymises everything iam_v2.guest_data_scope finds with '
  'the 0127 routine, and the guest''s sign-in codes, OAuth states and audit-log mentions. Deletes no row. Refused '
  '(SQLSTATE 55006, DETAIL pms_open / access_live / online) while anything is live. Returns the erasure reference.';

-- ---------------------------------------------------------------------------------------------------------
-- 7. Ownership, and least privilege mirrored in deploy/gatep/svc-guest-data-rights-grants.sql.
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.guest_data_erasures OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_data_find(uuid,uuid,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_data_scope(uuid,uuid,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_data_export_records(uuid,uuid,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.erase_guest_records(uuid,uuid,text,uuid,text,text,jsonb) OWNER TO iam_v2_owner';
  END IF;
END $own$;

REVOKE ALL ON iam_v2.guest_data_erasures FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.guest_data_find(uuid,uuid,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.guest_data_scope(uuid,uuid,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.guest_data_export_records(uuid,uuid,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION iam_v2.erase_guest_records(uuid,uuid,text,uuid,text,text,jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.guest_data_export(uuid,uuid,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.guest_data_erase(uuid,uuid,text,uuid,text,text) FROM PUBLIC;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.guest_data_find(uuid,uuid,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.guest_data_export(uuid,uuid,text,uuid) TO svc_edged;
    GRANT EXECUTE ON FUNCTION public.guest_data_erase(uuid,uuid,text,uuid,text,text) TO svc_edged;
    GRANT SELECT ON iam_v2.guest_data_erasures TO svc_edged;
  END IF;
END $g$;

COMMIT;
