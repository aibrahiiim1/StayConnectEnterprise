-- 0130 down -- sign-ins are no longer stamped with the privacy notice; KEEP what was recorded.
--
-- The trigger and its functions go, and the guest export is restored to its 0128 form. Kept: the notice versions
-- and the stamps on existing auth contexts -- the record of what guests were shown, which a rollback must not
-- lose.

BEGIN;

DROP TRIGGER IF EXISTS p9_auth_context_privacy_notice ON iam_v2.auth_contexts;
DROP FUNCTION IF EXISTS public.stamp_privacy_notice();
DROP FUNCTION IF EXISTS public.privacy_notice_in_force(uuid);

CREATE OR REPLACE FUNCTION public.guest_data_export(p_tenant uuid, p_site uuid, p_kind text, p_id uuid)
    RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public', 'pg_temp'
    AS $$
DECLARE v jsonb; v_ids text[]; v_vals text[];
BEGIN
  v := iam_v2.guest_data_export_records(p_tenant, p_site, p_kind, p_id);
  SELECT COALESCE(array_agg(DISTINCT o_id::text), '{}'),
         COALESCE(array_agg(DISTINCT lower(o_value)) FILTER (WHERE o_kind IN ('account', 'identity') AND o_value IS NOT NULL), '{}')
    INTO v_ids, v_vals FROM iam_v2.guest_data_scope(p_tenant, p_site, p_kind, p_id);
  RETURN jsonb_build_object('format', 'stayconnect.guest-data-export/1', 'exported_at', now()) || v || jsonb_build_object(
    'verification_codes', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'channel', o.channel, 'sent_to', o.destination, 'sent_at', o.issued_at, 'used_at', o.consumed_at)
        ORDER BY o.issued_at), '[]'::jsonb)
      FROM auth_otps o WHERE o.tenant_id = p_tenant AND lower(o.destination) = ANY (v_vals)),
    'activity_log', (SELECT COALESCE(jsonb_agg(jsonb_build_object('at', l.ts, 'action', l.action) ORDER BY l.ts), '[]'::jsonb)
      FROM audit_log l WHERE l.tenant_id = p_tenant AND l.target_id = ANY (v_ids)));
END $$;
COMMENT ON FUNCTION public.guest_data_export(uuid, uuid, text, uuid) IS
  'One guest''s data as a JSON document (0128, GDPR plan C4, Art. 15/20): client accounts, identities and sign-in '
  'methods, hotel stays, internet access and purchases, devices, sessions, sign-in attempts, verification codes '
  'sent and the activity log entries about them. Changes nothing.';

COMMIT;
