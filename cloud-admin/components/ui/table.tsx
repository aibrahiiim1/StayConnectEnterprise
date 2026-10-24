"use client";

import * as React from "react";
import { cn } from "@/lib/utils";

type Density = "compact" | "default";
const DensityContext = React.createContext<Density>("default");

export function Table({
  className,
  label,
  stickyHeader = false,
  density = "default",
  maxHeight,
  ...p
}: React.HTMLAttributes<HTMLTableElement> & {
  /** Names the scroll region when overflow makes it keyboard-focusable. */
  label?: string;
  /** Keeps the header row visible while the table scrolls inside `maxHeight` (or the page). */
  stickyHeader?: boolean;
  density?: Density;
  /** Bounds the table's own vertical scroll, e.g. "28rem". */
  maxHeight?: string;
}) {
  // Only an overflowing table gets a tab stop, so keyboard users can scroll the hidden columns.
  const scroller = React.useRef<HTMLDivElement | null>(null);
  const [overflowing, setOverflowing] = React.useState(false);
  React.useEffect(() => {
    const el = scroller.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const measure = () =>
      setOverflowing(el.scrollWidth > el.clientWidth + 1 || (!!maxHeight && el.scrollHeight > el.clientHeight + 1));
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    if (el.firstElementChild) ro.observe(el.firstElementChild);
    measure();
    return () => ro.disconnect();
  }, [maxHeight]);

  // `relative` makes the scroller the containing block, so an absolute descendant cannot widen the page on phones.
  return (
    <DensityContext.Provider value={density}>
      <div
        ref={scroller}
        data-sticky={stickyHeader ? "true" : undefined}
        style={maxHeight ? { maxHeight } : undefined}
        className={cn(
          "relative w-full overflow-x-auto focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
          maxHeight && "overflow-y-auto",
        )}
        {...(overflowing
          ? { tabIndex: 0, role: "region", "aria-label": label ?? p["aria-label"] ?? "Scrollable table" }
          : null)}
      >
        <table
          className={cn(
            "w-full border-separate border-spacing-0 text-sm",
            stickyHeader && "[&_thead_th]:sticky [&_thead_th]:top-0 [&_thead_th]:z-10",
            className,
          )}
          {...p}
        />
      </div>
    </DensityContext.Provider>
  );
}

export function THead({ className, ...p }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return (
    <thead
      className={cn(
        "text-start text-2xs font-medium text-muted-foreground",
        "[&_th]:border-b [&_th]:border-border [&_th]:bg-surface",
        className,
      )}
      {...p}
    />
  );
}

export function TBody({ className, ...p }: React.HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody className={cn("[&>tr:last-child>td]:border-b-0", className)} {...p} />;
}

/**
 * A row. Pass `onClick` (or `href` via onClick+router) to make the whole row open its details: it then becomes
 * keyboard-reachable and answers Enter. Controls inside the row should stop propagation (RowActions does).
 */
export function TR({
  className,
  onClick,
  onKeyDown,
  selected,
  ...p
}: React.HTMLAttributes<HTMLTableRowElement> & { selected?: boolean }) {
  const clickable = typeof onClick === "function";
  return (
    <tr
      {...p}
      onClick={onClick}
      tabIndex={clickable ? 0 : p.tabIndex}
      onKeyDown={(e) => {
        onKeyDown?.(e);
        if (!clickable || e.defaultPrevented || e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onClick?.(e as unknown as React.MouseEvent<HTMLTableRowElement>);
        }
      }}
      aria-selected={selected || undefined}
      className={cn(
        "transition-colors duration-press [&>td]:border-b [&>td]:border-border",
        "[tbody_&]:hover:bg-accent/50",
        clickable &&
          "cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
        selected && "bg-primary-subtle/50 [tbody_&]:hover:bg-primary-subtle/60",
        className,
      )}
    />
  );
}

type Align = "start" | "center" | "end";
const ALIGN: Record<Align, string> = { start: "text-start", center: "text-center", end: "text-end" };

export function TH({
  className,
  numeric,
  align,
  ...p
}: Omit<React.ThHTMLAttributes<HTMLTableCellElement>, "align"> & { numeric?: boolean; align?: Align }) {
  const density = React.useContext(DensityContext);
  return (
    <th
      className={cn(
        "whitespace-nowrap text-start align-middle font-medium",
        density === "compact" ? "h-8 px-3" : "h-9 px-4",
        numeric && "text-end",
        align && ALIGN[align],
        className,
      )}
      {...p}
    />
  );
}

export function TD({
  className,
  numeric,
  nowrap,
  truncate,
  muted,
  align,
  ...p
}: Omit<React.TdHTMLAttributes<HTMLTableCellElement>, "align"> & {
  /** Right-aligned tabular figures, never wrapped: bytes, counts, money, durations. */
  numeric?: boolean;
  /** Keeps short values (dates, speeds, codes, badges) on one line. */
  nowrap?: boolean;
  /** One line with an ellipsis; give the cell a max width. Put the full value in `title`. */
  truncate?: boolean;
  muted?: boolean;
  align?: Align;
}) {
  const density = React.useContext(DensityContext);
  return (
    <td
      className={cn(
        "align-middle",
        density === "compact" ? "px-3 py-2" : "px-4 py-2.5",
        numeric && "whitespace-nowrap text-end tabular",
        nowrap && "whitespace-nowrap",
        truncate && "max-w-[18rem] truncate",
        muted && "text-muted-foreground",
        align && ALIGN[align],
        className,
      )}
      {...p}
    />
  );
}

/** The empty or loading row inside a table body; spans every column. */
export function TableMessage({ colSpan, className, children }: { colSpan: number; className?: string; children: React.ReactNode }) {
  return (
    <tr>
      <td colSpan={colSpan} className={cn("px-4 py-10 text-center text-sm text-muted-foreground", className)}>
        {children}
      </td>
    </tr>
  );
}

export function TableWrap({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("overflow-hidden rounded-b-lg", className)} {...p} />;
}
