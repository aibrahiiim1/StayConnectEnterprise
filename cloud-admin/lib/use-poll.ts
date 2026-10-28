"use client";

import { useEffect, useRef } from "react";
import { BACKGROUND, type RequestOptions } from "@/lib/api";

/**
 * Pause in hidden tabs; timer requests carry BACKGROUND so polling cannot extend the operator's session.
 * When fn returns a promise, a tick that comes while the previous call is still pending is skipped, so slow
 * answers never stack up (performance audit D8-L1).
 */
export function usePoll(fn: (opts: RequestOptions) => void | Promise<unknown>, ms: number, enabled = true) {
  const ref = useRef(fn);
  ref.current = fn;
  const busy = useRef(false);
  useEffect(() => {
    if (!enabled) return;
    let timer: ReturnType<typeof setInterval> | null = null;
    const tick = () => {
      if (busy.current) return;
      const p = ref.current(BACKGROUND);
      if (p && typeof (p as Promise<unknown>).finally === "function") {
        busy.current = true;
        void (p as Promise<unknown>).catch(() => undefined).finally(() => { busy.current = false; });
      }
    };
    const start = () => {
      if (timer === null) timer = setInterval(tick, ms);
    };
    const stop = () => {
      if (timer !== null) clearInterval(timer);
      timer = null;
    };
    const onVisibility = () => {
      if (document.hidden) stop();
      else {
        tick();
        start();
      }
    };
    if (!document.hidden) start();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [ms, enabled]);
}
