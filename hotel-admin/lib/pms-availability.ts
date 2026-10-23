// Can PMS Room sign-in serve a guest right now, and which guests are affected. Feed health is the SERVER's answer
// (`room_auth_ready`);
// an interface whose health could not be read is unknown, not broken, so a transient API failure never raises a false
// outage notice.
// This file decides only the routing question: a guest resolves against the interface their network routes to, so "any
// interface healthy" is wrong both ways.

import type { PmsInterface, PmsInterfaceHealth, PmsGuestNetworkRoute } from "@/lib/api";

export type AffectedRoute = {
  guestNetwork: string;
  pmsInterface: string;
  reason: string;
};

export type RoomSignInReadiness =
  | { state: "unknown" }
  | { state: "ready"; unchecked: string[] }
  | { state: "down"; reason: string; affected: AffectedRoute[]; unchecked: string[] }
  | { state: "partial"; affected: AffectedRoute[]; unchecked: string[] };

/** Unknown reason codes use a general phrase so raw server tokens stay hidden. */
const REASON_WORDS: Record<string, string> = {
  INTERFACE_NOT_ACTIVE: "the property management system interface is not in use",
  NO_PUBLISHED_REVISION: "the property management system interface has no published configuration",
  CONTINUITY_GAP: "updates from the property management system were missed",
  CONTINUITY_NOT_ESTABLISHED: "no updates have arrived from the property management system yet",
  NOT_IN_SYNC: "the guest list is still loading",
  FEED_SILENT: "the property management system has stopped responding",
  REVISION_NOT_PINNED: "the property management system interface is being reconfigured",
  MIRROR_NEVER_SYNCHRONIZED: "the guest list has not been received from the property management system yet",
  RESYNC_IN_FLIGHT: "the guest list is being refreshed",
  MATERIALIZATION_BEHIND: "the guest list is being applied",
  // TRANSPORT_DOWN is deliberately absent: a dropped connection does not stop sign-in while the local guest list is
  // intact.
};
const words = (code: string | undefined): string =>
  (code && REASON_WORDS[code]) || "the property management system is unavailable";

type IfaceVerdict = { ready: true } | { ready: false; reason: string } | null;

function verdictFor(id: string, healthById: Map<string, PmsInterfaceHealth>): IfaceVerdict {
  const h = healthById.get(id);
  if (!h) return null;                       // health not read — unknown, never an outage
  if (h.room_auth_ready === undefined) return null; // server too old to answer; do not guess
  return h.room_auth_ready ? { ready: true } : { ready: false, reason: words(h.room_auth_reason) };
}

/**
 * Room sign-in readiness per routed guest network. Only routed interfaces matter; legacy rows with routing_mode
 * ALL_ACTIVE_INTERFACES are read like MAPPED (the resolver only ever consulted the mapped interface).
 */
export function roomSignInReadiness(
  interfaces: PmsInterface[] | null | undefined,
  healths: PmsInterfaceHealth[] | null | undefined,
  routes: PmsGuestNetworkRoute[] | null | undefined,
): RoomSignInReadiness {
  if (!interfaces || !healths || !routes || routes.length === 0) return { state: "unknown" };

  const activeIds = new Set(interfaces.filter((i) => i.lifecycle_state === "ACTIVE").map((i) => i.id));
  const healthById = new Map(healths.map((h) => [h.pms_interface_id, h]));
  const labelById = new Map(interfaces.map((i) => [i.id, i.display_label || i.id]));

  const ok: string[] = [];
  const affected: AffectedRoute[] = [];
  const unchecked: string[] = [];

  for (const r of routes) {
    const network = r.guest_network_name || r.guest_network_id;
    const label = (id: string) => r.pms_interface_label || labelById.get(id) || id;

    // A network mapped to a non-ACTIVE interface cannot resolve at all; the mapping is only visible here.
    if (!activeIds.has(r.pms_interface_id)) {
      affected.push({
        guestNetwork: network,
        pmsInterface: label(r.pms_interface_id),
        reason: REASON_WORDS.INTERFACE_NOT_ACTIVE,
      });
      continue;
    }

    const candidateIds = [r.pms_interface_id];
    if (candidateIds.length === 0) { unchecked.push(network); continue; }

    let firstReason: string | null = null;
    let sawUnknown = false;
    let usable = false;
    for (const id of candidateIds) {
      const v = verdictFor(id, healthById);
      if (v === null) { sawUnknown = true; continue; }
      if (v.ready) { usable = true; break; }
      firstReason = firstReason ?? v.reason;
    }

    if (usable) { ok.push(network); continue; }
    // Nothing healthy: if any candidate is unknown, so is the network (the unread one may have been healthy).
    if (sawUnknown) { unchecked.push(network); continue; }
    affected.push({
      guestNetwork: network,
      pmsInterface: candidateIds.length === 1 ? label(candidateIds[0]) : "every property management system interface",
      reason: firstReason ?? words(undefined),
    });
  }

  if (affected.length === 0 && ok.length === 0) return { state: "unknown" };
  if (affected.length === 0) return { state: "ready", unchecked };
  if (ok.length === 0) {
    // When all fail the same way, say it once.
    const reasons = new Set(affected.map((a) => a.reason));
    const reason = reasons.size === 1 ? [...reasons][0] : words(undefined);
    return { state: "down", reason, affected, unchecked };
  }
  return { state: "partial", affected, unchecked };
}
