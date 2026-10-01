-- 0106 down — drop the lineage view.
--
-- Dropping it restores the defect it closes: a pinned package revision naming a replaced client network stops
-- matching the guests on its successor, and an unused printed voucher pinned to such a revision stops redeeming.
-- Nothing is lost, because the view holds no data of its own -- the replacement chain it reads lives in
-- iam_v2.guest_network_replacements (0105) and is untouched here.

BEGIN;

DROP VIEW IF EXISTS iam_v2.guest_network_lineage;

COMMIT;
