-- PMS REVIEW BACKLOG -- a read-only classification of the messages waiting in MANUAL_REVIEW (audit H-15,
-- Product-Owner decision D-D: "counts-only investigation").
--
-- COUNTS ONLY. No reservation number, room, guest name, payload or event id is selected: every output column
-- is a code, a month, a yes/no class or a number, so the result can be pasted into a ticket or a PR.
-- READ ONLY. The whole script runs in a READ ONLY transaction; it writes, decides and expires nothing.
--
-- Run on the appliance (site database), for example:
--   docker exec -i stayconnect-pg psql -U postgres -d stayconnect_site -v ON_ERROR_STOP=1 \
--     < scripts/pms-review-backlog-report.sql
--
-- Re-matchable means: the message names a reservation and a stay on the same PMS connection carries it now, or
-- it names no reservation but a room, and a stay that was in that room when the message arrived exists -- the
-- same rule iam_v2.pms_review_link_candidates (migration 0123) uses to offer stays for "Link to stay".

BEGIN TRANSACTION READ ONLY;

\echo '== 1. waiting messages, decided vs undecided =='
SELECT count(*)                                                     AS in_review,
       count(*) FILTER (WHERE r.stay_event_id IS NULL)              AS undecided,
       count(*) FILTER (WHERE r.stay_event_id IS NOT NULL)          AS decided
  FROM iam_v2.stay_events e
  LEFT JOIN iam_v2.pms_case_resolutions r ON r.stay_event_id = e.id
 WHERE e.processing_status = 'MANUAL_REVIEW';

\echo '== 2. decided messages by disposition =='
SELECT r.disposition, count(*) AS messages
  FROM iam_v2.pms_case_resolutions r
  JOIN iam_v2.stay_events e ON e.id = r.stay_event_id
 WHERE e.processing_status = 'MANUAL_REVIEW'
 GROUP BY r.disposition
 ORDER BY 2 DESC;

\echo '== 3. undecided messages by review code, event type and admission =='
SELECT COALESCE(e.review_code, '(none)') AS review_code, e.event_type, e.admission_kind, count(*) AS messages
  FROM iam_v2.stay_events e
 WHERE e.processing_status = 'MANUAL_REVIEW'
   AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions r WHERE r.stay_event_id = e.id)
 GROUP BY 1, 2, 3
 ORDER BY 4 DESC;

\echo '== 4. undecided messages by month received and review code =='
SELECT to_char(date_trunc('month', e.received_at), 'YYYY-MM') AS month,
       COALESCE(e.review_code, '(none)') AS review_code, count(*) AS messages
  FROM iam_v2.stay_events e
 WHERE e.processing_status = 'MANUAL_REVIEW'
   AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions r WHERE r.stay_event_id = e.id)
 GROUP BY 1, 2
 ORDER BY 1, 3 DESC;

\echo '== 5. undecided messages by re-matchability against the current stays =='
WITH waiting AS (
    SELECT e.id, e.tenant_id, e.site_id, e.pms_interface_id, e.received_at, e.review_code,
           btrim(COALESCE(e.payload->>'reservation','')) AS res,
           upper(btrim(COALESCE(e.payload->>'room','')))  AS room
      FROM iam_v2.stay_events e
     WHERE e.processing_status = 'MANUAL_REVIEW'
       AND NOT EXISTS (SELECT 1 FROM iam_v2.pms_case_resolutions r WHERE r.stay_event_id = e.id)
), classed AS (
    SELECT w.review_code,
           CASE
             WHEN w.res <> '' AND EXISTS (
                    SELECT 1 FROM iam_v2.stays s
                     WHERE s.tenant_id = w.tenant_id AND s.site_id = w.site_id
                       AND s.pms_interface_id = w.pms_interface_id
                       AND btrim(s.external_reservation_id) = w.res)
               THEN 'BY_RESERVATION'
             WHEN w.res = '' AND w.room <> '' AND EXISTS (
                    SELECT 1 FROM iam_v2.stays s
                     WHERE s.tenant_id = w.tenant_id AND s.site_id = w.site_id
                       AND s.pms_interface_id = w.pms_interface_id
                       AND s.normalized_room_number = w.room
                       AND (s.arrival IS NULL OR s.arrival <= w.received_at)
                       AND (s.effective_checkout_at IS NULL OR s.effective_checkout_at >= w.received_at))
               THEN 'BY_ROOM'
             WHEN w.res <> '' THEN 'RESERVATION_NOT_IN_MIRROR'
             WHEN w.room <> '' THEN 'ROOM_WITHOUT_MATCHING_STAY'
             ELSE 'NO_RESERVATION_NO_ROOM'
           END AS matchability
      FROM waiting w
)
SELECT matchability, COALESCE(review_code, '(none)') AS review_code, count(*) AS messages
  FROM classed
 GROUP BY 1, 2
 ORDER BY 1, 3 DESC;

\echo '== 6. the expiry setting (off by default; applies only to messages received after it was switched on) =='
SELECT s.enabled, s.expire_after_days, s.applies_from IS NOT NULL AS has_start, s.config_version
  FROM iam_v2.pms_review_expiry_settings s;

ROLLBACK;
