"use client";

import { useEffect, useRef } from "react";

import { inBackground } from "@/lib/api";

/**
 * Runs `fn` every `ms` while the tab is VISIBLE; becoming visible refreshes at once. The first load is the caller's.
 * `fn` is read through a ref; `enabled: false` stops polling. Requests are marked background (inBackground), so return
 * the promise from `fn` (`() => load()`, not `() => void load()`).
 */
export function usePoll(fn: () => unknown, ms: number, { enabled = true }: { enabled?: boolean } = {}) {
  const latest = useRef(fn);
  useEffect(() => {
    latest.current = fn;
  }, [fn]);

  useEffect(() => {
    if (!enabled) return;
    const hidden = () => typeof document !== "undefined" && document.hidden;
    const iv = window.setInterval(() => {
      if (!hidden()) inBackground(latest.current);
    }, ms);
    const onVisibility = () => {
      if (!hidden()) inBackground(latest.current);
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.clearInterval(iv);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [ms, enabled]);
}
