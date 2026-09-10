// THE SENTENCES THAT REPLACED "Cloud sync outbox  69572 pending  8424 dead".
//
// That line was the specific complaint that produced lib/health-words. Three words and two integers, with nothing
// on the screen saying what an outbox is, what "dead" means, whether 8,424 is a catastrophe, or — the only
// question an operator actually has — whether any guest is affected.
//
// The tests that matter here are the last two in each block: the reassurance must be present on EVERY outbox
// answer, and the tone must escalate on shape rather than on size. A single source feeds the dashboard, the header
// pill and the Cloud connection page, so these assertions are what stops those three disagreeing.

import { describe, it, expect } from "vitest";
import {
  describeOutbox, describeDatabase, describeSessionController, describeLicense, describePmsReadiness,
} from "@/lib/health-words";

describe("describeOutbox", () => {
  it("says nothing is queued when the appliance does not report upward at all", () => {
    const r = describeOutbox({ enabled: false });
    expect(r.tone).toBe("default");
    expect(r.headline).toBe("Not in use");
    expect(r.summary).toMatch(/do not depend on it/i);
  });

  it("reports an empty queue as up to date", () => {
    const r = describeOutbox({ enabled: true, pending: 0, dead: 0 });
    expect(r.tone).toBe("ok");
    expect(r.headline).toBe("Up to date");
  });

  it("escalates on SHAPE, not on size", () => {
    // A few hundred items mid-flight is an ordinary busy period. Tens of thousands means nothing has drained for a
    // long time, which is a connection problem rather than a busy one — a different response, so a different tone.
    expect(describeOutbox({ enabled: true, pending: 300, dead: 0 }).tone).toBe("ok");
    expect(describeOutbox({ enabled: true, pending: 2_000, dead: 0 }).tone).toBe("warn");
    expect(describeOutbox({ enabled: true, pending: 69_572, dead: 0 }).tone).toBe("err");
    // "Dead" is never ordinary: it is work the appliance has abandoned, so it is never reported as ok.
    expect(describeOutbox({ enabled: true, pending: 0, dead: 1 }).tone).toBe("warn");
  });

  it("renders the exact figures from the complaint in words, with no jargon", () => {
    const r = describeOutbox({ enabled: true, pending: 69_572, dead: 8_424 });
    expect(r.headline).toBe("69,572 waiting · 8,424 given up");
    expect(r.summary).toMatch(/69,572 records are waiting to be sent/i);
    expect(r.summary).toMatch(/8,424 records were retried until the appliance gave up/i);
    expect(r.summary).toMatch(/not draining/i);
    // The vocabulary an operator cannot act on must be gone from what they read.
    expect(r.headline + r.summary).not.toMatch(/dead[- ]letter|outbox/i);
  });

  // THE ANSWER TO THE QUESTION THE NUMBERS PROVOKE. Whatever the figures, the operator must be told whether
  // guests are affected — and they are not, because this queue is cloud reporting and nothing else.
  it("always states that guests are unaffected", () => {
    for (const o of [
      { enabled: true, pending: 0, dead: 0 },
      { enabled: true, pending: 500, dead: 0 },
      { enabled: true, pending: 69_572, dead: 8_424 },
      { enabled: false },
    ]) {
      expect(describeOutbox(o).summary, JSON.stringify(o)).toMatch(/not affected|do not depend on it/i);
    }
  });

  it("treats a missing figure as zero rather than rendering undefined", () => {
    const r = describeOutbox({ enabled: true });
    expect(r.headline).toBe("Up to date");
    expect(r.summary).not.toMatch(/undefined|NaN/);
  });
});

describe("the service descriptions say what STOPS, not what the process is called", () => {
  it("describes the database by what depends on it", () => {
    expect(describeDatabase(true).tone).toBe("ok");
    const down = describeDatabase(false);
    expect(down.tone).toBe("err");
    expect(down.summary).toMatch(/guest sign-in/i);
  });

  it("describes the session controller by its effect, never by its process name", () => {
    const down = describeSessionController(false);
    expect(down.summary).toMatch(/cannot be connected/i);
    expect(down.summary + down.headline).not.toMatch(/\bscd\b/);
  });
});

describe("describeLicense", () => {
  it("never reports an uninstalled licence as Active", () => {
    // The permissive unlicensed-dev licstate reports state="Active" with NO licence installed. Rendering that as
    // Active told an operator a commissioning appliance was activated.
    const r = describeLicense("Active", false);
    expect(r.headline).toBe("Pending activation");
    expect(r.tone).toBe("warn");
  });

  it("distinguishes grace from expiry, because the response differs", () => {
    expect(describeLicense("GracePeriod", true).tone).toBe("warn");
    expect(describeLicense("GracePeriod", true).summary).toMatch(/keeps working/i);
    expect(describeLicense("Expired", true).tone).toBe("err");
  });

  it("passes an unrecognised state through instead of inventing one", () => {
    expect(describeLicense("SomeFutureState", true).headline).toBe("SomeFutureState");
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
    expect(r.summary).toMatch(/vouchers and guest accounts still work/i);
  });

  it("reports a refresh in progress as temporary rather than broken", () => {
    const r = describePmsReadiness({ transport: "CONNECTED", sync: "RESYNC_IN_PROGRESS", roomAuthReady: false });
    expect(r.tone).toBe("warn");
    expect(r.summary).toMatch(/resumes automatically/i);
  });
});
