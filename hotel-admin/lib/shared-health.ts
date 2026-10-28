"use client";

import { api, type EdgeHealth } from "@/lib/api";

// The top-bar badge and the dashboard both show /health on the same 30 s cadence. One answer serves both when they
// ask within a few seconds of each other, instead of two identical requests (performance audit D8-L2).

const SHARE_MS = 5_000;
let last: { at: number; answer: Promise<EdgeHealth> } | null = null;

export function getSharedHealth(): Promise<EdgeHealth> {
  const now = Date.now();
  if (last && now - last.at < SHARE_MS) return last.answer;
  const answer = api.get<EdgeHealth>("/health");
  const entry = { at: now, answer };
  last = entry;
  // A failure is not shared: the next caller asks again.
  answer.catch(() => {
    if (last === entry) last = null;
  });
  return answer;
}

/** Test seam: forget the shared answer. */
export function resetSharedHealth() {
  last = null;
}
