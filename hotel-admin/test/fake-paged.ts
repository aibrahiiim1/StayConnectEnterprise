// FAKE PAGED LIST ANSWERS, shaped the way edged answers since the lists moved to server paging.
//
// Tests mock api.get. A mock that returned the old unpaged shape would let a screen pass that cannot read the
// real one; these return data, meta.has_more, page, page_size and total exactly as edged does, and apply the
// page parameters to the rows they were given.

export type AuditRowLike = { action: string; [k: string]: unknown };

const pageParams = (path: string) => {
  const u = new URL("http://x" + path);
  const page = Number(u.searchParams.get("page") ?? "1");
  const size = Number(u.searchParams.get("page_size") ?? "50");
  return { u, page, size };
};

/** One page of `rows` for the request's page parameters, with the paging fields edged adds. */
export function pageOf<T>(path: string, rows: T[], extra: Record<string, unknown> = {}) {
  const { page, size } = pageParams(path);
  const from = (page - 1) * size;
  const data = rows.slice(from, from + size);
  return { data, meta: { has_more: from + size < rows.length }, page, page_size: size, total: rows.length, ...extra };
}

/** A fake GET /audit: applies action=, answers the period's per-action counts, and pages. */
export function fakeAudit(rows: AuditRowLike[]) {
  return (path: string) => {
    const { u } = pageParams(path);
    const actions = new Map<string, number>();
    for (const r of rows) actions.set(r.action, (actions.get(r.action) ?? 0) + 1);
    let shown = rows;
    if (u.searchParams.has("action")) {
      const codes = (u.searchParams.get("action") ?? "").split(",").filter(Boolean);
      shown = rows.filter((r) => codes.includes(r.action));
    }
    return pageOf(path, shown, { actions: [...actions].map(([action, count]) => ({ action, count })) });
  };
}
