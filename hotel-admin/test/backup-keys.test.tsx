import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), del: vi.fn(), postRaw: vi.fn() } };
});
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }), useSearchParams: () => new URLSearchParams("") }));

const ENC = "db-20261017T020000Z.sql.gz.age";
let keys: unknown;
beforeEach(() => {
  get.mockReset(); post.mockReset();
  keys = { available: true, encrypted: false, holders: [] };
  get.mockImplementation((path: string) => {
    const map: Record<string, unknown> = {
      "/auth/whoami": { roles: ["site_admin"], operator_id: "me" },
      "/backup-keys/": keys,
      "/backups/artifacts": { data: [{ name: ENC, size_bytes: 2048, modified_at: "2026-10-17T02:00:00Z", verified_at: "2026-10-17T03:00:00Z", verified_tables: 90 }] },
      "/backups/maintenance": { active: false },
      "/backups/last-restore": { present: false },
      "/backups/health": { retention_readable: false, timer_active: true, timer_readable: true, database_backups: 1 },
      "/backups/settings": { retention: [], schedule_known: false, config_present: false },
    };
    return Promise.resolve(map[path] ?? {});
  });
  URL.createObjectURL = vi.fn(() => "blob:x");
  URL.revokeObjectURL = vi.fn();
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderPage() {
  const Page = (await import("@/app/(app)/backups/page")).default;
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><Page /></ToastProvider>);
  await screen.findByText("Available backups");
}

describe("Backup encryption", () => {
  it("says plainly when backups are not encrypted", async () => {
    await renderPage();
    expect(await screen.findByText("Backups are not encrypted")).toBeTruthy();
  });

  it("hands a new key over once and switches on only with the check code typed back", async () => {
    post.mockImplementation((path: string) => path === "/backup-keys/holders"
      ? Promise.resolve({ id: "h1", label: "IT manager", public_key: "age1pub", created_at: "2026-10-17T10:00:00Z",
          private_key: "AGE-SECRET-KEY-1XYZ", check_code: "ABCD-EFGH", key_file: "# key\nAGE-SECRET-KEY-1XYZ\n", key_file_name: "k.txt" })
      : Promise.resolve({}));
    await renderPage();
    await userEvent.click(await screen.findByRole("button", { name: /Add key holder/ }));
    let dialog = await screen.findByRole("dialog");
    const create = within(dialog).getByRole("button", { name: "Create key" }) as HTMLButtonElement;
    await userEvent.type(within(dialog).getByLabelText(/Key holder/), "IT manager");
    await userEvent.type(within(dialog).getByLabelText(/Your password/), "pw");
    expect(create.disabled, "the loss acknowledgement is required").toBe(true);
    await userEvent.click(within(dialog).getByRole("checkbox"));
    await userEvent.click(create);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/backup-keys/holders", { label: "IT manager", acknowledge_loss: true, password: "pw" }));

    dialog = await screen.findByRole("dialog");
    await within(dialog).findByText(/only time the appliance shows this key/);
    expect(within(dialog).queryByText("AGE-SECRET-KEY-1XYZ"), "the key is in the file and on the sheet, not on the screen").toBeNull();
    expect(within(dialog).queryByText("ABCD-EFGH"), "the check code proves the file was saved, so it is not on the screen").toBeNull();
    const confirm = within(dialog).getByRole("button", { name: "Confirm" }) as HTMLButtonElement;
    expect((within(dialog).getByLabelText(/Check code/) as HTMLInputElement).disabled).toBe(true);
    await userEvent.click(within(dialog).getByRole("button", { name: /Download key file/ }));
    expect(URL.createObjectURL).toHaveBeenCalled();
    await userEvent.type(within(dialog).getByLabelText(/Check code/), "abcd-efgh");
    expect(confirm.disabled).toBe(false);
    await userEvent.click(confirm);
    await waitFor(() => expect(post).toHaveBeenCalledWith("/backup-keys/holders/h1/confirm", { check_code: "abcd-efgh" }));
  });

  it("warns when the restore test is overdue", async () => {
    keys = {
      available: true, encrypted: true, active_holders: 1,
      holders: [{ id: "h1", label: "Hotel safe", public_key: "age1pub", status: "ACTIVE", created_at: "2026-01-01T00:00:00Z",
        created_by: "me", confirmed_at: "2026-01-01T00:00:00Z", removed_at: null, last_tested_at: null }],
      restore_test: { interval_months: 6, last_tested_at: null, due_at: "2026-07-01T00:00:00Z", overdue: true },
    };
    await renderPage();
    expect(await screen.findByText("Restore test overdue")).toBeTruthy();
    expect(screen.queryByText("Backups are not encrypted")).toBeNull();
  });

  it("asks for a key holder's key to verify or restore an encrypted backup", async () => {
    post.mockResolvedValue({ ok: true, tables: 90, duration: "3s" });
    await renderPage();
    expect(await screen.findByText("Encrypted")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: `Re-verify ${ENC}` }));
    let dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Backup key"), "AGE-SECRET-KEY-1XYZ");
    await userEvent.click(within(dialog).getByRole("button", { name: "Verify" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(`/backups/artifacts/${encodeURIComponent(ENC)}/verify`, { identity: "AGE-SECRET-KEY-1XYZ" }));

    post.mockReset(); post.mockResolvedValue({});
    await userEvent.click(screen.getByRole("button", { name: `Restore ${ENC}` }));
    dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.type(within(dialog).getByLabelText(/to confirm/), ENC);
    await userEvent.click(within(dialog).getByRole("button", { name: "Restore this backup" }));
    expect(await within(dialog).findByText(/paste a key holder’s backup key/)).toBeTruthy();
    expect(post).not.toHaveBeenCalled();
    await userEvent.type(within(dialog).getByLabelText("Backup key"), "AGE-SECRET-KEY-1XYZ");
    await userEvent.click(within(dialog).getByRole("button", { name: "Restore this backup" }));
    await waitFor(() => expect(post).toHaveBeenCalledWith(`/backups/artifacts/${encodeURIComponent(ENC)}/restore`,
      { password: "pw", confirm: ENC, identity: "AGE-SECRET-KEY-1XYZ" }));
  });
});
