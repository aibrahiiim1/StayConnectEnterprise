-- Down for 0153. Snapshots and review items are lost. A SETTINGS change still waiting to be applied must be applied or cancelled first: it is removed here, so
-- a restore staged but not applied is simply forgotten (nothing had changed).

BEGIN;

DROP TABLE IF EXISTS iam_v2.network_review_items;
DROP TABLE IF EXISTS iam_v2.network_layer_snapshots;
DELETE FROM iam_v2.guest_network_changes WHERE kind = 'SETTINGS' AND state IN ('PENDING','CANCELLED','REVERTED');
ALTER TABLE iam_v2.guest_network_changes DROP COLUMN IF EXISTS before;
ALTER TABLE iam_v2.guest_network_changes DROP COLUMN IF EXISTS target;
ALTER TABLE iam_v2.guest_network_changes DROP CONSTRAINT IF EXISTS gnc_kind;
ALTER TABLE iam_v2.guest_network_changes ADD CONSTRAINT gnc_kind CHECK (kind IN ('ENABLE','DISABLE'));

COMMIT;
