-- Reverses 0203: the two columns come back empty (they were dropped only when every value was already empty).
DO $$ BEGIN
    IF to_regclass('public.pms_providers') IS NOT NULL THEN
        ALTER TABLE public.pms_providers ADD COLUMN IF NOT EXISTS auth_key text;
        ALTER TABLE public.pms_providers ADD COLUMN IF NOT EXISTS api_key text;
    END IF;
END $$;
