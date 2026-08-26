// Can PMS Room sign-in serve a guest right now — and if not, WHICH guests?
//
// This is a different question from whether the method is switched on. Guest authentication requires a live
// PMS feed, so an interface can be ACTIVE, correctly configured, with the method enabled and a valid mode
// selected, and still refuse every guest. The guest sees the uniform failure message — deliberately identical
// to a wrong surname — so the front desk re-checks spellings instead of the interface.
//
// TWO THINGS THIS FILE DELIBERATELY DOES NOT DECIDE.
//
// Whether a given interface's feed is healthy is the SERVER's answer, read from `room_auth_ready`. It is
// derived from the same runtime and active Revision that Phase 3 reads, including that Revision's own
// heartbeat_timeout_ms. This file used to re-implement the rule with the 300-second default hardcoded, which
// described an interface configured with any other timeout using a number that interface does not use.
//
// And an interface whose health could not be READ is not a broken interface. A failed request is absence of
// evidence; reporting it as "guests cannot sign in" turns a transient admin-API hiccup into a false outage
// notice, which is how an operator learns to distrust the notice that matters.
//
// WHAT THIS FILE DOES DECIDE is the routing question the server cannot answer per-interface: which guest
// networks are affected. A guest is resolved against the interface their network routes to, so "is any
// interface healthy" is wrong in both directions — a site with one broken interface out of two reports fine
// for the guests who can only reach the broken one, and an ACTIVE interface nobody routes to raises an alarm
// about guests that do not exist.

import type { PmsInterface, PmsInterfaceHealth, PmsGuestNetworkRoute } from "@/lib/api";

/** One guest network that cannot currently sign guests in, and why. */
export type AffectedRoute = {
  guestNetwork: string;
  pmsInterface: string;
  reason: string;
};

export type RoomSignInReadiness =
  /** Nothing could be established at all. Shows nothing. */
  | { state: "unknown" }
  /** Every routed guest network whose readiness is known can sign guests in. */
  | { state: "ready"; unchecked: string[] }
  /** No routed guest network with a known result can. */
  | { state: "down"; reason: string; affected: AffectedRoute[]; unchecked: string[] }
  /** Some can and some cannot — the operator needs to know which. */
  | { state: "partial"; affected: AffectedRoute[]; unchecked: string[] };

/**
 * Operator wording for the server's bounded reason codes.
 *
 * The server sends a code, never a sentence: a free-text reason assembled from runtime state is how PMS
 * detail reaches surfaces nobody audited. An unrecognised code falls back to a general phrase rather than
 * being rendered raw, so a future code added server-side cannot print an internal token at a hotel.
 */
const REASON_WORDS: Record<string, string> = {
  INTERFACE_NOT_ACTIVE: "the property management system interface is not in use",
  NO_PUBLISHED_REVISION: "the property management system interface has no published configuration",
  CONTINUITY_GAP: "updates from the property management system were missed",
  CONTINUITY_NOT_ESTABLISHED: "no updates have arrived from the property management system yet",
  NOT_IN_SYNC: "the guest list is still loading",
  FEED_SILENT: "the property management system has stopped responding",
  REVISION_NOT_PINNED: "the property management system interface is being reconfigured",
  // Sent only while the transport is down: the mirror is the fallback and it has nothing in it yet.
  MIRROR_NEVER_SYNCHRONIZED: "the guest list has not been received from the property management system yet",
  // Also transport-down only, and brief — a full refresh is partway through.
  RESYNC_IN_FLIGHT: "the guest list is being refreshed",
  // Seconds, not an outage: the list has arrived and is being written in.
  MATERIALIZATION_BEHIND: "the guest list is being applied",
  // TRANSPORT_DOWN is deliberately absent. The server no longer sends it: a dropped connection does not stop
  // guests signing in when the local guest list is intact, so it was never a reason on its own. Retaining a
  // phrase for it would let a stale server keep telling staff that guests cannot sign in when they can.
};
const words = (code: string | undefined): string =>
  (code && REASON_WORDS[code]) || "the property management system is unavailable";

/** ready | unavailable(reason) | unknown, for one interface. */
type IfaceVerdict = { ready: true } | { ready: false; reason: string } | null;

function verdictFor(id: string, healthById: Map<string, PmsInterfaceHealth>): IfaceVerdict {
  const h = healthById.get(id);
  if (!h) return null;                       // health not read — unknown, never an outage
  if (h.room_auth_ready === undefined) return null; // server too old to answer; do not guess
  return h.room_auth_ready ? { ready: true } : { ready: false, reason: words(h.room_auth_reason) };
}

/**
 * Room sign-in readiness, evaluated per routed guest network.
 *
 * `routes` decides which interfaces matter. An interface nobody routes to cannot affect the answer, and a
 * network routed in ALL_ACTIVE_INTERFACES mode fans out across every ACTIVE interface exactly as the resolver
 * does — so its guests are served while any one of them is healthy.
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

    // A network mapped to an interface that is not ACTIVE cannot resolve at all. That is a configuration
    // state the server also reports, but the mapping itself is only visible here, so it is named here.
    if (r.routing_mode !== "ALL_ACTIVE_INTERFACES" && !activeIds.has(r.pms_interface_id)) {
      affected.push({
        guestNetwork: network,
        pmsInterface: label(r.pms_interface_id),
        reason: REASON_WORDS.INTERFACE_NOT_ACTIVE,
      });
      continue;
    }

    const candidateIds = r.routing_mode === "ALL_ACTIVE_INTERFACES" ? [...activeIds] : [r.pms_interface_id];
    if (candidateIds.length === 0) { unchecked.push(network); continue; }

    // One healthy candidate is enough: that is the interface these guests resolve against.
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
    // Nothing healthy. If any candidate's state is unknown, this network's readiness is unknown too — the
    // unread one might have been the healthy one, and claiming an outage on that basis would be a guess.
    if (sawUnknown) { unchecked.push(network); continue; }
    affected.push({
      guestNetwork: network,
      pmsInterface: candidateIds.length === 1 ? label(candidateIds[0]) : "every property management system interface",
      reason: firstReason ?? words(undefined),
    });
  }

  // Nothing definite either way.
  if (affected.length === 0 && ok.length === 0) return { state: "unknown" };
  if (affected.length === 0) return { state: "ready", unchecked };
  if (ok.length === 0) {
    // Every network with a known result is affected. When they all fail the same way, say it once — an
    // operator with one interface should read a sentence, not a list of one.
    const reasons = new Set(affected.map((a) => a.reason));
    const reason = reasons.size === 1 ? [...reasons][0] : words(undefined);
    return { state: "down", reason, affected, unchecked };
  }
  return { state: "partial", affected, unchecked };
}
