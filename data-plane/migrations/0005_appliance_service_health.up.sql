-- 0005_appliance_service_health
--
-- Authoritative LOCAL service-health model for the appliance health supervisor
-- (runs inside edged). A service may be systemd-"active" yet unhealthy, so this
-- is derived from real service-specific health checks + systemd state + the
-- adaptive-backoff tracker, NOT merely from "is-active". State survives an edged
-- restart and an appliance reboot (operationally appropriate: an operator must
-- still see the last known failure/recovery after a reboot).

BEGIN;

-- One row per critical service, upserted by the supervisor each poll.
CREATE TABLE IF NOT EXISTS appliance_service_health (
  service              text PRIMARY KEY,
  -- healthy | degraded | recovering | crash_loop | failed | starting | unknown
  state                text NOT NULL DEFAULT 'unknown',
  process_state        text,                       -- systemd ActiveState/SubState
  health_ok            boolean,                    -- last service-specific check result
  health_detail        text,                       -- sanitized check detail
  consecutive_failures int  NOT NULL DEFAULT 0,
  restart_count        bigint NOT NULL DEFAULT 0,  -- systemd NRestarts (lifetime)
  restarts_in_window   int  NOT NULL DEFAULT 0,    -- from the backoff sliding window
  restart_window_secs  int  NOT NULL DEFAULT 0,
  backoff_level        int  NOT NULL DEFAULT 0,
  backoff_ms           bigint NOT NULL DEFAULT 0,
  next_retry_at        timestamptz,
  first_failure_at     timestamptz,
  last_failure_at      timestamptz,
  last_failure_reason  text,
  last_exit_code       int,
  last_exit_signal     text,
  last_healthy_at      timestamptz,
  last_recovery_at     timestamptz,
  time_since_healthy_s bigint,                     -- seconds since last_healthy_at (snapshot)
  degraded_dependency  text,                       -- dependency causing degradation, if any
  critical             boolean NOT NULL DEFAULT true,
  updated_at           timestamptz NOT NULL DEFAULT now()
);

-- Append-only recovery/operational history (bounded retention, trimmed by the
-- supervisor). Records failures, recovery progression and manual actions.
CREATE TABLE IF NOT EXISTS appliance_recovery_events (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  service       text NOT NULL,
  -- failure_detected | recovering | recovered | crash_loop | degraded |
  -- manual_restart | manual_recheck | boot_converged | boot_not_converged
  event         text NOT NULL,
  cause         text,
  action        text,
  backoff_level int,
  result        text,
  duration_ms   bigint,
  actor         text NOT NULL DEFAULT 'system',    -- operator id when manual
  detail        jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS recovery_events_service_idx ON appliance_recovery_events (service, created_at DESC);
CREATE INDEX IF NOT EXISTS recovery_events_created_idx ON appliance_recovery_events (created_at DESC);

-- Singleton boot-convergence tracker: after boot, did all required services
-- become healthy within the threshold? Survives edged restarts so the supervisor
-- can resolve/raise the convergence alert idempotently.
CREATE TABLE IF NOT EXISTS appliance_boot_convergence (
  id                 boolean PRIMARY KEY DEFAULT true CHECK (id),
  boot_id            text,
  boot_at            timestamptz,
  deadline_at        timestamptz,
  converged          boolean NOT NULL DEFAULT false,
  converged_at       timestamptz,
  required_services  text[] NOT NULL DEFAULT '{}',
  pending_services   text[] NOT NULL DEFAULT '{}',
  alert_open         boolean NOT NULL DEFAULT false,
  updated_at         timestamptz NOT NULL DEFAULT now()
);

INSERT INTO schema_migrations(version) VALUES ('0005_appliance_service_health')
ON CONFLICT DO NOTHING;

COMMIT;
