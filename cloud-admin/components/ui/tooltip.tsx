"use client";

import * as React from "react";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import { cn } from "@/lib/utils";

// THE PRODUCT IS FULL OF TERMS THAT NEED A SENTENCE.
//
// "Cloud sync outbox · 69,572 pending · 8,424 dead" was on the dashboard with nothing to say what an outbox is,
// what pending means, or whether 8,424 dead is a catastrophe. The `title` attribute was the only explanation
// mechanism available, and it is invisible on touch, unreadable by keyboard and unstyled.
//
// Radix Tooltip gives hover, focus AND keyboard access with the right ARIA wiring.

export const TooltipProvider = TooltipPrimitive.Provider;

export function Tooltip({
  content,
  side = "top",
  align = "center",
  delay = 150,
  className,
  children,
}: {
  content: React.ReactNode;
  side?: "top" | "right" | "bottom" | "left";
  align?: "start" | "center" | "end";
  delay?: number;
  className?: string;
  children: React.ReactNode;
}) {
  if (content == null || content === "") return <>{children}</>;
  return (
    // SELF-PROVIDING, so a tooltip cannot depend on a provider somebody remembered to add further up.
    //
    // Radix throws "`Tooltip` must be used within `TooltipProvider`" when one is missing, and that is not a
    // theoretical risk: the root layout has one, but every component test renders a page WITHOUT the layout, so
    // the first screen that gained a tooltip took its whole test file down with an uncaught exception. A
    // component that only works inside an ancestor it does not name is a component that will be used wrongly.
    //
    // Nesting providers is supported — the innermost wins — so the root provider still sets the shared
    // skip-delay for rapid hovering across several tooltips, and this one guarantees correctness in isolation.
    <TooltipPrimitive.Provider delayDuration={delay}>
      <TooltipPrimitive.Root delayDuration={delay}>
        <TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger>
        <TooltipPrimitive.Portal>
          <TooltipPrimitive.Content
            side={side}
            align={align}
            sideOffset={6}
            collisionPadding={12}
            className={cn(
              "z-[60] max-w-xs rounded-md border border-border bg-popover px-3 py-2",
              "text-xs leading-relaxed text-popover-foreground shadow-md",
              "data-[state=delayed-open]:animate-in data-[state=closed]:animate-out",
              "data-[state=delayed-open]:fade-in-0 data-[state=closed]:fade-out-0",
              "data-[state=delayed-open]:zoom-in-95 motion-reduce:animate-none",
              className,
            )}
          >
            {content}
            <TooltipPrimitive.Arrow className="fill-popover stroke-border" width={11} height={5} />
          </TooltipPrimitive.Content>
        </TooltipPrimitive.Portal>
      </TooltipPrimitive.Root>
    </TooltipPrimitive.Provider>
  );
}
