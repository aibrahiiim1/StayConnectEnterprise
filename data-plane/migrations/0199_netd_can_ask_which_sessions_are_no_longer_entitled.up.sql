-- 0199 -- NETD CAN ASK WHICH SESSIONS ARE NO LONGER ENTITLED (outage continuity, revocation channel B).
--
-- While acctd is away, netd's continuity governor keeps the authorization of sessions the last plan covered
-- (docs/architecture/ONEGATE_OUTAGE_CONTINUITY.md). A revocation made through the database by anything other than a staff
-- disconnect (a checkout, an entitlement suspended or terminated, a device removed, a session ended by an operator) would
-- otherwise go unseen until acctd returned. netd therefore asks the database, every few seconds, which of the sessions it holds
-- are no longer entitled.
--
-- The predicate is the one acctd's plan derivation uses (internal/enforce PlanForSite "entitled"), repeated here and
-- nowhere else; a session the database does not know in this tenant and site counts as NOT entitled (fail closed). The
-- function is read-only, returns only the ids it was given, and is SECURITY DEFINER so netd needs no table grant beyond the
-- one it already has. It does NOT report a spent data allowance: usage is counted by acctd, which is the thing that is away,
-- and netd meters bytes itself from the tc counters.

BEGIN;

CREATE OR REPLACE FUNCTION iam_v2.continuity_not_entitled(p_tenant uuid, p_site uuid, p_sessions uuid[])
RETURNS SETOF uuid
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = iam_v2, pg_temp
AS $$
  SELECT i.id
    FROM unnest(p_sessions) AS i(id)
    LEFT JOIN iam_v2.sessions s ON s.id = i.id AND s.tenant_id = p_tenant AND s.site_id = p_site
    LEFT JOIN iam_v2.entitlements e ON e.id = s.entitlement_id
   WHERE NOT (s.id IS NOT NULL
          AND s.state IN ('active','PENDING_ENFORCEMENT') AND s.ended IS NULL
          AND e.status = 'ACTIVE'
          AND (e.window_ends_at IS NULL OR e.window_ends_at > now()));
$$;

COMMENT ON FUNCTION iam_v2.continuity_not_entitled(uuid, uuid, uuid[]) IS
  'Which of these sessions is no longer entitled (0199): ended, not active, entitlement not ACTIVE, window past, or unknown in this '
  'tenant and site. Mirrors the entitled predicate of the plan derivation. Read-only; used by netd outage continuity.';

REVOKE ALL ON FUNCTION iam_v2.continuity_not_entitled(uuid, uuid, uuid[]) FROM PUBLIC;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_netd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.continuity_not_entitled(uuid, uuid, uuid[]) TO svc_netd;
  END IF;
END $g$;

COMMIT;
