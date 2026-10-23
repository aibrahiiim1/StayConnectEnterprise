import { describe, it, expect } from "vitest";
import { LIVE_REASONS, eraseConsequences, exportDescription } from "@/lib/guest-data-words";

// Room credentials prove eligibility, not identity: what staff are told a request covers must match the database, never
// shared access.

describe("what a guest-data request says it covers", () => {
  it("a stay request covers the reservation and says the room's access is shared and left alone", () => {
    for (const kind of ["stay", "stay_guest"]) {
      const exp = exportDescription(kind);
      expect(exp).toMatch(/reservation/i);
      expect(exp).toMatch(/shared by everyone in the room/i);
      const text = eraseConsequences(kind).join(" ");
      expect(text).toMatch(/Only the reservation goes/);
      expect(text).toMatch(/left alone/);
      expect(text).toMatch(/Accounts linked to the stay stay too/);
    }
  });

  it("an account or identity request covers the person, not the reservation or the room", () => {
    for (const kind of ["account", "principal"]) {
      const exp = exportDescription(kind);
      expect(exp).toMatch(/own data/i);
      expect(exp).toMatch(/not included/i);
      const text = eraseConsequences(kind).join(" ");
      expect(text).toMatch(/Only this person’s own records go/);
      expect(text).toMatch(/reservation an account is linked to/);
      expect(text).not.toMatch(/Linked records go too/);
    }
  });

  it("shared access is the hotel's to handle by hand, and retention expiry is never offered as the answer", () => {
    for (const kind of ["account", "principal", "stay", "stay_guest"]) {
      expect(exportDescription(kind)).toMatch(/by hand/);
      const text = eraseConsequences(kind).join(" ");
      expect(text).toMatch(/by hand/);
      expect(text).toMatch(/retention period (ending )?is not an answer/);
      expect(text).not.toMatch(/follows? the retention period/);
    }
  });

  it("every erasure still warns it cannot be undone and is refused while the guest is live", () => {
    for (const kind of ["account", "principal", "stay", "stay_guest", undefined]) {
      const text = eraseConsequences(kind).join(" ");
      expect(text).toMatch(/cannot be undone/);
      expect(text).toMatch(/Refused while the guest is in-house/);
    }
  });

  it("every reason the database can refuse with has plain words, and none promises the guest's own access for a reservation", () => {
    for (const code of ["co_guests", "pms_open", "pms_pending", "access_live", "online", "signin_restricted", "other_site", "busy"]) {
      expect(LIVE_REASONS[code], code).toBeTruthy();
    }
    expect(LIVE_REASONS.access_live).toMatch(/room/i);
    expect(LIVE_REASONS.online).toMatch(/room/i);
  });
});
