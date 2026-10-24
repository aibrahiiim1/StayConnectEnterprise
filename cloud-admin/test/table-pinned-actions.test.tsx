import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, it, expect } from "vitest";
import { fireEvent, render } from "@testing-library/react";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { RowActions } from "@/components/ui/row-actions";

// A table wider than its card must never hide its row actions behind the horizontal scroll (docs/ui/DESIGN_SYSTEM.md).
function scrollerOf(container: HTMLElement) {
  return container.querySelector("table")!.parentElement as HTMLDivElement;
}

function setGeometry(el: HTMLElement, g: { scrollWidth: number; clientWidth: number; scrollLeft: number }) {
  Object.defineProperty(el, "scrollWidth", { configurable: true, value: g.scrollWidth });
  Object.defineProperty(el, "clientWidth", { configurable: true, value: g.clientWidth });
  Object.defineProperty(el, "scrollLeft", { configurable: true, writable: true, value: g.scrollLeft });
}

describe("pinned row actions", () => {
  it("TD and TH pinEnd carry the pin class", () => {
    const { container } = render(
      <Table>
        <THead><TR><TH>Name</TH><TH pinEnd><span className="sr-only">Actions</span></TH></TR></THead>
        <TBody><TR><TD>alex</TD><TD pinEnd><button type="button">Open</button></TD></TR></TBody>
      </Table>,
    );
    expect(container.querySelector("th:last-child")!.className).toMatch(/(^|\s)cell-pin-end(\s|$)/);
    expect(container.querySelector("td:last-child")!.className).toMatch(/(^|\s)cell-pin-end(\s|$)/);
    expect(container.querySelector("td:first-child")!.className).not.toMatch(/cell-pin-end/);
  });

  it("RowActions marks itself so its last cell pins without a prop", () => {
    const { container, getByRole } = render(
      <Table>
        <TBody><TR><TD>alex</TD><TD><RowActions subject="alex" primary={{ label: "Edit", onSelect: () => {} }} /></TD></TR></TBody>
      </Table>,
    );
    expect(container.querySelector("td:last-child > [data-row-actions]")).not.toBeNull();
    // The accessible name is unchanged.
    expect(getByRole("button", { name: "Edit: alex" })).toBeTruthy();
  });

  it("the stylesheet pins the actions cell to the inline end, opaque, in both directions", () => {
    const css = readFileSync(join(__dirname, "..", "app", "globals.css"), "utf8");
    expect(css).toMatch(/td\.cell-pin-end, td:last-child:has\(\[data-row-actions\]\)\)\s*\{[^}]*position: sticky;[^}]*inset-inline-end: 0;/);
    expect(css).toMatch(/background: linear-gradient\(var\(--pin-tint\), var\(--pin-tint\)\), hsl\(var\(--card\)\)/);
    expect(css).toMatch(/tbody > tr:hover > :is\(td\.cell-pin-end/);
    expect(css).toMatch(/tr\[aria-selected="true"\] > :is\(td\.cell-pin-end/);
    expect(css).toMatch(/\[dir="rtl"\] :is\(\.cell-pin-end/);
    expect(css).toMatch(/\[data-scroll-under="true"\] :is\(\.cell-pin-end[^{]*::before \{ opacity: 1; \}/);
  });

  it("flags content under the pinned cell only while some is hidden, in LTR and RTL", () => {
    const { container } = render(
      <Table>
        <TBody><TR><TD>alex</TD><TD pinEnd>⋯</TD></TR></TBody>
      </Table>,
    );
    const el = scrollerOf(container);
    expect(el.getAttribute("data-scroll-under")).toBeNull();

    setGeometry(el, { scrollWidth: 1200, clientWidth: 900, scrollLeft: 0 });
    fireEvent.scroll(el);
    expect(el.getAttribute("data-scroll-under")).toBe("true");

    setGeometry(el, { scrollWidth: 1200, clientWidth: 900, scrollLeft: 300 });
    fireEvent.scroll(el);
    expect(el.getAttribute("data-scroll-under")).toBeNull();

    // RTL scrolls with a negative scrollLeft towards the inline end.
    setGeometry(el, { scrollWidth: 1200, clientWidth: 900, scrollLeft: -100 });
    fireEvent.scroll(el);
    expect(el.getAttribute("data-scroll-under")).toBe("true");
    setGeometry(el, { scrollWidth: 1200, clientWidth: 900, scrollLeft: -300 });
    fireEvent.scroll(el);
    expect(el.getAttribute("data-scroll-under")).toBeNull();
  });
});
