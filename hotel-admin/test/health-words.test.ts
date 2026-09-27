// THE SENTENCES lib/health-words PUTS ON THE DASHBOARD, THE HEADER PILL AND DIAGNOSTICS.
//
// A single source feeds all three, so these assertions are what stops them disagreeing. (The cloud-sync outbox
// sentences that started this file are gone with the cloud telemetry subsystem itself.)

import { describe, it, expect } from "vitest";
import {
  describeDatabase, describeSessionController, describeLicense, describePmsReadiness,
} from "@/lib/health-words";

describe("the service descriptions say what STOPS, not what the process is called", () => {
  it("describes the database by what depends on it", () => {
    expect(describeDatabase(true).tone).toBe("ok");
    const down = describeDatabase(false);
    expect(down.tone).toBe("err");
    expect(down.summary).toMatch(/client sign-in/i);
  });

  it("describes the session controller by its effect, never by its process name", () => {
    const down = describeSessionController(false);
    expect(down.summary).toMatch(/cannot be connected/i);
    expect(down.summary + down.headline).not.toMatch(/\bscd\b/);
  });
});

describe("describeLicense", () => {
  // The appliance's own section 8 vocabulary. This used to key on "Unlicensed" while the appliance sent
  // "unlicensed", and it said an unlicensed appliance "runs in a permissive mode" -- which it does not.
  it("words a missing licence as not activated, and never as permissive", () => {
    for (const s of ["none", null, undefined]) {
      const r = describeLicense(s as any);
      expect(r.headline).toBe("Not activated");
      expect(r.tone).toBe("warn");
      expect(r.summary).not.toMatch(/permissive/i);
      expect(r.summary).toMatch(/cannot sign in/i);
    }
  });

  it("distinguishes grace from expiry, because the response differs", () => {
    expect(describeLicense("grace").tone).toBe("warn");
    expect(describeLicense("grace").summary).toMatch(/keep signing in/i);
    expect(describeLicense("expired").tone).toBe("err");
    expect(describeLicense("expiring").tone).toBe("warn");
  });

  // GRACE IS A LAPSED LICENCE, NOT A FLAKY INTERNET CONNECTION.
  it("never blames the cloud for a grace period, and never promises a Restricted state", () => {
    const grace = describeLicense("grace").summary;
    expect(grace).toMatch(/licence end date has passed/i);
    expect(grace).not.toMatch(/cloud/i);
    expect(grace).not.toMatch(/offline/i);
    expect(grace).not.toMatch(/restrict/i);
  });

  it("says what happens to guests in grace and after it", () => {
    expect(describeLicense("grace").summary).toMatch(/new sign-ins stop/i);
    for (const s of ["expired", "suspended", "revoked"]) {
      expect(describeLicense(s).summary, s).toMatch(/already online are not disconnected/i);
    }
    expect(describeLicense("wrong_hardware").summary).toMatch(/different appliance/i);
  });

  it("passes an unrecognised state through instead of inventing one", () => {
    expect(describeLicense("SomeFutureState").headline).toBe("SomeFutureState");
  });
});

describe("describePmsReadiness states the consequence for the guest", () => {
  it("confirms room sign-in works and names the roster size", () => {
    const r = describePmsReadiness({ transport: "CONNECTED", sync: "IN_SYNC", roomAuthReady: true, inHouse: 461 });
    expect(r.tone).toBe("ok");
    expect(r.summary).toMatch(/461 in house/);
    expect(r.summary).toMatch(/room number and name/i);
  });

  it("says what still works when the PMS is down", () => {
    // The front desk must not start handing out workarounds for capabilities that are unaffected.
    const r = describePmsReadiness({ transport: "DISCONNECTED", roomAuthReady: false });
    expect(r.tone).toBe("err");
    expect(r.summary).toMatch(/vouchers and client accounts still work/i);
  });

  it("reports a refresh in progress as temporary rather than broken", () => {
    const r = describePmsReadiness({ transport: "CONNECTED", sync: "RESYNC_IN_PROGRESS", roomAuthReady: false });
    expect(r.tone).toBe("warn");
    expect(r.summary).toMatch(/resumes automatically/i);
  });
});
