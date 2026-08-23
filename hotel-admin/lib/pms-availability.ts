// Can PMS Room sign-in actually serve a guest right now?
//
// This is a DIFFERENT question from whether the method is switched on, and the difference is the whole reason
// this exists. Guest authentication requires a live PMS feed: iam_v2.p3_feed_authorizes refuses to mint an
// Auth Context unless the interface is CONNECTED and IN_SYNC, because occupancy evidence is only trusted
// while the mirror behind it is still being maintained.
//
// So an interface can be ACTIVE, correctly configured, with the method enabled and a valid mode selected, and
// still refuse every guest. The guest sees the uniform failure message — deliberately identical to a wrong
// surname — so the front desk starts re-checking spellings instead of the interface. The operator needs to be
// told the truth on the screen where they manage the method.

import type { PmsInterfaceHealth } from "@/lib/api";

/**
 * Returns null when Room sign-in can serve guests, or an operator-facing reason when it cannot.
 *
 * Null is also returned when readiness is UNKNOWN — a health read that failed must not put a warning on the
 * screen. A missing notice is a smaller error than a false one.
 */
export function roomSignInUnavailableReason(healths: PmsInterfaceHealth[] | null | undefined): string | null {
  if (!healths || healths.length === 0) return null;

  // ANY healthy interface, not every one. Guests are routed to an interface by the network they are on, so one
  // healthy interface means Room sign-in genuinely works for the guests routed to it. Warning because a
  // second, unrelated interface is down would be false for everybody it does not serve.
  const usable = healths.some((h) => h.transport_status === "CONNECTED" && h.sync_status === "IN_SYNC");
  if (usable) return null;

  // Nothing can authorise. Distinguish the two states an operator can act on differently: a disconnected feed
  // needs someone to restore the link, whereas a connected feed still loading its guest list needs nothing but
  // time. Saying "disconnected" for both would send an operator to chase a connection that is already up.
  const anyConnected = healths.some((h) => h.transport_status === "CONNECTED");
  return anyConnected
    ? "The property management system is connected but its guest list is still loading"
    : "The property management system is disconnected";
}
