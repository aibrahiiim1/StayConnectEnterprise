-- Gate P — rollback (site DB). Idempotent, fail-closed. Reverts the least-privilege roles.
-- Operational order: (1) restore each service .env DSN from its 0600 backup and restart the service
-- (returns it to the break-glass superuser DSN) FIRST, THEN (2) run this to drop the roles.
-- Roles own nothing (Gate P never transfers ownership), so DROP is clean. No destructive
-- ownership reassignment against production.

\set ON_ERROR_STOP on

DO $$
DECLARE r text; n int;
BEGIN
  FOREACH r IN ARRAY ARRAY['svc_scd','svc_edged','svc_acctd','svc_netd'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=r) THEN CONTINUE; END IF;
    -- fail closed: no active backend may still be connected as this role
    SELECT count(*) INTO n FROM pg_stat_activity WHERE usename=r;
    IF n > 0 THEN RAISE EXCEPTION 'ROLLBACK BLOCKER: % still has % active connection(s) — revert its DSN + restart the service first', r, n; END IF;
    -- fail closed: role must own nothing (Gate P never assigns ownership)
    SELECT count(*) INTO n FROM pg_class WHERE relowner=(SELECT oid FROM pg_roles WHERE rolname=r);
    IF n > 0 THEN RAISE EXCEPTION 'ROLLBACK BLOCKER: % owns % object(s) — refusing destructive reassignment', r, n; END IF;
    -- DROP OWNED BY removes every privilege GRANTed to the role and every default-ACL entry that
    -- references it (across schemas), in addition to any owned objects (there are none). This is the
    -- clean, complete way to detach a grantee before DROP ROLE.
    EXECUTE format('DROP OWNED BY %I', r);
    EXECUTE format('DROP ROLE %I', r);
  END LOOP;
END $$;
