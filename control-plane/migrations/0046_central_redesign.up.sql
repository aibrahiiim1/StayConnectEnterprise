-- CENTRAL REDESIGN: LICENSING, ACTIVATION AND FLEET STATUS ONLY (docs/CENTRAL_CONTROL_PLANE.md).
--
-- 1. The appliance lifecycle is reduced to IDENTITY: pending_approval, assigned, revoked, decommissioned.
--    Licence state (licensed/grace/license_expired/suspended) and presence (online/offline) are no longer
--    stored on the appliance; ctrlapi derives activation, connection and licence state on every read.
--    Suspending or revoking a licence therefore never cuts an appliance off from Central.
-- 2. appliances.status (a second, older presence/lifecycle column) and appliances.environment (never read)
--    are dropped. appliances.activated_at records when an operator activated the appliance.
-- 3. The empty legacy guest/commerce/SSO/enrollment-token tables are DROPPED. The guard below refuses to run
--    if any of them holds a row, so this migration can never destroy data: on the live Central every one of
--    them was verified empty on 2026-09-27.
-- 4. The commercial history that DOES hold rows (plans, plan_limits, plan_limit_history,
--    subscription_events) is MOVED, not dropped, into schema legacy_archive. No code reads it any more.
--
-- LOSSY ONLY IN ONE PLACE, AND ON PURPOSE: the down migration restores every column and table, but it cannot
-- restore the old fine-grained lifecycle values (licensed, grace, online, offline, suspended, ...). Those are
-- mapped as follows and come back from the down migration as pending_approval / assigned:
--
--   manufactured, installed_unenrolled, pending_enrollment, claimed, pending_approval  -> pending_approval
--   assigned, licensed, grace, online, offline, license_expired, activated, suspended  -> assigned
--                                         (only when tenant_id AND site_id are set; otherwise pending_approval)
--   revoked, decommissioned                                                           -> unchanged
--   any row whose old status column said 'retired'                                    -> decommissioned
--                                         (unless already revoked), because the old appliance authenticator
--                                         treated status='retired' as terminal and so must the new one.
--
-- The migration ledger row is written by deploy/scripts/central-migrate.sh, not here.

BEGIN;

-- ---------------------------------------------------------------------------------------------------------
-- 0. THE GUARD. Nothing below runs if a table about to be dropped holds even one row.
-- ---------------------------------------------------------------------------------------------------------
DO $$
DECLARE
    t text;
    n bigint;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'accounting_records', 'guests', 'sessions', 'vouchers', 'voucher_batches', 'ticket_templates',
        'auth_otps', 'social_oauth_states', 'social_oauth_providers', 'idp_providers', 'auth_oidc_states',
        'pms_attempts', 'pms_providers', 'notification_providers', 'stripe_accounts', 'payments',
        'stripe_events', 'invoices', 'invoice_lines', 'walled_garden_rules', 'networks',
        'tenant_subscriptions', 'tenant_limit_overrides', 'appliance_bootstrap_tokens'
    ] LOOP
        IF to_regclass('public.' || t) IS NOT NULL THEN
            EXECUTE format('SELECT count(*) FROM public.%I', t) INTO n;
            IF n > 0 THEN
                RAISE EXCEPTION '0046_central_redesign refuses to run: public.% holds % row(s). It would be dropped. Archive or remove that data deliberately first.', t, n;
            END IF;
        END IF;
    END LOOP;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- 1. Appliance lifecycle -> identity only.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_lifecycle_state_check;

UPDATE appliances SET lifecycle_state = 'decommissioned'
 WHERE status = 'retired' AND lifecycle_state NOT IN ('revoked', 'decommissioned');

UPDATE appliances SET lifecycle_state = CASE
        WHEN lifecycle_state IN ('revoked', 'decommissioned') THEN lifecycle_state
        WHEN lifecycle_state IN ('assigned', 'licensed', 'grace', 'online', 'offline', 'license_expired',
                                 'activated', 'suspended')
             AND tenant_id IS NOT NULL AND site_id IS NOT NULL THEN 'assigned'
        ELSE 'pending_approval'
    END
 WHERE lifecycle_state NOT IN ('pending_approval', 'assigned', 'revoked', 'decommissioned')
    OR (lifecycle_state = 'assigned' AND (tenant_id IS NULL OR site_id IS NULL));

ALTER TABLE appliances ALTER COLUMN lifecycle_state SET DEFAULT 'pending_approval';
ALTER TABLE appliances ADD CONSTRAINT appliances_lifecycle_state_check
    CHECK (lifecycle_state IN ('pending_approval', 'assigned', 'revoked', 'decommissioned'));

ALTER TABLE appliances ADD COLUMN IF NOT EXISTS activated_at timestamptz;
UPDATE appliances a SET activated_at = COALESCE(
        (SELECT max(e.created_at) FROM appliance_lifecycle_events e
          WHERE e.appliance_id = a.id AND e.to_state IN ('assigned', 'activated', 'licensed')),
        (SELECT max(x.created_at) FROM appliance_assignments x WHERE x.appliance_id = a.id))
 WHERE a.lifecycle_state = 'assigned' AND a.activated_at IS NULL;

ALTER TABLE appliances DROP CONSTRAINT IF EXISTS appliances_status_check;
ALTER TABLE appliances DROP COLUMN IF EXISTS status;
ALTER TABLE appliances DROP COLUMN IF EXISTS environment;

-- ---------------------------------------------------------------------------------------------------------
-- 2. Views over the commercial model go first; they depend on tables moved or dropped below.
-- ---------------------------------------------------------------------------------------------------------
DROP VIEW IF EXISTS tenant_effective_limits;
DROP VIEW IF EXISTS commercial_plans;

-- ---------------------------------------------------------------------------------------------------------
-- 3. Drop the empty legacy tables. No CASCADE: an unexpected dependency aborts the migration instead of
--    silently taking something else with it. The one known cross-reference (archived subscription_events ->
--    tenant_subscriptions) is removed explicitly first; the down migration restores it.
-- ---------------------------------------------------------------------------------------------------------
ALTER TABLE subscription_events DROP CONSTRAINT IF EXISTS subscription_events_subscription_id_fkey;

DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS stripe_events;
DROP TABLE IF EXISTS stripe_accounts;
DROP TABLE IF EXISTS accounting_records;   -- Timescale hypertable; DROP TABLE removes its chunks too
DROP TABLE IF EXISTS sessions;             -- also removes the out-of-band legacy_ro guard trigger, if present
DROP TABLE IF EXISTS guests;
DROP TABLE IF EXISTS auth_otps;
DROP TABLE IF EXISTS social_oauth_states;
DROP TABLE IF EXISTS vouchers;
DROP TABLE IF EXISTS voucher_batches;
DROP TABLE IF EXISTS ticket_templates;
DROP TABLE IF EXISTS social_oauth_providers;
DROP TABLE IF EXISTS auth_oidc_states;
DROP TABLE IF EXISTS idp_providers;
DROP TABLE IF EXISTS pms_attempts;
DROP TABLE IF EXISTS pms_providers;
DROP TABLE IF EXISTS notification_providers;
DROP TABLE IF EXISTS invoice_lines;
DROP TABLE IF EXISTS invoices;
DROP TABLE IF EXISTS walled_garden_rules;
DROP TABLE IF EXISTS networks;
DROP TABLE IF EXISTS tenant_limit_overrides;
DROP TABLE IF EXISTS tenant_subscriptions;
DROP TABLE IF EXISTS appliance_bootstrap_tokens;

-- ---------------------------------------------------------------------------------------------------------
-- 4. Commercial history is kept, out of the way.
-- ---------------------------------------------------------------------------------------------------------
CREATE SCHEMA IF NOT EXISTS legacy_archive;
COMMENT ON SCHEMA legacy_archive IS
    'Commercial history from the retired plans/subscriptions model (migration 0046). Kept for the record; nothing reads it.';
ALTER TABLE IF EXISTS public.plans               SET SCHEMA legacy_archive;
ALTER TABLE IF EXISTS public.plan_limits         SET SCHEMA legacy_archive;
ALTER TABLE IF EXISTS public.plan_limit_history  SET SCHEMA legacy_archive;
ALTER TABLE IF EXISTS public.subscription_events SET SCHEMA legacy_archive;
-- An archive must not lose rows because a live customer is deleted later: the cascade from tenants goes.
ALTER TABLE legacy_archive.subscription_events DROP CONSTRAINT IF EXISTS subscription_events_tenant_id_fkey;

COMMIT;
