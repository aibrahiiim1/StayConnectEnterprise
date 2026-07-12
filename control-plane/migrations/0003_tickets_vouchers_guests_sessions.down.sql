DROP TABLE IF EXISTS audit_log CASCADE;
DROP TABLE IF EXISTS accounting_records CASCADE;
DROP TABLE IF EXISTS sessions CASCADE;
DROP TABLE IF EXISTS guests CASCADE;
DROP TABLE IF EXISTS vouchers CASCADE;
DROP TABLE IF EXISTS ticket_templates CASCADE;
DELETE FROM schema_migrations WHERE version = '0003_tickets_vouchers_guests_sessions';
