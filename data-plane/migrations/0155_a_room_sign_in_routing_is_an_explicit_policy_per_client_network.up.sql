-- 0155 — ROOM SIGN-IN ROUTING IS AN EXPLICIT POLICY OF EACH CLIENT NETWORK.
--
-- Until now a client network's PMS routing was whatever rows iam_v2.guest_network_pms_map happened to hold, and the only write path replaced the network's route
-- with exactly one interface. "No row" could mean a deliberately PMS-free voucher network or a network nobody had decided about, and the Phase 0 STRICT
-- Multi-PMS resolution (docs/architecture/StayConnect-IAM-Phase0-Contract.md section 8) had no way to be configured: the resolver already fans out across every
-- mapped interface, but nothing could map two.
--
-- Each client network now carries one explicit policy:
--   NONE          no PMS room sign-in on this network (vouchers, accounts and the other methods are unaffected);
--   DEDICATED     exactly one selected PMS Interface;
--   SHARED_STRICT an explicitly selected set of two or three independent PMS Interfaces, resolved with the STRICT contract (never first-responder, never inferred
--                 from a room number, never every active interface).
-- A network with no policy row is UNCONFIGURED: it offers room sign-in to nobody until an operator decides, which is the existing fail-closed behaviour.
--
-- The map table keeps holding WHICH interfaces; this table holds WHAT KIND of routing it is. A deferred constraint trigger keeps the two consistent whenever a
-- policy row exists, so a policy and its interfaces can never disagree. Rows written before this migration (a network with exactly one mapped interface) are
-- backfilled as DEDICATED; nothing else is inferred, and nothing is mapped, copied or merged by this migration.
--
-- The consistency check runs when a policy or a mapping is written, not when one is deleted: deleting a client network cascades its mapping rows, and that must
-- stay possible. The authentication path verifies the policy against the mapping on every decision and fails closed on a disagreement.
--
-- The table has no foreign key to public.guest_networks: live-site migrations apply as iam_v2_owner, which owns nothing in public (see 0105, 0151, 0154). The map
-- table keeps the cross-schema anchor.

BEGIN;

CREATE TABLE IF NOT EXISTS iam_v2.guest_network_pms_policy (
  guest_network_id uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  site_id          uuid NOT NULL,
  mode             text NOT NULL,
  configured_at    timestamptz NOT NULL DEFAULT now(),
  configured_by    text NOT NULL DEFAULT '',
  CONSTRAINT gnpp_mode CHECK (mode IN ('NONE','DEDICATED','SHARED_STRICT'))
);
CREATE INDEX IF NOT EXISTS gnpp_site ON iam_v2.guest_network_pms_policy (tenant_id, site_id);

CREATE OR REPLACE FUNCTION iam_v2.gnpp_consistent() RETURNS trigger
  LANGUAGE plpgsql AS $fn$
DECLARE
  net  uuid;
  m    text;
  n    int;
BEGIN
  net := NEW.guest_network_id;
  SELECT mode INTO m FROM iam_v2.guest_network_pms_policy WHERE guest_network_id = net;
  IF m IS NULL THEN
    RETURN NULL;
  END IF;
  SELECT count(*) INTO n FROM iam_v2.guest_network_pms_map WHERE guest_network_id = net;
  IF (m = 'NONE' AND n <> 0) OR (m = 'DEDICATED' AND n <> 1) OR (m = 'SHARED_STRICT' AND (n < 2 OR n > 3)) THEN
    RAISE EXCEPTION 'client network % has routing policy % but % mapped PMS interface(s)', net, m, n
      USING ERRCODE = '23514', CONSTRAINT = 'gnpp_consistent';
  END IF;
  RETURN NULL;
END;
$fn$;

DROP TRIGGER IF EXISTS gnpp_policy_consistent ON iam_v2.guest_network_pms_policy;
CREATE CONSTRAINT TRIGGER gnpp_policy_consistent AFTER INSERT OR UPDATE ON iam_v2.guest_network_pms_policy
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION iam_v2.gnpp_consistent();
DROP TRIGGER IF EXISTS gnpp_map_consistent ON iam_v2.guest_network_pms_map;
CREATE CONSTRAINT TRIGGER gnpp_map_consistent AFTER INSERT OR UPDATE ON iam_v2.guest_network_pms_map
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION iam_v2.gnpp_consistent();

DO $own$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'iam_v2_owner') THEN
    EXECUTE 'ALTER TABLE iam_v2.guest_network_pms_policy OWNER TO iam_v2_owner';
    EXECUTE 'ALTER FUNCTION iam_v2.gnpp_consistent() OWNER TO iam_v2_owner';
  END IF;
END $own$;

DO $g$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_edged') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON iam_v2.guest_network_pms_policy TO svc_edged;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT SELECT ON iam_v2.guest_network_pms_policy TO svc_scd;
  END IF;
END;
$g$;

-- The one routing that existed, kept as it was: a network with exactly one mapped interface is DEDICATED to it. Networks with no row, or with more rows than the
-- contract allows, get no policy and stay UNCONFIGURED (fail closed) until an operator decides.
INSERT INTO iam_v2.guest_network_pms_policy (guest_network_id, tenant_id, site_id, mode, configured_by)
SELECT m.guest_network_id, (array_agg(m.tenant_id))[1], (array_agg(m.site_id))[1], 'DEDICATED', 'migration 0155'
  FROM iam_v2.guest_network_pms_map m
 GROUP BY m.guest_network_id
HAVING count(*) = 1
ON CONFLICT (guest_network_id) DO NOTHING;

-- Can this interface authorise anybody right now? The feed half of the same predicate the authentication path uses (iam_v2.p3_feed_authorizes), asked about ONE
-- interface so a shared network can judge each of its candidates. scd needs it to answer STALE for a candidate whose mirror cannot be trusted instead of treating
-- the other candidates' answers as the whole story. No guest, Stay, reservation or credential is exposed.
CREATE OR REPLACE FUNCTION iam_v2.p3_interface_can_authorise(p_tenant uuid, p_site uuid, p_interface uuid)
  RETURNS boolean
  LANGUAGE sql STABLE SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $fn$
  SELECT COALESCE(bool_or(iam_v2.p3_feed_authorizes(pi.tenant_id, pi.site_id, pi.id, pi.current_revision_id, now())), false)
    FROM iam_v2.pms_interfaces pi
   WHERE pi.tenant_id = p_tenant AND pi.site_id = p_site AND pi.id = p_interface AND pi.lifecycle_state = 'ACTIVE';
$fn$;

REVOKE ALL ON FUNCTION iam_v2.p3_interface_can_authorise(uuid,uuid,uuid) FROM PUBLIC;
DO $gf$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_scd') THEN
    GRANT EXECUTE ON FUNCTION iam_v2.p3_interface_can_authorise(uuid,uuid,uuid) TO svc_scd;
  END IF;
END
$gf$;

COMMIT;
