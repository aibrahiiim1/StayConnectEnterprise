-- 0130 -- EACH SIGN-IN RECORDS THE PRIVACY NOTICE IN FORCE
-- (GDPR remediation plan C6; Product-Owner decision D-C6: the portal shows the notice and there is no checkbox,
--  so the record is THAT the notice was shown, not consent; docs/privacy/GDPR_REMEDIATION_PLAN.md section 4.)
--
-- The hotel's notice (privacy_text, privacy_url) and terms link live in the portal design, tenants.branding. That
-- document keeps only its last 20 saves, so it cannot prove what a guest was shown a year ago. Two things are
-- added:
--
--   public.privacy_notice_versions  every distinct (notice text, notice link, terms link) a sign-in happened under,
--                                   stored once by its SHA-256 fingerprint, append-only. A design with no notice
--                                   is a version too, so "nothing was shown" is recorded rather than implied.
--   iam_v2.auth_contexts.privacy_notice_version_id
--                                   stamped by a trigger on every new sign-in, whatever the method (PMS, voucher,
--                                   account, code, social, post-stay, open selection, remembered device): every
--                                   one of them mints an auth context, through four different writers. The stamp,
--                                   and privacy_notice_shown_at (the sign-in time, which the table lacked),
--                                   cannot be supplied by the writer and cannot be changed afterwards. NULL means
--                                   the sign-in happened before this migration.
--
-- The stamp is the notice in force when the sign-in completed. The page a guest read was rendered at most a few
-- minutes earlier from the same design (portald caches it for 3 seconds); a notice edited inside that window is
-- the only case the two can differ.
--
-- Recording must never stop a guest signing in: if the stamp cannot be made, the sign-in goes ahead unstamped and
-- the database logs a warning. The notice text is the hotel's, not personal data, so neither retention nor
-- erasure touches the versions; the auth context they hang off holds no identifier of its own. The guest export
-- (public.guest_data_export, 0128) gains the notices each guest was shown.

BEGIN;

CREATE TABLE IF NOT EXISTS public.privacy_notice_versions (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL,
  fingerprint    text NOT NULL CONSTRAINT pnv_fingerprint CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
  privacy_text   text,
  privacy_url    text,
  terms_url      text,
  first_shown_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT pnv_one_per_content UNIQUE (tenant_id, fingerprint)
);
COMMENT ON TABLE public.privacy_notice_versions IS
  'Every privacy notice (with the terms link) a guest sign-in happened under (0130, GDPR plan C6, decision D-C6), '
  'stored once by the SHA-256 of [privacy_text, privacy_url, terms_url]. Append-only. Written only by '
  'public.privacy_notice_in_force.';

CREATE OR REPLACE FUNCTION public.privacy_notice_versions_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'privacy_notice_versions is append-only: % refused', TG_OP USING ERRCODE = 'restrict_violation';
END $$;
DROP TRIGGER IF EXISTS privacy_notice_versions_append_only ON public.privacy_notice_versions;
CREATE TRIGGER privacy_notice_versions_append_only BEFORE UPDATE OR DELETE ON public.privacy_notice_versions
  FOR EACH ROW EXECUTE FUNCTION public.privacy_notice_versions_append_only();
DROP TRIGGER IF EXISTS privacy_notice_versions_no_truncate ON public.privacy_notice_versions;
CREATE TRIGGER privacy_notice_versions_no_truncate BEFORE TRUNCATE ON public.privacy_notice_versions
  FOR EACH STATEMENT EXECUTE FUNCTION public.privacy_notice_versions_append_only();

-- The notice in force for a tenant now, as a version id; NULL when the tenant has no row. The design is the
-- "design" member of tenants.branding, or the whole document for one saved before revisions existed (the same
-- rule edged's loadBranding applies).
CREATE OR REPLACE FUNCTION public.privacy_notice_in_force(p_tenant uuid) RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE b jsonb; d jsonb; v_text text; v_url text; v_terms text; v_fp text; v_id uuid;
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
COMMENT ON FUNCTION public.privacy_notice_in_force(uuid) IS
  'The privacy notice in force for a tenant now, as a public.privacy_notice_versions id, recording the version on '
  'first use (0130, GDPR plan C6). NULL when the tenant has no row.';

ALTER TABLE iam_v2.auth_contexts
  ADD COLUMN IF NOT EXISTS privacy_notice_version_id uuid REFERENCES public.privacy_notice_versions (id),
  ADD COLUMN IF NOT EXISTS privacy_notice_shown_at timestamptz;
COMMENT ON COLUMN iam_v2.auth_contexts.privacy_notice_version_id IS
  'The privacy notice in force when this sign-in completed (0130, GDPR plan C6). Set by trigger, never by the '
  'writer, and never changed. NULL: before 0130, or the stamp could not be made (the sign-in was not refused).';
COMMENT ON COLUMN iam_v2.auth_contexts.privacy_notice_shown_at IS
  'When this sign-in was stamped with its privacy notice (0130, GDPR plan C6): the sign-in time, which the table '
  'did not otherwise record. Set by trigger with the stamp, never changed. NULL before 0130.';

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
COMMENT ON FUNCTION public.stamp_privacy_notice() IS
  'Trigger on iam_v2.auth_contexts (0130, GDPR plan C6): stamps every new sign-in with the privacy notice in '
  'force and refuses any later change to that stamp. A failure to stamp never refuses the sign-in.';

DROP TRIGGER IF EXISTS p9_auth_context_privacy_notice ON iam_v2.auth_contexts;
CREATE TRIGGER p9_auth_context_privacy_notice
  BEFORE INSERT OR UPDATE OF privacy_notice_version_id, privacy_notice_shown_at ON iam_v2.auth_contexts
  FOR EACH ROW EXECUTE FUNCTION public.stamp_privacy_notice();

-- ---------------------------------------------------------------------------------------------------------
-- The guest export (0128) gains the notices each guest was shown: one entry per sign-in, and the text of each
-- distinct notice once. Unchanged otherwise.
-- ---------------------------------------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION public.guest_data_export(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
    RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE v jsonb; v_ids text[]; v_vals text[];
        a uuid[]; p uuid[]; s uuid[]; e uuid[]; d uuid[]; v_ctx uuid[];
BEGIN
  v := iam_v2.guest_data_export_records(p_tenant, p_site, p_kind, p_id);
  SELECT COALESCE(array_agg(DISTINCT o_id::text), '{}'),
         COALESCE(array_agg(DISTINCT lower(o_value)) FILTER (WHERE o_kind IN ('account', 'identity') AND o_value IS NOT NULL), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'account'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind IN ('principal', 'identity')), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'stay'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'entitlement'), '{}'),
         COALESCE(array_agg(DISTINCT o_id) FILTER (WHERE o_kind = 'device'), '{}')
    INTO v_ids, v_vals, a, p, s, e, d FROM iam_v2.guest_data_scope(p_tenant, p_site, p_kind, p_id);
  -- The guest's sign-ins: by their own subject, from a device only they used, or the one that paid for access
  -- they held. A shared device is not in d, so another guest's sign-in on it is not exported.
  SELECT COALESCE(array_agg(ac.id), '{}') INTO v_ctx FROM iam_v2.auth_contexts ac
   WHERE ac.tenant_id = p_tenant AND ac.site_id = p_site
     AND (ac.guest_account_id = ANY (a) OR ac.guest_principal_id = ANY (p) OR ac.stay_id = ANY (s)
          OR ac.device_id = ANY (d)
          OR ac.id IN (SELECT pu.auth_context_id FROM iam_v2.purchases pu
                         JOIN iam_v2.entitlements en ON en.purchase_id = pu.id WHERE en.id = ANY (e)));
  RETURN jsonb_build_object('format', 'stayconnect.guest-data-export/1', 'exported_at', now()) || v || jsonb_build_object(
    'verification_codes', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'channel', o.channel, 'sent_to', o.destination, 'sent_at', o.issued_at, 'used_at', o.consumed_at)
        ORDER BY o.issued_at), '[]'::jsonb)
      FROM auth_otps o WHERE o.tenant_id = p_tenant AND lower(o.destination) = ANY (v_vals)),
    'activity_log', (SELECT COALESCE(jsonb_agg(jsonb_build_object('at', l.ts, 'action', l.action) ORDER BY l.ts), '[]'::jsonb)
      FROM audit_log l WHERE l.tenant_id = p_tenant AND l.target_id = ANY (v_ids)),
    'privacy_notices_shown', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'signed_in_at', ac.privacy_notice_shown_at, 'sign_in_method', ac.method, 'notice', left(pv.fingerprint, 12))
        ORDER BY ac.privacy_notice_shown_at), '[]'::jsonb)
      FROM iam_v2.auth_contexts ac JOIN privacy_notice_versions pv ON pv.id = ac.privacy_notice_version_id
     WHERE ac.id = ANY (v_ctx)),
    'privacy_notices', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'notice', left(pv.fingerprint, 12), 'first_shown_at', pv.first_shown_at, 'text', pv.privacy_text,
        'link', pv.privacy_url, 'terms_link', pv.terms_url) ORDER BY pv.first_shown_at), '[]'::jsonb)
      FROM privacy_notice_versions pv
     WHERE pv.id IN (SELECT ac.privacy_notice_version_id FROM iam_v2.auth_contexts ac WHERE ac.id = ANY (v_ctx))));
END $$;
COMMENT ON FUNCTION public.guest_data_export(uuid, uuid, text, uuid) IS
  'One guest''s data as a JSON document (0128, GDPR plan C4, Art. 15/20): client accounts, identities and sign-in '
  'methods, hotel stays, internet access and purchases, devices, sessions, sign-in attempts, verification codes '
  'sent, the activity log entries about them and (0130, C6) the privacy notices each sign-in happened under. '
  'Changes nothing.';

REVOKE ALL ON public.privacy_notice_versions FROM PUBLIC;
REVOKE ALL ON FUNCTION public.privacy_notice_in_force(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.stamp_privacy_notice() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.privacy_notice_versions_append_only() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.guest_data_export(uuid,uuid,text,uuid) FROM PUBLIC;

COMMIT;
