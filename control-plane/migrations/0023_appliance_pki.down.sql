-- REVERSES 0023_appliance_pki. DESTROYS the appliance PKI records (issued certificates, CSRs, revocations and
-- the certificate event log). An operator's tool for an empty or disposable database, never an incident
-- response: every appliance would lose the record of the certificate it presents.

BEGIN;

ALTER TABLE appliances DROP COLUMN IF EXISTS cert_not_after;
ALTER TABLE appliances DROP COLUMN IF EXISTS current_cert_fingerprint;
DROP TABLE IF EXISTS appliance_certificate_events;
DROP TABLE IF EXISTS appliance_certificate_revocations;
DROP TABLE IF EXISTS appliance_certificates;
DROP TABLE IF EXISTS appliance_certificate_requests;
DROP TABLE IF EXISTS appliance_ca_versions;

COMMIT;
