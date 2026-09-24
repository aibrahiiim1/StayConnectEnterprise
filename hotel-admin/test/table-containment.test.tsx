import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { Table, THead, TR, TH } from "@/components/ui/table";

// FOUND LIVE ON PRE-LIVE: at 390px the Internet packages page was 759px wide. The table scrolled correctly
// inside its own container, but the sr-only "Actions" header label is position:absolute, and with an
// unpositioned scroller its containing block was OUTSIDE the scroller -- so it stretched the whole document.
describe("Table scroll container", () => {
  it("is the containing block for absolutely positioned descendants", () => {
    const { container } = render(
      <Table>
        <THead>
          <TR>
            <TH>
              <span className="sr-only">Actions</span>
            </TH>
          </TR>
        </THead>
      </Table>,
    );
    const scroller = container.querySelector("table")!.parentElement!;
    expect(scroller.className).toMatch(/(^|\s)relative(\s|$)/);
    expect(scroller.className).toMatch(/overflow-x-auto/);
  });
});
