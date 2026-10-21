-- 0135 -- THE ROLLBACK COPY OF GUEST NAMES FOLLOWS THE STAY'S RETENTION
-- (GDPR audit 2026-10-19, A6.)
--
-- Migration 0066 kept the guest names and rooms it normalised in iam_v2.backfill_0066_identity_text, as rollback
-- evidence. The table was to be dropped when 0066's rollback window closed, which never happened, and the
-- hourly retention never touched it: only a guest erasure (0128) cleared it. So the names outlived the stay's
-- retention period.
--
--   * iam_v2.anonymise_stay, which retention and erasure both call, now also clears the copy for that stay's
--     room and guests;
--   * the copy is cleared now for every stay already anonymised, and for rows whose stay or guest is gone.
--
-- This applies the retention the Product Owner approved (D-C1) to a copy it had missed. Dropping the table is a
-- separate decision and is not made here. The rows that remain are those of stays still inside their period.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.anonymise_stay(p_stay uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'iam_v2', 'pg_temp'
    AS $$
BEGIN
  PERFORM set_config('iam_v2.anonymise', 'retention', true);
  PERFORM iam_v2.begin_controlled_operation('stay');
  UPDATE iam_v2.stays
     SET external_reservation_id = iam_v2.anonymised_token(), external_stay_identity = iam_v2.anonymised_token(),
         normalized_room_number = NULL, vip = NULL, travel_agent = NULL, rate_plan = NULL
   WHERE id = p_stay AND external_reservation_id NOT LIKE 'anon-%';
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

DO $do$
BEGIN
  IF to_regclass('iam_v2.backfill_0066_identity_text') IS NOT NULL THEN
    UPDATE iam_v2.backfill_0066_identity_text x
       SET prior_first_name = NULL, prior_last_name = NULL, prior_room = NULL
     WHERE num_nonnulls(x.prior_first_name, x.prior_last_name, x.prior_room) > 0
       AND CASE x.kind
             WHEN 'stay_room'  THEN NOT EXISTS (SELECT 1 FROM iam_v2.stays s
                                                  WHERE s.id = x.row_id AND s.external_reservation_id NOT LIKE 'anon-%')
             WHEN 'stay_guest' THEN NOT EXISTS (SELECT 1 FROM iam_v2.stay_guests g
                                                  JOIN iam_v2.stays s ON s.id = g.stay_id
                                                 WHERE g.id = x.row_id AND s.external_reservation_id NOT LIKE 'anon-%')
           END;
  END IF;
END $do$;

COMMIT;
