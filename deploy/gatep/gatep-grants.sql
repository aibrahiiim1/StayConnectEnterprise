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
-- RECONCILER PREAMBLE — converge each svc_* role to EXACTLY the allowlist below.
-- Fail closed on unexpected ownership or membership; revoke everything first so a
-- re-run is idempotent and any pre-existing excess privilege is removed.
-- ===========================================================================
DO $$
DECLARE r text; n int; who text;
BEGIN
  FOREACH r IN ARRAY ARRAY['svc_scd','svc_edged','svc_acctd','svc_netd'] LOOP
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

-- ---------------------------------------------------------------------------
-- svc_scd : session / auth / credential / appliance-lifecycle
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO svc_scd;
GRANT SELECT,INSERT,UPDATE        ON public.sessions               TO svc_scd; -- start/reissue/reaper (upsert)
GRANT SELECT,INSERT,UPDATE        ON public.guests                 TO svc_scd; -- upsert (tenant,mac) ON CONFLICT
GRANT SELECT,UPDATE               ON public.guest_accounts         TO svc_scd; -- validate + lockout (INSERT=edged)
GRANT SELECT,UPDATE               ON public.vouchers               TO svc_scd; -- redeem state flip
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
GRANT SELECT                      ON public.ticket_templates       TO svc_scd;
GRANT INSERT                      ON public.audit_log              TO svc_scd; -- append-only
GRANT SELECT,INSERT,UPDATE        ON public.appliances             TO svc_scd; -- enrollment/claim
GRANT SELECT,INSERT,DELETE        ON public.sites                  TO svc_scd; -- local assignment provisioning
GRANT SELECT,INSERT              ON public.edge_executed_commands  TO svc_scd; -- command channel
GRANT SELECT,INSERT              ON public.edge_installed_updates  TO svc_scd; -- updates
GRANT SELECT,INSERT,UPDATE       ON public.edge_offline_packages   TO svc_scd; -- offline pkgs
GRANT SELECT,INSERT,UPDATE,DELETE ON public.auth_throttle_buckets   TO svc_scd; -- durable throttle (0007): read/increment/block/cleanup; no sequence (composite PK)
GRANT SELECT                      ON public.otp_hmac_key_generations TO svc_scd; -- OTP gen metadata READ only (0008); creation/rotation are operational/migration-only

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
GRANT SELECT,INSERT,UPDATE,DELETE ON public.guest_accounts             TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.guest_networks             TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.network_interfaces         TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.network_config_revisions   TO svc_edged;
GRANT SELECT,INSERT               ON public.network_apply_events        TO svc_edged;
GRANT SELECT,INSERT               ON public.network_health_checks       TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.notification_providers     TO svc_edged;
GRANT SELECT,INSERT,DELETE        ON public.operator_roles             TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.operators                  TO svc_edged;
GRANT SELECT                      ON public.payments                   TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.pms_providers              TO svc_edged;
GRANT SELECT                      ON public.sessions                   TO svc_edged; -- admin read
GRANT SELECT,INSERT,UPDATE,DELETE ON public.social_oauth_providers     TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.stripe_accounts            TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.sync_outbox                TO svc_edged;
GRANT SELECT,INSERT               ON public.sync_checkpoints           TO svc_edged;
GRANT SELECT,UPDATE               ON public.tenants                    TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.tenant_effective_limits    TO svc_edged;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.ticket_templates          TO svc_edged;
GRANT SELECT,INSERT               ON public.voucher_batches            TO svc_edged;
GRANT SELECT,INSERT,UPDATE        ON public.vouchers                   TO svc_edged;
GRANT SELECT,INSERT,DELETE        ON public.walled_garden_rules        TO svc_edged;

-- ---------------------------------------------------------------------------
-- svc_acctd : accounting (append usage; update session usage)
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO svc_acctd;
GRANT SELECT,UPDATE ON public.sessions           TO svc_acctd; -- usage/quota update
GRANT INSERT        ON public.accounting_records TO svc_acctd; -- append-only samples
GRANT SELECT        ON public.vouchers           TO svc_acctd; -- quota JOIN (acctd/main.go:303)
GRANT SELECT        ON public.ticket_templates   TO svc_acctd; -- quota JOIN (acctd/main.go:304)

-- ---------------------------------------------------------------------------
-- svc_netd : networking only (no credentials, no iam_v2)
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO svc_netd;
GRANT SELECT,INSERT,UPDATE ON public.network_config_revisions TO svc_netd;
GRANT SELECT,INSERT        ON public.network_apply_events     TO svc_netd;
GRANT SELECT,INSERT        ON public.network_health_checks    TO svc_netd;
GRANT SELECT,INSERT        ON public.network_interfaces       TO svc_netd;
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
      ('svc_scd','sessions'),('svc_scd','guests'),('svc_scd','auth_otps'),('svc_scd','social_oauth_states'),
      ('svc_scd','sync_outbox'),('svc_scd','sync_checkpoints'),('svc_scd','pms_attempts'),('svc_scd','audit_log'),
      ('svc_scd','tenants'),('svc_scd','tenant_effective_limits'),('svc_scd','appliances'),('svc_scd','sites'),
      ('svc_scd','edge_executed_commands'),('svc_scd','edge_installed_updates'),('svc_scd','edge_offline_packages'),
      ('svc_edged','appliance_boot_convergence'),('svc_edged','appliance_recovery_events'),('svc_edged','appliance_service_health'),
      ('svc_edged','audit_log'),('svc_edged','dhcp_pools'),('svc_edged','dhcp_reservations'),('svc_edged','guest_accounts'),
      ('svc_edged','guest_networks'),('svc_edged','network_interfaces'),('svc_edged','network_config_revisions'),
      ('svc_edged','network_apply_events'),('svc_edged','network_health_checks'),('svc_edged','notification_providers'),
      ('svc_edged','operator_roles'),('svc_edged','operators'),('svc_edged','pms_providers'),('svc_edged','social_oauth_providers'),
      ('svc_edged','stripe_accounts'),('svc_edged','sync_outbox'),('svc_edged','sync_checkpoints'),('svc_edged','tenant_effective_limits'),
      ('svc_edged','ticket_templates'),('svc_edged','voucher_batches'),('svc_edged','vouchers'),('svc_edged','walled_garden_rules'),
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
