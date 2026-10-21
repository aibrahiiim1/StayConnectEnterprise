-- 0146 -- ASSISTED GUEST DATA REQUESTS: staff-supervised export and anonymisation of internet-usage records that no
-- automatic rule can attribute to one person (GDPR remediation plan; Product-Owner decision D-R5 follow-up).
--
-- THE PROBLEM. Migration 0145 stopped a guest request from reaching access that several people share (a room's
-- credentials, a voucher, a staff-issued account, a device used with someone else): "room credentials prove
-- eligibility for internet access, not identity". That left a real person with a real right (GDPR Art. 15 and 17)
-- and no way to exercise it for the usage of THEIR OWN phone when the hotel only ever knew the room.
--
-- THE ANSWER IS NOT A WEAKER RULE. A MAC address, a room number, a reservation credential or a shared device is
-- still no proof of who a person is or what is theirs. This migration adds a SUPERVISED path in which:
--   1. staff verify the requester and write down how (a method, three statements, a note);
--   2. the database classifies every record in the stated range for the stated address(es) and context:
--        ATTRIBUTABLE  already this person's by 0145's rules (their own account or identity, or their exclusive device)
--        CLAIMABLE     the access of the stated context, on an address no other access or person has ever used,
--                      and staff made all the statements: selectable
--        SHARED        a voucher, open access, or the statements are missing: never selectable
--        CONFLICTING   the address was also used under another access or by another person, or the record belongs to
--                      another reservation or account: never selectable
--        UNVERIFIABLE  the record is not fully inside the range, or has no owner record: never selectable
--   3. staff review the exact records, select, and confirm with their password, a reason, and the request reference;
--   4. the database recomputes the classification under locks, refuses if ANYTHING in the review changed, and only
--      then discloses or anonymises exactly the selection. Records that are not selected are untouched.
-- Nothing here weakens 0145: the automatic functions are unchanged; this is a separate set of functions that never
-- calls the automatic erase, and it only ever clears addresses (as retention does), never entitlements, quotas,
-- accounting, PMS data or financial records.
--
-- WHAT IT DOES TO A RECORD (the same primitives retention uses): an ended session loses its IP and MAC; a sign-in
-- attempt, a verification access, a code binding, a sign-in restriction, a sign-in pass and a sign-in redirect lose
-- the device address (and room typed / IP); a device whose every record is inside the request and selected is
-- anonymised like an exclusive device. A partly processed device keeps its identity row: its other records are
-- other people's or outside the range.
--
-- Additive and reversible. Down: the functions go, open requests are cancelled and scrubbed; the tables stay because
-- they are the proof that a request ran.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The request, its anchors, what was done, and what happened.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.assisted_guest_requests (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  ref                     text NOT NULL CONSTRAINT agr_ref CHECK (ref ~ '^AR-[0-9A-F]{10}$'),
  tenant_id               uuid NOT NULL,
  site_id                 uuid NOT NULL,
  purpose                 text NOT NULL CONSTRAINT agr_purpose CHECK (purpose IN ('ACCESS', 'ERASURE')),
  macs                    macaddr[],
  range_from              timestamptz NOT NULL,
  range_to                timestamptz NOT NULL,
  pms_interface_id        uuid,
  requester_ref           text NOT NULL CONSTRAINT agr_requester CHECK (length(btrim(requester_ref)) BETWEEN 2 AND 120),
  verification_method     text NOT NULL CONSTRAINT agr_method
                            CHECK (verification_method IN ('ID_CHECKED_IN_PERSON', 'ACCOUNT_CONTROL_SHOWN', 'CALLBACK_TO_KNOWN_CONTACT', 'OTHER_DOCUMENTED')),
  evidence_note           text NOT NULL CONSTRAINT agr_evidence CHECK (length(btrim(evidence_note)) BETWEEN 9 AND 1000),
  attest_identity         boolean NOT NULL CONSTRAINT agr_identity CHECK (attest_identity),
  attest_device           boolean NOT NULL,
  attest_sole_use         boolean NOT NULL,
  attest_private_address  boolean NOT NULL,
  open_reason             text NOT NULL CONSTRAINT agr_open_reason CHECK (length(btrim(open_reason)) BETWEEN 4 AND 500),
  status                  text NOT NULL DEFAULT 'OPEN' CONSTRAINT agr_status CHECK (status IN ('OPEN', 'COMPLETED', 'CANCELLED')),
  created_by              text NOT NULL CONSTRAINT agr_creator CHECK (length(btrim(created_by)) BETWEEN 1 AND 200),
  created_at              timestamptz NOT NULL DEFAULT now(),
  closed_by               text,
  closed_at               timestamptz,
  close_reason            text,
  result                  jsonb,
  CONSTRAINT agr_ref_unique UNIQUE (ref),
  CONSTRAINT agr_range CHECK (range_to > range_from AND range_to - range_from <= interval '366 days'),
  -- The addresses and the free text live only while the request is open: closing scrubs them (D-R2's rule for the
  -- activity log, applied to the request file -- who, when, how verified, what was done and how many stay).
  CONSTRAINT agr_macs CHECK ((status = 'OPEN') = (macs IS NOT NULL) AND (macs IS NULL OR cardinality(macs) BETWEEN 1 AND 5)),
  CONSTRAINT agr_closed CHECK ((status = 'OPEN') = (closed_at IS NULL) AND (closed_at IS NULL) = (closed_by IS NULL)
                               AND (closed_at IS NULL) = (close_reason IS NULL) AND (closed_at IS NULL) = (result IS NULL))
);
CREATE INDEX IF NOT EXISTS agr_site ON iam_v2.assisted_guest_requests (tenant_id, site_id, created_at DESC);
COMMENT ON TABLE iam_v2.assisted_guest_requests IS
  'A staff-supervised guest data request about records no automatic rule can attribute (0146). While OPEN it holds the '
  'addresses, the requester''s reference and the evidence note; closing it scrubs all three. Written only by the definer functions.';

CREATE TABLE IF NOT EXISTS iam_v2.assisted_guest_request_anchors (
  request_id  uuid NOT NULL REFERENCES iam_v2.assisted_guest_requests (id),
  anchor_kind text NOT NULL CONSTRAINT agra_kind CHECK (anchor_kind IN ('account', 'principal', 'stay', 'stay_guest')),
  anchor_id   uuid NOT NULL,
  PRIMARY KEY (request_id, anchor_kind, anchor_id)
);
COMMENT ON TABLE iam_v2.assisted_guest_request_anchors IS
  'The reservation, guest, account or identity the requester says the records belong to (0146): a claim, never proof.';

CREATE TABLE IF NOT EXISTS iam_v2.assisted_guest_request_records (
  request_id uuid NOT NULL REFERENCES iam_v2.assisted_guest_requests (id),
  rec_kind   text NOT NULL,
  rec_id     uuid NOT NULL,
  rec_class  text NOT NULL CONSTRAINT agrr_class CHECK (rec_class IN ('ATTRIBUTABLE', 'CLAIMABLE')),
  action     text NOT NULL CONSTRAINT agrr_action CHECK (action IN ('DISCLOSED', 'ANONYMISED')),
  done_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (request_id, rec_kind, rec_id, action)
);
COMMENT ON TABLE iam_v2.assisted_guest_request_records IS
  'Which records a request disclosed or anonymised (0146): kind, id and class only; no personal data.';

CREATE TABLE IF NOT EXISTS iam_v2.assisted_guest_request_events (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  request_id uuid NOT NULL REFERENCES iam_v2.assisted_guest_requests (id),
  at         timestamptz NOT NULL DEFAULT now(),
  actor      text NOT NULL,
  action     text NOT NULL CONSTRAINT agre_action CHECK (action IN ('OPENED', 'EXPORTED', 'ERASED', 'COMPLETED', 'CANCELLED', 'EXPIRED')),
  detail     jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS agre_request ON iam_v2.assisted_guest_request_events (request_id, at);
COMMENT ON TABLE iam_v2.assisted_guest_request_events IS
  'What happened to an assisted request, in order (0146). Counts and reasons only. Append-only.';
DROP TRIGGER IF EXISTS assisted_guest_request_events_append_only ON iam_v2.assisted_guest_request_events;
CREATE TRIGGER assisted_guest_request_events_append_only BEFORE UPDATE OR DELETE ON iam_v2.assisted_guest_request_events
  FOR EACH ROW EXECUTE FUNCTION iam_v2.data_retention_append_only();

-- A request is deleted never, changed only while open, and then only through its status and the closing scrub.
CREATE OR REPLACE FUNCTION iam_v2.assisted_requests_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'an assisted guest data request is never deleted' USING ERRCODE = 'restrict_violation';
  END IF;
  IF OLD.status <> 'OPEN' THEN
    RAISE EXCEPTION 'a closed assisted guest data request does not change' USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.id, NEW.ref, NEW.tenant_id, NEW.site_id, NEW.purpose, NEW.range_from, NEW.range_to, NEW.pms_interface_id,
      NEW.verification_method, NEW.attest_identity, NEW.attest_device, NEW.attest_sole_use, NEW.attest_private_address,
      NEW.open_reason, NEW.created_by, NEW.created_at)
     IS DISTINCT FROM
     (OLD.id, OLD.ref, OLD.tenant_id, OLD.site_id, OLD.purpose, OLD.range_from, OLD.range_to, OLD.pms_interface_id,
      OLD.verification_method, OLD.attest_identity, OLD.attest_device, OLD.attest_sole_use, OLD.attest_private_address,
      OLD.open_reason, OLD.created_by, OLD.created_at) THEN
    RAISE EXCEPTION 'what an assisted guest data request states cannot be edited' USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS assisted_guest_requests_guard ON iam_v2.assisted_guest_requests;
CREATE TRIGGER assisted_guest_requests_guard BEFORE UPDATE OR DELETE ON iam_v2.assisted_guest_requests
  FOR EACH ROW EXECUTE FUNCTION iam_v2.assisted_requests_guard();

-- The erasure register also lists an assisted erasure (its kind is 'assisted').
ALTER TABLE iam_v2.guest_data_erasures DROP CONSTRAINT IF EXISTS gde_kind;
ALTER TABLE iam_v2.guest_data_erasures ADD CONSTRAINT gde_kind
  CHECK (subject_kind = ANY (ARRAY['account', 'principal', 'stay', 'stay_guest', 'assisted']));

-- ---------------------------------------------------------------------------------------------------------
-- 2. Small helpers.
-- ---------------------------------------------------------------------------------------------------------
-- A private (randomised, locally administered) address: the phone chose it; it names a network profile, not a person.
CREATE OR REPLACE FUNCTION iam_v2.mac_is_private(p_mac macaddr) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    AS $$ SELECT (get_byte(decode(replace(p_mac::text, ':', ''), 'hex'), 0) & 2) <> 0 $$;

-- Which class a record falls in, from who it belongs to and what staff stated.
--   subject: attr (the person's own account or identity), ctx (the access of the stated context), none (an event
--   keyed only by the address), untied (keyed only by an address that nothing ties to the stated context), shared
--   (voucher or open access), other (another reservation, account or person), unk (no owner record at all).
CREATE OR REPLACE FUNCTION iam_v2.assisted_class(p_subj text, p_inside boolean, p_claim_ok boolean, p_priv_ok boolean, p_state text)
    RETURNS text[]
    LANGUAGE sql IMMUTABLE
    AS $$
  SELECT CASE
    WHEN NOT p_inside THEN ARRAY['UNVERIFIABLE', 'straddles_range']
    WHEN p_subj = 'attr' THEN ARRAY['ATTRIBUTABLE', 'own_account_or_identity']
    WHEN p_subj = 'shared' THEN ARRAY['SHARED', 'voucher_or_open_access']
    WHEN p_subj = 'multi' THEN ARRAY['SHARED', 'room_shared_by_named_guests']
    WHEN p_subj = 'other' THEN ARRAY['CONFLICTING', 'another_person_or_access']
    WHEN p_subj = 'untied' THEN ARRAY['UNVERIFIABLE', 'not_tied_to_the_stated_context']
    WHEN p_subj IN ('ctx', 'none') THEN
      CASE WHEN NOT p_claim_ok THEN ARRAY['SHARED', 'statements_missing']
           WHEN NOT p_priv_ok THEN ARRAY['SHARED', 'private_address_not_confirmed']
           WHEN p_state IS NOT NULL THEN ARRAY['CONFLICTING', p_state]
           WHEN p_subj = 'ctx' THEN ARRAY['CLAIMABLE', 'access_of_the_stated_context']
           ELSE ARRAY['CLAIMABLE', 'used_on_the_stated_device'] END
    ELSE ARRAY['UNVERIFIABLE', 'no_owner_record'] END
$$;

-- Has anyone but the stated context ever used this address? NULL when nobody has. p_devices is EVERY device row of
-- the address on this site: an address can have more than one (one per appliance that saw it), and the answer is about
-- the address, not about whichever row was found first.
--   device_used_by_another_person       a remembered device, an account created on it, a sign-in, a verified address or
--                                       a PMS-matched guest who is not among the stated anchors
--   device_used_under_another_access    a session, an authorisation, a slot action or a sign-in of another reservation,
--                                       a voucher, open access or another account -- present or past
-- Any time in the retained history counts: a returning guest adds the earlier stay as a second anchor.
CREATE OR REPLACE FUNCTION iam_v2.assisted_device_state(p_devices uuid[], p_mac macaddr, p_tenant uuid, p_site uuid,
    p_allowed uuid[], p_stays uuid[], p_guests uuid[], p_p uuid[], p_a uuid[], p_ch uuid[])
    RETURNS text
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT CASE
    WHEN EXISTS (SELECT 1 FROM principal_device_credentials c WHERE c.device_id = ANY (p_devices) AND NOT c.guest_principal_id = ANY (p_p))
      OR EXISTS (SELECT 1 FROM auth_contexts ac WHERE ac.device_id = ANY (p_devices) AND ac.guest_principal_id IS NOT NULL
                    AND NOT ac.guest_principal_id = ANY (p_p))
      OR EXISTS (SELECT 1 FROM auth_contexts ac JOIN guest_access_accounts g ON g.id = ac.guest_account_id
                  WHERE ac.device_id = ANY (p_devices) AND g.self_registered AND NOT g.id = ANY (p_a))
      OR EXISTS (SELECT 1 FROM guest_access_accounts g
                  WHERE g.tenant_id = p_tenant AND g.site_id = p_site AND g.created_by_device_mac = p_mac AND NOT g.id = ANY (p_a))
      OR EXISTS (SELECT 1 FROM verification_access_grants gr
                  WHERE gr.tenant_id = p_tenant AND gr.site_id = p_site AND gr.device_mac = p_mac
                    AND (gr.challenge_id IS NULL OR NOT gr.challenge_id = ANY (p_ch)))
      OR EXISTS (SELECT 1 FROM otp_device_bindings b WHERE b.tenant_id = p_tenant AND b.device_mac = p_mac AND NOT b.challenge_id = ANY (p_ch))
      -- The PMS matched a sign-in attempt on this address to a guest who is not the one stated.
      OR EXISTS (SELECT 1 FROM sign_in_attempts sa
                  WHERE sa.tenant_id = p_tenant AND sa.site_id = p_site AND sa.device_mac = p_mac
                    AND sa.matched_guest_id IS NOT NULL AND NOT sa.matched_guest_id = ANY (p_guests))
      THEN 'device_used_by_another_person'
    WHEN EXISTS (SELECT 1 FROM sessions se WHERE se.device_id = ANY (p_devices) AND NOT se.entitlement_id = ANY (p_allowed))
      OR EXISTS (SELECT 1 FROM entitlement_devices ed WHERE ed.device_id = ANY (p_devices) AND NOT ed.entitlement_id = ANY (p_allowed))
      -- Past use, left behind when a binding is released: the authorisation history and the slot actions.
      OR EXISTS (SELECT 1 FROM entitlement_device_authorizations a WHERE a.device_id = ANY (p_devices) AND NOT a.entitlement_id = ANY (p_allowed))
      OR EXISTS (SELECT 1 FROM device_slot_actions a
                  WHERE (a.device_id = ANY (p_devices) OR a.replacement_device_id = ANY (p_devices))
                    AND (a.entitlement_id IS NULL OR NOT a.entitlement_id = ANY (p_allowed)))
      OR EXISTS (SELECT 1 FROM guest_device_actions a WHERE a.device_id = ANY (p_devices) AND NOT a.entitlement_id = ANY (p_allowed))
      OR EXISTS (SELECT 1 FROM auth_contexts ac WHERE ac.device_id = ANY (p_devices)
                    AND ((ac.stay_id IS NOT NULL AND NOT ac.stay_id = ANY (p_stays))
                         OR ac.voucher_id IS NOT NULL OR ac.anonymous_subject_id IS NOT NULL OR ac.post_stay_profile_id IS NOT NULL
                         OR (ac.guest_account_id IS NOT NULL AND NOT ac.guest_account_id = ANY (p_a))))
      OR EXISTS (SELECT 1 FROM sign_in_attempts sa
                  WHERE sa.tenant_id = p_tenant AND sa.site_id = p_site AND sa.device_mac = p_mac
                    AND ((sa.entitlement_id IS NOT NULL AND NOT sa.entitlement_id = ANY (p_allowed))
                         OR (sa.matched_stay_id IS NOT NULL AND NOT sa.matched_stay_id = ANY (p_stays))))
      THEN 'device_used_under_another_access'
    END
$$;

-- Is this address tied to the stated context by anything at all? A record keyed only by the address (a failed
-- attempt, a restriction) belongs to nobody in particular; with nothing on the address that ties it to the reservation
-- or account the requester named, the stated context says nothing about it.
CREATE OR REPLACE FUNCTION iam_v2.assisted_device_tied(p_devices uuid[], p_mac macaddr, p_tenant uuid, p_site uuid,
    p_allowed uuid[], p_stays uuid[], p_p uuid[], p_a uuid[], p_ch uuid[])
    RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT EXISTS (SELECT 1 FROM sessions se WHERE se.device_id = ANY (p_devices) AND se.entitlement_id = ANY (p_allowed))
      OR EXISTS (SELECT 1 FROM entitlement_devices ed WHERE ed.device_id = ANY (p_devices) AND ed.entitlement_id = ANY (p_allowed))
      OR EXISTS (SELECT 1 FROM auth_contexts ac WHERE ac.device_id = ANY (p_devices)
                    AND (ac.stay_id = ANY (p_stays) OR ac.guest_account_id = ANY (p_a) OR ac.guest_principal_id = ANY (p_p)))
      OR EXISTS (SELECT 1 FROM principal_device_credentials c WHERE c.device_id = ANY (p_devices) AND c.guest_principal_id = ANY (p_p))
      OR EXISTS (SELECT 1 FROM verification_access_grants gr
                  WHERE gr.tenant_id = p_tenant AND gr.site_id = p_site AND gr.device_mac = p_mac AND gr.challenge_id = ANY (p_ch))
      OR EXISTS (SELECT 1 FROM sign_in_attempts sa
                  WHERE sa.tenant_id = p_tenant AND sa.site_id = p_site AND sa.device_mac = p_mac
                    AND (sa.entitlement_id = ANY (p_allowed) OR sa.matched_stay_id = ANY (p_stays)))
$$;

-- Is every record of this device inside the request's range? Only then may the device row itself be anonymised.
CREATE OR REPLACE FUNCTION iam_v2.assisted_device_whole(p_device uuid, p_mac macaddr, p_tenant uuid, p_site uuid, p_from timestamptz, p_to timestamptz)
    RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT NOT EXISTS (SELECT 1 FROM sessions se WHERE se.device_id = p_device
                        AND NOT (se.started >= p_from AND se.ended IS NOT NULL AND se.ended <= p_to))
     AND NOT EXISTS (SELECT 1 FROM sign_in_attempts sa WHERE sa.tenant_id = p_tenant AND sa.site_id = p_site AND sa.device_mac = p_mac
                        AND NOT (sa.occurred_at >= p_from AND sa.occurred_at < p_to))
     AND NOT EXISTS (SELECT 1 FROM verification_access_grants g WHERE g.tenant_id = p_tenant AND g.site_id = p_site AND g.device_mac = p_mac
                        AND NOT (g.started_at >= p_from AND g.started_at < p_to AND g.ended_at IS NOT NULL AND g.ended_at <= p_to))
     AND NOT EXISTS (SELECT 1 FROM otp_device_bindings b WHERE b.tenant_id = p_tenant AND b.device_mac = p_mac
                        AND NOT (b.issued_at >= p_from AND b.issued_at < p_to))
     AND NOT EXISTS (SELECT 1 FROM guest_signin_restrictions x WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = p_mac
                        AND NOT (COALESCE(x.restricted_at, x.created_at) >= p_from AND COALESCE(x.restricted_at, x.created_at) < p_to))
     AND NOT EXISTS (SELECT 1 FROM guest_signin_device_passes x WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = p_mac
                        AND NOT (x.granted_at >= p_from AND x.granted_at < p_to))
     AND NOT EXISTS (SELECT 1 FROM public.social_oauth_states x WHERE x.tenant_id = p_tenant AND x.client_mac = p_mac
                        AND NOT (x.created_at >= p_from AND x.created_at < p_to))
     AND NOT EXISTS (SELECT 1 FROM entitlement_devices ed WHERE ed.device_id = p_device
                        AND NOT (COALESCE(ed.first_authorized, p_from) >= p_from AND COALESCE(ed.last_authorized, p_from) <= p_to))
     AND NOT EXISTS (SELECT 1 FROM devices dv WHERE dv.id = p_device
                        AND ((dv.first_seen IS NOT NULL AND dv.first_seen < p_from) OR (dv.last_seen IS NOT NULL AND dv.last_seen > p_to)))
$$;

-- ---------------------------------------------------------------------------------------------------------
-- 3. The context the requester states: what is theirs by rule, what is the access of the stated reservation or
--    account, and what staff have stated.
-- ---------------------------------------------------------------------------------------------------------
DROP FUNCTION IF EXISTS iam_v2.assisted_context(uuid, uuid, uuid);
CREATE OR REPLACE FUNCTION iam_v2.assisted_context(p_tenant uuid, p_site uuid, p_request uuid)
    RETURNS TABLE(c_attr uuid[], c_ctx uuid[], c_ch uuid[], c_excl uuid[], c_p uuid[], c_a uuid[], c_stays uuid[], c_guests uuid[], c_multi uuid[],
                  c_claim_ok boolean, c_priv_ok boolean)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE r record; x record; v_attr uuid[] := '{}'; v_cred uuid[] := '{}'; v_ch uuid[] := '{}'; v_excl uuid[] := '{}';
        v_p uuid[] := '{}'; v_a uuid[] := '{}'; v_stays uuid[] := '{}'; v_ctx uuid[]; v_guests uuid[] := '{}'; v_gr uuid[]; v_multi uuid[];
BEGIN
  SELECT * INTO r FROM assisted_guest_requests q WHERE q.id = p_request AND q.tenant_id = p_tenant AND q.site_id = p_site;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'assisted request not found' USING ERRCODE = 'no_data_found';
  END IF;
  FOR x IN SELECT a.anchor_kind, a.anchor_id FROM assisted_guest_request_anchors a WHERE a.request_id = p_request LOOP
    SELECT v_attr || COALESCE(array_agg(o_id) FILTER (WHERE o_kind = 'entitlement'), '{}'),
           v_cred || COALESCE(array_agg(o_id) FILTER (WHERE o_kind = 'entitlement_credential'), '{}'),
           v_ch   || COALESCE(array_agg(o_id) FILTER (WHERE o_kind = 'challenge'), '{}'),
           v_excl || COALESCE(array_agg(o_id) FILTER (WHERE o_kind = 'device'), '{}'),
           v_p    || COALESCE(array_agg(o_id) FILTER (WHERE o_kind = 'principal'), '{}'),
           v_a    || COALESCE(array_agg(o_id) FILTER (WHERE o_kind = 'account'), '{}'),
           v_stays || COALESCE(array_agg(o_id) FILTER (WHERE o_kind IN ('stay', 'stay_shared')), '{}'),
           COALESCE(array_agg(o_id) FILTER (WHERE o_kind = 'stay_guest'), '{}')
      INTO v_attr, v_cred, v_ch, v_excl, v_p, v_a, v_stays, v_gr
      FROM guest_data_scope(p_tenant, p_site, x.anchor_kind, x.anchor_id);
    v_guests := v_guests || v_gr;
  END LOOP;
  -- The PMS guest rows the requester can be: the named guest of a stay with one named guest, or the stated guest of a
  -- shared one. On a shared stay named by the stay alone nobody is, so nothing the PMS matched to a guest there is theirs.
  v_guests := v_guests || COALESCE((SELECT array_agg(sg.id) FROM stay_guests sg
                                     WHERE sg.stay_id = ANY (v_stays) AND stay_named_guests(sg.stay_id) <= 1), '{}');
  -- A reservation that names more than one guest shares its room between identified people: the PMS itself says two
  -- persons hold the credentials, and a statement can not say whose phone is whose. Its room access is shared, never
  -- claimable (as 0145 keeps a shared stay whole).
  v_multi := COALESCE((SELECT array_agg(st) FROM unnest(v_stays) st WHERE stay_named_guests(st) > 1), '{}');
  v_stays := ARRAY(SELECT st FROM unnest(v_stays) st WHERE NOT st = ANY (v_multi));
  -- The access of the stated context: the room's entitlements of the single-guest reservations in scope, and the
  -- entitlements of a staff-issued account. Shared by nature; claimable only on staff's statements.
  SELECT COALESCE(array_agg(en.id), '{}') INTO v_ctx FROM entitlements en
   WHERE en.tenant_id = p_tenant AND en.site_id = p_site AND (en.stay_id = ANY (v_stays) OR en.id = ANY (v_cred));
  RETURN QUERY SELECT v_attr, v_ctx, v_ch, v_excl, v_p, v_a, v_stays, v_guests,
    COALESCE((SELECT array_agg(en.id) FROM entitlements en WHERE en.tenant_id = p_tenant AND en.site_id = p_site AND en.stay_id = ANY (v_multi)), '{}'),
    (r.attest_identity AND r.attest_device AND r.attest_sole_use),
    (r.attest_private_address OR NOT EXISTS (SELECT 1 FROM unnest(r.macs) m WHERE mac_is_private(m)));
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 4. The review: every record of the stated addresses in the stated range, and what class it is.
-- ---------------------------------------------------------------------------------------------------------
-- Kinds: session, sign_in_attempt, verification_access, code_device_binding, sign_in_restriction, sign_in_pass,
-- sign_in_redirect, device. o_erase_block says why an erasure would be refused for that record now (recent: newer
-- than a day, which the sign-in protection counters may still use; live: something is in use). It never blocks a
-- disclosure.
CREATE OR REPLACE FUNCTION iam_v2.assisted_request_review(p_tenant uuid, p_site uuid, p_request uuid)
    RETURNS TABLE(o_kind text, o_id uuid, o_at timestamptz, o_device uuid, o_class text, o_reason text, o_erase_block text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE r record; cx record; v_n integer; v_state jsonb; v_dev jsonb; v_tied jsonb; v_allowed uuid[];
BEGIN
  SELECT * INTO r FROM assisted_guest_requests q WHERE q.id = p_request AND q.tenant_id = p_tenant AND q.site_id = p_site;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'assisted request not found' USING ERRCODE = 'no_data_found';
  END IF;
  IF r.status <> 'OPEN' THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'closed';
  END IF;
  SELECT * INTO cx FROM assisted_context(p_tenant, p_site, p_request);
  v_allowed := cx.c_attr || cx.c_ctx;

  -- A bounded review: a person can read it. A year of a busy address is more than that; narrow the range.
  SELECT (SELECT count(*) FROM sessions se JOIN devices dv ON dv.id = se.device_id
           WHERE se.tenant_id = p_tenant AND se.site_id = p_site AND dv.mac = ANY (r.macs)
             AND se.started < r.range_to AND COALESCE(se.ended, 'infinity'::timestamptz) > r.range_from)
       + (SELECT count(*) FROM sign_in_attempts sa
           WHERE sa.tenant_id = p_tenant AND sa.site_id = p_site AND sa.device_mac = ANY (r.macs)
             AND sa.occurred_at >= r.range_from AND sa.occurred_at < r.range_to)
       + (SELECT count(*) FROM verification_access_grants g
           WHERE g.tenant_id = p_tenant AND g.site_id = p_site AND g.device_mac = ANY (r.macs)
             AND g.started_at >= r.range_from AND g.started_at < r.range_to)
    INTO v_n;
  IF v_n > 2000 THEN
    RAISE EXCEPTION 'these addresses have more than 2000 records in this range: choose a shorter range'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;

  -- What each stated address looks like: has anyone but the stated context ever used it, and does anything tie it to the
  -- stated context? Both are answered about the ADDRESS, over every device row it has on this site.
  SELECT COALESCE(jsonb_object_agg(m::text, to_jsonb(assisted_device_state(d.ids, m, p_tenant, p_site, v_allowed, cx.c_stays, cx.c_guests,
                                                                          cx.c_p, cx.c_a, cx.c_ch))), '{}'::jsonb),
         COALESCE(jsonb_object_agg(m::text, to_jsonb(d.ids[1]::text)) FILTER (WHERE cardinality(d.ids) > 0), '{}'::jsonb),
         COALESCE(jsonb_object_agg(m::text, to_jsonb(assisted_device_tied(d.ids, m, p_tenant, p_site, v_allowed, cx.c_stays,
                                                                         cx.c_p, cx.c_a, cx.c_ch))), '{}'::jsonb)
    INTO v_state, v_dev, v_tied
    FROM unnest(r.macs) m
    CROSS JOIN LATERAL (SELECT COALESCE(array_agg(dv.id ORDER BY dv.id), '{}') AS ids FROM devices dv
                         WHERE dv.tenant_id = p_tenant AND dv.site_id = p_site AND dv.mac = m) d;

  RETURN QUERY
  SELECT 'session'::text, se.id, se.started, se.device_id, k.v[1], k.v[2],
         CASE WHEN se.ended IS NOT NULL AND se.ended > now() - interval '24 hours' THEN 'recent' END
    FROM sessions se
    JOIN devices dv ON dv.id = se.device_id
    JOIN entitlements en ON en.id = se.entitlement_id
   CROSS JOIN LATERAL assisted_class(
          CASE WHEN se.entitlement_id = ANY (cx.c_attr) THEN 'attr'
               WHEN se.entitlement_id = ANY (cx.c_ctx) THEN 'ctx'
               WHEN se.entitlement_id = ANY (cx.c_multi) THEN 'multi'
               WHEN en.voucher_id IS NOT NULL OR en.anonymous_subject_id IS NOT NULL THEN 'shared'
               ELSE 'other' END,
          se.ended IS NOT NULL AND se.started >= r.range_from AND se.ended <= r.range_to,
          cx.c_claim_ok, cx.c_priv_ok, v_state ->> dv.mac::text) k(v)
   WHERE se.tenant_id = p_tenant AND se.site_id = p_site AND dv.mac = ANY (r.macs)
     AND se.started < r.range_to AND COALESCE(se.ended, 'infinity'::timestamptz) > r.range_from;

  RETURN QUERY
  SELECT 'sign_in_attempt'::text, sa.id, sa.occurred_at, (v_dev ->> sa.device_mac::text)::uuid, k.v[1], k.v[2],
         CASE WHEN sa.occurred_at > now() - interval '24 hours' THEN 'recent' END
    FROM sign_in_attempts sa
   CROSS JOIN LATERAL assisted_class(
          CASE WHEN sa.entitlement_id = ANY (cx.c_attr) THEN 'attr'
               WHEN sa.entitlement_id = ANY (cx.c_ctx) OR sa.matched_stay_id = ANY (cx.c_stays) THEN
                    CASE WHEN sa.matched_guest_id IS NOT NULL AND NOT sa.matched_guest_id = ANY (cx.c_guests) THEN 'other' ELSE 'ctx' END
               WHEN sa.entitlement_id IS NOT NULL OR sa.matched_stay_id IS NOT NULL OR sa.matched_guest_id IS NOT NULL THEN 'other'
               WHEN (v_tied ->> sa.device_mac::text) IS DISTINCT FROM 'true' THEN 'untied'
               ELSE 'none' END,
          true, cx.c_claim_ok, cx.c_priv_ok, v_state ->> sa.device_mac::text) k(v)
   WHERE sa.tenant_id = p_tenant AND sa.site_id = p_site AND sa.device_mac = ANY (r.macs)
     AND sa.occurred_at >= r.range_from AND sa.occurred_at < r.range_to
     AND num_nonnulls(sa.submitted_room, sa.device_ip, sa.device_mac, sa.sensitive_ciphertext) > 0;

  -- A verification access and a code binding are tied to a person by the address their one-time code went to, never
  -- by the device: theirs only if that code is among the stated person's own.
  RETURN QUERY
  SELECT 'verification_access'::text, g.id, g.started_at, (v_dev ->> g.device_mac::text)::uuid, k.v[1], k.v[2],
         CASE WHEN g.ended_at IS NULL THEN 'live' WHEN g.started_at > now() - interval '24 hours' THEN 'recent' END
    FROM verification_access_grants g
   CROSS JOIN LATERAL assisted_class(
          CASE WHEN g.challenge_id = ANY (cx.c_ch) THEN 'attr' WHEN g.challenge_id IS NULL THEN 'unk' ELSE 'other' END,
          g.ended_at IS NOT NULL AND g.ended_at <= r.range_to, cx.c_claim_ok, cx.c_priv_ok, NULL) k(v)
   WHERE g.tenant_id = p_tenant AND g.site_id = p_site AND g.device_mac = ANY (r.macs)
     AND g.started_at >= r.range_from AND g.started_at < r.range_to;

  RETURN QUERY
  SELECT 'code_device_binding'::text, b.challenge_id, b.issued_at, (v_dev ->> b.device_mac::text)::uuid, k.v[1], k.v[2],
         CASE WHEN b.issued_at > now() - interval '24 hours' THEN 'recent' END
    FROM otp_device_bindings b
   CROSS JOIN LATERAL assisted_class(
          CASE WHEN b.challenge_id = ANY (cx.c_ch) THEN 'attr' ELSE 'other' END,
          true, cx.c_claim_ok, cx.c_priv_ok, NULL) k(v)
   WHERE b.tenant_id = p_tenant AND b.device_mac = ANY (r.macs)
     AND b.issued_at >= r.range_from AND b.issued_at < r.range_to;

  RETURN QUERY
  SELECT 'sign_in_restriction'::text, x.id, COALESCE(x.restricted_at, x.created_at), (v_dev ->> x.device_mac::text)::uuid, k.v[1], k.v[2],
         CASE WHEN x.restricted_at IS NOT NULL AND x.released_at IS NULL AND x.expires_at >= now() THEN 'live'
              WHEN COALESCE(x.restricted_at, x.created_at) > now() - interval '24 hours' THEN 'recent' END
    FROM guest_signin_restrictions x
   CROSS JOIN LATERAL assisted_class(CASE WHEN (v_tied ->> x.device_mac::text) IS DISTINCT FROM 'true' THEN 'untied' ELSE 'none' END,
                                     true, cx.c_claim_ok, cx.c_priv_ok, v_state ->> x.device_mac::text) k(v)
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (r.macs)
     AND COALESCE(x.restricted_at, x.created_at) >= r.range_from AND COALESCE(x.restricted_at, x.created_at) < r.range_to;

  RETURN QUERY
  SELECT 'sign_in_pass'::text, x.id, x.granted_at, (v_dev ->> x.device_mac::text)::uuid, k.v[1], k.v[2],
         -- A pass lets a device try again while its pause lasts: anonymising it would end it early.
         CASE WHEN pz.ended_at IS NULL AND pz.expires_at > now() THEN 'live'
              WHEN x.granted_at > now() - interval '24 hours' THEN 'recent' END
    FROM guest_signin_device_passes x
    LEFT JOIN guest_signin_network_pauses pz ON pz.id = x.pause_id
   CROSS JOIN LATERAL assisted_class(CASE WHEN (v_tied ->> x.device_mac::text) IS DISTINCT FROM 'true' THEN 'untied' ELSE 'none' END,
                                     true, cx.c_claim_ok, cx.c_priv_ok, v_state ->> x.device_mac::text) k(v)
   WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (r.macs)
     AND x.granted_at >= r.range_from AND x.granted_at < r.range_to;

  -- A social sign-in redirect is keyed by the device's address, not by a record id: its id is a digest of its state.
  RETURN QUERY
  SELECT 'sign_in_redirect'::text, md5(x.state)::uuid, x.created_at, (v_dev ->> x.client_mac::text)::uuid, k.v[1], k.v[2],
         CASE WHEN x.created_at > now() - interval '24 hours' THEN 'recent' END
    FROM public.social_oauth_states x
   CROSS JOIN LATERAL assisted_class(CASE WHEN (v_tied ->> x.client_mac::text) IS DISTINCT FROM 'true' THEN 'untied' ELSE 'none' END,
                                     true, cx.c_claim_ok, cx.c_priv_ok, v_state ->> x.client_mac::text) k(v)
   WHERE x.tenant_id = p_tenant AND x.client_mac = ANY (r.macs)
     AND x.created_at >= r.range_from AND x.created_at < r.range_to
     AND num_nonnulls(x.client_ip, x.client_mac, x.user_agent) > 0;

  -- The device row itself is offered only when every record of the device is inside the range: otherwise anonymising
  -- it would reach records the request did not state.
  RETURN QUERY
  SELECT 'device'::text, dv.id, COALESCE(dv.last_seen, dv.first_seen), dv.id, k.v[1],
         CASE WHEN NOT w.whole THEN 'device_has_records_outside_the_request' ELSE k.v[2] END,
         CASE WHEN EXISTS (SELECT 1 FROM entitlement_devices ed JOIN entitlements en ON en.id = ed.entitlement_id
                            WHERE ed.device_id = dv.id AND en.status <> 'TERMINATED')
                OR EXISTS (SELECT 1 FROM sessions se WHERE se.device_id = dv.id AND se.ended IS NULL)
                OR EXISTS (SELECT 1 FROM guest_signin_restrictions x WHERE x.tenant_id = p_tenant AND x.site_id = p_site
                            AND x.device_mac = dv.mac AND x.restricted_at IS NOT NULL AND x.released_at IS NULL AND x.expires_at >= now())
                OR EXISTS (SELECT 1 FROM verification_access_grants g WHERE g.tenant_id = p_tenant AND g.site_id = p_site
                            AND g.device_mac = dv.mac AND g.ended_at IS NULL)
              THEN 'live'
              WHEN dv.last_seen > now() - interval '24 hours' THEN 'recent' END
    FROM devices dv
   CROSS JOIN LATERAL (SELECT assisted_device_whole(dv.id, dv.mac, p_tenant, p_site, r.range_from, r.range_to) AS whole) w
   CROSS JOIN LATERAL assisted_class(CASE WHEN dv.id = ANY (cx.c_excl) THEN 'attr'
                                              WHEN (v_tied ->> dv.mac::text) IS DISTINCT FROM 'true' THEN 'untied' ELSE 'ctx' END, w.whole,
                                     cx.c_claim_ok, cx.c_priv_ok, v_state ->> dv.mac::text) k(v)
   WHERE dv.tenant_id = p_tenant AND dv.site_id = p_site AND dv.mac = ANY (r.macs);
END $function$;
COMMENT ON FUNCTION iam_v2.assisted_request_review(uuid, uuid, uuid) IS
  'Classifies every record of an open assisted request (0146). Only ATTRIBUTABLE and CLAIMABLE records can ever be '
  'selected; a MAC address, a room or a shared device identifies nobody on its own.';

-- What a record would disclose (used for the review screen and the export, only for records that can be selected).
CREATE OR REPLACE FUNCTION iam_v2.assisted_record_detail(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
    RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT CASE p_kind
    WHEN 'session' THEN (SELECT jsonb_build_object(
        'started', se.started, 'ended', se.ended, 'end_reason', se.end_reason, 'ip', se.ip, 'mac_address', se.mac,
        'bytes_up', se.bytes_up, 'bytes_down', se.bytes_down,
        'usage_samples', (SELECT COALESCE(jsonb_agg(jsonb_build_object('at', ar.sampled_at, 'bytes_up', ar.bytes_up,
                                                                       'bytes_down', ar.bytes_down) ORDER BY ar.sampled_at), '[]'::jsonb)
                            FROM accounting_records ar WHERE ar.session_id = se.id))
      FROM sessions se WHERE se.id = p_id AND se.tenant_id = p_tenant AND se.site_id = p_site)
    WHEN 'sign_in_attempt' THEN (SELECT jsonb_build_object(
        'at', sa.occurred_at, 'room_entered', sa.submitted_room, 'checked_against', sa.verifier_kind, 'result', sa.result,
        'guest_network', sa.guest_network_name, 'device_ip', sa.device_ip, 'device_mac_address', sa.device_mac)
      FROM sign_in_attempts sa WHERE sa.id = p_id AND sa.tenant_id = p_tenant AND sa.site_id = p_site)
    WHEN 'verification_access' THEN (SELECT jsonb_build_object(
        'started_at', g.started_at, 'expires_at', g.expires_at, 'ended_at', g.ended_at, 'end_reason', g.end_reason,
        'ip', g.ip, 'device_mac_address', g.device_mac, 'used_bytes', g.used_bytes)
      FROM verification_access_grants g WHERE g.id = p_id AND g.tenant_id = p_tenant AND g.site_id = p_site)
    WHEN 'code_device_binding' THEN (SELECT jsonb_build_object('issued_at', b.issued_at, 'device_mac_address', b.device_mac)
      FROM otp_device_bindings b WHERE b.challenge_id = p_id AND b.tenant_id = p_tenant)
    WHEN 'sign_in_restriction' THEN (SELECT jsonb_build_object(
        'device_mac_address', x.device_mac, 'guest_network', x.guest_network_name, 'room_entered', x.last_submitted_room,
        'restricted_at', x.restricted_at, 'until', x.expires_at, 'failures', x.failure_count, 'reason', x.reason,
        'released_at', x.released_at)
      FROM guest_signin_restrictions x WHERE x.id = p_id AND x.tenant_id = p_tenant AND x.site_id = p_site)
    WHEN 'sign_in_pass' THEN (SELECT jsonb_build_object('device_mac_address', x.device_mac, 'granted_at', x.granted_at)
      FROM guest_signin_device_passes x WHERE x.id = p_id AND x.tenant_id = p_tenant AND x.site_id = p_site)
    WHEN 'sign_in_redirect' THEN (SELECT jsonb_build_object(
        'provider', x.provider, 'from_ip', x.client_ip, 'device_mac_address', x.client_mac, 'browser', x.user_agent,
        'started_at', x.created_at, 'completed_at', x.consumed_at)
      FROM public.social_oauth_states x WHERE md5(x.state)::uuid = p_id AND x.tenant_id = p_tenant)
    WHEN 'device' THEN (SELECT jsonb_build_object(
        'mac_address', dv.mac, 'first_seen', dv.first_seen, 'last_seen', dv.last_seen, 'last_ip', dv.last_ip,
        'networks_seen_on', (SELECT COALESCE(jsonb_agg(jsonb_build_object('guest_network', na.guest_network_id,
                                'first_seen', na.first_seen, 'last_seen', na.last_seen) ORDER BY na.first_seen), '[]'::jsonb)
                               FROM device_network_appearances na WHERE na.device_id = dv.id))
      FROM devices dv WHERE dv.id = p_id AND dv.tenant_id = p_tenant AND dv.site_id = p_site)
  END
$$;

-- The review as one document: the records with their class and (for the ones that can be selected) exactly what each
-- would disclose, and a signature of the whole review that the confirming call must present unchanged.
CREATE OR REPLACE FUNCTION iam_v2.assisted_review_signature(p_tenant uuid, p_site uuid, p_request uuid)
    RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT md5(COALESCE(string_agg(v.o_kind || ':' || v.o_id::text || ':' || v.o_class || ':' || v.o_reason
                                 || ':' || CASE WHEN q.purpose = 'ERASURE' THEN COALESCE(v.o_erase_block, '') ELSE '' END
                                 || ':' || CASE WHEN v.o_class IN ('ATTRIBUTABLE', 'CLAIMABLE')
                                                THEN md5(assisted_record_detail(p_tenant, p_site, v.o_kind, v.o_id)::text) ELSE '' END,
                                 ',' ORDER BY v.o_kind, v.o_id), ''))
    FROM assisted_request_review(p_tenant, p_site, p_request) v, assisted_guest_requests q WHERE q.id = p_request
$$;

CREATE OR REPLACE FUNCTION iam_v2.assisted_request_review_json(p_tenant uuid, p_site uuid, p_request uuid)
    RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE v_sig text; v_rows jsonb; v_sum jsonb; v_purpose text;
BEGIN
  SELECT q.purpose INTO v_purpose FROM assisted_guest_requests q WHERE q.id = p_request AND q.tenant_id = p_tenant AND q.site_id = p_site;
  v_sig := assisted_review_signature(p_tenant, p_site, p_request);
  SELECT COALESCE(jsonb_agg(jsonb_build_object(
            'kind', v.o_kind, 'id', v.o_id, 'at', v.o_at, 'class', v.o_class, 'reason', v.o_reason,
            'erase_block', CASE WHEN v_purpose = 'ERASURE' THEN v.o_erase_block END,
            'selectable', v.o_class IN ('ATTRIBUTABLE', 'CLAIMABLE')
                          AND (v_purpose = 'ACCESS' OR v.o_erase_block IS NULL),
            'detail', CASE WHEN v.o_class IN ('ATTRIBUTABLE', 'CLAIMABLE')
                           THEN assisted_record_detail(p_tenant, p_site, v.o_kind, v.o_id) END)
          ORDER BY v.o_at, v.o_kind, v.o_id), '[]'::jsonb)
    INTO v_rows FROM assisted_request_review(p_tenant, p_site, p_request) v;
  SELECT COALESCE(jsonb_object_agg(x.cls, x.n), '{}'::jsonb) INTO v_sum FROM (
    SELECT v.o_class AS cls, count(*) AS n FROM assisted_request_review(p_tenant, p_site, p_request) v GROUP BY v.o_class) x;
  RETURN jsonb_build_object('signature', v_sig, 'by_class', v_sum, 'records', v_rows);
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 5. Opening, reading, listing, closing and expiring a request.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.assisted_request_open(
    p_tenant uuid, p_site uuid, p_actor text, p_purpose text, p_macs text[], p_from timestamptz, p_to timestamptz,
    p_anchors jsonb, p_requester text, p_method text, p_evidence text,
    p_att_identity boolean, p_att_device boolean, p_att_sole boolean, p_att_private boolean, p_reason text)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE v_macs macaddr[] := '{}'; m text; v_mac macaddr; a record; v_id uuid := gen_random_uuid(); v_ref text; v_ifaces uuid[];
        v_n integer;
BEGIN
  IF p_actor IS NULL OR length(btrim(p_actor)) NOT BETWEEN 1 AND 200 THEN
    RAISE EXCEPTION 'an operator is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_purpose IS NULL OR p_purpose NOT IN ('ACCESS', 'ERASURE') THEN
    RAISE EXCEPTION 'the purpose must be ACCESS (hand over the records) or ERASURE (anonymise them)' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_macs IS NULL OR cardinality(p_macs) NOT BETWEEN 1 AND 5 THEN
    RAISE EXCEPTION 'give 1 to 5 device addresses' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  FOREACH m IN ARRAY p_macs LOOP
    IF m IS NULL OR lower(btrim(m)) !~ '^([0-9a-f]{2}:){5}[0-9a-f]{2}$|^([0-9a-f]{2}-){5}[0-9a-f]{2}$|^[0-9a-f]{12}$' THEN
      RAISE EXCEPTION 'a device address is six pairs of hexadecimal digits, such as 3c:22:fb:0a:11:7e' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    v_mac := regexp_replace(regexp_replace(lower(btrim(m)), '[:-]', '', 'g'), '(..)(?=.)', '\1:', 'g')::macaddr;
    IF (get_byte(decode(replace(v_mac::text, ':', ''), 'hex'), 0) & 1) = 1 THEN
      RAISE EXCEPTION 'that is not the address of a single device' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF NOT v_mac = ANY (v_macs) THEN
      v_macs := v_macs || v_mac;
    END IF;
  END LOOP;
  IF p_from IS NULL OR p_to IS NULL OR p_to <= p_from OR p_to - p_from > interval '366 days' OR p_to > now() + interval '1 minute' THEN
    RAISE EXCEPTION 'the range must end now or earlier, start before it ends and cover at most 366 days' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_requester IS NULL OR length(btrim(p_requester)) NOT BETWEEN 2 AND 120 THEN
    RAISE EXCEPTION 'say who is asking (2-120 characters)' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_evidence IS NULL OR length(btrim(p_evidence)) NOT BETWEEN 9 AND 1000 THEN
    RAISE EXCEPTION 'describe the evidence (9-1000 characters)' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_reason IS NULL OR length(btrim(p_reason)) NOT BETWEEN 4 AND 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF p_att_identity IS NOT TRUE THEN
    RAISE EXCEPTION 'the requester must be verified before a request is opened' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  -- A MAC address alone identifies nobody: the request names the reservation, guest or account it is about.
  IF jsonb_typeof(p_anchors) IS DISTINCT FROM 'array' OR jsonb_array_length(p_anchors) NOT BETWEEN 1 AND 5 THEN
    RAISE EXCEPTION 'name 1 to 5 reservations, guests or accounts the records are about: an address alone identifies nobody'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  FOR a IN SELECT DISTINCT x.kind, x.id FROM jsonb_to_recordset(p_anchors) AS x(kind text, id uuid) LOOP
    IF a.kind IS NULL OR a.id IS NULL OR a.kind NOT IN ('account', 'principal', 'stay', 'stay_guest') THEN
      RAISE EXCEPTION 'each record must be an account, identity, stay or stay guest from the search' USING ERRCODE = 'invalid_parameter_value';
    END IF;
    -- It must exist on this site and be one this site may export (not also another site's guest).
    PERFORM 1 FROM guest_data_scope(p_tenant, p_site, a.kind, a.id) LIMIT 1;
  END LOOP;
  -- The PMS interface the reservation came from, where there is one: reservations from two interfaces are two requests.
  SELECT COALESCE(array_agg(DISTINCT z.iface) FILTER (WHERE z.iface IS NOT NULL), '{}') INTO v_ifaces FROM (
    SELECT st.pms_interface_id AS iface FROM jsonb_to_recordset(p_anchors) AS x(kind text, id uuid)
      JOIN stays st ON st.id = x.id AND st.tenant_id = p_tenant AND st.site_id = p_site WHERE x.kind = 'stay'
    UNION ALL
    SELECT st.pms_interface_id FROM jsonb_to_recordset(p_anchors) AS x(kind text, id uuid)
      JOIN stay_guests sg ON sg.id = x.id JOIN stays st ON st.id = sg.stay_id AND st.tenant_id = p_tenant AND st.site_id = p_site
     WHERE x.kind = 'stay_guest') z;
  IF cardinality(v_ifaces) > 1 THEN
    RAISE EXCEPTION 'the reservations come from different PMS interfaces: make one request for each' USING ERRCODE = 'invalid_parameter_value';
  END IF;

  v_ref := 'AR-' || upper(encode(public.gen_random_bytes(5), 'hex'));
  INSERT INTO assisted_guest_requests (id, ref, tenant_id, site_id, purpose, macs, range_from, range_to, pms_interface_id,
        requester_ref, verification_method, evidence_note, attest_identity, attest_device, attest_sole_use, attest_private_address,
        open_reason, created_by)
  VALUES (v_id, v_ref, p_tenant, p_site, p_purpose, v_macs, p_from, p_to, v_ifaces[1], btrim(p_requester), p_method, btrim(p_evidence),
        true, COALESCE(p_att_device, false), COALESCE(p_att_sole, false), COALESCE(p_att_private, false), btrim(p_reason), btrim(p_actor));
  INSERT INTO assisted_guest_request_anchors (request_id, anchor_kind, anchor_id)
    SELECT v_id, x.kind, x.id FROM jsonb_to_recordset(p_anchors) AS x(kind text, id uuid) GROUP BY x.kind, x.id;
  GET DIAGNOSTICS v_n = ROW_COUNT;
  INSERT INTO assisted_guest_request_events (request_id, actor, action, detail)
  VALUES (v_id, btrim(p_actor), 'OPENED', jsonb_build_object('purpose', p_purpose, 'addresses', cardinality(v_macs), 'anchors', v_n,
                                                             'reason', btrim(p_reason)));
  RETURN jsonb_build_object('id', v_id, 'ref', v_ref);
END $function$;

CREATE OR REPLACE FUNCTION iam_v2.assisted_request_get(p_tenant uuid, p_site uuid, p_request uuid)
    RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE q record; v_anchors jsonb; v_events jsonb;
BEGIN
  SELECT * INTO q FROM assisted_guest_requests x WHERE x.id = p_request AND x.tenant_id = p_tenant AND x.site_id = p_site;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'assisted request not found' USING ERRCODE = 'no_data_found';
  END IF;
  IF q.status = 'OPEN' THEN
    SELECT COALESCE(jsonb_agg(jsonb_build_object('kind', a.anchor_kind, 'id', a.anchor_id, 'label', CASE a.anchor_kind
        WHEN 'account' THEN (SELECT CASE WHEN x.username NOT LIKE 'anon-%' THEN x.username END FROM guest_access_accounts x WHERE x.id = a.anchor_id)
        WHEN 'principal' THEN (SELECT COALESCE(g.display_name, 'a guest identity') FROM guest_principals g WHERE g.id = a.anchor_id)
        WHEN 'stay' THEN (SELECT 'Room ' || COALESCE(s.normalized_room_number, '?') || ' · reservation ' || s.external_reservation_id
                            FROM stays s WHERE s.id = a.anchor_id)
        WHEN 'stay_guest' THEN (SELECT COALESCE(sg.display_name, 'a guest') || ' · room ' || COALESCE(s.normalized_room_number, '?')
                                  FROM stay_guests sg JOIN stays s ON s.id = sg.stay_id WHERE sg.id = a.anchor_id) END)
        ORDER BY a.anchor_kind), '[]'::jsonb)
      INTO v_anchors FROM assisted_guest_request_anchors a WHERE a.request_id = p_request;
  ELSE
    v_anchors := '[]'::jsonb;
  END IF;
  SELECT COALESCE(jsonb_agg(jsonb_build_object('at', e.at, 'by', e.actor, 'action', e.action, 'detail', e.detail) ORDER BY e.at), '[]'::jsonb)
    INTO v_events FROM assisted_guest_request_events e WHERE e.request_id = p_request;
  RETURN jsonb_build_object(
    'id', q.id, 'ref', q.ref, 'purpose', q.purpose, 'status', q.status,
    'addresses', CASE WHEN q.macs IS NOT NULL THEN to_jsonb(ARRAY(SELECT m::text FROM unnest(q.macs) m)) END,
    'range_from', q.range_from, 'range_to', q.range_to, 'pms_interface_id', q.pms_interface_id,
    'requester', q.requester_ref, 'verification_method', q.verification_method,
    'evidence_note', CASE WHEN q.status = 'OPEN' THEN q.evidence_note END,
    'statements', jsonb_build_object('identity_verified', q.attest_identity, 'device_shown', q.attest_device,
                                     'sole_use', q.attest_sole_use, 'private_address_confirmed', q.attest_private_address),
    'reason', q.open_reason, 'created_by', q.created_by, 'created_at', q.created_at,
    'closed_by', q.closed_by, 'closed_at', q.closed_at, 'close_reason', q.close_reason, 'result', q.result,
    'anchors', v_anchors, 'events', v_events);
END $function$;

CREATE OR REPLACE FUNCTION iam_v2.assisted_request_list(p_tenant uuid, p_site uuid)
    RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(jsonb_agg(jsonb_build_object(
      'id', x.id, 'ref', x.ref, 'purpose', x.purpose, 'status', x.status, 'range_from', x.range_from, 'range_to', x.range_to,
      'requester', x.requester_ref, 'created_by', x.created_by, 'created_at', x.created_at, 'closed_at', x.closed_at,
      'closed_by', x.closed_by, 'result', x.result) ORDER BY x.created_at DESC), '[]'::jsonb)
    FROM (SELECT * FROM assisted_guest_requests q WHERE q.tenant_id = p_tenant AND q.site_id = p_site
           ORDER BY q.created_at DESC LIMIT 100) x
$$;

-- Closes a request: completes an ACCESS request that handed records over, or cancels any open one. The addresses and
-- the free text are scrubbed; what stays is who, when, how it was verified, what happened and how many.
CREATE OR REPLACE FUNCTION iam_v2.assisted_close(p_tenant uuid, p_site uuid, p_request uuid, p_actor text, p_outcome text,
                                                 p_reason text, p_result jsonb, p_event text)
    RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE v_purpose text;
BEGIN
  UPDATE assisted_guest_requests q
     SET status = p_outcome, closed_by = btrim(p_actor), closed_at = now(), close_reason = btrim(p_reason), result = p_result,
         macs = NULL, requester_ref = '[removed]', evidence_note = '[removed after the request closed]'
   WHERE q.id = p_request AND q.tenant_id = p_tenant AND q.site_id = p_site AND q.status = 'OPEN'
  RETURNING q.purpose INTO v_purpose;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'closed';
  END IF;
  -- Nothing left links the closed request to a person's record, as the erasure register has none: the anchors go; the
  -- ids of records that were only HANDED OVER (still identifiable) go; the ids of records that were anonymised stay.
  DELETE FROM assisted_guest_request_anchors WHERE request_id = p_request;
  IF v_purpose = 'ACCESS' THEN
    DELETE FROM assisted_guest_request_records WHERE request_id = p_request;
  END IF;
  INSERT INTO assisted_guest_request_events (request_id, actor, action, detail)
  VALUES (p_request, btrim(p_actor), p_event, jsonb_build_object('reason', btrim(p_reason)) || COALESCE(p_result, '{}'::jsonb));
END $$;

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

-- An open request does not wait for ever: the statutory answer is due within one month (Art. 12(3)), and a stale
-- request file holds a device address and a person's reference. Cancelled and scrubbed after 30 days.
CREATE OR REPLACE FUNCTION iam_v2.expire_assisted_requests(p_tenant uuid, p_site uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE r record; n integer := 0;
BEGIN
  FOR r IN SELECT q.id FROM assisted_guest_requests q
            WHERE q.tenant_id = p_tenant AND q.site_id = p_site AND q.status = 'OPEN' AND q.created_at < now() - interval '30 days'
            ORDER BY q.created_at LIMIT 200 FOR UPDATE LOOP
    PERFORM assisted_close(p_tenant, p_site, r.id, 'system', 'CANCELLED', 'not completed within 30 days',
                           jsonb_build_object('exports', 0, 'records_disclosed', 0, 'expired', true), 'EXPIRED');
    n := n + 1;
  END LOOP;
  RETURN n;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 6. The selection: exactly what staff reviewed, nothing else.
-- ---------------------------------------------------------------------------------------------------------
-- Recomputes the review, compares its signature with the one staff saw, and checks each selected record is listed,
-- selectable and (for an erasure) not blocked. A device is selected only with every record it carries.
CREATE OR REPLACE FUNCTION iam_v2.assisted_validate_selection(p_tenant uuid, p_site uuid, p_request uuid, p_signature text,
                                                              p_selected jsonb, p_erase boolean)
    RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE v_sig text; v_total integer; n_sel integer; n_found integer; n_bad integer; v_block text; v_inc boolean; v_rows jsonb;
        v_purpose text;
BEGIN
  IF jsonb_typeof(p_selected) IS DISTINCT FROM 'array' THEN
    RAISE EXCEPTION 'select the records to process' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT q.purpose INTO v_purpose FROM assisted_guest_requests q WHERE q.id = p_request AND q.tenant_id = p_tenant AND q.site_id = p_site;
  WITH rv AS MATERIALIZED (SELECT * FROM assisted_request_review(p_tenant, p_site, p_request)),
       sel AS (SELECT DISTINCT s.kind, s.id FROM jsonb_to_recordset(p_selected) AS s(kind text, id uuid))
  SELECT md5(COALESCE((SELECT string_agg(rv.o_kind || ':' || rv.o_id::text || ':' || rv.o_class || ':' || rv.o_reason || ':'
                                        || CASE WHEN v_purpose = 'ERASURE' THEN COALESCE(rv.o_erase_block, '') ELSE '' END
                                        || ':' || CASE WHEN rv.o_class IN ('ATTRIBUTABLE', 'CLAIMABLE')
                                                       THEN md5(assisted_record_detail(p_tenant, p_site, rv.o_kind, rv.o_id)::text) ELSE '' END,
                                        ',' ORDER BY rv.o_kind, rv.o_id) FROM rv), '')),
         (SELECT count(*) FROM rv),
         (SELECT count(*) FROM sel),
         (SELECT count(*) FROM rv JOIN sel ON sel.kind = rv.o_kind AND sel.id = rv.o_id),
         (SELECT count(*) FROM rv JOIN sel ON sel.kind = rv.o_kind AND sel.id = rv.o_id
           WHERE rv.o_class NOT IN ('ATTRIBUTABLE', 'CLAIMABLE')),
         (SELECT min(rv.o_erase_block) FROM rv JOIN sel ON sel.kind = rv.o_kind AND sel.id = rv.o_id
           WHERE p_erase AND rv.o_erase_block IS NOT NULL),
         EXISTS (SELECT 1 FROM rv d JOIN sel sd ON sd.kind = 'device' AND sd.id = d.o_id
                   JOIN rv o ON o.o_device = d.o_id AND o.o_kind <> 'device'
                  WHERE d.o_kind = 'device'
                    AND NOT EXISTS (SELECT 1 FROM sel so WHERE so.kind = o.o_kind AND so.id = o.o_id)),
         (SELECT COALESCE(jsonb_agg(jsonb_build_object('kind', rv.o_kind, 'id', rv.o_id, 'class', rv.o_class, 'device', rv.o_device)), '[]'::jsonb)
            FROM rv JOIN sel ON sel.kind = rv.o_kind AND sel.id = rv.o_id)
    INTO v_sig, v_total, n_sel, n_found, n_bad, v_block, v_inc, v_rows;
  IF p_signature IS DISTINCT FROM v_sig THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'changed';
  END IF;
  IF n_sel = 0 OR n_sel > 2000 THEN
    RAISE EXCEPTION 'select between 1 and 2000 records' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF n_found <> n_sel THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'not_listed';
  END IF;
  IF n_bad > 0 THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'not_eligible';
  END IF;
  IF v_block IS NOT NULL THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'blocked_' || v_block;
  END IF;
  IF v_inc THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'device_incomplete';
  END IF;
  RETURN jsonb_build_object('total', v_total, 'selected', v_rows);
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 7. The export: exactly the selected records, with the request's own record.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.assisted_request_export_doc(p_tenant uuid, p_site uuid, p_request uuid, p_actor text,
        p_reason text, p_confirm text, p_signature text, p_selected jsonb)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $function$
DECLARE q record; v jsonb; v_kinds jsonb; v_n integer; v_total integer;
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
  IF q.purpose <> 'ACCESS' THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'wrong_purpose';
  END IF;
  IF lower(btrim(COALESCE(p_confirm, ''))) <> lower(q.ref) THEN
    RAISE EXCEPTION 'assisted_request_refused' USING ERRCODE = 'object_in_use', DETAIL = 'confirmation';
  END IF;
  -- Nothing about these addresses is anonymised while the document is built.
  PERFORM pg_advisory_xact_lock_shared(hashtextextended(p_site::text || ':' || m::text, 146)) FROM unnest(q.macs) m ORDER BY m;
  PERFORM 1 FROM devices dv WHERE dv.tenant_id = p_tenant AND dv.site_id = p_site AND dv.mac = ANY (q.macs) ORDER BY dv.id FOR SHARE;

  v := assisted_validate_selection(p_tenant, p_site, p_request, p_signature, p_selected, false);
  v_total := (v ->> 'total')::integer;
  SELECT jsonb_object_agg(g.kind, g.docs) INTO v_kinds FROM (
    SELECT s.kind, jsonb_agg(assisted_record_detail(p_tenant, p_site, s.kind, s.id) ORDER BY s.kind, s.id) AS docs
      FROM jsonb_to_recordset(v -> 'selected') AS s(kind text, id uuid, class text, device uuid) GROUP BY s.kind) g;
  v_n := jsonb_array_length(v -> 'selected');
  INSERT INTO assisted_guest_request_records (request_id, rec_kind, rec_id, rec_class, action)
    SELECT p_request, s.kind, s.id, s.class, 'DISCLOSED' FROM jsonb_to_recordset(v -> 'selected') AS s(kind text, id uuid, class text, device uuid)
    ON CONFLICT DO NOTHING;
  INSERT INTO assisted_guest_request_events (request_id, actor, action, detail)
  VALUES (p_request, btrim(p_actor), 'EXPORTED', jsonb_build_object('reason', btrim(p_reason), 'records', v_n,
          'by_kind', (SELECT COALESCE(jsonb_object_agg(g.kind, g.n), '{}'::jsonb)
                        FROM (SELECT s.kind, count(*) AS n FROM jsonb_to_recordset(v -> 'selected') AS s(kind text, id uuid, class text, device uuid)
                               GROUP BY s.kind) g)));
  RETURN jsonb_build_object(
    'format', 'stayconnect.assisted-guest-data-export/1', 'exported_at', now(),
    'request', jsonb_build_object(
        -- What the requester is told about their own request. The hotel's evidence note and the staff member's name stay in
        -- the hotel's file (handed back when the request is closed), not in what is handed to the requester.
        'reference', q.ref, 'purpose', 'ACCESS', 'range_from', q.range_from, 'range_to', q.range_to,
        'requester', q.requester_ref, 'verification_method', q.verification_method,
        'statements', jsonb_build_object('identity_verified', q.attest_identity, 'device_shown', q.attest_device,
                                         'sole_use', q.attest_sole_use, 'private_address_confirmed', q.attest_private_address),
        'opened_at', q.created_at),
    'records', COALESCE(v_kinds, '{}'::jsonb),
    'not_included', jsonb_build_object(
        'records', GREATEST(v_total - v_n, 0),
        'why', 'Records for these device addresses that the hotel could not attribute to you with sufficient certainty, '
               || 'or did not select after review, are not included. A device address, a room or a shared internet '
               || 'access does not show who used it, and other people''s use is never disclosed to you. The hotel '
               || 'answers a request about those records separately, by its own manual and legal process.'));
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 8. The erasure: exactly the selected records, nothing else.
-- ---------------------------------------------------------------------------------------------------------
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
  UPDATE public.social_oauth_states x SET client_ip = NULL, client_mac = NULL, user_agent = NULL
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

-- ---------------------------------------------------------------------------------------------------------
-- 9. The public halves (what edged calls): the reason names nobody, and an erasure cleans the activity log.
-- ---------------------------------------------------------------------------------------------------------
-- The anchors of a request, as the guest-data reason check wants them.
CREATE OR REPLACE FUNCTION iam_v2.assisted_request_anchors_json(p_tenant uuid, p_site uuid, p_request uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT COALESCE(jsonb_agg(jsonb_build_object('kind', a.anchor_kind, 'id', a.anchor_id)), '[]'::jsonb)
    FROM assisted_guest_request_anchors a JOIN assisted_guest_requests q ON q.id = a.request_id
   WHERE a.request_id = p_request AND q.tenant_id = p_tenant AND q.site_id = p_site
$$;

-- A reason is kept in the activity log and the request's events, which erasure does not remove: it names nobody and
-- carries no device address, email address or phone number.
CREATE OR REPLACE FUNCTION public.assisted_reason_check(p_tenant uuid, p_site uuid, p_anchors jsonb, p_reason text)
    RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE a record;
BEGIN
  IF p_reason IS NULL OR length(btrim(p_reason)) NOT BETWEEN 4 AND 500 THEN
    RAISE EXCEPTION 'a reason of 4-500 characters is required' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  -- A device address in any spelling (pairs separated by : - . _ or a space, three groups of four, or twelve digits)
  -- or an IPv4 address.
  IF p_reason ~* '([0-9a-f]{2}[:._ -]){5}[0-9a-f]{2}' OR p_reason ~* '[0-9a-f]{4}[.-][0-9a-f]{4}[.-][0-9a-f]{4}'
     OR p_reason ~* '(^|[^0-9a-f])[0-9a-f]{12}($|[^0-9a-f])' OR p_reason ~ '[0-9]{1,3}([.][0-9]{1,3}){3}' THEN
    RAISE EXCEPTION 'the reason must not name the guest: leave out their name, email, phone, room, reservation and device'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
  IF jsonb_typeof(p_anchors) = 'array' THEN
    FOR a IN SELECT x.kind, x.id FROM jsonb_to_recordset(p_anchors) AS x(kind text, id uuid) LOOP
      PERFORM public.guest_data_reason_check(p_tenant, p_site, a.kind, a.id, p_reason);
    END LOOP;
  END IF;
END $$;

-- The same check for a request that exists: edged runs it BEFORE it writes the reason to the activity log, so a reason that
-- names a guest or an address is refused and never recorded.
CREATE OR REPLACE FUNCTION public.assisted_request_reason_check(p_tenant uuid, p_site uuid, p_request uuid, p_reason text)
    RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
  PERFORM public.assisted_reason_check(p_tenant, p_site, iam_v2.assisted_request_anchors_json(p_tenant, p_site, p_request), p_reason);
END $$;

CREATE OR REPLACE FUNCTION public.assisted_request_open(
    p_tenant uuid, p_site uuid, p_actor text, p_purpose text, p_macs text[], p_from timestamptz, p_to timestamptz,
    p_anchors jsonb, p_requester text, p_method text, p_evidence text,
    p_att_identity boolean, p_att_device boolean, p_att_sole boolean, p_att_private boolean, p_reason text)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
  PERFORM public.assisted_reason_check(p_tenant, p_site, p_anchors, p_reason);
  RETURN iam_v2.assisted_request_open(p_tenant, p_site, p_actor, p_purpose, p_macs, p_from, p_to, p_anchors, p_requester,
                                      p_method, p_evidence, p_att_identity, p_att_device, p_att_sole, p_att_private, p_reason);
END $$;

CREATE OR REPLACE FUNCTION public.assisted_request_close(p_tenant uuid, p_site uuid, p_request uuid, p_actor text, p_outcome text, p_reason text)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
  PERFORM public.assisted_reason_check(p_tenant, p_site, iam_v2.assisted_request_anchors_json(p_tenant, p_site, p_request), p_reason);
  RETURN iam_v2.assisted_request_close(p_tenant, p_site, p_request, p_actor, p_outcome, p_reason);
END $$;

CREATE OR REPLACE FUNCTION public.assisted_request_export(p_tenant uuid, p_site uuid, p_request uuid, p_actor text, p_reason text,
                                                          p_confirm text, p_signature text, p_selected jsonb)
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
  PERFORM public.assisted_reason_check(p_tenant, p_site, iam_v2.assisted_request_anchors_json(p_tenant, p_site, p_request), p_reason);
  RETURN iam_v2.assisted_request_export_doc(p_tenant, p_site, p_request, p_actor, p_reason, p_confirm, p_signature, p_selected);
END $$;

-- The erasure. The device addresses it anonymised are strong identifiers: wherever the activity log carries one it is
-- replaced (D-R2: who and when stay; the text goes). An address of a device that was not anonymised is not touched.
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
  -- Staff type an address in any spelling: colons, dashes, none, or three groups of four.
  v_macs := ARRAY(SELECT DISTINCT x FROM unnest(v_macs) m
                    CROSS JOIN LATERAL unnest(ARRAY[m, replace(m, ':', '-'), replace(m, ':', ''),
                      substr(replace(m, ':', ''), 1, 4) || '.' || substr(replace(m, ':', ''), 5, 4) || '.' || substr(replace(m, ':', ''), 9, 4)]) x);
  IF cardinality(v_macs) > 0 THEN
    v_since := COALESCE((v ->> 'since')::timestamptz, '-infinity');
    v_pats := ARRAY(SELECT '%' || replace(replace(replace(x, '\', '\\'), '%', '\%'), '_', '\_') || '%' FROM unnest(v_macs) x);
    WITH cand AS MATERIALIZED (
      SELECT l.ctid AS rid, l.payload AS old_payload, (lower(l.target_id) = ANY (v_macs)) AS about_it,
             iam_v2.redact_guest_text(l.payload, v_macs, l.action = 'guest_data.searched') AS new_payload
        FROM audit_log l
       WHERE l.tenant_id = p_tenant AND l.ts >= v_since
         AND (lower(l.target_id) = ANY (v_macs) OR l.action = 'guest_data.searched' OR lower(l.payload::text) LIKE ANY (v_pats)))
    UPDATE audit_log l
       SET payload = cand.new_payload || jsonb_build_object('erasure_ref', v_ref),
           target_id = CASE WHEN cand.about_it THEN NULL ELSE l.target_id END
      FROM cand
     WHERE l.ctid = cand.rid AND (cand.about_it OR cand.new_payload IS DISTINCT FROM cand.old_payload);
    GET DIAGNOSTICS n_audit = ROW_COUNT;
  END IF;
  RETURN (v - 'macs_cleared' - 'since') || jsonb_build_object('activity_log_entries_cleaned', n_audit);
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 11. Stale requests expire with the staff retention run.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.run_staff_retention(p_tenant uuid, p_site uuid)
  RETURNS jsonb
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $fn$
DECLARE v_days integer; v_audit_days integer; ids uuid[]; n_ops integer; n_audit integer; n_net integer; n_https integer; n_assisted integer;
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
  n_assisted := iam_v2.expire_assisted_requests(p_tenant, p_site);

  RETURN jsonb_build_object('staff_accounts_anonymised', n_ops, 'audit_entries', n_audit,
                            'staff_addresses_cleared', n_net + n_https, 'assisted_requests_expired', n_assisted);
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 10. Ownership and least privilege (mirrored in deploy/gatep/svc-guest-data-rights-grants.sql).
-- ---------------------------------------------------------------------------------------------------------
REVOKE ALL ON FUNCTION iam_v2.assisted_requests_guard(), iam_v2.mac_is_private(macaddr), iam_v2.assisted_class(text,boolean,boolean,boolean,text),
  iam_v2.assisted_device_state(uuid[],macaddr,uuid,uuid,uuid[],uuid[],uuid[],uuid[],uuid[],uuid[]),
  iam_v2.assisted_device_tied(uuid[],macaddr,uuid,uuid,uuid[],uuid[],uuid[],uuid[],uuid[]),
  iam_v2.assisted_device_whole(uuid,macaddr,uuid,uuid,timestamptz,timestamptz),
  iam_v2.assisted_context(uuid,uuid,uuid), iam_v2.assisted_request_review(uuid,uuid,uuid), iam_v2.assisted_record_detail(uuid,uuid,text,uuid),
  iam_v2.assisted_review_signature(uuid,uuid,uuid), iam_v2.assisted_request_review_json(uuid,uuid,uuid),
  iam_v2.assisted_request_open(uuid,uuid,text,text,text[],timestamptz,timestamptz,jsonb,text,text,text,boolean,boolean,boolean,boolean,text),
  iam_v2.assisted_request_get(uuid,uuid,uuid), iam_v2.assisted_request_list(uuid,uuid),
  iam_v2.assisted_close(uuid,uuid,uuid,text,text,text,jsonb,text), iam_v2.assisted_request_close(uuid,uuid,uuid,text,text,text),
  iam_v2.expire_assisted_requests(uuid,uuid), iam_v2.assisted_validate_selection(uuid,uuid,uuid,text,jsonb,boolean),
  iam_v2.assisted_request_export_doc(uuid,uuid,uuid,text,text,text,text,jsonb),
  iam_v2.assisted_request_erase_run(uuid,uuid,uuid,text,text,text,text,jsonb),
  iam_v2.assisted_request_anchors_json(uuid,uuid,uuid),
  public.assisted_reason_check(uuid,uuid,jsonb,text), public.assisted_request_reason_check(uuid,uuid,uuid,text),
  public.assisted_request_open(uuid,uuid,text,text,text[],timestamptz,timestamptz,jsonb,text,text,text,boolean,boolean,boolean,boolean,text),
  public.assisted_request_close(uuid,uuid,uuid,text,text,text),
  public.assisted_request_export(uuid,uuid,uuid,text,text,text,text,jsonb),
  public.assisted_request_erase(uuid,uuid,uuid,text,text,text,text,jsonb) FROM PUBLIC;
REVOKE ALL ON iam_v2.assisted_guest_requests, iam_v2.assisted_guest_request_anchors, iam_v2.assisted_guest_request_records,
  iam_v2.assisted_guest_request_events FROM PUBLIC;

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.assisted_guest_requests OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.assisted_guest_request_anchors OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.assisted_guest_request_records OWNER TO iam_v2_owner';
    EXECUTE 'ALTER TABLE iam_v2.assisted_guest_request_events OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_requests_guard() OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.mac_is_private(macaddr) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_class(text,boolean,boolean,boolean,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_device_state(uuid[],macaddr,uuid,uuid,uuid[],uuid[],uuid[],uuid[],uuid[],uuid[]) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_device_tied(uuid[],macaddr,uuid,uuid,uuid[],uuid[],uuid[],uuid[],uuid[]) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_device_whole(uuid,macaddr,uuid,uuid,timestamptz,timestamptz) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_context(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_request_review(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_record_detail(uuid,uuid,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_review_signature(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_request_review_json(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_request_open(uuid,uuid,text,text,text[],timestamptz,timestamptz,jsonb,text,text,text,boolean,boolean,boolean,boolean,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_request_get(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_request_list(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_close(uuid,uuid,uuid,text,text,text,jsonb,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_request_close(uuid,uuid,uuid,text,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.expire_assisted_requests(uuid,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_validate_selection(uuid,uuid,uuid,text,jsonb,boolean) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_request_export_doc(uuid,uuid,uuid,text,text,text,text,jsonb) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_request_erase_run(uuid,uuid,uuid,text,text,text,text,jsonb) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.assisted_request_anchors_json(uuid,uuid,uuid) OWNER TO iam_v2_owner';
    -- The review reads, and an erasure clears, the address of a social sign-in redirect: the columns it needs, no more.
    EXECUTE 'GRANT SELECT (tenant_id, state, client_ip, client_mac, user_agent, provider, created_at, consumed_at) ON public.social_oauth_states TO iam_v2_owner';
    EXECUTE 'GRANT UPDATE (client_ip, client_mac, user_agent) ON public.social_oauth_states TO iam_v2_owner';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    -- What edged calls. No table privilege is added: a request is read and written only through these.
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.assisted_request_get(uuid,uuid,uuid) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.assisted_request_list(uuid,uuid) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION iam_v2.assisted_request_review_json(uuid,uuid,uuid) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.assisted_reason_check(uuid,uuid,jsonb,text) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.assisted_request_reason_check(uuid,uuid,uuid,text) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.assisted_request_open(uuid,uuid,text,text,text[],timestamptz,timestamptz,jsonb,text,text,text,boolean,boolean,boolean,boolean,text) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.assisted_request_close(uuid,uuid,uuid,text,text,text) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.assisted_request_export(uuid,uuid,uuid,text,text,text,text,jsonb) TO svc_edged';
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.assisted_request_erase(uuid,uuid,uuid,text,text,text,text,jsonb) TO svc_edged';
  END IF;
END $own$;

COMMIT;
