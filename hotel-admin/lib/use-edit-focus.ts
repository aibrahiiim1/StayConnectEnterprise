"use client";

import { useEffect, useRef } from "react";

// Focus for inline "Change" panels (WCAG 2.4.3): opening moves focus to the first field; closing returns it to the
// opener
// (`triggerRef`, else whatever had focus when the panel opened).

const FIRST_FIELD = [
  'input:not([type="hidden"]):not([type="radio"]):not([disabled])',
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

  if (editing && !was.current && typeof document !== "undefined") {
    const active = document.activeElement;
    opener.current = active instanceof HTMLElement && active !== document.body ? active : null;
  }

  return { triggerRef, panelRef };
}
