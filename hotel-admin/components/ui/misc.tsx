"use client";

// The small pieces: a loading placeholder, a meter, a toggle, a divider, a copyable identifier, a
// definition list and an inline metric. Each one exists because three or more screens were building it inline.

import * as React from "react";
import * as SwitchPrimitive from "@radix-ui/react-switch";
import * as SeparatorPrimitive from "@radix-ui/react-separator";
import { Check, Copy } from "lucide-react";
import { cn } from "@/lib/utils";
import { Tooltip } from "./tooltip";

/** Skeleton — a shaped placeholder. "Loading…" as body text makes a page jump when the real content lands. */
export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded-md bg-surface", className)} />;
}

export function SkeletonRows({ rows = 5, cols = 4 }: { rows?: number; cols?: number }) {
  return (
    <div className="divide-y divide-border" aria-busy="true" aria-live="polite">
      <span className="sr-only">Loading</span>
      {Array.from({ length: rows }).map((_, r) => (
        <div key={r} className="flex items-center gap-4 px-4 py-3.5">
          {Array.from({ length: cols }).map((_, c) => (
            <Skeleton key={c} className={cn("h-4", c === 0 ? "w-40" : c === cols - 1 ? "ml-auto w-16" : "w-24")} />
          ))}
        </div>
      ))}
    </div>
  );
}

export function Separator({
  orientation = "horizontal",
  className,
}: {
  orientation?: "horizontal" | "vertical";
  className?: string;
}) {
  return (
    <SeparatorPrimitive.Root
      orientation={orientation}
      className={cn(
        "shrink-0 bg-border",
        orientation === "horizontal" ? "h-px w-full" : "h-full w-px",
        className,
      )}
    />
  );
}

export function Switch({
  checked, onCheckedChange, disabled, id, label, className,
}: {
  checked: boolean;
  onCheckedChange: (v: boolean) => void;
  disabled?: boolean;
  id?: string;
  label?: string;
  className?: string;
}) {
  return (
    <SwitchPrimitive.Root
      id={id}
      checked={checked}
      onCheckedChange={onCheckedChange}
      disabled={disabled}
      aria-label={label}
      className={cn(
        "peer inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent",
        "transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 focus-visible:ring-offset-2 focus-visible:ring-offset-background",
        "disabled:cursor-not-allowed disabled:opacity-50",
        "data-[state=checked]:bg-primary data-[state=unchecked]:bg-border-strong",
        className,
      )}
    >
      <SwitchPrimitive.Thumb
        className={cn(
          "pointer-events-none block size-4 rounded-full bg-card shadow-sm ring-0 transition-transform",
          "data-[state=checked]:translate-x-4 data-[state=unchecked]:translate-x-0",
        )}
      />
    </SwitchPrimitive.Root>
  );
}

/**
 * Meter — a labelled proportion bar. Used for data allowance, licensed capacity, DHCP pool utilisation.
 *
 * The tone escalates automatically: a pool at 94% should not need the caller to remember to pass "warning".
 */
export function Meter({
  value,
  max,
  label,
  caption,
  tone,
  className,
}: {
  value: number;
  max: number;
  label?: React.ReactNode;
  caption?: React.ReactNode;
  tone?: "ok" | "warn" | "err" | "info" | "neutral";
  className?: string;
}) {
  const safeMax = max > 0 ? max : 0;
  const pct = safeMax > 0 ? Math.min(100, Math.max(0, (value / safeMax) * 100)) : 0;
  const auto: NonNullable<typeof tone> = pct >= 90 ? "err" : pct >= 75 ? "warn" : "ok";
  const resolved = tone ?? auto;
  const fill: Record<string, string> = {
    ok: "bg-success",
    warn: "bg-warning",
    err: "bg-destructive",
    info: "bg-info",
    neutral: "bg-muted-foreground/60",
  };
  return (
    <div className={cn("min-w-0 space-y-1", className)}>
      {(label || caption) && (
        <div className="flex items-baseline justify-between gap-2 text-xs">
          {label && <span className="truncate text-muted-foreground">{label}</span>}
          {caption && <span className="shrink-0 tabular text-muted-foreground">{caption}</span>}
        </div>
      )}
      <div
        role="meter"
        aria-valuenow={value}
        aria-valuemin={0}
        aria-valuemax={safeMax || undefined}
        className="h-1.5 w-full overflow-hidden rounded-full bg-surface"
      >
        <div
          className={cn("h-full rounded-full transition-[width] duration-300", fill[resolved])}
          style={{ width: `${pct}%` }}
        />
      </div>
    </div>
  );
}

/**
 * MonoId — a long identifier, shortened, with copy-to-clipboard.
 *
 * Several screens print a raw UUID in a table cell. It is unreadable, it is the widest column on the page, and
 * it is almost always there because somebody might need to paste it into a support ticket — which is the one
 * thing a truncated, copyable chip does better than the full string.
 */
export function MonoId({
  value,
  head = 8,
  className,
  title,
}: {
  value?: string | null;
  head?: number;
  className?: string;
  title?: string;
}) {
  const [copied, setCopied] = React.useState(false);
  if (!value) return <span className="text-muted-foreground">—</span>;
  const short = value.length > head + 4 ? `${value.slice(0, head)}…` : value;

  async function copy() {
    try {
      await navigator.clipboard.writeText(value!);
      setCopied(true);
      setTimeout(() => setCopied(false), 1400);
    } catch {
      /* clipboard denied — the tooltip still shows the full value */
    }
  }

  return (
    <Tooltip content={<span className="break-all font-mono">{title ? `${title}: ${value}` : value}</span>}>
      <button
        type="button"
        onClick={copy}
        className={cn(
          "group inline-flex items-center gap-1 rounded border border-border bg-surface px-1.5 py-0.5",
          "font-mono text-2xs text-muted-foreground transition-colors hover:text-foreground",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
          className,
        )}
      >
        {short}
        {copied ? (
          <Check className="size-3 text-success" />
        ) : (
          <Copy className="size-3 opacity-0 transition-opacity group-hover:opacity-60" />
        )}
      </button>
    </Tooltip>
  );
}

/** DList — a definition list with a consistent label column. The admin has dozens of these, all different. */
export function DList({
  items,
  columns = 2,
  className,
}: {
  items: { label: React.ReactNode; value: React.ReactNode; span?: boolean }[];
  columns?: 1 | 2 | 3;
  className?: string;
}) {
  const grid =
    columns === 1 ? "sm:grid-cols-1" : columns === 3 ? "sm:grid-cols-3" : "sm:grid-cols-2";
  return (
    <dl className={cn("grid grid-cols-1 gap-x-8 gap-y-3", grid, className)}>
      {items.map((it, i) => (
        <div key={i} className={cn("min-w-0", it.span && "sm:col-span-full")}>
          <dt className="text-xs font-medium text-muted-foreground">{it.label}</dt>
          <dd className="mt-0.5 break-words text-sm text-foreground">{it.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** Metric — a label over a figure, for use inside a card that already has a title. */
export function Metric({
  label, value, sub, tone, className,
}: {
  label: React.ReactNode;
  value: React.ReactNode;
  sub?: React.ReactNode;
  tone?: "default" | "ok" | "warn" | "err";
  className?: string;
}) {
  const colors: Record<string, string> = {
    default: "text-foreground",
    ok: "text-success",
    warn: "text-warning",
    err: "text-destructive",
  };
  return (
    <div className={cn("min-w-0", className)}>
      <div className="truncate text-xs font-medium text-muted-foreground">{label}</div>
      <div className={cn("mt-0.5 text-lg font-semibold tabular", colors[tone ?? "default"])}>{value}</div>
      {sub && <div className="mt-0.5 truncate text-xs text-muted-foreground">{sub}</div>}
    </div>
  );
}
