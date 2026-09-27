-- 0093 — THE APPLIANCE REPORTS TO NOBODY: THE CLOUD TELEMETRY SUBSYSTEM IS REMOVED.
--
-- Product-Owner decision (standing since 2026-09-13, CLAUDE.md §0E): Central serves this appliance for
-- licensing only. The cloud telemetry link was switched off by 0071 and has carried nothing since; Central no
-- longer has any consumer for it. The Product Owner has now directed that the dormant subsystem be removed
-- completely rather than kept switched off, and has AUTHORISED DELETING ITS DATA.
--
-- The code went first, in the same delivery: scd no longer has a NATS transport, a telemetry outbox, any
-- producer (usage, health, service_health, license_ack, security), outbox retention, a command channel, an
-- update agent or a cloud-mode setting; edged no longer has the cloud-sync settings / recovery surfaces or
-- the service-health producer. Nothing reads or writes any object below any more.
--
-- WHAT THIS DROPS
--   iam_v2.site_cloud_mode, iam_v2.cloud_mode_changes, iam_v2.cloud_mode_get/_set and its append-only trigger
--     function -- the licensing-only mode setting (0071). With no telemetry left, the mode has nothing to gate.
--   iam_v2.site_cloud_sync_settings, iam_v2.cloud_sync_settings_changes, iam_v2.cloud_sync_settings_get/_set
--     and its append-only trigger function -- the delivered-record retention setting (0069).
--   iam_v2.sync_outbox_recovery_log and its append-only trigger function, iam_v2.sync_outbox_recover_exhausted,
--     iam_v2.sync_outbox_prune_delivered, iam_v2.sync_outbox_accounting -- the queue's recovery, retention and
--     accounting operations (0069).
--   public.sync_outbox, public.sync_checkpoints -- the queue itself and its checkpoints (0001), WITH THEIR ROWS.
--   public.edge_executed_commands, public.edge_installed_updates -- the ledgers of the signed command channel
--     and the software-update agent (0048), WITH THEIR ROWS. Both writers are removed; the Product Owner
--     authorised deleting that data too.
--
-- WHAT IT DELIBERATELY KEEPS
--   Everything 0069 added for PMS departures (stay_event_reoffers, pms_reoffer_stay_event, the three views);
--   public.appliance_service_health (local health, read by Hotel Admin) and public.edge_offline_packages (the
--   offline-activation single-use ledger, which the offline reconcile still reads).
--
-- THE PUBLIC TABLES AND THE ROLE THAT APPLIES THIS. A live site applies migrations as iam_v2_owner, which
-- deliberately cannot drop public objects it does not own (these four belong to the account that created
-- them -- 0001, 0048, or scd at runtime before 0048). On that path they are skipped here with a NOTICE and removed by the one-line
-- owner statement in the deployment notes; everywhere the applying role can drop them (a factory-clean
-- reconstruction, a disposable database), they go here. Nothing depends on them either way: no code reads
-- them, and Gate-P no longer grants on them.

BEGIN;

-- ---- the cloud mode (0071) ---------------------------------------------------------------------------------
DROP FUNCTION IF EXISTS iam_v2.cloud_mode_set(uuid, uuid, text, text, text);
DROP FUNCTION IF EXISTS iam_v2.cloud_mode_get(uuid, uuid);
DROP TABLE IF EXISTS iam_v2.cloud_mode_changes;   -- its append-only trigger goes with it
DROP TABLE IF EXISTS iam_v2.site_cloud_mode;
DROP FUNCTION IF EXISTS iam_v2.cloud_mode_changes_append_only();

-- ---- the queue's settings, recovery, retention and accounting (0069, part one) ------------------------------
DROP FUNCTION IF EXISTS iam_v2.cloud_sync_settings_set(uuid, uuid, integer, text, text);
DROP FUNCTION IF EXISTS iam_v2.cloud_sync_settings_get(uuid, uuid);
DROP FUNCTION IF EXISTS iam_v2.sync_outbox_recover_exhausted(text, text, integer);
DROP FUNCTION IF EXISTS iam_v2.sync_outbox_prune_delivered(integer);
DROP FUNCTION IF EXISTS iam_v2.sync_outbox_accounting();
DROP TABLE IF EXISTS iam_v2.cloud_sync_settings_changes;
DROP TABLE IF EXISTS iam_v2.site_cloud_sync_settings;
DROP TABLE IF EXISTS iam_v2.sync_outbox_recovery_log;
DROP FUNCTION IF EXISTS iam_v2.cloud_sync_settings_changes_append_only();
DROP FUNCTION IF EXISTS iam_v2.sync_outbox_recovery_log_append_only();

-- ---- the queue and its checkpoints (0001); the command-channel and update-agent ledgers (0048) --------------
DO $public$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['public.sync_outbox', 'public.sync_checkpoints',
                           'public.edge_executed_commands', 'public.edge_installed_updates'] LOOP
    IF to_regclass(t) IS NULL THEN
      CONTINUE;
    END IF;
    IF pg_has_role(current_user, (SELECT relowner FROM pg_class WHERE oid = to_regclass(t)), 'USAGE') THEN
      EXECUTE format('DROP TABLE %s', t);
    ELSE
      RAISE NOTICE '0093: % is not owned by % and is left for its owner to drop (see the deployment notes)',
        t, current_user;
    END IF;
  END LOOP;
END $public$;

-- ASSERTED: nothing of the cloud-mode / cloud-sync surface survives in iam_v2.
DO $verify$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
              WHERE n.nspname = 'iam_v2'
                AND c.relname IN ('site_cloud_mode','cloud_mode_changes','site_cloud_sync_settings',
                                  'cloud_sync_settings_changes','sync_outbox_recovery_log')) THEN
    RAISE EXCEPTION '0093: a cloud telemetry table survived in iam_v2';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
              WHERE n.nspname = 'iam_v2'
                AND (p.proname LIKE 'cloud\_mode\_%' OR p.proname LIKE 'cloud\_sync\_%'
                     OR p.proname LIKE 'sync\_outbox\_%')) THEN
    RAISE EXCEPTION '0093: a cloud telemetry function survived in iam_v2';
  END IF;
END $verify$;

COMMIT;
