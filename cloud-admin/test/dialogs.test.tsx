import { describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockFetch } from "./helpers";

vi.mock("next/navigation", () => ({
  usePathname: () => "/sites",
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), refresh: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}));

import { ConfirmDialog } from "@/components/ui/dialog";
import { DeleteDialog } from "@/components/delete-dialog";
import { StepUpProvider } from "@/components/step-up";
import { api, withStepUp } from "@/lib/api";

describe("ConfirmDialog — typed confirmation", () => {
  it("enables the danger action only after the exact text and a reason", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <ConfirmDialog
        open
        onOpenChange={() => {}}
        title="Delete site"
        confirmLabel="Delete site"
        confirmVariant="danger"
        consequences={["It cannot be undone."]}
        confirmText="hurghada"
        confirmTextLabel="Type the site code"
        requireReason
        onConfirm={onConfirm}
      />,
    );
    expect(screen.getByText("It cannot be undone.")).toBeInTheDocument();
    const button = screen.getByRole("button", { name: "Delete site" });
    expect(button).toBeDisabled();

    const typed = screen.getByLabelText(/Type the site code/);
    await user.type(typed, "Hurghada"); // case matters
    await user.type(screen.getByLabelText(/Reason/), "closed property");
    expect(button).toBeDisabled();

    await user.clear(typed);
    await user.type(typed, "hurghada");
    expect(button).toBeEnabled();
    await user.click(button);
    expect(onConfirm).toHaveBeenCalledWith({ reason: "closed property", password: "" });
  });

  it("requires the password when asked and never shows it in clear text", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    render(
      <ConfirmDialog open onOpenChange={() => {}} title="Revoke" confirmLabel="Revoke" requirePassword onConfirm={onConfirm} />,
    );
    const pw = screen.getByLabelText(/Confirm your password/);
    expect(pw).toHaveAttribute("type", "password");
    expect(screen.getByRole("button", { name: "Revoke" })).toBeDisabled();
    await user.type(pw, "s3cret");
    await user.click(screen.getByRole("button", { name: "Revoke" }));
    expect(onConfirm).toHaveBeenCalledWith({ reason: "", password: "s3cret" });
  });
});

describe("DeleteDialog — the destructive-action pattern", () => {
  it("sends the typed confirmation and reason, and lists blockers from a 409", async () => {
    const user = userEvent.setup();
    const { calls } = mockFetch([
      {
        method: "DELETE",
        match: /\/api\/v1\/sites\/s1/,
        status: 409,
        body: { error: "conflict", message: "Site still has appliances.", blocking: [{ type: "appliances", label: "appliances", count: 2, resource: "appliances" }] },
      },
    ]);
    render(
      <DeleteDialog
        open
        onClose={() => {}}
        onDeleted={() => {}}
        title='Delete site "Coral"'
        what="Site"
        expected="coral"
        confirmHint="Type the site code"
        deleteUrl="/v1/sites/s1?tenant_id=t1"
      />,
    );
    expect(screen.getByText("It cannot be undone.")).toBeInTheDocument();
    await user.type(screen.getByLabelText(/Type the site code/), "coral");
    await user.type(screen.getByLabelText(/Reason/), "duplicate");
    await user.click(screen.getByRole("button", { name: "Delete site" }));

    await waitFor(() => expect(screen.getByText(/cannot be deleted because it still contains/)).toBeInTheDocument());
    expect(screen.getByText("2 appliances")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /View appliances/ })).toHaveAttribute("href", "/appliances");
    expect(calls[0]).toMatchObject({ method: "DELETE", url: "/api/v1/sites/s1?tenant_id=t1", body: { confirm: "coral", reason: "duplicate" } });
  });
});

describe("Password confirmation (step-up)", () => {
  it("asks with a designed dialog, re-authenticates and retries once", async () => {
    const user = userEvent.setup();
    const promptSpy = vi.spyOn(window, "prompt");
    let first = true;
    const { calls } = mockFetch([
      { method: "POST", match: "/api/v1/auth/reauth", body: { ok: true } },
    ]);
    // The action itself: 403 reauth_required the first time, then success.
    const fetchMock = globalThis.fetch as unknown as ReturnType<typeof vi.fn>;
    const routed = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input) === "/api/cloud/v1/licenses/l1/revoke") {
        calls.push({ method: "POST", url: String(input), body: undefined });
        const status = first ? 403 : 200;
        first = false;
        return {
          ok: status === 200, status,
          headers: { get: () => "application/json" },
          json: async () => (status === 200 ? { status: "revoked" } : { error: "reauth_required" }),
        } as unknown as Response;
      }
      return routed(input, init);
    });

    render(<StepUpProvider><div>page</div></StepUpProvider>);
    let result: unknown;
    let done = false;
    act(() => {
      void withStepUp(() => api.post("/cloud/v1/licenses/l1/revoke")).then((r) => { result = r; done = true; });
    });

    const pw = await screen.findByLabelText(/^Password/);
    expect(pw).toHaveAttribute("type", "password");
    await user.type(pw, "hunter22");
    await user.click(screen.getByRole("button", { name: "Confirm" }));

    await waitFor(() => expect(done).toBe(true));
    expect(result).toEqual({ status: "revoked" });
    expect(promptSpy).not.toHaveBeenCalled();
    expect(calls.map((c) => c.url)).toEqual([
      "/api/cloud/v1/licenses/l1/revoke",
      "/api/v1/auth/reauth",
      "/api/cloud/v1/licenses/l1/revoke",
    ]);
    expect(calls[1].body).toEqual({ password: "hunter22" });
  });
});
