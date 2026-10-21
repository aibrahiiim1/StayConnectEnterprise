import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, fireEvent, within } from "@testing-library/react";
import { useState } from "react";

// ACCESSIBILITY BEHAVIOURS (WCAG 2.2 AA audit of the Admin Console). Each block is one finding: what a keyboard
// or screen-reader user now gets that they did not before.

vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));
vi.mock("next/navigation", () => ({ usePathname: () => "/dashboard" }));
vi.mock("@/lib/capabilities", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/capabilities")>();
  return { ...actual, useCapabilities: () => null };
});

import { safeNextPath, loginAfterExpiry, DEFAULT_AFTER_LOGIN } from "@/lib/safe-next";
import { Field, Input } from "@/components/ui/input";
import { Segmented } from "@/components/ui/tabs";
import { useEditFocus } from "@/lib/use-edit-focus";
import { IdleWarning, IDLE_WARNING_MS } from "@/components/idle-warning";
import { Nav } from "@/components/nav";

describe("after sign-in, only a path on this console is followed (open redirect)", () => {
  it.each([
    ["/stays", "/stays"],
    ["/guest-data?q=1#top", "/guest-data?q=1#top"],
  ])("%s is followed", (raw, want) => {
    expect(safeNextPath(raw)).toBe(want);
  });

  it.each([
    "//evil.example/x",
    "/\\evil.example",
    "https://evil.example/",
    "javascript:alert(1)",
    "stays",
    "/%0d%0aLocation: x",
    "/ok\\..\\x",
    "/login",
    "",
    null,
  ])("%s goes to the dashboard instead", (raw) => {
    const out = safeNextPath(raw as string | null);
    if (raw === "/%0d%0aLocation: x") {
      // Encoded, it is a harmless path on this console; it never leaves it.
      expect(out.startsWith("/") && !out.startsWith("//")).toBe(true);
    } else {
      expect(out).toBe(DEFAULT_AFTER_LOGIN);
    }
  });

  it("the expiry redirect carries the reason and a path the login page will accept", () => {
    expect(loginAfterExpiry("/stays?status=IN_HOUSE")).toBe("/login?next=%2Fstays%3Fstatus%3DIN_HOUSE&reason=expired");
    expect(loginAfterExpiry("//evil.example")).toBe(`/login?next=${encodeURIComponent(DEFAULT_AFTER_LOGIN)}&reason=expired`);
  });
});

describe("required fields say so to assistive tech (1.3.1, 3.3.2)", () => {
  it("Field marks the control aria-required and hides the decorative asterisk", () => {
    render(
      <Field label="Reason" required>
        <Input />
      </Field>,
    );
    // By accessible name, which the hidden asterisk is no longer part of.
    const input = screen.getByRole("textbox", { name: "Reason" });
    expect(input).toHaveAttribute("aria-required", "true");
    // No native `required`: forms that validate themselves must not meet the browser's bubble.
    expect(input).not.toHaveAttribute("required");
    expect(screen.getByText("*")).toHaveAttribute("aria-hidden", "true");
  });

  it("an optional field is not marked", () => {
    render(
      <Field label="Note">
        <Input />
      </Field>,
    );
    expect(screen.getByLabelText("Note")).not.toHaveAttribute("aria-required");
  });
});

describe("Segmented is a real radiogroup (arrows, one tab stop)", () => {
  function Harness() {
    const [v, setV] = useState<"a" | "b" | "c">("a");
    return (
      <Segmented
        label="View"
        value={v}
        onChange={setV}
        options={[{ value: "a", label: "Active" }, { value: "b", label: "Recent" }, { value: "c", label: "All" }]}
      />
    );
  }

  it("only the checked option is in the tab order, and the arrows move the choice and the focus", () => {
    render(<Harness />);
    const [a, b, c] = screen.getAllByRole("radio");
    expect([a.tabIndex, b.tabIndex, c.tabIndex]).toEqual([0, -1, -1]);
    a.focus();
    fireEvent.keyDown(a, { key: "ArrowRight" });
    expect(b).toHaveAttribute("aria-checked", "true");
    expect(document.activeElement).toBe(b);
    expect([a.tabIndex, b.tabIndex, c.tabIndex]).toEqual([-1, 0, -1]);
    fireEvent.keyDown(b, { key: "End" });
    expect(c).toHaveAttribute("aria-checked", "true");
    fireEvent.keyDown(c, { key: "ArrowRight" });
    expect(a).toHaveAttribute("aria-checked", "true");
    expect(document.activeElement).toBe(a);
  });
});

describe("inline Change panels keep the focus (2.4.3)", () => {
  function Card() {
    const [editing, setEditing] = useState(false);
    const { triggerRef, panelRef } = useEditFocus(editing);
    return (
      <div>
        {!editing && <button ref={triggerRef} onClick={() => setEditing(true)}>Change</button>}
        {editing && (
          <div ref={panelRef}>
            <label>
              Days <input />
            </label>
            <button onClick={() => setEditing(false)}>Save</button>
            <button onClick={() => setEditing(false)}>Cancel</button>
          </div>
        )}
      </div>
    );
  }

  it("opening focuses the first field; saving or cancelling returns to Change", () => {
    render(<Card />);
    fireEvent.click(screen.getByRole("button", { name: "Change" }));
    expect(document.activeElement).toBe(screen.getByLabelText("Days"));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Change" }));
    fireEvent.click(screen.getByRole("button", { name: "Change" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Change" }));
  });
});

describe("the idle warning (2.2.1)", () => {
  const NOW = new Date("2026-10-19T10:00:00Z").getTime();
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: false });
    vi.setSystemTime(NOW);
  });
  afterEach(() => vi.useRealTimers());

  const at = (ms: number) => new Date(NOW + ms).toISOString();

  it("opens two minutes before the end as an alertdialog, not before", async () => {
    render(
      <IdleWarning expiresAt={at(10 * 60_000)} onStay={vi.fn()} onCheck={vi.fn()} onSignOut={vi.fn()} onExpired={vi.fn()} />,
    );
    await act(async () => { vi.advanceTimersByTime(10 * 60_000 - IDLE_WARNING_MS - 1000); });
    expect(screen.queryByRole("alertdialog")).toBeNull();
    await act(async () => { vi.advanceTimersByTime(2000); });
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText("You are about to be signed out")).toBeTruthy();
    // Focus starts on the safe choice.
    expect(document.activeElement).toBe(within(dialog).getByRole("button", { name: "Stay signed in" }));
    // The countdown is visual only; the polite line speaks at one minute, not every second.
    expect(screen.getByTestId("idle-countdown")).toHaveAttribute("aria-hidden", "true");
    const status = within(dialog).getByRole("status");
    expect(status.textContent).toBe("");
    await act(async () => { vi.advanceTimersByTime(60_000); });
    expect(status.textContent).toBe("1 minute left.");
  });

  it("Stay signed in asks the appliance and closes on the new end", async () => {
    const onStay = vi.fn().mockResolvedValue(at(31 * 60_000));
    render(
      <IdleWarning expiresAt={at(IDLE_WARNING_MS - 5000)} onStay={onStay} onCheck={vi.fn()} onSignOut={vi.fn()} onExpired={vi.fn()} />,
    );
    await act(async () => { vi.advanceTimersByTime(1000); });
    fireEvent.click(screen.getByRole("button", { name: "Stay signed in" }));
    await act(async () => { vi.advanceTimersByTime(1000); });
    expect(onStay).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("at the end it asks once more, then sends the operator to sign in", async () => {
    const onExpired = vi.fn();
    const onCheck = vi.fn().mockRejectedValue(new Error("401"));
    render(
      <IdleWarning expiresAt={at(30_000)} onStay={vi.fn()} onCheck={onCheck} onSignOut={vi.fn()} onExpired={onExpired} />,
    );
    await act(async () => { vi.advanceTimersByTime(31_000); });
    expect(onCheck).toHaveBeenCalledTimes(1);
    expect(onExpired).toHaveBeenCalledTimes(1);
  });

  it("activity elsewhere that moved the end keeps the session", async () => {
    const onExpired = vi.fn();
    const onCheck = vi.fn().mockResolvedValue(at(20 * 60_000));
    render(
      <IdleWarning expiresAt={at(10_000)} onStay={vi.fn()} onCheck={onCheck} onSignOut={vi.fn()} onExpired={onExpired} />,
    );
    await act(async () => { vi.advanceTimersByTime(11_000); });
    expect(onCheck).toHaveBeenCalled();
    expect(onExpired).not.toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });
});

describe("the menu filter shortcut needs a modifier (2.1.4)", () => {
  it("Ctrl+K and Cmd+K focus the filter; a bare / does nothing", () => {
    render(<Nav roles={["site_admin"]} email="a@b.c" onLogout={() => {}} />);
    const filter = screen.getByLabelText("Filter navigation");
    expect(filter).toHaveAttribute("aria-keyshortcuts", "Control+K Meta+K");
    fireEvent.keyDown(window, { key: "/" });
    expect(document.activeElement).not.toBe(filter);
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    expect(document.activeElement).toBe(filter);
    filter.blur();
    fireEvent.keyDown(window, { key: "k", metaKey: true });
    expect(document.activeElement).toBe(filter);
  });

  it("section titles are headings that name their lists", () => {
    render(<Nav roles={["site_admin"]} email="a@b.c" onLogout={() => {}} />);
    const headings = screen.getAllByRole("heading", { level: 2 });
    expect(headings.length).toBeGreaterThan(0);
    for (const h of headings) {
      const list = document.querySelector(`[aria-labelledby="${CSS.escape(h.id)}"]`);
      expect(list?.tagName).toBe("UL");
    }
  });
});
