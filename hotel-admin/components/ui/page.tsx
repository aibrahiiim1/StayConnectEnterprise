import Link from "next/link";
import { cn } from "@/lib/utils";
import { HelpTip } from "@/components/help";

// The scrolling layout owns page gutters; this shell controls width and spacing only.

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

/** The eyebrow orients visitors who arrive directly from a link. */
export function PageHeader({
  title,
  description,
  eyebrow,
  actions,
  icon,
  help,
  helpTitle,
  className,
  children,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  /** Keep warnings on the page, outside optional help. */
  help?: React.ReactNode;
  helpTitle?: string;
  eyebrow?: React.ReactNode;
  actions?: React.ReactNode;
  icon?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}) {
  return (
    <header className={cn("space-y-3", className)}>
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div className="flex min-w-0 items-start gap-3.5">
        {icon && (
          <span
            className="mt-1 hidden size-11 shrink-0 items-center justify-center rounded-lg bg-primary-subtle text-primary-subtle-foreground sm:inline-flex [&_svg]:size-5"
            aria-hidden
          >
            {icon}
          </span>
        )}
        <div className="min-w-0 space-y-1">
          {eyebrow && (
            <div className="text-micro uppercase tracking-[0.08em] text-muted-foreground">
              {eyebrow}
            </div>
          )}
          <div className="flex items-center gap-2">
            <h1 className="text-subtitle sm:text-title">{title}</h1>
            {help && (
              <HelpTip title={helpTitle ?? (typeof title === "string" ? title : "This page")}>{help}</HelpTip>
            )}
          </div>
          {description && (
            <p className="max-w-3xl text-sm leading-relaxed text-muted-foreground">{description}</p>
          )}
        </div>
        </div>
        {actions && <div className="flex max-w-full flex-wrap items-center gap-2 sm:shrink-0">{actions}</div>}
      </div>
      {children}
    </header>
  );
}

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
        <div className="text-metric leading-none tabular">{value}</div>
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
    "block rounded-lg border border-border bg-card p-4 text-card-foreground shadow-card",
    href &&
      "transition-[box-shadow,border-color] duration-base ease-onegate hover:border-border-strong hover:shadow-card-hover focus-visible:shadow-card-hover",
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
