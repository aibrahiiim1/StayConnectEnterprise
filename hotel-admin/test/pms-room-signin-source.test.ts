import { describe, expect, it } from "vitest";
import { describePmsReadiness } from "@/lib/health-words";
import { ROOM_AUTH_WORDS } from "@/lib/api/pms-connections";

// D69: while a refresh runs (or the PMS is away) Room sign-in answers from the last complete guest list, and the
// console must say so instead of claiming a live feed or an outage.
describe("Room sign-in readiness wording follows the server's source", () => {
  it("names the last guest list during a connected refresh", () => {
    const w = describePmsReadiness({ transport: "CONNECTED", sync: "RESYNC_IN_PROGRESS", roomAuthReady: true, source: "LAST_GOOD_ROSTER", inHouse: 482 });
    expect(w.headline).toBe("Room sign-in working while the guest list refreshes");
    expect(w.summary).toMatch(/last complete guest list/);
    expect(w.summary).not.toMatch(/connected and its guest list is loaded/);
    expect(w.tone).toBe("warn");
  });

  it("names the last guest list while the PMS is unreachable", () => {
    const w = describePmsReadiness({ transport: "DISCONNECTED", sync: "RESYNC_REQUIRED", roomAuthReady: true, source: "LAST_GOOD_ROSTER" });
    expect(w.headline).toBe("Room sign-in working without the PMS");
    expect(w.summary).toMatch(/not connected/);
  });

  it("keeps the live wording for a live feed", () => {
    const w = describePmsReadiness({ transport: "CONNECTED", sync: "IN_SYNC", roomAuthReady: true, source: "LIVE_FEED", inHouse: 3 });
    expect(w.headline).toBe("Room sign-in working");
    expect(w.tone).toBe("ok");
  });

  it("still reports an outage when the server says not ready", () => {
    const w = describePmsReadiness({ transport: "CONNECTED", sync: "RESYNC_IN_PROGRESS", roomAuthReady: false });
    expect(w.headline).toBe("Loading the guest list");
    expect(w.tone).not.toBe("ok");
  });

  it("words a first load without promising a refresh fixes it", () => {
    expect(ROOM_AUTH_WORDS.NOT_IN_SYNC).toMatch(/first guest list is still loading/);
    expect(ROOM_AUTH_WORDS.NOT_IN_SYNC).not.toMatch(/refresh will fix/);
  });
});
