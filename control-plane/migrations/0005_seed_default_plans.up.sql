-- Seed default plans: starter / pro / enterprise (monthly & yearly variants).
-- Limit keys are documented in docs/limits.md.

WITH ins_plans AS (
    INSERT INTO plans (code, name, description, billing_cycle, price_cents, currency, trial_days, sort_order)
    VALUES
        ('starter-monthly','Starter (Monthly)','1 site, up to 50 concurrent devices.','monthly',  4900,'USD',14, 10),
        ('starter-yearly', 'Starter (Yearly)', '1 site, up to 50 concurrent devices.','yearly',  49000,'USD',14, 11),
        ('pro-monthly',    'Pro (Monthly)',    '5 sites, 500 devices, SSO, PMS integration.','monthly',19900,'USD',14, 20),
        ('pro-yearly',     'Pro (Yearly)',     '5 sites, 500 devices, SSO, PMS integration.','yearly', 199000,'USD',14,21),
        ('enterprise-monthly','Enterprise (Monthly)','Unlimited sites, HA, white-label, SLA.','monthly', 79900,'USD',30, 30),
        ('enterprise-yearly', 'Enterprise (Yearly)', 'Unlimited sites, HA, white-label, SLA.','yearly',  799000,'USD',30, 31)
    ON CONFLICT (code) DO NOTHING
    RETURNING id, code
),
existing AS (
    SELECT id, code FROM plans WHERE code IN (
        'starter-monthly','starter-yearly','pro-monthly','pro-yearly','enterprise-monthly','enterprise-yearly'
    )
),
plan_ids AS (
    SELECT id, code FROM ins_plans
    UNION
    SELECT id, code FROM existing
)
INSERT INTO plan_limits (plan_id, key, value_type, int_value, bool_value, unit)
SELECT id, key, value_type, int_value, bool_value, unit
FROM plan_ids p
CROSS JOIN LATERAL (
    VALUES
        -- STARTER
        ('starter',  'max_sites',                   'int',  1::bigint,     NULL::boolean, 'sites'),
        ('starter',  'max_appliances',              'int',  1,             NULL, 'appliances'),
        ('starter',  'max_concurrent_devices',      'int',  50,            NULL, 'devices'),
        ('starter',  'max_monthly_active_devices',  'int',  500,           NULL, 'devices'),
        ('starter',  'max_vouchers_per_month',      'int',  2000,          NULL, 'vouchers'),
        ('starter',  'max_operators',               'int',  3,             NULL, 'operators'),
        ('starter',  'max_bandwidth_mbps_per_site', 'int',  100,           NULL, 'mbps'),
        ('starter',  'max_bandwidth_gb_per_month',  'int',  500,           NULL, 'gb'),
        ('starter',  'max_ssids_per_site',          'int',  2,             NULL, 'ssids'),
        ('starter',  'retention_days_accounting',   'int',  30,            NULL, 'days'),
        ('starter',  'retention_days_audit',        'int',  90,            NULL, 'days'),
        ('starter',  'api_rate_limit_rpm',          'int',  60,            NULL, 'rpm'),
        ('starter',  'feature.sso_saml',            'bool', NULL,          false, NULL),
        ('starter',  'feature.pms_integration',     'bool', NULL,          false, NULL),
        ('starter',  'feature.ha_pair',             'bool', NULL,          false, NULL),
        ('starter',  'feature.api_access',          'bool', NULL,          true,  NULL),
        ('starter',  'feature.white_label',         'bool', NULL,          false, NULL),

        -- PRO
        ('pro',      'max_sites',                   'int',  5,             NULL, 'sites'),
        ('pro',      'max_appliances',              'int',  10,            NULL, 'appliances'),
        ('pro',      'max_concurrent_devices',      'int',  500,           NULL, 'devices'),
        ('pro',      'max_monthly_active_devices',  'int',  10000,         NULL, 'devices'),
        ('pro',      'max_vouchers_per_month',      'int',  50000,         NULL, 'vouchers'),
        ('pro',      'max_operators',               'int',  15,            NULL, 'operators'),
        ('pro',      'max_bandwidth_mbps_per_site', 'int',  500,           NULL, 'mbps'),
        ('pro',      'max_bandwidth_gb_per_month',  'int',  10000,         NULL, 'gb'),
        ('pro',      'max_ssids_per_site',          'int',  4,             NULL, 'ssids'),
        ('pro',      'retention_days_accounting',   'int',  180,           NULL, 'days'),
        ('pro',      'retention_days_audit',        'int',  365,           NULL, 'days'),
        ('pro',      'api_rate_limit_rpm',          'int',  600,           NULL, 'rpm'),
        ('pro',      'feature.sso_saml',            'bool', NULL,          true,  NULL),
        ('pro',      'feature.pms_integration',     'bool', NULL,          true,  NULL),
        ('pro',      'feature.ha_pair',             'bool', NULL,          false, NULL),
        ('pro',      'feature.api_access',          'bool', NULL,          true,  NULL),
        ('pro',      'feature.white_label',         'bool', NULL,          false, NULL),

        -- ENTERPRISE
        ('enterprise','max_sites',                   'int',  -1,           NULL, 'sites'),        -- -1 = unlimited
        ('enterprise','max_appliances',              'int',  -1,           NULL, 'appliances'),
        ('enterprise','max_concurrent_devices',      'int',  -1,           NULL, 'devices'),
        ('enterprise','max_monthly_active_devices',  'int',  -1,           NULL, 'devices'),
        ('enterprise','max_vouchers_per_month',      'int',  -1,           NULL, 'vouchers'),
        ('enterprise','max_operators',               'int',  -1,           NULL, 'operators'),
        ('enterprise','max_bandwidth_mbps_per_site', 'int',  -1,           NULL, 'mbps'),
        ('enterprise','max_bandwidth_gb_per_month',  'int',  -1,           NULL, 'gb'),
        ('enterprise','max_ssids_per_site',          'int',  8,            NULL, 'ssids'),
        ('enterprise','retention_days_accounting',   'int',  730,          NULL, 'days'),
        ('enterprise','retention_days_audit',        'int',  2555,         NULL, 'days'),         -- 7 years
        ('enterprise','api_rate_limit_rpm',          'int',  6000,         NULL, 'rpm'),
        ('enterprise','feature.sso_saml',            'bool', NULL,         true,  NULL),
        ('enterprise','feature.pms_integration',     'bool', NULL,         true,  NULL),
        ('enterprise','feature.ha_pair',             'bool', NULL,         true,  NULL),
        ('enterprise','feature.api_access',          'bool', NULL,         true,  NULL),
        ('enterprise','feature.white_label',         'bool', NULL,         true,  NULL)
) AS t(tier, key, value_type, int_value, bool_value, unit)
WHERE p.code LIKE t.tier || '-%'
ON CONFLICT (plan_id, key) DO NOTHING;

INSERT INTO schema_migrations(version) VALUES ('0005_seed_default_plans') ON CONFLICT DO NOTHING;
