// A dialog moves focus to its first focusable element when it opens. In a form whose hints are icons that element is a
// hint, and the hint must not open by itself: it opens on hover, or on focus that came from the keyboard.

import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@/components/ui/dialog";
import { Field, Input, QuietHints } from "@/components/ui/input";

class ResizeObserverStub { observe() {} unobserve() {} disconnect() {} }
(globalThis as Record<string, unknown>).ResizeObserver = (globalThis as Record<string, unknown>).ResizeObserver ?? ResizeObserverStub;

describe("form hints", () => {
  it("do not open when a dialog opens and focuses its first hint", async () => {
    render(
      <Dialog open>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add a service plan</DialogTitle>
            <DialogDescription>Form</DialogDescription>
          </DialogHeader>
          <QuietHints>
            <Field label="Name" hint="What operators see in lists.">
              <Input />
            </Field>
          </QuietHints>
        </DialogContent>
      </Dialog>,
    );
    const hint = await screen.findByRole("button", { name: "What this means" });
    hint.focus();
    fireEvent.focus(hint);
    await new Promise((r) => setTimeout(r, 400));
    expect(screen.queryByRole("tooltip")).toBeNull();
    // Hovering still opens it.
    fireEvent.pointerMove(hint, { pointerType: "mouse" });
    expect(await screen.findByRole("tooltip")).toHaveTextContent("What operators see in lists.");
    // The words stay available to assistive technology through the field.
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveAccessibleDescription("What operators see in lists.");
  });
});
