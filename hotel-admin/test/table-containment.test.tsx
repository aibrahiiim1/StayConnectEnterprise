import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { Table, THead, TR, TH } from "@/components/ui/table";

// An absolutely positioned sr-only table header can widen the page if its scroller is not positioned.
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
