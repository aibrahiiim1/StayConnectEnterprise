-- PORTAL HOSTNAMES AND CERTIFICATES (contract docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md).
--
-- Central allocates each appliance a permanent portal hostname under gw.onegate.work, publishes its A record
-- (the appliance's private portal address) in the onegate.work zone, verifies a hotel's own portal domain,
-- and obtains publicly trusted certificates for both from Let's Encrypt (ACME DNS-01) for CSRs the appliance
-- generates. The private keys stay on the appliances; Central keeps public material only. The DNS API token
-- and the ACME account key are root-only files under /etc/stayconnect/portal-tls, never columns.
--
-- Nothing here carries guest data: hostnames, addresses, certificates and their status only.

BEGIN;

CREATE TABLE IF NOT EXISTS portal_tls_settings (
  id                 boolean PRIMARY KEY DEFAULT true CHECK (id),
  acme_directory     text NOT NULL DEFAULT 'staging' CHECK (acme_directory IN ('staging','production')),
  contact_email      text CHECK (contact_email IS NULL OR contact_email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$'),
  zone_name          text NOT NULL DEFAULT 'onegate.work',
  zone_id            text,
  account_url        text,
  account_directory  text,
  weekly_new_limit   integer NOT NULL DEFAULT 45 CHECK (weekly_new_limit BETWEEN 1 AND 50),
  caa_applied_at     timestamptz,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  updated_by         text
);
INSERT INTO portal_tls_settings DEFAULT VALUES ON CONFLICT DO NOTHING;
COMMENT ON TABLE portal_tls_settings IS
  'How Central obtains portal certificates: ACME directory (staging until switched), contact, the DNS zone and '
  'the weekly cap on NEW gw.onegate.work certificates (Let''s Encrypt allows 50 per registered domain per 7 '
  'days; renewals are exempt). The DNS token and ACME account key are files, not columns.';

CREATE TABLE IF NOT EXISTS portal_hostnames (
  appliance_id    uuid PRIMARY KEY REFERENCES appliances(id) ON DELETE CASCADE,
  label           text NOT NULL UNIQUE CHECK (label ~ '^[a-z][a-z0-9]{7}$'),
  fqdn            text NOT NULL UNIQUE,
  portal_ip       inet CHECK (portal_ip IS NULL OR (family(portal_ip) = 4 AND masklen(portal_ip) = 32)),
  dns_record_id   text,
  dns_synced_ip   inet,
  dns_synced_at   timestamptz,
  dns_error       text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  released_at     timestamptz
);
COMMENT ON TABLE portal_hostnames IS
  'The permanent portal hostname of each appliance (<label>.gw.onegate.work) and the private portal address '
  'its public A record points at. released_at is set when the appliance is decommissioned; a released label '
  'is never reused.';

CREATE TABLE IF NOT EXISTS portal_custom_domains (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  appliance_id     uuid NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
  fqdn             text NOT NULL CHECK (fqdn = lower(fqdn) AND length(fqdn) BETWEEN 4 AND 253),
  verify_token     text NOT NULL,
  challenge_label  text NOT NULL UNIQUE CHECK (challenge_label ~ '^[a-z0-9]{16}$'),
  status           text NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','VERIFIED','FAILED','REMOVED')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  verified_at      timestamptz,
  last_checked_at  timestamptz,
  last_error       text,
  removed_at       timestamptz,
  CHECK ((status = 'REMOVED') = (removed_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS portal_custom_domains_one_claim ON portal_custom_domains (fqdn) WHERE status <> 'REMOVED';
CREATE UNIQUE INDEX IF NOT EXISTS portal_custom_domains_one_per_appliance ON portal_custom_domains (appliance_id) WHERE status <> 'REMOVED';
COMMENT ON TABLE portal_custom_domains IS
  'A hotel''s own portal hostname. Proven by three public records the hotel publishes: TXT '
  '_onegate-verify.<fqdn> = verify_token, CNAME <fqdn> -> <label>.gw.onegate.work, and CNAME '
  '_acme-challenge.<fqdn> -> <challenge_label>.acme.onegate.work (the only name Central ever writes for it). '
  'One live claim per hostname, one hotel domain per appliance.';

CREATE TABLE IF NOT EXISTS portal_certificate_orders (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  appliance_id     uuid NOT NULL REFERENCES appliances(id) ON DELETE CASCADE,
  hostname         text NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('ONEGATE','CUSTOM')),
  csr_der          bytea NOT NULL,
  status           text NOT NULL DEFAULT 'QUEUED'
                     CHECK (status IN ('QUEUED','PROCESSING','ISSUED','FAILED','REVOKED','SUPERSEDED')),
  counts_new       boolean NOT NULL DEFAULT false,
  attempts         integer NOT NULL DEFAULT 0,
  next_attempt_at  timestamptz NOT NULL DEFAULT now(),
  acme_order_url   text,
  -- The ACME directory that issued (or is issuing) this certificate. Staging and production certificates
  -- coexist while a deployment is switched over; ARI and revocation must go to the CA that issued each one.
  acme_directory_url text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  issued_at        timestamptz,
  not_before       timestamptz,
  not_after        timestamptz,
  serial           text,
  chain_pem        text,
  renew_after      timestamptz,
  ari_checked_at   timestamptz,
  error            text,
  revoke_requested_at timestamptz,
  revoke_reason    text,
  revoked_at       timestamptz
);
CREATE INDEX IF NOT EXISTS portal_certificate_orders_work ON portal_certificate_orders (next_attempt_at)
  WHERE status IN ('QUEUED','PROCESSING');
CREATE INDEX IF NOT EXISTS portal_certificate_orders_appliance ON portal_certificate_orders (appliance_id, hostname, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS portal_certificate_orders_one_open
  ON portal_certificate_orders (appliance_id, hostname) WHERE status IN ('QUEUED','PROCESSING');
COMMENT ON TABLE portal_certificate_orders IS
  'One certificate request from an appliance (its CSR; the key never leaves the appliance) and its outcome. '
  'counts_new marks an issuance that counts against the weekly cap on new gw.onegate.work certificates.';

CREATE TABLE IF NOT EXISTS portal_tls_events (
  id            bigserial PRIMARY KEY,
  at            timestamptz NOT NULL DEFAULT now(),
  appliance_id  uuid,
  hostname      text,
  kind          text NOT NULL,
  actor         text,
  detail        jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS portal_tls_events_appliance ON portal_tls_events (appliance_id, at DESC);
COMMENT ON TABLE portal_tls_events IS
  'Audit trail of portal hostname, DNS, domain-verification and certificate actions. Append-only.';

CREATE OR REPLACE FUNCTION portal_tls_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'portal_tls_events is append-only';
END $$;
DROP TRIGGER IF EXISTS portal_tls_events_append_only ON portal_tls_events;
CREATE TRIGGER portal_tls_events_append_only BEFORE UPDATE OR DELETE ON portal_tls_events
  FOR EACH ROW EXECUTE FUNCTION portal_tls_events_append_only();

COMMIT;
