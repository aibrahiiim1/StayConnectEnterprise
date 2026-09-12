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
  // WHY THE QUEUE IS NOT DRAINING IS A FACT, NOT AN INFERENCE FROM ITS SIZE.
  //
  // The old rule read the pending count and concluded "the queue is not draining — check Cloud connection".
  // On the appliance that produced this work the network was fine, the appliance held an established
  // mutually-authenticated connection to the cloud, and nothing drained because nothing at the far end was
  // listening. That sentence sent an operator to the wrong system for an afternoon. These four cases are the
  // four different problems it could not tell apart.
  it("names a severed link as a link problem, and says the records are safe", () => {
    const r = describeOutbox({ enabled: true, pending: 5000, dead: 0, delivery: { state: "TRANSPORT_UNAVAILABLE" } });
    expect(r.headline).toMatch(/no connection to the cloud/i);
    expect(r.summary).toMatch(/kept safely/i);
    expect(r.tone).toBe("warn");
  });

  it("names an absent receiver as a CLOUD-side problem, and exonerates the hotel network", () => {
    const r = describeOutbox({ enabled: true, pending: 5000, dead: 0, delivery: { state: "RECEIVER_UNAVAILABLE" } });
    expect(r.headline).toMatch(/not listening/i);
    expect(r.summary).toMatch(/cloud-side problem/i);
    expect(r.summary).toMatch(/hotel network is not the cause/i);
    expect(r.tone).toBe("err");
  });

  it("says a refusal will not be fixed by waiting", () => {
    const r = describeOutbox({ enabled: true, pending: 10, dead: 0, delivery: { state: "RECEIVER_REJECTED" } });
    expect(r.summary).toMatch(/retrying will not change that/i);
    expect(r.tone).toBe("err");
  });

  it("does not call a large queue broken when it is actually draining", () => {
    // The case the size rule got backwards: 60,000 records mid-recovery is progress, not a fault.
    const r = describeOutbox({ enabled: true, pending: 60_000, dead: 0, delivery: { state: "DRAINING", sent: 100 } });
    expect(r.summary).toMatch(/being sent now, oldest first/i);
    expect(r.summary).not.toMatch(/not draining/i);
    expect(r.tone).toBe("warn"); // still worth noticing at that depth, but not an error
  });

  // Records the appliance gave up on used to be a dead end: the number went up and no screen offered a way
  // to act on it. The sentence must now say they will not move on their own, and where the button is.
  it("says abandoned records need recovering, and where", () => {
    const r = describeOutbox({ enabled: true, pending: 0, dead: 9395, delivery: { state: "IDLE" } });
    expect(r.summary).toMatch(/gave up on them/i);
    expect(r.summary).toMatch(/will not be sent again until they are recovered/i);
    expect(r.summary).toMatch(/cloud connection/i);
    expect(r.tone).toBe("warn");
  });

  it("keeps the guest reassurance on every answer, including the new states", () => {
    for (const state of ["TRANSPORT_UNAVAILABLE", "RECEIVER_UNAVAILABLE", "RECEIVER_REJECTED", "DRAINING", "IDLE", "UNKNOWN"] as const) {
      const r = describeOutbox({ enabled: true, pending: 1, dead: 0, delivery: { state } });
      expect(r.summary, state).toMatch(/not affected by this queue/i);
    }
  });

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

  // REWRITTEN, AND THE OLD EXPECTATION WAS THE DEFECT. This asserted that 69,572 waiting is an ERROR on the
  // strength of the number alone. It is not: the same depth is an ordinary recovery when the queue is
  // draining, and a genuine error when nothing at the far end is listening. Size now sets a floor on how
  // loud the message is; the EVIDENCE decides what it says and how bad it is.
  it("escalates on EVIDENCE, with size only setting a floor", () => {
    expect(describeOutbox({ enabled: true, pending: 300, dead: 0, delivery: { state: "IDLE" } }).tone).toBe("ok");
    // A large queue that is moving is not an error — it is a recovery in progress.
    expect(describeOutbox({ enabled: true, pending: 69_572, dead: 0, delivery: { state: "DRAINING" } }).tone).toBe("warn");
    // The same depth with nothing listening at the far end IS an error, and a different person's problem.
    expect(describeOutbox({ enabled: true, pending: 69_572, dead: 0, delivery: { state: "RECEIVER_UNAVAILABLE" } }).tone).toBe("err");
    // "Given up on" is never ordinary: it is work the appliance abandoned, outside the retry cycle entirely.
    expect(describeOutbox({ enabled: true, pending: 0, dead: 1, delivery: { state: "IDLE" } }).tone).toBe("warn");
  });

  it("renders the exact figures from the complaint in words, with no jargon", () => {
    const r = describeOutbox({ enabled: true, pending: 69_572, dead: 8_424, delivery: { state: "IDLE" } });
    expect(r.headline).toBe("69,572 waiting · 8,424 given up");
    expect(r.summary).toMatch(/69,572 records are waiting to be sent/i);
    expect(r.summary).toMatch(/8,424 records were retried until the appliance gave up/i);
    // "not draining" is gone on purpose: it was an inference from the queue's size that named the wrong
    // cause. What replaces it is what the last attempt actually learned, asserted state by state above.
    expect(r.summary).toMatch(/will not be sent again until they are recovered/i);
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
