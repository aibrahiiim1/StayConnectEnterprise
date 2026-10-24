import { fireEvent, screen, waitFor, within } from "@testing-library/react";

// Radix menus measure with ResizeObserver, which jsdom lacks.
if (!("ResizeObserver" in globalThis)) {
  (globalThis as Record<string, unknown>).ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
}

function triggerIn(root: HTMLElement, subject?: string | RegExp): HTMLElement | null {
  const name = subject === undefined
    ? /^Actions for /
    : typeof subject === "string"
      ? `Actions for ${subject}`
      : new RegExp(`^Actions for ${subject.source}`, subject.flags);
  return within(root).queryAllByRole("button", { name })[0] ?? null;
}

/** Opens the "⋯" menu (RowActions) inside `root`, or the one named for `subject`. The trigger opens from the keyboard in jsdom. */
export async function openRowMenu(root: HTMLElement = document.body, subject?: string | RegExp): Promise<HTMLElement> {
  const trigger = triggerIn(root, subject);
  if (!trigger) throw new Error("No row actions menu here");
  fireEvent.keyDown(trigger, { key: "Enter" });
  return screen.findByRole("menu");
}

/** Opens a row's menu by its subject and chooses an item. */
export async function chooseRowAction(subject: string | RegExp, item: string | RegExp, root: HTMLElement = document.body) {
  const menu = await openRowMenu(root, subject);
  fireEvent.click(within(menu).getByRole("menuitem", { name: item }));
}

/** Opens the menu inside a row element and chooses an item. */
export async function chooseInRow(row: HTMLElement, item: string | RegExp) {
  const menu = await openRowMenu(row);
  fireEvent.click(within(menu).getByRole("menuitem", { name: item }));
}

/** Whether the row's menu offers an item; closes the menu again. False when the row has no menu at all. */
export async function rowMenuHas(row: HTMLElement, item: string | RegExp): Promise<boolean> {
  if (!triggerIn(row)) return false;
  const menu = await openRowMenu(row);
  const has = within(menu).queryByRole("menuitem", { name: item }) !== null;
  fireEvent.keyDown(menu, { key: "Escape" });
  await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
  return has;
}
