import { cn } from "@/lib/utils";

// Tones keep their existing names (`default | ok | warn | err | info`) because the whole admin passes them as
// strings, often through `as any` from a status-mapping helper. What changed is that each one is now a pair of
// status tokens rather than a hand-picked hex for one dark background — so the same badge is legible on a white
// table row and on a near-black one.
//
// `neutral` and `accent` are additions for counts and non-status labels.
type Tone = "default" | "ok" | "warn" | "err" | "info" | "neutral" | "accent";

const TONES: Record<Tone, string> = {
  default: "border-border bg-surface text-muted-foreground",
  neutral: "border-border bg-surface text-foreground",
  ok: "border-success/25 bg-success-subtle text-success-subtle-foreground",
  warn: "border-warning/30 bg-warning-subtle text-warning-subtle-foreground",
  err: "border-destructive/25 bg-destructive-subtle text-destructive-subtle-foreground",
  info: "border-info/25 bg-info-subtle text-info-subtle-foreground",
  accent: "border-primary/25 bg-primary-subtle text-primary-subtle-foreground",
};

export function Badge({
  tone = "default",
  dot = false,
  className,
  children,
}: {
  tone?: Tone;
  // A leading dot, for the cases where the word alone is the status ("active", "connected") and the colour is
  // doing the work. Off by default so nothing existing changes shape.
  dot?: boolean;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium leading-5 whitespace-nowrap",
        TONES[tone] ?? TONES.default,
        className,
      )}
    >
      {dot && <span className="size-1.5 shrink-0 rounded-full bg-current opacity-80" aria-hidden />}
      {children}
    </span>
  );
}

// StatusDot — the smallest possible health indicator, for dense lists where a full badge is too loud.
export function StatusDot({
  tone = "default",
  className,
  title,
}: {
  tone?: "ok" | "warn" | "err" | "info" | "default";
  className?: string;
  title?: string;
}) {
  const colors: Record<string, string> = {
    ok: "bg-success",
    warn: "bg-warning",
    err: "bg-destructive",
    info: "bg-info",
    default: "bg-muted-foreground/50",
  };
  return (
    <span
      title={title}
      className={cn("inline-block size-2 shrink-0 rounded-full", colors[tone] ?? colors.default, className)}
    />
  );
}
