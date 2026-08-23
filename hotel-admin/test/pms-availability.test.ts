// Room sign-in readiness is a per-guest-network question, and getting it wrong is a defect in both
// directions.
//
// A missing warning means an operator watches a correctly configured, enabled method refuse guests with a
// message that reads like a wrong surname. A false warning means they hunt a PMS problem that does not exist,
// or switch a working method off. A warning that is right for the property but wrong for a network is both at
// once — which is exactly what "is any interface healthy" produces once a site has more than one.
//
// The health condition mirrors the server's own (iam_v2.p3_feed_authorizes): CONNECTED, IN_SYNC, CONTINUOUS
// and recently alive. These tests exist so that if the server rule moves, the screen explaining it to a human
// cannot quietly keep asserting the old one.

import { describe, it, expect } from "vitest";
import { roomSignInReadiness } from "@/lib/pms-availability";
import type { PmsInterface, PmsInterfaceHealth, PmsGuestNetworkRoute } from "@/lib/api";

const NOW = Date.parse("2026-08-23T18:00:00Z");
const fresh = new Date(NOW - 30_000).toISOString();
const stale = new Date(NOW - 20 * 60_000).toISOString();

const iface = (id: string, label: string, state = "ACTIVE"): PmsInterface => ({
  id, display_label: label, connector_kind: "protel-fias", lifecycle_state: state,
  revision_count: 1, published: true,
});

const health = (
  id: string, transport: string, sync: string, continuity = "CONTINUOUS", lastSeen: string | null = fresh,
): PmsInterfaceHealth => ({
  pms_interface_id: id,
  transport_status: transport, sync_status: sync, continuity_status: continuity,
  last_heartbeat_at: lastSeen ?? undefined,
  in_house_stays: 0, pending_events: 0, review_events: 0,
});

const route = (
  net: string, ifaceId: string, label: string, mode = "MAPPED",
): PmsGuestNetworkRoute => ({
  guest_network_id: `${net}-id`, guest_network_name: net,
  pms_interface_id: ifaceId, pms_interface_label: label,
  is_default: true, routing_mode: mode,
});

const HEALTHY = (id: string) => health(id, "CONNECTED", "IN_SYNC");

describe("roomSignInReadiness — the healthy case", () => {
  it("is ready when the routed interface is connected, in sync, continuous and alive", () => {
    const r = roomSignInReadiness([iface("a", "Protel")], [HEALTHY("a")], [route("Guest Wi-Fi", "a", "Protel")], NOW);
    expect(r.state).toBe("ready");
  });
});

describe("roomSignInReadiness — a single routed interface that cannot authorise", () => {
  // Each of these is a state the server refuses to mint an Auth Context in. A check that looked only at
  // transport would call three of them healthy.
  it.each([
    ["disconnected", health("a", "DISCONNECTED", "IN_SYNC"), "the connection to the property management system is down"],
    ["still loading", health("a", "CONNECTED", "RESYNC_IN_PROGRESS"), "the guest list is still loading"],
    ["needs a refresh", health("a", "CONNECTED", "RESYNC_REQUIRED"), "the guest list is still loading"],
    ["gapped", health("a", "CONNECTED", "IN_SYNC", "GAP_DETECTED"), "updates from the property management system were missed"],
    ["continuity never established", health("a", "CONNECTED", "IN_SYNC", "UNKNOWN"), "no updates have arrived from the property management system yet"],
    ["silent past its timeout", health("a", "CONNECTED", "IN_SYNC", "CONTINUOUS", stale), "the property management system has stopped responding"],
  ])("reports %s", (_name, h, reason) => {
    const r = roomSignInReadiness([iface("a", "Protel")], [h], [route("Guest Wi-Fi", "a", "Protel")], NOW);
    expect(r.state).toBe("down");
    if (r.state === "down") expect(r.reason).toBe(reason);
  });
});

describe("roomSignInReadiness — per network, not per property", () => {
  // THE CORRECTION THIS FILE EXISTS FOR. Two networks, two interfaces, one broken. Reporting "ready" because
  // some interface somewhere is healthy is false for every guest on the broken one.
  it("reports a partial outage and names the affected network and interface", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [HEALTHY("a"), health("b", "DISCONNECTED", "IN_SYNC")],
      [route("Main Wi-Fi", "a", "Protel Main"), route("Annexe Wi-Fi", "b", "Protel Annexe")],
      NOW,
    );
    expect(r.state).toBe("partial");
    if (r.state !== "partial") return;
    expect(r.affected).toHaveLength(1);
    expect(r.affected[0].guestNetwork).toBe("Annexe Wi-Fi");
    expect(r.affected[0].pmsInterface).toBe("Protel Annexe");
    expect(r.affected[0].reason).toContain("connection");
  });

  it("is down, not partial, when every routed network is affected", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [health("a", "DISCONNECTED", "IN_SYNC"), health("b", "DISCONNECTED", "IN_SYNC")],
      [route("Main Wi-Fi", "a", "Protel Main"), route("Annexe Wi-Fi", "b", "Protel Annexe")],
      NOW,
    );
    expect(r.state).toBe("down");
  });

  // An ACTIVE interface nobody routes to serves no guests, so its state cannot make the screen warn.
  it("ignores an unrouted interface, however broken", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("spare", "Spare interface")],
      [HEALTHY("a"), health("spare", "DISCONNECTED", "IN_SYNC", "GAP_DETECTED")],
      [route("Main Wi-Fi", "a", "Protel Main")],
      NOW,
    );
    expect(r.state).toBe("ready");
  });

  it("keeps healthy networks out of the affected list", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [HEALTHY("a"), health("b", "DISCONNECTED", "IN_SYNC")],
      [route("Main Wi-Fi", "a", "Protel Main"), route("Annexe Wi-Fi", "b", "Protel Annexe")],
      NOW,
    );
    if (r.state !== "partial") throw new Error(`expected partial, got ${r.state}`);
    expect(r.affected.map((a) => a.guestNetwork)).not.toContain("Main Wi-Fi");
  });
});

describe("roomSignInReadiness — routing modes and lifecycle", () => {
  // ALL_ACTIVE_INTERFACES fans a network out across every ACTIVE interface, exactly as the resolver does, so
  // one healthy interface is enough for its guests.
  it("treats ALL_ACTIVE_INTERFACES as served while any active interface is healthy", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [health("a", "DISCONNECTED", "IN_SYNC"), HEALTHY("b")],
      [route("Guest Wi-Fi", "a", "Protel Main", "ALL_ACTIVE_INTERFACES")],
      NOW,
    );
    expect(r.state).toBe("ready");
  });

  it("reports ALL_ACTIVE_INTERFACES as down when no active interface is healthy", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [health("a", "DISCONNECTED", "IN_SYNC"), health("b", "DISCONNECTED", "IN_SYNC")],
      [route("Guest Wi-Fi", "a", "Protel Main", "ALL_ACTIVE_INTERFACES")],
      NOW,
    );
    expect(r.state).toBe("down");
  });

  // Routed to an interface that has been switched off: its guests cannot resolve, and the reason is a
  // lifecycle state rather than a connection fault.
  it("reports a network routed to a non-ACTIVE interface", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("old", "Retired interface", "AUTH_DISABLED")],
      [HEALTHY("a")],
      [route("Main Wi-Fi", "a", "Protel Main"), route("Old Wi-Fi", "old", "Retired interface")],
      NOW,
    );
    expect(r.state).toBe("partial");
    if (r.state === "partial") expect(r.affected[0].reason).toContain("not in use");
  });
});

describe("roomSignInReadiness — unknown readiness never warns", () => {
  // A failed read is not evidence of an outage, and a false alarm on a working system costs more trust than a
  // missing one.
  it.each([
    ["interfaces unread", null, [HEALTHY("a")], [route("Guest Wi-Fi", "a", "Protel")]],
    ["health unread", [iface("a", "Protel")], null, [route("Guest Wi-Fi", "a", "Protel")]],
    ["routing unread", [iface("a", "Protel")], [HEALTHY("a")], null],
    ["nothing routed", [iface("a", "Protel")], [HEALTHY("a")], []],
  ])("stays silent when %s", (_n, ifaces, healths, routes) => {
    const r = roomSignInReadiness(
      ifaces as PmsInterface[] | null, healths as PmsInterfaceHealth[] | null,
      routes as PmsGuestNetworkRoute[] | null, NOW);
    expect(r.state).toBe("unknown");
  });

  // Health missing for a routed interface is not the same as health saying "bad", but it still cannot be
  // reported as ready — we do not know that it is.
  it("does not claim ready when a routed interface has no health at all", () => {
    const r = roomSignInReadiness([iface("a", "Protel")], [], [route("Guest Wi-Fi", "a", "Protel")], NOW);
    expect(r.state).toBe("down");
  });
});
