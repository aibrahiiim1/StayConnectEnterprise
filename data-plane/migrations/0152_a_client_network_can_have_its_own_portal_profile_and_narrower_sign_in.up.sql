-- 0152 — PORTAL PROFILES, THEIR ASSIGNMENT TO CLIENT NETWORKS, AND PER-NETWORK SIGN-IN NARROWING.
--
-- The site's portal design stays where it is (tenants.branding) and is the DEFAULT profile: every network without an assignment shows it, so nothing already
-- accepted changes. A named profile is the same document (design, draft, revisions) kept in a row here, assigned to any number of networks. A profile carries
-- appearance and wording only; it does not decide which sign-in methods exist, which PMS interface a network uses, or who may sign in.
--
-- network_signin_overrides can only NARROW what the site offers: methods can be switched off for a network, the order can be set, and a shorter remember-device
-- period can be chosen. Nothing here can enable a method the licence, the deployment or the site does not already allow.
--
-- No foreign keys to public.* (iam_v2_owner owns nothing there). The rows hold no guest data.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.portal_profiles (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  name       text NOT NULL,
  CONSTRAINT pp_name CHECK (length(btrim(name)) BETWEEN 1 AND 80),
  doc        jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS pp_name_per_site ON iam_v2.portal_profiles (site_id, lower(btrim(name)));

CREATE TABLE IF NOT EXISTS iam_v2.portal_profile_assignments (
  network_id uuid PRIMARY KEY,
  profile_id uuid NOT NULL REFERENCES iam_v2.portal_profiles(id) ON DELETE CASCADE,
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  assigned_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ppa_profile ON iam_v2.portal_profile_assignments (profile_id);

CREATE TABLE IF NOT EXISTS iam_v2.network_signin_overrides (
  network_id       uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  site_id          uuid NOT NULL,
  disabled_methods text[] NOT NULL DEFAULT '{}',
  primary_method   text,
  remember_days    integer,
  CONSTRAINT nso_remember CHECK (remember_days IS NULL OR remember_days BETWEEN 0 AND 365),
  updated_at       timestamptz NOT NULL DEFAULT now()
);

DO $own$
DECLARE t text;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    FOREACH t IN ARRAY ARRAY['portal_profiles','portal_profile_assignments','network_signin_overrides'] LOOP
      EXECUTE format('ALTER TABLE iam_v2.%I OWNER TO iam_v2_owner', t);
    END LOOP;
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON iam_v2.portal_profiles, iam_v2.portal_profile_assignments, iam_v2.network_signin_overrides TO svc_edged;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT ON iam_v2.portal_profiles, iam_v2.portal_profile_assignments, iam_v2.network_signin_overrides TO svc_scd;
  END IF;
END;
$g$;

-- The privacy notice a sign-in is recorded under (0130) is the one the portal showed. A profile carries its own notice text and links, so for a network with a
-- profile the stamp comes from the profile's design; every other network keeps the site's notice.
CREATE OR REPLACE FUNCTION public.privacy_notice_in_force(p_tenant uuid, p_network uuid) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE b jsonb; pd jsonb; d jsonb; v_text text; v_url text; v_terms text; v_fp text; v_id uuid;
        c_builtin constant text := '[built-in notice] This Wi-Fi is provided by the hotel, which is responsible for your personal data on it. To give you internet access and keep the network secure, it records your device''s network addresses, when you connect and how much data you use, and what you sign in with. These records are deleted or made anonymous after a set time. Ask at reception for the hotel''s full privacy notice, or to see, correct or delete your data. (Shown in the guest''s language.)';
BEGIN
  SELECT t.branding INTO b FROM public.tenants t WHERE t.id = p_tenant;
  IF NOT FOUND THEN
    RETURN NULL;
  END IF;
  -- A network with its own portal profile shows that profile's notice, so that is the notice the sign-in is recorded under.
  IF p_network IS NOT NULL THEN
    SELECT p.doc INTO pd FROM iam_v2.portal_profile_assignments a JOIN iam_v2.portal_profiles p ON p.id = a.profile_id
     WHERE a.network_id = p_network AND p.tenant_id = p_tenant;
    IF FOUND THEN
      b := pd;
    END IF;
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

CREATE OR REPLACE FUNCTION public.privacy_notice_in_force(p_tenant uuid) RETURNS uuid
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$ SELECT public.privacy_notice_in_force(p_tenant, NULL::uuid) $$;

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
    NEW.privacy_notice_version_id := public.privacy_notice_in_force(NEW.tenant_id, NEW.guest_network_id);
  EXCEPTION WHEN OTHERS THEN
    RAISE WARNING 'privacy notice not recorded for auth context %: % (%)', NEW.id, SQLERRM, SQLSTATE;
  END;
  RETURN NEW;
END $$;

COMMIT;
