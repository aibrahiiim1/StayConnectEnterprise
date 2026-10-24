"use client";

import * as React from "react";
import { ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";

export type HistoryItem = {
  id: string;
  /** What happened, in a few words: "Speed limit changed", "Revision 14 applied". */
  title: React.ReactNode;
  /** Who did it: an operator email, "System", "PMS". */
  actor?: React.ReactNode;
  /** When. A Date or ISO string is formatted locally; any other node is shown as given. */
  at?: Date | string | React.ReactNode;
  /** One short line of what changed. The full detail opens in a DetailsDrawer. */
  summary?: React.ReactNode;
  tone?: "ok" | "warn" | "err" | "info" | "neutral";
  /** A Badge, e.g. "Current" or "Rolled back". */
  badge?: React.ReactNode;
};

const DOT: Record<NonNullable<HistoryItem["tone"]>, string> = {
  ok: "bg-success",
  warn: "bg-warning",
  err: "bg-destructive",
  info: "bg-info",
  neutral: "bg-muted-foreground/50",
};

function when(at: HistoryItem["at"]): React.ReactNode {
  if (at instanceof Date) return at.toLocaleString();
  if (typeof at === "string") {
    const t = Date.parse(at);
    return Number.isFinite(t) ? new Date(t).toLocaleString() : at;
  }
  return at;
}

/**
 * A compact list of changes — versions, revisions, audit entries: what, who, when, one line of summary. With
 * `onSelect`, each entry is a button that opens its full detail (in a DetailsDrawer). Newest first is the caller's
 * job.
 */
export function HistoryList({
  items,
  onSelect,
  selectedId,
  label = "History",
  emptyLabel = "No changes recorded yet.",
  className,
}: {
  items: HistoryItem[];
  onSelect?: (item: HistoryItem) => void;
  selectedId?: string | null;
  /** Names the list for assistive technology. */
  label?: string;
  emptyLabel?: React.ReactNode;
  className?: string;
}) {
  if (items.length === 0) {
    return <p className={cn("px-1 py-6 text-center text-sm text-muted-foreground", className)}>{emptyLabel}</p>;
  }
  return (
    <ol aria-label={label} className={cn("divide-y divide-border", className)}>
      {items.map((it) => {
        const selected = it.id === selectedId;
        const content = (
          <>
            <span className={cn("mt-1.5 size-2 shrink-0 rounded-full", DOT[it.tone ?? "neutral"])} aria-hidden />
            <span className="min-w-0 flex-1">
              <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="text-sm font-medium text-foreground">{it.title}</span>
                {it.badge}
              </span>
              {it.summary && <span className="mt-0.5 block truncate text-xs text-muted-foreground">{it.summary}</span>}
              {(it.actor || it.at) && (
                <span className="mt-0.5 block text-xs text-muted-foreground">
                  {it.actor}
                  {it.actor && it.at ? " · " : null}
                  {it.at ? <span className="tabular">{when(it.at)}</span> : null}
                </span>
              )}
            </span>
            {onSelect && <ChevronRight className="mt-1 size-4 shrink-0 text-muted-foreground rtl:rotate-180" aria-hidden />}
          </>
        );
        return (
          <li key={it.id}>
            {onSelect ? (
              <button
                type="button"
                onClick={() => onSelect(it)}
                aria-current={selected ? "true" : undefined}
                className={cn(
                  "flex w-full items-start gap-3 px-4 py-2.5 text-start transition-colors",
                  "hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                  selected && "bg-primary-subtle/50",
                )}
              >
                {content}
              </button>
            ) : (
              <div className="flex items-start gap-3 px-4 py-2.5">{content}</div>
            )}
          </li>
        );
      })}
    </ol>
  );
}

/** Field-by-field before/after for one change, for the body of a DetailsDrawer. */
export function ChangeDiff({
  changes,
  beforeLabel = "Before",
  afterLabel = "After",
  emptyLabel = "No field changed.",
  className,
}: {
  changes: { field: React.ReactNode; before?: React.ReactNode; after?: React.ReactNode }[];
  beforeLabel?: string;
  afterLabel?: string;
  emptyLabel?: React.ReactNode;
  className?: string;
}) {
  if (changes.length === 0) return <p className="text-sm text-muted-foreground">{emptyLabel}</p>;
  const dash = <span className="text-muted-foreground">—</span>;
  return (
    <div className={cn("overflow-x-auto rounded-md border border-border", className)}>
      <table className="w-full border-separate border-spacing-0 text-sm">
        <thead className="text-2xs font-medium text-muted-foreground">
          <tr className="[&>th]:border-b [&>th]:border-border [&>th]:bg-surface [&>th]:px-3 [&>th]:py-2 [&>th]:text-start">
            <th scope="col">Field</th>
            <th scope="col">{beforeLabel}</th>
            <th scope="col">{afterLabel}</th>
          </tr>
        </thead>
        <tbody>
          {changes.map((c, i) => (
            <tr key={i} className="align-top [&>*]:border-b [&>*]:border-border [&>*]:px-3 [&>*]:py-2 last:[&>*]:border-0">
              <th scope="row" className="text-start font-normal text-muted-foreground">{c.field}</th>
              <td className="break-words text-destructive-subtle-foreground">
                {c.before === undefined || c.before === null || c.before === "" ? dash : <del className="no-underline">{c.before}</del>}
              </td>
              <td className="break-words text-success-subtle-foreground">
                {c.after === undefined || c.after === null || c.after === "" ? dash : <ins className="no-underline">{c.after}</ins>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
