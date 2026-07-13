ALTER TABLE appliances DROP COLUMN IF EXISTS replacement_deadline;
DELETE FROM schema_migrations WHERE version = '0039_replacement_window';
