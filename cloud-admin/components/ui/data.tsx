"use client";

// DATA CONTROLS — the pieces every list screen was building for itself.
//
// Search boxes of three different heights, state filters as native <select>s on one screen and as buttons on the
// next, "Load more" on vouchers and nothing on guest activity. These are the one version of each.

import * as React from "react";
import { ChevronLeft, ChevronRight, Search, X } from "lucide-react";
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

/** KeyValueGrid — a compact two-column fact sheet for a record. Values may be any node. */
export function KeyValueGrid({
  items,
  columns = 2,
  className,
}: {
  items: { label: React.ReactNode; value: React.ReactNode; hint?: React.ReactNode; wide?: boolean }[];
  columns?: 1 | 2 | 3;
  className?: string;
}) {
  const cols = { 1: "sm:grid-cols-1", 2: "sm:grid-cols-2", 3: "sm:grid-cols-3" }[columns];
  return (
    <dl className={cn("grid grid-cols-1 gap-x-6 gap-y-3.5", cols, className)}>
      {items.map((it, i) => (
        <div key={i} className={cn("min-w-0 space-y-0.5", it.wide && "sm:col-span-full")}>
          <dt className="text-xs text-muted-foreground">{it.label}</dt>
          <dd className="min-w-0 break-words text-sm">{it.value ?? <span className="text-muted-foreground">—</span>}</dd>
          {it.hint && <dd className="text-xs text-muted-foreground">{it.hint}</dd>}
        </div>
      ))}
    </dl>
  );
}

/**
 * MetricStrip — a row of small figures inside a card header or hero ("12 in house · 3 arriving · 2 departing").
 * Lighter than a row of StatCards when the figures belong to one object.
 */
export function MetricStrip({
  items,
  className,
}: {
  items: { label: React.ReactNode; value: React.ReactNode; tone?: "ok" | "warn" | "err" | "info" }[];
  className?: string;
}) {
  const tone: Record<string, string> = {
    ok: "text-success",
    warn: "text-warning-subtle-foreground",
    err: "text-destructive",
    info: "text-info",
  };
  return (
    <dl className={cn("grid grid-cols-2 gap-px overflow-hidden rounded-lg border border-border bg-border sm:grid-cols-4", className)}>
      {items.map((it, i) => (
        <div key={i} className="min-w-0 bg-card px-4 py-3">
          <dt className="truncate text-xs text-muted-foreground">{it.label}</dt>
          <dd className={cn("mt-1 truncate text-lg font-semibold tabular", it.tone && tone[it.tone])}>{it.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** Timeline — a vertical list of dated events (publications, lifecycle changes, audit entries). */
export function Timeline({
  items,
  className,
  emptyLabel = "No history yet",
}: {
  items: {
    key?: string;
    title: React.ReactNode;
    when?: React.ReactNode;
    body?: React.ReactNode;
    tone?: "ok" | "warn" | "err" | "info" | "neutral";
  }[];
  className?: string;
  emptyLabel?: string;
}) {
  const dot: Record<string, string> = {
    ok: "bg-success",
    warn: "bg-warning",
    err: "bg-destructive",
    info: "bg-info",
    neutral: "bg-muted-foreground/60",
  };
  if (items.length === 0) return <p className="text-sm text-muted-foreground">{emptyLabel}</p>;
  return (
    <ol className={cn("relative space-y-4 border-s border-border ps-5", className)}>
      {items.map((it, i) => (
        <li key={it.key ?? i} className="relative">
          <span
            className={cn(
              "absolute -start-[25px] top-1.5 size-2.5 rounded-full ring-4 ring-card",
              dot[it.tone ?? "neutral"],
            )}
            aria-hidden
          />
          <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
            <div className="text-sm font-medium">{it.title}</div>
            {it.when && <div className="text-xs tabular text-muted-foreground">{it.when}</div>}
          </div>
          {it.body && <div className="mt-1 text-sm text-muted-foreground">{it.body}</div>}
        </li>
      ))}
    </ol>
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
        return (
          <li key={s} className="flex items-center gap-2">
            <button
              type="button"
              disabled={!clickable}
              onClick={() => clickable && onStep?.(i)}
              aria-current={active ? "step" : undefined}
              className={cn(
                "inline-flex items-center gap-2 rounded-full text-xs font-medium",
                clickable ? "cursor-pointer hover:text-foreground" : "cursor-default",
                active ? "text-foreground" : done ? "text-muted-foreground" : "text-muted-foreground/70",
              )}
            >
              <span
                className={cn(
                  "inline-flex size-6 items-center justify-center rounded-full border text-2xs tabular",
                  active && "border-primary bg-primary text-primary-foreground",
                  done && "border-primary/40 bg-primary-subtle text-primary-subtle-foreground",
                  !active && !done && "border-border bg-card",
                )}
              >
                {done ? "✓" : i + 1}
              </span>
              {s}
            </button>
            {i < steps.length - 1 && <span className="h-px w-6 bg-border" aria-hidden />}
          </li>
        );
      })}
    </ol>
  );
}

/**
 * OptionCard — a large selectable card for a choice that deserves a description (a PMS provider, a portal
 * template, a voucher format). Radio semantics, so the group is one tab stop with arrow-key navigation left to
 * the browser's native radio behaviour through the hidden input.
 */
export function OptionCard({
  name,
  value,
  checked,
  onChange,
  title,
  description,
  badge,
  icon,
  disabled,
  children,
  className,
}: {
  name: string;
  value: string;
  checked: boolean;
  onChange: (v: string) => void;
  title: React.ReactNode;
  description?: React.ReactNode;
  badge?: React.ReactNode;
  icon?: React.ReactNode;
  disabled?: boolean;
  children?: React.ReactNode;
  className?: string;
}) {
  return (
    <label
      className={cn(
        "relative flex cursor-pointer flex-col gap-2 rounded-lg border bg-card p-4 transition-colors",
        "has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring/50",
        checked ? "border-primary bg-primary-subtle/40" : "border-border hover:border-border-strong",
        disabled && "cursor-not-allowed opacity-60",
        className,
      )}
    >
      <input
        type="radio"
        name={name}
        value={value}
        checked={checked}
        disabled={disabled}
        onChange={() => onChange(value)}
        className="sr-only"
      />
      <div className="flex items-start gap-3">
        {icon && (
          <span
            className={cn(
              "inline-flex size-9 shrink-0 items-center justify-center rounded-md [&_svg]:size-4",
              checked ? "bg-primary text-primary-foreground" : "bg-surface text-muted-foreground",
            )}
            aria-hidden
          >
            {icon}
          </span>
        )}
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-semibold">{title}</span>
            {badge}
          </div>
          {description && <p className="text-xs leading-relaxed text-muted-foreground">{description}</p>}
        </div>
        <span
          className={cn(
            "mt-0.5 inline-flex size-4 shrink-0 items-center justify-center rounded-full border",
            checked ? "border-primary" : "border-border-strong",
          )}
          aria-hidden
        >
          {checked && <span className="size-2 rounded-full bg-primary" />}
        </span>
      </div>
      {children}
    </label>
  );
}
