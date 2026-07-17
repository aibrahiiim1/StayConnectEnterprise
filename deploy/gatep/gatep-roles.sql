-- Gate P — runtime role creation (site DB). Idempotent. NO passwords here.
-- Passwords are set separately by deploy/gatep/gatep-set-passwords.sh, which computes a
-- SCRAM-SHA-256 verifier on the appliance so cleartext never appears in any SQL statement,
-- argv, rendered file, or log. This file contains NO secret and NO plaintext token.
--
-- Every runtime role: LOGIN, NOSUPERUSER, NOCREATEDB, NOCREATEROLE, NOBYPASSRLS, NOREPLICATION,
-- no ownership, connection + per-parameter timeout guards. ZERO iam_v2 privileges (never granted
-- here or in gatep-grants.sql).

\set ON_ERROR_STOP on

DO $$ BEGIN CREATE ROLE svc_scd   LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE svc_edged LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE svc_acctd LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION; EXCEPTION WHEN duplicate_object THEN NULL; END $$;
DO $$ BEGIN CREATE ROLE svc_netd  LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION; EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- Re-assert attributes (idempotent hardening).
ALTER ROLE svc_scd   NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION CONNECTION LIMIT 20;
ALTER ROLE svc_edged NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION CONNECTION LIMIT 20;
ALTER ROLE svc_acctd NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION CONNECTION LIMIT 10;
ALTER ROLE svc_netd  NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS NOREPLICATION CONNECTION LIMIT 10;

-- Per-parameter guards — one SET per statement (chained SET in a single ALTER ROLE is invalid).
ALTER ROLE svc_scd   SET statement_timeout = '30s';
ALTER ROLE svc_scd   SET lock_timeout = '5s';
ALTER ROLE svc_scd   SET idle_in_transaction_session_timeout = '30s';
ALTER ROLE svc_edged SET statement_timeout = '60s';
ALTER ROLE svc_edged SET lock_timeout = '5s';
ALTER ROLE svc_edged SET idle_in_transaction_session_timeout = '60s';
ALTER ROLE svc_acctd SET statement_timeout = '30s';
ALTER ROLE svc_acctd SET lock_timeout = '5s';
ALTER ROLE svc_acctd SET idle_in_transaction_session_timeout = '30s';
ALTER ROLE svc_netd  SET statement_timeout = '30s';
ALTER ROLE svc_netd  SET lock_timeout = '5s';
ALTER ROLE svc_netd  SET idle_in_transaction_session_timeout = '30s';
