import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { DialogForm } from "@/components/ui/dialog";

describe("the unsaved-change guard", () => {
  it("only discards on the explicit Discard changes, never on a repeated dismissal", () => {
    const onOpenChange = vi.fn();
    render(
      <DialogForm open onOpenChange={onOpenChange} title="Edit" submitLabel="Save" dirty onSubmit={(e) => e.preventDefault()}>
        <input aria-label="Name" />
      </DialogForm>,
    );
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    expect(screen.getByText("Discard the changes you made?")).toBeInTheDocument();
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    expect(onOpenChange).not.toHaveBeenCalledWith(false);
    expect(screen.getByText("Discard the changes you made?")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
