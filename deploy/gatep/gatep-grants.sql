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
GRANT SELECT                      ON public.pms_attempts           TO svc_scd; -- read by the cross-tenant check; the legacy per-room lockout writer (internal/pmsguard) is not linked into scd, so no INSERT (audit B-7)
GRANT SELECT,INSERT,UPDATE,DELETE ON public.tenants                TO svc_scd; -- local assignment provisioning
GRANT SELECT,INSERT,UPDATE        ON public.tenant_effective_limits TO svc_scd;
GRANT SELECT,UPDATE               ON public.guest_networks         TO svc_scd; -- IP->network + status
GRANT SELECT,UPDATE               ON public.notification_providers TO svc_scd;
GRANT SELECT                      ON public.walled_garden_rules    TO svc_scd;
GRANT INSERT                      ON public.audit_log              TO svc_scd; -- append-only
GRANT SELECT,INSERT,UPDATE        ON public.appliances             TO svc_scd; -- enrollment/claim
GRANT SELECT,INSERT,DELETE        ON public.sites                  TO svc_scd; -- local assignment provisioning
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
GRANT SELECT,DELETE               ON public.operator_roles          TO svc_scd; -- cross-tenant detect + purge
GRANT SELECT,DELETE               ON public.operators               TO svc_scd; -- cross-tenant detect + purge
-- ONE COLUMN, NOT THE TABLE. scd binds the appliance's local operators to the tenant its SIGNED assignment
-- names (cmd/scd/assignment.go, seedTenantSiteMirror), because iam_v2.p4_assert_financial_actor decides who
-- may act on financial recovery by reading operators.tenant_id -- and on an appliance that column was NULL,
-- so all four actor-gated kernels were unperformable. Measured during this closure's restore drill: a
-- supported restore entered FINANCIAL_RECOVERY_MODE and nobody could release it.
--
-- A plain `GRANT UPDATE ON public.operators` would let scd rewrite password_hash and status -- it could
-- disable an administrator or replace their credential. Column-level UPDATE is exactly the authority the
-- binding needs and nothing more: PostgreSQL refuses an UPDATE that touches any other column, and
-- has_table_privilege('svc_scd','public.operators','UPDATE') stays FALSE, because a column grant is not a
-- table grant. Verified on PRE-LIVE after applying this: the column UPDATE succeeds and the table-level
-- privilege still reads false.
-- TWO COLUMNS, because the statement sets two. Column-level UPDATE covers exactly the columns named, and
-- an UPDATE that touches one ungranted column is refused for the whole table: measured here, `SET
-- tenant_id` succeeded as svc_scd and `SET tenant_id, updated_at = now()` failed with "permission denied
-- for table operators". updated_at is a timestamp the write should keep honest, not a privilege question.
GRANT UPDATE (tenant_id, updated_at) ON public.operators            TO svc_scd; -- assignment tenant binding
GRANT DELETE                      ON public.pms_attempts            TO svc_scd; -- cross-tenant purge (already had S)
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
-- THE CLOUD TELEMETRY QUEUE IS GONE (migration 0093). public.sync_outbox and public.sync_checkpoints, and
-- the grants to svc_scd, svc_edged and iam_v2_owner that served them, were removed with it; so were the
-- command-channel and update-agent ledgers (edge_executed_commands, edge_installed_updates) and svc_scd's
-- grants on them.
GRANT SELECT,UPDATE               ON public.tenants                    TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.tenant_effective_limits    TO svc_edged;
GRANT SELECT,INSERT,DELETE        ON public.walled_garden_rules        TO svc_edged;
-- Admin Console sign-in protection (0119): the policy is read and changed ONLY through its definer functions
-- (the setter appends the change row), and the live counters ONLY through fail/state/clear -- no table write,
-- so the policy cannot change without a record and a lockout cannot be wiped except by a correct password.
-- GUARDED on 0119 being present (audit B-20), so the reconcile runs on a database rolled back below it.
DO $g0119$
BEGIN
  IF to_regclass('public.operator_signin_protection_changes') IS NULL
     OR to_regprocedure('public.operator_signin_throttle_fail(text,text)') IS NULL THEN
    RAISE NOTICE 'migration 0119 is not applied -- operator sign-in protection grants skipped';
    RETURN;
  END IF;
  GRANT EXECUTE ON FUNCTION public.operator_signin_protection_get()                                         TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.operator_signin_protection_set(integer,integer,integer,integer,integer,text,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.operator_signin_throttle_state(text,text)                                TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.operator_signin_throttle_fail(text,text)                                 TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.operator_signin_throttle_clear(text,text)                                TO svc_edged;
  GRANT SELECT  ON public.operator_signin_protection_changes                                                 TO svc_edged;
END $g0119$;

-- ---------------------------------------------------------------------------
-- svc_acctd : accounting (append usage; update session usage)
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO svc_acctd;
-- No grant on public.accounting_records any more (audit B-7, migration 0124): acctd stopped writing the public
-- table when accounting moved to iam_v2.ingest_absolute_counters, and holds no statement against it.

-- ---------------------------------------------------------------------------
-- svc_netd : networking only (no credentials, no iam_v2)
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO svc_netd;
GRANT SELECT,INSERT,UPDATE ON public.network_config_revisions TO svc_netd;
GRANT SELECT,INSERT        ON public.network_apply_events     TO svc_netd;
GRANT SELECT,INSERT        ON public.network_health_checks    TO svc_netd;
-- network_interfaces is an INVENTORY netd REFRESHES, not an append-only log, so it needs UPDATE.
--
-- netd syncs it with INSERT ... ON CONFLICT (name) DO UPDATE, and PostgreSQL requires UPDATE privilege for
-- the DO UPDATE clause whether or not a row actually conflicts. With SELECT,INSERT alone every refresh
-- failed with "permission denied for table network_interfaces" -- and both call sites in netd discard that
-- error, so nothing was written and nothing was logged.
--
-- The consequence landed nowhere near the cause. Guest-network validation reads this table to decide which
-- interfaces exist, so a table that never refreshes made parents fail with interface_not_found -- naming an
-- interface that is present, that netd discovers, and that the wizard offers from its own dropdown. It
-- reads as broken hardware.
--
-- Measured on the PRE-LIVE appliance before this grant existed: last_seen_at frozen at 2026-08-22 across
-- every later netd restart, holding a bridge that no longer exists and missing every bridge that does.
GRANT SELECT,INSERT,UPDATE ON public.network_interfaces       TO svc_netd;
GRANT SELECT, INSERT       ON public.system_network_audit     TO svc_netd; -- append-only; SELECT for the history screen
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
      ('svc_scd','audit_log'),
      ('svc_scd','tenants'),('svc_scd','tenant_effective_limits'),('svc_scd','appliances'),('svc_scd','sites'),
      ('svc_scd','edge_offline_packages'),
      ('svc_edged','appliance_boot_convergence'),('svc_edged','appliance_recovery_events'),('svc_edged','appliance_service_health'),
      ('svc_edged','audit_log'),('svc_edged','dhcp_pools'),('svc_edged','dhcp_reservations'),
      ('svc_edged','guest_networks'),('svc_edged','network_interfaces'),('svc_edged','network_config_revisions'),
      ('svc_edged','network_apply_events'),('svc_edged','network_health_checks'),('svc_edged','notification_providers'),
      ('svc_edged','operator_roles'),('svc_edged','operators'),('svc_edged','pms_providers'),('svc_edged','social_oauth_providers'),
      ('svc_edged','tenant_effective_limits'),
      ('svc_edged','walled_garden_rules'),
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
\ir svc-payment-least-privilege-grants.sql
-- svc_pmsd is reconciled here like every other runtime role. It used to be converged nowhere: absent from the
-- revoke loop above and from this list, so its privileges were whatever the last incremental apply happened to
-- leave, and the reconcile's own D32 assertion then judged a role it had never converged.
\ir svc-pmsd-iamv2-connector-grants.sql
-- The HTTPS portal and sealed social secrets (0116).
\ir svc-portal-https-grants.sql
-- Enforcement policy: private-network reach, MAC binding, cut-off (0190).
\ir svc-enforcement-policy-grants.sql
-- Email accounts: self-service registration, verification and reset (0117).
\ir svc-self-service-grants.sql
-- Device management: client replacement and release, staff slot release and fresh sign-in (0118).
\ir svc-device-management-grants.sql
-- Client accounts: Disable suspends the package, Enable resumes it (0120).
\ir svc-client-accounts-grants.sql
-- Plan controls (idle / maximum session / DISCONNECT_OLDEST / time allowance) and accounting retention (0122).
\ir svc-plan-controls-grants.sql
-- Usage rollup and purge budget (0240).
\ir svc-usage-rollup-grants.sql
-- Guest-data retention: the hourly anonymisation run and its setting (0127).
\ir svc-data-retention-grants.sql
-- Guest data rights: look up, export and erase one guest (0128).
\ir svc-guest-data-rights-grants.sql
-- Backup key holders: backups encrypted to the hotel's own keys (0129).
\ir svc-backup-keys-grants.sql
-- Admin Console authenticator codes and session limits (0132).
\ir svc-operator-mfa-grants.sql
-- Many wrong room sign-ins pause room sign-in on that network; staff confirm a guest's device (0136).
\ir svc-signin-network-pause-grants.sql
-- Disabled staff accounts are anonymised after a set period; staff addresses follow the activity log (0138).
\ir svc-staff-retention-grants.sql
-- Sign-in limits and capacity settings, device retention, settings/session change notifications (0170; AD-30/31/34/51/52/58).
\ir svc-signin-capacity-grants.sql

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
    -- The per-profile privacy-notice stamp (0152, iam_v2.profile_privacy_notice_stamp, SECURITY DEFINER as iam_v2_owner) records the notice a profile network showed.
    EXECUTE 'GRANT SELECT, INSERT ON public.privacy_notice_versions TO iam_v2_owner';
    -- REFERENCES on public.operators is granted in gatep-iam-roles.sql, NOT here, and the difference
    -- matters: this file runs AFTER the numbered migrations, and the privilege is needed BY one of them.
    -- Granting it here would be granting it too late to be used.
  END IF;
END $$;

-- ===========================================================================================================
-- OWNERSHIP ASSERTION -- every iam_v2 table belongs to iam_v2_owner, re-checked on every reconcile.
--
-- Ownership in this schema is load-bearing: the boundary functions are SECURITY DEFINER and execute as their
-- OWNER, and iam_v2_rollback is a member of iam_v2_owner and of nothing else -- so a table owned by anybody
-- else cannot be dropped by the guarded rollback path.
--
-- FOUR TABLES ON PRE-LIVE WERE WRONG, from two different causes, and neither was visible to anything that
-- counted tables: three were created by a migration running under the applying role rather than the owner,
-- and one belonged to the administrative role because its migration was applied by hand. Migrations 0089
-- and 0090 corrected them. This assertion is what stops the next one landing quietly -- a correction fixes
-- what happened, an assertion fixes the class.
--
-- Guarded, because iam_v2_owner exists only where the IAM-v2 domain has been created.
DO $ownassert$
DECLARE bad text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN RETURN; END IF;
  IF to_regnamespace('iam_v2') IS NULL THEN RETURN; END IF;
  SELECT string_agg(c.relname || ' (' || pg_get_userbyid(c.relowner) || ')', ', ' ORDER BY c.relname)
    INTO bad
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = 'iam_v2' AND c.relkind = 'r'
     AND pg_get_userbyid(c.relowner) <> 'iam_v2_owner';
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'iam_v2 tables not owned by iam_v2_owner: %', bad
      USING HINT = 'apply iam_v2 migrations with --apply-role iam_v2_owner, or have the migration declare '
                   'its own ALTER TABLE ... OWNER TO iam_v2_owner';
  END IF;
END $ownassert$;

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

-- ===========================================================================================================
-- B-7 CLOSING ASSERTION -- every runtime role holds what its code uses, and nothing re-widens it (migration 0124).
--
-- The grants above were derived statement by statement from the binaries (the deploy/gatep files say which
-- statement uses each). These are the properties that derivation established and that were found broken before
-- it: an unused privilege is silent, so the only way to keep it from coming back is to refuse the reconcile when
-- it does. Judged over the CATALOG, not over this file's text, so a grant made anywhere -- a later migration, a
-- hand-typed GRANT, a new per-service file -- is judged the same way.
--
-- The two SECURITY INVOKER writers below were reachable through PUBLIC (made effective for every service role by
-- their iam_v2 USAGE); no runtime path calls them. Withdrawn here as well as in 0124, so a reconcile on a schema
-- rolled back below 0124 still holds the property.
DO $b7pub$
BEGIN
  IF to_regprocedure('iam_v2.apply_adjustment(uuid,text,text,uuid,text)') IS NOT NULL THEN
    REVOKE EXECUTE ON FUNCTION iam_v2.apply_adjustment(uuid, text, text, uuid, text) FROM PUBLIC;
  END IF;
  IF to_regprocedure('iam_v2.ingest_sample(uuid,bigint,bigint,bigint,integer)') IS NOT NULL THEN
    REVOKE EXECUTE ON FUNCTION iam_v2.ingest_sample(uuid, bigint, bigint, bigint, integer) FROM PUBLIC;
  END IF;
END $b7pub$;

DO $b7$
DECLARE bad text; f text;
BEGIN
  IF to_regnamespace('iam_v2') IS NULL THEN RETURN; END IF;

  -- 1. The entitlement state writer: admission (scd) and the checkout converter (pmsd) only.
  f := 'iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)';
  IF to_regprocedure(f) IS NOT NULL THEN
    SELECT string_agg(r.rolname, ', ') INTO bad FROM pg_roles r
     WHERE r.rolname LIKE 'svc\_%' AND r.rolname NOT IN ('svc_scd','svc_pmsd')
       AND has_function_privilege(r.rolname, f, 'EXECUTE');
    IF bad IS NOT NULL THEN
      RAISE EXCEPTION 'GATE-P BLOCKER (B-7): % can execute the entitlement state writer', bad;
    END IF;
  END IF;

  -- 2. netd and acctd write no iam_v2 table: every write they make is a SECURITY DEFINER writer's (0039).
  SELECT string_agg(format('%s %s %s', r.rolname, p.priv, c.relname), ', ') INTO bad
    FROM pg_roles r CROSS JOIN pg_class c CROSS JOIN unnest(ARRAY['INSERT','UPDATE','DELETE']) p(priv)
   WHERE r.rolname IN ('svc_netd','svc_acctd') AND c.relnamespace = 'iam_v2'::regnamespace AND c.relkind IN ('r','p')
     AND (has_table_privilege(r.rolname, c.oid, p.priv)
          OR (p.priv <> 'DELETE' AND has_any_column_privilege(r.rolname, c.oid, p.priv)));
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'GATE-P BLOCKER (B-7): iam_v2 write authority for %', bad;
  END IF;

  -- 3. acctd executes none of the device-authorization, rebinding or boundary-termination writers (0039).
  SELECT string_agg(p.oid::regprocedure::text, ', ') INTO bad FROM pg_proc p
   WHERE p.pronamespace = 'iam_v2'::regnamespace
     AND p.proname IN ('authorize_entitlement_device','deauthorize_entitlement_device','p6_record_time_termination',
                       'p6_guest_release_device','p6_set_guest_device_self_service','terminate_entitlement_at_boundary',
                       'rebind_session_entitlement')
     AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_acctd')
     AND has_function_privilege('svc_acctd', p.oid, 'EXECUTE');
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'GATE-P BLOCKER (B-7): svc_acctd can execute %', bad;
  END IF;

  -- 4. No runtime role holds table-level UPDATE on entitlements or sessions, and column-level UPDATE only on the
  --    columns a statement in its binary sets (or a row lock needs).
  SELECT string_agg(format('%s %s', r.rolname, t), ', ') INTO bad
    FROM pg_roles r CROSS JOIN unnest(ARRAY['iam_v2.entitlements','iam_v2.sessions']) t
   WHERE r.rolname LIKE 'svc\_%' AND to_regclass(t) IS NOT NULL AND has_table_privilege(r.rolname, t, 'UPDATE');
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'GATE-P BLOCKER (B-7): table-level UPDATE for %', bad;
  END IF;
  SELECT string_agg(format('%s %s.%s', r.rolname, c.relname, a.attname), ', ') INTO bad
    FROM pg_roles r CROSS JOIN pg_class c JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
   WHERE r.rolname LIKE 'svc\_%' AND c.relnamespace = 'iam_v2'::regnamespace AND c.relname IN ('entitlements','sessions')
     AND has_column_privilege(r.rolname, c.oid, a.attnum, 'UPDATE')
     AND (r.rolname::text, c.relname::text, a.attname::text) NOT IN (
           ('svc_scd','entitlements','status'), ('svc_pmsd','entitlements','status'),
           ('svc_pmsd','sessions','state'), ('svc_pmsd','sessions','ended'), ('svc_pmsd','sessions','end_reason'),
           ('svc_edged','sessions','entitlement_id'));
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'GATE-P BLOCKER (B-7): column UPDATE beyond purpose: %', bad;
  END IF;

  -- 5. The invoker adjustment / scratch ingest writers are the owner's alone.
  SELECT string_agg(x, ', ') INTO bad
    FROM unnest(ARRAY['iam_v2.apply_adjustment(uuid,text,text,uuid,text)',
                      'iam_v2.ingest_sample(uuid,bigint,bigint,bigint,integer)']) x
   WHERE to_regprocedure(x) IS NOT NULL
     AND (has_function_privilege('public', x, 'EXECUTE')
          OR EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname LIKE 'svc\_%' AND has_function_privilege(r.rolname, x, 'EXECUTE')));
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'GATE-P BLOCKER (B-7): % is executable by PUBLIC or a runtime role', bad;
  END IF;

  -- 6. The PMS settings writers and the snapshot-case disposer belong to the console alone.
  SELECT string_agg(format('%s %s', r.rolname, x), ', ') INTO bad
    FROM pg_roles r CROSS JOIN unnest(ARRAY[
           'iam_v2.pms_connection_settings_set(uuid,uuid,uuid,text,text,integer,integer,integer,integer,integer)',
           'iam_v2.pms_reconciliation_settings_set(uuid,uuid,integer,integer,text,text,integer,integer)',
           'iam_v2.pms_dispose_snapshot_cases(uuid,uuid,uuid,text,text)']) x
   WHERE r.rolname LIKE 'svc\_%' AND r.rolname <> 'svc_edged' AND to_regprocedure(x) IS NOT NULL
     AND has_function_privilege(r.rolname, x, 'EXECUTE');
  IF bad IS NOT NULL THEN
    RAISE EXCEPTION 'GATE-P BLOCKER (B-7): PMS settings writer reachable by %', bad;
  END IF;

  -- 7. Half two, as for D32: the narrowing must not have taken away what the product runs on.
  FOREACH f IN ARRAY ARRAY[
      'svc_scd|iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)',
      'svc_scd|iam_v2.authorize_entitlement_device(uuid,uuid,timestamptz)',
      'svc_netd|iam_v2.activate_session_enforcement(uuid,uuid,uuid,text,integer,bigint)',
      'svc_netd|iam_v2.end_session_enforcement(uuid,uuid,uuid,text)',
      'svc_acctd|iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,integer,bigint,bigint,bigint,timestamptz)',
      'svc_acctd|iam_v2.p6_expire_entitlement(uuid)',
      'svc_pmsd|iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)'] LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = split_part(f, '|', 1))
       AND to_regprocedure(split_part(f, '|', 2)) IS NOT NULL
       AND NOT has_function_privilege(split_part(f, '|', 1), split_part(f, '|', 2), 'EXECUTE') THEN
      RAISE EXCEPTION 'GATE-P BLOCKER (B-7): % lost EXECUTE on %, which its code calls',
        split_part(f, '|', 1), split_part(f, '|', 2);
    END IF;
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd')
     AND NOT has_column_privilege('svc_scd', 'iam_v2.entitlements', 'status', 'UPDATE') THEN
    RAISE EXCEPTION 'GATE-P BLOCKER (B-7): svc_scd cannot lock an entitlement row (UPDATE (status)), so admission fails';
  END IF;
END $b7$;

-- CARD PAYMENT LOGIN ROLES (migration 0096). Each holds exactly one group role and nothing else: svc_payment runs
-- checkouts and the paid grant as sc_payment_runtime; svc_payment_outcome applies provider-verified outcomes as
-- sc_payment_outcome. scd opens one pool per role. svc_scd itself stays OUT of both, so the process-wide scd
-- credential can neither move money nor declare it moved.
DO $pay$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_payment') AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_payment_runtime') THEN
    EXECUTE 'GRANT sc_payment_runtime TO svc_payment';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_payment_outcome') AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_payment_outcome') THEN
    EXECUTE 'GRANT sc_payment_outcome TO svc_payment_outcome';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_posting') AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_posting_runtime') THEN
    EXECUTE 'GRANT sc_posting_runtime TO svc_posting';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_payment_runtime')
     AND (pg_has_role('svc_scd', 'sc_payment_runtime', 'MEMBER') OR pg_has_role('svc_scd', 'sc_payment_outcome', 'MEMBER')) THEN
    RAISE EXCEPTION 'GATE-P BLOCKER: svc_scd must not be a member of a payment role';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_payment') AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sc_payment_outcome')
     AND pg_has_role('svc_payment', 'sc_payment_outcome', 'MEMBER') THEN
    RAISE EXCEPTION 'GATE-P BLOCKER: the payment runtime must not hold the outcome authority';
  END IF;
END $pay$;

COMMIT;
