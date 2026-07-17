-- Gate P — rollback (site DB). Idempotent. Reverts the least-privilege roles.
-- Operational rollback order: (1) restore each service .env DSN from its 0600 backup and restart
-- the service (returns it to the break-glass superuser DSN), THEN (2) run this to drop the roles.
-- Roles own nothing (Gate P never transfers object ownership), so DROP is clean.

\set ON_ERROR_STOP on

DO $$
DECLARE r text;
BEGIN
  FOREACH r IN ARRAY ARRAY['svc_scd','svc_edged','svc_acctd','svc_netd'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=r) THEN
      EXECUTE format('REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM %I', r);
      EXECUTE format('REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM %I', r);
      EXECUTE format('REVOKE ALL ON SCHEMA public FROM %I', r);
      EXECUTE format('DROP ROLE %I', r);
    END IF;
  END LOOP;
END $$;
