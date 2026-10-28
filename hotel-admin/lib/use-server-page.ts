"use client";

// One paging pattern for growing lists: edged pages them (?page, ?page_size 1..200); the browser holds one page.
// A new question (path, headers, page size) resets to page 1 on the same render; only the latest request may answer;
// search text goes in `headers`, never `path` (edged logs request lines); an emptied page steps back.

import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";

export const PAGE_SIZE = 50;

export type PagedFields = {
  meta?: { has_more?: boolean };
  page?: number;
  page_size?: number;
  total?: number;
};

export function useServerPage<R extends PagedFields>({
  path,
  headers,
  pageSize = PAGE_SIZE,
  enabled = true,
  rowsOf = (r: R) => (r as { data?: unknown[] }).data,
}: {
  path: string;
  headers?: Record<string, string>;
  pageSize?: number;
  enabled?: boolean;
  rowsOf?: (r: R) => unknown[] | undefined;
}) {
  const question = JSON.stringify([path, headers ?? null, pageSize]);
  const [pos, setPos] = useState({ question, offset: 0 });
  const offset = pos.question === question ? pos.offset : 0;
  const setOffset = useCallback((n: number) => setPos({ question, offset: Math.max(0, n) }), [question]);

  const sep = path.includes("?") ? "&" : "?";
  const url = `${path}${sep}page=${Math.floor(offset / pageSize) + 1}&page_size=${pageSize}`;

  const [resp, setResp] = useState<R | null>(null);
  const [respUrl, setRespUrl] = useState<string | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);

  const latest = useRef(0);
  const inflight = useRef<AbortController | null>(null);
  const headerKey = headers ? JSON.stringify(headers) : "";
  const reload = useCallback(async () => {
    const mine = ++latest.current;
    // A newer question cancels the older request, so edged stops its query instead of finishing it for nobody.
    inflight.current?.abort();
    const ctl = typeof AbortController !== "undefined" ? new AbortController() : null;
    inflight.current = ctl;
    setLoading(true);
    try {
      const r = await api.get<R>(url, headerKey ? (JSON.parse(headerKey) as Record<string, string>) : undefined,
        ctl ? { signal: ctl.signal } : undefined);
      if (mine !== latest.current) return; // superseded
      setResp(r);
      setRespUrl(url + "\n" + headerKey);
      setErr(null);
    } catch (e) {
      if (mine !== latest.current) return; // superseded
      setErr(e);
    } finally {
      if (mine === latest.current) {
        setLoading(false);
        inflight.current = null;
      }
    }
  }, [url, headerKey]);
  useEffect(() => () => inflight.current?.abort(), []);

  useEffect(() => {
    if (enabled) void reload();
  }, [reload, enabled]);

  const current = respUrl === url + "\n" + headerKey;
  const shown = resp ? rowsOf(resp)?.length ?? 0 : 0;
  useEffect(() => {
    if (current && resp && shown === 0 && offset > 0) setOffset(offset - pageSize);
  }, [current, resp, shown, offset, pageSize, setOffset]);

  return {
    resp,
    current,
    err,
    loading,
    offset,
    setOffset,
    pageSize,
    hasMore: !!resp?.meta?.has_more,
    total: typeof resp?.total === "number" ? resp.total : null,
    reload,
  };
}
