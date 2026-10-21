-- 0054 down: Central keeps everything again. Records already removed or anonymised are not restored.

BEGIN;

DROP FUNCTION IF EXISTS central_retention_apply();

CREATE OR REPLACE FUNCTION portal_tls_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'portal_tls_events is append-only';
END $$;

DROP TRIGGER IF EXISTS operators_track_disabled ON operators;
DROP FUNCTION IF EXISTS operators_track_disabled();
ALTER TABLE operators DROP COLUMN IF EXISTS anonymised_at, DROP COLUMN IF EXISTS disabled_at;

DROP TABLE IF EXISTS central_retention_settings;

COMMIT;
