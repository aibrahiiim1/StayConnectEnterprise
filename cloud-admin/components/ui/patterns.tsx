"use client";

/*
  ONEGATE PRODUCT PATTERNS — the recurring situations every screen needs, designed once.

  The primitives (Button, Card, Badge, Dialog…) say how a thing looks. These say how a SITUATION looks: data
  that refreshes on its own, and a block that is absent (not found, not available to this role) rather than
  merely empty.

  See design-system/README.md, "Patterns".
*/

import * as React from "react";
import { EyeOff, RefreshCw } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./button";

/* ------------------------------------------------------------------------------------------------------ */
/* LiveStatus — "Updated x ago" for polling screens                                                        */
/* ------------------------------------------------------------------------------------------------------ */

function relative(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 5) return "just now";
  if (s < 60) return `${s} s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  return `${h} h ago`;
}

/**
 * Tells the operator the numbers are live and how fresh they are, without moving anything else on the
 * screen. A polling screen DIMS its content while refreshing (see `refreshingClass`) rather than blanking
 * it, so a reload never makes rows jump.
 */
export function LiveStatus({
  updatedAt,
  refreshing,
  onRefresh,
  intervalSeconds,
  error,
  className,
}: {
  updatedAt: Date | number | null | undefined;
  refreshing?: boolean;
  onRefresh?: () => void;
  intervalSeconds?: number;
  error?: boolean;
  className?: string;
}) {
  const [, force] = React.useState(0);
  React.useEffect(() => {
    const iv = setInterval(() => force((n) => n + 1), 5000);
    return () => clearInterval(iv);
  }, []);
  const at = updatedAt instanceof Date ? updatedAt.getTime() : updatedAt ?? null;

  // WHAT A SCREEN READER HEARS IS AN EVENT, NOT THE CLOCK. The visible "Updated 25 s ago" re-renders every five
  // seconds; inside a live region that would be an announcement every five seconds. The live region below
  // changes only when something happened: a refresh failed, recovered, or a refresh the operator asked for
  // finished.
  const [announcement, setAnnouncement] = React.useState("");
  const prev = React.useRef({ error, refreshing });
  React.useEffect(() => {
    const was = prev.current;
    if (error && !was.error) setAnnouncement("Could not refresh — showing the last answer");
    else if (!error && was.error) setAnnouncement("Refreshed");
    else if (!error && was.refreshing && !refreshing) setAnnouncement("Refreshed");
    prev.current = { error, refreshing };
  }, [error, refreshing]);

  return (
    <div className={cn("inline-flex items-center gap-2 text-caption text-muted-foreground", className)}>
      <span className="relative inline-flex size-2" aria-hidden>
        {!error && intervalSeconds ? (
          <span className="absolute inline-flex size-full animate-ping rounded-full bg-success/60 motion-reduce:hidden" />
        ) : null}
        <span className={cn("relative inline-flex size-2 rounded-full", error ? "bg-warning" : intervalSeconds ? "bg-success" : "bg-muted-foreground/50")} />
      </span>
      <span role="status" aria-live="polite" className="sr-only">{announcement}</span>
      <span className="tabular">
        {error
          ? "Could not refresh — showing the last answer"
          : refreshing
            ? "Refreshing…"
            : at
              ? `Updated ${relative(Date.now() - at)}`
              : "Loading…"}
        {intervalSeconds && !error ? <span className="hidden sm:inline"> · every {intervalSeconds} s</span> : null}
      </span>
      {onRefresh && (
        <Button variant="ghost" size="icon-sm" aria-label="Refresh now" onClick={onRefresh} disabled={refreshing}>
          <RefreshCw className={cn(refreshing && "animate-spin motion-reduce:animate-none")} />
        </Button>
      )}
    </div>
  );
}

/** Apply to a container while it reloads: it stays readable and in place, just visibly quieter. */
export const refreshingClass = "opacity-60 transition-opacity duration-base";

/* ------------------------------------------------------------------------------------------------------ */
/* NotAvailable                                                                                            */
/* ------------------------------------------------------------------------------------------------------ */

/**
 * A block the operator cannot see, or a record that is not there. Dashed, so it is visibly an absence and not
 * an empty result; always with the reason.
 */
export function NotAvailable({
  title = "Not available",
  reason,
  icon,
  className,
}: {
  title?: React.ReactNode;
  reason: React.ReactNode;
  icon?: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex min-h-24 flex-col items-center justify-center gap-1 rounded-lg border border-dashed border-border-strong px-4 py-6 text-center",
        className,
      )}
    >
      <span className="text-muted-foreground [&_svg]:size-5" aria-hidden>
        {icon ?? <EyeOff />}
      </span>
      <div className="text-label">{title}</div>
      <p className="max-w-sm text-caption text-muted-foreground">{reason}</p>
    </div>
  );
}
