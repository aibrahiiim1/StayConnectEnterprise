import Link from "next/link";
import { cn } from "@/lib/utils";

// THE PAGE MEASURE AND RHYTHM.
//
// The GUTTER is deliberately NOT here — it lives on the scrolling container in app/(app)/layout.tsx, so that a
// screen cannot render flush against the window edge by forgetting to ask for padding. That is exactly how half
// this admin ended up doing it: about half the screens opened with a padded, centred container of their own and
// the other half with a bare `space-y-4` div, which has no padding at all — so their cards touched both the
// window edge and the sidebar. The padded half did not agree either: max-w-7xl, max-w-[92rem] and max-w-3xl all
// appeared, alongside two different padding scales.
//
// What this component owns is the maximum measure and the vertical spacing between blocks. `width` is the only
// knob: a session table with eight columns wants the room, and a single form does not want 96rem of it.

const WIDTHS = {
  wide: "max-w-[96rem]",
  default: "max-w-7xl",
  narrow: "max-w-3xl",
  form: "max-w-2xl",
} as const;

export function PageShell({
  width = "default",
  className,
  children,
}: {
  width?: keyof typeof WIDTHS;
  className?: string;
  children: React.ReactNode;
}) {
  return <div className={cn("mx-auto w-full space-y-5", WIDTHS[width], className)}>{children}</div>;
}

/**
 * PageHeader — the title block. `eyebrow` is the section the screen belongs to, which the sidebar also shows;
 * having it here is what lets someone arriving from a link know where they are.
 */
export function PageHeader({
  title,
  description,
  eyebrow,
  actions,
  className,
  children,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  eyebrow?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}) {
  return (
    <header className={cn("space-y-3", className)}>
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="min-w-0 space-y-1">
          {eyebrow && (
            <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">
              {eyebrow}
            </div>
          )}
          <h1 className="text-xl font-semibold tracking-tight sm:text-2xl">{title}</h1>
          {description && (
            <p className="max-w-3xl text-sm leading-relaxed text-muted-foreground">{description}</p>
          )}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {children}
    </header>
  );
}

/**
 * StatCard — the dashboard tile. Replaces `KpiCard`, which could show a label, a value and one line of hint
 * and nothing else: no trend, no secondary figure, no link to the screen that explains it, and no way to mark
 * a figure as a problem.
 */
export function StatCard({
  label,
  value,
  hint,
  icon,
  tone = "default",
  trend,
  href,
  explain,
  footer,
  className,
}: {
  label: React.ReactNode;
  value: React.ReactNode;
  hint?: React.ReactNode;
  icon?: React.ReactNode;
  tone?: "default" | "ok" | "warn" | "err" | "info" | "primary";
  trend?: { value: React.ReactNode; direction: "up" | "down" | "flat"; good?: boolean };
  href?: string;
  explain?: React.ReactNode;
  footer?: React.ReactNode;
  className?: string;
}) {
  const accents: Record<string, string> = {
    default: "text-muted-foreground bg-surface",
    ok: "text-success bg-success-subtle",
    warn: "text-warning-subtle-foreground bg-warning-subtle",
    err: "text-destructive-subtle-foreground bg-destructive-subtle",
    info: "text-info-subtle-foreground bg-info-subtle",
    primary: "text-primary-subtle-foreground bg-primary-subtle",
  };

  const body = (
    <>
      <div className="flex items-start justify-between gap-3">
        <div className="inline-flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <span className="truncate">{label}</span>
          {explain}
        </div>
        {icon && (
          <span
            className={cn(
              "inline-flex size-7 shrink-0 items-center justify-center rounded-md [&_svg]:size-4",
              accents[tone],
            )}
            aria-hidden
          >
            {icon}
          </span>
        )}
      </div>
      <div className="mt-2 flex items-end gap-2">
        <div className="text-2xl font-semibold leading-none tabular tracking-tight">{value}</div>
        {trend && (
          <span
            className={cn(
              "mb-0.5 text-xs font-medium tabular",
              trend.direction === "flat"
                ? "text-muted-foreground"
                : trend.good === false
                  ? "text-destructive"
                  : "text-success",
            )}
          >
            {trend.direction === "up" ? "↑" : trend.direction === "down" ? "↓" : "·"} {trend.value}
          </span>
        )}
      </div>
      {hint && <div className="mt-1.5 text-xs leading-relaxed text-muted-foreground">{hint}</div>}
      {footer && <div className="mt-3">{footer}</div>}
    </>
  );

  const shell = cn(
    "block rounded-lg border border-border bg-card p-4 text-card-foreground shadow-xs",
    href && "transition-colors hover:border-border-strong hover:bg-surface/40",
    className,
  );

  return href ? (
    <Link href={href} className={shell}>
      {body}
    </Link>
  ) : (
    <div className={shell}>{body}</div>
  );
}

/**
 * Toolbar — the filter/search row that sits above a table. Pages were building this with ad-hoc flex rows, so
 * the search box was a different height from the select next to it on most screens.
 */
export function Toolbar({
  className,
  children,
}: {
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={cn("flex flex-wrap items-end justify-between gap-3", className)}>{children}</div>
  );
}
