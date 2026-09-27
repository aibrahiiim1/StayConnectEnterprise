import { cn } from "@/lib/utils";

// A card is the one container the whole admin is built from, so its job is to be quiet: one hairline border,
// the faintest elevation, and generous padding. The previous version carried a fake inner highlight
// (`shadow-panel` as two inset white rings) that only read correctly on a near-black page.
//
// The exported names and signatures are unchanged, so every existing screen picks the new treatment up without
// being edited. `CardDescription` and `CardFooter` are additions.
export function Card({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("rounded-lg border border-border bg-card text-card-foreground shadow-card", className)}
      {...p}
    />
  );
}

export function CardHeader({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-center justify-between gap-3 border-b border-border px-5 py-4",
        className,
      )}
      {...p}
    />
  );
}

export function CardTitle({ className, ...p }: React.HTMLAttributes<HTMLHeadingElement>) {
  return <h2 className={cn("text-emphasis tracking-tight", className)} {...p} />;
}

export function CardDescription({ className, ...p }: React.HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn("text-sm text-muted-foreground", className)} {...p} />;
}

export function CardBody({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("px-5 py-4", className)} {...p} />;
}

export function CardFooter({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("flex flex-wrap items-center gap-2 border-t border-border bg-surface/50 px-5 py-3", className)}
      {...p}
    />
  );
}
