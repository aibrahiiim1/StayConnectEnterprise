-- 0145 -- A GUEST REQUEST NEVER REACHES SHARED ROOM ACCESS OR ANOTHER PERSON
-- (Product Owner decision D-R5, 2026-10-20: room credentials prove eligibility, not identity.)
--
-- THE FACT THE EARLIER MIGRATIONS GOT WRONG. A PMS may name only the reservation holder while several people
-- use the room and its credentials; one named guest on a stay is not one occupant. A room number, a reservation
-- credential, a MAC address or a shared internet access identifies nobody. 0127-0144 treated the stay's access as
-- the named guest's: erasing or exporting the holder reached every device and session under the room's access,
-- which belong to whoever used the room -- and a client account linked to a stay pulled the stay's reservation
-- data into the account holder's request.
--
-- THE OWNERSHIP MODEL (one definition, used by export, erasure and live checks).
--
--   PMS RESERVATION DATA -- the stay, its guest rows, its PMS messages, its folio (room charges), its post-stay
--     access metadata. Owned by the named PMS guest(s). Reached by a stay or stay_guest request ONLY.
--
--   INDIVIDUAL DATA -- a client account the guest registered themselves (self_registered), an identity (email,
--     phone, social) with its remembered devices, the person's own entitlements and purchases, and the devices
--     EXCLUSIVELY tied to that person. Reached by an account or principal request ONLY, and never through a stay.
--
--   SHARED ACCESS -- everything used through a room (stay) credential, a voucher, an anonymous subject or a
--     staff-issued account, and any device with a link to anyone else or to shared access. It cannot be told
--     apart per person, so NO request exports it or changes it. It is counted, its liveness still blocks an
--     erasure, and it is anonymised when the hotel's retention ends (0127).
--
--   A DEVICE IS A PERSON'S ONLY when something the person did binds it to them (a remembered-device tick, the
--   self-registration made on it, or authentication of their own identity or self-registered account on it) AND
--   nothing else touches it: no other entitlement, session, credential or sign-in, no other account made on it.
--   One foreign link makes it shared -- a person's own phone also used with the room's credentials is shared,
--   because the MAC address does not say who held it. Such a device is left alone and counted, never guessed at.
--
-- WHAT CHANGES
--   * A client account request no longer adds the stay the account is linked to (the export says only that it is
--     linked to a reservation). A stay request no longer reaches the room's access, sessions, devices,
--     purchases, payments, sign-in attempts or sign-in records.
--   * A stay request still refuses while the room's access is live or online (anonymising a stay under people
--     online would cut them off); it only does not change that access.
--   * Room-credential sign-in attempts matched to the stay lose what was typed (the holder's name, the room) but
--     keep the typist's address and device: the content is the holder's, the identifiers are the typist's.
--   * Session addresses are cleared only on the person's exclusive devices, not on every session of their access.
--   * MAC-keyed records (verification grants, sign-in restrictions, code bindings, sign-in redirects) follow the
--     exclusive devices only.
--   * A PMS guest number is the same person only on the SAME PMS interface; across interfaces the number means
--     nothing, so only a unique full name can link (D-R4).
--   * The search by MAC address offers accounts and identities, never a stay: a device does not identify the
--     reservation holder.
--   * The folio's PMS guest number is shown only to the reservation's own export.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. One person is the same person only within one PMS interface.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.same_guest(a_ext text, a_first text, a_last text, a_iface uuid,
                                              b_ext text, b_first text, b_last text, b_iface uuid)
    RETURNS text
    LANGUAGE sql IMMUTABLE
    AS $$
  SELECT CASE
    WHEN a_iface IS NOT DISTINCT FROM b_iface
     AND NULLIF(btrim(a_ext), '') IS NOT NULL AND NULLIF(btrim(b_ext), '') IS NOT NULL THEN
      CASE WHEN btrim(a_ext) = btrim(b_ext) THEN 'id' END
    WHEN NULLIF(btrim(a_first), '') IS NOT NULL AND NULLIF(btrim(a_last), '') IS NOT NULL
     AND NULLIF(btrim(b_first), '') IS NOT NULL AND NULLIF(btrim(b_last), '') IS NOT NULL
     AND upper(btrim(a_first)) = upper(btrim(b_first)) AND upper(btrim(a_last)) = upper(btrim(b_last)) THEN 'name'
  END;
$$;
COMMENT ON FUNCTION iam_v2.same_guest(text,text,text,uuid,text,text,text,uuid) IS
  'Whether two PMS guest rows are one person (0145, D-R4/D-R5): the PMS guest number only within one PMS '
  'interface; otherwise the same first AND last name. A number from another interface says nothing.';

CREATE OR REPLACE FUNCTION iam_v2.same_guest_rows(p_guests uuid[], p_stays uuid[]) RETURNS uuid[]
    LANGUAGE plpgsql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v uuid[];
BEGIN
  IF EXISTS (
      SELECT 1
        FROM stay_guests a
        JOIN stay_guests b ON b.stay_id = ANY (p_stays) AND b.stay_id <> a.stay_id
       WHERE a.id = ANY (p_guests)
         AND same_guest(a.external_guest_id, a.first_name_norm, a.last_name_norm, a.pms_interface_id,
                        b.external_guest_id, b.first_name_norm, b.last_name_norm, b.pms_interface_id) IS NOT NULL
       -- More than one row of one stay that could be this guest -- by number or by name, an id-less namesake
       -- included -- cannot be told apart: refused, never guessed.
       GROUP BY a.id, b.stay_id HAVING count(*) > 1)
     OR EXISTS (
      SELECT 1
        FROM stay_guests a
        JOIN stay_guests t ON t.stay_id = a.stay_id AND t.id <> a.id
       WHERE a.id = ANY (p_guests) AND NOT t.id = ANY (p_guests)
         AND same_guest(NULL, a.first_name_norm, a.last_name_norm, a.pms_interface_id,
                        NULL, t.first_name_norm, t.last_name_norm, t.pms_interface_id) = 'name'
         AND EXISTS (SELECT 1 FROM stay_guests b
                      WHERE b.stay_id = ANY (p_stays) AND b.stay_id <> a.stay_id
                        AND same_guest(a.external_guest_id, a.first_name_norm, a.last_name_norm, a.pms_interface_id,
                                       b.external_guest_id, b.first_name_norm, b.last_name_norm, b.pms_interface_id) = 'name')) THEN
    RAISE EXCEPTION 'ambiguous guest: more than one guest has this name, so the guest cannot be told apart on a '
                    'linked stay. Choose the guest row from the search.' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT COALESCE(array_agg(DISTINCT b.id), '{}') INTO v
    FROM stay_guests a JOIN stay_guests b ON b.stay_id = ANY (p_stays)
   WHERE a.id = ANY (p_guests)
     AND (b.id = a.id
          OR (b.stay_id <> a.stay_id
              AND same_guest(a.external_guest_id, a.first_name_norm, a.last_name_norm, a.pms_interface_id,
                             b.external_guest_id, b.first_name_norm, b.last_name_norm, b.pms_interface_id) IS NOT NULL));
  RETURN v;
END $$;

-- A guest of a PMS message is matched by number only on the message's own interface (0145).
CREATE OR REPLACE FUNCTION iam_v2.stay_guests_as_json(p_guests uuid[]) RETURNS jsonb
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  -- 'rival': someone else on the same stay has this guest's surname, so a surname alone cannot say which of them
  -- a PMS message means.
  SELECT COALESCE(jsonb_agg(jsonb_build_object('id', sg.external_guest_id, 'iface', sg.pms_interface_id,
                                               'first', upper(btrim(sg.first_name_norm)),
                                               'last', upper(btrim(sg.last_name_norm)),
                                               'rival', EXISTS (SELECT 1 FROM iam_v2.stay_guests o
                                                                 WHERE o.stay_id = sg.stay_id AND NOT o.id = ANY (p_guests)
                                                                   AND upper(btrim(o.last_name_norm)) = upper(btrim(sg.last_name_norm))))),
                    '[]'::jsonb)
    FROM iam_v2.stay_guests sg WHERE sg.id = ANY (p_guests);
$$;

CREATE OR REPLACE FUNCTION iam_v2.event_payload_for_guests(p_payload jsonb, p_guests jsonb, p_keep boolean, p_iface uuid)
    RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  WITH g AS (SELECT CASE WHEN x->>'iface' IS NULL OR p_iface IS NULL OR x->>'iface' = p_iface::text
                         THEN NULLIF(x->>'id', '') END AS id,
                    NULLIF(x->>'first', '') AS f, NULLIF(x->>'last', '') AS l,
                    COALESCE((x->>'rival')::boolean, false) AS rival
               FROM jsonb_array_elements(COALESCE(p_guests, '[]'::jsonb)) x),
  sh AS (SELECT s.value AS v, s.ordinality AS n,
                NULLIF(btrim(s.value->>'external_guest_id'), '') AS id,
                NULLIF(upper(btrim(s.value->>'first_name')), '') AS f,
                NULLIF(upper(btrim(s.value->>'last_name')), '') AS l
           FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p_payload->'sharers') = 'array'
                                          THEN p_payload->'sharers' ELSE '[]'::jsonb END) WITH ORDINALITY s),
  -- The SAME test keeps a name (export) and takes it out (erasure): a PMS guest number on this interface; a full
  -- first AND last name that is unique among the sharers; or a surname alone when nobody else on the stay or among
  -- the sharers has it. A surname shared with a relative is nobody's, in either direction, so the relative is
  -- neither handed over nor altered.
  shm AS (SELECT sh.v, sh.n,
                 EXISTS (SELECT 1 FROM g
                          WHERE (g.id IS NOT NULL AND sh.id = g.id)
                             OR ((g.id IS NULL OR sh.id IS NULL) AND g.f IS NOT NULL AND g.l IS NOT NULL
                                 AND sh.f = g.f AND sh.l = g.l
                                 AND (SELECT count(*) FROM sh o WHERE o.f = sh.f AND o.l = sh.l) = 1)
                             OR (sh.f IS NULL AND sh.l IS NOT NULL AND g.l = sh.l AND NOT g.rival
                                 AND (SELECT count(*) FROM sh o WHERE o.l = sh.l) = 1)) AS mine
            FROM sh),
  top AS (SELECT EXISTS (SELECT 1 FROM g
                          WHERE (g.f IS NOT NULL AND g.l IS NOT NULL
                                 AND NULLIF(upper(btrim(p_payload->>'last_name')), '') = g.l
                                 AND NULLIF(upper(btrim(p_payload->>'first_name')), '') = g.f)
                             OR (NULLIF(btrim(p_payload->>'first_name'), '') IS NULL AND g.l IS NOT NULL AND NOT g.rival
                                 AND NULLIF(upper(btrim(p_payload->>'last_name')), '') = g.l)) AS mine)
  SELECT CASE WHEN p_payload IS NULL OR jsonb_typeof(p_payload) <> 'object' THEN p_payload ELSE
    (CASE WHEN (SELECT mine FROM top) = p_keep THEN p_payload ELSE p_payload - 'last_name' - 'first_name' - 'vip' - 'travel_agent' END)
    || CASE WHEN jsonb_typeof(p_payload->'sharers') = 'array' THEN jsonb_build_object('sharers', COALESCE((
         SELECT jsonb_agg(shm.v ORDER BY shm.n) FROM shm WHERE shm.mine = p_keep), '[]'::jsonb))
       ELSE '{}'::jsonb END
  END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- 2. Whose a device is.
-- ---------------------------------------------------------------------------------------------------------
-- True only when something the person did binds the device to them AND nothing else touches it. p_own_ind: the
-- entitlements of the person's identity and of their self-registered accounts; p_principals and p_accounts: the
-- person's identities and self-registered accounts; p_challenges: the one-time codes sent to the person's own
-- addresses (a verification or code binding on the device that is not one of them is someone else's).
CREATE OR REPLACE FUNCTION iam_v2.device_exclusive_to(p_device uuid, p_own_ind uuid[], p_principals uuid[], p_accounts uuid[], p_challenges uuid[])
    RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT (
      EXISTS (SELECT 1 FROM entitlement_devices ed WHERE ed.device_id = p_device AND ed.entitlement_id = ANY (p_own_ind))
      OR EXISTS (SELECT 1 FROM sessions se WHERE se.device_id = p_device AND se.entitlement_id = ANY (p_own_ind))
      OR EXISTS (SELECT 1 FROM principal_device_credentials c WHERE c.device_id = p_device AND c.guest_principal_id = ANY (p_principals))
      OR EXISTS (SELECT 1 FROM auth_contexts ac WHERE ac.device_id = p_device
                    AND (ac.guest_account_id = ANY (p_accounts) OR ac.guest_principal_id = ANY (p_principals)))
      OR EXISTS (SELECT 1 FROM devices dv JOIN guest_access_accounts g
                    ON g.created_by_device_mac = dv.mac AND g.tenant_id = dv.tenant_id AND g.site_id = dv.site_id
                  WHERE dv.id = p_device AND g.id = ANY (p_accounts))
    )
    AND NOT EXISTS (SELECT 1 FROM entitlement_devices ed WHERE ed.device_id = p_device AND NOT ed.entitlement_id = ANY (p_own_ind))
    AND NOT EXISTS (SELECT 1 FROM sessions se WHERE se.device_id = p_device AND NOT se.entitlement_id = ANY (p_own_ind))
    AND NOT EXISTS (SELECT 1 FROM principal_device_credentials c WHERE c.device_id = p_device AND NOT c.guest_principal_id = ANY (p_principals))
    AND NOT EXISTS (SELECT 1 FROM auth_contexts ac WHERE ac.device_id = p_device
                       AND NOT (COALESCE(ac.guest_account_id = ANY (p_accounts), false)
                                OR COALESCE(ac.guest_principal_id = ANY (p_principals), false)))
    AND NOT EXISTS (SELECT 1 FROM devices dv JOIN guest_access_accounts g
                       ON g.created_by_device_mac = dv.mac AND g.tenant_id = dv.tenant_id AND g.site_id = dv.site_id
                     WHERE dv.id = p_device AND NOT g.id = ANY (p_accounts))
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
                     WHERE dv.id = p_device AND sa.entitlement_id IS NOT NULL AND NOT sa.entitlement_id = ANY (p_own_ind));
$$;
COMMENT ON FUNCTION iam_v2.device_exclusive_to(uuid, uuid[], uuid[], uuid[], uuid[]) IS
  'A device is one person''s only when something that person did binds it to them and no other entitlement, session, '
  'credential, sign-in or account touches it (0145, D-R5). A MAC address alone identifies nobody.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. The scope of one guest, by ownership.
-- ---------------------------------------------------------------------------------------------------------
-- Kinds returned: account, principal, identity (o_id = its principal), stay, stay_event, stay_guest (the guest's
-- own row on a shared stay), stay_shared (a shared stay the guest is on: only their rows), stay_shared_other (a
-- shared stay reached without the guest: counted only), entitlement (an individual's OWN access),
-- entitlement_credential (a staff-issued account's access: usage only), challenge (their own one-time codes), device (exclusive),
-- device_shared (reached but not exclusive: counted only), access_shared (the room's access of the stays in
-- scope: checked for liveness, never exported or changed).
CREATE OR REPLACE FUNCTION iam_v2.guest_data_scope(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS TABLE(o_kind text, o_id uuid, o_value text)
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[] := '{}'; p uuid[] := '{}'; s uuid[] := '{}'; ss uuid[] := '{}'; sso uuid[] := '{}'; gr uuid[] := '{}';
        anchor uuid[] := '{}'; own uuid[]; own_ind uuid[]; ind_a uuid[]; room uuid[]; cand uuid[]; d uuid[]; dshared uuid[];
        ch uuid[]; v_origin uuid; v_seen uuid[]; v_front uuid[]; v_next uuid[]; v_depth integer := 0; v_st uuid;
        v_rows uuid[]; v_walked boolean := false;
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
    WHILE cardinality(v_front) > 0 AND v_depth < 20 LOOP
      SELECT COALESCE(array_agg(DISTINCT CASE WHEN k.from_stay = ANY (v_front) THEN k.to_stay ELSE k.from_stay END), '{}')
        INTO v_next
        FROM stay_links k
       WHERE k.tenant_id = p_tenant AND k.site_id = p_site AND (k.from_stay = ANY (v_front) OR k.to_stay = ANY (v_front));
      v_next := ARRAY(SELECT z.x FROM unnest(v_next) z(x) WHERE NOT z.x = ANY (v_seen));
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
            WHERE i.guest_principal_id = ANY (p) AND i.factor_value_norm NOT LIKE 'anon-%'
           UNION SELECT lower(x.username) FROM guest_access_accounts x
            WHERE x.id = ANY (ind_a) AND x.username NOT LIKE 'anon-%');

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
  -- The stay's PMS messages, and those that never resolved to a stay but name its reservation ON THE SAME PMS
  -- INTERFACE: a reservation number means something only inside its own interface (0145).
  RETURN QUERY SELECT 'stay_event'::text, ev.id, ev.processing_status FROM stay_events ev
                WHERE ev.tenant_id = p_tenant AND ev.site_id = p_site
                  AND (ev.stay_id = ANY (s)
                       OR (ev.stay_id IS NULL AND EXISTS (
                             SELECT 1 FROM stays st
                              WHERE st.id = ANY (s) AND st.external_reservation_id NOT LIKE 'anon-%'
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

-- ---------------------------------------------------------------------------------------------------------
-- 4. The search: a device identifies nobody.
-- ---------------------------------------------------------------------------------------------------------
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
       -- Not another site's guest (0144): an identity also used at another site is not offered here, as the
       -- scope refuses it.
       AND NOT EXISTS (SELECT 1 FROM auth_contexts ac WHERE ac.guest_principal_id = g.id AND ac.site_id <> p_site)
       AND NOT EXISTS (SELECT 1 FROM entitlements en WHERE en.guest_principal_id = g.id AND en.site_id <> p_site)
       AND (lower(COALESCE(g.display_name, '')) LIKE pat
            OR EXISTS (SELECT 1 FROM guest_principal_identities i
                        WHERE i.guest_principal_id = g.id AND i.factor_value_norm NOT LIKE 'anon-%'
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
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.external_reservation_id NOT LIKE 'anon-%'
       AND (lower(COALESCE(x.normalized_room_number, '')) = q OR lower(x.external_reservation_id) = q
            OR EXISTS (SELECT 1 FROM stay_guests sg
                        WHERE sg.stay_id = x.id
                          AND (lower(COALESCE(sg.display_name, '')) LIKE pat OR lower(COALESCE(sg.last_name_norm, '')) LIKE pat
                               OR lower(COALESCE(sg.first_name_norm, '')) LIKE pat)))
  ) r ORDER BY 6 DESC NULLS LAST LIMIT 50;
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 5. The export: the person's own data, the reservation's own data, and a note of what is shared.
-- ---------------------------------------------------------------------------------------------------------
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

-- ---------------------------------------------------------------------------------------------------------
-- 6. The erasure: only what is the person's or the reservation's own.
-- ---------------------------------------------------------------------------------------------------------
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
  IF cardinality(live) > 0 THEN
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
    || (COALESCE(p_public_counts, '{}'::jsonb) - '_erasure_ref');
  INSERT INTO guest_data_erasures (erasure_ref, tenant_id, site_id, subject_kind, erased_by, erase_reason, counts)
  VALUES (v_ref, p_tenant, p_site, p_kind, btrim(p_operator), btrim(p_reason), v_counts);
  RETURN jsonb_build_object('erasure_ref', v_ref, 'counts', v_counts);
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 6b. What identifies the guest, in three tiers (used to keep reasons and the activity log free of them).
-- ---------------------------------------------------------------------------------------------------------
--   strong: an account name, an identity value, a device address, a reservation number -- nothing but this guest has it
--   name:   a full name or display name
--   weak:   a room number, a first or last name alone -- shared by many people, so used only for a staff search
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
    WHERE t.v IS NOT NULL AND length(btrim(t.v)) >= 2 AND t.v NOT LIKE 'anon-%';
END $$;
REVOKE ALL ON FUNCTION iam_v2.guest_data_identifiers(uuid,uuid,text,uuid) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 7. The public halves: shared records are no one's identifiers in the export and the activity log.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.guest_data_export(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS jsonb
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'pg_catalog', 'public', 'pg_temp'
AS $function$
DECLARE v jsonb; v_ids text[]; v_vals text[]; v_macs text[];
        a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; v_ctx uuid[];
BEGIN
  v := iam_v2.guest_data_export_records(p_tenant, p_site, p_kind, p_id);
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
DECLARE v_ids text[]; v_vals text[]; v_strong text[]; v_names text[]; v_weak text[]; v_macs text[]; v_pats text[];
        v_since timestamptz; n_codes integer; n_oauth integer; n_audit integer; v_ref text; v_res jsonb;
        a uuid[]; p uuid[]; s uuid[]; d uuid[]; gr uuid[];
BEGIN
  -- What identifies the guest, read BEFORE anything is anonymised.
  SELECT COALESCE(array_agg(o_value) FILTER (WHERE o_kind = 'strong'), '{}'),
         COALESCE(array_agg(o_value) FILTER (WHERE o_kind = 'name'), '{}'),
         COALESCE(array_agg(o_value) FILTER (WHERE o_kind = 'weak'), '{}')
    INTO v_strong, v_names, v_weak FROM iam_v2.guest_data_identifiers(p_tenant, p_site, p_kind, p_id);
  PERFORM public.guest_data_reason_check(p_tenant, p_site, p_kind, p_id, p_reason);
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
  UPDATE social_oauth_states x SET client_ip = NULL, client_mac = NULL, user_agent = NULL
   WHERE x.tenant_id = p_tenant AND x.client_mac::text = ANY (v_macs);
  GET DIAGNOSTICS n_oauth = ROW_COUNT;

  -- THE ACTIVITY LOG KEEPS WHO AND WHEN (D-R2), AND TOUCHES NOTHING THAT IS NOT ABOUT THIS GUEST (0145). An entry
  -- about the guest's own records keeps its action, staff member and time; the record id gives way to the erasure
  -- reference and the guest's identifiers and name become "[erased]". Any other entry loses only a STRONG
  -- identifier (an address, a username, a reservation number, a device address): a room number, a first or last
  -- name alone, or a display name never rewrites an entry about something else (a VLAN 101, a Smith family
  -- request). What staff typed into the guest-data screens is read more widely, since staff typed it about
  -- someone: the full name there, and in a search any part of a name or room. A refusal below undoes this.
  v_ref := 'ER-' || upper(encode(public.gen_random_bytes(6), 'hex'));
  v_pats := ARRAY(SELECT '%' || replace(replace(replace(x, '\', '\\'), '%', '\%'), '_', '\_') || '%'
                    FROM unnest(v_strong || v_names || v_weak) x WHERE length(x) >= 3);
  WITH cand AS MATERIALIZED (
    SELECT l.ctid AS rid, l.payload AS old_payload, l.actor_type,
           (l.target_id = ANY (v_ids) OR lower(l.target_id) = ANY (v_strong)) AS about_it,
           CASE
             WHEN l.action = 'guest_data.searched' THEN iam_v2.redact_guest_text(l.payload, v_strong || v_names || v_weak, true)
             WHEN l.target_id = ANY (v_ids) OR lower(l.target_id) = ANY (v_strong) OR l.action LIKE 'guest_data.%'
               THEN iam_v2.redact_guest_text(l.payload, v_strong || v_names, false)
             ELSE iam_v2.redact_guest_text(l.payload, v_strong, false)
           END AS new_payload
      FROM audit_log l
     WHERE l.tenant_id = p_tenant AND l.ts >= v_since
       AND (l.target_id = ANY (v_ids) OR lower(l.target_id) = ANY (v_strong)
            OR l.action = 'guest_data.searched' OR lower(l.payload::text) LIKE ANY (v_pats)))
  UPDATE audit_log l
     SET payload = cand.new_payload || jsonb_build_object('erasure_ref', v_ref),
         target_id = CASE WHEN cand.about_it THEN NULL ELSE l.target_id END,
         ip = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.ip END,
         user_agent = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.user_agent END,
         actor_id = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.actor_id END
    FROM cand
   WHERE l.ctid = cand.rid AND (cand.about_it OR cand.new_payload IS DISTINCT FROM cand.old_payload);
  GET DIAGNOSTICS n_audit = ROW_COUNT;

  -- Refuses (and so undoes the above) while anything is live.
  v_res := iam_v2.erase_guest_records(p_tenant, p_site, p_kind, p_id, p_operator, p_reason,
             jsonb_build_object('verification_codes', n_codes, 'oauth_states', n_oauth, 'audit_entries', n_audit,
                                '_erasure_ref', v_ref));
  UPDATE auth_otps o SET destination = 'anonymised', ip = NULL, user_agent = NULL
   WHERE o.tenant_id = p_tenant AND lower(o.destination) = ANY (v_vals);
  RETURN v_res;
END $function$;

-- The helpers are internal: owned by iam_v2_owner, granted to nobody.
REVOKE ALL ON FUNCTION iam_v2.device_exclusive_to(uuid,uuid[],uuid[],uuid[],uuid[]),
                       iam_v2.same_guest(text,text,text,uuid,text,text,text,uuid),
                       iam_v2.event_payload_for_guests(jsonb,jsonb,boolean,uuid) FROM PUBLIC;
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER FUNCTION iam_v2.device_exclusive_to(uuid,uuid[],uuid[],uuid[],uuid[]) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.same_guest(text,text,text,uuid,text,text,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.event_payload_for_guests(jsonb,jsonb,boolean,uuid) OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- The scope reads which one-time codes went to the person's own addresses: three columns of public.auth_otps, for
-- iam_v2_owner and nothing else (mirrored in deploy/gatep/svc-guest-data-rights-grants.sql).
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'GRANT SELECT (id, tenant_id, destination) ON public.auth_otps TO iam_v2_owner';
  END IF;
END $g$;

-- Nothing calls the old signatures any more.
DROP FUNCTION iam_v2.same_guest(text,text,text,text,text,text);
DROP FUNCTION iam_v2.event_payload_for_guests(jsonb,jsonb,boolean);

COMMIT;
