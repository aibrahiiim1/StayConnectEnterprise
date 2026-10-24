"use client";

import { DescriptionList } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { sentenceCase } from "@/lib/status";

/** Loading, empty and no-match states of a list, so every list on Central handles them the same way. */
export function ListState({
  rows,
  cols = 5,
  filtered = false,
  empty,
  noMatch,
  children,
}: {
  rows: readonly unknown[] | null;
  cols?: number;
  /** True when a filter or search narrows the list; picks `noMatch` over `empty`. */
  filtered?: boolean;
  empty: React.ReactNode;
  noMatch?: React.ReactNode;
  children: React.ReactNode;
}) {
  if (rows === null) return <SkeletonRows rows={5} cols={cols} />;
  if (rows.length === 0) return <>{filtered && noMatch ? noMatch : empty}</>;
  return <>{children}</>;
}

const hasKeys = (v: unknown): v is Record<string, unknown> =>
  !!v && typeof v === "object" && !Array.isArray(v) && Object.keys(v as object).length > 0;

export function hasPayload(v: unknown): v is Record<string, unknown> {
  return hasKeys(v);
}

function show(v: unknown): React.ReactNode {
  if (v === null || v === undefined || v === "") return null;
  if (typeof v === "boolean") return v ? "Yes" : "No";
  if (typeof v === "string" || typeof v === "number") return <span className="break-all">{String(v)}</span>;
  if (Array.isArray(v) && v.every((x) => typeof x === "string" || typeof x === "number")) {
    return v.length ? <span className="break-all">{v.join(", ")}</span> : null;
  }
  return <code className="block whitespace-pre-wrap break-all font-mono text-xs text-muted-foreground">{JSON.stringify(v, null, 2)}</code>;
}

/** A record's payload as label/value rows: scalar fields read as text, nested values as formatted JSON. */
export function PayloadView({ payload }: { payload: Record<string, unknown> }) {
  return (
    <DescriptionList
      items={Object.entries(payload).map(([k, v]) => ({ label: sentenceCase(k), value: show(v) }))}
    />
  );
}
