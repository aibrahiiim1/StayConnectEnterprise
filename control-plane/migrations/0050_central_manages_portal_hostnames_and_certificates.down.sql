BEGIN;
DROP TRIGGER IF EXISTS portal_tls_events_append_only ON portal_tls_events;
DROP TABLE IF EXISTS portal_tls_events;
DROP FUNCTION IF EXISTS portal_tls_events_append_only();
DROP TABLE IF EXISTS portal_certificate_orders;
DROP TABLE IF EXISTS portal_custom_domains;
DROP TABLE IF EXISTS portal_hostnames;
DROP TABLE IF EXISTS portal_tls_settings;
COMMIT;
