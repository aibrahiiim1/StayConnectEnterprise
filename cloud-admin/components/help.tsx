"use client";

import { Lightbulb } from "lucide-react";
import { cn } from "@/lib/utils";
import { Sheet, SheetBody, SheetContent, SheetHeader, SheetTrigger } from "@/components/ui/sheet";

/*
  HELP LIVES BEHIND A LIGHTBULB.

  Screens used to carry their explanations as always-visible paragraphs and callouts. The explanations were
  worth keeping; reading them every time was not. A page or a section now shows a small lightbulb beside its
  title, and the explanation opens in a drawer when somebody wants it.

  WHAT MUST NOT MOVE IN HERE: a warning about a consequence (what an action will do, what cannot be undone, why a
  control is disabled right now) stays on the page, next to the thing it is about. This drawer is for "what is
  this and how does it work", never for "be careful".
*/

export function HelpTip({
  title,
  eyebrow = "Tips",
  label,
  children,
  className,
}: {
  /** The drawer title, usually the page or section name. */
  title: React.ReactNode;
  eyebrow?: React.ReactNode;
  /** Accessible name for the button; defaults to "Tips: <title>" when the title is a string. */
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
          "transition-colors hover:border-amber-300 hover:bg-amber-50 hover:text-amber-600",
          "dark:hover:border-amber-400/40 dark:hover:bg-amber-400/10 dark:hover:text-amber-300",
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

/** A titled block inside a help drawer. */
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

/** A tidy bullet list for help content. */
export function HelpList({ items }: { items: React.ReactNode[] }) {
  return (
    <ul className="list-disc space-y-1 pl-5 marker:text-muted-foreground/60">
      {items.map((it, i) => (
        <li key={i}>{it}</li>
      ))}
    </ul>
  );
}
