import { cn } from "@/lib/utils";

type Tone = "default" | "ok" | "warn" | "err" | "info" | "neutral" | "accent";

const TONES: Record<Tone, string> = {
  default: "border-transparent bg-accent text-muted-foreground",
  neutral: "border-transparent bg-accent text-foreground",
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
  dot?: boolean;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[0.6875rem] font-bold leading-4 tracking-[0.01em] whitespace-nowrap",
        TONES[tone] ?? TONES.default,
        className,
      )}
    >
      {dot && <span className="size-1.5 shrink-0 rounded-full bg-current opacity-80" aria-hidden />}
      {children}
    </span>
  );
}
