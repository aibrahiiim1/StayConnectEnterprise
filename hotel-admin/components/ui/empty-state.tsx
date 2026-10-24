import { cn } from "@/lib/utils";

export function EmptyState({
  title,
  hint,
  icon,
  action,
  size = "md",
  className,
}: {
  title: string;
  hint?: React.ReactNode;
  icon?: React.ReactNode;
  action?: React.ReactNode;
  /** `sm` for an empty list inside a card section; `md` for an empty page-level list. */
  size?: "sm" | "md";
  className?: string;
}) {
  return (
    // A status role announces "no results" after a search.
    <div
      role="status"
      className={cn(
        "flex flex-col items-center justify-center px-6 text-center",
        size === "sm" ? "py-8" : "py-12",
        className,
      )}
    >
      {icon && (
        <div className="mb-3 flex size-9 items-center justify-center rounded-lg border border-border bg-surface text-muted-foreground [&_svg]:size-4">
          {icon}
        </div>
      )}
      <div className="text-sm font-medium text-foreground">{title}</div>
      {hint && <div className="mt-1 max-w-md text-sm text-muted-foreground">{hint}</div>}
      {action && <div className="mt-4 flex flex-wrap items-center justify-center gap-2">{action}</div>}
    </div>
  );
}
