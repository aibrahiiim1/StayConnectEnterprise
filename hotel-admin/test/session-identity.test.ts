// A session list must not reveal voucher codes or imply identity when entitlement lookup fails.

import { describe, it, expect } from "vitest";
import { identifySession, methodLabel, stateWords, endReasonWords, speedPair } from "@/lib/session-words";
import type { Session } from "@/lib/api";

const base: Session = {
  id: "s1", tenant_id: "t", site_id: "s", appliance_id: "a", guest_id: "g",
  ip: "10.80.4.62", mac: "a4:83:e7:2f:11:c0", state: "active",
  started_at: new Date().toISOString(), last_activity_at: new Date().toISOString(),
  bytes_up: 0, bytes_down: 0,
};

describe("identifySession", () => {
  it("leads with the room number for a room-authenticated session", () => {
    const id = identifySession({
      ...base, subject_kind: "room", subject_label: "412", room: "412",
      subject_name: "Anderson", external_reservation_id: "R8831",
    });
    expect(id.title).toBe("Room 412");
    expect(id.subtitle).toBe("Anderson");
    expect(id.anonymous).toBe(false);
  });

  it("falls back to the reservation when the PMS sent no guest name", () => {
    const id = identifySession({
      ...base, subject_kind: "room", room: "318", external_reservation_id: "R9001",
    });
    expect(id.title).toBe("Room 318");
    expect(id.subtitle).toBe("Reservation R9001");
  });

  it("leads with the username for an account session", () => {
    const id = identifySession({
      ...base, subject_kind: "account", subject_label: "conf-desk", subject_name: "Conference desk",
    });
    expect(id.title).toBe("conf-desk");
    expect(id.subtitle).toBe("Conference desk");
  });

  // Voucher codes require audited reveal; this unattended session list has no privilege to read them.
  it("names a voucher session without ever showing a code, and points at where one can be recovered", () => {
    const id = identifySession({ ...base, subject_kind: "voucher" });
    expect(id.title).toBe("Voucher");
    expect(id.subtitle).toMatch(/not shown here/i);
    expect(id.subtitle).toMatch(/records who looked/i);
    expect(id.anonymous).toBe(false);
    expect(JSON.stringify(id)).not.toMatch(/code_last4|code_hmac/);
  });

  it("describes an email/social sign-in by the method, since no identity is readable", () => {
    const id = identifySession({ ...base, subject_kind: "guest", credential_method: "EMAIL_OTP" });
    expect(id.title).toBe("Client sign-in");
    expect(id.subtitle).toBe("Email code (former)");
  });

  // Mark MAC-only identity as fallback when entitlement cannot be read.
  it("marks a session with no readable subject as anonymous and falls back to the device", () => {
    const id = identifySession({ ...base, subject_kind: "" });
    expect(id.title).toBe("a4:83:e7:2f:11:c0");
    expect(id.anonymous).toBe(true);
  });

  it("falls back to the IP when even the MAC is absent", () => {
    const id = identifySession({ ...base, subject_kind: "", mac: "" });
    expect(id.title).toBe("10.80.4.62");
    expect(id.anonymous).toBe(true);
  });
});

describe("operator wording", () => {
  it("names every credential method in product words", () => {
    expect(methodLabel("ROOM")).toBe("Room number and name");
    expect(methodLabel("VOUCHER")).toBe("Voucher code");
    expect(methodLabel("SMS_OTP")).toBe("Texted code");
    expect(methodLabel(null)).toBe("—");
    // Keep unknown connector methods visible.
    expect(methodLabel("SOME_NEW_METHOD")).toBe("some new method");
  });

  it("calls PENDING_ENFORCEMENT what the operator is actually watching", () => {
    expect(stateWords("PENDING_ENFORCEMENT")).toEqual({ label: "Connecting", tone: "warn" });
    expect(stateWords("active")).toEqual({ label: "Online", tone: "ok" });
    expect(stateWords("ended").label).toBe("Ended");
    expect(stateWords("closed").label).toBe("Ended");
  });

  it("explains why a session ended", () => {
    expect(endReasonWords("DATA")).toBe("Data allowance used up");
    expect(endReasonWords("CHECKOUT")).toBe("Guest checked out");
    expect(endReasonWords("admin")).toBe("Disconnected by an operator");
    expect(endReasonWords(null)).toBeUndefined();
  });

  it("renders a speed pair in the units an operator reads, and nothing when there is no plan", () => {
    expect(speedPair(10000, 5000)).toBe("10 Mbps down · 5 Mbps up");
    expect(speedPair(2500, 512)).toBe("2.5 Mbps down · 512 kbps up");
    // Missing plan numbers leave room for the caller to show the plan code.
    expect(speedPair(null, null)).toBeUndefined();
    expect(speedPair(0, 0)).toBeUndefined();
  });
});
