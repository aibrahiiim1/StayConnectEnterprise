"use client";

// ONE PAGING PATTERN FOR EVERY ADMIN CONSOLE LIST THAT GROWS WITH REAL DATA.
//
// edged pages these lists itself: ?page (1-based) and ?page_size (1..200), answering meta.has_more and, where it
// is cheap, the total. The browser never holds more than one page, and never filters or counts the page it
// holds as if it were the whole list -- that is how the old screens came to say "200 sessions" on a busy night.
//
// What this hook owns, so every list behaves the same:
//
//   * A NEW QUESTION STARTS AT PAGE 1. The position is remembered against the question (path, headers, page
//     size); change any of them and the offset is 0 on the very same render. Resetting with an effect instead
//     would first fire the old offset's request for the new question.
//   * ONLY THE LATEST QUESTION MAY ANSWER. Each request takes a number; a response is applied only if no newer
//     request started since. Quick typing, or a filter changed on page 3, cannot land an old page last.
//   * SEARCH TEXT GOES IN A HEADER. The caller passes it in `headers`, never in `path`: edged logs request lines,
//     and the search box is where an operator types a guest's name.
//   * A PAGE THAT EMPTIED steps back. A poll can shrink the list under the page being read (guests leave).

import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";

export const PAGE_SIZE = 50;

/** The paging fields edged adds to a list answer. */
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
  /** The list path with its filters, without page parameters. */
  path: string;
  /** Request headers -- the search text goes here. Omit (undefined) when there is none. */
  headers?: Record<string, string>;
  pageSize?: number;
  /** False holds the request (for a question that is not ready to be asked yet). */
  enabled?: boolean;
  /** Where the rows are in the answer, to notice a page that has emptied. Defaults to `data`. */
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
  const headerKey = headers ? JSON.stringify(headers) : "";
  const reload = useCallback(async () => {
    const mine = ++latest.current;
    setLoading(true);
    try {
      const r = await api.get<R>(url, headerKey ? (JSON.parse(headerKey) as Record<string, string>) : undefined);
      if (mine !== latest.current) return; // superseded
      setResp(r);
      setRespUrl(url + "\n" + headerKey);
      setErr(null);
    } catch (e) {
      if (mine !== latest.current) return; // superseded
      setErr(e);
    } finally {
      if (mine === latest.current) setLoading(false);
    }
  }, [url, headerKey]);

  useEffect(() => {
    if (enabled) void reload();
  }, [reload, enabled]);

  // A page that emptied under the reader steps back one page, rather than showing "no rows" on page 5.
  const current = respUrl === url + "\n" + headerKey;
  const shown = resp ? rowsOf(resp)?.length ?? 0 : 0;
  useEffect(() => {
    if (current && resp && shown === 0 && offset > 0) setOffset(offset - pageSize);
  }, [current, resp, shown, offset, pageSize, setOffset]);

  return {
    /** The last answer that arrived, which may belong to the previous question while `current` is false. */
    resp,
    /** Whether `resp` answers the question the controls ask now. */
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
