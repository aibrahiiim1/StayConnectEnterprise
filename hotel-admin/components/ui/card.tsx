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
      className={cn("rounded-lg border border-border bg-card text-card-foreground shadow-xs", className)}
      {...p}
    />
  );
}

export function CardHeader({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-center justify-between gap-3 border-b border-border px-5 py-3.5",
        className,
      )}
      {...p}
    />
  );
}

export function CardTitle({ className, ...p }: React.HTMLAttributes<HTMLHeadingElement>) {
  return <h2 className={cn("text-sm font-semibold tracking-tight", className)} {...p} />;
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

// Section — a titled block that is NOT a card, for grouping inside one. Pages were hand-rolling their own
// <h2> inside card bodies, at four different sizes.
export function Section({
  title, description, actions, className, children,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}) {
  return (
    <div className={cn("space-y-3", className)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <h3 className="text-sm font-semibold tracking-tight">{title}</h3>
          {description && <p className="max-w-3xl text-sm text-muted-foreground">{description}</p>}
        </div>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
      {children}
    </div>
  );
}
