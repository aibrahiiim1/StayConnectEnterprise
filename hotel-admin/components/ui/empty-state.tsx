import { cn } from "@/lib/utils";

// EmptyState kept its `{ title, hint }` signature — roughly thirty screens call it that way. `icon` and
// `action` are optional additions, so an empty list can now offer the thing that would fill it instead of
// only reporting that it is empty.
export function EmptyState({
  title,
  hint,
  icon,
  action,
  className,
}: {
  title: string;
  hint?: React.ReactNode;
  icon?: React.ReactNode;
  action?: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col items-center justify-center px-6 py-12 text-center", className)}>
      {icon && (
        <div className="mb-3 flex size-10 items-center justify-center rounded-full border border-border bg-surface text-muted-foreground [&_svg]:size-5">
          {icon}
        </div>
      )}
      <div className="text-sm font-medium text-foreground">{title}</div>
      {hint && <div className="mt-1 max-w-md text-sm text-muted-foreground">{hint}</div>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}
