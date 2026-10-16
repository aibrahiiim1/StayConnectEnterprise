-- REVERSES 0034_drop_legacy_sessions_appliance_fk: the archived sessions table gets its foreign key to
-- appliances back -- which is what made deleting an appliance with historical sessions fail. The sessions
-- table was removed by 0046 and exists again only after that down ran; the key is restored only when every
-- session still names an existing appliance.

BEGIN;

DO $$
BEGIN
  IF to_regclass('public.sessions') IS NULL THEN
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM sessions s WHERE s.appliance_id IS NOT NULL
               AND NOT EXISTS (SELECT 1 FROM appliances a WHERE a.id = s.appliance_id)) THEN
    RAISE EXCEPTION '0034 down: sessions name appliances that no longer exist; the foreign key cannot come back';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'sessions_appliance_id_fkey') THEN
    EXECUTE 'ALTER TABLE sessions ADD CONSTRAINT sessions_appliance_id_fkey
             FOREIGN KEY (appliance_id) REFERENCES appliances(id) ON DELETE CASCADE';
  END IF;
END $$;

COMMIT;
