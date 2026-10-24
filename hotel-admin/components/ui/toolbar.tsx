import { cn } from "@/lib/utils";

/**
 * The row above a list: segmented filters on the left, then search, then actions on the right — one line on a laptop,
 * wrapping cleanly on a phone (search takes the full width first). Put it inside the list's Card (`inset`) or above
 * it. Use `children` for anything else (a date range, a select); it sits after the filters.
 */
export function FilterBar({
  filters,
  search,
  actions,
  inset = false,
  className,
  children,
}: {
  filters?: React.ReactNode;
  search?: React.ReactNode;
  actions?: React.ReactNode;
  /** Draws the bar as the top section of a card, with a divider under it. */
  inset?: boolean;
  className?: string;
  children?: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-2",
        inset && "border-b border-border px-4 py-3",
        className,
      )}
    >
      {filters && <div className="flex min-w-0 max-w-full flex-wrap items-center gap-2">{filters}</div>}
      {children}
      {search && <div className="order-first w-full min-w-0 sm:order-none sm:w-auto sm:max-w-sm sm:flex-1 [&>*]:!w-full">{search}</div>}
      {actions && <div className="ms-auto flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}
