-- 0212 -- THE RETIRED PMS PROVIDER SECRETS ARE DROPPED ONCE EMPTIED (reliability audit, Product Owner authorization 2026-11-06).
-- edge-migrate: platform
--
-- 0170 drops public.pms_providers.auth_key and api_key only when no row holds a value. An appliance that applied 0170 while legacy
-- rows still held secrets kept both columns, and once the rows were removed nothing ran 0170 again: the test appliance is in that state
-- and it is its only schema difference from a fresh installation (docs/runbooks/LEGACY_PMS_PROVIDER_COLUMNS.md). This is 0170's check
-- again, so such an appliance converges on upgrade.
--
-- Unchanged from 0170: the columns are dropped ONLY if every row has them empty; otherwise nothing changes and a NOTICE says so.
-- Idempotent, and a no-op on a fresh installation (the baseline has no such columns).

DO $$
DECLARE v_used bigint; v_has_auth boolean; v_has_api boolean;
BEGIN
    IF to_regclass('public.pms_providers') IS NULL THEN
        RETURN;
    END IF;
    SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='pms_providers' AND column_name='auth_key'),
           EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='pms_providers' AND column_name='api_key')
      INTO v_has_auth, v_has_api;
    IF NOT v_has_auth AND NOT v_has_api THEN
        RETURN;
    END IF;
    EXECUTE format('SELECT count(*) FROM public.pms_providers WHERE %s',
                   concat_ws(' OR ',
                             CASE WHEN v_has_auth THEN 'coalesce(auth_key, '''') <> ''''' END,
                             CASE WHEN v_has_api  THEN 'coalesce(api_key,  '''') <> ''''' END))
       INTO v_used;
    IF v_used > 0 THEN
        RAISE NOTICE '0212: % legacy PMS provider row(s) still hold a stored secret; auth_key and api_key are LEFT in place.', v_used;
        RETURN;
    END IF;
    IF v_has_auth THEN ALTER TABLE public.pms_providers DROP COLUMN auth_key; END IF;
    IF v_has_api  THEN ALTER TABLE public.pms_providers DROP COLUMN api_key;  END IF;
END $$;
