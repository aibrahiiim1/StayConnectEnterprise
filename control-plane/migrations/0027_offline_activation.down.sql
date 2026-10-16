-- REVERSES 0027_offline_activation. Drops the record of every offline activation package ever issued.

BEGIN;
DROP TABLE IF EXISTS offline_activation_packages;
COMMIT;
