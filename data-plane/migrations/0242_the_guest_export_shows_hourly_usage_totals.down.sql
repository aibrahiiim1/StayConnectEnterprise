-- 0242 down -- the export as 0147 defined it (raw samples only).

BEGIN;

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

COMMIT;
