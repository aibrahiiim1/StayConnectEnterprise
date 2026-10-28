"use client";

import { useEffect, useRef } from "react";

import { inBackground } from "@/lib/api";

/**
 * Runs `fn` every `ms` while the tab is VISIBLE; becoming visible refreshes at once. The first load is the caller's.
 * `fn` is read through a ref; `enabled: false` stops polling. Requests are marked background (inBackground), so return
 * the promise from `fn` (`() => load()`, not `() => void load()`). A tick is skipped while the previous call has not
 * settled, so a slow answer never stacks a second copy of the same query (performance audit D8-L1).
 */
export function usePoll(fn: () => unknown, ms: number, { enabled = true }: { enabled?: boolean } = {}) {
  const latest = useRef(fn);
  useEffect(() => {
    latest.current = fn;
  }, [fn]);

  useEffect(() => {
    if (!enabled) return;
    let inFlight = false;
    const hidden = () => typeof document !== "undefined" && document.hidden;
    const run = () => {
      if (hidden() || inFlight) return;
      let out: unknown;
      try {
        out = inBackground(latest.current);
      } catch {
        return;
      }
      if (out && typeof (out as PromiseLike<unknown>).then === "function") {
        inFlight = true;
        const done = () => {
          inFlight = false;
        };
        (out as PromiseLike<unknown>).then(done, done);
      }
    };
    const iv = window.setInterval(run, ms);
    document.addEventListener("visibilitychange", run);
    return () => {
      window.clearInterval(iv);
      document.removeEventListener("visibilitychange", run);
    };
  }, [ms, enabled]);
}
