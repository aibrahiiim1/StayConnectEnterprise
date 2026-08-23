// Can PMS Room sign-in serve a guest right now — and if not, WHICH guests?
//
// This is a different question from whether the method is switched on, and the difference is why this exists.
// Guest authentication requires a live PMS feed: iam_v2.p3_feed_authorizes refuses to mint an Auth Context
// unless the interface is CONNECTED, IN_SYNC and CONTINUOUS with recent liveness, because occupancy evidence
// is only trusted while the mirror behind it is still being maintained.
//
// So an interface can be ACTIVE, correctly configured, with the method enabled and a valid mode selected, and
// still refuse every guest. The guest sees the uniform failure message — deliberately identical to a wrong
// surname — so the front desk re-checks spellings instead of the interface.
//
// IT IS ANSWERED PER GUEST NETWORK, NOT PER SITE. A guest is resolved against the interface their network
// routes to, so "is any interface healthy" is the wrong question in both directions: a property with one
// broken interface out of two would be reported as fine for the guests who can only reach the broken one, and
// an ACTIVE interface that no network routes to would raise an alarm about guests nobody has.

import type { PmsInterface, PmsInterfaceHealth, PmsGuestNetworkRoute } from "@/lib/api";

/** One guest network that cannot currently sign guests in, and why. */
export type AffectedRoute = {
  guestNetwork: string;
  pmsInterface: string;
  reason: string;
};

export type RoomSignInReadiness =
  /** Readiness could not be established. Shows nothing: a failed read is not evidence of an outage. */
  | { state: "unknown" }
  /** Every routed guest network can sign guests in. */
  | { state: "ready" }
  /** No routed guest network can. */
  | { state: "down"; reason: string; affected: AffectedRoute[] }
  /** Some can and some cannot — the operator needs to know which. */
  | { state: "partial"; affected: AffectedRoute[] };

/**
 * How long a feed may be silent before it stops counting as live.
 *
 * Mirrors the server's heartbeat_timeout_ms default (300000ms) — the same value the PRE-LIVE interface is
 * configured with. The authoritative gate is always iam_v2.p3_feed_authorizes, which reads the bound from the
 * interface's own Revision config; this constant only decides whether an operator is TOLD, and it exists so a
 * socket that hung without reporting an error is not displayed as healthy.
 */
const FEED_SILENCE_LIMIT_MS = 300_000;

/** Why this interface cannot authorise, or null if it can. Mirrors the server's feed-health condition. */
function interfaceUnavailableReason(h: PmsInterfaceHealth, now: number): string | null {
  if (h.transport_status !== "CONNECTED") return "the connection to the property management system is down";
  if (h.continuity_status === "GAP_DETECTED") return "updates from the property management system were missed";
  if (h.sync_status !== "IN_SYNC") return "the guest list is still loading";
  // CONTINUOUS is required, and UNKNOWN is not a pass: it means continuity was never established.
  if (h.continuity_status !== "CONTINUOUS") return "no updates have arrived from the property management system yet";
  // Connected and in sync on paper, but nothing has been heard for longer than the feed's own timeout. A hung
  // socket reports neither an error nor a disconnect, so without this it would read as perfectly healthy.
  const lastSeen = h.last_heartbeat_at ?? h.last_connected_at;
  if (lastSeen) {
    const age = now - new Date(lastSeen).getTime();
    if (Number.isFinite(age) && age > FEED_SILENCE_LIMIT_MS) {
      return "the property management system has stopped responding";
    }
  }
  return null;
}

/**
 * Room sign-in readiness, evaluated per routed guest network.
 *
 * `routes` decides which interfaces matter. An interface nobody routes to cannot affect the answer, and a
 * network routed in ALL_ACTIVE_INTERFACES mode fans out across every ACTIVE interface exactly as the resolver
 * does — so its guests are served as long as one of them is healthy.
 */
export function roomSignInReadiness(
  interfaces: PmsInterface[] | null | undefined,
  healths: PmsInterfaceHealth[] | null | undefined,
  routes: PmsGuestNetworkRoute[] | null | undefined,
  now: number = Date.now(),
): RoomSignInReadiness {
  if (!interfaces || !healths || !routes) return { state: "unknown" };

  const activeIds = new Set(interfaces.filter((i) => i.lifecycle_state === "ACTIVE").map((i) => i.id));
  const healthById = new Map(healths.map((h) => [h.pms_interface_id, h]));
  const labelById = new Map(interfaces.map((i) => [i.id, i.display_label || i.id]));

  // Only routed networks are evaluated. No routes means no guest can be resolved at all, which is a routing
  // configuration matter with its own screen — not something to report here as a PMS outage.
  if (routes.length === 0) return { state: "unknown" };

  const ok: string[] = [];
  const affected: AffectedRoute[] = [];

  for (const r of routes) {
    const candidateIds = r.routing_mode === "ALL_ACTIVE_INTERFACES"
      ? [...activeIds]
      : (activeIds.has(r.pms_interface_id) ? [r.pms_interface_id] : []);

    const network = r.guest_network_name || r.guest_network_id;

    if (candidateIds.length === 0) {
      // Routed to an interface that is not ACTIVE. Guests on this network cannot be resolved, and saying so
      // is more useful than silence, but it is a lifecycle state rather than a connection fault.
      affected.push({
        guestNetwork: network,
        pmsInterface: r.pms_interface_label || labelById.get(r.pms_interface_id) || r.pms_interface_id,
        reason: "the property management system interface is not in use",
      });
      continue;
    }

    // Readiness for this network is decided by its candidates. One healthy candidate is enough: that is the
    // interface its guests will resolve against.
    let reason: string | null = null;
    let usable = false;
    for (const id of candidateIds) {
      const h = healthById.get(id);
      if (!h) { reason = reason ?? "the property management system state is unknown"; continue; }
      const why = interfaceUnavailableReason(h, now);
      if (!why) { usable = true; break; }
      // Report the first candidate's reason; with a single mapped interface that is the only one there is.
      reason = reason ?? why;
    }

    if (usable) { ok.push(network); continue; }
    affected.push({
      guestNetwork: network,
      pmsInterface: candidateIds.length === 1
        ? (r.pms_interface_label || labelById.get(candidateIds[0]) || candidateIds[0])
        : "every property management system interface",
      reason: reason ?? "the property management system is unavailable",
    });
  }

  if (affected.length === 0) return { state: "ready" };
  if (ok.length === 0) {
    // Every routed network is affected. When they all fail the same way, say it once rather than repeating it
    // per network — an operator with one interface should read one sentence, not a list of one.
    const reasons = new Set(affected.map((a) => a.reason));
    const reason = reasons.size === 1 ? [...reasons][0] : "the property management system is unavailable";
    return { state: "down", reason, affected };
  }
  return { state: "partial", affected };
}
