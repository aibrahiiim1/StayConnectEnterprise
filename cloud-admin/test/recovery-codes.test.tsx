import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { renderAs, PLATFORM_ME } from "./helpers";
import TwoStepSigninPage from "@/app/(app)/account/security/page";

type Sent = { method: string; url: string; headers: Record<string, string>; body: unknown };

function recordFetch(answer: (s: Sent) => { status?: number; body: unknown } = () => ({ body: {} })) {
  const sent: Sent[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const s: Sent = {
      method: (init?.method ?? "GET").toUpperCase(),
      url: String(input),
      headers: { ...((init?.headers as Record<string, string>) ?? {}) },
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    };
    sent.push(s);
    const a = answer(s);
    const status = a.status ?? 200;
    return {
      ok: status >= 200 && status < 300,
      status,
      headers: { get: () => "application/json" },
      json: async () => a.body,
      text: async () => JSON.stringify(a.body),
    } as unknown as Response;
  }));
  return sent;
}

// D-R3: a new set of recovery codes needs a current code from the authenticator app (and the password step-up).
describe("new recovery codes", () => {
  afterEach(() => vi.unstubAllGlobals());

  const mfa = { enrolled: true, confirmed_at: "2026-10-01T10:00:00Z", recovery_codes_left: 2 };

  it("asks for the 6-digit code, sends it, and shows the new set", async () => {
    const codes = Array.from({ length: 10 }, (_, i) => `AAAAA-BBBB${i}`);
    const sent = recordFetch((s) =>
      s.url === "/api/v1/auth/mfa/recovery-codes" ? { body: { recovery_codes: codes } } : { body: mfa });
    renderAs(PLATFORM_ME, <TwoStepSigninPage />);
    const field = await screen.findByLabelText("Code from your authenticator app");
    expect(field).toHaveAttribute("autocomplete", "one-time-code");

    // Nothing typed: told so under the field, nothing sent.
    fireEvent.click(screen.getByRole("button", { name: "Make new recovery codes" }));
    expect(await screen.findByText(/Enter the 6-digit code your authenticator app shows now/)).toBeInTheDocument();
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(sent.some((s) => s.method === "POST")).toBe(false);

    fireEvent.change(field, { target: { value: "123 456" } });
    fireEvent.click(screen.getByRole("button", { name: "Make new recovery codes" }));
    expect(await screen.findByRole("list", { name: "New recovery codes" })).toBeInTheDocument();
    const post = sent.find((s) => s.method === "POST");
    expect(post?.body).toEqual({ code: "123456" });
  });

  it("a wrong code is reported under the field", async () => {
    recordFetch((s) =>
      s.url === "/api/v1/auth/mfa/recovery-codes"
        ? { status: 400, body: { error: "wrong_code", message: "That code is not right. Enter the 6-digit code your authenticator app shows now." } }
        : { body: mfa });
    renderAs(PLATFORM_ME, <TwoStepSigninPage />);
    const field = await screen.findByLabelText("Code from your authenticator app");
    fireEvent.change(field, { target: { value: "000000" } });
    fireEvent.click(screen.getByRole("button", { name: "Make new recovery codes" }));
    await waitFor(() => expect(field).toHaveAttribute("aria-invalid", "true"));
    expect(screen.getByText(/That code is not right/)).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "New recovery codes" })).not.toBeInTheDocument();
  });
});
