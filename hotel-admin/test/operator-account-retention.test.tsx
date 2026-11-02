import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";

vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<Record<string, unknown>>);
  return { ...actual, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});
vi.mock("@/components/ui/toast", () => ({ useToast: () => ({ success: vi.fn(), error: vi.fn() }) }));

import { api } from "@/lib/api";
import { OperatorAccountRetention } from "@/components/operator-account-retention";

const get = api.get as unknown as ReturnType<typeof vi.fn>;
const put = api.put as unknown as ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.clearAllMocks();
  get.mockResolvedValue({
    settings: { disabled_account_days: 730, config_version: 0 },
    bounds: [{ key: "disabled_account_days", label: "Keep disabled accounts for", unit: "days", min: 30, max: 3650,
      default: 730, explains: "A disabled staff account is anonymised this long after it was disabled." }],
  });
  put.mockResolvedValue({ config_version: 1 });
});

describe("Disabled accounts", () => {
  it("saves a new period after one confirmation, with no reason or password to retype", async () => {
    render(<OperatorAccountRetention writable />);
    await userEvent.click(await screen.findByText("Disabled accounts"));
    const field = screen.getByLabelText(/Keep disabled accounts for/);
    await userEvent.clear(field);
    await userEvent.type(field, "365");
    await userEvent.click(screen.getByRole("button", { name: "Save disabled-account period" }));
    expect(screen.queryByLabelText(/password/i)).toBeNull();
    expect(screen.queryByLabelText(/^Reason/)).toBeNull();
    await userEvent.click(await screen.findByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/operators/account-retention", { disabled_account_days: 365 }));
  });

  it("is read-only for a role that cannot change operators", async () => {
    render(<OperatorAccountRetention writable={false} />);
    await userEvent.click(await screen.findByText("Disabled accounts"));
    expect(screen.queryByRole("button", { name: "Save disabled-account period" })).not.toBeInTheDocument();
  });
});
