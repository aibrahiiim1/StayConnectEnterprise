-- 0064: onegate.work is retired (Product Owner, 2026-11-01). Central's portal name base becomes
-- onegate.getsemantics.com: appliance hostnames <label>.gw.onegate.getsemantics.com, hotel-domain challenge
-- delegates <x>.acme.onegate.getsemantics.com, and the shared Client Portal name portal.onegate.getsemantics.com
-- lies under the base (its challenge is answered directly). The Cloudflare zone is getsemantics.com; ctrlapi finds
-- it as the zone enclosing the base and writes nothing outside the base.
--
-- The move is made only when nothing is published under the old domain: no DNS record Central created there and no
-- valid certificate for one of its names (both would need the retired domain's credentials to clean up). Then each
-- live hostname keeps its LABEL -- the appliance's portal identity -- and moves to the new base; open orders for an
-- old name are cancelled (the appliance orders again for its new name); every move is recorded as an event. A
-- released hostname keeps its old name: it is history. Otherwise the database is left as it is and a NOTICE says why.
BEGIN;

ALTER TABLE portal_tls_settings ALTER COLUMN zone_name SET DEFAULT 'onegate.getsemantics.com';

DO $m0064$
DECLARE
  old_zone constant text := 'onegate.work';
  new_zone constant text := 'onegate.getsemantics.com';
  published int;
  certs int;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM portal_tls_settings WHERE id AND zone_name = old_zone) THEN
    RETURN;
  END IF;
  SELECT count(*) INTO published FROM portal_hostnames WHERE dns_record_id IS NOT NULL;
  SELECT count(*) INTO certs FROM portal_certificate_orders
   WHERE issued_at IS NOT NULL AND revoked_at IS NULL AND not_after > now()
     AND (hostname = old_zone OR hostname LIKE '%.' || old_zone);
  IF published > 0 OR certs > 0 THEN
    RAISE NOTICE 'portal names stay under %: % published record(s) and % valid certificate(s) exist there; move them by hand',
      old_zone, published, certs;
    RETURN;
  END IF;

  INSERT INTO portal_tls_events (appliance_id, hostname, kind, actor, detail)
  SELECT appliance_id, label || '.gw.' || new_zone, 'HOSTNAME_ALLOCATED', 'migration 0064',
         jsonb_build_object('label', label, 'moved_from', fqdn, 'reason', old_zone || ' retired')
    FROM portal_hostnames WHERE released_at IS NULL AND fqdn LIKE '%.' || old_zone;

  UPDATE portal_certificate_orders
     SET status = 'FAILED', error = 'cancelled: ' || old_zone || ' retired; the appliance orders again for its new name'
   WHERE status IN ('QUEUED','PROCESSING') AND (hostname = old_zone OR hostname LIKE '%.' || old_zone);

  UPDATE portal_hostnames
     SET fqdn = label || '.gw.' || new_zone, dns_error = NULL, dns_synced_ip = NULL, dns_synced_at = NULL
   WHERE released_at IS NULL AND fqdn LIKE '%.' || old_zone;

  UPDATE portal_tls_settings
     SET zone_name = new_zone, zone_id = NULL, caa_applied_at = NULL, updated_at = now(), updated_by = 'migration 0064'
   WHERE id;

  INSERT INTO portal_tls_events (hostname, kind, actor, detail)
  VALUES (new_zone, 'SETTINGS_CHANGED', 'migration 0064',
          jsonb_build_object('zone_name', jsonb_build_object('from', old_zone, 'to', new_zone), 'reason', old_zone || ' retired'));
END $m0064$;

COMMENT ON TABLE portal_hostnames IS
  'The permanent portal hostname of each appliance (<label>.gw.<zone_name>, onegate.getsemantics.com since 0064) and '
  'the private portal address its public A record points at. released_at is set when the appliance is decommissioned; '
  'a released label is never reused.';

COMMIT;
