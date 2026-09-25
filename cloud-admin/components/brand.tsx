import { cn } from "@/lib/utils";

/*
  THE VELONET MARK. Two dotted chevrons -- the bracket motif of the brand's logo -- drawn inline so the
  appliance ships no image for it and it always takes the current brand token. Seven dots per bracket on a
  fixed grid: largest and full-strength at the tip, fading outward. It is a mark, never a wordmark: the
  product name is always set in type beside it (design-system/README.md, "Logo").
*/
const DOTS: { dx: number; dy: number; r: number; o: number }[] = [
  { dx: 0, dy: 0, r: 1.9, o: 1 },
  { dx: 2.6, dy: -2.8, r: 1.6, o: 0.75 },
  { dx: 2.6, dy: 2.8, r: 1.6, o: 0.75 },
  { dx: 5.2, dy: -5.6, r: 1.3, o: 0.5 },
  { dx: 5.2, dy: 5.6, r: 1.3, o: 0.5 },
  { dx: 7.8, dy: -8.4, r: 1.05, o: 0.3 },
  { dx: 7.8, dy: 8.4, r: 1.05, o: 0.3 },
];

export function VelonetMark({ className, title }: { className?: string; title?: string }) {
  return (
    <svg
      viewBox="0 0 32 24"
      className={cn("size-5", className)}
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      fill="currentColor"
    >
      {DOTS.map((d, i) => (
        <circle key={`l${i}`} cx={4 + d.dx} cy={12 + d.dy} r={d.r} opacity={d.o} />
      ))}
      {DOTS.map((d, i) => (
        <circle key={`r${i}`} cx={28 - d.dx} cy={12 + d.dy} r={d.r} opacity={d.o} />
      ))}
      <circle cx="16" cy="12" r="2.4" />
    </svg>
  );
}

/** The mark in its brand tile, with the product line beside it. */
export function VelonetLockup({
  product,
  collapsed = false,
  inverse = false,
  className,
}: {
  product: string;
  collapsed?: boolean;
  inverse?: boolean;
  className?: string;
}) {
  return (
    <div className={cn("flex min-w-0 items-center gap-2.5", className)}>
      <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground shadow-control">
        <VelonetMark className="size-6" />
      </span>
      {!collapsed && (
        <div className="min-w-0 leading-tight">
          <div className={cn("truncate text-[0.9375rem] font-bold tracking-[-0.01em]", inverse ? "text-white" : "text-foreground")}>
            Velonet
          </div>
          <div className={cn("truncate text-nano uppercase tracking-[0.12em]", inverse ? "text-sidebar-muted" : "text-muted-foreground")}>
            {product}
          </div>
        </div>
      )}
    </div>
  );
}
