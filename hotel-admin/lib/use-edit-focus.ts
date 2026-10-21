"use client";

import { useEffect, useRef } from "react";

// FOCUS FOR THE INLINE "CHANGE" PANELS (WCAG 2.4.3).
//
// Several settings cards swap their read-only view for a form in place: "Change" hides itself and the fields
// appear. The button that had focus was unmounted, so the browser dropped focus on <body> -- a keyboard or
// screen-reader user pressed Change and was thrown back to the top of the page, and pressing Save or Cancel did it
// again. This keeps them where they were: opening moves focus to the first field, closing (saved or cancelled)
// returns it to the button that opened the panel.
//
//   const { triggerRef, panelRef } = useEditFocus(editing);
//   <Button ref={triggerRef} onClick={() => setEditing(true)}>Change</Button>
//   {editing && <div ref={panelRef}>…fields…</div>}
//
// When the opener is not the one `triggerRef` names (a card with two ways in), focus returns to whatever had it
// when the panel opened, if that is still on the page.

const FIRST_FIELD = [
  'input:not([type="hidden"]):not([type="radio"]):not([disabled])',
  // A radio group is entered at its checked option, as Tab would.
  'input[type="radio"]:checked:not([disabled])',
  "select:not([disabled])",
  "textarea:not([disabled])",
  '[contenteditable="true"]',
  '[role="radio"][tabindex="0"]',
  '[role="switch"]:not([disabled])',
].join(", ");

const ANY_FOCUSABLE = 'button:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])';

export function useEditFocus<P extends HTMLElement = HTMLDivElement, T extends HTMLElement = HTMLButtonElement>(
  editing: boolean,
) {
  const triggerRef = useRef<T | null>(null);
  const panelRef = useRef<P | null>(null);
  const opener = useRef<HTMLElement | null>(null);
  const was = useRef(editing);

  useEffect(() => {
    if (was.current === editing) return;
    was.current = editing;
    if (editing) {
      const panel = panelRef.current;
      const first = panel?.querySelector<HTMLElement>(FIRST_FIELD) ?? panel?.querySelector<HTMLElement>(ANY_FOCUSABLE);
      first?.focus();
      return;
    }
    const back = triggerRef.current ?? (opener.current?.isConnected ? opener.current : null);
    back?.focus();
  }, [editing]);

  // Remember what had focus at the moment of opening, before the effect above moves it.
  if (editing && !was.current && typeof document !== "undefined") {
    const active = document.activeElement;
    opener.current = active instanceof HTMLElement && active !== document.body ? active : null;
  }

  return { triggerRef, panelRef };
}
