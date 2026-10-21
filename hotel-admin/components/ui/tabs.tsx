"use client";

import * as React from "react";
import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "@/lib/utils";
import { useRadioKeys } from "./data";

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
        "-mb-px inline-flex min-h-10 items-center gap-2 border-b-2 border-transparent px-3 py-2 text-[0.8125rem] font-semibold",
        "text-muted-foreground transition-colors hover:text-foreground",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1",
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
      // Radix makes every panel a tab stop (tabIndex=0), so it needs a visible ring like every other stop (WCAG
      // 2.4.7); removing the outline with nothing in its place made the focus vanish between the tabs and the panel.
      className={cn(
        "rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 focus-visible:ring-offset-2 focus-visible:ring-offset-background animate-fade-in",
        className,
      )}
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
  // The radiogroup keyboard contract (arrows, Home/End, one tab stop), the same one the theme toggle and the
  // filter chips use. role="radio" promises it; without it each segment was its own tab stop.
  const onKeyDown = useRadioKeys(options.map((o) => o.value), value, onChange);
  const known = options.some((o) => o.value === value);
  return (
    // A radiogroup is one tab stop on its CHECKED radio (roving tabIndex, useRadioKeys), never the group
    // itself, which is the ARIA pattern; jsx-a11y reads the container role as a control.
    // eslint-disable-next-line jsx-a11y/interactive-supports-focus
    <div
      role="radiogroup"
      aria-label={label}
      onKeyDown={onKeyDown}
      className={cn(
        "inline-flex items-center gap-0.5 rounded-md border border-border bg-surface p-0.5",
        className,
      )}
    >
      {options.map((o, i) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={active}
            tabIndex={active || (!known && i === 0) ? 0 : -1}
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
