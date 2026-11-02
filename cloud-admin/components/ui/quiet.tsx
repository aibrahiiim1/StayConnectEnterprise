"use client";

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

export function QuietDescription({ children, className }: { children: React.ReactNode; className?: string }) {
  if (children == null || children === "" || children === false) return null;
  return (
    <span className={cn("inline-flex items-center align-middle", className)}>
      <span className="sr-only">{children}</span>
      <Explain>{children}</Explain>
    </span>
  );
}
