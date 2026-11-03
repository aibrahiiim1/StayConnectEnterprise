"use client";

import { Notice } from "@/components/ui/error-banner";
import { InfoHint } from "@/components/ui/quiet";
import { moduleEffective, useCapabilities } from "@/lib/capabilities";

export function RoomChargeLiveNotice({ className }: { className?: string }) {
  const caps = useCapabilities();
  if (!moduleEffective(caps, "room_charge")) return null;
  return (
    <div className={className} data-testid="room-charge-live-notice">
      <Notice tone="warning" title="Room-charge posting is live on this appliance">
        Charge to room creates a real charge on the guest&rsquo;s reservation.
        <InfoHint className="ms-1">Test with a package and a stay you are allowed to charge, and reverse any test charge in the PMS.</InfoHint>
      </Notice>
    </div>
  );
}
