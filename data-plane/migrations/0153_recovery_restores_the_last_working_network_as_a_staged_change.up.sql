-- 0153 — RECOVERY RESTORES THE LAST WORKING NETWORK AS A STAGED CHANGE.
--
-- Restore Last Working, Restore Factory Defaults and Full Client Networking Reset are staged exactly like an activation (0151): nothing in public.guest_networks
-- moves until the operator applies, and a failed apply, a rollback or a lapsed confirmation window puts it all back. So a staged change can now also carry
-- the settings to write at apply (target) and the settings it replaced (before), and may change settings without switching the network on or off (SETTINGS).
--
-- network_layer_snapshots records, at every confirmation, what each network was layered with: its portal profile, its sign-in narrowing and its PMS routing.
-- A restore compares the present with it, version by version, to say which layers are unchanged, which were put back and which can no longer be.
--
-- network_review_items holds what a restore asks staff to look at. For a network whose PMS routing changed or vanished, room sign-in is paused on THAT network
-- only (paused_pms) until the item is acknowledged. Nothing here stores guest data.

BEGIN;

ALTER TABLE iam_v2.guest_network_changes DROP CONSTRAINT IF EXISTS gnc_kind;
ALTER TABLE iam_v2.guest_network_changes ADD CONSTRAINT gnc_kind CHECK (kind IN ('ENABLE','DISABLE','SETTINGS'));
ALTER TABLE iam_v2.guest_network_changes ADD COLUMN IF NOT EXISTS target jsonb;
ALTER TABLE iam_v2.guest_network_changes ADD COLUMN IF NOT EXISTS before jsonb;

CREATE TABLE IF NOT EXISTS iam_v2.network_layer_snapshots (
  revision_id uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  site_id     uuid NOT NULL,
  taken_at    timestamptz NOT NULL DEFAULT now(),
  layers      jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS nls_site ON iam_v2.network_layer_snapshots (tenant_id, site_id, taken_at DESC);

CREATE TABLE IF NOT EXISTS iam_v2.network_review_items (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   uuid NOT NULL,
  site_id     uuid NOT NULL,
  network_id  uuid NOT NULL,
  layer       text NOT NULL,
  CONSTRAINT nri_layer CHECK (layer IN ('portal_profile','signin','pms_routing')),
  detail      text NOT NULL,
  paused_pms  boolean NOT NULL DEFAULT false,
  change_id   uuid,
  created_at  timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS nri_one_open ON iam_v2.network_review_items (network_id, layer) WHERE resolved_at IS NULL;

DO $own$
DECLARE t text;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    FOREACH t IN ARRAY ARRAY['network_layer_snapshots','network_review_items'] LOOP
      EXECUTE format('ALTER TABLE iam_v2.%I OWNER TO iam_v2_owner', t);
    END LOOP;
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON iam_v2.network_layer_snapshots, iam_v2.network_review_items TO svc_edged;
  END IF;
END;
$g$;

COMMIT;
