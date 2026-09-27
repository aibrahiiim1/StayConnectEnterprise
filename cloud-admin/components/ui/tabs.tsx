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

/**
 * TabList — the sections of one record (a customer's Sites, Appliances, Licenses…). WAI-ARIA tabs: one tab stop,
 * arrow keys / Home / End move between tabs, and each tab controls a `TabPanel` with the matching id. The
 * selected tab is owned by the caller, so it can live in the address (`?tab=sites`) and survive a reload.
 */
export function TabList<T extends string>({
  value,
  onChange,
  tabs,
  label,
  idBase,
  className,
}: {
  value: T;
  onChange: (v: T) => void;
  tabs: { value: T; label: React.ReactNode; count?: number }[];
  label: string;
  idBase: string;
  className?: string;
}) {
  const refs = React.useRef<Record<string, HTMLButtonElement | null>>({});
  function move(to: number) {
    const t = tabs[(to + tabs.length) % tabs.length];
    onChange(t.value);
    refs.current[t.value]?.focus();
  }
  return (
    <div
      role="tablist"
      aria-label={label}
      className={cn("flex gap-1 overflow-x-auto border-b border-border", className)}
      onKeyDown={(e) => {
        const i = tabs.findIndex((t) => t.value === value);
        if (e.key === "ArrowRight") { e.preventDefault(); move(i + 1); }
        else if (e.key === "ArrowLeft") { e.preventDefault(); move(i - 1); }
        else if (e.key === "Home") { e.preventDefault(); move(0); }
        else if (e.key === "End") { e.preventDefault(); move(tabs.length - 1); }
      }}
    >
      {tabs.map((t) => {
        const active = t.value === value;
        return (
          <button
            key={t.value}
            ref={(el) => { refs.current[t.value] = el; }}
            type="button"
            role="tab"
            id={`${idBase}-tab-${t.value}`}
            aria-selected={active}
            aria-controls={`${idBase}-panel-${t.value}`}
            tabIndex={active ? 0 : -1}
            onClick={() => onChange(t.value)}
            className={cn(
              "relative -mb-px inline-flex h-10 shrink-0 items-center gap-1.5 border-b-2 px-3 text-sm font-medium transition-colors",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
              active
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:border-border-strong hover:text-foreground",
            )}
          >
            {t.label}
            {typeof t.count === "number" && (
              <span className="rounded-full bg-surface px-1.5 text-2xs tabular text-muted-foreground">{t.count}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}

export function TabPanel({
  idBase,
  value,
  className,
  children,
}: {
  idBase: string;
  value: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div
      role="tabpanel"
      id={`${idBase}-panel-${value}`}
      aria-labelledby={`${idBase}-tab-${value}`}
      tabIndex={0}
      className={cn("focus-visible:outline-none", className)}
    >
      {children}
    </div>
  );
}
