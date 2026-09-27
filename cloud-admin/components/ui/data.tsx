"use client";

// DATA CONTROLS — the pieces every list screen was building for itself.
//
// Search boxes of three different heights, state filters as native <select>s on one screen and as buttons on the
// next, "Load more" on vouchers and nothing on guest activity. These are the one version of each.

import * as React from "react";
import { Check, ChevronLeft, ChevronRight, Search, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./button";

/** A search field with a leading icon and a clear button. Debounces onChange by `delay` ms when given. */
export function SearchInput({
  value,
  onChange,
  placeholder = "Search",
  label,
  delay = 0,
  className,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  /** Accessible name. Defaults to the placeholder. */
  label?: string;
  delay?: number;
  className?: string;
}) {
  const [local, setLocal] = React.useState(value);
  React.useEffect(() => setLocal(value), [value]);
  React.useEffect(() => {
    if (delay <= 0 || local === value) return;
    const t = window.setTimeout(() => onChange(local), delay);
    return () => window.clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [local, delay]);

  return (
    <div className={cn("relative w-full sm:w-72", className)}>
      <Search className="pointer-events-none absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
      <input
        type="search"
        aria-label={label ?? placeholder}
        value={local}
        placeholder={placeholder}
        onChange={(e) => {
          setLocal(e.target.value);
          if (delay <= 0) onChange(e.target.value);
        }}
        className={cn(
          "h-9 w-full rounded-md border border-input bg-background ps-8 pe-8 text-sm shadow-xs",
          "placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          "[&::-webkit-search-cancel-button]:appearance-none",
        )}
      />
      {local && (
        <button
          type="button"
          onClick={() => {
            setLocal("");
            onChange("");
          }}
          className="absolute end-2 top-1/2 -translate-y-1/2 rounded p-0.5 text-muted-foreground hover:bg-surface hover:text-foreground"
        >
          <X className="size-3.5" />
          <span className="sr-only">Clear search</span>
        </button>
      )}
    </div>
  );
}

/**
 * FilterChips — a single-choice filter with counts ("All 214 · Unused 180 · Used 30 · Cancelled 4").
 *
 * A count on each option answers "is there anything there?" before the click, which a <select> cannot.
 */
export function FilterChips<T extends string>({
  value,
  onChange,
  options,
  label,
  className,
}: {
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: React.ReactNode; count?: number; tone?: "ok" | "warn" | "err" | "info" }[];
  label: string;
  className?: string;
}) {
  const dot: Record<string, string> = {
    ok: "bg-success",
    warn: "bg-warning",
    err: "bg-destructive",
    info: "bg-info",
  };
  return (
    <div role="radiogroup" aria-label={label} className={cn("flex flex-wrap items-center gap-1.5", className)}>
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(o.value)}
            className={cn(
              "inline-flex h-8 items-center gap-1.5 rounded-full border px-3 text-xs font-medium transition-colors",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
              active
                ? "border-primary/40 bg-primary-subtle text-primary-subtle-foreground"
                : "border-border bg-card text-muted-foreground hover:border-border-strong hover:text-foreground",
            )}
          >
            {o.tone && <span className={cn("size-1.5 rounded-full", dot[o.tone])} aria-hidden />}
            {o.label}
            {typeof o.count === "number" && (
              <span
                className={cn(
                  "rounded-full px-1.5 tabular text-2xs",
                  active ? "bg-primary/15" : "bg-surface text-muted-foreground",
                )}
              >
                {o.count.toLocaleString()}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}

/** Pagination — "Showing 201–400 of 1,204" with previous/next. `total` may be unknown (null). */
export function Pagination({
  offset,
  limit,
  shown,
  total,
  onChange,
  className,
}: {
  offset: number;
  limit: number;
  /** How many rows the current page actually holds. */
  shown: number;
  total?: number | null;
  onChange: (offset: number) => void;
  className?: string;
}) {
  const from = shown === 0 ? 0 : offset + 1;
  const to = offset + shown;
  const hasNext = total != null ? to < total : shown >= limit;
  return (
    <div className={cn("flex flex-wrap items-center justify-between gap-3 text-xs text-muted-foreground", className)}>
      <span className="tabular">
        {shown === 0
          ? "No rows"
          : total != null
            ? `Showing ${from.toLocaleString()}–${to.toLocaleString()} of ${total.toLocaleString()}`
            : `Showing ${from.toLocaleString()}–${to.toLocaleString()}`}
      </span>
      <div className="flex items-center gap-1.5">
        <Button
          size="xs"
          variant="secondary"
          disabled={offset <= 0}
          onClick={() => onChange(Math.max(0, offset - limit))}
        >
          <ChevronLeft /> Previous
        </Button>
        <Button size="xs" variant="secondary" disabled={!hasNext} onClick={() => onChange(offset + limit)}>
          Next <ChevronRight />
        </Button>
      </div>
    </div>
  );
}

/**
 * Stepper — the progress header of a multi-step flow (add a PMS connection, issue vouchers).
 * Steps before `current` are complete; the operator can click back to one, never forward past validation.
 */
export function Stepper({
  steps,
  current,
  onStep,
  className,
}: {
  steps: string[];
  current: number;
  onStep?: (i: number) => void;
  className?: string;
}) {
  return (
    <ol className={cn("flex flex-wrap items-center gap-x-2 gap-y-2", className)}>
      {steps.map((s, i) => {
        const done = i < current;
        const active = i === current;
        const clickable = !!onStep && i < current;
        // A step is a BUTTON only when it does something. The rest are text: a disabled button announces a
        // control the operator can never use, and dims its label below legibility.
        const Tag = clickable ? "button" : "span";
        return (
          <li key={s} className="flex items-center gap-2" aria-current={active ? "step" : undefined}>
            <Tag
              {...(clickable ? { type: "button" as const, onClick: () => onStep?.(i) } : null)}
              className={cn(
                "inline-flex items-center gap-2 rounded-full text-xs font-medium",
                clickable && "cursor-pointer hover:text-foreground",
                active ? "text-foreground" : "text-muted-foreground",
              )}
            >
              <span
                className={cn(
                  "inline-flex size-6 items-center justify-center rounded-full border text-2xs tabular",
                  active && "border-primary bg-primary text-primary-foreground",
                  done && "border-primary/40 bg-primary-subtle text-primary-subtle-foreground",
                  !active && !done && "border-border bg-card",
                )}
                aria-hidden={done || undefined}
              >
                {done ? <Check className="size-3.5" /> : i + 1}
              </span>
              {done && <span className="sr-only">Completed: </span>}
              {s}
            </Tag>
            {i < steps.length - 1 && <span className="h-px w-6 bg-border" aria-hidden />}
          </li>
        );
      })}
    </ol>
  );
}
