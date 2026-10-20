-- Guest data rights: look up, export and erase one guest (migration 0128; GDPR remediation plan C3 and C4,
-- Product-Owner decisions D-C3 and D-C4). Idempotent; included by gatep-grants.sql after its revoke, so a Gate-P
-- reconcile reproduces exactly what the migration granted.
--
--   svc_edged  runs the lookup, the export and the erasure through their definer functions (who may is edged's
--              guest-data-rights key: site_admin and the IT manager, password and reason, audited), and reads
--              the erasure record. No table privilege on guest data is added: the scope, the export records and
--              the iam_v2 half of the erasure are granted to nobody.
--
-- GUARDED on the migration being present, like every file granting on 0116+ objects (audit B-20).
DO $g$
BEGIN
  IF to_regprocedure('public.guest_data_erase(uuid,uuid,text,uuid,text,text)') IS NULL THEN
    RAISE NOTICE 'migration 0128 is not applied; guest-data-rights grants skipped';
    RETURN;
  END IF;
  GRANT EXECUTE ON FUNCTION iam_v2.guest_data_find(uuid,uuid,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.guest_data_export(uuid,uuid,text,uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.guest_data_erase(uuid,uuid,text,uuid,text,text) TO svc_edged;
  GRANT SELECT ON iam_v2.guest_data_erasures TO svc_edged;
  -- Migration 0144: edged refuses an export reason that names the guest before it runs the export.
  IF to_regprocedure('public.guest_data_reason_check(uuid,uuid,text,uuid,text)') IS NOT NULL THEN
    GRANT EXECUTE ON FUNCTION public.guest_data_reason_check(uuid,uuid,text,uuid,text) TO svc_edged;
  END IF;
  -- Migration 0145: the guest scope (iam_v2_owner's definer) reads which one-time codes went to the person's own
  -- addresses, to tie a verification access or a code binding to a person by the address, not the device. Three
  -- columns, for that role only.
  IF to_regclass('public.auth_otps') IS NOT NULL AND to_regprocedure('iam_v2.device_exclusive_to(uuid,uuid[],uuid[],uuid[],uuid[])') IS NOT NULL
     AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    GRANT SELECT (id, tenant_id, destination) ON public.auth_otps TO iam_v2_owner;
  END IF;
END $g$;

-- Migration 0146 (assisted guest data requests): edged runs the workflow through definer functions only; no table
-- privilege is added. iam_v2_owner (the definer) reads and clears the address of a social sign-in redirect, which is
-- a public table: the columns it needs, no more. Guarded like the rest.
DO $g$
BEGIN
  IF to_regprocedure('public.assisted_request_erase(uuid,uuid,uuid,text,text,text,text,jsonb)') IS NULL THEN
    RAISE NOTICE 'migration 0146 is not applied; assisted guest data request grants skipped';
    RETURN;
  END IF;
  GRANT EXECUTE ON FUNCTION iam_v2.assisted_request_get(uuid,uuid,uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION iam_v2.assisted_request_list(uuid,uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION iam_v2.assisted_request_review_json(uuid,uuid,uuid) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.assisted_reason_check(uuid,uuid,jsonb,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.assisted_request_reason_check(uuid,uuid,uuid,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.assisted_request_open(uuid,uuid,text,text,text[],timestamptz,timestamptz,jsonb,text,text,text,boolean,boolean,boolean,boolean,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.assisted_request_close(uuid,uuid,uuid,text,text,text) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.assisted_request_export(uuid,uuid,uuid,text,text,text,text,jsonb) TO svc_edged;
  GRANT EXECUTE ON FUNCTION public.assisted_request_erase(uuid,uuid,uuid,text,text,text,text,jsonb) TO svc_edged;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    GRANT SELECT (tenant_id, state, client_ip, client_mac, user_agent, provider, created_at, consumed_at) ON public.social_oauth_states TO iam_v2_owner;
    GRANT UPDATE (client_ip, client_mac, user_agent) ON public.social_oauth_states TO iam_v2_owner;
  END IF;
END $g$;

-- Migration 0147 (an erasure survives a restore; nothing identifying is left behind): scd copies the erasure journal to
-- a file next to the backups and replays it after a restore and every hour. Nothing else is granted: the journal table
-- and the scrub registry have no grant to a runtime role.
DO $g$
BEGIN
  IF to_regprocedure('public.erasure_journal_replay(jsonb)') IS NULL THEN
    RAISE NOTICE 'migration 0147 is not applied; erasure journal grants skipped';
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION public.erasure_journal_export(uuid,uuid) TO svc_scd;
    GRANT EXECUTE ON FUNCTION public.erasure_journal_replay(jsonb) TO svc_scd;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.request_register_open(uuid,uuid,text,text,date,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.request_register_extend(uuid,uuid,uuid,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.request_register_answer(uuid,uuid,uuid,text,text,text,text,text,text) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.request_register_list(uuid,uuid) TO svc_edged;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    GRANT SELECT (code_verifier, nonce) ON public.social_oauth_states TO iam_v2_owner;
    GRANT UPDATE (code_verifier, nonce) ON public.social_oauth_states TO iam_v2_owner;
    GRANT SELECT ON public.dhcp_reservations TO iam_v2_owner;
  END IF;
END $g$;

-- Migration 0148 (the erasure ledger is kept in two places and a restore reapplies it): edged confirms the entries an erasure
-- appended; scd reconciles the ledger, applies it to a restored database, appends the daily checkpoint and keeps the secondary
-- storage setting. Nothing reads the ledger tables directly: only these functions.
DO $g$
BEGIN
  IF to_regprocedure('public.ledger_apply(jsonb,boolean)') IS NULL THEN
    RAISE NOTICE 'migration 0148 is not applied; erasure ledger grants skipped';
    RETURN;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.ledger_pending() TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.ledger_confirm(text[]) TO svc_edged;
    GRANT EXECUTE ON FUNCTION iam_v2.ledger_head() TO svc_edged;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.ledger_append(uuid,uuid,text,jsonb) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.ledger_pending() TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.ledger_confirm(text[]) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.ledger_head() TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.ledger_export(bigint) TO svc_scd;
    GRANT EXECUTE ON FUNCTION iam_v2.ledger_verify() TO svc_scd;
    GRANT EXECUTE ON FUNCTION public.ledger_apply(jsonb,boolean) TO svc_scd;
    GRANT EXECUTE ON FUNCTION public.ledger_destination_get() TO svc_scd;
    GRANT EXECUTE ON FUNCTION public.ledger_destination_secret() TO svc_scd;
    GRANT EXECUTE ON FUNCTION public.ledger_destination_set(text,jsonb,bytea,bytea,text,integer,text) TO svc_scd;
    GRANT EXECUTE ON FUNCTION public.ledger_destination_note(boolean,text) TO svc_scd;
  END IF;
END $g$;

