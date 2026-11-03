-- ONE-TIME driver for onetime-fullstay-grant.sql. Run with psql as the database owner:
--   psql -v rooms='1101,1102,...' -v iface_label='CACsite2' -v free_code='freeinternet' -v target_code='fullstay' \
--        -v operator='<operator uuid>' -v cutoff='2026-11-03' -v apply='no' -f onetime-fullstay-run.sql
-- apply='no' prints the match only; apply='yes' grants, one stay per transaction (a refused stay is reported, the rest go on).
-- The room list is passed in, never committed: rooms are guest data.
\set ON_ERROR_STOP on
\ir onetime-fullstay-grant.sql

CREATE TEMP TABLE m AS
WITH rooms(room) AS (SELECT DISTINCT btrim(r) FROM unnest(string_to_array(:'rooms', ',')) r),
     ifc AS (SELECT id, tenant_id, site_id FROM iam_v2.pms_interfaces WHERE display_label = :'iface_label'),
     ih AS (SELECT r.room, st.id stay, st.arrival::date arr
              FROM rooms r LEFT JOIN iam_v2.stays st ON st.normalized_room_number = r.room AND st.status = 'IN_HOUSE'
                                                     AND st.pms_interface_id = (SELECT id FROM ifc)),
     cnt AS (SELECT room, count(stay) n FROM ih GROUP BY room),
     live AS (SELECT e.stay_id, p.code, e.status FROM iam_v2.entitlements e
                JOIN iam_v2.internet_package_revisions r ON r.id = e.package_revision_id
                JOIN iam_v2.internet_packages p ON p.id = r.package_id
               WHERE e.status IN ('PENDING','ACTIVE','SUSPENDED'))
SELECT ih.room, ih.stay, cnt.n,
       CASE WHEN cnt.n = 0 THEN 'SKIP_NO_STAY'
            WHEN cnt.n > 1 THEN 'SKIP_SEVERAL_STAYS'
            WHEN ih.arr >= :'cutoff'::date THEN 'SKIP_ARRIVED_AFTER_CUTOFF'
            WHEN l.code = :'target_code' THEN 'SKIP_ALREADY_TARGET'
            WHEN l.code IS NULL THEN 'GRANT'
            WHEN l.code = :'free_code' AND l.status = 'ACTIVE' THEN 'SWITCH'
            ELSE 'SKIP_OTHER_PACKAGE' END AS decision
  FROM ih JOIN cnt USING (room) LEFT JOIN live l ON l.stay_id = ih.stay;

SELECT decision, count(DISTINCT room) AS rooms FROM m GROUP BY 1 ORDER BY 1;
SELECT decision, string_agg(DISTINCT room, ' ' ORDER BY room) AS skipped_rooms FROM m WHERE decision LIKE 'SKIP%' GROUP BY 1;

CREATE TEMP TABLE results (room text, stay uuid, outcome text, old_entitlement uuid, new_entitlement uuid, purchase uuid,
                           devices int, sessions int);
\set ON_ERROR_STOP off
SELECT format($q$INSERT INTO results SELECT %L, %L, g.* FROM pg_temp.onetime_fullstay_grant(%L, %L, %L, %L, %L, %L, %L,
                 'paid Full Stay on the previous system (one-time migration 2026-11-03)') g$q$,
              m.room, m.stay, i.tenant_id, i.site_id, m.stay, i.id,
              (SELECT id FROM iam_v2.internet_packages WHERE code = :'free_code' AND tenant_id = i.tenant_id AND site_id = i.site_id),
              (SELECT current_revision_id FROM iam_v2.internet_packages WHERE code = :'target_code' AND tenant_id = i.tenant_id AND site_id = i.site_id),
              :'operator')
  FROM m, iam_v2.pms_interfaces i
 WHERE i.display_label = :'iface_label' AND m.decision IN ('GRANT','SWITCH') AND :'apply' = 'yes'
 ORDER BY m.room
\gexec
\set ON_ERROR_STOP on
SELECT outcome, count(*) AS stays, sum(devices) AS devices_moved, sum(sessions) AS sessions_moved FROM results GROUP BY 1 ORDER BY 1;
SELECT m.room AS not_granted FROM m LEFT JOIN results r USING (room)
 WHERE m.decision IN ('GRANT','SWITCH') AND r.room IS NULL AND :'apply' = 'yes' ORDER BY 1;
