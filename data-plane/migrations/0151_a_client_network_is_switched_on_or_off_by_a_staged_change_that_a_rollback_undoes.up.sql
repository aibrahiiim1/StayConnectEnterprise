-- 0151 — A CLIENT NETWORK IS SWITCHED ON OR OFF BY A STAGED CHANGE THAT A ROLLBACK UNDOES.
--
-- Activating a prepared network, turning a port into a trunk, and the recovery operations all end the same way: some client networks become enabled and some
-- become disabled, TOGETHER, or not at all. Writing public.guest_networks.enabled directly cannot give that: if the apply fails, is rolled back or its
-- confirmation window lapses, the database would still say "enabled" for a network the appliance is not running.
--
-- So the request is recorded HERE, as an intent, and nothing in public.guest_networks moves until the operator APPLIES the configuration, exactly as a
-- topology replacement is staged (0105):
--
--   PENDING    asked for; nothing has changed anywhere. It can be cancelled.
--   APPLIED    the apply materialised it: the row's enabled flag was set (the flag it had is recorded in prior_enabled), inside the confirmation window.
--   CONFIRMED  the operator kept it.
--   REVERTED   the apply failed, was rolled back or timed out: the flag is back to prior_enabled and the request is PENDING again (or closed).
--   CANCELLED  dropped before it was ever applied.
--
-- group_id ties the changes one operation staged together (a port conversion, a factory activation, a reset), so they are listed, cancelled and reported as one.
-- No free text and no operator identity is stored here: the audit log carries who asked and why.
--
-- WHY HERE AND NOT A COLUMN ON public.guest_networks: live-site migrations apply as iam_v2_owner, which owns nothing in public (see 0099, 0105).

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.guest_network_changes (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL,
  site_id       uuid NOT NULL,
  -- No foreign key to public.guest_networks (iam_v2_owner cannot reference a table it does not own).
  network_id    uuid NOT NULL,
  kind          text NOT NULL,
  CONSTRAINT gnc_kind CHECK (kind IN ('ENABLE','DISABLE')),
  state         text NOT NULL DEFAULT 'PENDING',
  CONSTRAINT gnc_state CHECK (state IN ('PENDING','APPLIED','CONFIRMED','CANCELLED','REVERTED')),
  -- The enabled flag the row had when the apply materialised this change; a revert restores it.
  prior_enabled boolean,
  CONSTRAINT gnc_prior CHECK (state <> 'APPLIED' OR prior_enabled IS NOT NULL),
  group_id      uuid NOT NULL,
  group_kind    text NOT NULL,
  CONSTRAINT gnc_group_kind CHECK (group_kind IN ('ACTIVATE','FACTORY_ACTIVATE','PORT_MODE','RESTORE','FACTORY_RESET','FULL_RESET')),
  -- The netd revision the apply created, so a reconcile can ask what became of it.
  revision_id   uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  applied_at    timestamptz,
  settled_at    timestamptz
);

-- ONE LIVE CHANGE PER NETWORK: two staged changes to one network's flag would contradict each other.
CREATE UNIQUE INDEX IF NOT EXISTS gnc_one_live_per_network
  ON iam_v2.guest_network_changes (network_id) WHERE state IN ('PENDING','APPLIED');
CREATE INDEX IF NOT EXISTS gnc_site_state ON iam_v2.guest_network_changes (tenant_id, site_id, state, created_at DESC);
CREATE INDEX IF NOT EXISTS gnc_group ON iam_v2.guest_network_changes (group_id);

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.guest_network_changes OWNER TO iam_v2_owner';
  END IF;
END $own$;

-- Least privilege, mirrored in deploy/gatep/svc-edged-phase345-admin-grants.sql. edged is the only service that stages, materialises and settles a change.
DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON iam_v2.guest_network_changes TO svc_edged;
  END IF;
END;
$g$;

COMMIT;
