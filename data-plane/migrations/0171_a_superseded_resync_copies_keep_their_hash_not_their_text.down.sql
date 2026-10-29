-- Reverses 0171: every emptied payload is put back from the whole row it equals (or, if that row was anonymised since, the
-- anonymised marker, which is what the data now is). The stay_events guard is switched off for this one statement only;
-- it is a restore of evidence, which the guard (rightly) refuses in normal operation.
DO $$
BEGIN
    IF to_regclass('iam_v2.stay_event_payload_reductions') IS NULL THEN
        RETURN;
    END IF;
    ALTER TABLE iam_v2.stay_events DISABLE TRIGGER p3_stay_event_guard;
    ALTER TABLE iam_v2.stay_events DISABLE TRIGGER p3_stay_event_controlled_writer;
    UPDATE iam_v2.stay_events s
       SET payload = COALESCE(NULLIF(src.payload, '{}'::jsonb), '{"anonymised": true}'::jsonb)
      FROM iam_v2.stay_event_payload_reductions r
      JOIN iam_v2.stay_events src ON src.id = r.equals_event_id
     WHERE s.id = r.event_id AND s.payload = '{}'::jsonb;
    ALTER TABLE iam_v2.stay_events ENABLE TRIGGER p3_stay_event_guard;
    ALTER TABLE iam_v2.stay_events ENABLE TRIGGER p3_stay_event_controlled_writer;
END $$;
DROP FUNCTION IF EXISTS iam_v2.pms_resync_supersede_payloads(uuid, uuid, integer);
DROP INDEX IF EXISTS iam_v2.se_resync_payload_digest;
DROP TABLE IF EXISTS iam_v2.stay_event_payload_reductions;
