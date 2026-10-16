"use client";

// "ROOM-CHARGE POSTING IS LIVE ON THIS APPLIANCE" (Product-Owner decision D-E).
//
// D47 authorises real room-charge posting on the PRE-LIVE appliance, and that stays. What was missing is that the
// console never said so: an operator testing a package, a PMS connection or the room charge screen could not tell
// that a client choosing "charge to room" creates a real charge on a guest's reservation in the PMS. The notice is
// shown wherever posting is in effect -- the room_charge module is EFFECTIVE (licensed, switched on and ready,
// which includes the appliance's transmit switch) -- and nowhere else.

import { Callout } from "@/components/ui/error-banner";
import { moduleEffective, useCapabilities } from "@/lib/capabilities";

export function RoomChargeLiveNotice({ className }: { className?: string }) {
  const caps = useCapabilities();
  if (!moduleEffective(caps, "room_charge")) return null;
  return (
    <div className={className} data-testid="room-charge-live-notice">
      <Callout tone="warning" title="Room-charge posting is live on this appliance">
        A client who chooses charge to room creates a real charge on the guest&rsquo;s reservation in the PMS.
        Test with a package and a stay you are allowed to charge, and reverse any test charge in the PMS.
      </Callout>
    </div>
  );
}
