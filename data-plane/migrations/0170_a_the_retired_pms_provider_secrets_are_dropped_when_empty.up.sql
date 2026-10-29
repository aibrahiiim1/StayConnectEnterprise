-- AD-39 (audit P7 L7): the legacy PMS provider layer is retired. Nothing reads public.pms_providers any more (scd's three admin
-- routes, internal/pmsloader, internal/pmsguard and the in-process Protel/Mews/Apaleo/stub providers are gone), but the table
-- still has two PLAINTEXT secret columns, auth_key and api_key, from the days those providers were configured by hand.
--
-- This drops those two columns ONLY IF they are provably unused on this database: every row has them empty. If any row still
-- holds a value the migration leaves the columns exactly as they are and says so, because dropping a stored secret is not
-- reversible and the operator may need to see it once. The table and its other columns stay (the cross-tenant purge and the
-- site migration still name the table); the rest of the retirement is a later, separate decision.
--
-- Idempotent: a second run, or a run after the columns are gone, does nothing.

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
        RAISE NOTICE '0170: % legacy PMS provider row(s) still hold a stored secret; auth_key and api_key are LEFT in place. '
                     'Remove the rows (they are not read by anything) and re-run this migration to drop the columns.', v_used;
        RETURN;
    END IF;
    IF v_has_auth THEN ALTER TABLE public.pms_providers DROP COLUMN auth_key; END IF;
    IF v_has_api  THEN ALTER TABLE public.pms_providers DROP COLUMN api_key;  END IF;
END $$;
