"use client";

/*
  ONEGATE PRODUCT PATTERNS — the recurring states every screen needs, designed once.

  The primitives (Button, Card, Badge, Dialog…) say how a thing looks. These say how a SITUATION looks:
  a secret shown once, data that refreshes on its own, a role that may look but not touch, a block the
  operator's role cannot see, what a destructive action will do. Before these existed each screen
  answered those situations in its own words and its own layout, which is how the same product ended up
  with four different "this is read-only" treatments.

  See design-system/README.md, "Patterns".
*/

import * as React from "react";
import {
  AlertTriangle, Check, Copy, Eye, EyeOff, Lock, RefreshCw, ShieldAlert, EyeOff as Hidden,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./button";
import {
  Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "./dialog";

/* ------------------------------------------------------------------------------------------------------ */
/* CopyButton                                                                                              */
/* ------------------------------------------------------------------------------------------------------ */

function CopyButton({
  value,
  label = "Copy",
  copiedLabel = "Copied",
  variant = "secondary",
  size = "sm",
  className,
}: {
  value: string;
  label?: string;
  copiedLabel?: string;
  variant?: React.ComponentProps<typeof Button>["variant"];
  size?: React.ComponentProps<typeof Button>["size"];
  className?: string;
}) {
  const [copied, setCopied] = React.useState(false);
  React.useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1600);
    return () => clearTimeout(t);
  }, [copied]);
  return (
    <Button
      variant={variant}
      size={size}
      className={className}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
        } catch {
          /* Clipboard refused (insecure context, permission). The value is on screen; nothing to do. */
        }
      }}
    >
      {copied ? <Check /> : <Copy />}
      <span aria-live="polite">{copied ? copiedLabel : label}</span>
    </Button>
  );
}

/* ------------------------------------------------------------------------------------------------------ */
/* OneTimeReveal — "this is the only time you will see this"                                              */
/* ------------------------------------------------------------------------------------------------------ */

/**
 * A secret the appliance will never show again: a new guest-account password, a reset post-stay PIN, an
 * enrollment token. The design makes that unmissable: a modal (it cannot scroll away), a warning band that
 * says it in words, the value large and monospaced, copy beside it, and an explicit acknowledgement as the
 * ONLY way out other than Escape. It never offers "view again", because there is no such thing.
 */
export function OneTimeReveal({
  open,
  title,
  description,
  value,
  valueLabel,
  acknowledgeLabel = "I have it",
  concealable = false,
  onAcknowledge,
  children,
}: {
  open: boolean;
  title: React.ReactNode;
  description?: React.ReactNode;
  value: string;
  valueLabel?: string;
  acknowledgeLabel?: string;
  /** Let the operator hide the value on a screen others can see. It starts visible. */
  concealable?: boolean;
  onAcknowledge: () => void;
  children?: React.ReactNode;
}) {
  const [shown, setShown] = React.useState(true);
  React.useEffect(() => {
    if (open) setShown(true);
  }, [open]);
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onAcknowledge()}>
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <DialogBody className="space-y-4">
          <div
            role="alert"
            className="flex items-start gap-2.5 rounded-md border border-warning/30 bg-warning-subtle px-3 py-2.5 text-sm text-warning-subtle-foreground"
          >
            <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span>
              <strong className="font-semibold">Shown once.</strong> It cannot be looked up again after you
              close this window.
            </span>
          </div>
          <div className="space-y-1.5">
            {valueLabel && <div className="text-label text-muted-foreground">{valueLabel}</div>}
            <div className="flex items-center gap-2">
              <code
                data-testid="one-time-value"
                className="min-w-0 flex-1 select-all break-all rounded-md border border-border bg-surface px-3.5 py-3 font-mono text-xl font-semibold tracking-wider"
              >
                {shown ? value : "•".repeat(Math.min(value.length, 24))}
              </code>
              {concealable && (
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label={shown ? "Hide the value" : "Show the value"}
                  onClick={() => setShown((s) => !s)}
                >
                  {shown ? <EyeOff /> : <Eye />}
                </Button>
              )}
            </div>
          </div>
          {children}
        </DialogBody>
        <DialogFooter className="justify-between">
          <CopyButton value={value} />
          <Button onClick={onAcknowledge}>{acknowledgeLabel}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/* ------------------------------------------------------------------------------------------------------ */
/* LiveStatus — "Updated x ago" for polling screens                                                        */
/* ------------------------------------------------------------------------------------------------------ */

function relative(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
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

  // WHAT A SCREEN READER HEARS IS AN EVENT, NOT THE CLOCK. The visible "Updated 25s ago" re-renders every five
  // seconds; inside a live region that was an announcement every five seconds for as long as the page stayed
  // open. The live region below changes only when something happened: a refresh failed, recovered, or a
  // refresh the operator asked for finished.
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
        {intervalSeconds && !error ? <span className="hidden sm:inline"> · every {intervalSeconds}s</span> : null}
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
/* ReadOnlyNotice / RestrictedBlock                                                                        */
/* ------------------------------------------------------------------------------------------------------ */

/**
 * The one line a read-only screen shows. The server enforces the permission; this is what stops the
 * operator wondering why the fields are grey.
 */
export function ReadOnlyNotice({
  children = "Your role can view this but not change it.",
  className,
}: {
  children?: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      role="note"
      className={cn(
        "flex items-center gap-2 rounded-md border border-border bg-surface px-3 py-2 text-sm text-muted-foreground",
        className,
      )}
    >
      <Lock className="size-4 shrink-0" aria-hidden />
      <span>{children}</span>
    </div>
  );
}

/**
 * A block of a screen the operator's role may not see, or a figure that is not available. Dashed, so it is
 * visibly an absence and not an empty result; always with the reason.
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
        {icon ?? <Hidden />}
      </span>
      <div className="text-label">{title}</div>
      <p className="max-w-sm text-caption text-muted-foreground">{reason}</p>
    </div>
  );
}

/* ------------------------------------------------------------------------------------------------------ */
/* ConsequenceList — what a destructive action will do                                                     */
/* ------------------------------------------------------------------------------------------------------ */

export function ConsequenceList({
  items,
  tone = "danger",
  title,
}: {
  items: React.ReactNode[];
  tone?: "danger" | "warning";
  title?: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        "rounded-md border px-3.5 py-3 text-sm",
        tone === "danger"
          ? "border-destructive/30 bg-destructive-subtle text-destructive-subtle-foreground"
          : "border-warning/30 bg-warning-subtle text-warning-subtle-foreground",
      )}
    >
      <div className="mb-1.5 flex items-center gap-2 font-semibold">
        <AlertTriangle className="size-4 shrink-0" aria-hidden />
        {title ?? "This cannot be undone"}
      </div>
      <ul className="list-disc space-y-1 ps-5">
        {items.map((it, i) => (
          <li key={i}>{it}</li>
        ))}
      </ul>
    </div>
  );
}
