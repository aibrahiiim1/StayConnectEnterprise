import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

// THE THREE THINGS THAT WERE DISPLAYED BUT NOT DOABLE.
//
// Retention and the nightly schedule were shown and not editable. The logo and background were URL-only, on a
// page a guest reaches precisely because they have no internet. The language selector changed nothing at all.
// Each of these told an operator something was under their control when it was not.

const get = vi.fn();
const put = vi.fn();
const post = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    put: (...a: any[]) => put(...a),
    post: (...a: any[]) => post(...a),
    del: (...a: any[]) => del(...a),
  },
  ApiError: class ApiError extends Error {},
}));

beforeEach(() => { get.mockReset(); put.mockReset(); post.mockReset(); del.mockReset(); });
afterEach(() => vi.resetModules());

/** The schema the appliance serves, matching stayconnect-backup-cleanup's own defaults. */
const SETTINGS = {
  retention: [
    { key: "KEEP_DB", label: "Database backups", unit: "backups", default: 7, min: 1, max: 365, value: 7, explains: "How many to retain." },
    { key: "KEEP_BINARIES", label: "Service binaries", unit: "versions", default: 5, min: 2, max: 30, value: 5, explains: "Rollback targets." },
    { key: "DISK_WARN", label: "Disk warning threshold", unit: "% used", default: 80, min: 50, max: 95, value: 80, explains: "Warn here." },
    { key: "DISK_CRIT", label: "Disk critical threshold", unit: "% used", default: 90, min: 60, max: 99, value: 90, explains: "Alarm here." },
  ],
  schedule: "03:30",
  schedule_known: true,
  config_present: false,
};

function mockBackups() {
  get.mockImplementation((p: string) => {
    if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (p === "/backups/health") return Promise.resolve({
      retention_readable: true, retention: { disk_pct: 40, retained: 8, protected: 7, pinned: 0 },
      timer_active: true, timer_readable: true, database_backups: 1,
    });
    if (p === "/backups/artifacts") return Promise.resolve({ data: [], meta: { has_more: false } });
    if (p === "/backups/settings") return Promise.resolve(SETTINGS);
    return Promise.resolve({});
  });
}

describe("backup retention is editable, not just visible", () => {
  it("shows every knob with its unit, bounds and default", async () => {
    mockBackups();
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);

    const field = await screen.findByLabelText(/Database backups/i);
    expect((field as HTMLInputElement).value).toBe("7");
    // An operator cannot set a value responsibly without knowing what it does and what it may be.
    expect(screen.getByText(/Allowed 1–365; default 7/)).toBeTruthy();
    expect(await screen.findByLabelText(/Nightly sweep runs at/i)).toBeTruthy();
  });

  it("says the defaults are in force when nothing has been saved", async () => {
    // A freshly onboarded appliance has no config file. "No policy saved" and "no policy in force" are
    // different claims, and only the first is true.
    mockBackups();
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    expect(await screen.findByText(/built-in defaults below are in force/i)).toBeTruthy();
  });

  it("sends the edited policy and the schedule under a password step-up", async () => {
    mockBackups();
    put.mockResolvedValue({ ...SETTINGS, config_present: true, schedule: "02:15" });
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);

    fireEvent.change(await screen.findByLabelText(/Database backups/i), { target: { value: "30" } });
    fireEvent.change(screen.getByLabelText(/Nightly sweep runs at/i), { target: { value: "02:15" } });
    fireEvent.change(await screen.findByLabelText(/Confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /Save retention policy/i }));

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const [path, body] = put.mock.calls[0];
    expect(path).toBe("/backups/settings");
    expect(body.retention.KEEP_DB).toBe(30);
    // Untouched knobs travel too: sending only what changed would let the appliance rewrite the rest to
    // defaults it never asked for.
    expect(body.retention.KEEP_BINARIES).toBe(5);
    expect(body.schedule).toBe("02:15");
    expect(body.password).toBe("pw");
  });

  it("offers nothing to save until something actually changed", async () => {
    mockBackups();
    const Page = (await import("@/app/(app)/backups/page")).default;
    render(<Page />);
    await screen.findByLabelText(/Database backups/i);
    expect(screen.queryByRole("button", { name: /Save retention policy/i })).toBeNull();
  });
});

describe("the portal's images live on the appliance", () => {
  function mockBranding(design: Record<string, any> = {}) {
    get.mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      if (p === "/portal-branding") return Promise.resolve({ design, draft: {}, revisions: [], published: false });
      if (p === "/portal-assets") return Promise.resolve({ data: [], meta: { has_more: false } });
      return Promise.resolve({});
    });
  }

  it("offers an upload rather than asking for a URL", async () => {
    mockBranding();
    const Page = (await import("@/app/(app)/portal-branding/page")).default;
    render(<Page />);
    expect(await screen.findByLabelText(/Upload logo/i)).toBeTruthy();
    expect(screen.getByLabelText(/Upload background photograph/i)).toBeTruthy();
  });

  it("says why SVG is refused, before an operator tries it", async () => {
    // The refusal is a security boundary, not a format preference, and an operator who is told only "not
    // supported" will keep trying to find the supported way to ship their vector logo.
    mockBranding();
    const Page = (await import("@/app/(app)/portal-branding/page")).default;
    render(<Page />);
    const notes = await screen.findAllByText(/SVG is refused: it can carry script/i);
    expect(notes.length).toBeGreaterThan(0);
  });
});

describe("the language selector is backed by real words", () => {
  function mockBranding(design: Record<string, any>) {
    get.mockImplementation((p: string) => {
      if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
      if (p === "/portal-branding") return Promise.resolve({ design, draft: {}, revisions: [], published: true });
      if (p === "/portal-assets") return Promise.resolve({ data: [], meta: { has_more: false } });
      return Promise.resolve({});
    });
  }

  it("offers the six shipped languages without anyone typing a word", async () => {
    // The old screen started empty and made offering Arabic a data-entry project. The portal ships the words,
    // so this screen only has to let an operator choose.
    mockBranding({});
    const Page = (await import("@/app/(app)/portal-branding/page")).default;
    render(<Page />);

    for (const native of ["English", "العربية", "Deutsch", "Français", "Italiano", "Русский"]) {
      expect((await screen.findAllByText(native)).length).toBeGreaterThan(0);
    }
    // English cannot be switched off: it is what everything else falls back to.
    expect((screen.getByLabelText(/Offer English to guests/i) as HTMLInputElement).disabled).toBe(true);
  });

  it("edits one language at a time rather than stacking six", async () => {
    // Six languages rendered as six blocks was ~300 inputs down one column. A tab strip picks one.
    mockBranding({});
    const Page = (await import("@/app/(app)/portal-branding/page")).default;
    render(<Page />);

    const tabs = await screen.findAllByRole("tab");
    expect(tabs.length).toBe(6);

    fireEvent.click(screen.getByRole("tab", { name: /العربية/ }));
    // The fields an operator fills in are the guest-facing strings, named in English rather than as keys, and
    // labelled with the language so two languages can never be confused for one another.
    expect(await screen.findByLabelText(/Room Number in العربية/)).toBeTruthy();
    expect(screen.getByLabelText(/Voucher Code in العربية/)).toBeTruthy();
    expect(screen.getByLabelText(/Use Personal Account in العربية/)).toBeTruthy();
    // And only that one language's fields are on screen.
    expect(screen.queryByLabelText(/Room Number in Deutsch/)).toBeNull();
  });

  it("says how much of an added language will show in English", async () => {
    // A shipped language is never missing a string. One the hotel adds starts with nothing, and an operator
    // needs to see that before guests do.
    mockBranding({});
    const Page = (await import("@/app/(app)/portal-branding/page")).default;
    render(<Page />);

    fireEvent.change(await screen.findByLabelText(/Language code/i), { target: { value: "es" } });
    fireEvent.change(screen.getByLabelText(/Shown as/i), { target: { value: "Español" } });
    fireEvent.click(screen.getByRole("button", { name: /Add language/i }));

    const tab = await screen.findByRole("tab", { name: /Español/ });
    expect(within(tab).getByText(/\d+ in English/)).toBeTruthy();
    // Filling one string in reduces the count rather than leaving the badge stale.
    const before = Number(within(tab).getByText(/(\d+) in English/).textContent!.match(/\d+/)![0]);
    fireEvent.change(screen.getByLabelText(/Room Number in Español/), { target: { value: "Número de habitación" } });
    await waitFor(() =>
      expect(Number(within(tab).getByText(/(\d+) in English/).textContent!.match(/\d+/)![0])).toBe(before - 1),
    );
  });

  it("publishes the translations with the design, so the selector has something to switch to", async () => {
    mockBranding({ hotel_name: "Coral Sea", translations: { ar: { "pms.room": "رقم الغرفة" } } });
    post.mockResolvedValue({ version: 2 });
    const Page = (await import("@/app/(app)/portal-branding/page")).default;
    render(<Page />);

    fireEvent.change(await screen.findByLabelText(/Hotel name/i), { target: { value: "Coral Sea Resort" } });
    fireEvent.click(screen.getByRole("button", { name: /Publish to guests/i }));
    fireEvent.change(await screen.findByLabelText(/Confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /Confirm and publish/i }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const body = post.mock.calls[0][1];
    // The words travel with the design. A published design without them is the selector that changes nothing.
    expect(body.design.translations.ar["pms.room"]).toBe("رقم الغرفة");
  });
});
