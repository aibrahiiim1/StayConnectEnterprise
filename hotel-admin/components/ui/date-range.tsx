"use client";

import * as React from "react";
import { CalendarDays } from "lucide-react";
import { cn } from "@/lib/utils";
import { Input, Select } from "./input";

export type PeriodPreset =
  | "all" | "today" | "yesterday" | "this_week" | "last_week" | "this_month" | "last_month" | "custom";

/** A period the reader picked; custom days are YYYY-MM-DD in the browser's time zone, both included. */
export type Period = { preset: PeriodPreset; from?: string; to?: string };

export const PERIOD_PRESETS: { value: PeriodPreset; label: string }[] = [
  { value: "all", label: "All time" },
  { value: "today", label: "Today" },
  { value: "yesterday", label: "Yesterday" },
  { value: "this_week", label: "This week" },
  { value: "last_week", label: "Last week" },
  { value: "this_month", label: "This month" },
  { value: "last_month", label: "Last month" },
  { value: "custom", label: "Custom range" },
];

const day = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate());
const addDays = (d: Date, n: number) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);

export function dayString(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

function parseDay(s?: string): Date | null {
  const m = s ? /^(\d{4})-(\d{2})-(\d{2})$/.exec(s) : null;
  if (!m) return null;
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  return d.getMonth() === Number(m[2]) - 1 ? d : null;
}

/**
 * The half-open interval [from, to) of local midnights a period covers; null for All time or an incomplete custom range.
 * Weeks start on Monday.
 */
export function periodBounds(p: Period, now: Date = new Date()): { from: Date; to: Date } | null {
  const today = day(now);
  const monday = addDays(today, -((today.getDay() + 6) % 7));
  const month = new Date(today.getFullYear(), today.getMonth(), 1);
  switch (p.preset) {
    case "today": return { from: today, to: addDays(today, 1) };
    case "yesterday": return { from: addDays(today, -1), to: today };
    case "this_week": return { from: monday, to: addDays(monday, 7) };
    case "last_week": return { from: addDays(monday, -7), to: monday };
    case "this_month": return { from: month, to: new Date(month.getFullYear(), month.getMonth() + 1, 1) };
    case "last_month": return { from: new Date(month.getFullYear(), month.getMonth() - 1, 1), to: month };
    case "custom": {
      const f = parseDay(p.from);
      const t = parseDay(p.to);
      return f && t && f <= t ? { from: f, to: addDays(t, 1) } : null;
    }
    default: return null;
  }
}

/** "1 – 6 Nov 2026" for the days a period covers, or "" for All time. */
export function periodWords(p: Period, now: Date = new Date()): string {
  const b = periodBounds(p, now);
  if (!b) return "";
  const last = addDays(b.to, -1);
  const full: Intl.DateTimeFormatOptions = { day: "numeric", month: "short", year: "numeric" };
  if (b.from.getTime() === last.getTime()) return b.from.toLocaleDateString(undefined, full);
  const sameYear = b.from.getFullYear() === last.getFullYear();
  const start = b.from.toLocaleDateString(undefined, sameYear ? { day: "numeric", month: "short" } : full);
  return `${start} – ${last.toLocaleDateString(undefined, full)}`;
}

/**
 * The period control: a preset list and, for Custom range, From and To days picked on the browser's calendar. A custom
 * range reaches `onChange` only once both days are set and in order, so a half-typed range never starts a query.
 */
export function DateRangeFilter({
  value,
  onChange,
  label = "Period",
  className,
}: {
  value: Period;
  onChange: (p: Period) => void;
  label?: string;
  className?: string;
}) {
  const [draft, setDraft] = React.useState({ from: value.from ?? "", to: value.to ?? "" });
  React.useEffect(() => setDraft({ from: value.from ?? "", to: value.to ?? "" }), [value.from, value.to]);
  const today = dayString(new Date());
  const custom = value.preset === "custom";
  const backwards = custom && !!draft.from && !!draft.to && draft.from > draft.to;

  function pick(preset: PeriodPreset) {
    if (preset !== "custom") return onChange({ preset });
    const b = periodBounds(value) ?? periodBounds({ preset: "today" })!;
    onChange({ preset, from: dayString(b.from), to: dayString(addDays(b.to, -1)) });
  }

  function edit(next: { from: string; to: string }) {
    setDraft(next);
    if (parseDay(next.from) && parseDay(next.to) && next.from <= next.to) onChange({ preset: "custom", ...next });
  }

  return (
    <div className={cn("flex min-w-0 flex-wrap items-center gap-2", className)}>
      <div className="relative w-full sm:w-auto">
        <CalendarDays className="pointer-events-none absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
        <Select aria-label={label} className="w-full ps-8 sm:w-40" value={value.preset}
          onChange={(e) => pick(e.target.value as PeriodPreset)}>
          {PERIOD_PRESETS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
        </Select>
      </div>
      {custom ? (
        <div className="flex items-center gap-1.5">
          <Input type="date" aria-label="From" className="w-[9.5rem]" value={draft.from} max={draft.to || today}
            aria-invalid={backwards || undefined} onChange={(e) => edit({ ...draft, from: e.target.value })} />
          <span className="text-muted-foreground" aria-hidden>–</span>
          <Input type="date" aria-label="To" className="w-[9.5rem]" value={draft.to} min={draft.from || undefined}
            aria-invalid={backwards || undefined} onChange={(e) => edit({ ...draft, to: e.target.value })} />
        </div>
      ) : value.preset !== "all" ? (
        <span className="text-xs text-muted-foreground tabular">{periodWords(value)}</span>
      ) : null}
      {backwards && <span role="alert" className="text-xs text-destructive">From must be on or before To.</span>}
    </div>
  );
}
