"use client";

import { useCallback, useEffect, useState } from "react";

/** Set width before paint to avoid hydration jumps; React reads the device-local attribute for labels and ARIA. */
export const SIDEBAR_STORAGE_KEY = "onegate-central-sidebar";

/** Absence means expanded, so the default needs no stored value. */
const COLLAPSED = "collapsed";

/** localStorage can throw when site data is blocked; the shell still renders expanded. */
export const SIDEBAR_INIT_SCRIPT = `(function(){try{
if(localStorage.getItem(${JSON.stringify(SIDEBAR_STORAGE_KEY)})===${JSON.stringify(COLLAPSED)}){
document.documentElement.setAttribute("data-sidebar",${JSON.stringify(COLLAPSED)});}
}catch(e){}})();`;

function readAttribute(): boolean {
  if (typeof document === "undefined") return false;
  return document.documentElement.getAttribute("data-sidebar") === COLLAPSED;
}

export function useSidebarCollapsed(): {
  collapsed: boolean;
  setCollapsed: (next: boolean) => void;
  toggle: () => void;
} {
  const [collapsed, setState] = useState<boolean>(readAttribute);

  // The pre-paint attribute is the visible state; hydrate React from it after mount.
  useEffect(() => {
    setState(readAttribute());
  }, []);

  // Keep tabs on the same device in sync.
  useEffect(() => {
    const onStorage = (e: StorageEvent) => {
      if (e.key !== SIDEBAR_STORAGE_KEY) return;
      const next = e.newValue === COLLAPSED;
      document.documentElement.toggleAttribute("data-sidebar", false);
      if (next) document.documentElement.setAttribute("data-sidebar", COLLAPSED);
      setState(next);
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const setCollapsed = useCallback((next: boolean) => {
    setState(next);
    if (typeof document !== "undefined") {
      if (next) document.documentElement.setAttribute("data-sidebar", COLLAPSED);
      else document.documentElement.removeAttribute("data-sidebar");
    }
    try {
      // Store only the non-default choice.
      if (next) window.localStorage.setItem(SIDEBAR_STORAGE_KEY, COLLAPSED);
      else window.localStorage.removeItem(SIDEBAR_STORAGE_KEY);
    } catch {
      // Apply the choice for this page even when storage is denied.
    }
  }, []);

  const toggle = useCallback(() => setCollapsed(!collapsed), [collapsed, setCollapsed]);

  return { collapsed, setCollapsed, toggle };
}
