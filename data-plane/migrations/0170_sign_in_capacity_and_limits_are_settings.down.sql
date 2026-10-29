-- Down for 0170. Restores the 0136 gate and default (30). The settings and their change log are dropped: they hold
-- configuration only. The change log is audit of settings, so a rollback is a deliberate loss of that history and is
-- taken only after a database dump (the rollback runbook).
BEGIN;
DROP TRIGGER IF EXISTS notify_session_active ON iam_v2.sessions;
DROP TRIGGER IF EXISTS notify_settings_changed ON public.tenants;
DROP TRIGGER IF EXISTS notify_settings_changed ON iam_v2.site_signin_capacity_settings;

CREATE OR REPLACE FUNCTION iam_v2.guest_signin_network_policy_get(p_tenant uuid, p_site uuid)
  RETURNS TABLE (network_max_failures integer, network_window_seconds integer, network_pause_seconds integer,
                 config_version bigint, updated_at timestamptz, is_default boolean)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(s.network_max_failures, 30), COALESCE(s.network_window_seconds, 600),
         COALESCE(s.network_pause_seconds, 900), COALESCE(s.config_version, 1), s.updated_at, (s.tenant_id IS NULL)
    FROM (SELECT 1) one
    LEFT JOIN iam_v2.site_guest_signin_network_policy s ON s.tenant_id = p_tenant AND s.site_id = p_site;
$fn$;

CREATE OR REPLACE FUNCTION iam_v2.guest_signin_network_gate(p_tenant uuid, p_site uuid, p_network uuid, p_mac macaddr)
  RETURNS TABLE (paused boolean, pause_id uuid, remaining_seconds integer)
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT (p.id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM iam_v2.guest_signin_device_passes d
                                             WHERE d.pause_id = p.id AND d.device_mac = p_mac)),
         p.id,
         CASE WHEN p.id IS NOT NULL THEN GREATEST(1, CEIL(EXTRACT(EPOCH FROM (p.expires_at - now())))::int) END
    FROM (SELECT 1) one
    LEFT JOIN LATERAL (
      SELECT x.id, x.expires_at FROM iam_v2.guest_signin_network_pauses x
       WHERE x.tenant_id = p_tenant AND x.site_id = p_site AND x.guest_network_id = p_network
         AND x.ended_at IS NULL AND x.expires_at > now()
       ORDER BY x.expires_at DESC LIMIT 1) p ON true;
$fn$;

DROP FUNCTION IF EXISTS iam_v2.prune_idle_devices(uuid,uuid,integer);
DROP FUNCTION IF EXISTS iam_v2.notify_session_active();
DROP FUNCTION IF EXISTS iam_v2.notify_settings_changed();
DROP FUNCTION IF EXISTS iam_v2.signin_capacity_settings_set(uuid,uuid,jsonb,text,text);
DROP FUNCTION IF EXISTS iam_v2.signin_capacity_settings_get(uuid,uuid);
DROP TABLE IF EXISTS iam_v2.signin_capacity_settings_changes;
DROP FUNCTION IF EXISTS iam_v2.signin_capacity_settings_changes_append_only();
DROP TABLE IF EXISTS iam_v2.site_signin_capacity_settings;
COMMIT;
