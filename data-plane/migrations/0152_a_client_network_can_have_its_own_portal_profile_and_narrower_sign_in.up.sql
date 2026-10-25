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
-- profile the stamp must come from the profile's design.
--
-- This is an iam_v2 trigger that runs AFTER the public stamp (BEFORE triggers fire in name order) and replaces the version only for a network that has a
-- profile. It deliberately changes nothing in `public`: a live site applies migrations as iam_v2_owner, which owns no public function and cannot create one.
-- The function is SECURITY DEFINER (owner iam_v2_owner) and records the version in public.privacy_notice_versions, which Gate-P grants it (SELECT, INSERT).
-- Like the public stamp it never stops a sign-in: any failure leaves the site notice stamp in place and logs a warning.
CREATE OR REPLACE FUNCTION iam_v2.profile_privacy_notice_stamp() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'iam_v2', 'pg_temp'
    AS $$
DECLARE pd jsonb; d jsonb; v_text text; v_url text; v_terms text; v_fp text; v_id uuid;
        c_builtin constant text := '[built-in notice] This Wi-Fi is provided by the hotel, which is responsible for your personal data on it. To give you internet access and keep the network secure, it records your device''s network addresses, when you connect and how much data you use, and what you sign in with. These records are deleted or made anonymous after a set time. Ask at reception for the hotel''s full privacy notice, or to see, correct or delete your data. (Shown in the guest''s language.)';
BEGIN
  IF NEW.guest_network_id IS NULL THEN
    RETURN NEW;
  END IF;
  SELECT p.doc INTO pd FROM iam_v2.portal_profile_assignments a JOIN iam_v2.portal_profiles p ON p.id = a.profile_id
   WHERE a.network_id = NEW.guest_network_id AND p.tenant_id = NEW.tenant_id;
  IF NOT FOUND THEN
    RETURN NEW;
  END IF;
  d := CASE WHEN jsonb_typeof(pd) = 'object' AND pd ? 'design' THEN pd -> 'design' ELSE pd END;
  IF jsonb_typeof(d) = 'object' THEN
    v_text  := NULLIF(CASE WHEN jsonb_typeof(d -> 'privacy_text') = 'string' THEN d ->> 'privacy_text' END, '');
    v_url   := NULLIF(CASE WHEN jsonb_typeof(d -> 'privacy_url') = 'string' THEN d ->> 'privacy_url' END, '');
    v_terms := NULLIF(CASE WHEN jsonb_typeof(d -> 'terms_url') = 'string' THEN d ->> 'terms_url' END, '');
  END IF;
  IF v_text IS NULL THEN
    v_text := c_builtin;
  END IF;
  v_fp := encode(sha256(convert_to(jsonb_build_array(v_text, v_url, v_terms)::text, 'UTF8')), 'hex');
  INSERT INTO public.privacy_notice_versions (tenant_id, fingerprint, privacy_text, privacy_url, terms_url)
  VALUES (NEW.tenant_id, v_fp, v_text, v_url, v_terms)
  ON CONFLICT ON CONSTRAINT pnv_one_per_content DO NOTHING
  RETURNING id INTO v_id;
  IF v_id IS NULL THEN
    SELECT x.id INTO v_id FROM public.privacy_notice_versions x WHERE x.tenant_id = NEW.tenant_id AND x.fingerprint = v_fp;
  END IF;
  IF v_id IS NOT NULL THEN
    NEW.privacy_notice_version_id := v_id;
  END IF;
  RETURN NEW;
EXCEPTION WHEN OTHERS THEN
  RAISE WARNING 'profile privacy notice not recorded for auth context %: % (%)', NEW.id, SQLERRM, SQLSTATE;
  RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS p9_z_profile_privacy_notice ON iam_v2.auth_contexts;
CREATE TRIGGER p9_z_profile_privacy_notice
  BEFORE INSERT ON iam_v2.auth_contexts
  FOR EACH ROW EXECUTE FUNCTION iam_v2.profile_privacy_notice_stamp();

COMMIT;
