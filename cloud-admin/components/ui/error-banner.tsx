import { AlertCircle, CheckCircle2, Info, AlertTriangle } from "lucide-react";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/utils";

// formatError accepts either a thrown ApiError, a free-form string, or null.
// Returns the user-visible message + optional trace id for support tickets.
export function ErrorBanner({ err, className }: { err: unknown; className?: string }) {
  if (err == null || err === "") return null;
  let message = "";
  let traceId: string | undefined;
  if (err instanceof ApiError) {
    message = err.message;
    traceId = err.traceId;
  } else if (typeof err === "string") {
    message = err;
  } else if (err instanceof Error) {
    message = err.message;
  } else {
    message = String(err);
  }
  return (
    // role="alert" + aria-live: an error that only appears visually is an error a screen-reader user is
    // never told about, and this component is how most of the admin reports failures. Pages that hand-rolled
    // a <p role="alert"> already announced; those switching to this shared banner silently stopped, which is
    // how the gap surfaced.
    <div
      role="alert"
      aria-live="assertive"
      className={cn(
        "mb-4 flex items-start justify-between gap-4 rounded-md border border-destructive/25",
        "bg-destructive-subtle px-3.5 py-2.5 text-sm text-destructive-subtle-foreground",
        className,
      )}
    >
      <span className="flex min-w-0 items-start gap-2.5">
        <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden />
        <span className="min-w-0 break-words">{message}</span>
      </span>
      {traceId && (
        <span
          className="shrink-0 font-mono text-xs opacity-70"
          title="Server trace id — include when reporting issues"
        >
          {traceId}
        </span>
      )}
    </div>
  );
}

// Callout — the same shape for the three non-error notices the admin keeps re-inventing: a confirmation, a
// caveat, an explanation. Pages were producing these as bare `<p className="text-sm text-success-subtle-foreground">`, which
// is invisible on a dark card and unannounced to a screen reader.
const CALLOUT_TONES = {
  info: {
    box: "border-info/25 bg-info-subtle text-info-subtle-foreground",
    Icon: Info,
    role: undefined as string | undefined,
  },
  success: {
    box: "border-success/25 bg-success-subtle text-success-subtle-foreground",
    Icon: CheckCircle2,
    role: "status",
  },
  warning: {
    box: "border-warning/30 bg-warning-subtle text-warning-subtle-foreground",
    Icon: AlertTriangle,
    role: "status",
  },
  danger: {
    box: "border-destructive/25 bg-destructive-subtle text-destructive-subtle-foreground",
    Icon: AlertCircle,
    role: "alert",
  },
  neutral: {
    box: "border-border bg-surface text-foreground",
    Icon: Info,
    role: undefined as string | undefined,
  },
} as const;

export function Callout({
  tone = "info",
  title,
  icon,
  className,
  children,
}: {
  tone?: keyof typeof CALLOUT_TONES;
  title?: React.ReactNode;
  icon?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}) {
  const t = CALLOUT_TONES[tone];
  const Icon = t.Icon;
  return (
    <div
      role={t.role}
      aria-live={t.role === "alert" ? "assertive" : t.role === "status" ? "polite" : undefined}
      className={cn("flex items-start gap-2.5 rounded-md border px-3.5 py-2.5 text-sm", t.box, className)}
    >
      <span className="mt-0.5 shrink-0 [&_svg]:size-4" aria-hidden>
        {icon ?? <Icon className="size-4" />}
      </span>
      <div className="min-w-0 space-y-1">
        {title && <div className="font-medium">{title}</div>}
        {children && <div className="[&_strong]:font-semibold">{children}</div>}
      </div>
    </div>
  );
}
