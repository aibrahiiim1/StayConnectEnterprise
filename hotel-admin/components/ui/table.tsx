"use client";

import * as React from "react";
import { cn } from "@/lib/utils";

export function Table({
  className,
  label,
  ...p
}: React.HTMLAttributes<HTMLTableElement> & {
  /** Names the scroll region when overflow makes it keyboard-focusable. */
  label?: string;
}) {
  // Only overflowing tables get a tab stop so keyboard users can scroll hidden columns.
  const scroller = React.useRef<HTMLDivElement | null>(null);
  const [overflowing, setOverflowing] = React.useState(false);
  React.useEffect(() => {
    const el = scroller.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const measure = () => setOverflowing(el.scrollWidth > el.clientWidth + 1);
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    if (el.firstElementChild) ro.observe(el.firstElementChild);
    measure();
    return () => ro.disconnect();
  }, []);

  // Position the scroller so absolute descendants cannot widen the page on phones.
  return (
    <div
      ref={scroller}
      className="relative w-full overflow-x-auto focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
      {...(overflowing
        ? { tabIndex: 0, role: "region", "aria-label": label ?? p["aria-label"] ?? "Scrollable table" }
        : null)}
    >
      <table className={cn("w-full border-collapse text-sm", className)} {...p} />
    </div>
  );
}

export function THead({ className, ...p }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return (
    <thead
      className={cn(
        "bg-surface text-left text-micro uppercase tracking-[0.06em] text-muted-foreground",
        "[&_th]:border-b [&_th]:border-border",
        className,
      )}
      {...p}
    />
  );
}

export function TBody({ className, ...p }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody className={cn("divide-y divide-border", className)} {...p} />;
}

export function TR({ className, ...p }: React.HTMLAttributes<HTMLTableRowElement>) {
  return (
    <tr
      className={cn(
        "border-b border-border last:border-0 transition-colors",
        "[tbody_&]:hover:bg-accent/60",
        className,
      )}
      {...p}
    />
  );
}

export function TH({ className, ...p }: React.ThHTMLAttributes<HTMLTableCellElement>) {
  return <th className={cn("whitespace-nowrap px-4 py-2.5", className)} {...p} />;
}

export function TD({ className, ...p }: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={cn("px-4 py-3 align-middle", className)} {...p} />;
}

export function TableWrap({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("overflow-hidden rounded-b-lg", className)} {...p} />;
}
