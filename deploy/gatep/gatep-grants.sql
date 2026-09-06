-- Gate P — exact table-scoped least-privilege GRANTS for site-DB runtime roles.
-- Derived from real Go source (write-verb inventory per service) and validated on an isolated
-- reconstruction of the production schema (docs/acceptance/Phase1B-GateP-Grant-Derivation.md).
-- Idempotent. Grants ONLY; role creation + passwords are applied separately at execution time
-- (deploy/gatep/gatep-roles.sql.tmpl, secrets generated on the appliance).
--
-- BINDING: public-schema only; ZERO iam_v2 (schema/table/sequence/function); no ALL-TABLES grants;
-- DELETE only where the code deletes; sequence USAGE only for tables the role INSERTs.
-- PHASE_1B_PRODUCTION_IAM_V2_RUNTIME: NONE.

\set ON_ERROR_STOP on

-- ===========================================================================
-- ONE TRANSACTION. The revoke, every per-service re-grant and every assertion
-- either all take effect or none do.
--
-- This file's preamble strips every iam_v2 privilege from the runtime roles
-- before re-granting the allowlist, so between those two points the services
-- can do nothing. Run as a sequence of autocommitted statements, any failure
-- in between -- a missing include, an invalid grant, a failed assertion --
-- leaves Production in that gap permanently, with the revoke applied and the
-- re-grant not.
--
-- That is not hypothetical. A reconcile aborted on the D32 assertion after the
-- revoke had already committed, and svc_scd lost EXECUTE on the PMS
-- authentication path until the per-service files were reapplied by hand.
--
-- Inside a transaction the same failure changes nothing at all: ON_ERROR_STOP
-- aborts, the implicit ROLLBACK discards the revoke along with everything
-- else, and the effective privilege set is exactly what it was before. A
-- reconcile that fails must be a non-event.
-- ===========================================================================
BEGIN;

-- ===========================================================================
-- RECONCILER PREAMBLE — converge each svc_* role to EXACTLY the allowlist below.
-- Fail closed on unexpected ownership or membership; revoke everything first so a
-- re-run is idempotent and any pre-existing excess privilege is removed.
-- ===========================================================================
DO $$
DECLARE r text; n int; who text;
BEGIN
  FOREACH r IN ARRAY ARRAY['svc_scd','svc_edged','svc_acctd','svc_netd','svc_pmsd'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=r) THEN CONTINUE; END IF;
    -- fail closed: runtime role must own NO object
    SELECT count(*) INTO n FROM pg_class WHERE relowner=(SELECT oid FROM pg_roles WHERE rolname=r);
    IF n > 0 THEN RAISE EXCEPTION 'GATE-P BLOCKER: role % owns % object(s) — must own nothing', r, n; END IF;
    -- fail closed: runtime role must be a member of NO role
    SELECT string_agg(g.rolname, ',') INTO who
      FROM pg_auth_members m JOIN pg_roles g ON g.oid=m.roleid
      WHERE m.member=(SELECT oid FROM pg_roles WHERE rolname=r);
    IF who IS NOT NULL THEN RAISE EXCEPTION 'GATE-P BLOCKER: role % has unexpected membership: %', r, who; END IF;
    -- revoke everything (public + iam_v2) so the grants below are the exact effective set
    EXECUTE format('REVOKE ALL PRIVILEGES ON ALL TABLES    IN SCHEMA public FROM %I', r);
    EXECUTE format('REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public FROM %I', r);
    EXECUTE format('REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA public FROM %I', r);
    EXECUTE format('REVOKE ALL ON SCHEMA public FROM %I', r);
    EXECUTE format('REVOKE ALL PRIVILEGES ON ALL TABLES    IN SCHEMA iam_v2 FROM %I', r);
    EXECUTE format('REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA iam_v2 FROM %I', r);
    EXECUTE format('REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA iam_v2 FROM %I', r);
    EXECUTE format('REVOKE ALL ON SCHEMA iam_v2 FROM %I', r);
  END LOOP;
END $$;

-- ZERO-LEGACY: the superseded guest-IAM tables are gone, and so are their grants.
--
-- public.sessions, guests, guest_accounts, vouchers, voucher_batches, ticket_templates and payments were
-- dropped by migration 0049. Every GRANT and every sequence-ownership row naming them was removed with them.
-- A grant on a table that does not exist is not harmless: it fails this file, and Gate-P failing is a stop.
--
-- Nothing replaced these grants at this layer. The privileges the services actually need on the current
-- guest domain are the per-service iam_v2 grants included below.
-- ---------------------------------------------------------------------------
-- svc_scd : session / auth / credential / appliance-lifecycle
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO svc_scd;
GRANT SELECT,INSERT,UPDATE        ON public.auth_otps              TO svc_scd; -- issue/verify/attempts
GRANT SELECT,INSERT,UPDATE        ON public.social_oauth_states    TO svc_scd; -- CSRF single-use
GRANT SELECT                      ON public.social_oauth_providers TO svc_scd;
GRANT SELECT,UPDATE               ON public.pms_providers          TO svc_scd; -- provider read + status
GRANT SELECT,INSERT               ON public.pms_attempts           TO svc_scd; -- per-room/IP lockout
GRANT SELECT,INSERT,UPDATE,DELETE ON public.sync_outbox            TO svc_scd; -- outbox drain
GRANT SELECT,INSERT,UPDATE        ON public.sync_checkpoints       TO svc_scd;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.tenants                TO svc_scd; -- local assignment provisioning
GRANT SELECT,INSERT,UPDATE        ON public.tenant_effective_limits TO svc_scd;
GRANT SELECT,UPDATE               ON public.guest_networks         TO svc_scd; -- IP->network + status
GRANT SELECT,UPDATE               ON public.notification_providers TO svc_scd;
GRANT SELECT                      ON public.walled_garden_rules    TO svc_scd;
GRANT INSERT                      ON public.audit_log              TO svc_scd; -- append-only
GRANT SELECT,INSERT,UPDATE        ON public.appliances             TO svc_scd; -- enrollment/claim
GRANT SELECT,INSERT,DELETE        ON public.sites                  TO svc_scd; -- local assignment provisioning
GRANT SELECT,INSERT              ON public.edge_executed_commands  TO svc_scd; -- command channel
GRANT SELECT,INSERT              ON public.edge_installed_updates  TO svc_scd; -- updates
GRANT SELECT,INSERT,UPDATE       ON public.edge_offline_packages   TO svc_scd; -- offline pkgs
GRANT SELECT,INSERT,UPDATE,DELETE ON public.auth_throttle_buckets   TO svc_scd; -- durable throttle (0007): read/increment/block/cleanup; no sequence (composite PK)
GRANT SELECT                      ON public.otp_hmac_key_generations TO svc_scd; -- OTP gen metadata READ only (0008); creation/rotation are operational/migration-only

-- svc_scd — secure cross-tenant transition (reconcileTenantOwnership) + tenant/site mirror seed.
-- On EVERY boot scd runs hasForeignTenantData() which SELECTs EXISTS across tenants/sites and every
-- tenant-owned table; on a confirmed cross-tenant reassignment it purges (DELETE) all foreign-owned
-- rows in one transaction. It also upserts the tenant/site mirror rows (INSERT .. ON CONFLICT DO
-- UPDATE). These grants make scd the owner of that reconciliation without touching iam_v2.
GRANT UPDATE                      ON public.sites                   TO svc_scd; -- mirror upsert ON CONFLICT DO UPDATE (already had S/I/D)
GRANT SELECT,DELETE               ON public.accounting_records      TO svc_scd; -- cross-tenant detect + purge
GRANT SELECT,DELETE               ON public.stripe_events           TO svc_scd; -- cross-tenant detect + purge
GRANT SELECT,DELETE               ON public.stripe_accounts         TO svc_scd; -- cross-tenant detect + purge
GRANT SELECT,DELETE               ON public.operator_roles          TO svc_scd; -- cross-tenant detect + purge
GRANT SELECT,DELETE               ON public.operators               TO svc_scd; -- cross-tenant detect + purge
GRANT DELETE                      ON public.pms_attempts            TO svc_scd; -- cross-tenant purge (already had S/I)
GRANT DELETE                      ON public.walled_garden_rules     TO svc_scd; -- cross-tenant purge (already had S)
GRANT DELETE                      ON public.notification_providers  TO svc_scd; -- cross-tenant purge (already had S/U)
GRANT DELETE                      ON public.pms_providers           TO svc_scd; -- cross-tenant purge (already had S/U)
GRANT DELETE                      ON public.social_oauth_providers  TO svc_scd; -- cross-tenant purge (already had S)
GRANT DELETE                      ON public.tenant_effective_limits TO svc_scd; -- cross-tenant purge (already had S/I/U)

-- ---------------------------------------------------------------------------
-- svc_edged : admin API / Hotel-Admin backend (broad CRUD on config)
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.appliance_boot_convergence TO svc_edged;
GRANT SELECT,INSERT,DELETE        ON public.appliance_recovery_events  TO svc_edged;
GRANT SELECT,INSERT               ON public.appliance_service_health   TO svc_edged;
GRANT INSERT                      ON public.audit_log                  TO svc_edged;
GRANT SELECT                      ON public.backup_records             TO svc_edged;
GRANT SELECT,INSERT,DELETE        ON public.dhcp_pools                 TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.dhcp_reservations          TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.guest_networks             TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.network_interfaces         TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.network_config_revisions   TO svc_edged;
GRANT SELECT,INSERT               ON public.network_apply_events        TO svc_edged;
GRANT SELECT,INSERT               ON public.network_health_checks       TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.notification_providers     TO svc_edged;
GRANT SELECT,INSERT,DELETE        ON public.operator_roles             TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.operators                  TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.pms_providers              TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.social_oauth_providers     TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.stripe_accounts            TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.sync_outbox                TO svc_edged;
-- sync_checkpoints is a single upserted row per checkpoint name, not a log: the outbox drainer writes it
-- with INSERT ... ON CONFLICT (name) DO UPDATE, which needs UPDATE for the same reason as
-- network_interfaces above. Without it the drain checkpoint silently stops advancing.
GRANT SELECT,INSERT,UPDATE        ON public.sync_checkpoints           TO svc_edged;
GRANT SELECT,UPDATE               ON public.tenants                    TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.tenant_effective_limits    TO svc_edged;
GRANT SELECT,INSERT,DELETE        ON public.walled_garden_rules        TO svc_edged;

-- ---------------------------------------------------------------------------
-- svc_acctd : accounting (append usage; update session usage)
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO svc_acctd;
GRANT INSERT        ON public.accounting_records TO svc_acctd; -- append-only samples

-- ---------------------------------------------------------------------------
-- svc_netd : networking only (no credentials, no iam_v2)
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO svc_netd;
GRANT SELECT,INSERT,UPDATE ON public.network_config_revisions TO svc_netd;
GRANT SELECT,INSERT        ON public.network_apply_events     TO svc_netd;
GRANT SELECT,INSERT        ON public.network_health_checks    TO svc_netd;
-- network_interfaces is an INVENTORY netd refreshes, not an append-only log, so it needs UPDATE.
--
-- netd syncs it with INSERT ... ON CONFLICT (name) DO UPDATE, and PostgreSQL requires UPDATE privilege
-- for the DO UPDATE clause whether or not a row actually conflicts. With SELECT,INSERT alone every sync
-- failed with "permission denied for table network_interfaces" — and netd ignores that error, so the
-- table simply stayed empty.
--
-- The consequence landed nowhere near the cause. Guest-network validation reads this table to decide
-- which interfaces exist, so an empty inventory made EVERY parent interface fail with
-- interface_not_found: "interface \"ens192\" was not found on the appliance" — for an interface that is
-- present, discovered, and offered by the wizard's own dropdown. It reads as broken hardware.
GRANT SELECT,INSERT,UPDATE ON public.network_interfaces       TO svc_netd;
GRANT INSERT               ON public.system_network_audit     TO svc_netd; -- append-only
GRANT SELECT               ON public.guest_networks           TO svc_netd; -- read for apply
GRANT SELECT               ON public.dhcp_pools               TO svc_netd;
GRANT SELECT               ON public.dhcp_reservations        TO svc_netd;

-- ---------------------------------------------------------------------------
-- Per-table sequence USAGE — ONLY for sequences owned by columns of tables the
-- role INSERTs (no GRANT ON ALL SEQUENCES). Generated from pg_depend, so it stays
-- exact even as identity/serial columns change.
-- ---------------------------------------------------------------------------
DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT g.rolname, s.relname AS seq
    FROM (VALUES
      ('svc_scd','auth_otps'),('svc_scd','social_oauth_states'),
      ('svc_scd','sync_outbox'),('svc_scd','sync_checkpoints'),('svc_scd','pms_attempts'),('svc_scd','audit_log'),
      ('svc_scd','tenants'),('svc_scd','tenant_effective_limits'),('svc_scd','appliances'),('svc_scd','sites'),
      ('svc_scd','edge_executed_commands'),('svc_scd','edge_installed_updates'),('svc_scd','edge_offline_packages'),
      ('svc_edged','appliance_boot_convergence'),('svc_edged','appliance_recovery_events'),('svc_edged','appliance_service_health'),
      ('svc_edged','audit_log'),('svc_edged','dhcp_pools'),('svc_edged','dhcp_reservations'),
      ('svc_edged','guest_networks'),('svc_edged','network_interfaces'),('svc_edged','network_config_revisions'),
      ('svc_edged','network_apply_events'),('svc_edged','network_health_checks'),('svc_edged','notification_providers'),
      ('svc_edged','operator_roles'),('svc_edged','operators'),('svc_edged','pms_providers'),('svc_edged','social_oauth_providers'),
      ('svc_edged','stripe_accounts'),('svc_edged','sync_outbox'),('svc_edged','sync_checkpoints'),('svc_edged','tenant_effective_limits'),
      ('svc_edged','walled_garden_rules'),
      ('svc_acctd','accounting_records'),
      ('svc_netd','network_config_revisions'),('svc_netd','network_apply_events'),('svc_netd','network_health_checks'),
      ('svc_netd','network_interfaces'),('svc_netd','system_network_audit')
    ) AS g(rolname,tbl)
    JOIN pg_class t ON t.relname=g.tbl AND t.relnamespace='public'::regnamespace
    JOIN pg_depend d ON d.refobjid=t.oid AND d.deptype IN ('a','i')
    JOIN pg_class s ON s.oid=d.objid AND s.relkind='S'
  LOOP
    EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE public.%I TO %I', r.seq, r.rolname);
  END LOOP;
END $$;

-- ---------------------------------------------------------------------------
-- Deny future objects to service roles by default — explicit FOR ROLE per owner
-- that creates public objects (current owner `stayconnect`; `site_migrator` when present).
-- ---------------------------------------------------------------------------
DO $$
DECLARE o text;
BEGIN
  FOREACH o IN ARRAY ARRAY['stayconnect','site_migrator'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=o) THEN
      EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public REVOKE ALL ON TABLES    FROM svc_scd, svc_edged, svc_acctd, svc_netd', o);
      EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public REVOKE ALL ON SEQUENCES FROM svc_scd, svc_edged, svc_acctd, svc_netd', o);
      EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA public REVOKE ALL ON FUNCTIONS FROM svc_scd, svc_edged, svc_acctd, svc_netd', o);
    END IF;
  END LOOP;
END $$;

-- ---------------------------------------------------------------------------------------------------------
-- THE CONTROLLED-OPERATION OPENER, FOR THE ADMIN WRITER (added for the DEVELOPMENT IAM-v2 trial, D31/T0068)
--
-- edged refuses to serve the Phase-3 admin surface at startup unless the role it connects as can OPEN a
-- controlled operation: "this process cannot open an auth_context operation (no EXECUTE on
-- iam_v2.begin_controlled_operation(text)), so every authoritative write in that family would be refused at
-- the first attempt". That guard is correct -- a writer that cannot open the operation would fail on its first
-- authoritative write, and failing at startup is better than failing in front of an operator.
--
-- This is the MINIMUM that satisfies it: EXECUTE on the opener only. It grants no table write, no schema
-- create, no ownership and no membership. The controlled-writer trigger still refuses any write that is not
-- inside an open operation, so the privilege lets edged open one -- it does not let it bypass one.
--
-- It lives here, in the Gate-P privilege bootstrap, rather than as an undocumented GRANT typed on one
-- appliance, so any rebuilt environment reproduces it.
GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) TO svc_edged;

-- ...AND FOR THE ACCOUNTING WRITER. acctd enforces the identical boundary and was missed when edged's grant
-- was written, because edged was the service being debugged that day and nobody asked which OTHER services
-- run the same check.
--
-- The cost of missing it was hidden until a real reboot: acctd refused to start, systemd restarted it every
-- few seconds under Restart=always, and `systemctl show` answered "active running" whenever it was sampled
-- during the brief window between a restart and the next failure. Ten restarts in three minutes read as a
-- healthy service. Only counting restart jobs per boot from the journal exposed it -- the same lesson as
-- netd, on a different service, found only because this reboot was measured rather than assumed.
--
-- Same minimum as above: EXECUTE on the opener, nothing else. acctd still cannot write outside an open
-- controlled operation.
GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) TO svc_acctd;

-- ===========================================================================================================
-- D32 -- CHECKOUT GRACE: the audited policy boundary, and the privileges it actually needs.
--
-- WHY THIS IS HERE AND NOT IN A SIDE FILE
-- ---------------------------------------
-- The reconciler preamble at the top of this file REVOKEs ALL privileges on schema iam_v2 from every svc_*
-- role before re-granting the allowlist. Any iam_v2 grant that lives in a separate script is therefore
-- ERASED the next time Gate-P is applied, and the surface it feeds starts failing at some later, unrelated
-- moment -- exactly the failure shape that made the netd stored-bundle replay so expensive to find. A grant
-- that a re-run of the canonical bootstrap silently deletes is not reproducible, whatever file it is in.
--
-- This was not one stray file. EVERY per-service iam_v2 grant written during the trial lived beside this
-- bootstrap rather than inside it, so a single Gate-P re-run would have silently disarmed the enforcement
-- plane, accounting, guest auth, voucher redemption and the commerce surface at once -- each one failing later,
-- somewhere else, for a reason that would not point back to the re-run. They are all INCLUDED here, after the
-- revoke, in dependency-free order. \ir resolves relative to THIS file, so it works from any working directory.
--
-- Each included file remains individually applicable (that is how they are deployed incrementally), and each is
-- idempotent, so including them changes nothing about a single-file apply -- it only removes the window in
-- which the canonical path and the real privilege set disagree.
\ir svc-service-health-grants.sql
\ir svc-scd-iamv2-guest-auth-grants.sql
\ir svc-scd-iamv2-guest-commerce-grants.sql
\ir svc-voucher-iamv2-grants.sql
\ir svc-netd-iamv2-enforcement-grants.sql
\ir svc-acctd-guest-networks-grant.sql
\ir svc-acctd-iamv2-accounting-grants.sql
\ir svc-edged-phase2-commerce-grants.sql
\ir svc-edged-phase345-admin-grants.sql
-- svc_pmsd is reconciled here like every other runtime role. It used to be converged nowhere: absent from the
-- revoke loop above and from this list, so its privileges were whatever the last incremental apply happened to
-- leave, and the reconcile's own D32 assertion then judged a role it had never converged.
\ir svc-pmsd-iamv2-connector-grants.sql

-- EXECUTE on the CANONICAL audited, versioned publication boundary. This is the only function the product
-- calls to change checkout grace policy: it requires an active operator as actor, a bounded machine reason
-- code and the version the caller last read, validates the derived package with the same matcher the checkout
-- conversion uses, and appends to iam_v2.checkout_grace_policy_publications.
GRANT EXECUTE ON FUNCTION iam_v2.publish_checkout_grace_policy(
  uuid, uuid, uuid, integer, integer, integer, bigint, integer, text, integer, integer, uuid, text
) TO svc_edged;

-- ...and on the matcher itself, which the publication path consults BEFORE publishing so a bad derivation is
-- reported as the specific condition it violated rather than as a generic publication failure.
GRANT EXECUTE ON FUNCTION iam_v2.grace_package_mismatch_reason(
  uuid, uuid, uuid, integer, integer, integer, bigint, integer, text
) TO svc_edged;

-- ---- and the privilege that is NOT the caller's ------------------------------------------------------
-- iam_v2.publish_checkout_grace_policy is SECURITY DEFINER owned by iam_v2_owner, and it validates the actor
-- against public.operators. A SECURITY DEFINER function runs as its OWNER, so the privilege that decides
-- whether that lookup succeeds is iam_v2_owner's -- granting the CALLING service SELECT on public.operators
-- changes nothing and publication still fails with "permission denied for table operators". That is a genuinely
-- misleading failure: the caller has every privilege the error appears to be about.
--
-- Read-only, and only this table: the actor check reads an operator, it never writes one.
--
-- Guarded, because iam_v2_owner exists only where the IAM-v2 domain has been created; on a public-schema-only
-- database this section must be a no-op rather than an error.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'GRANT SELECT ON public.operators TO iam_v2_owner';
  END IF;
END $$;

-- ===========================================================================================================
-- D32 CLOSING ASSERTION -- no runtime role, and not PUBLIC, may reach grace policy any way but the audited one.
--
-- WHY AN ASSERTION AND NOT JUST THE REVOKES ABOVE
-- ----------------------------------------------
-- The revokes are each written where the matching grant was, which is correct for readability and useless as a
-- guarantee: they only close the doors somebody remembered. The raw writer stayed reachable by svc_netd through
-- the entire D32 retirement precisely because that retirement was written from the perspective of the service
-- being fixed -- edged's raw path was closed in code AND privilege while netd, which nobody was looking at,
-- kept EXECUTE on the very function the design had just declared unreachable.
--
-- An unused privilege is silent. Nothing fails, no log line appears, and the next person to read the design
-- sees "the raw path is closed" while the database says otherwise. So the property is asserted here directly
-- against the catalog, over EVERY runtime role rather than a list of the ones we thought of, and the bootstrap
-- REFUSES to complete if it does not hold.
--
-- The property has two halves, and both matter:
--   1. no runtime role and no PUBLIC may EXECUTE the raw writer, or INSERT/UPDATE/DELETE the config table;
--   2. the canonical audited boundary MUST still be executable by svc_edged -- an "everything revoked" state
--      would satisfy half 1 perfectly and leave the product unable to save grace at all.
-- Asserting only the first half would make a totally broken system look maximally secure.
DO $$
DECLARE
  r record;
  raw_fn text := 'iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,integer,integer,integer,bigint,integer,text,integer)';
  pol_fn text := 'iam_v2.publish_checkout_grace_policy(uuid,uuid,uuid,integer,integer,integer,bigint,integer,text,integer,integer,uuid,text)';
  offenders text := '';
BEGIN
  IF to_regprocedure(pol_fn) IS NULL THEN
    -- Nothing to assert on a database that predates the boundary; a public-schema-only or pre-Phase-3
    -- database must not fail the whole bootstrap.
    RETURN;
  END IF;

  -- Half 1, over every non-superuser login role that is not an owner: the set is taken from pg_roles rather
  -- than from a hand-maintained list, so a runtime role added later is covered on the day it is created.
  FOR r IN
    SELECT rolname FROM pg_roles
     WHERE rolcanlogin AND NOT rolsuper
       AND rolname NOT IN ('iam_v2_owner', 'stayconnect')
  LOOP
    IF to_regprocedure(raw_fn) IS NOT NULL
       AND has_function_privilege(r.rolname, raw_fn, 'EXECUTE') THEN
      offenders := offenders || format(' %s:EXECUTE-on-raw-writer', r.rolname);
    END IF;
    IF has_table_privilege(r.rolname, 'iam_v2.site_checkout_grace_config', 'INSERT')
       OR has_table_privilege(r.rolname, 'iam_v2.site_checkout_grace_config', 'UPDATE')
       OR has_table_privilege(r.rolname, 'iam_v2.site_checkout_grace_config', 'DELETE') THEN
      offenders := offenders || format(' %s:direct-DML-on-grace-config', r.rolname);
    END IF;
  END LOOP;

  IF to_regprocedure(raw_fn) IS NOT NULL
     AND has_function_privilege('public', raw_fn, 'EXECUTE') THEN
    offenders := offenders || ' PUBLIC:EXECUTE-on-raw-writer';
  END IF;
  IF has_function_privilege('public', pol_fn, 'EXECUTE') THEN
    offenders := offenders || ' PUBLIC:EXECUTE-on-audited-boundary';
  END IF;
  IF has_table_privilege('public', 'iam_v2.site_checkout_grace_config', 'INSERT')
     OR has_table_privilege('public', 'iam_v2.site_checkout_grace_config', 'UPDATE')
     OR has_table_privilege('public', 'iam_v2.site_checkout_grace_config', 'DELETE') THEN
    offenders := offenders || ' PUBLIC:direct-DML-on-grace-config';
  END IF;

  IF offenders <> '' THEN
    RAISE EXCEPTION 'GATE-P BLOCKER (D32): a raw grace mutation path is still reachable:%', offenders;
  END IF;

  -- Half 2: the audited path must still work for the service that serves the Hotel-Admin surface.
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged')
     AND NOT has_function_privilege('svc_edged', pol_fn, 'EXECUTE') THEN
    RAISE EXCEPTION
      'GATE-P BLOCKER (D32): every raw grace path is closed but svc_edged cannot EXECUTE the audited '
      'boundary either, so grace policy cannot be published at all';
  END IF;
END $$;

COMMIT;
