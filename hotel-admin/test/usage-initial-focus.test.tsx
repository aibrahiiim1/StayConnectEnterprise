import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver ??= ResizeObserverStub;

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      get: vi.fn((path: string) =>
        Promise.resolve(path.startsWith("/usage/sources") ? { data: [], meta: { has_more: false } } : {})),
      post: vi.fn(), put: vi.fn(),
    },
  };
});

async function openPage() {
  const { default: UsageExplorerPage } = await import("@/app/(app)/usage/page");
  render(<UsageExplorerPage />);
  await screen.findByText(/No usage recorded yet/);
}

describe("Usage Explorer initial focus", () => {
  it("leaves focus out of every text field after it opens and loads", async () => {
    await openPage();
    await waitFor(() => {
      const a = document.activeElement;
      expect(a?.tagName === "INPUT" || a?.tagName === "TEXTAREA").toBe(false);
    });
    const search = screen.getAllByRole("searchbox")[0];
    search.focus();
    expect(document.activeElement).toBe(search);
  });

  it("gives the browser and password managers no reason to offer saved credentials", async () => {
    await openPage();
    const search = screen.getByRole("searchbox", { name: /card reference/i });
    expect(search).toHaveAttribute("type", "search");
    expect(search).toHaveAttribute("autocomplete", "off");
    expect(search).toHaveAttribute("spellcheck", "false");
    expect(search).toHaveAttribute("data-1p-ignore");
    expect(search).toHaveAttribute("data-lpignore", "true");
    expect(search).toHaveAttribute("data-form-type", "other");
    expect(search).not.toHaveAttribute("autofocus");
    // A credential-looking name or id is what password managers key on.
    for (const attr of ["name", "id"]) {
      expect(search.getAttribute(attr) ?? "").not.toMatch(/user|login|email|pass/i);
    }
    // Not inside a form: a lone text box in a form reads as a sign-in form.
    expect(search.closest("form")).toBeNull();
    expect(search.closest("[role=search]")).not.toBeNull();
  });
});
