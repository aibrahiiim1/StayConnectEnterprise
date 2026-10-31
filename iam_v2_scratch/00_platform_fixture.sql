-- SCRATCH platform fixture — minimal stand-ins for EXISTING live platform tables in `public`.
-- This is NOT an IAM migration; it represents the pre-existing platform so cross-schema FKs resolve.
-- The IAM migrations (MG-0..MG-9) add NOTHING to public except the MG-0 guest_networks anchor.
BEGIN;
CREATE TABLE IF NOT EXISTS public.tenants (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid());
-- The hotel's guest sign-in method switches (Hotel Admin > Sign-in methods). scd reads them on every guest
-- sign-in and refuses a switched-off method before anything else, so a harness without the column refused
-- every room sign-in as "unreadable switches" and none of the room sign-in suites tested what they claim.
-- Same column, type, default and nullability migration 0001 creates on a real appliance.
ALTER TABLE public.tenants ADD COLUMN IF NOT EXISTS auth_methods jsonb
  DEFAULT '{"voucher": {"enabled": true, "template_id": null}}'::jsonb NOT NULL;
CREATE TABLE IF NOT EXISTS public.sites (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES public.tenants(id),
  UNIQUE (tenant_id, id));
CREATE TABLE IF NOT EXISTS public.guest_networks (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL);
-- The Phase-3 guest path resolves a device's NETWORK from its source address, against these columns. A stub
-- carrying only ids cannot exercise that at all — a test against it would prove the resolver runs, not that a
-- real guest on a real subnet reaches the right PMS interface. Added idempotently so the fixture stays safe to
-- re-run and matches the shape migration 0002 creates on a real appliance.
ALTER TABLE public.guest_networks
  ADD COLUMN IF NOT EXISTS name text,
  ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS parent_interface text,
  ADD COLUMN IF NOT EXISTS bridge_name text,
  ADD COLUMN IF NOT EXISTS vlan_id int,
  ADD COLUMN IF NOT EXISTS gateway_cidr inet,
  ADD COLUMN IF NOT EXISTS gateway_ip inet,
  ADD COLUMN IF NOT EXISTS subnet_cidr cidr,
  -- The THREE OPERATOR-FACING FACTS about a guest network: how addresses are handed out, whether the captive
  -- portal intercepts, and whether the network reaches the internet at all. Migration 0002 has carried them
  -- since edge networking shipped; this fixture did not, so any query that SELECTs them failed here with
  -- "column does not exist" and returned an empty result set -- which reads as "this site has no guest
  -- networks" rather than as a broken fixture. Added with 0002's own defaults and CHECK so the fixture keeps
  -- matching the shape a real appliance has, which is the stated purpose of the block above.
  ADD COLUMN IF NOT EXISTS dhcp_mode text NOT NULL DEFAULT 'local',
  ADD COLUMN IF NOT EXISTS captive_portal_enabled boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS internet_access_enabled boolean NOT NULL DEFAULT true,
  -- The dashboard's network card reads how each network resolves names (resources_overview.go). 0002's
  -- defaults and CHECK, for the same reason as the three above.
  ADD COLUMN IF NOT EXISTS dns_mode text NOT NULL DEFAULT 'appliance',
  ADD COLUMN IF NOT EXISTS dns_servers jsonb NOT NULL DEFAULT '[]'::jsonb,
  -- Untagged or VLAN, as 0002 declares it. The network pages and the staged-change code SELECT it.
  ADD COLUMN IF NOT EXISTS network_type text NOT NULL DEFAULT 'untagged';
-- The transactional apply record of network changes (0002), the same shape: the edged and netd integration tests
-- join and insert against it, and a JOIN against a table the fixture lacks errors and reads as an empty site.
CREATE TABLE IF NOT EXISTS public.network_config_revisions (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  seq            bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
  state          text NOT NULL DEFAULT 'draft'
                   CHECK (state IN ('draft','validated','applying','pending_confirmation',
                                    'active','failed','rolled_back','superseded')),
  summary        text,
  bundle_path    text,
  intent         jsonb NOT NULL DEFAULT '{}'::jsonb,
  validation     jsonb NOT NULL DEFAULT '{}'::jsonb,
  previous_seq   bigint,
  created_by     uuid,
  validated_by   uuid,
  applied_by     uuid,
  confirmed_by   uuid,
  created_at     timestamptz NOT NULL DEFAULT now(),
  validated_at   timestamptz,
  applied_at     timestamptz,
  confirmed_at   timestamptz,
  confirm_deadline timestamptz,
  failure_reason text
);
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'guest_networks_dhcp_mode_check') THEN
    ALTER TABLE public.guest_networks
      ADD CONSTRAINT guest_networks_dhcp_mode_check
      CHECK (dhcp_mode IN ('local','external','relay','disabled'));
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'guest_networks_dns_mode_check') THEN
    ALTER TABLE public.guest_networks
      ADD CONSTRAINT guest_networks_dns_mode_check CHECK (dns_mode IN ('appliance','custom'));
  END IF;
END $$;
-- seed one tenant/site/guest_network for tests (deterministic uuids)
INSERT INTO public.tenants(id) VALUES ('11111111-1111-1111-1111-111111111111') ON CONFLICT DO NOTHING;
INSERT INTO public.sites(id, tenant_id) VALUES
  ('22222222-2222-2222-2222-222222222222','11111111-1111-1111-1111-111111111111') ON CONFLICT DO NOTHING;
INSERT INTO public.guest_networks(id, tenant_id, site_id, name, parent_interface, bridge_name,
                                  gateway_cidr, gateway_ip, subnet_cidr, enabled) VALUES
  ('33333333-3333-3333-3333-333333333333','11111111-1111-1111-1111-111111111111','22222222-2222-2222-2222-222222222222',
   'fixture-guests','ens192','br-fixture','10.90.0.1/22','10.90.0.1','10.90.0.0/22',true)
  ON CONFLICT DO NOTHING;
-- Phase 6 anchors its per-appliance product settings and its audit actor to the REAL platform records, so a
-- settings row cannot name an appliance that does not exist and an audit row cannot name an operator the
-- server never authenticated. Those FKs need the platform tables to exist here too. Same shape as migration
-- 0001/0005 create on a real appliance, reduced to the columns the FKs and the gate actually use.
-- THE ADDRESS RANGES A GUEST NETWORK HANDS OUT.
--
-- Migration 0002 creates this alongside guest_networks and every real appliance has it; the fixture did not, so
-- any query joining it failed with "relation does not exist" and returned an EMPTY RESULT SET rather than an
-- error the caller could see. That is the same trap the missing guest_networks columns set: a reader concludes
-- "this site has no guest networks" when the truth is that the fixture could not answer the question.
--
-- Same shape and same constraint as 0002, minus the index, which nothing here measures.
CREATE TABLE IF NOT EXISTS public.dhcp_pools (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  guest_network_id uuid NOT NULL REFERENCES public.guest_networks(id) ON DELETE CASCADE,
  start_ip         inet NOT NULL,
  end_ip           inet NOT NULL,
  sort_order       int NOT NULL DEFAULT 0,
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dhcp_pool_order CHECK (start_ip <= end_ip));

CREATE TABLE IF NOT EXISTS public.appliances (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL,
  site_id uuid NOT NULL,
  serial text UNIQUE,
  name text);
CREATE TABLE IF NOT EXISTS public.operators (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid,
  email text,
  name text);
-- The edged API integration suites seed operators the way the real appliance schema shapes them. The stub
-- carried only id/tenant/email/name, so every one of those suites failed at fixture time on a missing column
-- and could not run here at all -- which is not the same thing as passing. These columns are additive and
-- match the appliance's own operators table.
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS display_name text;
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS password_hash text;
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS status text DEFAULT 'active';
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS site_id uuid;
ALTER TABLE public.operators ADD COLUMN IF NOT EXISTS created_at timestamptz DEFAULT now();
-- The offline-activation single-use ledger, exactly as migration 0048 declares it. scd reads and stamps it
-- when it reconciles consumed offline packages with Central.
CREATE TABLE IF NOT EXISTS public.edge_offline_packages (
  package_id    uuid PRIMARY KEY,
  nonce         text UNIQUE,
  consumed_at   timestamptz NOT NULL DEFAULT now(),
  reconciled_at timestamptz);
-- The transient one-time-code challenge store, as the production baseline declares it (the key-generation FK is
-- not modelled: the fixture carries no OTP key ring). scd issues, verifies and (since 0110/0113/0114) binds,
-- escrows and records reveals against these rows; the integration suites insert and read them directly.
CREATE TABLE IF NOT EXISTS public.auth_otps (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id          uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
  appliance_id       uuid,
  channel            text NOT NULL CHECK (channel IN ('email','sms','whatsapp')),
  destination        text NOT NULL,
  code_hash          text NOT NULL,
  issued_at          timestamptz NOT NULL DEFAULT now(),
  expires_at         timestamptz NOT NULL,
  attempts           integer NOT NULL DEFAULT 0,
  max_attempts       integer NOT NULL DEFAULT 5,
  consumed_at        timestamptz,
  ip                 inet,
  user_agent         text,
  otp_key_generation integer);
-- The notification senders, as the production baseline declares them, with the 'smtp' kind 0107 adds.
CREATE TABLE IF NOT EXISTS public.notification_providers (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL,
  channel         text NOT NULL CHECK (channel IN ('email','sms','whatsapp')),
  kind            text NOT NULL CHECK (kind IN ('stub','sendgrid','ses','twilio','meta_whatsapp','twilio_whatsapp','smtp')),
  enabled         boolean NOT NULL DEFAULT true,
  display_name    text,
  api_key         text,
  api_user        text,
  from_address    text,
  from_name       text,
  region          text,
  extra           jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_success_at timestamptz,
  last_error      text,
  last_error_at   timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now());
-- Social sign-in providers and attempts, as the production baseline declares them after 0116 (callback hosts,
-- nullable secret and redirect URI, PKCE verifier and nonce on the attempt).
CREATE TABLE IF NOT EXISTS public.social_oauth_providers (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id       uuid NOT NULL,
  provider        text NOT NULL CHECK (provider IN ('google','apple','facebook','microsoft')),
  enabled         boolean NOT NULL DEFAULT true,
  display_name    text,
  client_id       text NOT NULL,
  client_secret   text,
  redirect_uri    text,
  scopes          text,
  extra           jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_success_at timestamptz,
  last_error      text,
  last_error_at   timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  callback_hosts  text[] NOT NULL DEFAULT '{}');
CREATE TABLE IF NOT EXISTS public.social_oauth_states (
  state         text PRIMARY KEY,
  tenant_id     uuid NOT NULL,
  appliance_id  uuid,
  provider      text NOT NULL,
  client_ip     inet,
  client_mac    macaddr,
  redirect_uri  text NOT NULL,
  user_agent    text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL,
  consumed_at   timestamptz,
  code_verifier text,
  nonce         text);
-- Admin Console sign-in protection and throttle (migration 0119), copied verbatim so the edged sign-in
-- integration suites find the tables whichever schema they run against.
CREATE TABLE IF NOT EXISTS public.operator_signin_protection (
  id                     boolean PRIMARY KEY DEFAULT true CONSTRAINT osp_singleton CHECK (id),
  account_max_failures   integer NOT NULL DEFAULT 5
    CONSTRAINT osp_account_max CHECK (account_max_failures BETWEEN 3 AND 20),
  account_window_seconds integer NOT NULL DEFAULT 900
    CONSTRAINT osp_account_window CHECK (account_window_seconds BETWEEN 60 AND 86400),
  ip_max_failures        integer NOT NULL DEFAULT 20
    CONSTRAINT osp_ip_max CHECK (ip_max_failures BETWEEN 5 AND 500),
  ip_window_seconds      integer NOT NULL DEFAULT 900
    CONSTRAINT osp_ip_window CHECK (ip_window_seconds BETWEEN 60 AND 86400),
  lockout_seconds        integer NOT NULL DEFAULT 900
    CONSTRAINT osp_lockout CHECK (lockout_seconds BETWEEN 60 AND 86400),
  config_version         bigint  NOT NULL DEFAULT 1 CONSTRAINT osp_version CHECK (config_version >= 1),
  updated_at             timestamptz NOT NULL DEFAULT now(),
  updated_by             text
);
CREATE TABLE IF NOT EXISTS public.operator_signin_protection_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  changed_by         text NOT NULL CONSTRAINT ospc_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text NOT NULL CONSTRAINT ospc_reason CHECK (length(btrim(change_reason)) > 0),
  old_values         jsonb,
  new_values         jsonb NOT NULL,
  new_config_version bigint NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS public.operator_signin_throttle (
  scope             text NOT NULL CONSTRAINT ost_scope CHECK (scope IN ('account','ip','reauth')),
  key_hash          text NOT NULL CONSTRAINT ost_key_hex CHECK (key_hash ~ '^[0-9a-f]{64}$'),
  failures          integer NOT NULL DEFAULT 0 CONSTRAINT ost_failures CHECK (failures >= 0),
  window_started_at timestamptz NOT NULL DEFAULT now(),
  last_failure_at   timestamptz NOT NULL DEFAULT now(),
  locked_until      timestamptz,
  PRIMARY KEY (scope, key_hash)
);
-- Tables the migrations after the original fixture created and integration tests or queries reference (fixture parity, tools/check-fixture-parity.py).
-- Each is the migration's own definition, copied unchanged, so a query that selects one of their columns finds it here exactly as on an appliance.
CREATE TABLE IF NOT EXISTS public.backup_key_holders (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL,
  site_id           uuid NOT NULL,
  label             text NOT NULL CONSTRAINT bkh_label CHECK (length(btrim(label)) BETWEEN 1 AND 80),
  public_key        text NOT NULL CONSTRAINT bkh_public_key CHECK (public_key ~ '^age1[02-9ac-hj-np-z]{58}$'),
  status            text NOT NULL DEFAULT 'PENDING' CONSTRAINT bkh_status CHECK (status IN ('PENDING', 'ACTIVE', 'REMOVED')),
  check_code_hash   text CONSTRAINT bkh_check CHECK (check_code_hash IS NULL OR check_code_hash ~ '^[0-9a-f]{64}$'),
  loss_acknowledged boolean NOT NULL CONSTRAINT bkh_loss_ack CHECK (loss_acknowledged),
  created_at        timestamptz NOT NULL DEFAULT now(),
  created_by        text NOT NULL CONSTRAINT bkh_created_by CHECK (length(btrim(created_by)) BETWEEN 1 AND 200),
  confirmed_at      timestamptz,
  removed_at        timestamptz,
  removed_by        text,
  remove_reason     text,
  last_tested_at    timestamptz,
  CONSTRAINT bkh_active_confirmed CHECK (status = 'PENDING' OR confirmed_at IS NOT NULL OR status = 'REMOVED'),
  CONSTRAINT bkh_removed CHECK ((status = 'REMOVED') = (removed_at IS NOT NULL))
);
CREATE TABLE IF NOT EXISTS public.backup_key_holder_events (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id  uuid NOT NULL,
  site_id    uuid NOT NULL,
  holder_id  uuid NOT NULL REFERENCES public.backup_key_holders (id),
  event      text NOT NULL CONSTRAINT bkhe_event CHECK (event IN ('added', 'confirmed', 'removed', 'restore_tested')),
  actor      text NOT NULL CONSTRAINT bkhe_actor CHECK (length(btrim(actor)) BETWEEN 1 AND 200),
  reason     text,
  at         timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS public.ledger_destination (
  singleton       boolean PRIMARY KEY DEFAULT true CONSTRAINT ledger_dest_one CHECK (singleton),
  kind            text NOT NULL CONSTRAINT ledger_dest_kind CHECK (kind IN ('dir', 'sftp', 's3')),
  config          jsonb NOT NULL,
  secret_ct       bytea,
  secret_nonce    bytea,
  secret_key_id   text,
  secret_ver      integer,
  configured_by   text NOT NULL,
  configured_at   timestamptz NOT NULL DEFAULT now(),
  verified_at     timestamptz,
  last_ok_at      timestamptz,
  last_error      text,
  last_error_at   timestamptz
);
CREATE TABLE IF NOT EXISTS public.operator_mfa (
  operator_id    uuid PRIMARY KEY REFERENCES public.operators (id) ON DELETE CASCADE,
  key_id         text NOT NULL CONSTRAINT om_key_id CHECK (length(key_id) BETWEEN 1 AND 64),
  nonce          bytea NOT NULL,
  ciphertext     bytea NOT NULL,
  cipher_version integer NOT NULL CONSTRAINT om_cipher CHECK (cipher_version = 1),
  created_at     timestamptz NOT NULL DEFAULT now(),
  confirmed_at   timestamptz,
  last_used_step bigint
);
CREATE TABLE IF NOT EXISTS public.operator_mfa_recovery_codes (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  operator_id uuid NOT NULL REFERENCES public.operators (id) ON DELETE CASCADE,
  code_mac    text NOT NULL CONSTRAINT omrc_mac CHECK (code_mac ~ '^[0-9a-f]{64}$'),
  created_at  timestamptz NOT NULL DEFAULT now(),
  used_at     timestamptz,
  CONSTRAINT omrc_unique UNIQUE (operator_id, code_mac)
);
CREATE TABLE IF NOT EXISTS public.operator_session_policy_changes (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  changed_by         text NOT NULL CONSTRAINT osesspc_actor CHECK (length(btrim(changed_by)) > 0),
  change_reason      text NOT NULL CONSTRAINT osesspc_reason CHECK (length(btrim(change_reason)) > 0),
  old_values         jsonb,
  new_values         jsonb NOT NULL,
  new_config_version bigint NOT NULL,
  changed_at         timestamptz NOT NULL DEFAULT now()
);
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
CREATE TABLE IF NOT EXISTS public.system_network_audit (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor             TEXT        NOT NULL,          -- operator email/id (or 'system')
    actor_id          UUID,                          -- operator id when known
    source_ip         TEXT,                          -- client IP that requested it
    action            TEXT        NOT NULL CHECK (action IN ('validate','apply','confirm','rollback','auto_rollback')),
    target            TEXT        NOT NULL CHECK (target IN ('wan','lan','both')),
    previous_config   JSONB,
    requested_config  JSONB,
    validation_result JSONB,
    apply_result      TEXT,
    confirm_result    TEXT,
    rollback_result   TEXT,
    failure_reason    TEXT,
    backup_path       TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO public.appliances(id, tenant_id, site_id, serial, name) VALUES
  ('44444444-4444-4444-4444-444444444444','11111111-1111-1111-1111-111111111111',
   '22222222-2222-2222-2222-222222222222','APP-FIXTURE-0001','fixture-appliance')
  ON CONFLICT DO NOTHING;
INSERT INTO public.operators(id, tenant_id, email, name) VALUES
  ('55555555-5555-5555-5555-555555555555','11111111-1111-1111-1111-111111111111',
   'fixture-operator@example.invalid','Fixture Operator')
  ON CONFLICT DO NOTHING;
COMMIT;
