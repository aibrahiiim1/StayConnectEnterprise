-- 0144 -- GUEST-DATA REQUESTS STAY WITH THE GUEST WHO ASKED, AND RETENTION KEEPS WHAT IS STILL IN USE
-- (GDPR branch review of 2026-10-20; Product Owner decisions D-R2 and D-R4 of 2026-10-20.)
--
-- Export and erasure (C3/C4, 0137-0143):
--   * A ROOM-MOVE WALK STOPS AT A SHARED STAY. 0142 followed every stay link and removed shared stays only
--     afterwards, so a walk passed THROUGH a shared room and out the other side: erasing X, who moved into a
--     room shared with Y, also took Y's later room of their own. The walk now only continues from a stay of
--     the guest who asked; a shared stay is reached, never passed through.
--   * ONE PERSON, NOT ONE NAME (D-R4). A guest on another stay is the same person by PMS guest number. The
--     name is used only when a number is missing on either side, both first and last name are present, and
--     exactly one guest on that stay has that full name; otherwise the request is refused as ambiguous and
--     staff pick the guest row. Two guests with the same surname, or the same full name, are never merged.
--   * A SHARED STAY IS EXPORTED OR ERASED ONLY WHERE THE GUEST IS ON IT. A linked shared stay with no row of
--     the guest is out of scope (it was exported whole), and a linked stay where the guest is the only one
--     named is their own stay, exported and erased whole (it was left out as "shared").
--   * NOT WHILE THE GUEST MAY STILL BE IN HOUSE. A stay with no departure counts as gone from the PMS only
--     when the PMS's latest full room list was sent AFTER everything the stay was told; a guest who checked
--     in after the night's list is still live.
--   * NOTHING CAN JOIN THE SCOPE WHILE IT IS ERASED. The records in scope are locked and the scope computed
--     again under the locks, so access granted at that moment cannot be left pointing at an anonymised stay.
--   * NO OTHER SITE'S GUEST. A guest identity that was also used at another site is refused ('other_site'),
--     and the search finds identities used at this site only.
--   * THE REASON NAMES NOBODY. A reason that contains the guest's name, address, phone, room, reservation or
--     device address, or any email address or phone number, is refused: it is kept in the erasure log and the
--     activity log, which are not erased.
--   * THE ACTIVITY LOG KEEPS WHO AND WHEN, NOT WHO ABOUT (D-R2). Erasure no longer blanks whole entries. In
--     every entry about the guest, and in every staff search whose text names the guest (by name, room or any
--     other identifier), the identifying text is replaced by "[erased]" and the record id is cleared; the
--     action, the staff member and the time stay, and the entry gains the erasure reference. Matching is by whole value or
--     word, so erasing ann@x.com no longer touches joann@x.com, and only entries since the guest's first
--     record are read.
--   * THE EXPORT CARRIES what erasure clears: code-device bindings, sign-in redirects and the 0066 rollback
--     copy of the guest's names.
--
-- Retention (C1, 0127-0143):
--   * AN ACCOUNT OF UNKNOWN AGE IS NOT OLD. Client accounts made before 0104 have no creation time and read as
--     infinitely old, so an enabled, never-used account was disabled on the first run. Their age is now
--     counted from when 0104 recorded it could not know (iam_v2.retention_floor), and an arrival in the future
--     (valid_from) keeps an account. Devices with no sighting time are treated the same way.
--   * A REVIEW MESSAGE IS GIVEN UP, NOT LEFT BLANK IN THE QUEUE. An unlinked PMS message waiting for a staff
--     decision is, after the stay retention, closed as EXPIRED_UNDECIDED (evidence DATA_RETENTION) -- the
--     decision 0123's expiry already records -- and only then blanked. A message whose reservation is still
--     open in the mirror is neither closed nor blanked.
--   * DATES ARE THE HOTEL'S. A departure date becomes a time in the site's own time zone, not the session's.
--   * ANONYMISATION ONLY REMOVES. The PMS inbox accepts an anonymised payload only if it is the blank marker or
--     a part of the payload it replaces.
--   * A STAFF ACCOUNT CREATED DISABLED is dated too, so it is anonymised after the period like any other; the
--     staff-address clean-up works in batches.
--
-- Backups (C5, 0129):
--   * THE NIGHTLY BACKUP USES THIS HOTEL'S KEYS ONLY. Without a site named, the recipients are the active key
--     holders of the site the appliance now serves (public.sites); holders left by a previous owner are never
--     used, and no longer make every nightly backup fail once the new hotel adds its own.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. The floor for rows whose age is unknown.
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS iam_v2.retention_floor (
  id    boolean PRIMARY KEY DEFAULT true CONSTRAINT rf_singleton CHECK (id),
  since timestamptz NOT NULL
);
COMMENT ON TABLE iam_v2.retention_floor IS
  'The latest time a row with no recorded creation or sighting time can have been made (0144): when migration '
  '0104 started recording client-account creation, or when this migration ran if 0104 predates the ledger. '
  'Retention counts such rows'' age from here, so a row of unknown age is never taken for an old one.';
INSERT INTO iam_v2.retention_floor (id, since)
SELECT true, COALESCE((SELECT min(m.applied_at) FROM public.schema_migrations m
                        WHERE m.version = '0104' OR m.version LIKE '0104\_%'), now())
ON CONFLICT (id) DO NOTHING;
REVOKE ALL ON iam_v2.retention_floor FROM PUBLIC;

-- The site's time zone, for turning a PMS date into a time (read by the iam_v2 retention and erasure).
CREATE OR REPLACE FUNCTION iam_v2.site_day_start(p_site uuid, p_day date) RETURNS timestamptz
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT p_day::timestamp AT TIME ZONE COALESCE((SELECT s.timezone FROM public.sites s WHERE s.id = p_site), 'UTC');
$$;
COMMENT ON FUNCTION iam_v2.site_day_start(uuid, date) IS
  'Midnight at the start of a date, in the site''s time zone (public.sites.timezone; UTC if unknown) (0144).';
CREATE OR REPLACE FUNCTION iam_v2.site_today(p_site uuid) RETURNS date
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT (now() AT TIME ZONE COALESCE((SELECT s.timezone FROM public.sites s WHERE s.id = p_site), 'UTC'))::date;
$$;
REVOKE ALL ON FUNCTION iam_v2.site_day_start(uuid, date), iam_v2.site_today(uuid) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 2. Who is the same guest (D-R4).
-- ---------------------------------------------------------------------------------------------------------
-- 'id' when both rows carry the PMS guest number and it is the same; NULL when both carry one and it differs;
-- 'name' when a number is missing on either side and both have the same first AND last name; NULL otherwise.
CREATE OR REPLACE FUNCTION iam_v2.same_guest(a_ext text, a_first text, a_last text, b_ext text, b_first text, b_last text)
    RETURNS text
    LANGUAGE sql IMMUTABLE
    AS $$
  SELECT CASE
    WHEN NULLIF(btrim(a_ext), '') IS NOT NULL AND NULLIF(btrim(b_ext), '') IS NOT NULL THEN
      CASE WHEN btrim(a_ext) = btrim(b_ext) THEN 'id' END
    WHEN NULLIF(btrim(a_first), '') IS NOT NULL AND NULLIF(btrim(a_last), '') IS NOT NULL
     AND NULLIF(btrim(b_first), '') IS NOT NULL AND NULLIF(btrim(b_last), '') IS NOT NULL
     AND upper(btrim(a_first)) = upper(btrim(b_first)) AND upper(btrim(a_last)) = upper(btrim(b_last)) THEN 'name'
  END;
$$;

-- The rows on p_stays that are the same person as one of the p_guests rows. Two rows of one stay are always two
-- guests. Refuses ('ambiguous guest') when a name would decide and more than one row on one stay carries that
-- name, or the guest's own stay has a second row with their full name.
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
         AND same_guest(a.external_guest_id, a.first_name_norm, a.last_name_norm,
                        b.external_guest_id, b.first_name_norm, b.last_name_norm) = 'name'
       GROUP BY a.id, b.stay_id HAVING count(*) > 1)
     OR EXISTS (
      SELECT 1
        FROM stay_guests a
        JOIN stay_guests t ON t.stay_id = a.stay_id AND t.id <> a.id
       WHERE a.id = ANY (p_guests) AND NOT t.id = ANY (p_guests)
         AND same_guest(NULL, a.first_name_norm, a.last_name_norm, NULL, t.first_name_norm, t.last_name_norm) = 'name'
         AND EXISTS (SELECT 1 FROM stay_guests b
                      WHERE b.stay_id = ANY (p_stays) AND b.stay_id <> a.stay_id
                        AND same_guest(a.external_guest_id, a.first_name_norm, a.last_name_norm,
                                       b.external_guest_id, b.first_name_norm, b.last_name_norm) = 'name')) THEN
    RAISE EXCEPTION 'ambiguous guest: more than one guest has this name, so the guest cannot be told apart on a '
                    'linked stay. Choose the guest row from the search.' USING ERRCODE = 'invalid_parameter_value';
  END IF;
  SELECT COALESCE(array_agg(DISTINCT b.id), '{}') INTO v
    FROM stay_guests a JOIN stay_guests b ON b.stay_id = ANY (p_stays)
   WHERE a.id = ANY (p_guests)
     AND (b.id = a.id
          OR (b.stay_id <> a.stay_id
              AND same_guest(a.external_guest_id, a.first_name_norm, a.last_name_norm,
                             b.external_guest_id, b.first_name_norm, b.last_name_norm) IS NOT NULL));
  RETURN v;
END $$;
REVOKE ALL ON FUNCTION iam_v2.same_guest(text,text,text,text,text,text), iam_v2.same_guest_rows(uuid[],uuid[]) FROM PUBLIC;

-- The guest's names inside a PMS event: kept (export) or taken out (erasure). The two directions err on the safe
-- side (D-R4). An EXPORT keeps a name only when it is certainly the guest's: a top-level name with first AND last
-- name, a sharer by PMS guest number, or by full name when no number decides and no other sharer has that full
-- name -- so it never hands over a relative who shares a surname. An ERASURE takes out every name that may be the
-- guest's (a surname alone, as 0141 did), so the erased guest's name never stays behind; at worst a copy of a
-- relative's name inside a PMS message goes with it, never their stay record.
CREATE OR REPLACE FUNCTION iam_v2.event_payload_for_guests(p_payload jsonb, p_guests jsonb, p_keep boolean)
    RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  WITH g AS (SELECT NULLIF(x->>'id', '') AS id, NULLIF(x->>'first', '') AS f, NULLIF(x->>'last', '') AS l
               FROM jsonb_array_elements(COALESCE(p_guests, '[]'::jsonb)) x),
  sh AS (SELECT s.value AS v, s.ordinality AS n,
                NULLIF(btrim(s.value->>'external_guest_id'), '') AS id,
                NULLIF(upper(btrim(s.value->>'first_name')), '') AS f,
                NULLIF(upper(btrim(s.value->>'last_name')), '') AS l
           FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p_payload->'sharers') = 'array'
                                          THEN p_payload->'sharers' ELSE '[]'::jsonb END) WITH ORDINALITY s),
  shm AS (SELECT sh.v, sh.n,
                 CASE WHEN p_keep THEN
                   EXISTS (SELECT 1 FROM g
                            WHERE (g.id IS NOT NULL AND sh.id = g.id)
                               OR ((g.id IS NULL OR sh.id IS NULL) AND g.f IS NOT NULL AND g.l IS NOT NULL
                                   AND sh.f = g.f AND sh.l = g.l
                                   AND (SELECT count(*) FROM sh o WHERE o.f = sh.f AND o.l = sh.l) = 1))
                 ELSE
                   EXISTS (SELECT 1 FROM g
                            WHERE (g.id IS NOT NULL AND sh.id = g.id)
                               OR (g.l IS NOT NULL AND sh.l = g.l AND (g.f IS NULL OR sh.f IS NULL OR sh.f = g.f)))
                 END AS mine
            FROM sh),
  top AS (SELECT CASE WHEN p_keep THEN
                   EXISTS (SELECT 1 FROM g WHERE g.f IS NOT NULL AND g.l IS NOT NULL
                              AND NULLIF(upper(btrim(p_payload->>'last_name')), '') = g.l
                              AND NULLIF(upper(btrim(p_payload->>'first_name')), '') = g.f)
                 ELSE
                   EXISTS (SELECT 1 FROM g WHERE g.l IS NOT NULL
                              AND NULLIF(upper(btrim(p_payload->>'last_name')), '') = g.l
                              AND (g.f IS NULL OR NULLIF(btrim(p_payload->>'first_name'), '') IS NULL
                                   OR upper(btrim(p_payload->>'first_name')) = g.f))
                 END AS mine)
  SELECT CASE WHEN p_payload IS NULL OR jsonb_typeof(p_payload) <> 'object' THEN p_payload ELSE
    (CASE WHEN (SELECT mine FROM top) = p_keep THEN p_payload ELSE p_payload - 'last_name' - 'first_name' - 'vip' - 'travel_agent' END)
    || CASE WHEN jsonb_typeof(p_payload->'sharers') = 'array' THEN jsonb_build_object('sharers', COALESCE((
         SELECT jsonb_agg(shm.v ORDER BY shm.n) FROM shm WHERE shm.mine = p_keep), '[]'::jsonb))
       ELSE '{}'::jsonb END
  END;
$$;

-- ---------------------------------------------------------------------------------------------------------
-- 3. A stay is gone from the PMS only when the latest full room list came after everything the stay was told.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.stay_absent_from_published_roster(p_stay uuid) RETURNS boolean
    LANGUAGE sql STABLE
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
  SELECT EXISTS (
    SELECT 1 FROM iam_v2.stays s
      JOIN iam_v2.pms_interface_runtime r
        ON r.tenant_id = s.tenant_id AND r.site_id = s.site_id AND r.pms_interface_id = s.pms_interface_id
       AND r.published_resync_generation > 0
      CROSS JOIN LATERAL (
        SELECT COALESCE(r.last_complete_sync_at, max(se.received_at)) AS sent_at FROM iam_v2.stay_events se
         WHERE se.tenant_id = s.tenant_id AND se.site_id = s.site_id AND se.pms_interface_id = s.pms_interface_id
           AND se.admission_kind = 'RESYNC' AND se.resync_generation = r.published_resync_generation) roster
     WHERE s.id = p_stay
       AND roster.sent_at IS NOT NULL
       AND roster.sent_at > COALESCE((SELECT max(e.received_at) FROM iam_v2.stay_events e WHERE e.stay_id = s.id), 'infinity')
       AND NOT EXISTS (
         SELECT 1 FROM iam_v2.stay_events se
          WHERE se.tenant_id = s.tenant_id AND se.site_id = s.site_id AND se.pms_interface_id = s.pms_interface_id
            AND se.admission_kind = 'RESYNC' AND se.resync_generation = r.published_resync_generation
            AND se.event_type IN ('GI', 'GC')
            AND (btrim(COALESCE(se.payload->>'reservation', '')) = s.external_reservation_id
                 OR (btrim(COALESCE(se.payload->>'reservation', '')) = ''
                     AND upper(btrim(COALESCE(se.payload->>'room', ''))) = s.normalized_room_number))));
$$;
COMMENT ON FUNCTION iam_v2.stay_absent_from_published_roster(uuid) IS
  'True only when the stay''s PMS has published a full room list (a resync), that list was published '
  '(last_complete_sync_at) after the last message about the stay, and the stay is not in it. False when no list was published, or the stay heard from '
  'the PMS since: nothing can be concluded (0143, 0144).';

-- ---------------------------------------------------------------------------------------------------------
-- 4. Anonymisation of a processed PMS event may only remove (0139's inbox guard, otherwise unchanged).
-- ---------------------------------------------------------------------------------------------------------
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
  -- event fingerprint of a processed event -- and nothing else. The new payload is the blank marker or a part
  -- of the old one (0144): anonymisation removes, it never writes something new.
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
    IF NEW.payload IS DISTINCT FROM OLD.payload
       AND NEW.payload IS DISTINCT FROM '{"anonymised": true}'::jsonb
       AND NOT (OLD.payload @> NEW.payload) THEN
      RAISE EXCEPTION 'stay_events anonymisation may only remove from a payload or blank it';
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

-- A review message closed by retention is recorded with its own evidence kind.
ALTER TABLE iam_v2.pms_case_resolutions DROP CONSTRAINT IF EXISTS pms_case_resolutions_evidence_kind_check;
ALTER TABLE iam_v2.pms_case_resolutions ADD CONSTRAINT pms_case_resolutions_evidence_kind_check CHECK (evidence_kind = ANY (ARRAY[
    'PUBLISHED_ROSTER_GENERATION','ADMISSION_KIND','STAY_STATE','NONE','OPERATOR_DECISION','EXPIRY_SETTING',
    'DATA_RETENTION']));

-- ---------------------------------------------------------------------------------------------------------
-- 5. The scope of one guest.
-- ---------------------------------------------------------------------------------------------------------
-- Kinds returned: account, principal, identity (o_id = its principal), stay, entitlement, device,
-- device_shared, stay_event, stay_guest (the guest's own row on a shared stay), stay_shared (a shared stay the
-- guest is on: only their rows on it are in scope), stay_shared_other (a shared stay reached but without the
-- guest: counted, nothing exported or erased).
CREATE OR REPLACE FUNCTION iam_v2.guest_data_scope(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS TABLE(o_kind text, o_id uuid, o_value text)
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[] := '{}'; p uuid[] := '{}'; s uuid[] := '{}'; own uuid[]; e uuid[]; d uuid[]; shared uuid[];
        resv text[]; ss uuid[] := '{}'; sso uuid[] := '{}'; gr uuid[] := '{}'; anchor uuid[] := '{}';
        v_origin uuid; v_seen uuid[]; v_front uuid[]; v_next uuid[]; v_depth integer := 0; v_st uuid; v_rows uuid[];
        v_walked boolean := false;
BEGIN
  IF p_kind = 'stay_guest' THEN
    SELECT ARRAY[sg.id], ARRAY[sg.stay_id] INTO anchor, v_front
      FROM stay_guests sg WHERE sg.id = p_id AND sg.tenant_id = p_tenant AND sg.site_id = p_site;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'guest record not found' USING ERRCODE = 'no_data_found';
    END IF;
    -- THE GUEST'S OWN STAYS: from their row, through stay links, continuing only from a stay they are on (0144).
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
    -- Stays the guest is on: a stay where they are the only one named is theirs, whole; on a shared one, only
    -- their rows.
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

  IF NOT v_walked THEN
    s := s || COALESCE((SELECT array_agg(DISTINCT x.stay_id) FROM guest_access_accounts x
                         WHERE x.id = ANY (a) AND x.stay_id IS NOT NULL AND NOT x.stay_id = ANY (s)), '{}');
    -- A ROOM MOVE OR A TRANSFER IS THE SAME STAY CONTINUED: followed both ways, at most 20 links, and only
    -- onward from a stay with one guest named. A shared stay is reached, never passed through (0144).
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

  -- NO OTHER SITE'S GUEST (0144). An identity is the tenant's; if it was used at another site, this site's staff
  -- cannot export or erase it.
  IF EXISTS (SELECT 1 FROM entitlements x WHERE x.guest_principal_id = ANY (p) AND x.site_id <> p_site)
     OR EXISTS (SELECT 1 FROM auth_contexts x WHERE x.guest_principal_id = ANY (p) AND x.site_id <> p_site) THEN
    RAISE EXCEPTION 'guest_data_live' USING ERRCODE = 'object_in_use', DETAIL = 'other_site';
  END IF;

  -- Asked for by stay: the guest of that stay is the guest who asked, so on a shared stay it continues as, their
  -- own rows are in scope (D-R4: PMS guest number, or a unique full name). Reached through an account or an
  -- identity, which guest it is cannot be known, so no rows are.
  IF p_kind = 'stay' AND cardinality(ss) > 0 THEN
    anchor := ARRAY(SELECT sg.id FROM stay_guests sg WHERE sg.stay_id = ANY (s)
                     AND num_nonnulls(sg.first_name_norm, sg.last_name_norm, sg.display_name, sg.external_guest_id) > 0);
    gr := ARRAY(SELECT z.x FROM unnest(same_guest_rows(anchor, ss)) z(x) WHERE NOT z.x = ANY (anchor));
  END IF;
  -- A shared stay the guest is on is in scope for their rows only; one they are not on is only counted.
  sso := ARRAY(SELECT z.x FROM unnest(ss) z(x) WHERE NOT EXISTS (SELECT 1 FROM stay_guests sg WHERE sg.stay_id = z.x AND sg.id = ANY (gr)));
  ss := ARRAY(SELECT z.x FROM unnest(ss) z(x) WHERE NOT z.x = ANY (sso));

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
  RETURN QUERY SELECT 'stay_shared_other'::text, z.x, NULL::text FROM unnest(sso) z(x);
  -- The guest's own rows on those shared stays, and the shared stays' PMS events, from which only they are removed.
  IF cardinality(gr) > 0 THEN
    RETURN QUERY SELECT 'stay_guest'::text, sg.id, sg.display_name FROM stay_guests sg WHERE sg.id = ANY (gr);
    RETURN QUERY SELECT 'stay_event'::text, ev.id, ev.processing_status FROM stay_events ev
                  WHERE ev.tenant_id = p_tenant AND ev.site_id = p_site AND ev.stay_id = ANY (ss);
  END IF;
END $function$;

-- Every text that identifies the guest in scope, lower case: account names, identity values, device
-- addresses, reservations, rooms, and the names on their stay rows. Used to keep reasons and the activity log
-- free of them.
CREATE OR REPLACE FUNCTION iam_v2.guest_data_identifiers(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
    RETURNS TABLE(o_kind text, o_value text)
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
DECLARE ks text[]; ids uuid[]; vs text[]; s uuid[]; gr uuid[];
BEGIN
  SELECT COALESCE(array_agg(sc.o_kind), '{}'), COALESCE(array_agg(sc.o_id), '{}'), COALESCE(array_agg(sc.o_value), '{}')
    INTO ks, ids, vs FROM guest_data_scope(p_tenant, p_site, p_kind, p_id) sc;
  SELECT COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'stay'), '{}'), COALESCE(array_agg(z.i) FILTER (WHERE z.k = 'stay_guest'), '{}')
    INTO s, gr FROM unnest(ks, ids) z(k, i);
  RETURN QUERY
    SELECT DISTINCT z.k, lower(btrim(z.v)) FROM (
      SELECT CASE WHEN g.k = 'stay' THEN 'reservation' ELSE g.k END AS k, g.v FROM unnest(ks, vs) g(k, v)
       WHERE g.k IN ('account', 'principal', 'identity', 'device', 'stay', 'stay_guest')
      UNION ALL SELECT 'room', x.normalized_room_number FROM stays x WHERE x.id = ANY (s)
      UNION ALL SELECT 'name', sg.display_name FROM stay_guests sg WHERE sg.stay_id = ANY (s) OR sg.id = ANY (gr)
      UNION ALL SELECT 'name', sg.first_name_norm FROM stay_guests sg WHERE sg.stay_id = ANY (s) OR sg.id = ANY (gr)
      UNION ALL SELECT 'name', sg.last_name_norm FROM stay_guests sg WHERE sg.stay_id = ANY (s) OR sg.id = ANY (gr)
      UNION ALL SELECT 'name', concat_ws(' ', sg.first_name_norm, sg.last_name_norm)
                  FROM stay_guests sg WHERE (sg.stay_id = ANY (s) OR sg.id = ANY (gr))
                   AND sg.first_name_norm IS NOT NULL AND sg.last_name_norm IS NOT NULL
      UNION ALL SELECT 'reservation', x.external_reservation_id FROM stays x
                 WHERE x.id IN (SELECT sg.stay_id FROM stay_guests sg WHERE sg.id = ANY (gr))
                   AND stay_named_guests(x.id) <= 1
    ) z
    WHERE z.v IS NOT NULL AND length(btrim(z.v)) >= 2 AND z.v NOT LIKE 'anon-%';
END $$;
REVOKE ALL ON FUNCTION iam_v2.guest_data_identifiers(uuid,uuid,text,uuid) FROM PUBLIC;

-- Whether a text names one of p_vals as a whole value or word (letters and digits around it break the match).
CREATE OR REPLACE FUNCTION iam_v2.text_names_any(p_text text, p_vals text[]) RETURNS boolean
    LANGUAGE sql IMMUTABLE
    AS $$
  SELECT p_text IS NOT NULL AND EXISTS (
    SELECT 1 FROM unnest(p_vals) v
     WHERE length(v) >= 3
       AND (lower(btrim(p_text)) = v
            OR lower(p_text) ~ ('(^|[^[:alnum:]])' || regexp_replace(v, '([.*+?^${}()|\[\]\\-])', '\\\1', 'g')
                                || '($|[^[:alnum:]])')));
$$;

-- p_payload with every string naming the guest replaced by "[erased]". p_search: the entry is a staff search,
-- so a text that is part of an identifier (a partial name, a room) is erased too.
CREATE OR REPLACE FUNCTION iam_v2.redact_guest_text(p_payload jsonb, p_vals text[], p_search boolean) RETURNS jsonb
    LANGUAGE plpgsql IMMUTABLE
    AS $$
DECLARE r jsonb; k text; v jsonb;
BEGIN
  CASE jsonb_typeof(p_payload)
    WHEN 'object' THEN
      r := '{}'::jsonb;
      FOR k, v IN SELECT * FROM jsonb_each(p_payload) LOOP
        r := r || jsonb_build_object(k, iam_v2.redact_guest_text(v, p_vals, p_search));
      END LOOP;
      RETURN r;
    WHEN 'array' THEN
      RETURN COALESCE((SELECT jsonb_agg(iam_v2.redact_guest_text(x, p_vals, p_search) ORDER BY n)
                         FROM jsonb_array_elements(p_payload) WITH ORDINALITY t(x, n)), '[]'::jsonb);
    WHEN 'string' THEN
      IF iam_v2.text_names_any(p_payload #>> '{}', p_vals)
         OR (p_search AND length(btrim(p_payload #>> '{}')) >= 2
             AND EXISTS (SELECT 1 FROM unnest(p_vals) x WHERE position(lower(btrim(p_payload #>> '{}')) IN x) > 0)) THEN
        RETURN to_jsonb('[erased]'::text);
      END IF;
      RETURN p_payload;
    ELSE
      RETURN p_payload;
  END CASE;
END $$;
REVOKE ALL ON FUNCTION iam_v2.text_names_any(text,text[]), iam_v2.redact_guest_text(jsonb,text[],boolean) FROM PUBLIC;

-- Refuses a reason that names the guest, or carries any email address or phone number (0144).
CREATE OR REPLACE FUNCTION public.guest_data_reason_check(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_reason text)
    RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE v_vals text[];
BEGIN
  SELECT COALESCE(array_agg(o_value), '{}') INTO v_vals FROM iam_v2.guest_data_identifiers(p_tenant, p_site, p_kind, p_id);
  -- A phone number is a run of at least nine digits (spaces, dots, dashes and brackets between them allowed); a
  -- date such as 2026-10-17 has eight and passes.
  IF p_reason ~* '[[:alnum:]._%+-]+@[[:alnum:].-]+\.[[:alpha:]]{2,}'
     OR EXISTS (SELECT 1 FROM regexp_matches(p_reason, '(\+?[0-9][0-9 ().-]{6,}[0-9])', 'g') m
                 WHERE length(regexp_replace(m[1], '[^0-9]', '', 'g')) >= 9)
     OR iam_v2.text_names_any(p_reason, v_vals) THEN
    RAISE EXCEPTION 'the reason must not name the guest: leave out their name, email, phone, room, reservation and device'
      USING ERRCODE = 'invalid_parameter_value';
  END IF;
END $$;
COMMENT ON FUNCTION public.guest_data_reason_check(uuid, uuid, text, uuid, text) IS
  'Refuses (22023) a guest-data export or erasure reason that contains one of the guest''s identifiers as a word, '
  'an email address or a phone number (0144, D-C4): the reason is kept in the erasure and activity logs.';
REVOKE ALL ON FUNCTION public.guest_data_reason_check(uuid, uuid, text, uuid, text) FROM PUBLIC;

-- ---------------------------------------------------------------------------------------------------------
-- 6. The search: identities used at this site only.
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
                               OR lower(COALESCE(sg.first_name_norm, '')) LIKE pat))
            OR EXISTS (SELECT 1 FROM entitlements en WHERE en.id = ANY (v_ents) AND en.stay_id = x.id))
  ) r ORDER BY 6 DESC NULLS LAST LIMIT 50;
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 7. The export: what erasure clears is exported too.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.guest_data_export_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
 RETURNS jsonb
 LANGUAGE plpgsql
 STABLE SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; ev uuid[]; macs macaddr[]; se_ids uuid[]; pu_ids uuid[];
        ss uuid[]; gr uuid[]; v_who jsonb; v_ids text[]; n_other integer;
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
         count(*) FILTER (WHERE o_kind = 'stay_shared_other')
    INTO a, p, s, e, d, ev, ss, gr, n_other FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
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
         WHERE (k.from_stay = x.id OR k.to_stay = x.id) AND (o.id = ANY (s) OR o.id = ANY (ss)))) ORDER BY x.arrival), '[]'::jsonb)
      FROM stays x WHERE x.id = ANY (s) OR x.id = ANY (ss)),
    'pms_events', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'type', x.event_type, 'received_at', x.received_at, 'pms_time', x.pms_timestamp_utc,
        'status', x.processing_status,
        'details', CASE WHEN cardinality(gr) > 0 THEN event_payload_for_guests(x.payload, v_who, true) ELSE x.payload END)
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
     WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.device_mac = ANY (macs)),
    -- 0144: what erasure also clears.
    'code_device_bindings', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'device_mac_address', x.device_mac, 'issued_at', x.issued_at) ORDER BY x.issued_at), '[]'::jsonb)
      FROM otp_device_bindings x WHERE x.tenant_id = p_tenant AND x.device_mac = ANY (macs)),
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
     ELSE '{}'::jsonb END;
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 8. The erasure: locked, refused while live, and only for the guest who asked.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION iam_v2.erase_guest_records(p_tenant uuid, p_site uuid, p_kind text, p_id uuid, p_operator text, p_reason text, p_public_counts jsonb)
 RETURNS jsonb
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'iam_v2', 'pg_temp'
AS $function$
DECLARE a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; ev uuid[]; macs macaddr[]; live text[] := '{}';
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
  -- under the locks: new access, sessions or links on those records wait for this transaction. If the scope
  -- moved in between, lock the new records too, a few times at most.
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
           md5(string_agg(o_kind || ':' || o_id::text, ',' ORDER BY o_kind, o_id))
      INTO a, p, s, e, d, ev, v_shared, ss, gr, v_other, v_sig FROM guest_data_scope(p_tenant, p_site, p_kind, p_id);
    PERFORM 1 FROM stays x WHERE x.id = ANY (s) OR x.id = ANY (ss) ORDER BY x.id FOR UPDATE;
    PERFORM 1 FROM guest_access_accounts x WHERE x.id = ANY (a) ORDER BY x.id FOR UPDATE;
    PERFORM 1 FROM guest_principals x WHERE x.id = ANY (p) ORDER BY x.id FOR UPDATE;
    PERFORM 1 FROM entitlements x WHERE x.id = ANY (e) ORDER BY x.id FOR UPDATE;
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

  -- REFUSED WHILE LIVE (D-C3). The PMS would copy an open stay straight back, and anonymising access or a
  -- device in use would cut a guest off mid-session. A stay the PMS left open after its departure is not live.
  -- A STAY WITH MORE THAN ONE GUEST IS ERASED ONE GUEST AT A TIME (0141, GDPR audit C3): erasing it whole would
  -- erase the other guests too, who did not ask.
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

  -- The public half names the reference when it already used it in the activity log (0144).
  v_ref := COALESCE(p_public_counts->>'_erasure_ref', 'ER-' || upper(encode(public.gen_random_bytes(6), 'hex')));
  v_counts := jsonb_build_object(
    'client_accounts', cardinality(a), 'guest_identities', cardinality(p), 'hotel_stays', cardinality(s),
    'stay_guests', v_guests, 'hotel_stays_kept_shared', cardinality(ss) + v_other,
    'pms_events', v_events, 'devices', cardinality(d), 'devices_kept_shared', v_shared, 'sessions', v_sessions,
    'sign_in_attempts', v_attempts, 'verification_records', v_verif)
    || (COALESCE(p_public_counts, '{}'::jsonb) - '_erasure_ref');
  INSERT INTO guest_data_erasures (erasure_ref, tenant_id, site_id, subject_kind, erased_by, erase_reason, counts)
  VALUES (v_ref, p_tenant, p_site, p_kind, btrim(p_operator), btrim(p_reason), v_counts);
  RETURN jsonb_build_object('erasure_ref', v_ref, 'counts', v_counts);
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 9. The public halves: export adds sign-in redirects; erasure keeps the activity log's who and when (D-R2).
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
             WHERE o_kind NOT IN ('stay_shared', 'stay_shared_other')
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
DECLARE v_ids text[]; v_vals text[]; v_all text[]; v_macs text[]; v_pats text[]; v_since timestamptz;
        n_codes integer; n_oauth integer; n_audit integer; v_ref text;
        a uuid[]; p uuid[]; s uuid[]; d uuid[]; gr uuid[];
BEGIN
  -- What identifies the guest, read BEFORE anything is anonymised.
  SELECT COALESCE(array_agg(o_value), '{}') INTO v_all FROM iam_v2.guest_data_identifiers(p_tenant, p_site, p_kind, p_id);
  PERFORM public.guest_data_reason_check(p_tenant, p_site, p_kind, p_id, p_reason);
  SELECT COALESCE(array_agg(DISTINCT o_id::text) FILTER (
             WHERE o_kind NOT IN ('stay_shared', 'stay_shared_other')
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

  UPDATE auth_otps o SET destination = 'anonymised', ip = NULL, user_agent = NULL
   WHERE o.tenant_id = p_tenant AND lower(o.destination) = ANY (v_vals);
  GET DIAGNOSTICS n_codes = ROW_COUNT;
  UPDATE social_oauth_states x SET client_ip = NULL, client_mac = NULL, user_agent = NULL
   WHERE x.tenant_id = p_tenant AND x.client_mac::text = ANY (v_macs);
  GET DIAGNOSTICS n_oauth = ROW_COUNT;

  -- THE ACTIVITY LOG KEEPS WHO AND WHEN (D-R2). Entries about the guest's records, or naming them, keep their
  -- action, staff member and time; the record id gives way to the erasure reference, and every text naming the
  -- guest becomes "[erased]". A staff search whose text is part of the guest's name, room or another
  -- identifier is erased the same way. A guest's own address and browser go; a staff member's stay. A refusal
  -- below undoes this with everything else.
  v_ref := 'ER-' || upper(encode(public.gen_random_bytes(6), 'hex'));
  v_pats := ARRAY(SELECT '%' || replace(replace(replace(x, '\', '\\'), '%', '\%'), '_', '\_') || '%'
                    FROM unnest(v_all) x WHERE length(x) >= 3);
  UPDATE audit_log l
     SET payload = iam_v2.redact_guest_text(l.payload, v_all, l.action = 'guest_data.searched')
                   || jsonb_build_object('erasure_ref', v_ref),
         target_id = CASE WHEN l.target_id = ANY (v_ids) OR lower(l.target_id) = ANY (v_all) THEN NULL ELSE l.target_id END,
         ip = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.ip END,
         user_agent = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.user_agent END,
         actor_id = CASE WHEN l.actor_type IN ('operator', 'system', 'appliance') THEN l.actor_id END
   WHERE l.tenant_id = p_tenant AND l.ts >= v_since
     AND (l.target_id = ANY (v_ids) OR lower(l.target_id) = ANY (v_all)
          OR ((l.action = 'guest_data.searched' OR lower(l.payload::text) LIKE ANY (v_pats))
              AND iam_v2.redact_guest_text(l.payload, v_all, l.action = 'guest_data.searched') IS DISTINCT FROM l.payload));
  GET DIAGNOSTICS n_audit = ROW_COUNT;

  -- Refuses (and so undoes the above) while anything is live.
  RETURN iam_v2.erase_guest_records(p_tenant, p_site, p_kind, p_id, p_operator, p_reason,
           jsonb_build_object('verification_codes', n_codes, 'oauth_states', n_oauth, 'audit_entries', n_audit,
                              '_erasure_ref', v_ref));
END $function$;

-- ---------------------------------------------------------------------------------------------------------
-- 10. Retention.
-- ---------------------------------------------------------------------------------------------------------
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
       AND s.external_reservation_id NOT LIKE 'anon-%'
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

-- ---------------------------------------------------------------------------------------------------------
-- 11. Staff accounts: an account created disabled is dated; the address clean-up works in batches.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.operators_track_disabled() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status = 'disabled' THEN
      NEW.disabled_at := COALESCE(NEW.disabled_at, now());
    ELSE
      NEW.disabled_at := NULL;
    END IF;
    RETURN NEW;
  END IF;
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
DROP TRIGGER IF EXISTS operators_track_disabled ON public.operators;
CREATE TRIGGER operators_track_disabled BEFORE INSERT OR UPDATE OF status ON public.operators
  FOR EACH ROW EXECUTE FUNCTION public.operators_track_disabled();
UPDATE public.operators SET disabled_at = COALESCE(updated_at, now()) WHERE status = 'disabled' AND disabled_at IS NULL;

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

  RETURN jsonb_build_object('staff_accounts_anonymised', n_ops, 'audit_entries', n_audit,
                            'staff_addresses_cleared', n_net + n_https);
END $fn$;

-- ---------------------------------------------------------------------------------------------------------
-- 12. The nightly backup encrypts to this hotel's key holders only.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.backup_key_recipients(p_tenant uuid DEFAULT NULL, p_site uuid DEFAULT NULL)
    RETURNS SETOF text
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
  IF p_tenant IS NULL OR p_site IS NULL THEN
    -- The site the appliance serves is the one in public.sites; a previous owner's holders are never used.
    IF (SELECT count(DISTINCT (h.tenant_id, h.site_id)) FROM backup_key_holders h
          JOIN sites st ON st.id = h.site_id AND st.tenant_id = h.tenant_id
         WHERE h.status = 'ACTIVE') > 1 THEN
      RAISE EXCEPTION 'backup key holders of more than one site are active; name the site'
        USING ERRCODE = 'cardinality_violation';
    END IF;
    RETURN QUERY SELECT h.public_key FROM backup_key_holders h
                   JOIN sites st ON st.id = h.site_id AND st.tenant_id = h.tenant_id
                  WHERE h.status = 'ACTIVE' ORDER BY h.confirmed_at;
  ELSE
    RETURN QUERY SELECT public_key FROM backup_key_holders
                  WHERE status = 'ACTIVE' AND tenant_id = p_tenant AND site_id = p_site ORDER BY confirmed_at;
  END IF;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 13. Ownership and least privilege, mirrored in deploy/gatep/svc-guest-data-rights-grants.sql and
--     svc-data-retention-grants.sql.
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.retention_floor OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.site_day_start(uuid, date) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.site_today(uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.same_guest(text,text,text,text,text,text) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.same_guest_rows(uuid[],uuid[]) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.guest_data_identifiers(uuid,uuid,text,uuid) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.text_names_any(text,text[]) OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.redact_guest_text(jsonb,text[],boolean) OWNER TO iam_v2_owner';
    EXECUTE 'GRANT SELECT (id, tenant_id, timezone) ON public.sites TO iam_v2_owner';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    EXECUTE 'GRANT EXECUTE ON FUNCTION public.guest_data_reason_check(uuid,uuid,text,uuid,text) TO svc_edged';
  END IF;
END $own$;

COMMIT;
