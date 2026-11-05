import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { AttentionBar, type AttentionRow } from "@/app/(app)/dashboard/attention-bar";

const row = (text: string): AttentionRow => ({ text, href: "/health", action: "Open " + text, tone: "warn" });

describe("the attention bar keeps keyboard focus across a refresh", () => {
  it("does not remount when the count changes", () => {
    const { rerender } = render(<AttentionBar rows={[row("Kea")]} notes={[]} ready />);
    const link = screen.getByText("Open Kea");
    link.focus();
    expect(document.activeElement).toBe(link);

    rerender(<AttentionBar rows={[row("Kea"), row("PMS")]} notes={[]} ready />);
    expect(screen.getByText("Open Kea")).toBe(link);
    expect(document.activeElement).toBe(link);
  });
});
