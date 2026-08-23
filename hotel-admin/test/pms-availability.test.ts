// Room sign-in readiness is a per-guest-network question, and getting it wrong is a defect in both
// directions.
//
// A missing warning means an operator watches a correctly configured, enabled method refuse guests with a
// message that reads like a wrong surname. A false warning means they hunt a PMS problem that does not exist.
// A warning that is right for the property but wrong for a network is both at once.
//
// TWO THINGS ARE NO LONGER DECIDED HERE, and the tests below pin that. Whether an interface's feed is healthy
// is the server's answer (`room_auth_ready`), derived from the same runtime and active Revision that Phase 3
// reads — including that Revision's own heartbeat_timeout_ms, which this client cannot know. And an interface
// whose health could not be READ is unknown, never an outage.

import { describe, it, expect } from "vitest";
import { roomSignInReadiness } from "@/lib/pms-availability";
import type { PmsInterface, PmsInterfaceHealth, PmsGuestNetworkRoute } from "@/lib/api";

const iface = (id: string, label: string, state = "ACTIVE"): PmsInterface => ({
  id, display_label: label, connector_kind: "protel-fias", lifecycle_state: state,
  revision_count: 1, published: true,
});

/** A health row carrying the SERVER's verdict. `reason` is a bounded code, as the server sends. */
const health = (id: string, ready: boolean, reason?: string): PmsInterfaceHealth => ({
  pms_interface_id: id,
  transport_status: ready ? "CONNECTED" : "DISCONNECTED",
  sync_status: ready ? "IN_SYNC" : "RESYNC_REQUIRED",
  continuity_status: "CONTINUOUS",
  room_auth_ready: ready,
  room_auth_reason: reason,
  in_house_stays: 0, pending_events: 0, review_events: 0,
});

/** A health row from a server that does not answer the question — indistinguishable from unknown. */
const healthWithoutVerdict = (id: string): PmsInterfaceHealth => ({
  pms_interface_id: id,
  transport_status: "CONNECTED", sync_status: "IN_SYNC", continuity_status: "CONTINUOUS",
  in_house_stays: 0, pending_events: 0, review_events: 0,
});

const route = (net: string, ifaceId: string, label: string, mode = "MAPPED"): PmsGuestNetworkRoute => ({
  guest_network_id: `${net}-id`, guest_network_name: net,
  pms_interface_id: ifaceId, pms_interface_label: label,
  is_default: true, routing_mode: mode,
});

const READY = (id: string) => health(id, true);

describe("the server decides feed health, not this client", () => {
  it("is ready when the server says the routed interface can serve Room auth", () => {
    const r = roomSignInReadiness([iface("a", "Protel")], [READY("a")], [route("Guest Wi-Fi", "a", "Protel")]);
    expect(r.state).toBe("ready");
  });

  // THE NON-DEFAULT TIMEOUT CASE. An interface whose Revision sets heartbeat_timeout_ms well above the
  // 300-second default is silent-but-healthy by its own configuration, and the server says so. The client
  // used to hardcode 300s and would have called this an outage; it must now simply believe the server.
  it("believes a server that reports ready despite a long silence under a non-default heartbeat timeout", () => {
    const longTimeout: PmsInterfaceHealth = {
      pms_interface_id: "a",
      transport_status: "CONNECTED", sync_status: "IN_SYNC", continuity_status: "CONTINUOUS",
      // Twenty minutes since anything was heard — past the 300s default, inside this Revision's own bound.
      last_heartbeat_at: new Date(Date.now() - 20 * 60_000).toISOString(),
      room_auth_ready: true,
      in_house_stays: 0, pending_events: 0, review_events: 0,
    };
    const r = roomSignInReadiness([iface("a", "Protel")], [longTimeout], [route("Guest Wi-Fi", "a", "Protel")]);
    expect(r.state).toBe("ready");
  });

  // And the converse: a Revision with a SHORTER timeout than the default. The server calls it silent while a
  // 300-second client rule would still have called it healthy.
  it("believes a server that reports FEED_SILENT under a shorter-than-default heartbeat timeout", () => {
    const shortTimeout: PmsInterfaceHealth = {
      pms_interface_id: "a",
      transport_status: "CONNECTED", sync_status: "IN_SYNC", continuity_status: "CONTINUOUS",
      last_heartbeat_at: new Date(Date.now() - 60_000).toISOString(), // 1 min: well inside 300s
      room_auth_ready: false, room_auth_reason: "FEED_SILENT",
      in_house_stays: 0, pending_events: 0, review_events: 0,
    };
    const r = roomSignInReadiness([iface("a", "Protel")], [shortTimeout], [route("Guest Wi-Fi", "a", "Protel")]);
    expect(r.state).toBe("down");
    if (r.state === "down") expect(r.reason).toBe("the property management system has stopped responding");
  });

  it.each([
    ["TRANSPORT_DOWN", "the connection to the property management system is down"],
    ["NOT_IN_SYNC", "the guest list is still loading"],
    ["CONTINUITY_GAP", "updates from the property management system were missed"],
    ["CONTINUITY_NOT_ESTABLISHED", "no updates have arrived from the property management system yet"],
    ["FEED_SILENT", "the property management system has stopped responding"],
    ["REVISION_NOT_PINNED", "the property management system interface is being reconfigured"],
    ["NO_PUBLISHED_REVISION", "the property management system interface has no published configuration"],
  ])("renders the server's %s code in operator words", (code, wording) => {
    const r = roomSignInReadiness([iface("a", "Protel")], [health("a", false, code)], [route("W", "a", "Protel")]);
    expect(r.state).toBe("down");
    if (r.state === "down") expect(r.reason).toBe(wording);
  });

  // A code this build does not know must never be printed raw at a hotel.
  it("falls back to a general phrase for an unrecognised reason code", () => {
    const r = roomSignInReadiness([iface("a", "Protel")], [health("a", false, "SOME_FUTURE_CODE")], [route("W", "a", "Protel")]);
    if (r.state !== "down") throw new Error(`expected down, got ${r.state}`);
    expect(r.reason).toBe("the property management system is unavailable");
    expect(r.reason).not.toContain("SOME_FUTURE_CODE");
  });
});

describe("an unreadable health result is unknown, never an outage", () => {
  // THE SECOND CORRECTION. The fetch for this interface failed, so it is absent from the health list. That is
  // absence of evidence: reporting "guests cannot sign in" would turn an admin-API hiccup into a false outage.
  it("does not claim an outage when a routed interface's health could not be read", () => {
    const r = roomSignInReadiness([iface("a", "Protel")], [], [route("Guest Wi-Fi", "a", "Protel")]);
    expect(r.state).toBe("unknown");
  });

  it("treats a health row without a server verdict as unknown rather than guessing from the axes", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel")], [healthWithoutVerdict("a")], [route("Guest Wi-Fi", "a", "Protel")]);
    expect(r.state).toBe("unknown");
  });

  // Mixed: one network definitely broken, one unreadable. The broken one is reported; the unreadable one is
  // listed as unchecked and is NOT counted among the affected.
  it("separates unchecked networks from affected ones", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe"), iface("c", "Protel Spa")],
      [READY("a"), health("b", false, "TRANSPORT_DOWN")], // c's health missing
      [route("Main", "a", "Protel Main"), route("Annexe", "b", "Protel Annexe"), route("Spa", "c", "Protel Spa")],
    );
    expect(r.state).toBe("partial");
    if (r.state !== "partial") return;
    expect(r.affected.map((a) => a.guestNetwork)).toEqual(["Annexe"]);
    expect(r.unchecked).toEqual(["Spa"]);
  });

  // An ALL_ACTIVE_INTERFACES network where one candidate is broken and another unreadable: the unread one
  // might have been the healthy one, so this is unknown rather than an outage.
  it("is unchecked when an unread candidate could have been the healthy one", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [health("a", false, "TRANSPORT_DOWN")], // b unread
      [route("Guest Wi-Fi", "a", "Protel Main", "ALL_ACTIVE_INTERFACES")],
    );
    expect(r.state).toBe("unknown");
  });
});

describe("routing semantics are unchanged", () => {
  it("reports a partial outage and names only the affected network and interface", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [READY("a"), health("b", false, "TRANSPORT_DOWN")],
      [route("Main Wi-Fi", "a", "Protel Main"), route("Annexe Wi-Fi", "b", "Protel Annexe")],
    );
    expect(r.state).toBe("partial");
    if (r.state !== "partial") return;
    expect(r.affected).toHaveLength(1);
    expect(r.affected[0].guestNetwork).toBe("Annexe Wi-Fi");
    expect(r.affected[0].pmsInterface).toBe("Protel Annexe");
    expect(r.affected.map((a) => a.guestNetwork)).not.toContain("Main Wi-Fi");
  });

  it("is down when every routed network with a known result is affected", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [health("a", false, "TRANSPORT_DOWN"), health("b", false, "TRANSPORT_DOWN")],
      [route("Main Wi-Fi", "a", "Protel Main"), route("Annexe Wi-Fi", "b", "Protel Annexe")],
    );
    expect(r.state).toBe("down");
  });

  it("ignores an unrouted interface, however broken", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("spare", "Spare")],
      [READY("a"), health("spare", false, "TRANSPORT_DOWN")],
      [route("Main Wi-Fi", "a", "Protel Main")],
    );
    expect(r.state).toBe("ready");
  });

  it("treats ALL_ACTIVE_INTERFACES as served while any active interface is healthy", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [health("a", false, "TRANSPORT_DOWN"), READY("b")],
      [route("Guest Wi-Fi", "a", "Protel Main", "ALL_ACTIVE_INTERFACES")],
    );
    expect(r.state).toBe("ready");
  });

  it("reports ALL_ACTIVE_INTERFACES as down when no active interface is healthy", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [health("a", false, "TRANSPORT_DOWN"), health("b", false, "TRANSPORT_DOWN")],
      [route("Guest Wi-Fi", "a", "Protel Main", "ALL_ACTIVE_INTERFACES")],
    );
    expect(r.state).toBe("down");
  });

  it("keeps a network mapped to a non-ACTIVE interface visible as a configuration problem", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("old", "Retired", "AUTH_DISABLED")],
      [READY("a")],
      [route("Main Wi-Fi", "a", "Protel Main"), route("Old Wi-Fi", "old", "Retired")],
    );
    expect(r.state).toBe("partial");
    if (r.state === "partial") expect(r.affected[0].reason).toContain("not in use");
  });

  it.each([
    ["interfaces unread", null, [READY("a")], [route("W", "a", "Protel")]],
    ["health unread", [iface("a", "Protel")], null, [route("W", "a", "Protel")]],
    ["routing unread", [iface("a", "Protel")], [READY("a")], null],
    ["nothing routed", [iface("a", "Protel")], [READY("a")], []],
  ])("stays silent when %s", (_n, ifaces, healths, routes) => {
    const r = roomSignInReadiness(
      ifaces as PmsInterface[] | null, healths as PmsInterfaceHealth[] | null,
      routes as PmsGuestNetworkRoute[] | null);
    expect(r.state).toBe("unknown");
  });
});
