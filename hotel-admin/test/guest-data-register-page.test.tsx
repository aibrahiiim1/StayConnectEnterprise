import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup, fireEvent, waitFor } from "@testing-library/react";
import { dueWords } from "@/lib/guest-data-register-words";

// The request register: record a request, see its deadline, close it with a ground when it is refused.

const get = vi.fn();
const post = vi.fn();
vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return { ...actual, api: { get: (...a: any[]) => get(...a), post: (...a: any[]) => post(...a), put: vi.fn(), patch: vi.fn(), del: vi.fn() } };
});

const open = {
  id: "11111111-1111-4111-8111-111111111111", ref: "RQ-AAAAAAAA", kind: "ACCESS", received_at: "2026-10-01", due_at: "2026-11-01",
  extended: false, extension_reason: null, requester: "Ada L.", verification_method: "ID_CHECKED_IN_PERSON", status: "OPEN",
  outcome: null, erasure_ref: null, assisted_ref: null, days_left: 12, overdue: false,
};

beforeEach(() => {
  get.mockReset(); post.mockReset();
  get.mockResolvedValue({ requests: [open] });
});
afterEach(() => { cleanup(); vi.resetModules(); });

async function renderPage() {
  const { default: Page } = await import("@/app/(app)/guest-data/register/page");
  const { ToastProvider } = await import("@/components/ui/toast");
  render(<ToastProvider><Page /></ToastProvider>);
  await screen.findByText("RQ-AAAAAAAA");
}

describe("Request register", () => {
  it("shows the deadline of an open request", async () => {
    await renderPage();
    expect(screen.getByText("12 days left")).toBeTruthy();
    expect(screen.getByText("Ada L.")).toBeTruthy();
  });

  it("records a request with the day it was received", async () => {
    post.mockResolvedValue({ ref: "RQ-BBBBBBBB" });
    await renderPage();
    fireEvent.change(screen.getByLabelText(/who is asking/i), { target: { value: "Bo Peep" } });
    fireEvent.click(screen.getByRole("button", { name: /record request/i }));
    await waitFor(() => expect(post).toHaveBeenCalled());
    expect(post.mock.calls[0][0]).toBe("/guest-data-rights/register");
    expect(post.mock.calls[0][1]).toMatchObject({ kind: "ACCESS", requester: "Bo Peep" });
  });

  it("will not close a refusal without its ground", async () => {
    await renderPage();
    fireEvent.click(screen.getByRole("button", { name: /close request RQ-AAAAAAAA/i }));
    fireEvent.change(screen.getByLabelText(/^result/i), { target: { value: "REFUSED" } });
    const submit = screen.getByRole("button", { name: /^close request$/i }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/ground for refusing/i), { target: { value: "LEGAL_RETENTION" } });
    expect(submit.disabled).toBe(false);
  });

  it("says days in plain words", () => {
    expect(dueWords(1, false)).toBe("1 day left");
    expect(dueWords(0, false)).toBe("due today");
    expect(dueWords(-3, true)).toBe("3 days overdue");
  });
});
