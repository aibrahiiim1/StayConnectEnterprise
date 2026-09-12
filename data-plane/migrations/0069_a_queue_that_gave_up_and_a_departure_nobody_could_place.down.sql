-- Reverse of 0069.
--
-- The three views, the two settings tables, the two append-only logs and the six functions go. No PMS event,
-- stay, entitlement or sync record is touched: this migration never owned any of those, and a rollback that
-- deleted queued telemetry or rewrote an event's processing state to undo a reporting change would be doing
-- harm to reverse a screen.
--
-- A re-offer already performed is NOT undone. The engine has since re-evaluated that event and may have
-- applied it through the Checkout Converter; reversing the audit row would delete the only record of why.

BEGIN;

DROP VIEW IF EXISTS iam_v2.pms_reconciliation_cases;
DROP VIEW IF EXISTS iam_v2.pms_stays_past_departure;
DROP VIEW IF EXISTS iam_v2.pms_rooms_multi_occupancy;

DROP FUNCTION IF EXISTS iam_v2.pms_reoffer_stay_event(uuid, uuid, uuid, uuid, text, text, jsonb);
DROP FUNCTION IF EXISTS iam_v2.cloud_sync_settings_set(uuid, uuid, integer, text, text);
DROP FUNCTION IF EXISTS iam_v2.cloud_sync_settings_get(uuid, uuid);
DROP FUNCTION IF EXISTS public.sync_outbox_accounting();
DROP FUNCTION IF EXISTS public.sync_outbox_prune_delivered(integer);
DROP FUNCTION IF EXISTS public.sync_outbox_recover_exhausted(text, text, integer);

DROP TRIGGER IF EXISTS stay_event_reoffers_no_update ON iam_v2.stay_event_reoffers;
DROP TRIGGER IF EXISTS cloud_sync_settings_changes_no_update ON iam_v2.cloud_sync_settings_changes;
DROP TRIGGER IF EXISTS sync_outbox_recovery_log_no_update ON public.sync_outbox_recovery_log;

DROP TABLE IF EXISTS iam_v2.stay_event_reoffers;
DROP TABLE IF EXISTS iam_v2.cloud_sync_settings_changes;
DROP TABLE IF EXISTS iam_v2.site_cloud_sync_settings;
DROP TABLE IF EXISTS public.sync_outbox_recovery_log;

DROP FUNCTION IF EXISTS iam_v2.stay_event_reoffers_append_only();
DROP FUNCTION IF EXISTS iam_v2.cloud_sync_settings_changes_append_only();
DROP FUNCTION IF EXISTS public.sync_outbox_recovery_log_append_only();

COMMIT;
