-- CENTRAL IS LICENSING ONLY: REMOVE THE TELEMETRY IT NO LONGER COLLECTS.
--
-- The code that wrote these tables was deleted in this same delivery -- the fleet consumer, the heartbeat
-- consumer, the command and update channels, the usage API. What is left here is stored data with nothing
-- to write it and nothing to read it.
--
-- WHAT IS BEING DELETED, AND WHOSE IT IS. 251 166 fleet_telemetry rows and 251 286 dedupe marks, from 13
-- appliance identities, spanning 2026-07-12 to 2026-09-13. This is operational telemetry -- session counts,
-- byte totals, service health, licence acknowledgements -- and it is sanitized by construction: the ingest
-- path rejected any payload carrying a guest, room, reservation or credential field. It is nonetheless
-- hotel operational data that a decision has been taken not to hold, and the Product Owner has authorised
-- its permanent removal rather than its disablement.
--
-- NONE OF IT IS LICENSING. A licence is issued, fetched, validated and enforced over HTTPS against
-- appliances, licenses, assignments and the certificate tables, none of which are touched here. license_ack
-- is the closest thing to an exception and is not one: Central accepted it as a telemetry KIND and stored a
-- row that nothing consumes and no licence operation depends on. It is a report ABOUT licensing.
--
-- Verified before writing this: no foreign key anywhere in the database references any of these tables, so
-- nothing else loses a row.

BEGIN;

DROP TABLE IF EXISTS fleet_telemetry_dedupe;
DROP TABLE IF EXISTS fleet_telemetry;
DROP TABLE IF EXISTS usage_counters;
DROP TABLE IF EXISTS appliance_commands;
DROP TABLE IF EXISTS appliance_update_assignments;

COMMIT;
