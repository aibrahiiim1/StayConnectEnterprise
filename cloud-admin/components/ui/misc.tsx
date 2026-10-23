"use client";

import * as React from "react";
import * as SwitchPrimitive from "@radix-ui/react-switch";
import { Check, Copy } from "lucide-react";
import { cn } from "@/lib/utils";
import { Tooltip } from "./tooltip";

/** Reduced motion keeps the loading shape static. */
export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded-md bg-surface motion-reduce:animate-none", className)} />;
}

export function SkeletonRows({ rows = 5, cols = 4 }: { rows?: number; cols?: number }) {
  return (
    <div className="divide-y divide-border" aria-busy="true" aria-live="polite">
      <span className="sr-only">Loading</span>
      {Array.from({ length: rows }).map((_, r) => (
        <div key={r} className="flex items-center gap-4 px-4 py-3.5">
          {Array.from({ length: cols }).map((_, c) => (
            <Skeleton key={c} className={cn("h-4", c === 0 ? "w-40" : c === cols - 1 ? "ms-auto w-16" : "w-24")} />
          ))}
        </div>
      ))}
    </div>
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
        "peer inline-flex h-6 w-11 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent",
        "transition-colors duration-base ease-onegate focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
        "disabled:cursor-not-allowed disabled:opacity-50",
        "data-[state=checked]:bg-primary data-[state=unchecked]:bg-input",
        className,
      )}
    >
      <SwitchPrimitive.Thumb
        className={cn(
          "pointer-events-none block size-5 rounded-full bg-white shadow-control ring-0 transition-transform duration-base ease-onegate",
          "data-[state=checked]:translate-x-5 data-[state=unchecked]:translate-x-0",
        )}
      />
    </SwitchPrimitive.Root>
  );
}

/** Tone escalates with usage unless the caller supplies one. */
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

/** Truncate long IDs visually while copying their full value. */
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
      /* The tooltip retains the full value if clipboard access is denied. */
    }
  }

  return (
    <>
      <Tooltip content={<span className="break-all font-mono">{title ? `${title}: ${value}` : value}</span>}>
        <button
          type="button"
          onClick={copy}
          aria-label={`Copy ${title ? title.toLowerCase() : "value"} ${short}`}
          className={cn(
            "group inline-flex items-center gap-1 rounded border border-border bg-surface px-1.5 py-0.5",
            "font-mono text-2xs text-muted-foreground transition-colors hover:text-foreground",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
            className,
          )}
        >
          {short}
          {copied ? (
            <Check className="size-3 text-success" aria-hidden />
          ) : (
            <Copy className="size-3 opacity-0 transition-opacity group-hover:opacity-60" aria-hidden />
          )}
        </button>
      </Tooltip>
      {/* Keep the live region mounted so copying is announced when its content changes. */}
      <span role="status" aria-live="polite" className="sr-only">{copied ? "Copied" : ""}</span>
    </>
  );
}
