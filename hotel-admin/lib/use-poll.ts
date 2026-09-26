"use client";

import { useEffect, useRef } from "react";

/**
 * usePoll -- run `fn` every `ms` while the tab is VISIBLE.
 *
 * Every poll on this admin used a bare setInterval, so a console left open in a background tab kept asking the
 * appliance for sessions every ten seconds all night, for a screen nobody could see. A hidden tab now skips its
 * ticks, and becoming visible again refreshes at once rather than waiting out the rest of the interval: the
 * operator switching back sees current data, not whatever was on screen when they left.
 *
 * The first load is the caller's; this only schedules the repeats. `fn` is read through a ref, so a new closure
 * each render does not restart the timer. `enabled: false` stops polling without unmounting anything.
 */
export function usePoll(fn: () => void, ms: number, { enabled = true }: { enabled?: boolean } = {}) {
  const latest = useRef(fn);
  useEffect(() => {
    latest.current = fn;
  }, [fn]);

  useEffect(() => {
    if (!enabled) return;
    const hidden = () => typeof document !== "undefined" && document.hidden;
    const iv = window.setInterval(() => {
      if (!hidden()) latest.current();
    }, ms);
    const onVisibility = () => {
      if (!hidden()) latest.current();
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.clearInterval(iv);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [ms, enabled]);
}
