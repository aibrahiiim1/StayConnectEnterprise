import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react";

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), put: (...a: any[]) => put(...a), post: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

let current: number | null = null;
beforeEach(() => {
  current = null;
  get.mockReset(); put.mockReset();
  get.mockImplementation((path: string) => path === "/accounting-retention/"
    ? Promise.resolve({ settings: { detail_retention_days: current }, changes: [], purge_runs: [] })
    : Promise.resolve({}));
  put.mockImplementation((_p: string, body: any) => {
    current = body.unlimited ? null : body.detail_retention_days;
    return Promise.resolve({});
  });
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderCard(writable: boolean) {
  const { AccountingRetentionCard } = await import("@/app/(app)/backups/accounting-retention-card");
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><AccountingRetentionCard writable={writable} /></ToastProvider>);
  await screen.findByText("Unlimited");
}

describe("Usage detail retention", () => {
  it("shows an administrator's Unlimited and offers no change to a read-only role", async () => {
    await renderCard(false);
    expect(screen.getByText(/Default 365 days/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /change/i })).toBeNull();
  });

  it("saves a number of days only within bounds, with a reason and the password", async () => {
    await renderCard(true);
    fireEvent.click(screen.getByRole("button", { name: /change/i }));
    fireEvent.click(screen.getByLabelText(/keep detail for a number of days/i));
    const save = screen.getByRole("button", { name: /^save$/i }) as HTMLButtonElement;
    fireEvent.change(screen.getByLabelText(/^days/i), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: "keep a year" } });
    fireEvent.change(screen.getByLabelText(/your password/i), { target: { value: "pw" } });
    expect(save.disabled).toBe(true); // 10 days is below the minimum of 30
    fireEvent.change(screen.getByLabelText(/^days/i), { target: { value: "365" } });
    expect(save.disabled).toBe(false);
    fireEvent.click(save);
    await waitFor(() => expect(put).toHaveBeenCalledWith("/accounting-retention/",
      { detail_retention_days: 365, reason: "keep a year", password: "pw" }));
    await screen.findByText("365 days");
  });

  it("sends Unlimited explicitly", async () => {
    current = 90;
    get.mockImplementation(() => Promise.resolve({ settings: { detail_retention_days: current } }));
    const { AccountingRetentionCard } = await import("@/app/(app)/backups/accounting-retention-card");
    const { ToastProvider } = await import("@/components/ui/toast");
    render(<ToastProvider><AccountingRetentionCard writable /></ToastProvider>);
    await screen.findByText("90 days");
    fireEvent.click(screen.getByRole("button", { name: /change/i }));
    fireEvent.click(screen.getByLabelText(/unlimited \(keep all detail\)/i));
    fireEvent.change(screen.getByLabelText(/reason/i), { target: { value: "keep everything" } });
    fireEvent.change(screen.getByLabelText(/your password/i), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/accounting-retention/",
      { unlimited: true, reason: "keep everything", password: "pw" }));
  });

  it("shows nothing on an appliance without the setting", async () => {
    get.mockImplementation(() => Promise.reject(new Error("404")));
    const { AccountingRetentionCard } = await import("@/app/(app)/backups/accounting-retention-card");
    const { ToastProvider } = await import("@/components/ui/toast");
    const { container } = render(<ToastProvider><AccountingRetentionCard writable /></ToastProvider>);
    await waitFor(() => expect(get).toHaveBeenCalled());
    expect(container.textContent).not.toContain("Usage detail kept for");
  });
});
