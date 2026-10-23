"use client";

import * as React from "react";
import { EyeOff, RefreshCw } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./button";

function relative(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 5) return "just now";
  if (s < 60) return `${s} s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  return `${h} h ago`;
}

/** Keep prior content visible while a refresh runs so rows do not jump. */
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

  // Announce refresh events, not the changing clock, to avoid repeated screen-reader announcements.
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

export const refreshingClass = "opacity-60 transition-opacity duration-base";

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
