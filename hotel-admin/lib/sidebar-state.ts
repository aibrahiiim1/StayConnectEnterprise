"use client";

import { useCallback, useEffect, useState } from "react";

/** Set sidebar width before first paint to avoid a hydration snap; React then reads the device-local attribute. */
export const SIDEBAR_STORAGE_KEY = "stayconnect-admin-sidebar";

const COLLAPSED = "collapsed";

/** localStorage can throw when site data is blocked. */
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

  // The attribute is the source of truth for what is on screen; re-read it after mount.
  useEffect(() => {
    setState(readAttribute());
  }, []);

  // Follow changes from another tab (the preference is device-local).
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
      if (next) window.localStorage.setItem(SIDEBAR_STORAGE_KEY, COLLAPSED);
      else window.localStorage.removeItem(SIDEBAR_STORAGE_KEY);
    } catch {
    }
  }, []);

  const toggle = useCallback(() => setCollapsed(!collapsed), [collapsed, setCollapsed]);

  return { collapsed, setCollapsed, toggle };
}
