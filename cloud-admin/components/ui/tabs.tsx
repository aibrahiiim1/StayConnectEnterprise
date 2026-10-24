"use client";

import * as React from "react";
import * as TabsPrimitive from "@radix-ui/react-tabs";
import { cn } from "@/lib/utils";
import { useRadioKeys } from "./data";

export const Tabs = TabsPrimitive.Root;

const TAB = cn(
  "relative inline-flex h-9 shrink-0 items-center gap-1.5 whitespace-nowrap border-b-2 border-transparent px-1 text-label",
  "text-muted-foreground transition-colors hover:text-foreground",
  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
  "disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-4",
);

export function TabsList({ className, ...p }: React.ComponentPropsWithoutRef<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn("flex items-center gap-5 overflow-x-auto shadow-[inset_0_-1px_0_hsl(var(--border))]", className)}
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
      className={cn(TAB, "data-[state=active]:border-foreground data-[state=active]:text-foreground", className)}
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
      className={cn(
        "rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
        className,
      )}
      {...p}
    />
  );
}

/** Tabs whose selection the caller owns, e.g. to keep the active tab in the URL. Pair with TabPanel. */
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
  function onKeyDown(e: React.KeyboardEvent<HTMLButtonElement>) {
    const i = tabs.findIndex((t) => t.value === value);
    if (e.key === "ArrowRight") { e.preventDefault(); move(i + 1); }
    else if (e.key === "ArrowLeft") { e.preventDefault(); move(i - 1); }
    else if (e.key === "Home") { e.preventDefault(); move(0); }
    else if (e.key === "End") { e.preventDefault(); move(tabs.length - 1); }
  }
  return (
    <div role="tablist" aria-label={label} className={cn("flex gap-5 overflow-x-auto shadow-[inset_0_-1px_0_hsl(var(--border))]", className)}>
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
            onKeyDown={onKeyDown}
            className={cn(TAB, active && "border-foreground text-foreground")}
          >
            {t.label}
            {typeof t.count === "number" && (
              <span className="rounded bg-surface px-1.5 text-2xs tabular text-muted-foreground">{t.count}</span>
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
      className={cn("rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background", className)}
    >
      {children}
    </div>
  );
}

/** A view filter (it narrows one list rather than switching panels), so it is a radio group. */
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
  const onKeyDown = useRadioKeys(options.map((o) => o.value), value, onChange);
  const known = options.some((o) => o.value === value);
  return (
    // eslint-disable-next-line jsx-a11y/interactive-supports-focus
    <div
      role="radiogroup"
      aria-label={label}
      onKeyDown={onKeyDown}
      className={cn(
        "inline-flex max-w-full items-center gap-0.5 overflow-x-auto rounded-md border border-border bg-surface p-0.5",
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
              "inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-[5px] font-medium transition-colors",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              size === "sm" ? "h-6 px-2 text-xs" : "h-7 px-2.5 text-xs pointer-coarse:h-10",
              active
                ? "bg-card text-foreground shadow-control ring-1 ring-border"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {o.label}
            {o.count !== undefined && (
              <span className="tabular text-2xs text-muted-foreground">{o.count}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}
