"use client";

import * as React from "react";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";
import { HelpCircle } from "lucide-react";
import { cn } from "@/lib/utils";

export const TooltipProvider = TooltipPrimitive.Provider;

/**
 * A tooltip opens on hover, and on focus only when the focus came from the keyboard. A dialog moves focus to its first
 * focusable element when it opens, and in a form whose hints are icons that element is a hint: without this, every such
 * form opened with a hint already showing. Preventing the event's default skips Radix's own focus handler.
 */
let lastInputWasKeyboard = false;
if (typeof window !== "undefined") {
  // Capture phase: recorded before any handler moves focus in response to the same key or click.
  window.addEventListener("keydown", (e) => { if (e.key === "Tab" || e.key.startsWith("Arrow")) lastInputWasKeyboard = true; }, true);
  window.addEventListener("pointerdown", () => { lastInputWasKeyboard = false; }, true);
  window.addEventListener("mousedown", () => { lastInputWasKeyboard = false; }, true);
}

export function openOnlyOnKeyboardFocus(e: React.FocusEvent<HTMLElement>) {
  if (!lastInputWasKeyboard) e.preventDefault();
}

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
    // A local provider lets tooltips work when tests render components without the app layout.
    <TooltipPrimitive.Provider delayDuration={delay}>
      <TooltipPrimitive.Root delayDuration={delay}>
        <TooltipPrimitive.Trigger asChild onFocus={openOnlyOnKeyboardFocus}>{children}</TooltipPrimitive.Trigger>
        <TooltipPrimitive.Portal>
          <TooltipPrimitive.Content
            side={side}
            align={align}
            sideOffset={6}
            collisionPadding={12}
            className={cn(
              "z-[60] max-w-xs rounded-md border border-border bg-popover px-2.5 py-1.5",
              "text-xs leading-relaxed text-popover-foreground shadow-overlay",
              "data-[state=delayed-open]:animate-in data-[state=closed]:animate-out",
              "data-[state=delayed-open]:fade-in-0 data-[state=closed]:fade-out-0",
              "data-[state=delayed-open]:zoom-in-95",
              "motion-reduce:animate-none",
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

export function Explain({ children, className }: { children: React.ReactNode; className?: string }) {
  return (
    // A short wait, so a hint opens when the pointer rests on the icon, not as it passes over.
    <Tooltip content={children} delay={300}>
      <button
        type="button"
        // Avoid announcing visible help text twice.
        aria-label="What this means"
        // Expand the pointer target without shifting adjacent text.
        className={cn(
          "-m-1 inline-flex size-6 shrink-0 items-center justify-center rounded-full align-text-bottom",
          "text-muted-foreground transition-colors hover:text-foreground",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
          className,
        )}
      >
        <HelpCircle className="size-3.5" />
      </button>
    </Tooltip>
  );
}
