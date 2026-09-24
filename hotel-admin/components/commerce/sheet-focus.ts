// When a record sheet opens, Radix moves focus to the first focusable element inside it. In these sheets that is
// often a support id whose tooltip then opens by itself and swallows the first Escape. Focusing the sheet itself
// keeps focus trapped inside the dialog (Escape and Tab behave normally) without popping anything open.
export function focusSheetItself(e: Event) {
  e.preventDefault();
  const el = e.currentTarget as HTMLElement | null;
  el?.focus?.();
}
