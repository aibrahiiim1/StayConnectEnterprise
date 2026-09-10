"use client";

import { useCallback, useEffect, useState } from "react";

/**
 * The operator's sidebar width preference: remembered, and applied BEFORE the first paint.
 *
 * WHY THE ATTRIBUTE EXISTS AND THE STATE IS NOT ENOUGH.
 *
 * React state cannot decide the sidebar's width without a visible jump. The server renders this app's shell
 * with no access to localStorage, so a state-driven width is necessarily "expanded" in the server HTML; a
 * collapsed operator would then see a 16rem rail snap to 3.5rem on hydration, every single navigation. The
 * width therefore comes from a CSS custom property keyed off a `data-sidebar` attribute that a tiny script
 * stamps onto <html> before paint -- the same technique next-themes already uses in this app for the theme,
 * and the reason <html> already carries suppressHydrationWarning.
 *
 * React state still exists, because the LABELS, TOOLTIPS and ARIA have to change too, and those are not
 * expressible in CSS. It is seeded from the attribute rather than from storage, so the value React renders is
 * by construction the value already on screen.
 *
 * The default is EXPANDED: an operator who has never chosen sees the full navigation, which is the mode where
 * the labels are readable. Nothing about this preference is per-site or per-role, so it is a plain
 * device-local setting and belongs in localStorage.
 */
export const SIDEBAR_STORAGE_KEY = "stayconnect-admin-sidebar";

/** The attribute value that means collapsed. Its ABSENCE means expanded, so the default needs no write. */
const COLLAPSED = "collapsed";

/**
 * The pre-paint script, as source. Injected by the root layout with dangerouslySetInnerHTML.
 *
 * It is wrapped in try/catch because localStorage throws outright in a browser with site data blocked, and a
 * navigation shell that fails to render at all is a far worse outcome than one that renders expanded.
 */
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

  // The attribute is the source of truth for what is ON SCREEN, so re-read it once after mount. During server
  // rendering readAttribute() answered false; if the script had in fact stamped "collapsed", this corrects
  // React's copy without ever having rendered a different width (the width was CSS all along).
  useEffect(() => {
    setState(readAttribute());
  }, []);

  // A second tab is still the same operator on the same device. Following the change keeps the two windows
  // from disagreeing about a preference that is explicitly device-local.
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
      // Only the non-default is stored. An operator who expands is back at the default and leaves no residue.
      if (next) window.localStorage.setItem(SIDEBAR_STORAGE_KEY, COLLAPSED);
      else window.localStorage.removeItem(SIDEBAR_STORAGE_KEY);
    } catch {
      // Storage denied: the choice still applies to this page, it simply will not outlive it.
    }
  }, []);

  const toggle = useCallback(() => setCollapsed(!collapsed), [collapsed, setCollapsed]);

  return { collapsed, setCollapsed, toggle };
}
