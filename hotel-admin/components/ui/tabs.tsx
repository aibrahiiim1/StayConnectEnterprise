"use client";

import * as React from "react";
import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "@/lib/utils";

// Radix Tabs, so arrow-key navigation and the tab/panel ARIA relationship come for free. The admin's existing
// tab strips are plain buttons plus a `useState`, which look like tabs and behave like nothing.
export const Tabs = TabsPrimitive.Root;

export function TabsList({ className, ...p }: React.ComponentPropsWithoutRef<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn("flex items-center gap-1 border-b border-border", className)}
      {...p}
    />
  );
}

export function TabsTrigger({
  className,
  ...p
}: React.ComponentPropsWithoutRef<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        "-mb-px inline-flex items-center gap-2 border-b-2 border-transparent px-3 py-2 text-sm font-medium",
        "text-muted-foreground transition-colors hover:text-foreground",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-offset-1",
        "data-[state=active]:border-primary data-[state=active]:text-foreground",
        "disabled:pointer-events-none disabled:opacity-50",
        className,
      )}
      {...p}
    />
  );
}

export function TabsContent({
  className,
  ...p
}: React.ComponentPropsWithoutRef<typeof TabsPrimitive.Content>) {
  return (
    <TabsPrimitive.Content
      className={cn("focus-visible:outline-none animate-fade-in", className)}
      {...p}
    />
  );
}

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
              "inline-flex items-center gap-1.5 rounded-[5px] font-medium transition-colors",
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
