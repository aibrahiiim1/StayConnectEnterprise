import { cn } from "@/lib/utils";

export type Tone = "default" | "ok" | "warn" | "err" | "info" | "neutral" | "accent";

const TONES: Record<Tone, string> = {
  default: "border-border bg-surface text-muted-foreground",
  neutral: "border-border bg-surface text-foreground",
  ok: "border-success/20 bg-success-subtle text-success-subtle-foreground",
  warn: "border-warning/25 bg-warning-subtle text-warning-subtle-foreground",
  err: "border-destructive/20 bg-destructive-subtle text-destructive-subtle-foreground",
  info: "border-info/20 bg-info-subtle text-info-subtle-foreground",
  accent: "border-primary/20 bg-primary-subtle text-primary-subtle-foreground",
};

const DOTS: Record<Tone, string> = {
  default: "bg-muted-foreground/60",
  neutral: "bg-muted-foreground/60",
  ok: "bg-success",
  warn: "bg-warning",
  err: "bg-destructive",
  info: "bg-info",
  accent: "bg-primary",
};

export function Badge({
  tone = "default",
  dot = false,
  size = "md",
  className,
  title,
  children,
}: {
  tone?: Tone;
  dot?: boolean;
  size?: "sm" | "md";
  className?: string;
  title?: string;
  children: React.ReactNode;
}) {
  return (
    <span
      title={title}
      className={cn(
        "inline-flex max-w-full items-center gap-1.5 whitespace-nowrap rounded-full border font-medium",
        size === "sm" ? "h-5 px-1.5 text-[0.6875rem] leading-4" : "h-[1.375rem] px-2 text-2xs",
        TONES[tone] ?? TONES.default,
        className,
      )}
    >
      {dot && <span className={cn("size-1.5 shrink-0 rounded-full", DOTS[tone] ?? DOTS.default)} aria-hidden />}
      {children}
    </span>
  );
}

/** A state word with its dot: the one way to show a lifecycle state (Active, Suspended, Failed…) in a row. */
export function StatusBadge({
  tone = "default",
  className,
  title,
  children,
}: {
  tone?: Tone;
  className?: string;
  title?: string;
  children: React.ReactNode;
}) {
  return (
    <Badge tone={tone} dot className={className} title={title}>
      {children}
    </Badge>
  );
}

export function StatusDot({
  tone = "default",
  className,
  title,
}: {
  tone?: "ok" | "warn" | "err" | "info" | "default";
  className?: string;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={cn("inline-block size-2 shrink-0 rounded-full", DOTS[tone] ?? DOTS.default, className)}
    />
  );
}
