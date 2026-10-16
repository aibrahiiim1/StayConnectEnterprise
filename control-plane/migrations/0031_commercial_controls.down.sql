-- REVERSES 0031_commercial_controls: the 'scheduled' subscription status, override windows, plan-limit
-- history and limits_version, and the 0004 definition of tenant_effective_limits.
--
-- The commercial tables were removed by 0046/0047; they exist again only after those downs ran. Each step is
-- skipped when its table is absent, so this down is safe in either order. Refused while a subscription is
-- 'scheduled' (the 0004 constraint cannot hold it). Plan-limit history is DROPPED.

BEGIN;

DO $$
BEGIN
  IF to_regclass('public.tenant_subscriptions') IS NOT NULL THEN
    IF EXISTS (SELECT 1 FROM tenant_subscriptions WHERE status = 'scheduled') THEN
      RAISE EXCEPTION '0031 down: a subscription is scheduled, which the 0004 constraint does not allow';
    END IF;
    EXECUTE 'DROP INDEX IF EXISTS tenant_subscriptions_one_active';
    EXECUTE $q$CREATE UNIQUE INDEX IF NOT EXISTS tenant_subscriptions_one_active ON tenant_subscriptions(tenant_id)
             WHERE status IN ('trialing','active','past_due','paused')$q$;
    EXECUTE 'ALTER TABLE tenant_subscriptions DROP CONSTRAINT IF EXISTS tenant_subscriptions_status_check';
    EXECUTE $q$ALTER TABLE tenant_subscriptions ADD CONSTRAINT tenant_subscriptions_status_check
             CHECK (status IN ('trialing','active','past_due','canceled','paused'))$q$;
    EXECUTE 'ALTER TABLE tenant_subscriptions DROP COLUMN IF EXISTS auto_renew';
  END IF;
  IF to_regclass('public.plans') IS NOT NULL THEN
    EXECUTE 'ALTER TABLE plans DROP COLUMN IF EXISTS limits_version';
  END IF;
  EXECUTE 'DROP TABLE IF EXISTS plan_limit_history';
  IF to_regclass('public.tenant_limit_overrides') IS NOT NULL AND to_regclass('public.plan_limits') IS NOT NULL
     AND to_regclass('public.tenant_subscriptions') IS NOT NULL THEN
    EXECUTE 'DROP VIEW IF EXISTS tenant_effective_limits';
    EXECUTE 'ALTER TABLE tenant_limit_overrides DROP COLUMN IF EXISTS starts_at';
    EXECUTE $q$CREATE VIEW tenant_effective_limits AS
      WITH sub AS (
          SELECT ts.tenant_id, ts.plan_id FROM tenant_subscriptions ts
           WHERE ts.status IN ('trialing','active','past_due','paused')),
      plan_vals AS (
          SELECT s.tenant_id, pl.key, pl.value_type, pl.int_value, pl.bool_value, pl.str_value, pl.unit,
                 'plan'::text AS source
            FROM sub s JOIN plan_limits pl ON pl.plan_id = s.plan_id),
      ovr_vals AS (
          SELECT o.tenant_id, o.key, o.value_type, o.int_value, o.bool_value, o.str_value, NULL::text AS unit,
                 'override'::text AS source
            FROM tenant_limit_overrides o
           WHERE o.expires_at IS NULL OR o.expires_at > now()),
      merged AS (
          SELECT * FROM ovr_vals
          UNION ALL
          SELECT pv.* FROM plan_vals pv
            LEFT JOIN ovr_vals ov ON ov.tenant_id = pv.tenant_id AND ov.key = pv.key
           WHERE ov.tenant_id IS NULL)
      SELECT * FROM merged$q$;
  END IF;
END $$;

COMMIT;
