import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError } from "@/lib/api";
import { backupError, restoreStepWords } from "@/lib/backup-errors";

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), del: vi.fn(), postRaw: vi.fn() } };
});
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }), useSearchParams: () => new URLSearchParams("") }));

const ENC = "db-20261017T020000Z.sql.gz.age";
const STATUS = {
  state: "off", message: "", key_holders: true, running: false,
  schedule: { enabled: false, hour: 2, every_days: 1, keep: 3, safety_keep: 2, next: null },
  backups: [], problems: [], earlier: { count: 0, size_bytes: 0, unencrypted: 0, oldest: null, sets: [] },
};
let routes: Record<string, unknown>;
let failing: Set<string>;

beforeEach(() => {
  get.mockReset(); post.mockReset();
  failing = new Set();
  routes = {
    "/auth/whoami": { roles: ["site_admin"], operator_id: "me" },
    "/backup-keys/": { available: true, encrypted: true, active_holders: 1, holders: [] },
    "/backups/artifacts": { data: [{ name: ENC, size_bytes: 2048, modified_at: "2026-10-17T02:00:00Z", verified_at: "2026-10-17T03:00:00Z", verified_tables: 90 }] },
    "/backups/maintenance": { active: false },
    "/backups/last-restore": { present: false },
    "/backups/status": STATUS,
    "/backups/health": { retention_readable: false, timer_active: true, timer_readable: true, database_backups: 1 },
    "/backups/settings": { retention: [], schedule_known: false, config_present: false },
  };
  get.mockImplementation((path: string) => failing.has(path)
    ? Promise.reject(new ApiError(503, { error: "scd_unreachable", message: "the appliance's backup service could not be reached" }))
    : Promise.resolve(routes[path] ?? {}));
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderPage() {
  const Page = (await import("@/app/(app)/backups/page")).default;
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><Page /></ToastProvider>);
  await screen.findByText("Available backups");
}

describe("Backup words", () => {
  it("puts the appliance's refusals in plain words", () => {
    expect(backupError(new ApiError(409, { error: "no_key_holder", message: "the backup did not complete: no backup key holder is set up" }), "x"))
      .toContain("add one under Backup encryption");
    expect(backupError(new ApiError(403, { error: "reauth_required", message: "password confirmation required" }), "x"))
      .toBe("Your password was not accepted. Nothing was changed.");
    expect(backupError(new ApiError(409, { error: "last_holder", message: "The last key holder cannot be removed." }), "x"))
      .toBe("The last key holder cannot be removed. Nothing was changed.");
    expect(backupError(new ApiError(500, { error: "internal", message: "pq: relation does not exist" }), "Fallback.")).toBe("Fallback.");
    expect(restoreStepWords("safety_backup")).toBe("Took a safety copy of the current data");
    expect(restoreStepWords("something_new")).toBe("Another step");
  });
});

describe("Backups page", () => {
  it("offers no backup at all while there is no key holder, and says why", async () => {
    routes["/backups/artifacts"] = { data: [] };
    routes["/backups/status"] = { ...STATUS, key_holders: false };
    routes["/backup-keys/"] = { available: true, encrypted: false, holders: [] };
    await renderPage();
    expect(await screen.findAllByText("No backup can be taken yet")).toBeTruthy();
    expect(screen.getByText(/Add a backup key holder under Backup encryption first/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Back up now/ })).toBeNull();
  });

  it("says a new backup was proven restorable, and refreshes the schedule", async () => {
    post.mockResolvedValue({ name: "db-new.sql.gz.age", size_bytes: 10, restorable: true });
    await renderPage();
    const statusReads = () => get.mock.calls.filter(([p]) => p === "/backups/status").length;
    const before = statusReads();
    await userEvent.click(screen.getByRole("button", { name: "Back up now" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(within(dialog).getByRole("button", { name: "Back up now" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/backups/run", { password: "pw" }));
    expect(await screen.findByText("It is encrypted and was proven restorable before it was kept.")).toBeTruthy();
    expect(screen.queryByText(/Verify it to prove/)).toBeNull();
    await waitFor(() => expect(statusReads()).toBeGreaterThan(before));
  });

  it("puts a refused backup in plain words instead of the appliance's text", async () => {
    post.mockRejectedValue(new ApiError(409, { error: "no_key_holder", message: "the backup did not complete: no backup key holder is set up" }));
    await renderPage();
    await userEvent.click(screen.getByRole("button", { name: "Back up now" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(within(dialog).getByRole("button", { name: "Back up now" }));
    expect(await within(dialog).findByText(/No backup was taken/)).toBeTruthy();
    expect(within(dialog).queryByText(/the backup did not complete/)).toBeNull();
  });

  it("shows Try again when the backups cannot be read, and recovers", async () => {
    failing.add("/backups/artifacts");
    const Page = (await import("@/app/(app)/backups/page")).default;
    const { ToastProvider } = await import("@/components/ui/toast");
    render(<ToastProvider><Page /></ToastProvider>);
    expect(await screen.findByText("The backups could not be loaded")).toBeTruthy();
    expect(screen.queryByText(/could not be reached/)).toBeNull();
    failing.delete("/backups/artifacts");
    await userEvent.click(screen.getAllByRole("button", { name: /Try again/ })[0]);
    expect(await screen.findByText("This site can be recovered from its latest backup")).toBeTruthy();
  });

  it("keeps the key dialog open and explains a key that does not open the backup", async () => {
    post.mockRejectedValue(new ApiError(400, { error: "backup_key", message: "age: no identity matched any of the recipients" }));
    await renderPage();
    await userEvent.click(screen.getByRole("button", { name: `Re-verify: ${ENC}` }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Backup key"), "AGE-SECRET-KEY-1WRONG");
    await userEvent.click(within(dialog).getByRole("button", { name: "Verify" }));
    expect(await within(dialog).findByText(/That key did not open this backup/)).toBeTruthy();
    expect(screen.queryByText(/no identity matched/)).toBeNull();
  });

  it("names each restore step in words", async () => {
    routes["/backups/last-restore"] = {
      present: true, ok: true, running: false, backup: ENC, summary: "The backup was restored.",
      steps: [{ step: "safety_backup", ok: true }, { step: "swap", ok: true, detail: "kept the previous database" }],
    };
    await renderPage();
    expect(await screen.findByText("Took a safety copy of the current data")).toBeTruthy();
    expect(screen.getByText("Swapped in the restored database")).toBeTruthy();
    expect(screen.getByText(/: kept the previous database/)).toBeTruthy();
    expect(screen.queryByText("safety backup")).toBeNull();
  });
});
