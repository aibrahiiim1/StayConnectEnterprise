// Focus the sheet so its first tooltip cannot open automatically and swallow Escape.
export function focusSheetItself(e: Event) {
  e.preventDefault();
  const el = e.currentTarget as HTMLElement | null;
  el?.focus?.();
}
