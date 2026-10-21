"use client";

import { useEffect, useRef } from "react";
import { BACKGROUND, type RequestOptions } from "@/lib/api";

/**
 * Calls `fn` every `ms` while the tab is visible, and once immediately when a hidden tab becomes visible again.
 * A background tab asks the server nothing: an operator with ten Central tabs open is not ten pollers.
 *
 * `fn` is given BACKGROUND and must pass it to every request it makes (`api.get(path, opts)`): these calls come
 * from a timer, not from the person, and must not keep the sign-in alive (lib/api.ts, X-OneGate-Background).
 */
export function usePoll(fn: (opts: RequestOptions) => void, ms: number, enabled = true) {
  const ref = useRef(fn);
  ref.current = fn;
  useEffect(() => {
    if (!enabled) return;
    let timer: ReturnType<typeof setInterval> | null = null;
    const start = () => {
      if (timer === null) timer = setInterval(() => ref.current(BACKGROUND), ms);
    };
    const stop = () => {
      if (timer !== null) clearInterval(timer);
      timer = null;
    };
    const onVisibility = () => {
      if (document.hidden) stop();
      else {
        ref.current(BACKGROUND);
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
