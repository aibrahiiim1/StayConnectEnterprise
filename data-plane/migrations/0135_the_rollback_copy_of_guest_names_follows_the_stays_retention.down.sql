-- 0135 down: anonymise_stay no longer clears the 0066 copy. Names already cleared are not restored.

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
END $$;

COMMIT;
