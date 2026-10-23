// Use the server's per-network readiness verdict because heartbeat timeouts depend on each active revision.
// Unreadable interface health is unknown, not evidence of an outage.

import { describe, it, expect } from "vitest";
import { roomSignInReadiness } from "@/lib/pms-availability";
import type { PmsInterface, PmsInterfaceHealth, PmsGuestNetworkRoute } from "@/lib/api";

const iface = (id: string, label: string, state = "ACTIVE"): PmsInterface => ({
  id, display_label: label, connector_kind: "protel-fias", lifecycle_state: state,
  revision_count: 1, published: true,
});

const health = (id: string, ready: boolean, reason?: string): PmsInterfaceHealth => ({
  pms_interface_id: id,
  transport_status: ready ? "CONNECTED" : "DISCONNECTED",
  sync_status: ready ? "IN_SYNC" : "RESYNC_REQUIRED",
  continuity_status: "CONTINUOUS",
  room_auth_ready: ready,
  room_auth_reason: reason,
  in_house_stays: 0, pending_events: 0, review_events: 0,
});

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

  // A revision can set a longer heartbeat timeout than the default; trust the server's verdict.
  it("believes a server that reports ready despite a long silence under a non-default heartbeat timeout", () => {
    const longTimeout: PmsInterfaceHealth = {
      pms_interface_id: "a",
      transport_status: "CONNECTED", sync_status: "IN_SYNC", continuity_status: "CONTINUOUS",
      last_heartbeat_at: new Date(Date.now() - 20 * 60_000).toISOString(),
      room_auth_ready: true,
      in_house_stays: 0, pending_events: 0, review_events: 0,
    };
    const r = roomSignInReadiness([iface("a", "Protel")], [longTimeout], [route("Guest Wi-Fi", "a", "Protel")]);
    expect(r.state).toBe("ready");
  });

  // Shorter revision timeouts must also override a client-side default.
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
    ["MIRROR_NEVER_SYNCHRONIZED", "the guest list has not been received from the property management system yet"],
    ["RESYNC_IN_FLIGHT", "the guest list is being refreshed"],
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

  // Unknown internal codes must not be displayed raw to hotel staff.
  it("falls back to a general phrase for an unrecognised reason code", () => {
    const r = roomSignInReadiness([iface("a", "Protel")], [health("a", false, "SOME_FUTURE_CODE")], [route("W", "a", "Protel")]);
    if (r.state !== "down") throw new Error(`expected down, got ${r.state}`);
    expect(r.reason).toBe("the property management system is unavailable");
    expect(r.reason).not.toContain("SOME_FUTURE_CODE");
  });
});

describe("an unreadable health result is unknown, never an outage", () => {
  // A failed health fetch must not become a false outage warning.
  it("does not claim an outage when a routed interface's health could not be read", () => {
    const r = roomSignInReadiness([iface("a", "Protel")], [], [route("Guest Wi-Fi", "a", "Protel")]);
    expect(r.state).toBe("unknown");
  });

  it("treats a health row without a server verdict as unknown rather than guessing from the axes", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel")], [healthWithoutVerdict("a")], [route("Guest Wi-Fi", "a", "Protel")]);
    expect(r.state).toBe("unknown");
  });

  it("separates unchecked networks from affected ones", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe"), iface("c", "Protel Spa")],
      [READY("a"), health("b", false, "MIRROR_NEVER_SYNCHRONIZED")], // c's health missing
      [route("Main", "a", "Protel Main"), route("Annexe", "b", "Protel Annexe"), route("Spa", "c", "Protel Spa")],
    );
    expect(r.state).toBe("partial");
    if (r.state !== "partial") return;
    expect(r.affected.map((a) => a.guestNetwork)).toEqual(["Annexe"]);
    expect(r.unchecked).toEqual(["Spa"]);
  });

  it("is unchecked when the routed interface was not read", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [health("b", false, "MIRROR_NEVER_SYNCHRONIZED")], // a unread
      [route("Guest Wi-Fi", "a", "Protel Main")],
    );
    expect(r.state).toBe("unknown");
  });
});

describe("routing semantics are unchanged", () => {
  it("reports a partial outage and names only the affected network and interface", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [READY("a"), health("b", false, "MIRROR_NEVER_SYNCHRONIZED")],
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
      [health("a", false, "MIRROR_NEVER_SYNCHRONIZED"), health("b", false, "MIRROR_NEVER_SYNCHRONIZED")],
      [route("Main Wi-Fi", "a", "Protel Main"), route("Annexe Wi-Fi", "b", "Protel Annexe")],
    );
    expect(r.state).toBe("down");
  });

  it("ignores an unrouted interface, however broken", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("spare", "Spare")],
      [READY("a"), health("spare", false, "MIRROR_NEVER_SYNCHRONIZED")],
      [route("Main Wi-Fi", "a", "Protel Main")],
    );
    expect(r.state).toBe("ready");
  });

  // routing_mode does not fan out; readiness follows the mapped interface only.
  it("reads a stored ALL_ACTIVE_INTERFACES route as the one interface it is mapped to", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [health("a", false, "MIRROR_NEVER_SYNCHRONIZED"), READY("b")],
      [route("Guest Wi-Fi", "a", "Protel Main", "ALL_ACTIVE_INTERFACES")],
    );
    expect(r.state).toBe("down");
  });

  it("reports a stored ALL_ACTIVE_INTERFACES route as down when its interface is unhealthy", () => {
    const r = roomSignInReadiness(
      [iface("a", "Protel Main"), iface("b", "Protel Annexe")],
      [health("a", false, "MIRROR_NEVER_SYNCHRONIZED"), health("b", false, "MIRROR_NEVER_SYNCHRONIZED")],
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
