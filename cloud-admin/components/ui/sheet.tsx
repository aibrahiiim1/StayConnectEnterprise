"use client";

// THE SIDE SHEET.
//
// A dialog is right for a decision ("disable this package?"). It is wrong for a record: a voucher, a PMS
// connection, a package's activity. Those want the list to stay visible behind them, so the operator can move
// from one row to the next without losing their place — and they want the full viewport height, because a PMS
// connection's diagnostics do not fit in a centred box.
//
// Built on the same Radix Dialog as `dialog.tsx`, so focus trapping, Escape, restoring focus to the row that
// opened it and the inert background are correct rather than approximated. Below `sm` it becomes a full-width
// panel, since a 420px drawer on a 390px phone is a clipped page.

import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { cn } from "@/lib/utils";

export const Sheet = DialogPrimitive.Root;
export const SheetTrigger = DialogPrimitive.Trigger;
export const SheetClose = DialogPrimitive.Close;

const WIDTHS = {
  sm: "sm:max-w-md",
  md: "sm:max-w-xl",
  lg: "sm:max-w-3xl",
  xl: "sm:max-w-5xl",
} as const;

export function SheetContent({
  width = "md",
  className,
  children,
  ...props
}: React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content> & { width?: keyof typeof WIDTHS }) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay
        className={cn(
          "fixed inset-0 z-50 bg-foreground/35 backdrop-blur-[1px]",
          "data-[state=open]:animate-in data-[state=closed]:animate-out",
          "data-[state=open]:fade-in-0 data-[state=closed]:fade-out-0",
        )}
      />
      <DialogPrimitive.Content
        className={cn(
          "fixed inset-y-0 end-0 z-50 flex h-full w-full flex-col border-s border-border bg-card text-card-foreground shadow-lg",
          "data-[state=open]:animate-in data-[state=closed]:animate-out",
          "data-[state=open]:slide-in-from-right data-[state=closed]:slide-out-to-right",
          "data-[state=open]:duration-200 data-[state=closed]:duration-150",
          WIDTHS[width],
          className,
        )}
        {...props}
      >
        {children}
        <DialogPrimitive.Close
          className={cn(
            "absolute end-3.5 top-3.5 rounded-md p-1 text-muted-foreground",
            "transition-colors hover:bg-surface hover:text-foreground",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
          )}
        >
          <X className="size-4" />
          <span className="sr-only">Close</span>
        </DialogPrimitive.Close>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}

/** The fixed top of the sheet: an optional icon/eyebrow, the title, a description and status badges. */
export function SheetHeader({
  eyebrow,
  title,
  description,
  badges,
  icon,
  className,
  children,
}: {
  eyebrow?: React.ReactNode;
  title: React.ReactNode;
  description?: React.ReactNode;
  badges?: React.ReactNode;
  icon?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}) {
  return (
    <div className={cn("shrink-0 space-y-3 border-b border-border px-5 py-4 pe-12", className)}>
      <div className="flex items-start gap-3">
        {icon && (
          <span
            className="inline-flex size-9 shrink-0 items-center justify-center rounded-md bg-primary-subtle text-primary-subtle-foreground [&_svg]:size-4"
            aria-hidden
          >
            {icon}
          </span>
        )}
        <div className="min-w-0 space-y-1">
          {eyebrow && (
            <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">{eyebrow}</div>
          )}
          <DialogPrimitive.Title className="text-base font-semibold tracking-tight">{title}</DialogPrimitive.Title>
          {description ? (
            <DialogPrimitive.Description className="text-sm text-muted-foreground">
              {description}
            </DialogPrimitive.Description>
          ) : (
            // Radix warns when a Content has no Description; an empty one satisfies it without adding text.
            <DialogPrimitive.Description className="sr-only">{typeof title === "string" ? title : ""}</DialogPrimitive.Description>
          )}
          {badges && <div className="flex flex-wrap items-center gap-1.5 pt-1">{badges}</div>}
        </div>
      </div>
      {children}
    </div>
  );
}

/** The scrolling middle. */
export function SheetBody({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("min-h-0 flex-1 space-y-5 overflow-y-auto px-5 py-5", className)} {...p} />;
}

/** The fixed bottom, for the record's actions. */
export function SheetFooter({ className, ...p }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex shrink-0 flex-wrap items-center justify-end gap-2 border-t border-border bg-surface/40 px-5 py-3",
        className,
      )}
      {...p}
    />
  );
}

/** A titled group inside a sheet body. */
export function SheetSection({
  title,
  description,
  actions,
  className,
  children,
}: {
  title: React.ReactNode;
  description?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
  children?: React.ReactNode;
}) {
  return (
    <section className={cn("space-y-2.5", className)}>
      <div className="flex items-start justify-between gap-3">
        <div className="space-y-0.5">
          <h3 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">{title}</h3>
          {description && <p className="text-sm text-muted-foreground">{description}</p>}
        </div>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
      {children}
    </section>
  );
}
