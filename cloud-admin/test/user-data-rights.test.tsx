import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch, PLATFORM_ME, renderAs } from "./helpers";

vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);

import { UsersManager } from "@/components/users-manager";

const TEAM = { items: [
  { id: "op-1", email: "admin@example.test", status: "active", roles: ["platform_admin"], two_step_enrolled: true },
  { id: "op-2", email: "grace@example.test", status: "active", roles: ["platform_support"], two_step_enrolled: true },
] };

describe("a Central user's own data", () => {
  it("corrects an email", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      { method: "GET", match: "/api/cloud/v1/team", body: TEAM },
      { method: "PATCH", match: "/api/cloud/v1/team/op-2", body: { id: "op-2" } },
    ]);
    renderAs(PLATFORM_ME, <UsersManager base="/cloud/v1/team" roles={["platform_support"]} canManage meId="op-1" canResetPassword noun="member" />);
    await screen.findAllByText("grace@example.test");
    await user.click(screen.getByRole("button", { name: "Actions for grace@example.test" }));
    await user.click(await screen.findByRole("menuitem", { name: /Correct the email/ }));
    const dialog = await screen.findByRole("dialog");
    const field = within(dialog).getByLabelText(/^Email/);
    await user.clear(field);
    await user.type(field, "grace.hopper@example.test");
    await user.click(within(dialog).getByRole("button", { name: "Save email" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ email: "grace.hopper@example.test" }));
  });

  it("downloads one person's data with a reason, and only for the platform", async () => {
    const user = userEvent.setup();
    (URL as any).createObjectURL = vi.fn(() => "blob:x");
    (URL as any).revokeObjectURL = vi.fn();
    const { calls } = mockFetch([
      { method: "GET", match: "/api/cloud/v1/team", body: TEAM },
      { method: "POST", match: "/api/cloud/v1/users/op-2/data-export", body: { format: "onegate.central-user-export/1" } },
    ]);
    const { unmount } = renderAs(PLATFORM_ME, <UsersManager base="/cloud/v1/team" roles={["platform_support"]} canManage meId="op-1" canResetPassword canExportData noun="member" />);
    await screen.findAllByText("grace@example.test");
    await user.click(screen.getByRole("button", { name: "Actions for grace@example.test" }));
    await user.click(await screen.findByRole("menuitem", { name: /Download their data/ }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "Download" });
    expect(submit).toBeDisabled();
    await user.type(within(dialog).getByLabelText(/^Reason/), "her written request");
    await user.click(submit);
    await waitFor(() => expect(calls.find((c) => c.method === "POST")?.body).toEqual({ reason: "her written request" }));
    unmount();

    renderAs(PLATFORM_ME, <UsersManager base="/cloud/v1/team" roles={["platform_support"]} canManage meId="op-1" canResetPassword noun="member" />);
    await screen.findAllByText("grace@example.test");
    await user.click(screen.getByRole("button", { name: "Actions for grace@example.test" }));
    expect(await screen.findByRole("menuitem", { name: /Correct the email/ })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /Download their data/ })).toBeNull();
  });
});
