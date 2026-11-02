import { cn } from "@/lib/utils";
import { QuietDescription } from "@/components/ui/quiet";

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
        "flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-b border-border px-5 py-3.5",
        className,
      )}
      {...p}
    />
  );
}

export function CardTitle({ className, children, ...p }: React.HTMLAttributes<HTMLHeadingElement>) {
  return <h2 className={cn("inline-block align-middle text-emphasis text-foreground", className)} {...p}>{children}</h2>;
}

/** An info icon beside the card title (its words stay in the page for assistive technology), not a line under it. */
export function CardDescription({ className, children }: React.HTMLAttributes<HTMLParagraphElement>) {
  return <QuietDescription className={cn("ms-1", className)}>{children}</QuietDescription>;
}

export function CardBody({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("px-5 py-4", className)} {...p} />;
}

export function CardFooter({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-2 rounded-b-lg border-t border-border bg-surface/60 px-5 py-3",
        className,
      )}
      {...p}
    />
  );
}

/** A titled group inside a page or card, without its own border. */
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
        <div className="min-w-0 space-y-0.5">
          <div className="flex flex-wrap items-center gap-1">
            <h3 className="text-emphasis">{title}</h3>
            <QuietDescription>{description}</QuietDescription>
          </div>
        </div>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
      {children}
    </div>
  );
}

/**
 * The standard settings/content card: title, optional one-line description, actions on the right, a body and an
 * optional footer. `flush` removes body padding for a table or list that runs edge to edge.
 */
export function SectionCard({
  title, description, actions, footer, flush = false, id, className, bodyClassName, children,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  actions?: React.ReactNode;
  footer?: React.ReactNode;
  flush?: boolean;
  id?: string;
  className?: string;
  bodyClassName?: string;
  children?: React.ReactNode;
}) {
  return (
    <Card id={id} className={cn(flush && "overflow-hidden", className)}>
      <CardHeader>
        <div className="min-w-0">
          <CardTitle>{title}</CardTitle>
          {description && <CardDescription>{description}</CardDescription>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </CardHeader>
      <div className={cn(flush ? "" : "px-5 py-4", bodyClassName)}>{children}</div>
      {footer && <CardFooter className="justify-end">{footer}</CardFooter>}
    </Card>
  );
}
