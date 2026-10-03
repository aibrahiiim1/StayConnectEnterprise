import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

// OPENING USAGE EXPLORER DOES NOT PUT THE KEYBOARD IN THE SEARCH FIELD.
//
// The operator opens it to look around (the recent access sources load by themselves); typing should start only
// when they choose to search. Keyboard users still reach the field with Tab, and a deliberate click or Tab into
// it works as before.

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

describe("Usage Explorer initial focus", () => {
  it("leaves focus out of every text field after it opens and loads", async () => {
    const { default: UsageExplorerPage } = await import("@/app/(app)/usage/page");
    render(<UsageExplorerPage />);
    await screen.findByText(/No usage recorded yet/);
    await waitFor(() => {
      const a = document.activeElement;
      expect(a?.tagName === "INPUT" || a?.tagName === "TEXTAREA").toBe(false);
    });
    // The field is still there and reachable.
    const search = screen.getAllByRole("textbox")[0];
    search.focus();
    expect(document.activeElement).toBe(search);
  });
});
