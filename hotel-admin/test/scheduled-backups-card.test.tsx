import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, cleanup, fireEvent, within } from "@testing-library/react";
import { backupHeadline, backupKindLabel, SCHEDULE_SETTING_KEYS, type BackupStatus } from "@/lib/ledger-words";

// The Scheduled backups card: OFF by default, one source of settings, manual backups, the kinds of backup, the earlier backups that
// only an administrator can remove, and the plain statement that the protections before a restore, a migration or a deployment do not
// depend on the switch.

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: (...a: any[]) => put(...a), del: vi.fn(), postRaw: vi.fn() } };
});

const SETTINGS = {
  retention: [
    { key: "BACKUP_ENABLED", label: "Scheduled backups", unit: "0 = off, 1 = on", default: 0, min: 0, max: 1, value: 0, explains: "Off until you switch it on." },
    { key: "BACKUP_HOUR", label: "Scheduled backups: hour of the day", unit: "o'clock", default: 2, min: 0, max: 23, value: 2, explains: "At or after this hour." },
    { key: "BACKUP_EVERY_DAYS", label: "Scheduled backups: every", unit: "days", default: 1, min: 1, max: 7, value: 1, explains: "Days between." },
    { key: "BACKUP_KEEP", label: "Backups kept", unit: "backups", default: 7, min: 1, max: 30, value: 7, explains: "How many." },
    { key: "SAFETY_BACKUP_KEEP", label: "Safety backups kept", unit: "backups", default: 3, min: 2, max: 20, value: 3, explains: "Before a change." },
    { key: "KEEP_BINARIES", label: "Service binaries", unit: "versions", default: 5, min: 2, max: 30, value: 5, explains: "Rollback targets." },
  ],
  schedule: "03:30", schedule_known: true, config_present: false,
};

const status: BackupStatus = {
  state: "off", message: "", key_holders: true,
  schedule: { enabled: false, hour: 2, every_days: 1, keep: 7, safety_keep: 3, next: null },
  backups: [
    { name: "db-20261020T020000Z.sql.gz.age", kind: "manual", by: "ops@hotel", size_bytes: 5242880, taken_at: "2026-10-20T02:00:00Z", tables: 150, restorable: true, ledger_seq: 12, etc_files: 30 },
    { name: "db-20261019T020000Z.sql.gz.age", kind: "pre-change", reason: "migration 0149", size_bytes: 5242880, taken_at: "2026-10-19T02:00:00Z", tables: 150, restorable: true, ledger_seq: 11, etc_files: 30 },
  ],
  problems: [],
  earlier: { count: 2, size_bytes: 20971520, unencrypted: 1, oldest: "2026-08-01T00:00:00Z", sets: [
    { name: "site-20260801-020000", kind: "nightly set", taken: "2026-08-01T02:00:00Z", size_bytes: 10485760, encrypted: false, restorable_here: false },
    { name: "db-20260802T020000Z.sql.gz", kind: "earlier dump", taken: "2026-08-02T02:00:00Z", size_bytes: 10485760, encrypted: true, restorable_here: true },
  ] },
};

let st: BackupStatus;
beforeEach(() => {
  get.mockReset(); post.mockReset(); put.mockReset();
  st = status;
  get.mockImplementation((path: string) => Promise.resolve(path === "/backups/status" ? st : path === "/backups/settings" ? SETTINGS : {}));
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderCard(writable = true) {
  const { ScheduledBackupsCard } = await import("@/app/(app)/backups/scheduled-backups-card");
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><ScheduledBackupsCard writable={writable} /></ToastProvider>);
}

describe("Backup words", () => {
  it("says off plainly and that the protections do not depend on it", () => {
    const h = backupHeadline(st);
    expect(h.title).toBe("Scheduled backups are off");
    expect(h.detail).toContain("always taken");
    expect(backupHeadline({ ...st, state: "needs_key_holder" }).tone).toBe("err");
    expect(backupHeadline({ ...st, state: "ok", schedule: { ...st.schedule, enabled: true, keep: 7, every_days: 3 } }).detail).toContain("every 3 days");
    expect(backupHeadline({ ...st, state: "waiting" }).detail).toContain("02:00");
    expect(backupKindLabel("pre-change")).toBe("Before a change");
    expect(backupKindLabel("earlier")).toBe("Earlier backup");
    expect(SCHEDULE_SETTING_KEYS).toContain("BACKUP_ENABLED");
  });
});

describe("Scheduled backups card", () => {
  it("is OFF by default and offers no save until something changes", async () => {
    await renderCard();
    expect((await screen.findAllByText("Scheduled backups are off")).length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: /save backup settings/i })).toBeNull();
    expect(screen.queryByLabelText(/service binaries/i)).toBeNull(); // the sweep values are not on this card
  });

  it("switches on and saves all five settings under a password step-up", async () => {
    put.mockResolvedValue({ ok: true });
    await renderCard();
    fireEvent.click(await screen.findByRole("switch", { name: /scheduled backups/i }));
    fireEvent.change(screen.getByLabelText("Backups kept"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: /save backup settings/i }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /^save$/i }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const [path, body] = put.mock.calls[0];
    expect(path).toBe("/backups/settings");
    expect(body.retention).toEqual({ BACKUP_ENABLED: 1, BACKUP_HOUR: 2, BACKUP_EVERY_DAYS: 1, BACKUP_KEEP: 10, SAFETY_BACKUP_KEEP: 3 });
    expect(body.password).toBe("pw");
  });

  it("refuses a retention outside its safe limits", async () => {
    await renderCard();
    fireEvent.change(await screen.findByLabelText("Backups kept"), { target: { value: "99" } });
    expect((screen.getByRole("button", { name: /save backup settings/i }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("lists each backup with its kind, reason and who asked", async () => {
    await renderCard();
    expect(await screen.findByText("Manual")).toBeTruthy();
    expect(screen.getByText("Before a change")).toBeTruthy();
    expect(screen.getByText(/migration 0149/)).toBeTruthy();
    expect(screen.getByText(/by ops@hotel/)).toBeTruthy();
  });

  it("takes a manual backup with the password", async () => {
    post.mockResolvedValue({ name: "db-1" });
    await renderCard();
    fireEvent.click(await screen.findByRole("button", { name: /take a backup now/i }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /^take a backup$/i }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/backups/run", { password: "pw" }));
  });

  it("cannot take a backup with no key holder", async () => {
    st = { ...status, state: "needs_key_holder", key_holders: false, backups: [] };
    await renderCard();
    expect(await screen.findByText("A backup key holder is required")).toBeTruthy();
    expect((screen.getByRole("button", { name: /take a backup now/i }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("shouts when a stored backup no longer matches its checksum", async () => {
    st = { ...status, state: "attention", problems: [{ name: "db-1.sql.gz.age", detail: "its contents changed since it was verified" }] };
    await renderCard();
    expect(await screen.findByText("A stored backup changed")).toBeTruthy();
  });

  it("lists the earlier backups and removes them only with the password AND the typed confirmation", async () => {
    post.mockResolvedValue({ removed_sets: 2 });
    await renderCard();
    expect(await screen.findByText(/Earlier backups \(2/)).toBeTruthy();
    expect(screen.getByText(/1 of them are not encrypted/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /remove the earlier backups/i }));
    const dialog = await screen.findByRole("dialog");
    const go = within(dialog).getByRole("button", { name: /remove them/i }) as HTMLButtonElement;
    fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "pw" } });
    expect(go.disabled).toBe(true);
    fireEvent.change(within(dialog).getByLabelText(/to confirm/i), { target: { value: "remove earlier backups" } });
    await waitFor(() => expect(go.disabled).toBe(false));
    fireEvent.click(go);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/backups/earlier/remove", { password: "pw", confirm: "remove earlier backups" }));
  });

  it("will not offer removal until a new-kind backup has been proven restorable", async () => {
    st = { ...status, backups: [] };
    await renderCard();
    const b = (await screen.findByRole("button", { name: /remove the earlier backups/i })) as HTMLButtonElement;
    expect(b.disabled).toBe(true);
  });

  it("hides every control from a reader who may not change settings", async () => {
    await renderCard(false);
    await screen.findAllByText("Scheduled backups are off");
    expect(screen.queryByRole("button", { name: /take a backup now/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /remove the earlier backups/i })).toBeNull();
    expect((screen.getByRole("switch", { name: /scheduled backups/i }) as HTMLButtonElement).disabled).toBe(true);
  });
});
