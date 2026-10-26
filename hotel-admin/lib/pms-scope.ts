"use client";

// Which PMS connections this site has, for screens that show something a PMS owns. A room number is only meaningful together with the connection it came from:
// "101" at one hotel and "101" at another are two different rooms, never a duplicate. Screens name the connection only where there is more than one to tell apart.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";

export type PmsScopeConnection = { id: string; label: string; lifecycle: string; retired: boolean };

type Raw = { id: string; display_label: string; lifecycle_state: string; published?: boolean };

let cache: { at: number; list: PmsScopeConnection[] } | null = null;
let inflight: Promise<PmsScopeConnection[]> | null = null;
const TTL_MS = 30_000;

async function fetchConnections(): Promise<PmsScopeConnection[]> {
  const r = await api.get<{ interfaces?: Raw[] }>("/pms-interfaces?include=retired");
  const list = (r?.interfaces ?? [])
    .filter((i) => i.published || i.lifecycle_state !== "AUTH_DISABLED")
    .map((i) => ({ id: i.id, label: i.display_label || "Unnamed connection", lifecycle: i.lifecycle_state, retired: i.lifecycle_state === "DECOMMISSIONED" }));
  cache = { at: Date.now(), list };
  return list;
}

export function resetPmsScopeCache() { cache = null; inflight = null; }

/** True when the site has more than one PMS connection to tell apart (a retired one still counts: its stays are history that remain listed). */
export function isMultiPms(list: PmsScopeConnection[] | null): boolean {
  return (list?.length ?? 0) > 1;
}

export function usePmsConnections(): { connections: PmsScopeConnection[] | null; multi: boolean; labelOf: (id?: string | null, fallback?: string | null) => string } {
  const [list, setList] = useState<PmsScopeConnection[] | null>(cache && Date.now() - cache.at < TTL_MS ? cache.list : null);
  useEffect(() => {
    let live = true;
    if (cache && Date.now() - cache.at < TTL_MS) { setList(cache.list); return; }
    inflight ??= fetchConnections().finally(() => { inflight = null; });
    inflight.then((l) => { if (live) setList(l); }).catch(() => { if (live) setList([]); });
    return () => { live = false; };
  }, []);
  return {
    connections: list,
    multi: isMultiPms(list),
    labelOf: (id, fallback) => list?.find((c) => c.id === id)?.label ?? fallback ?? "",
  };
}
