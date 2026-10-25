-- Down for 0152. Named profiles and per-network narrowing are lost; the site's default portal design (tenants.branding) is untouched, and every network
-- returns to it and to the site's sign-in methods.

BEGIN;

-- Back to stamping every sign-in with the site's notice only.
CREATE OR REPLACE FUNCTION public.stamp_privacy_notice() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
BEGIN
  IF TG_OP = 'UPDATE' THEN
    IF NEW.privacy_notice_version_id IS DISTINCT FROM OLD.privacy_notice_version_id
       OR NEW.privacy_notice_shown_at IS DISTINCT FROM OLD.privacy_notice_shown_at THEN
      RAISE EXCEPTION 'the privacy notice a sign-in happened under is a record and cannot be changed'
        USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
  END IF;
  -- Whatever the writer supplied is replaced: the stamp is the database's, not the caller's.
  NEW.privacy_notice_version_id := NULL;
  NEW.privacy_notice_shown_at := now();
  BEGIN
    NEW.privacy_notice_version_id := public.privacy_notice_in_force(NEW.tenant_id);
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'privacy notice not recorded for auth context %: % (%)', NEW.id, SQLERRM, SQLSTATE;
  END;
  RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION public.privacy_notice_in_force(p_tenant uuid) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE b jsonb; d jsonb; v_text text; v_url text; v_terms text; v_fp text; v_id uuid;
        c_builtin constant text := '[built-in notice] This Wi-Fi is provided by the hotel, which is responsible for your personal data on it. To give you internet access and keep the network secure, it records your device''s network addresses, when you connect and how much data you use, and what you sign in with. These records are deleted or made anonymous after a set time. Ask at reception for the hotel''s full privacy notice, or to see, correct or delete your data. (Shown in the guest''s language.)';
BEGIN
  SELECT t.branding INTO b FROM public.tenants t WHERE t.id = p_tenant;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;
  d := CASE WHEN jsonb_typeof(b) = 'object' AND b ? 'design' THEN b -> 'design' ELSE b END;
  IF jsonb_typeof(d) = 'object' THEN
    v_text  := NULLIF(CASE WHEN jsonb_typeof(d -> 'privacy_text') = 'string' THEN d ->> 'privacy_text' END, '');
    v_url   := NULLIF(CASE WHEN jsonb_typeof(d -> 'privacy_url') = 'string' THEN d ->> 'privacy_url' END, '');
    v_terms := NULLIF(CASE WHEN jsonb_typeof(d -> 'terms_url') = 'string' THEN d ->> 'terms_url' END, '');
  END IF;
  -- With no notice of its own the portal shows its short built-in one (privacy.default): that is what the guest was shown,
  -- and what is recorded -- not "nothing".
  IF v_text IS NULL THEN
    v_text := c_builtin;
  END IF;
  v_fp := encode(sha256(convert_to(jsonb_build_array(v_text, v_url, v_terms)::text, 'UTF8')), 'hex');

  INSERT INTO public.privacy_notice_versions (tenant_id, fingerprint, privacy_text, privacy_url, terms_url)
  VALUES (p_tenant, v_fp, v_text, v_url, v_terms)
  ON CONFLICT ON CONSTRAINT pnv_one_per_content DO NOTHING
  RETURNING id INTO v_id;
  IF v_id IS NULL THEN
    SELECT x.id INTO v_id FROM public.privacy_notice_versions x WHERE x.tenant_id = p_tenant AND x.fingerprint = v_fp;
  END IF;
  RETURN v_id;
END $$;

DROP FUNCTION IF EXISTS public.privacy_notice_in_force(uuid, uuid);

DROP TABLE IF EXISTS iam_v2.network_signin_overrides;
DROP TABLE IF EXISTS iam_v2.portal_profile_assignments;
DROP TABLE IF EXISTS iam_v2.portal_profiles;

COMMIT;
