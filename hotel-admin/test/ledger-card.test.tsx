import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, cleanup, fireEvent, within } from "@testing-library/react";
import { agoWords, kindLabel, ledgerHeadline, STORAGE_KINDS, type LedgerStatus } from "@/lib/ledger-words";

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: (...a: any[]) => put(...a), del: vi.fn(), postRaw: vi.fn() } };
});

const base: LedgerStatus = {
  erasure_available: true, state: "ok", message: "", checked: "2026-10-21T01:00:00Z",
  database: { entries: 12, intact: true, unconfirmed: 0 },
  primary: { present: true, intact: true, entries: 12, path: "/var/lib/stayconnect/ledger/erasure-ledger.jsonl" },
  secondary: { configured: true, kind: "sftp", describe: "sftp://ledger@nas:22/backups", reachable: true, in_sync: true, entries: 12, encrypted: true, last_ok: "2026-10-21T00:50:00Z" },
};

let ledger: LedgerStatus;
beforeEach(() => {
  get.mockReset(); post.mockReset(); put.mockReset();
  ledger = base;
  get.mockImplementation((path: string) => Promise.resolve(path.startsWith("/backups/ledger") ? ledger : {}));
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderCards(writable = true) {
  const { LedgerCard } = await import("@/app/(app)/backups/ledger-card");
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><LedgerCard writable={writable} /></ToastProvider>);
}

describe("Ledger words", () => {
  it("says what each state means and never blames guest internet", () => {
    expect(ledgerHeadline(base).title).toBe("Both copies are in step");
    for (const state of ["not_configured", "recovery_needed", "blocked", "attention", "unavailable"]) {
      const h = ledgerHeadline({ ...base, state, erasure_available: false, message: "x" });
      expect(h.tone).not.toBe("ok");
      expect(h.detail.toLowerCase()).not.toContain("internet access is affected");
    }
    expect(ledgerHeadline({ ...base, state: "not_configured" }).detail).toContain("refused");
    expect(ledgerHeadline({ ...base, state: "attention", message: "disk gone" }).detail).toContain("Guest internet access is not affected");
    expect(ledgerHeadline(null).title).toBe("Checking…");
  });
  it("names the storage kinds", () => {
    expect(STORAGE_KINDS.map((k) => k.value)).toEqual(["dir", "sftp", "s3"]);
    expect(kindLabel("s3")).toContain("S3");
    expect(kindLabel(undefined)).toBe("Not set up");
  });
  it("says how long ago in plain words", () => {
    const now = Date.parse("2026-10-21T12:00:00Z");
    expect(agoWords(undefined, now)).toBe("never");
    expect(agoWords("2026-10-21T11:59:30Z", now)).toBe("just now");
    expect(agoWords("2026-10-21T10:00:00Z", now)).toBe("2 hours ago");
    expect(agoWords("2026-10-17T12:00:00Z", now)).toBe("4 days ago");
  });
});

describe("Ledger card", () => {
  it("shows both copies in step", async () => {
    await renderCards();
    expect(await screen.findByText("Both copies are in step")).toBeTruthy();
    expect(screen.getByText(/sftp:\/\/ledger@nas/)).toBeTruthy();
    expect(screen.getByText(/· encrypted/)).toBeTruthy();
    expect(screen.getByRole("button", { name: /edit or replace the secondary storage/i })).toBeTruthy();
  });

  it("requires the secondary storage: the form is open and erasure is said to be refused", async () => {
    ledger = { ...base, state: "not_configured", erasure_available: false, secondary: { ...base.secondary, configured: false, in_sync: false } };
    await renderCards();
    expect(await screen.findByText("Set up the secondary storage")).toBeTruthy();
    expect(screen.getByLabelText("Kind of secondary storage")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^save and copy the ledger…$/i })).toBeTruthy();
  });

  it("will not save an SFTP server whose identity has not been confirmed", async () => {
    ledger = { ...base, state: "not_configured", erasure_available: false, secondary: { ...base.secondary, configured: false, in_sync: false } };
    post.mockResolvedValue({ fingerprint: "SHA256:abcdef" });
    await renderCards();
    fireEvent.change(await screen.findByLabelText("Kind of secondary storage"), { target: { value: "sftp" } });
    const save = screen.getByRole("button", { name: /^save and copy the ledger…$/i }) as HTMLButtonElement;
    const test = screen.getByRole("button", { name: /^connectivity test$/i }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    expect(test.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Server"), { target: { value: "nas.hotel.example" } });
    fireEvent.click(screen.getByRole("button", { name: /check the server.s identity/i }));
    const box = await screen.findByRole("checkbox");
    expect(post).toHaveBeenCalledWith("/backups/ledger/destination/probe", { host: "nas.hotel.example", port: 22 });
    fireEvent.click(box);
    await waitFor(() => expect((screen.getByRole("button", { name: /^save and copy the ledger…$/i }) as HTMLButtonElement).disabled).toBe(false));
  });

  it("sends an SFTP server's password under its own name, apart from the operator's step-up password", async () => {
    ledger = { ...base, state: "not_configured", erasure_available: false, secondary: { ...base.secondary, configured: false, in_sync: false } };
    post.mockResolvedValue({ fingerprint: "SHA256:abcdef" });
    put.mockResolvedValue({ ok: true });
    await renderCards();
    fireEvent.change(await screen.findByLabelText("Kind of secondary storage"), { target: { value: "sftp" } });
    fireEvent.change(screen.getByLabelText("Server"), { target: { value: "nas.hotel.example" } });
    fireEvent.change(screen.getByLabelText("User name"), { target: { value: "ledger" } });
    fireEvent.change(screen.getByLabelText("Folder on the server"), { target: { value: "/ledger" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "SERVER-PW" } });
    fireEvent.click(screen.getByRole("button", { name: /check the server.s identity/i }));
    fireEvent.click(await screen.findByRole("checkbox"));
    await waitFor(() => expect((screen.getByRole("button", { name: /^save and copy the ledger…$/i }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: /^save and copy the ledger…$/i }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "OPERATOR-PW" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /save and copy the ledger/i }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const body = put.mock.calls[0][1];
    expect(body.sftp_password).toBe("SERVER-PW");
    expect(body.password).toBe("OPERATOR-PW");
  });

  it("saves a folder with the password step-up, and sends no credentials in the clear elsewhere", async () => {
    ledger = { ...base, state: "not_configured", erasure_available: false, secondary: { ...base.secondary, configured: false, in_sync: false } };
    put.mockResolvedValue({ ok: true });
    await renderCards();
    fireEvent.change(await screen.findByLabelText("Folder"), { target: { value: "/mnt/ledger" } });
    fireEvent.click(screen.getByRole("button", { name: /^save and copy the ledger…$/i }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /save and copy the ledger/i }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(put.mock.calls[0][0]).toBe("/backups/ledger/destination");
    expect(put.mock.calls[0][1]).toEqual({ kind: "dir", path: "/mnt/ledger", password: "pw" });
  });

  it("offers recovery when the secondary storage is ahead, and asks for a key holder's key", async () => {
    ledger = { ...base, state: "recovery_needed", erasure_available: false, message: "the secondary storage holds erasures newer than this appliance's" };
    post.mockResolvedValue({ ok: true });
    await renderCards();
    fireEvent.click(await screen.findByRole("button", { name: /recover from the secondary storage/i }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /^recover$/i }));
    expect(await within(dialog).findByText(/paste a backup key holder/i)).toBeTruthy();
    expect(post).not.toHaveBeenCalled();
    fireEvent.change(within(dialog).getByLabelText("Backup key"), { target: { value: "AGE-SECRET-KEY-1XYZ" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /^recover$/i }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/backups/ledger/recover", { identity: "AGE-SECRET-KEY-1XYZ", password: "pw" }));
  });

  it("labels a test as a connectivity test and never as a copy of the ledger", async () => {
    ledger = { ...base, state: "not_configured", erasure_available: false, secondary: { ...base.secondary, configured: false, in_sync: false } };
    post.mockResolvedValue({ ok: true, test: "connectivity", replicated: false, message: "Connectivity test passed: a test file was written, read back and removed. The erasure ledger has NOT been copied there; it is copied when you save.", location: { where: "/mnt/nas/onegate", medium: "nas_smb", filesystem: "cifs" } });
    await renderCards();
    fireEvent.change(await screen.findByLabelText("Folder"), { target: { value: "/mnt/nas/onegate" } });
    fireEvent.click(screen.getByRole("button", { name: /^connectivity test$/i }));
    expect(await screen.findByText(/has NOT been copied/)).toBeTruthy();
    expect(screen.getByText(/NAS share \(SMB\/CIFS\)/)).toBeTruthy();
    expect(screen.queryByText(/in step/i)).toBeNull();
  });

  it("shows a save that was stored but not confirmed in step as a warning, never as a success", async () => {
    const { ApiError } = await import("@/lib/api");
    ledger = { ...base, state: "not_configured", erasure_available: false, secondary: { ...base.secondary, configured: false, in_sync: false } };
    put.mockRejectedValue(new ApiError(502, { ok: false, saved: true, error: "not_in_step", message: "Saved, but the copy is not confirmed in step: permission denied" }));
    await renderCards();
    fireEvent.change(await screen.findByLabelText("Folder"), { target: { value: "/mnt/ledger" } });
    fireEvent.click(screen.getByRole("button", { name: /^save and copy the ledger…$/i }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /save and copy the ledger/i }));
    expect(await screen.findByText(/not confirmed in step: permission denied/)).toBeTruthy();
    expect(screen.queryByText("Secondary storage in step")).toBeNull();
  });

  it("edits the saved storage with its settings filled in and keeps the saved password when the field is left empty", async () => {
    ledger = { ...base, secondary: { ...base.secondary, settings: { kind: "sftp", host: "nas", port: 22, user: "ledger", dir: "backups", fingerprint: "SHA256:abc" }, secrets: { password: true } } };
    put.mockResolvedValue({ ok: true, message: "Saved. The erasure ledger (12 entries) was copied there and read back." });
    await renderCards();
    fireEvent.click(await screen.findByRole("button", { name: /edit or replace the secondary storage/i }));
    expect((screen.getByLabelText("Server") as HTMLInputElement).value).toBe("nas");
    expect((screen.getByLabelText("Folder on the server") as HTMLInputElement).value).toBe("backups");
    expect(screen.getByText(/Leave empty to keep it/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /^save and copy the ledger…$/i }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "pw" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /save and copy the ledger/i }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const body = put.mock.calls[0][1];
    expect(body.keep_secret).toBe(true);
    expect(body.sftp_password).toBe("");
    expect(body.fingerprint).toBe("SHA256:abc");
  });

  it("removes the setting only with the password and the typed confirmation, and says the remote copy stays", async () => {
    post.mockResolvedValue({ ok: true, message: "The secondary storage setting was removed. Nothing kept there was deleted." });
    await renderCards();
    fireEvent.click(await screen.findByRole("button", { name: /^remove…$/i }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/is NOT deleted/)).toBeTruthy();
    fireEvent.change(within(dialog).getByLabelText(/password/i), { target: { value: "pw" } });
    const typed = within(dialog).getAllByRole("textbox").find((el) => (el as HTMLInputElement).type !== "password")!;
    fireEvent.change(typed, { target: { value: "remove secondary storage" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /remove the setting/i }));
    await waitFor(() => expect(post).toHaveBeenCalledWith("/backups/ledger/destination/remove", { password: "pw", confirm: "remove secondary storage" }));
  });

  it("hides every control from a reader who may not change settings", async () => {
    ledger = { ...base, state: "not_configured", erasure_available: false, secondary: { ...base.secondary, configured: false, in_sync: false } };
    await renderCards(false);
    await screen.findByText("Set up the secondary storage");
    expect(screen.queryByLabelText("Kind of secondary storage")).toBeNull();
  });
});
