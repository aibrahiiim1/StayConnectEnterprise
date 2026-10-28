/**
 * Returns next, reusing the previous object for every row (matched by id) whose content did not change, so a
 * memoised row component re-renders only the rows that changed (performance audit D8-M2). When nothing changed
 * at all the previous array itself is returned.
 */
export function keepUnchanged<T extends { id: string }>(prev: readonly T[] | null | undefined, next: T[]): T[] {
  if (!prev || prev.length === 0) return next;
  const old = new Map<string, { row: T; json: string }>();
  for (const r of prev) old.set(r.id, { row: r, json: JSON.stringify(r) });
  let same = prev.length === next.length;
  const out = next.map((r, i) => {
    const o = old.get(r.id);
    if (o && o.json === JSON.stringify(r)) {
      if (prev[i] !== o.row) same = false;
      return o.row;
    }
    same = false;
    return r;
  });
  return same ? (prev as T[]) : out;
}
