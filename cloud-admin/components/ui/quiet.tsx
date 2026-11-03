"use client";

import { ChevronRight } from "lucide-react";
import { Explain } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

/**
 * The explanation that used to sit as a line of text under a title. It is one small info icon beside the title now, and
 * its text is read on hover or focus; assistive technology and search still get the words, in place, so nothing is
 * removed from the page, only from its visual noise.
 */
/**
 * A form note that is read on hover or focus of an info icon instead of taking a paragraph. The words stay in the page
 * (assistive technology, search, tests by data-testid). Warnings that need action are not hints: keep those visible.
 */
export function InfoHint({ children, testId, className }: { children: React.ReactNode; testId?: string; className?: string }) {
  if (children == null || children === "" || children === false) return null;
  return (
    <span className={cn("inline-flex items-center align-middle", className)}>
      <span className="sr-only" data-testid={testId}>{children}</span>
      <Explain>{children}</Explain>
    </span>
  );
}

/**
 * Progressive disclosure for secondary settings or long instructions: closed by default, one line when closed. The
 * content stays in the page (a native details element), so a form keeps its fields and their values when it is closed.
 */
export function Disclosure({
  title, defaultOpen = false, children, className, testId,
}: { title: React.ReactNode; defaultOpen?: boolean; children: React.ReactNode; className?: string; testId?: string }) {
  return (
    <details className={cn("group rounded-md border border-border", className)} open={defaultOpen || undefined} data-testid={testId}>
      <summary className="flex cursor-pointer select-none items-center gap-2 rounded-md px-3.5 py-2.5 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" aria-hidden />
        {title}
      </summary>
      <div className="space-y-4 border-t border-border px-3.5 py-3.5">{children}</div>
    </details>
  );
}

export function QuietDescription({ children, className }: { children: React.ReactNode; className?: string }) {
  if (children == null || children === "" || children === false) return null;
  return (
    <span className={cn("inline-flex items-center align-middle", className)}>
      <span className="sr-only">{children}</span>
      <Explain>{children}</Explain>
    </span>
  );
}
