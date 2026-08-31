-- HOW A PLAN'S RATE IS SHARED WHEN AN ACCOUNT HAS MORE THAN ONE DEVICE.
--
-- Until now a service plan revision stated one down/up rate and every authorized device got that rate to
-- itself. That is one legitimate commercial product and not the only one: a property selling "20 Mbit for the
-- room" means twenty megabits for the ROOM, not twenty per phone, and there was no way to express it.
--
-- So the revision now says which it is:
--
--   PER_DEVICE  every authorized device may use the configured rate independently. Today's behaviour, and the
--               default, so every revision written before this column existed keeps the meaning it was sold
--               under. An immutable revision must never change what it promised.
--
--   SHARED      all active sessions under the Entitlement share ONE aggregate ceiling. Deliberately NOT a
--               division into fixed per-device slices: a guest with three devices, two of them idle, should
--               get the whole ceiling on the third. The edge expresses this as an HTB parent class at the
--               aggregate rate with each session as a child that may borrow up to it, so capacity follows
--               demand instead of being reserved and wasted.
--
-- WHY IT LIVES ON THE REVISION AND NOT ON THE ENTITLEMENT. The revision is the immutable thing a guest bought;
-- the entitlement points at one. Putting the mode on the entitlement would let it be edited under a guest who
-- purchased something else, which is exactly what pinning a revision exists to prevent.

BEGIN;

ALTER TABLE iam_v2.service_plan_revisions
  ADD COLUMN IF NOT EXISTS speed_allocation text NOT NULL DEFAULT 'PER_DEVICE';

-- A bounded set, checked in the database. The applier reads this value and builds kernel state from it, so an
-- unrecognised mode must be impossible rather than defaulted at the edge — a silent default would hand a
-- SHARED plan the per-device behaviour and overspend the property's capacity without anything failing.
ALTER TABLE iam_v2.service_plan_revisions
  DROP CONSTRAINT IF EXISTS spr_speed_allocation_check;
ALTER TABLE iam_v2.service_plan_revisions
  ADD CONSTRAINT spr_speed_allocation_check
  CHECK (speed_allocation IN ('PER_DEVICE','SHARED'));

COMMENT ON COLUMN iam_v2.service_plan_revisions.speed_allocation IS
  'PER_DEVICE (default): every authorized device may use down_kbps/up_kbps independently. SHARED: all active '
  'sessions under the Entitlement share one aggregate ceiling of down_kbps/up_kbps, allocated dynamically '
  'rather than divided into fixed per-device portions. Immutable with the revision it belongs to.';

COMMIT;
