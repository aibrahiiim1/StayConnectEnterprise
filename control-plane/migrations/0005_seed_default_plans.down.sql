DELETE FROM plans WHERE code IN (
    'starter-monthly','starter-yearly',
    'pro-monthly','pro-yearly',
    'enterprise-monthly','enterprise-yearly'
);
DELETE FROM schema_migrations WHERE version = '0005_seed_default_plans';
