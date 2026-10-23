"use client";

import * as React from "react";
import {
  AlertTriangle, Check, Copy, Eye, EyeOff, Lock, RefreshCw, ShieldAlert, Timer, EyeOff as Hidden,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./button";
import {
  Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "./dialog";

export function CopyButton({
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
          /* The value remains visible if clipboard access is denied. */
        }
      }}
    >
      {copied ? <Check /> : <Copy />}
      <span aria-live="polite">{copied ? copiedLabel : label}</span>
    </Button>
  );
}

/** One-time secrets require explicit acknowledgement because the appliance cannot reveal them again. */
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
  /** Permit hiding a visible secret from nearby observers. */
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

export function useSecondsLeft(deadline: string | number | null | undefined): number | null {
  const target = React.useMemo(() => {
    if (deadline === null || deadline === undefined || deadline === "") return null;
    const t = typeof deadline === "number" ? deadline : Date.parse(deadline);
    return Number.isFinite(t) ? t : null;
  }, [deadline]);
  const [now, setNow] = React.useState(() => Date.now());
  React.useEffect(() => {
    if (target === null) return;
    const iv = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(iv);
  }, [target]);
  if (target === null) return null;
  return Math.max(0, Math.ceil((target - now) / 1000));
}

export function formatCountdown(seconds: number): string {
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return m > 0 ? `${m}:${String(s).padStart(2, "0")}` : `${s}s`;
}

/** Unconfirmed network changes roll back automatically, so show the remaining confirmation window. */
export function PendingChangeBanner({
  title,
  description,
  deadline,
  secondsLeft: secondsOverride,
  onConfirm,
  onRollback,
  confirmLabel = "Keep this change",
  rollbackLabel = "Roll back now",
  busy,
  canAct = true,
  children,
  className,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  deadline?: string | number | null;
  secondsLeft?: number | null;
  onConfirm?: () => void;
  onRollback?: () => void;
  confirmLabel?: string;
  rollbackLabel?: string;
  busy?: "confirm" | "rollback" | null;
  canAct?: boolean;
  children?: React.ReactNode;
  className?: string;
}) {
  const ticking = useSecondsLeft(deadline ?? null);
  const left = secondsOverride ?? ticking;
  const urgent = left !== null && left <= 30;
  return (
    <section
      role="status"
      aria-live="polite"
      className={cn(
        "overflow-hidden rounded-lg border bg-card shadow-card",
        urgent ? "border-destructive/50" : "border-warning/50",
        className,
      )}
    >
      <div className={cn("flex flex-wrap items-center gap-4 px-5 py-4", urgent ? "bg-destructive-subtle" : "bg-warning-subtle")}>
        <div
          className={cn(
            "flex size-14 shrink-0 flex-col items-center justify-center rounded-lg bg-card font-mono tabular shadow-card",
            urgent ? "text-destructive" : "text-warning-subtle-foreground",
          )}
          aria-label={left !== null ? `${left} seconds left` : undefined}
        >
          <Timer className="size-4" aria-hidden />
          <span className="text-sm font-bold">{left !== null ? formatCountdown(left) : "—"}</span>
        </div>
        <div className="min-w-0 flex-1 space-y-0.5">
          <div className={cn("text-emphasis", urgent ? "text-destructive-subtle-foreground" : "text-warning-subtle-foreground")}>
            {title}
          </div>
          <p className="text-sm text-foreground/80">
            {description ??
              "The change is live now. Confirm it to keep it; if nobody does before the timer runs out, the appliance puts the previous configuration back on its own."}
          </p>
        </div>
        {canAct && (onConfirm || onRollback) && (
          <div className="flex shrink-0 flex-wrap gap-2">
            {onRollback && (
              <Button variant="secondary" onClick={onRollback} disabled={!!busy}>
                {busy === "rollback" ? "Rolling back…" : rollbackLabel}
              </Button>
            )}
            {onConfirm && (
              <Button onClick={onConfirm} disabled={!!busy}>
                {busy === "confirm" ? "Confirming…" : confirmLabel}
              </Button>
            )}
          </div>
        )}
      </div>
      {children && <div className="border-t border-border px-5 py-4">{children}</div>}
    </section>
  );
}

function relative(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
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
  return (
    <div className={cn("inline-flex items-center gap-2 text-caption text-muted-foreground", className)}>
      <span className="relative inline-flex size-2" aria-hidden>
        {!error && intervalSeconds ? (
          <span className="absolute inline-flex size-full animate-ping rounded-full bg-success/60 motion-reduce:hidden" />
        ) : null}
        <span className={cn("relative inline-flex size-2 rounded-full", error ? "bg-warning" : intervalSeconds ? "bg-success" : "bg-muted-foreground/50")} />
      </span>
      <span aria-live="polite" className="tabular">
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

export const refreshingClass = "opacity-60 transition-opacity duration-base";

/** The server enforces read-only access; this explains disabled controls. */
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

export function SettingField({
  id,
  label,
  value,
  onChange,
  unit,
  min,
  max,
  defaultValue,
  explanation,
  readOnly,
  error,
}: {
  id?: string;
  label: React.ReactNode;
  value: string;
  onChange: (v: string) => void;
  unit: string;
  min: number;
  max: number;
  defaultValue: number;
  explanation?: React.ReactNode;
  readOnly?: boolean;
  error?: React.ReactNode;
}) {
  const gen = React.useId();
  const fid = id ?? gen;
  const n = Number(value);
  const out = value !== "" && (!Number.isFinite(n) || n < min || n > max);
  return (
    <div className="min-w-0 space-y-1.5">
      <label htmlFor={fid} className="block text-label">
        {label}
      </label>
      <div className="flex items-stretch">
        <input
          id={fid}
          inputMode="numeric"
          value={value}
          readOnly={readOnly}
          disabled={readOnly}
          aria-invalid={out || !!error || undefined}
          aria-describedby={`${fid}-hint`}
          onChange={(e) => onChange(e.target.value)}
          className={cn(
            "h-10 w-28 rounded-s-md border border-input bg-card px-3 text-sm tabular text-foreground",
            "focus:border-foreground focus:outline-none focus:ring-2 focus:ring-ring/20",
            "disabled:bg-surface disabled:text-muted-foreground",
            (out || error) && "border-destructive",
          )}
        />
        <span className="inline-flex items-center rounded-e-md border border-s-0 border-input bg-surface px-3 text-sm text-muted-foreground">
          {unit}
        </span>
      </div>
      <p id={`${fid}-hint`} className={cn("text-caption", out || error ? "text-destructive" : "text-muted-foreground")}>
        {error ??
          (out
            ? `Must be between ${min} and ${max} ${unit}.`
            : (
                <>
                  {explanation} {explanation ? " " : ""}
                  Default {defaultValue} {unit}; allowed {min}–{max}.
                </>
              ))}
      </p>
    </div>
  );
}
