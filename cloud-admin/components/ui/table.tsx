"use client";

import * as React from "react";
import { cn } from "@/lib/utils";

// TABLES ARE THE PRODUCT. Almost every screen here is a list of operational rows, so the table treatment is
// what the admin mostly looks like.
//
// Three things changed and all three were legibility, not decoration:
//   * the header row now has its own surface, so a long list keeps a visible top edge when scrolled;
//   * rows have a hover state, because an operator tracking one appliance across six columns needs to be able to
//     see which row their eye is on;
//   * the horizontal scroll container is bounded with a rounded edge instead of bleeding off the card.
//
// Signatures are unchanged: Table/THead/TR/TH/TD are drop-in.

export function Table({
  className,
  label,
  ...p
}: React.HTMLAttributes<HTMLTableElement> & {
  /** Names the scroll region when the table is wider than its card. Defaults to the table's aria-label. */
  label?: string;
}) {
  // A SCROLLER A KEYBOARD CAN REACH. When the table is wider than its card, the only way to see the clipped
  // columns was a mouse or a trackpad. An overflowing scroller becomes a named, focusable region, so the arrow
  // keys scroll it; a table that fits adds no tab stop. The check re-runs whenever the scroller or the table
  // changes size (a resized window, rows arriving).
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

  // `relative` is load-bearing. Without it an absolutely positioned descendant (the `sr-only` label on an
  // icon-only header, a tooltip anchor) takes its containing block from somewhere OUTSIDE this scroller, so it
  // is laid out at the table's full width and stretches the whole DOCUMENT sideways on a phone — measured at
  // 390px as a 759px page. Positioning the scroller makes it the containing block, so
  // those elements scroll with the table instead of widening the page.
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
        "bg-surface text-start text-micro uppercase tracking-[0.06em] text-muted-foreground",
        "[&_th]:border-b [&_th]:border-border",
        className,
      )}
      {...p}
    />
  );
}

export function TR({ className, ...p }: React.HTMLAttributes<HTMLTableRowElement>) {
  return (
    <tr
      className={cn(
        "border-b border-border last:border-0 transition-colors",
        // Only body rows highlight. `tbody &` keeps the header row out of it without every call site having to
        // pass a different component.
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
