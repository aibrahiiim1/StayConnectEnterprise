"use client";

import { Lightbulb } from "lucide-react";
import { cn } from "@/lib/utils";
import { Sheet, SheetBody, SheetContent, SheetHeader, SheetTrigger } from "@/components/ui/sheet";

// Keep action consequences and disabled-state warnings beside the control, outside optional help.

export function HelpTip({
  title,
  eyebrow = "Tips",
  label,
  children,
  className,
}: {
  title: React.ReactNode;
  eyebrow?: React.ReactNode;
  label?: string;
  children: React.ReactNode;
  className?: string;
}) {
  const aria = label ?? (typeof title === "string" ? `Tips: ${title}` : "Tips");
  return (
    <Sheet>
      <SheetTrigger
        type="button"
        aria-label={aria}
        title={aria}
        className={cn(
          "inline-flex size-7 shrink-0 items-center justify-center rounded-full border border-border bg-card text-muted-foreground",
          "transition-colors hover:border-warning/40 hover:bg-warning-subtle hover:text-warning-subtle-foreground",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
          className,
        )}
      >
        <Lightbulb className="size-4" aria-hidden />
      </SheetTrigger>
      <SheetContent width="md" aria-describedby={undefined}>
        <SheetHeader icon={<Lightbulb />} eyebrow={eyebrow} title={title} />
        <SheetBody>
          <div className="space-y-5 text-sm leading-relaxed text-foreground/90">{children}</div>
        </SheetBody>
      </SheetContent>
    </Sheet>
  );
}

export function HelpSection({ title, children }: { title?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="space-y-1.5">
      {title && <h3 className="text-sm font-semibold text-foreground">{title}</h3>}
      <div className="space-y-2 text-muted-foreground [&_strong]:font-semibold [&_strong]:text-foreground">
        {children}
      </div>
    </section>
  );
}

export function HelpList({ items }: { items: React.ReactNode[] }) {
  return (
    <ul className="list-disc space-y-1 pl-5 marker:text-muted-foreground/60">
      {items.map((it, i) => (
        <li key={i}>{it}</li>
      ))}
    </ul>
  );
}
