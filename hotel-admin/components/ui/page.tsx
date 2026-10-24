import Link from "next/link";
import { cn } from "@/lib/utils";
import { HelpTip } from "@/components/help";

// The scrolling layout owns the page gutter; this shell controls the measure and the vertical rhythm only.

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
 * Title, the help trigger (detailed guidance lives in `help`, shown in the HelpTip side sheet), at most one short
 * description line, and the page's actions on the right. The top-bar breadcrumb provides orientation.
 */
export function PageHeader({
  title,
  description,
  actions,
  help,
  helpTitle,
  meta,
  className,
  children,
}: {
  title: React.ReactNode;
  /** One short sentence. Anything longer belongs in `help`. */
  description?: React.ReactNode;
  /** Detailed guidance, opened from the lightbulb next to the title. Operational warnings stay on the page. */
  help?: React.ReactNode;
  helpTitle?: string;
  actions?: React.ReactNode;
  /** A small inline status beside the title, e.g. a Badge or LiveStatus. */
  meta?: React.ReactNode;
  /** @deprecated Not rendered: the breadcrumb already names the section. Remove from callers. */
  eyebrow?: React.ReactNode;
  /** @deprecated Not rendered. Remove from callers. */
  icon?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}) {
  return (
    <header className={cn("space-y-3", className)}>
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="min-w-[min(100%,16rem)] flex-1">
          <div className="flex min-h-9 flex-wrap items-center gap-x-2 gap-y-1">
            <h1 className="text-subtitle sm:text-title">{title}</h1>
            {help && (
              <HelpTip title={helpTitle ?? (typeof title === "string" ? title : "This page")}>{help}</HelpTip>
            )}
            {meta && <div className="flex items-center gap-2">{meta}</div>}
          </div>
          {description && (
            <p className="mt-0.5 max-w-3xl text-sm text-muted-foreground">{description}</p>
          )}
        </div>
        {actions && <div className="flex max-w-full flex-wrap items-center gap-2 sm:shrink-0">{actions}</div>}
      </div>
      {children}
    </header>
  );
}

type StatTone = "default" | "ok" | "warn" | "err" | "info" | "primary";

const VALUE_TONE: Record<StatTone, string> = {
  default: "text-foreground",
  primary: "text-foreground",
  ok: "text-success",
  warn: "text-warning-subtle-foreground",
  err: "text-destructive",
  info: "text-foreground",
};

/** Label, value, and at most one short hint. Long explanations go in `explain` (a tooltip) or the page help. */
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
  /** One short line, e.g. "of 250 licensed". */
  hint?: React.ReactNode;
  /** A small muted glyph beside the label. */
  icon?: React.ReactNode;
  /** Colours the value only for a state that needs attention (warn, err) or is confirmed good (ok). */
  tone?: StatTone;
  trend?: { value: React.ReactNode; direction: "up" | "down" | "flat"; good?: boolean };
  href?: string;
  explain?: React.ReactNode;
  footer?: React.ReactNode;
  className?: string;
}) {
  const body = (
    <>
      <div className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
        {icon && <span className="inline-flex shrink-0 [&_svg]:size-3.5" aria-hidden>{icon}</span>}
        <span className="truncate">{label}</span>
        {explain}
      </div>
      <div className="mt-1.5 flex items-baseline gap-2">
        <div className={cn("text-metric tabular", VALUE_TONE[tone])}>{value}</div>
        {trend && (
          <span
            className={cn(
              "text-xs font-medium tabular",
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
      {hint && <div className="mt-1 line-clamp-1 text-xs text-muted-foreground">{hint}</div>}
      {footer && <div className="mt-3">{footer}</div>}
    </>
  );

  const shell = cn(
    "block min-w-0 rounded-lg border border-border bg-card px-4 py-3.5 text-card-foreground shadow-card",
    href &&
      "transition-[box-shadow,border-color] duration-base ease-onegate hover:border-border-strong hover:shadow-card-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
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

/** A responsive row of StatCards: two per row on phones, up to `cols` on wide screens. */
export function StatGrid({ cols = 4, className, children }: { cols?: 2 | 3 | 4 | 5; className?: string; children: React.ReactNode }) {
  const grid = {
    2: "sm:grid-cols-2",
    3: "sm:grid-cols-3",
    4: "sm:grid-cols-2 lg:grid-cols-4",
    5: "sm:grid-cols-3 xl:grid-cols-5",
  }[cols];
  return <div className={cn("grid grid-cols-2 gap-3", grid, className)}>{children}</div>;
}

/** A loose row of controls aligned on their baseline. For filters + search + actions use FilterBar. */
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
