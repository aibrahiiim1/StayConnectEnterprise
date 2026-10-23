"use client";

// Show the notice only when room-charge posting can create real PMS charges on guest reservations.

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
