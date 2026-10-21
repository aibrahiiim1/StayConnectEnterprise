import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";

// GDPR audit B4 (migration 0138): how long a disabled staff account is kept before it is anonymised. Read by
// everyone who can open Operators, changed by the site admin with a reason and the password.

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
  it("saves a new period with a reason and the password", async () => {
    render(<OperatorAccountRetention writable />);
    await userEvent.click(await screen.findByText("Disabled accounts"));
    const field = screen.getByLabelText(/Keep disabled accounts for/);
    await userEvent.clear(field);
    await userEvent.type(field, "365");
    await userEvent.type(screen.getByLabelText(/^Reason/), "staff contract terms");
    await userEvent.type(screen.getByLabelText(/password/i), "admin-pass-1");
    await userEvent.click(screen.getByRole("button", { name: "Save disabled-account period" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/operators/account-retention",
      { disabled_account_days: 365, reason: "staff contract terms", password: "admin-pass-1" }));
  });

  it("is read-only for a role that cannot change operators", async () => {
    render(<OperatorAccountRetention writable={false} />);
    await userEvent.click(await screen.findByText("Disabled accounts"));
    expect(screen.queryByRole("button", { name: "Save disabled-account period" })).not.toBeInTheDocument();
  });
});
