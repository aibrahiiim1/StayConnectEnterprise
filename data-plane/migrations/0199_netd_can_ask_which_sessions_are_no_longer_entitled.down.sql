-- Removing the probe. WHAT THIS COSTS: netd's continuity governor can no longer see a database-side revocation while acctd is
-- away (staff disconnects, which netd is told directly, and spent byte budgets are unaffected); every probe then fails and
-- the governor keeps its sessions only until their window, hard end or budget. Set the continuity window to 0 first if that is not acceptable.
BEGIN;
DROP FUNCTION IF EXISTS iam_v2.continuity_not_entitled(uuid, uuid, uuid[]);
COMMIT;
