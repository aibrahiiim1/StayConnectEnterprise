-- 0139 down: the inbox guard, retention and erasure as 0127/0137 left them. Events already given up stay given up.
BEGIN;
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
  IF TG_OP = 'UPDATE'
     AND current_setting('iam_v2.anonymise', true) = 'retention'
     AND current_user = (SELECT pg_get_userbyid(c.relowner) FROM pg_class c WHERE c.oid = TG_RELID) THEN
    IF OLD.processing_status = 'PENDING'
       OR (to_jsonb(NEW) - 'payload' - 'external_event_identity')
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
                AND (s.departure + 1)::timestamptz < v_cut))
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

CREATE OR REPLACE FUNCTION iam_v2.erase_guest_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text, p_public_counts jsonb)
 RETURNS jsonb
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; ev uuid[]; macs macaddr[]; live text[] := '{}';
        v_id uuid; v_shared integer; v_attempts integer; v_sessions integer; v_k integer; v_verif integer := 0;
        v_events integer := 0; v_ref text; v_counts jsonb;
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
  IF EXISTS (SELECT 1 FROM stay_events x WHERE x.id = ANY (ev) AND x.processing_status = 'PENDING') THEN
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
  -- their stay below). None is pending: that was refused above.
  PERFORM begin_controlled_operation('stay');
  UPDATE stay_events x
     SET payload = '{"anonymised": true}'::jsonb,
         external_event_identity = 'anon-' || encode(public.gen_random_bytes(32), 'hex')
   WHERE x.id = ANY (ev) AND x.stay_id IS NULL AND x.processing_status <> 'PENDING'
     AND x.payload IS DISTINCT FROM '{"anonymised": true}'::jsonb;
  GET DIAGNOSTICS v_events = ROW_COUNT;

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


DROP FUNCTION IF EXISTS iam_v2.give_up_waiting_stay_events(uuid, uuid, timestamptz, uuid[], integer);
COMMIT;
