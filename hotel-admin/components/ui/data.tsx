"use client";

import * as React from "react";
import { ChevronLeft, ChevronRight, Search, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./button";

/**
 * Attributes that keep a search box out of the browser's and password managers' autofill: a field whose label or
 * placeholder starts with "Username" is otherwise offered saved credentials. Every search box in both consoles uses
 * these (SearchInput applies them; spread SEARCH_FIELD_PROPS onto a bespoke <input type="search">).
 */
export const SEARCH_FIELD_PROPS = {
  type: "search",
  autoComplete: "off",
  autoCorrect: "off",
  autoCapitalize: "off",
  spellCheck: false,
  enterKeyHint: "search",
  "data-1p-ignore": "true",
  "data-lpignore": "true",
  "data-bwignore": "true",
  "data-form-type": "other",
} as const;

export const SearchInput = React.forwardRef<
  HTMLInputElement,
  Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type" | "size"> & {
    value: string;
    onChange: (v: string) => void;
    label?: string;
    /** Debounce in ms before onChange fires; 0 fires on every keystroke. */
    delay?: number;
    /** Called on Enter, with the current text. */
    onSearch?: (v: string) => void;
    size?: "sm" | "md";
  }
>(function SearchInput(
  { value, onChange, placeholder = "Search", label, delay = 0, onSearch, size = "md", className, id, name, ...rest },
  ref,
) {
  const [local, setLocal] = React.useState(value);
  const generated = React.useId();
  React.useEffect(() => setLocal(value), [value]);
  React.useEffect(() => {
    if (delay <= 0 || local === value) return;
    const t = window.setTimeout(() => onChange(local), delay);
    return () => window.clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [local, delay]);

  return (
    <div className={cn("relative w-full sm:w-72", className)}>
      <Search
        className="pointer-events-none absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
        aria-hidden
      />
      <input
        ref={ref}
        {...rest}
        {...SEARCH_FIELD_PROPS}
        id={id ?? `search-${generated.replace(/:/g, "")}`}
        name={name ?? "onegate-filter"}
        aria-label={label ?? placeholder}
        value={local}
        placeholder={placeholder}
        onChange={(e) => {
          setLocal(e.target.value);
          if (delay <= 0) onChange(e.target.value);
        }}
        onKeyDown={(e) => {
          rest.onKeyDown?.(e);
          if (e.key === "Enter" && onSearch) {
            e.preventDefault();
            if (local !== value) onChange(local);
            onSearch(local);
          } else if (e.key === "Escape" && local) {
            setLocal("");
            onChange("");
          }
        }}
        className={cn(
          "w-full rounded-md border border-input/70 bg-card ps-8 pe-8 text-sm text-foreground shadow-control",
          "placeholder:text-muted-foreground/80 hover:border-input",
          "focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/15",
          "[&::-webkit-search-cancel-button]:appearance-none [&::-webkit-search-decoration]:appearance-none",
          size === "sm" ? "h-8" : "h-9 pointer-coarse:h-11",
        )}
      />
      {local && (
        <button
          type="button"
          onClick={() => {
            setLocal("");
            onChange("");
          }}
          className="absolute end-1.5 top-1/2 inline-flex size-6 -translate-y-1/2 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <X className="size-3.5" aria-hidden />
          <span className="sr-only">Clear search</span>
        </button>
      )}
    </div>
  );
});

/** Keep the result-count live region mounted before search answers arrive so screen readers announce changes. */
export function ResultsCount({
  count,
  noun = "result",
  plural,
  more = false,
}: {
  count: number | null | undefined;
  noun?: string;
  plural?: string;
  more?: boolean;
}) {
  const text =
    count == null
      ? ""
      : count === 0
        ? `No ${plural ?? noun + "s"} found`
        : `${count.toLocaleString()}${more ? " or more" : ""} ${count === 1 && !more ? noun : plural ?? noun + "s"} found`;
  return (
    <p role="status" aria-live="polite" className="sr-only">
      {text}
    </p>
  );
}

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
  const onKeyDown = useRadioKeys(options.map((o) => o.value), value, onChange);
  return (
    // eslint-disable-next-line jsx-a11y/interactive-supports-focus
    <div role="radiogroup" aria-label={label} onKeyDown={onKeyDown}
      className={cn("flex flex-wrap items-center gap-1.5", className)}>
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={active}
            // The checked chip is the group's tab stop; arrows move within it.
            tabIndex={active || (!options.some((x) => x.value === value) && o === options[0]) ? 0 : -1}
            onClick={() => onChange(o.value)}
            className={cn(
              "inline-flex h-8 items-center gap-1.5 whitespace-nowrap rounded-md border px-2.5 text-xs font-medium transition-colors pointer-coarse:min-h-11",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              active
                ? "border-foreground/15 bg-foreground/[0.06] text-foreground"
                : "border-border bg-card text-muted-foreground hover:border-border-strong hover:text-foreground",
            )}
          >
            {o.tone && <span className={cn("size-1.5 rounded-full", dot[o.tone])} aria-hidden />}
            {o.label}
            {typeof o.count === "number" && (
              <span
                className={cn(
                  "rounded px-1 tabular text-2xs",
                  active ? "bg-card text-foreground" : "bg-surface text-muted-foreground",
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

/** Keep radio keyboard order aligned with the rendered values. */
export function useRadioKeys<T extends string>(values: readonly T[], value: T, onChange: (v: T) => void) {
  return React.useCallback(
    (e: React.KeyboardEvent<HTMLElement>) => {
      if (values.length === 0) return;
      const at = Math.max(0, values.indexOf(value));
      let next: number;
      switch (e.key) {
        case "ArrowRight":
        case "ArrowDown":
          next = (at + 1) % values.length;
          break;
        case "ArrowLeft":
        case "ArrowUp":
          next = (at - 1 + values.length) % values.length;
          break;
        case "Home":
          next = 0;
          break;
        case "End":
          next = values.length - 1;
          break;
        default:
          return;
      }
      e.preventDefault();
      onChange(values[next]);
      const radios = e.currentTarget.querySelectorAll<HTMLElement>('[role="radio"]');
      radios[next]?.focus();
    },
    [values, value, onChange],
  );
}

export function Pagination({
  offset,
  limit,
  shown,
  total,
  hasMore,
  onChange,
  className,
}: {
  offset: number;
  limit: number;
  shown: number;
  total?: number | null;
  /** Enables Next when the total is unknown. */
  hasMore?: boolean;
  onChange: (offset: number) => void;
  className?: string;
}) {
  const from = shown === 0 ? 0 : offset + 1;
  const to = offset + shown;
  const hasNext = total != null ? to < total : hasMore ?? shown >= limit;
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
 * Label/value pairs. `layout="inline"` puts the label on the left of each row (details drawers, summaries);
 * `stacked` puts it above (wide cards with several columns).
 */
export function DescriptionList({
  items,
  layout = "inline",
  columns = 1,
  className,
}: {
  items: { label: React.ReactNode; value: React.ReactNode; hint?: React.ReactNode; wide?: boolean; hidden?: boolean }[];
  layout?: "inline" | "stacked";
  columns?: 1 | 2 | 3;
  className?: string;
}) {
  const shown = items.filter((it) => !it.hidden);
  if (layout === "stacked") return <KeyValueGrid items={shown} columns={columns} className={className} />;
  const cols = { 1: "", 2: "md:grid-cols-2 md:gap-x-8", 3: "md:grid-cols-3 md:gap-x-8" }[columns];
  return (
    <dl className={cn("grid grid-cols-1 text-sm", cols, className)}>
      {shown.map((it, i) => (
        <div
          key={i}
          className={cn(
            "grid min-w-0 grid-cols-[minmax(7rem,38%)_1fr] gap-3 border-b border-border py-2 last:border-0",
            it.wide && "md:col-span-full",
          )}
        >
          <dt className="min-w-0 text-muted-foreground">{it.label}</dt>
          <dd className="min-w-0 break-words text-foreground">
            {it.value ?? <span className="text-muted-foreground">—</span>}
            {it.hint && <div className="mt-0.5 text-xs text-muted-foreground">{it.hint}</div>}
          </dd>
        </div>
      ))}
    </dl>
  );
}

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
    <ol className={cn("relative space-y-4 border-l border-border pl-5", className)}>
      {items.map((it, i) => (
        <li key={it.key ?? i} className="relative">
          <span
            className={cn(
              "absolute -left-[25px] top-1.5 size-2.5 rounded-full ring-4 ring-card",
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

/** Steps before current are complete; forward navigation waits for validation. */
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

/** Native radio inputs provide one tab stop and arrow navigation for the card group. */
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
