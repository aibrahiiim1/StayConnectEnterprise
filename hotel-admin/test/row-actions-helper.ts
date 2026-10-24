import { fireEvent, screen } from "@testing-library/react";

// Radix menus measure with ResizeObserver, which jsdom lacks.
if (!("ResizeObserver" in globalThis)) {
  (globalThis as Record<string, unknown>).ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
}

/** Opens a row's "⋯" menu (RowActions) and chooses an item. The trigger opens from the keyboard in jsdom. */
export async function chooseRowAction(subject: string | RegExp, item: string | RegExp, root: HTMLElement = document.body) {
  const name = typeof subject === "string" ? `Actions for ${subject}` : new RegExp(`^Actions for ${subject.source}`, subject.flags);
  const triggers = screen.getAllByRole("button", { name }).filter((b) => root.contains(b));
  const trigger = triggers[0];
  fireEvent.keyDown(trigger, { key: "Enter" });
  fireEvent.click(await screen.findByRole("menuitem", { name: item }));
}
