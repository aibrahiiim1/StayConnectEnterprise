-- 0116 — the Client Portal is served over HTTPS on its own address, and a hotel's social-login secrets are
-- sealed (contract docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md).
--
--   * public.portal_https_settings: the ONE portal address (an RFC 1918 /32 that exists only inside the
--     appliance, answered on guest networks only) and the hotel portal domain the site asked for. An operator
--     setting (docs/OPERATIONAL_SETTINGS_POLICY.md): default, bounds, audit (portal_https_settings_changes).
--     The default address is chosen HERE, once, to avoid every subnet this appliance already carries.
--   * public.portal_tls_state: what scd last learnt from Central and installed -- the OneGate hostname, the
--     hotel domain's verification state, the canonical hostname, and whether HTTPS is active or the approved
--     HTTP fallback is in force (and why). Written by scd only; read by edged, netd and portald (via scd).
--   * public.portal_certificates: one row per hostname this appliance serves a certificate for. The private
--     key never enters the database (it is a 0640 file under /etc/stayconnect/portal-tls).
--   * social_oauth_providers: callback_hosts (the portal hostnames whose callback URL the hotel registered in
--     its own provider app); redirect_uri is now derived, so it becomes nullable; client_secret moves to
--     iam_v2.social_provider_secret_generations (AES-256-GCM under social_dek.key, sealed by scd).
--   * social_oauth_states: PKCE verifier and OIDC nonce for a sign-in attempt.
--
-- Additive; the .down removes what this adds (it cannot restore a plaintext secret it never kept).
BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Portal address + hotel portal domain (operator setting, singleton).
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.portal_https_settings (
  id             boolean PRIMARY KEY DEFAULT true CHECK (id),
  portal_ip      inet NOT NULL CHECK (family(portal_ip) = 4 AND masklen(portal_ip) = 32
                   AND (portal_ip << '10.0.0.0/8'::inet OR portal_ip << '172.16.0.0/12'::inet
                        OR portal_ip << '192.168.0.0/16'::inet)),
  custom_domain  text CHECK (custom_domain IS NULL OR (length(custom_domain) BETWEEN 4 AND 253
                   AND custom_domain = lower(custom_domain)
                   AND custom_domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$'
                   AND custom_domain NOT LIKE '%.onegate.work' AND custom_domain <> 'onegate.work')),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  updated_by     text
);
COMMENT ON TABLE public.portal_https_settings IS
  'The portal address (one RFC 1918 /32 that exists only inside the appliance on a dummy interface, reachable '
  'from guest networks on 443/80 only, never routed, never NATed, never on the WAN) and the hotel portal '
  'domain requested by the site. A change to portal_ip is a network change applied through netd''s '
  'validate/apply/confirm workflow. Every change is recorded in portal_https_settings_changes (0116).';

CREATE TABLE IF NOT EXISTS public.portal_https_settings_changes (
  id            bigserial PRIMARY KEY,
  changed_at    timestamptz NOT NULL DEFAULT now(),
  changed_by    text NOT NULL CHECK (length(changed_by) BETWEEN 1 AND 200),
  field         text NOT NULL CHECK (field IN ('portal_ip','custom_domain')),
  old_value     text,
  new_value     text,
  source_ip     inet
);
COMMENT ON TABLE public.portal_https_settings_changes IS
  'Append-only history of portal_https_settings (0116).';

CREATE OR REPLACE FUNCTION public.portal_https_settings_changes_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'portal_https_settings_changes is append-only';
END $$;
DROP TRIGGER IF EXISTS portal_https_settings_changes_append_only ON public.portal_https_settings_changes;
CREATE TRIGGER portal_https_settings_changes_append_only BEFORE UPDATE OR DELETE
  ON public.portal_https_settings_changes FOR EACH ROW EXECUTE FUNCTION public.portal_https_settings_changes_append_only();

-- The default address: the first candidate that no guest network on this appliance overlaps. Candidates avoid
-- 172.16.0.0/12 (Docker's default address pools live there) and 192.168.0.0/16 (common hotel and home LANs).
DO $seed$
DECLARE cand inet;
BEGIN
  IF EXISTS (SELECT 1 FROM public.portal_https_settings) THEN RETURN; END IF;
  FOREACH cand IN ARRAY ARRAY['10.255.255.1/32','10.254.254.1/32','10.253.253.1/32','10.252.252.1/32']::inet[] LOOP
    IF NOT EXISTS (SELECT 1 FROM public.guest_networks g WHERE g.subnet_cidr >>= cand) THEN
      INSERT INTO public.portal_https_settings (portal_ip, updated_by) VALUES (cand, 'migration 0116');
      INSERT INTO public.portal_https_settings_changes (changed_by, field, old_value, new_value)
        VALUES ('migration 0116', 'portal_ip', NULL, host(cand));
      RETURN;
    END IF;
  END LOOP;
  RAISE NOTICE 'portal_https_settings: every default candidate overlaps a guest network; the site must choose a portal address';
END $seed$;

-- ---------------------------------------------------------------------------------------------------------
-- 2. What scd learnt and installed (singleton, scd-written).
-- ---------------------------------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.portal_tls_state (
  id                     boolean PRIMARY KEY DEFAULT true CHECK (id),
  central_available      boolean NOT NULL DEFAULT false,
  onegate_hostname       text,
  dns_state              text NOT NULL DEFAULT 'UNKNOWN'
                           CHECK (dns_state IN ('UNKNOWN','PENDING','SYNCED','ERROR')),
  custom_domain          text,
  custom_domain_status   text CHECK (custom_domain_status IS NULL
                           OR custom_domain_status IN ('PENDING','VERIFIED','FAILED','REMOVED')),
  custom_domain_records  jsonb NOT NULL DEFAULT '[]'::jsonb,
  custom_domain_error    text,
  canonical_hostname     text,
  https_active           boolean NOT NULL DEFAULT false,
  fallback_reason        text CHECK (fallback_reason IS NULL OR fallback_reason IN (
                           'NOT_CONFIGURED','NO_HOSTNAME','NO_CERTIFICATE','CERTIFICATE_EXPIRED',
                           'PORTAL_ADDRESS_NOT_ACTIVE','CENTRAL_UNREACHABLE')),
  fallback_since         timestamptz,
  last_sync_at           timestamptz,
  last_sync_error        text,
  updated_at             timestamptz NOT NULL DEFAULT now(),
  CHECK (https_active = (fallback_reason IS NULL)),
  CHECK (NOT https_active OR canonical_hostname IS NOT NULL)
);
INSERT INTO public.portal_tls_state (fallback_reason, fallback_since) VALUES ('NOT_CONFIGURED', now())
  ON CONFLICT DO NOTHING;
COMMENT ON TABLE public.portal_tls_state IS
  'The portal''s HTTPS state as scd last established it (0116). https_active=false means the approved HTTP '
  'fallback is in force: manual code entry only, code-only emails, no social sign-in; fallback_reason says '
  'why. Recovery is automatic when a valid certificate is installed again.';

CREATE TABLE IF NOT EXISTS public.portal_certificates (
  hostname          text PRIMARY KEY CHECK (length(hostname) BETWEEN 4 AND 253 AND hostname = lower(hostname)),
  kind              text NOT NULL CHECK (kind IN ('ONEGATE','CUSTOM')),
  serial            text,
  not_before        timestamptz,
  not_after         timestamptz,
  renew_after       timestamptz,
  fingerprint       text,
  installed_at      timestamptz,
  pending_order_id  uuid,
  pending_since     timestamptz,
  last_error        text,
  last_attempt_at   timestamptz,
  updated_at        timestamptz NOT NULL DEFAULT now()
);
COMMENT ON TABLE public.portal_certificates IS
  'The publicly trusted certificate this appliance serves for each portal hostname (0116). The private key is '
  'generated on the appliance, never leaves it, and is a file, not a column.';

-- ---------------------------------------------------------------------------------------------------------
-- 3. Social sign-in: callback hosts, sealed secrets, PKCE + nonce.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE public.social_oauth_providers ADD COLUMN IF NOT EXISTS callback_hosts text[] NOT NULL DEFAULT '{}';
ALTER TABLE public.social_oauth_providers ALTER COLUMN redirect_uri DROP NOT NULL;
ALTER TABLE public.social_oauth_providers ALTER COLUMN client_secret DROP NOT NULL;
UPDATE public.social_oauth_providers
   SET callback_hosts = ARRAY[lower(substring(redirect_uri from '^https://([^/:?#]+)'))]
 WHERE cardinality(callback_hosts) = 0 AND redirect_uri ~ '^https://[^/:?#]+';
COMMENT ON COLUMN public.social_oauth_providers.callback_hosts IS
  'Portal hostnames whose callback URL (https://<host>/auth/social/callback) the hotel registered in its own '
  'provider app. The portal offers the provider only while HTTPS is active and the canonical hostname is one '
  'of these (0116).';

ALTER TABLE public.social_oauth_states ADD COLUMN IF NOT EXISTS code_verifier text;
ALTER TABLE public.social_oauth_states ADD COLUMN IF NOT EXISTS nonce text;

CREATE TABLE IF NOT EXISTS iam_v2.social_provider_secret_generations (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id      uuid NOT NULL,
  provider_id    uuid NOT NULL,
  generation_no  integer NOT NULL CONSTRAINT spsg_generation CHECK (generation_no >= 1),
  ciphertext     bytea NOT NULL,
  nonce          bytea NOT NULL,
  key_id         text NOT NULL,
  cipher_version integer NOT NULL DEFAULT 1,
  created_at     timestamptz NOT NULL DEFAULT now(),
  superseded_at  timestamptz,
  UNIQUE (tenant_id, provider_id, generation_no)
);
CREATE UNIQUE INDEX IF NOT EXISTS spsg_one_current
  ON iam_v2.social_provider_secret_generations (tenant_id, provider_id) WHERE superseded_at IS NULL;
COMMENT ON TABLE iam_v2.social_provider_secret_generations IS
  'The client secret (or Apple .p8 key) of a public.social_oauth_providers row, sealed by scd with AES-256-GCM '
  'under social_dek.key and AAD bound to (tenant, provider, generation). Exactly one current generation per '
  'provider. social_oauth_providers.client_secret is cleared once sealed (0116).';

-- ---------------------------------------------------------------------------------------------------------
-- 4. Ownership and least privilege. Mirrored in deploy/gatep/svc-portal-https-grants.sql.
-- ---------------------------------------------------------------------------------------------------------
DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.social_provider_secret_generations OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT ON public.portal_https_settings TO svc_scd;
    GRANT SELECT, INSERT, UPDATE ON public.portal_tls_state, public.portal_certificates TO svc_scd;
    GRANT DELETE ON public.portal_certificates TO svc_scd;
    GRANT SELECT, INSERT, UPDATE ON iam_v2.social_provider_secret_generations TO svc_scd;
    -- a cross-tenant transition purges the previous tenant's sealed secrets (cmd/scd/tenant_transition.go)
    GRANT DELETE ON iam_v2.social_provider_secret_generations, iam_v2.notification_provider_secret_generations TO svc_scd;
    GRANT UPDATE (client_secret) ON public.social_oauth_providers TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT, INSERT, UPDATE ON public.portal_https_settings TO svc_edged;
    GRANT SELECT, INSERT ON public.portal_https_settings_changes TO svc_edged;
    GRANT USAGE ON SEQUENCE public.portal_https_settings_changes_id_seq TO svc_edged;
    GRANT SELECT ON public.portal_tls_state, public.portal_certificates TO svc_edged;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_netd') THEN
    GRANT SELECT ON public.portal_https_settings, public.portal_tls_state TO svc_netd;
  END IF;
END $g$;

COMMIT;
