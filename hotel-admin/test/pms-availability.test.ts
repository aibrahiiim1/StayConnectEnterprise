// The operator must be told when Room sign-in cannot serve a guest — and must NOT be told when it can.
//
// Both directions are defects. A missing notice means an operator watches a correctly configured, enabled
// method refuse every guest with a message that reads like a wrong surname. A false notice means they go
// looking for a PMS problem that does not exist, or switch a working method off.
//
// The condition mirrors the server's own rule (iam_v2.p3_feed_authorizes): CONNECTED and IN_SYNC, nothing
// less. These tests exist mainly so that if the server rule is ever widened or narrowed, the screen that
// explains it to a human does not quietly keep asserting the old one.

import { describe, it, expect } from "vitest";
import { roomSignInUnavailableReason } from "@/lib/pms-availability";
import type { PmsInterfaceHealth } from "@/lib/api";

const health = (transport: string, sync: string): PmsInterfaceHealth => ({
  pms_interface_id: "i1",
  transport_status: transport,
  continuity_status: "CONTINUOUS",
  sync_status: sync,
  in_house_stays: 0,
  pending_events: 0,
  review_events: 0,
});

describe("roomSignInUnavailableReason", () => {
  it("says nothing when the feed is connected and in sync", () => {
    expect(roomSignInUnavailableReason([health("CONNECTED", "IN_SYNC")])).toBeNull();
  });

  it("reports a disconnected PMS — the state the appliance is in when the socket is given to another system", () => {
    const reason = roomSignInUnavailableReason([health("DISCONNECTED", "RESYNC_REQUIRED")]);
    expect(reason).toBe("The property management system is disconnected");
  });

  // A connected feed that is still loading needs time, not intervention. Calling it "disconnected" would send
  // an operator to chase a link that is already up.
  it("distinguishes a connected feed that is still loading its guest list", () => {
    const reason = roomSignInUnavailableReason([health("CONNECTED", "RESYNC_IN_PROGRESS")]);
    expect(reason).toBe("The property management system is connected but its guest list is still loading");
  });

  // Connected but not in sync authorises nobody, because occupancy evidence is only trusted while the mirror
  // behind it is maintained. A check that only looked at transport would call this healthy.
  it.each(["RESYNC_REQUIRED", "RESYNC_IN_PROGRESS", "SYNC_FAILED", "UNKNOWN"])(
    "treats CONNECTED/%s as unable to serve guests", (sync) => {
      expect(roomSignInUnavailableReason([health("CONNECTED", sync)])).not.toBeNull();
    });

  // Guests reach an interface by the network they are on, so one healthy interface means Room sign-in really
  // does work for the guests routed to it. Warning because an unrelated interface is down would be false.
  it("stays silent when at least one routed interface is healthy", () => {
    expect(roomSignInUnavailableReason([
      health("DISCONNECTED", "RESYNC_REQUIRED"),
      health("CONNECTED", "IN_SYNC"),
    ])).toBeNull();
  });

  // Unknown readiness must not produce a warning. A health read that failed is not evidence of an outage, and
  // a false alarm on a working system costs more trust than a missing one.
  it("says nothing when readiness is unknown", () => {
    expect(roomSignInUnavailableReason(null)).toBeNull();
    expect(roomSignInUnavailableReason(undefined)).toBeNull();
    expect(roomSignInUnavailableReason([])).toBeNull();
  });
});
