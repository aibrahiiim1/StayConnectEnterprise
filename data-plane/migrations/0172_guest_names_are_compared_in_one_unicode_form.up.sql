-- 0172 -- GUEST NAMES AND ROOMS ARE COMPARED IN ONE UNICODE FORM (audit decision AD-33).
--
-- internal/namenorm now brings every name and room to NFC (Unicode canonical composition) around the trim and the
-- upper-case, on the stored side and on the typed side. A name the PMS stored decomposed (a letter plus a combining
-- mark) never matched what a guest's keyboard produces. NFC only unifies spellings that are canonically the SAME text;
-- it strips and loosens nothing (the namenorm "deliberately not done" list stands).
--
-- This migration is the SQL twin: it redefines iam_v2.norm_identity_text and re-normalises the stored *_norm columns,
-- so rows written before the Go change are compared in the same form. It REWRITES DATA, but only to a canonically
-- equivalent spelling, so no original is lost in meaning and the down migration needs no restore. It is idempotent:
-- every UPDATE carries WHERE value IS DISTINCT FROM normalised(value), so a second run changes nothing.
--
-- Columns: iam_v2.stay_guests.first_name_norm, iam_v2.stay_guests.last_name_norm, iam_v2.stays.normalized_room_number.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.norm_identity_text(s text)
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE
AS $$
  SELECT CASE WHEN s IS NULL THEN NULL
              ELSE normalize(upper(regexp_replace(regexp_replace(normalize(s, NFC), '^\s+', ''), '\s+$', '')), NFC)
         END
$$;

COMMENT ON FUNCTION iam_v2.norm_identity_text(text) IS
  'Canonical form for the *_norm identity columns: NFC, Unicode-aware trim, upper-case, NFC. Must stay identical to '
  'data-plane/internal/namenorm. Introduced by migration 0066; NFC added by 0172 (AD-33).';

DO $n$
DECLARE v_guests bigint; v_rooms bigint;
BEGIN
  UPDATE iam_v2.stay_guests g
     SET first_name_norm = iam_v2.norm_identity_text(g.first_name_norm),
         last_name_norm  = iam_v2.norm_identity_text(g.last_name_norm)
   WHERE g.first_name_norm IS DISTINCT FROM iam_v2.norm_identity_text(g.first_name_norm)
      OR g.last_name_norm  IS DISTINCT FROM iam_v2.norm_identity_text(g.last_name_norm);
  GET DIAGNOSTICS v_guests = ROW_COUNT;
  UPDATE iam_v2.stays s
     SET normalized_room_number = iam_v2.norm_identity_text(s.normalized_room_number)
   WHERE s.normalized_room_number IS DISTINCT FROM iam_v2.norm_identity_text(s.normalized_room_number);
  GET DIAGNOSTICS v_rooms = ROW_COUNT;
  RAISE NOTICE '0172: re-normalised % stay guests and % stay rooms', v_guests, v_rooms;
END $n$;

COMMIT;
