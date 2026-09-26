"use client";

import * as React from "react";
import { cn } from "@/lib/utils";

/**
 * Segmented — a small two-or-three-way switch for a view mode ("Active / Recent", "Cards / Table").
 *
 * This is NOT Tabs and is deliberately a different control: it swaps a filter on the same content rather than
 * swapping panels, so it renders as a compact group of radios instead of a full tab strip.
 */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  size = "md",
  className,
  label,
}: {
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: React.ReactNode; count?: number }[];
  size?: "sm" | "md";
  className?: string;
  label?: string;
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn(
        "inline-flex items-center gap-0.5 rounded-md border border-border bg-surface p-0.5",
        className,
      )}
    >
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
              "inline-flex items-center gap-1.5 rounded-sm font-medium transition-colors",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
              size === "sm" ? "h-6 px-2 text-xs" : "h-7 px-2.5 text-sm",
              active
                ? "bg-card text-foreground shadow-xs"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {o.label}
            {o.count !== undefined && (
              <span
                className={cn(
                  "rounded px-1 text-2xs tabular",
                  active ? "bg-surface text-muted-foreground" : "bg-card/60 text-muted-foreground",
                )}
              >
                {o.count}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}
