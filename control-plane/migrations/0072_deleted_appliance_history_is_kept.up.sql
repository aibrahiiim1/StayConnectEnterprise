-- 0072 -- A DELETED APPLIANCE'S CERTIFICATE AND LIFECYCLE HISTORY IS KEPT (AD-65, audit P3 L10)
--
-- Deleting an appliance (DELETE FROM appliances) cascaded away its lifecycle events, certificates, certificate
-- requests and assignment moves (certificate_events and portal_tls_events carry no foreign key and already
-- survive). The history is now copied here, in the same
-- transaction as the delete (internal/api/appliance_ops.go deleteAppliance), and the cascade then proceeds as before.
--
-- Why an archive and not RESTRICT plus a soft-delete flag: a soft delete would have to be filtered out of every
-- query on appliances (about 60, in licensing, registration and activation too), and appliances.serial is UNIQUE, so
-- a kept row would block the same serial from ever registering again. The archive keeps the history without
-- touching either. Nothing here references appliances: the archive must outlive the row.
--
-- Additive: one new table. History is anonymised and aged out by central_retention_apply like the live tables
-- (people's fields are cleared by retention for the live tables; this archive carries the rows as they were, so
-- deleted_at + the history period is its own retention horizon, applied by the same function in a later release).

BEGIN;

CREATE TABLE IF NOT EXISTS appliance_history_archive (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  appliance_id  uuid NOT NULL,
  serial        text,
  tenant_id     uuid,
  site_id       uuid,
  lifecycle_state text,
  deleted_at    timestamptz NOT NULL DEFAULT now(),
  deleted_by    text,
  lifecycle_events    jsonb NOT NULL DEFAULT '[]'::jsonb,
  certificates        jsonb NOT NULL DEFAULT '[]'::jsonb,
  certificate_requests jsonb NOT NULL DEFAULT '[]'::jsonb,
  assignment_moves    jsonb NOT NULL DEFAULT '[]'::jsonb
);
CREATE INDEX IF NOT EXISTS appliance_history_archive_appliance ON appliance_history_archive (appliance_id);
CREATE INDEX IF NOT EXISTS appliance_history_archive_serial ON appliance_history_archive (serial);
COMMENT ON TABLE appliance_history_archive IS
  'The lifecycle, certificate, assignment and portal-certificate history of an appliance at the moment it was deleted (0072, AD-65). No foreign key: it outlives the appliance row.';

COMMIT;
